// Package broker contains bounded runner-side capability executors. Construction
// is not authorization: callers must supply current authoritative grant checks.
package broker

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"slices"
	"strconv"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/hostnet"
)

var (
	ErrDenied      = errors.New("broker request denied")
	ErrUnavailable = errors.New("broker unavailable")
	// ErrUncertain means dispatch was attempted; a remote effect may have happened.
	// Callers must not automatically replay it, even when no response is returned.
	ErrUncertain = errors.New("broker outcome uncertain")
	// ErrBusy is ErrUnavailable because a concurrency or rate budget is spent, not
	// because anything is wrong; the caller may retry shortly. It wraps
	// ErrUnavailable so every existing errors.Is(err, ErrUnavailable) holds.
	ErrBusy = fmt.Errorf("%w: budget exhausted", ErrUnavailable)
)

// HTTPPolicy is an internal development policy, not a public wire format or a
// stored grant. Exactly one public HTTPS origin is supported by this adapter.
// Private-service rules and credential injection need separate authority adapters.
type HTTPPolicy = appplatform.HTTPPolicy

type HTTPRequest struct {
	Method      string
	URL         string
	Accept      string
	ContentType string
	Body        []byte
	HeaderName  string
	HeaderValue []byte
	// SignedHeaders are the headers Portal computed for a provider that signs each request (doc
	// 177), mutually exclusive with HeaderName. The executor sets exactly these (and Accept,
	// Accept-Encoding and User-Agent); it checks that they are the closed set for this very
	// request, so a request it could alter is refused rather than sent.
	SignedHeaders []appplatform.SignedHeader
}

// signedRequestOK reports whether the signed headers are exactly what Portal produces for this
// request: no credential header beside them, a body whose digest is the signed
// x-acs-content-sha256, and the closed header set of package appplatform.
func signedRequestOK(input HTTPRequest) bool {
	if len(input.SignedHeaders) == 0 {
		return true
	}
	if input.HeaderName != "" || len(input.HeaderValue) != 0 {
		return false
	}
	sum := sha256.Sum256(input.Body)
	return appplatform.CheckACS3SignedHeaders(input.Method, input.URL, input.ContentType, hex.EncodeToString(sum[:]), input.SignedHeaders) == nil
}

// HTTPResponse deliberately excludes arbitrary upstream headers, cookies and URLs.
type HTTPResponse struct {
	Status      int
	ContentType string
	Body        []byte
	// RetryAfterSeconds is the single integer Retry-After of a 429 or 503
	// response, 0..MaxRetryAfterSeconds, or -1 when absent or not acceptable.
	// Responses built outside Do must set -1 explicitly; the zero value means
	// "retry immediately".
	RetryAfterSeconds int
}

// MaxRetryAfterSeconds bounds a released Retry-After (one day).
const MaxRetryAfterSeconds = 86400

// parseRetryAfter releases a Retry-After only for 429 and 503, only as exactly
// one header holding one unsigned decimal integer (no HTTP-date, sign, space or
// list). Anything else is dropped, never turned into an error: the response
// body and status were already accepted.
func parseRetryAfter(status int, values []string) int {
	if status != http.StatusTooManyRequests && status != http.StatusServiceUnavailable || len(values) != 1 {
		return -1
	}
	v := values[0]
	if v == "" || len(v) > 6 {
		return -1
	}
	n := 0
	for i := 0; i < len(v); i++ {
		if v[i] < '0' || v[i] > '9' {
			return -1
		}
		n = n*10 + int(v[i]-'0')
	}
	if n > MaxRetryAfterSeconds {
		return -1
	}
	return n
}

type HTTP struct {
	// policy is the v1 policy this executor was built from; it is the zero value
	// for an executor built from raw (v1 or v2) bytes.
	policy HTTPPolicy
	// set is the parsed policy and raw the exact bytes it came from; a signed
	// command must carry these bytes to use this executor.
	set appplatform.HTTPPolicyV2
	raw []byte
	addressGuard
	authorize func(context.Context) error
	slots     chan struct{}
	lookup    func(context.Context, string) ([]netip.Addr, error)
	dial      func(context.Context, string, string) (net.Conn, error)
	tlsConfig *tls.Config
}

// NewHTTP snapshots a host-selected v1 policy. authorize must check the current
// installation/generation/epoch, grant revision and actor/resource authority
// against that same policy; a successful callback is never cached. One executor
// must be shared by all calls using its concurrency allocation. No production
// route constructs this executor until grant review and dispatch wiring exist.
func NewHTTP(p HTTPPolicy, authorize func(context.Context) error) (*HTTP, error) {
	p, err := appplatform.NormalizeHTTPPolicy(p)
	if err != nil || authorize == nil {
		return nil, ErrDenied
	}
	raw, err := appplatform.CanonicalHTTPPolicy(p)
	if err != nil {
		return nil, ErrDenied
	}
	h, err := newHTTP(appplatform.HTTPPolicyFromV1(p), raw, nil, authorize)
	if err != nil {
		return nil, err
	}
	h.policy = p
	return h, nil
}

