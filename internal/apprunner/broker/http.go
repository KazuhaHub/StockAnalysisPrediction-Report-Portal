// Package broker contains bounded runner-side capability executors. Construction
// is not authorization: callers must supply current authoritative grant checks.
package broker

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
)

var (
	ErrDenied      = errors.New("broker request denied")
	ErrUnavailable = errors.New("broker unavailable")
	// ErrUncertain means dispatch was attempted; a remote effect may have happened.
	// Callers must not automatically replay it, even when no response is returned.
	ErrUncertain = errors.New("broker outcome uncertain")
)

// HTTPPolicy is an internal development policy, not a public wire format or a
// stored grant. Exactly one public HTTPS origin is supported by this adapter.
// Private-service rules and credential injection need separate authority adapters.
type HTTPPolicy struct {
	Origin        string
	Paths         []string
	Methods       []string
	RequestBytes  int64
	ResponseBytes int64
	Deadline      time.Duration
	Concurrency   int
}

type HTTPRequest struct {
	Method      string
	URL         string
	Accept      string
	ContentType string
	Body        []byte
}

// HTTPResponse deliberately excludes arbitrary upstream headers, cookies and URLs.
type HTTPResponse struct {
	Status      int
	ContentType string
	Body        []byte
}

type HTTP struct {
	policy    HTTPPolicy
	origin    *url.URL
	authorize func(context.Context) error
	slots     chan struct{}
	lookup    func(context.Context, string) ([]netip.Addr, error)
	dial      func(context.Context, string, string) (net.Conn, error)
	tlsConfig *tls.Config
}

