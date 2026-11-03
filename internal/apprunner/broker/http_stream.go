package broker

import (
	"bytes"
	"context"
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

type HTTPStreamPolicy = appplatform.HTTPStreamPolicy

type HTTPStreamCallbacks struct {
	Start func(status int, contentType string) error
	Chunk func([]byte) error
}

type HTTPStreamResult struct {
	Status      int
	ContentType string
	Bytes       int64
	Frames      int
}

// HTTPStream is a separately constructed ordinary-HTTPS response stream. It is
// not a WebSocket, CONNECT, HTTP upgrade or generic tunnel.
type HTTPStream struct {
	policy    HTTPStreamPolicy
	origin    *url.URL
	authorize func(context.Context) error
	slots     chan struct{}
	lookup    func(context.Context, string) ([]netip.Addr, error)
	dial      func(context.Context, string, string) (net.Conn, error)
	tlsConfig *tls.Config
}

func NewHTTPStream(p HTTPStreamPolicy, authorize func(context.Context) error) (*HTTPStream, error) {
	p, err := appplatform.NormalizeHTTPStreamPolicy(p)
	if err != nil || authorize == nil {
		return nil, ErrDenied
	}
	u, err := url.Parse(p.Origin)
	if err != nil {
		return nil, ErrDenied
	}
	d := &net.Dialer{Timeout: 5 * time.Second}
	return &HTTPStream{
		policy:    p,
		origin:    u,
		authorize: authorize,
		slots:     make(chan struct{}, p.Concurrency),
		lookup: func(ctx context.Context, host string) ([]netip.Addr, error) {
			return net.DefaultResolver.LookupNetIP(ctx, "ip", host)
		},
		dial: d.DialContext,
	}, nil
}

// Do emits response bytes only after the exact request and current authority
// pass the pre-dispatch checks. Every frame is preceded by another authority
// check; a denied later check returns uncertain because the remote effect or
// earlier frames may already exist.
func (h *HTTPStream) Do(parent context.Context, input HTTPRequest, extra func(context.Context) error, callbacks HTTPStreamCallbacks) (HTTPStreamResult, error) {
	var zero HTTPStreamResult
	if h == nil || h.authorize == nil || h.origin == nil || parent == nil || callbacks.Start == nil {
		return zero, ErrDenied
	}
	if parent.Err() != nil {
		return zero, ErrUnavailable
	}
	if int64(len(input.Body)) > h.policy.RequestBytes ||
		!appplatform.ValidHTTPPolicyHeaderValue(input.Accept) ||
		!appplatform.ValidHTTPPolicyHeaderValue(input.ContentType) ||
		appplatform.ValidateHTTPStreamPolicyTarget(h.policy, input.Method, input.URL) != nil ||
		!validHTTPStreamCredential(input) {
		return zero, ErrDenied
	}
	ctx, cancel := context.WithTimeout(parent, h.policy.Duration)
	defer cancel()
	select {
	case h.slots <- struct{}{}:
		defer func() { <-h.slots }()
	default:
		return zero, ErrUnavailable
	}
	input.Body = bytes.Clone(input.Body)
	defer clear(input.Body)
	authorize := func(ctx context.Context) error {
		if err := h.authorize(ctx); err != nil {
			return err
		}
		if extra != nil {
			return extra(ctx)
		}
		return nil
	}
	if authorize(ctx) != nil || ctx.Err() != nil {
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
	if authorize(ctx) != nil {
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
	transport := &http.Transport{
		Proxy:                  nil,
		DisableKeepAlives:      true,
		DisableCompression:     true,
		MaxResponseHeaderBytes: 8192,
		TLSHandshakeTimeout:    5 * time.Second,
		ResponseHeaderTimeout:  minDuration(h.policy.IdleTimeout, 5*time.Second),
		TLSClientConfig:        config,
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			if address != net.JoinHostPort(h.origin.Hostname(), port) {
				return nil, ErrDenied
			}
			return h.dial(ctx, "tcp", pinned)
		},
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{
		Transport: transport,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	req, err := http.NewRequestWithContext(ctx, input.Method, input.URL, bytes.NewReader(input.Body))
	if err != nil {
		return zero, ErrDenied
	}
	req.Header.Set("Accept-Encoding", "identity")
	req.Header.Set("User-Agent", "Portal-App-Broker-Stream/2-dev")
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
	if !validHTTPStreamResponseHeaders(response, h.policy) {
		return zero, ErrUncertain
	}
	contentType := response.Header.Get("Content-Type")
	if credentialResponseEcho([]byte(contentType), input.HeaderName, input.HeaderValue) {
		return zero, ErrUncertain
	}
	if authorize(ctx) != nil || ctx.Err() != nil {
		return zero, ErrUncertain
	}
	if err := callbacks.Start(response.StatusCode, contentType); err != nil {
		return zero, ErrUncertain
	}
	result := HTTPStreamResult{Status: response.StatusCode, ContentType: contentType}
	guard := newHTTPStreamEchoGuard(input.HeaderName, input.HeaderValue)
	type readResult struct {
		data []byte
		err  error
	}
	reads := make(chan readResult, 1)
	readerDone := make(chan struct{})
	readerStop := make(chan struct{})
	go func() {
		defer close(readerDone)
		send := func(result readResult) bool {
			select {
			case reads <- result:
				return true
			case <-readerStop:
				return false
			}
		}
		for {
			buffer := make([]byte, appplatform.MaxHTTPStreamFrameBytes)
			n, readErr := response.Body.Read(buffer)
			if n > 0 {
				if !send(readResult{data: buffer[:n]}) {
					return
				}
			}
			if readErr != nil {
				send(readResult{err: readErr})
				return
			}
			if n == 0 {
				send(readResult{err: io.ErrNoProgress})
				return
			}
		}
	}()
	defer func() {
		close(readerStop)
		_ = response.Body.Close()
		<-readerDone
	}()
	idle := time.NewTimer(h.policy.IdleTimeout)
	defer idle.Stop()
	resetIdle := func() {
		if !idle.Stop() {
			select {
			case <-idle.C:
			default:
			}
		}
		idle.Reset(h.policy.IdleTimeout)
	}
	emit := func(data []byte) error {
		if len(data) == 0 {
			return nil
		}
		if result.Bytes+int64(len(data)) > h.policy.StreamBytes {
			return ErrUncertain
		}
		if callbacks.Chunk != nil {
			if err := callbacks.Chunk(data); err != nil {
				return ErrUncertain
			}
		}
		result.Bytes += int64(len(data))
		result.Frames++
		return nil
	}
	for {
		select {
		case <-ctx.Done():
			return zero, ErrUncertain
		case <-idle.C:
			return zero, ErrUncertain
		case read := <-reads:
			resetIdle()
			if read.err != nil {
				if read.err != io.EOF {
					return zero, ErrUncertain
				}
				if authorize(ctx) != nil || ctx.Err() != nil {
					return zero, ErrUncertain
				}
				safe, _ := guard.Accept(nil)
				if err := emit(safe); err != nil {
					return zero, err
				}
				return result, nil
			}
			if authorize(ctx) != nil || ctx.Err() != nil {
				return zero, ErrUncertain
			}
			safe, leaked := guard.Accept(read.data)
			if leaked {
				return zero, ErrUncertain
			}
			if err := emit(safe); err != nil {
				return zero, err
			}
		}
	}
}

func validHTTPStreamCredential(input HTTPRequest) bool {
	if len(input.HeaderValue) == 0 {
		return input.HeaderName == ""
	}
	if input.HeaderName == "Authorization" && len(input.HeaderValue) <= 16400 {
		const prefix = "Bearer "
		return len(input.HeaderValue) > len(prefix) && string(input.HeaderValue[:len(prefix)]) == prefix
	}
	return input.HeaderName == "X-API-Key" && len(input.HeaderValue) <= 16384
}

func validHTTPStreamResponseHeaders(response *http.Response, p HTTPStreamPolicy) bool {
	encodings := response.Header.Values("Content-Encoding")
	if response.StatusCode < 200 || response.StatusCode >= 300 ||
		response.ContentLength > p.StreamBytes ||
		response.Header.Get("Upgrade") != "" ||
		len(encodings) > 1 || len(encodings) == 1 && encodings[0] != "identity" ||
		len(response.Header.Values("Content-Type")) != 1 {
		return false
	}
	_, ok := appplatform.ValidateHTTPStreamContentType(p, response.Header.Get("Content-Type"))
	return ok
}

type httpStreamEchoGuard struct {
	header string
	value  []byte
	tail   []byte
}

func newHTTPStreamEchoGuard(header string, value []byte) *httpStreamEchoGuard {
	return &httpStreamEchoGuard{header: header, value: bytes.Clone(value)}
}

func (g *httpStreamEchoGuard) Accept(data []byte) ([]byte, bool) {
	if len(g.value) == 0 {
		return bytes.Clone(data), false
	}
	window := append(bytes.Clone(g.tail), data...)
	if credentialResponseEcho(window, g.header, g.value) {
		clear(window)
		return nil, true
	}
	keep := len(g.value) - 1
	if len(window) <= keep {
		g.tail = window
		return nil, false
	}
	safe := bytes.Clone(window[:len(window)-keep])
	g.tail = bytes.Clone(window[len(window)-keep:])
	clear(window)
	return safe, false
}

func minDuration(left, right time.Duration) time.Duration {
	if left < right {
		return left
	}
	return right
}