// NewHTTPPolicy builds an executor from exact canonical v1 or v2 policy bytes.
// A policy with a private target needs the runner's private-network allowlist;
// an empty allowlist can never serve one.
func NewHTTPPolicy(raw []byte, networks []appplatform.PrivateNetwork, authorize func(context.Context) error) (*HTTP, error) {
	parsed, err := appplatform.ParseHTTPPolicyAny(raw)
	if err != nil || authorize == nil {
		return nil, ErrDenied
	}
	return newHTTP(parsed.Policy, bytes.Clone(raw), networks, authorize)
}

func newHTTP(set appplatform.HTTPPolicyV2, raw []byte, networks []appplatform.PrivateNetwork, authorize func(context.Context) error) (*HTTP, error) {
	if appplatform.ValidatePrivateNetworks(networks) != nil || set.HasPrivate() && len(networks) == 0 {
		return nil, ErrDenied
	}
	networks = slices.Clone(networks)
	for i := range networks {
		networks[i].Ports = slices.Clone(networks[i].Ports)
	}
	d := &net.Dialer{Timeout: 5 * time.Second}
	return &HTTP{set: set, raw: raw, addressGuard: addressGuard{networks: networks, now: time.Now, interfaces: hostnet.List}, authorize: authorize, slots: make(chan struct{}, set.Concurrency), lookup: func(ctx context.Context, host string) ([]netip.Addr, error) {
		return net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	}, dial: d.DialContext}, nil
}

// allowlistMatches reports whether a command's private-network digest is the one
// this executor serves: required exactly when its policy has a private target,
// and then equal to the digest of the allowlist the executor was built with.
func (h *HTTP) allowlistMatches(digest string) bool {
	if !h.set.HasPrivate() {
		return digest == ""
	}
	sum, err := appplatform.PrivateNetworksSHA256(h.networks)
	return err == nil && digest == sum
}

// addressesAllowed applies the selected target's class to every address.
func (h *HTTP) addressesAllowed(target appplatform.EffectiveHTTPTarget, addresses []netip.Addr) bool {
	return h.addressGuard.addressesAllowed(target.Private, target.Port, target.Scheme, addresses)
}

func (h *HTTP) Do(parent context.Context, input HTTPRequest) (HTTPResponse, error) {
	return h.do(parent, input, nil)
}

