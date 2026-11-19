// Package broker contains bounded runner-side capability executors. Construction
// is not authorization: callers must supply current authoritative grant checks.
package broker

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
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
	set       appplatform.HTTPPolicyV2
	raw       []byte
	networks  []appplatform.PrivateNetwork
	authorize func(context.Context) error
	slots     chan struct{}
	lookup    func(context.Context, string) ([]netip.Addr, error)
	dial      func(context.Context, string, string) (net.Conn, error)
	tlsConfig *tls.Config
	now       func() time.Time
	// interfaceAddrs lists the addresses assigned to this host's interfaces. A
	// private target may never reach one, even inside an allowlisted prefix.
	interfaceAddrs func() ([]netip.Addr, error)
	hostMu         sync.Mutex
	hostAt         time.Time
	hostAddrs      []netip.Addr
	hostValid      bool
}

// hostAddressTTL bounds how stale the interface snapshot of a private call is.
const hostAddressTTL = 5 * time.Second

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
	return &HTTP{set: set, raw: raw, networks: networks, authorize: authorize, slots: make(chan struct{}, set.Concurrency), now: time.Now, lookup: func(ctx context.Context, host string) ([]netip.Addr, error) {
		return net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	}, dial: d.DialContext, interfaceAddrs: systemInterfaceAddrs}, nil
}

func systemInterfaceAddrs() ([]netip.Addr, error) {
	all, err := net.InterfaceAddrs()
	if err != nil {
		return nil, err
	}
	out := make([]netip.Addr, 0, len(all))
	for _, a := range all {
		if p, err := netip.ParsePrefix(a.String()); err == nil {
			out = append(out, appplatform.Unmap(p.Addr()))
		}
	}
	return out, nil
}

// hostAddresses is the interface snapshot, reused for hostAddressTTL.
func (h *HTTP) hostAddresses() ([]netip.Addr, bool) {
	h.hostMu.Lock()
	defer h.hostMu.Unlock()
	if h.hostValid && h.now().Sub(h.hostAt) < hostAddressTTL {
		return h.hostAddrs, true
	}
	if h.interfaceAddrs == nil {
		return nil, false
	}
	addrs, err := h.interfaceAddrs()
	if err != nil {
		h.hostValid = false
		return nil, false
	}
	h.hostAddrs, h.hostAt, h.hostValid = addrs, h.now(), true
	return addrs, true
}

// addressesAllowed applies the target's class to every address. Public targets
// keep the public-only rule. Private targets admit only addresses the runner's
// allowlist admits for the port and scheme, never an always-denied address and
// never one of this host's own; a public address is in no allowlist, so a
// mixed answer is denied as a whole.
func (h *HTTP) addressesAllowed(target appplatform.EffectiveHTTPTarget, addresses []netip.Addr) bool {
	if !target.Private {
		for _, ip := range addresses {
			if !publicAddress(ip) {
				return false
			}
		}
		return true
	}
	host, ok := h.hostAddresses()
	if !ok {
		return false
	}
	for _, ip := range addresses {
		ip = appplatform.Unmap(ip)
		if appplatform.AlwaysDenied(ip) || !appplatform.Admit(h.networks, ip, target.Port, target.Scheme) || slices.Contains(host, ip) {
			return false
		}
	}
	return true
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
	// A credential is never sent over plain HTTP.
	if target.Scheme != "https" && input.HeaderName != "" {
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
	if credentialResponseEcho(body, input.HeaderName, input.HeaderValue) {
		clear(body)
		return zero, ErrUncertain
	}
	contentType := response.Header.Get("Content-Type")
	if !appplatform.ValidHTTPPolicyHeaderValue(contentType) || len(response.Header.Values("Content-Type")) > 1 {
		return zero, ErrUncertain
	}
	if credentialResponseEcho([]byte(contentType), input.HeaderName, input.HeaderValue) {
		return zero, ErrUncertain
	}
	if authorize(ctx) != nil || ctx.Err() != nil {
		return zero, ErrUncertain
	}
	return HTTPResponse{Status: response.StatusCode, ContentType: contentType, Body: body, RetryAfterSeconds: parseRetryAfter(response.StatusCode, response.Header.Values("Retry-After"))}, nil
}

func credentialResponseEcho(body []byte, header string, value []byte) bool {
	if len(value) == 0 {
		return false
	}
	if bytes.Contains(body, value) {
		return true
	}
	if header == "Authorization" && bytes.HasPrefix(value, []byte("Bearer ")) && len(value) > len("Bearer ") {
		return bytes.Contains(body, value[len("Bearer "):])
	}
	return false
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