// NewHTTP snapshots a host-selected policy. authorize must check the current
// installation/generation/epoch, grant revision and actor/resource authority
// against that same policy; a successful callback is never cached. One executor
// must be shared by all calls using its concurrency allocation. No production
// route constructs this executor until grant review and dispatch wiring exist.
func NewHTTP(p HTTPPolicy, authorize func(context.Context) error) (*HTTP, error) {
	u, err := url.Parse(p.Origin)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || !validHost(u.Hostname()) || u.Hostname() != strings.ToLower(u.Hostname()) || strings.Contains(p.Origin, "#") || p.Origin != "https://"+u.Host {
		return nil, ErrDenied
	}
	if port := u.Port(); port != "" {
		n, e := strconv.Atoi(port)
		if e != nil || n < 1 || n > 65535 || strconv.Itoa(n) != port || n == 443 {
			return nil, ErrDenied
		}
	} else if u.Host != u.Hostname() {
		return nil, ErrDenied
	}
	if authorize == nil || p.RequestBytes < 1 || p.RequestBytes > 65536 || p.ResponseBytes < 1 || p.ResponseBytes > 1048576 || p.Deadline <= 0 || p.Deadline > 15*time.Second || p.Concurrency < 1 || p.Concurrency > 4 || len(p.Paths) < 1 || len(p.Paths) > 16 || len(p.Methods) < 1 || len(p.Methods) > 7 {
		return nil, ErrDenied
	}
	for i, path := range p.Paths {
		if !validPath(path) || (len(path) > 1 && strings.HasSuffix(path, "/")) || slices.Contains(p.Paths[:i], path) {
			return nil, ErrDenied
		}
	}
	for i, method := range p.Methods {
		if !slices.Contains([]string{"GET", "HEAD", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"}, method) || slices.Contains(p.Methods[:i], method) {
			return nil, ErrDenied
		}
	}
	p.Paths = slices.Clone(p.Paths)
	p.Methods = slices.Clone(p.Methods)
	d := &net.Dialer{Timeout: 5 * time.Second}
	return &HTTP{policy: p, origin: u, authorize: authorize, slots: make(chan struct{}, p.Concurrency), lookup: func(ctx context.Context, host string) ([]netip.Addr, error) {
		return net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	}, dial: d.DialContext}, nil
}

func (h *HTTP) Do(parent context.Context, input HTTPRequest) (HTTPResponse, error) {
	var zero HTTPResponse
	if h == nil || h.authorize == nil || h.origin == nil || parent == nil {
		return zero, ErrDenied
	}
	if parent.Err() != nil {
		return zero, ErrUnavailable
	}
	// Validate before allocating a slot or invoking any external work.
	if int64(len(input.Body)) > h.policy.RequestBytes || !slices.Contains(h.policy.Methods, input.Method) || !validHeader(input.Accept) || !validHeader(input.ContentType) {
		return zero, ErrDenied
	}
	u, err := url.Parse(input.URL)
	if err != nil || len(input.URL) > 4096 || u.Scheme != h.origin.Scheme || u.Host != h.origin.Host || u.User != nil || u.Fragment != "" || strings.Contains(input.URL, "#") || u.Opaque != "" || u.RawPath != "" || !validPath(u.Path) || !h.pathAllowed(u.Path) {
		return zero, ErrDenied
	}
	ctx, cancel := context.WithTimeout(parent, h.policy.Deadline)
	defer cancel()
	select {
	case h.slots <- struct{}{}:
		defer func() { <-h.slots }()
	default:
		return zero, ErrUnavailable
	}
	input.Body = bytes.Clone(input.Body)
	if h.authorize(ctx) != nil {
		return zero, ErrDenied
	}
	addresses, err := h.lookup(ctx, h.origin.Hostname())
	if err != nil || ctx.Err() != nil {
		return zero, ErrUnavailable
	}
	if len(addresses) == 0 || len(addresses) > 16 {
		return zero, ErrDenied
	}
	for _, ip := range addresses {
		if !publicAddress(ip) {
			return zero, ErrDenied
		}
	}
	if h.authorize(ctx) != nil {
		return zero, ErrDenied
	}
	if ctx.Err() != nil {
		return zero, ErrUnavailable
	}
	port := h.origin.Port()
	if port == "" {
		port = "443"
	}
	pinned := net.JoinHostPort(addresses[0].Unmap().String(), port)
	config := &tls.Config{MinVersion: tls.VersionTLS12, ServerName: h.origin.Hostname()}
	if h.tlsConfig != nil {
		config = h.tlsConfig.Clone()
	}
	transport := &http.Transport{Proxy: nil, DisableKeepAlives: true, DisableCompression: true, MaxResponseHeaderBytes: 8192, TLSHandshakeTimeout: 5 * time.Second, ResponseHeaderTimeout: h.policy.Deadline, TLSClientConfig: config, DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		// No second DNS resolution, environment proxy or caller-selected dial target.
		if address != net.JoinHostPort(h.origin.Hostname(), port) {
			return nil, ErrDenied
		}
		return h.dial(ctx, "tcp", pinned)
	}}
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
	response, err := client.Do(req)
	if err != nil {
		return zero, ErrUncertain
	}
	defer response.Body.Close()
	// No response or raw diagnostic is released on an uncertain/revoked outcome.
	encodings := response.Header.Values("Content-Encoding")
	if response.StatusCode < 200 || response.StatusCode >= 300 && response.StatusCode < 400 || response.StatusCode > 599 || response.ContentLength > h.policy.ResponseBytes || len(encodings) > 1 || len(encodings) == 1 && encodings[0] != "identity" {
		return zero, ErrUncertain
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, h.policy.ResponseBytes+1))
	if err != nil || int64(len(body)) > h.policy.ResponseBytes || ctx.Err() != nil {
		return zero, ErrUncertain
	}
	contentType := response.Header.Get("Content-Type")
	if !validHeader(contentType) || len(response.Header.Values("Content-Type")) > 1 {
		return zero, ErrUncertain
	}
	if h.authorize(ctx) != nil || ctx.Err() != nil {
		return zero, ErrUncertain
	}
	return HTTPResponse{Status: response.StatusCode, ContentType: contentType, Body: body}, nil
}

func (h *HTTP) pathAllowed(path string) bool {
	for _, prefix := range h.policy.Paths {
		if prefix == "/" || path == prefix || strings.HasPrefix(path, prefix+"/") {
			return true
		}
	}
	return false
}

func validHeader(s string) bool {
	if len(s) > 256 {
		return false
	}
	for _, b := range []byte(s) {
		if b < 32 || b > 126 {
			return false
		}
	}
	return true
}
func validHost(host string) bool {
	if len(host) < 3 || len(host) > 253 || !strings.Contains(host, ".") || net.ParseIP(host) != nil {
		return false
	}
	labels := strings.Split(host, ".")
	for _, label := range labels {
		if len(label) < 1 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, b := range []byte(label) {
			if !(b >= 'a' && b <= 'z' || b >= '0' && b <= '9' || b == '-') {
				return false
			}
		}
	}
	for _, b := range []byte(labels[len(labels)-1]) {
		if b >= 'a' && b <= 'z' {
			return true
		}
	}
	return false
}
func validPath(path string) bool {
	if len(path) < 1 || len(path) > 1024 || path[0] != '/' || strings.Contains(path, "//") {
		return false
	}
	for _, segment := range strings.Split(path, "/") {
		if segment == "." || segment == ".." {
			return false
		}
		for _, b := range []byte(segment) {
			if !(b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9' || strings.ContainsRune("._~-", rune(b))) {
				return false
			}
		}
	}
	return true
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