func (h *HTTP) do(parent context.Context, input HTTPRequest, extra func(context.Context) error) (HTTPResponse, error) {
	var zero HTTPResponse
	if h == nil || h.authorize == nil || len(h.set.Targets) == 0 || parent == nil {
		return zero, ErrDenied
	}
	if parent.Err() != nil {
		return zero, ErrUnavailable
	}
	// Select the target before allocating a slot or invoking any external work.
	target, err := h.set.Select(input.Method, input.URL)
	if err != nil {
		return zero, ErrDenied
	}
	if int64(len(input.Body)) > target.RequestBytes || !appplatform.ValidHTTPPolicyHeaderValue(input.Accept) || !appplatform.ValidHTTPPolicyHeaderValue(input.ContentType) {
		return zero, ErrDenied
	}
	if len(input.HeaderValue) > 0 && (input.HeaderName != "Authorization" || len(input.HeaderValue) > 16400) {
		if input.HeaderName != "X-API-Key" || len(input.HeaderValue) > 16384 {
			return zero, ErrDenied
		}
	}
	if len(input.HeaderValue) == 0 && input.HeaderName != "" {
		return zero, ErrDenied
	}
	// The target was selected above. A signed request must carry exactly the reviewed signed headers, and a
	// credential or signature is never sent over plain HTTP.
	if !signedRequestOK(input) || target.Scheme != "https" && (input.HeaderName != "" || len(input.SignedHeaders) > 0) {
		return zero, ErrDenied
	}
	ctx, cancel := context.WithTimeout(parent, target.Deadline)
	defer cancel()
	select {
	case h.slots <- struct{}{}:
		defer func() { <-h.slots }()
	default:
		return zero, ErrUnavailable
	}
	input.Body = bytes.Clone(input.Body)
	authorize := func(ctx context.Context) error {
		if err := h.authorize(ctx); err != nil {
			return err
		}
		if extra != nil {
			return extra(ctx)
		}
		return nil
	}
	if authorize(ctx) != nil {
		return zero, ErrDenied
	}
	// An IP literal needs no lookup: the reviewed origin already names the address.
	addresses := []netip.Addr{target.Literal}
	if !target.Literal.IsValid() {
		addresses, err = h.lookup(ctx, target.Host)
		if err != nil || ctx.Err() != nil {
			return zero, ErrUnavailable
		}
	}
	if len(addresses) == 0 || len(addresses) > 16 {
		return zero, ErrDenied
	}
	if !h.addressesAllowed(target, addresses) {
		return zero, ErrDenied
	}
	if authorize(ctx) != nil {
		return zero, ErrDenied
	}
	if ctx.Err() != nil {
		return zero, ErrUnavailable
	}
	port := strconv.Itoa(int(target.Port))
	pinned := net.JoinHostPort(addresses[0].Unmap().String(), port)
	transport := &http.Transport{Proxy: nil, DisableKeepAlives: true, DisableCompression: true, MaxResponseHeaderBytes: 8192, ResponseHeaderTimeout: target.Deadline, DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		// No second DNS resolution, environment proxy or caller-selected dial target.
		if address != net.JoinHostPort(target.Host, port) {
			return nil, ErrDenied
		}
		return h.dial(ctx, "tcp", pinned)
	}}
	if target.Scheme == "https" {
		config := &tls.Config{MinVersion: tls.VersionTLS12, ServerName: target.Host}
		if h.tlsConfig != nil {
			config = h.tlsConfig.Clone()
			if config.ServerName == "" {
				config.ServerName = target.Host
			}
		}
		transport.TLSClientConfig = config
		transport.TLSHandshakeTimeout = 5 * time.Second
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	req, err := http.NewRequestWithContext(ctx, input.Method, input.URL, bytes.NewReader(input.Body))
	if err != nil {
		return zero, ErrDenied
	}
	req.Header.Set("Accept-Encoding", "identity")
	req.Header.Set("User-Agent", "Portal-App-Broker/2-dev")
	if input.Accept != "" {
		req.Header.Set("Accept", input.Accept)
	}
	if input.ContentType != "" {
		req.Header.Set("Content-Type", input.ContentType)
	}
	if input.HeaderName != "" {
		req.Header.Set(input.HeaderName, string(input.HeaderValue))
	}
	for _, signed := range input.SignedHeaders {
		// host is carried by the URL, which the check above tied to the signed value.
		if signed.Name != "host" {
			req.Header.Set(signed.Name, string(signed.Value))
		}
	}
	response, err := client.Do(req)
	if err != nil {
		return zero, ErrUncertain
	}
	defer response.Body.Close()
	// No response or raw diagnostic is released on an uncertain/revoked outcome.
	encodings := response.Header.Values("Content-Encoding")
	if response.StatusCode < 200 || response.StatusCode >= 300 && response.StatusCode < 400 || response.StatusCode > 599 || response.ContentLength > target.ResponseBytes || len(encodings) > 1 || len(encodings) == 1 && encodings[0] != "identity" {
		return zero, ErrUncertain
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, target.ResponseBytes+1))
	if err != nil || int64(len(body)) > target.ResponseBytes || ctx.Err() != nil {
		return zero, ErrUncertain
	}
	echo := newRequestEchoMatcher(input)
	defer echo.Clear()
	if echo.Match(body) {
		clear(body)
		return zero, ErrUncertain
	}
	contentType := response.Header.Get("Content-Type")
	if !appplatform.ValidHTTPPolicyHeaderValue(contentType) || len(response.Header.Values("Content-Type")) > 1 {
		return zero, ErrUncertain
	}
	if echo.Match([]byte(contentType)) {
		return zero, ErrUncertain
	}
	if authorize(ctx) != nil || ctx.Err() != nil {
		return zero, ErrUncertain
	}
	return HTTPResponse{Status: response.StatusCode, ContentType: contentType, Body: body, RetryAfterSeconds: parseRetryAfter(response.StatusCode, response.Header.Values("Retry-After"))}, nil
}

var deniedPrefixes = func() []netip.Prefix {
	raw := []string{"0.0.0.0/8", "10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8", "169.254.0.0/16", "172.16.0.0/12", "192.0.0.0/24", "192.0.2.0/24", "192.88.99.0/24", "192.168.0.0/16", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "224.0.0.0/4", "240.0.0.0/4", "2001::/23", "2001:db8::/32", "2002::/16", "3fff::/20"}
	out := make([]netip.Prefix, 0, len(raw))
	for _, s := range raw {
		out = append(out, netip.MustParsePrefix(s))
	}
	return out
}()

func publicAddress(ip netip.Addr) bool {
	if !ip.IsValid() || ip.Zone() != "" {
		return false
	}
	ip = ip.Unmap()
	if !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
		return false
	}
	if ip.Is6() && !netip.MustParsePrefix("2000::/3").Contains(ip) {
		return false
	}
	for _, prefix := range deniedPrefixes {
		if prefix.Contains(ip) {
			return false
		}
	}
	return true
}
