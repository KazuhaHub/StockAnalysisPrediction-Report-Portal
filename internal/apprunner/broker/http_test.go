package broker

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func testPolicy() HTTPPolicy {
	return HTTPPolicy{Origin: "https://api.example.com", Paths: []string{"/v1"}, Methods: []string{"GET", "POST"}, RequestBytes: 64, ResponseBytes: 64, Deadline: time.Second, Concurrency: 1}
}

func TestHTTPPolicyRefusesBeforeNetwork(t *testing.T) {
	for _, target := range []string{"http://api.example.com/v1", "https://other.example.com/v1", "https://api.example.com.evil/v1", "https://api.example.com/v10", "https://api.example.com/v1/../admin", "https://api.example.com/v1/%2e%2e/admin", "https://api.example.com/v1//a", "https://api.example.com/v1#x", "https://u:p@api.example.com/v1", "https://api.example.com:443/v1"} {
		t.Run(target, func(t *testing.T) {
			h, err := NewHTTP(testPolicy(), func(context.Context) error { return nil })
			if err != nil {
				t.Fatal(err)
			}
			h.lookup = func(context.Context, string) ([]netip.Addr, error) {
				t.Fatal("invalid URL reached DNS")
				return nil, nil
			}
			_, err = h.Do(context.Background(), HTTPRequest{Method: "GET", URL: target})
			if !errors.Is(err, ErrDenied) {
				t.Fatalf("got %v", err)
			}
		})
	}
}

func TestHTTPRejectsUnsafeDNSSet(t *testing.T) {
	for _, address := range []string{"127.0.0.1", "10.0.0.1", "169.254.169.254", "100.64.0.1", "0.0.0.1", "192.0.2.1", "198.18.0.1", "224.0.0.1", "240.0.0.1", "::1", "fc00::1", "fe80::1", "::ffff:127.0.0.1", "64:ff9b::7f00:1", "2002:7f00:1::1", "2001:db8::1", "3fff::1"} {
		t.Run(address, func(t *testing.T) {
			h, _ := NewHTTP(testPolicy(), func(context.Context) error { return nil })
			h.lookup = func(context.Context, string) ([]netip.Addr, error) {
				return []netip.Addr{netip.MustParseAddr("8.8.8.8"), netip.MustParseAddr(address)}, nil
			}
			h.dial = func(context.Context, string, string) (net.Conn, error) {
				t.Fatal("unsafe answer dialed")
				return nil, nil
			}
			_, err := h.Do(context.Background(), HTTPRequest{Method: "GET", URL: "https://api.example.com/v1"})
			if !errors.Is(err, ErrDenied) {
				t.Fatalf("got %v", err)
			}
		})
	}
}

// Only tests may redirect a verified public destination to a local TLS fixture.
// Production constructor exposes neither a replacement dialer nor custom roots.
func fixture(t *testing.T, p HTTPPolicy, auth func(context.Context) error, handler http.HandlerFunc) (*HTTP, *atomic.Int32, *atomic.Int32) {
	t.Helper()
	s := httptest.NewTLSServer(handler)
	t.Cleanup(s.Close)
	h, err := NewHTTP(p, auth)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(s.Certificate())
	h.tlsConfig = &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12, ServerName: "example.com"}
	var lookups, dials atomic.Int32
	h.lookup = func(context.Context, string) ([]netip.Addr, error) {
		lookups.Add(1)
		return []netip.Addr{netip.MustParseAddr("8.8.8.8")}, nil
	}
	h.dial = func(ctx context.Context, network, address string) (net.Conn, error) {
		dials.Add(1)
		if address != "8.8.8.8:443" {
			t.Errorf("unbound dial %q", address)
		}
		return (&net.Dialer{}).DialContext(ctx, network, s.Listener.Addr().String())
	}
	return h, &lookups, &dials
}

func TestHTTPRealTLSBufferedResponseAndAuthority(t *testing.T) {
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:1")
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:1")
	var checks atomic.Int32
	h, lookups, dials := fixture(t, testPolicy(), func(context.Context) error { checks.Add(1); return nil }, func(w http.ResponseWriter, r *http.Request) {
		if r.Host != "api.example.com" || r.Header.Get("Cookie") != "" || r.Header.Get("Authorization") != "" {
			t.Error("wrong authority/headers")
		}
		if r.Header.Get("Accept-Encoding") != "identity" {
			t.Error("compression enabled")
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Set-Cookie", "private=x")
		w.WriteHeader(201)
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	got, err := h.Do(context.Background(), HTTPRequest{Method: "POST", URL: "https://api.example.com/v1/check?q=a%20b", ContentType: "application/json", Body: []byte(`{}`)})
	if err != nil || got.Status != 201 || string(got.Body) != `{"ok":true}` || got.ContentType != "application/json" {
		t.Fatalf("%+v %v", got, err)
	}
	if checks.Load() != 3 || lookups.Load() != 1 || dials.Load() != 1 {
		t.Fatalf("checks=%d dns=%d dial=%d", checks.Load(), lookups.Load(), dials.Load())
	}
}

func TestHTTPNeverReplaysOrReleasesUntrustedResponse(t *testing.T) {
	for _, mode := range []string{"redirect", "oversized", "compressed", "revoked", "disconnect", "tls", "tls-name", "headers", "chunked", "encoding-list"} {
		t.Run(mode, func(t *testing.T) {
			var calls, checks atomic.Int32
			h, _, dials := fixture(t, testPolicy(), func(context.Context) error {
				if checks.Add(1) == 3 && mode == "revoked" {
					return errors.New("revoked secret diagnostic")
				}
				return nil
			}, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				switch mode {
				case "redirect":
					w.Header().Set("Location", "https://api.example.com/v1/next")
					w.WriteHeader(307)
				case "oversized":
					_, _ = w.Write([]byte(strings.Repeat("x", 65)))
				case "encoding-list":
					w.Header().Add("Content-Encoding", "identity")
					w.Header().Add("Content-Encoding", "gzip")
					_, _ = w.Write([]byte("private"))
				case "compressed":
					w.Header().Set("Content-Encoding", "gzip")
					_, _ = w.Write([]byte("private"))
				case "headers":
					w.Header().Set("X-Large", strings.Repeat("x", 10000))
					_, _ = w.Write([]byte("private"))
				case "chunked":
					w.(http.Flusher).Flush()
					_, _ = w.Write([]byte(strings.Repeat("x", 65)))
				case "disconnect":
					c, _, _ := w.(http.Hijacker).Hijack()
					_ = c.Close()
				default:
					_, _ = w.Write([]byte("private"))
				}
			})
			if mode == "tls-name" {
				h.tlsConfig.ServerName = "wrong.invalid"
			}
			if mode == "tls" {
				h.tlsConfig = &tls.Config{MinVersion: tls.VersionTLS12}
			}
			got, err := h.Do(context.Background(), HTTPRequest{Method: "POST", URL: "https://api.example.com/v1"})
			if !errors.Is(err, ErrUncertain) || len(got.Body) != 0 || got.Status != 0 || strings.Contains(err.Error(), "secret") {
				t.Fatalf("leak %+v %v", got, err)
			}
			if dials.Load() != 1 || calls.Load() > 1 {
				t.Fatalf("replayed calls=%d dials=%d", calls.Load(), dials.Load())
			}
			if mode != "tls" && mode != "tls-name" && calls.Load() != 1 {
				t.Fatal("fixture was not exercised")
			}
		})
	}
}

func TestHTTPConcurrencyCancellationAndPreDispatchRevocation(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	h, _, _ := fixture(t, testPolicy(), func(context.Context) error { return nil }, func(w http.ResponseWriter, r *http.Request) { close(entered); <-release; _, _ = w.Write([]byte("ok")) })
	done := make(chan error, 1)
	go func() {
		_, err := h.Do(context.Background(), HTTPRequest{Method: "GET", URL: "https://api.example.com/v1"})
		done <- err
	}()
	<-entered
	_, err := h.Do(context.Background(), HTTPRequest{Method: "GET", URL: "https://api.example.com/v1"})
	if !errors.Is(err, ErrUnavailable) {
		t.Fatal(err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	var count atomic.Int32
	h, _ = NewHTTP(testPolicy(), func(context.Context) error {
		if count.Add(1) == 2 {
			return errors.New("revoked")
		}
		return nil
	})
	h.lookup = func(context.Context, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("8.8.8.8")}, nil
	}
	h.dial = func(context.Context, string, string) (net.Conn, error) {
		t.Fatal("revoked call dispatched")
		return nil, nil
	}
	if _, err = h.Do(context.Background(), HTTPRequest{Method: "GET", URL: "https://api.example.com/v1"}); !errors.Is(err, ErrDenied) {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = h.Do(ctx, HTTPRequest{Method: "GET", URL: "https://api.example.com/v1"}); !errors.Is(err, ErrUnavailable) {
		t.Fatal(err)
	}
}

func TestHTTPPolicyAndInputs(t *testing.T) {
	for _, mutate := range []func(*HTTPPolicy){func(p *HTTPPolicy) { p.Origin = "http://api.example.com" }, func(p *HTTPPolicy) { p.Origin = "https://127.0.0.1" }, func(p *HTTPPolicy) { p.Paths = nil }, func(p *HTTPPolicy) { p.Methods = []string{"CONNECT"} }, func(p *HTTPPolicy) { p.Concurrency = 5 }, func(p *HTTPPolicy) { p.Deadline = 16 * time.Second }, func(p *HTTPPolicy) { p.ResponseBytes = 0 }} {
		p := testPolicy()
		mutate(&p)
		if _, err := NewHTTP(p, func(context.Context) error { return nil }); !errors.Is(err, ErrDenied) {
			t.Fatal("accepted bad policy", err)
		}
	}
	if _, err := NewHTTP(testPolicy(), nil); !errors.Is(err, ErrDenied) {
		t.Fatal("nil authority")
	}
	var h HTTP
	if _, err := h.Do(context.Background(), HTTPRequest{}); !errors.Is(err, ErrDenied) {
		t.Fatal("zero value enabled")
	}
	for _, r := range []HTTPRequest{{Method: "DELETE"}, {Method: "GET", Body: []byte(strings.Repeat("x", 65))}, {Method: "GET", Accept: "x\r\nCookie: a"}, {Method: "GET", ContentType: "x\x00"}} {
		h, _ := NewHTTP(testPolicy(), func(context.Context) error { return nil })
		h.lookup = func(context.Context, string) ([]netip.Addr, error) {
			t.Fatal("bad request reached DNS")
			return nil, nil
		}
		r.URL = "https://api.example.com/v1"
		if _, err := h.Do(context.Background(), r); !errors.Is(err, ErrDenied) {
			t.Fatal(err)
		}
	}
}

func TestHTTPDeadlineAndCallerCancellation(t *testing.T) {
	for _, cancelCaller := range []bool{false, true} {
		t.Run(map[bool]string{false: "deadline", true: "caller"}[cancelCaller], func(t *testing.T) {
			p := testPolicy()
			p.Deadline = 150 * time.Millisecond
			entered := make(chan struct{})
			h, _, dials := fixture(t, p, func(context.Context) error { return nil }, func(w http.ResponseWriter, r *http.Request) { close(entered); <-r.Context().Done() })
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() {
				_, err := h.Do(ctx, HTTPRequest{Method: "POST", URL: "https://api.example.com/v1"})
				done <- err
			}()
			select {
			case <-entered:
			case <-time.After(time.Second):
				t.Fatal("no dispatch")
			}
			if cancelCaller {
				cancel()
			}
			select {
			case err := <-done:
				if !errors.Is(err, ErrUncertain) {
					t.Fatal(err)
				}
			case <-time.After(time.Second):
				t.Fatal("deadline not enforced")
			}
			if dials.Load() != 1 || len(h.slots) != 0 {
				t.Fatal("replay or retained slot")
			}
		})
	}
}

func TestHTTPPolicyIsCopiedAndDNSFailureDoesNotDial(t *testing.T) {
	p := testPolicy()
	h, _ := NewHTTP(p, func(context.Context) error { return nil })
	p.Paths[0] = "/admin"
	p.Methods[0] = "DELETE"
	h.lookup = func(context.Context, string) ([]netip.Addr, error) {
		return nil, errors.New("private resolver diagnostic")
	}
	h.dial = func(context.Context, string, string) (net.Conn, error) {
		t.Fatal("DNS failure dialed")
		return nil, nil
	}
	_, err := h.Do(context.Background(), HTTPRequest{Method: "GET", URL: "https://api.example.com/v1"})
	if !errors.Is(err, ErrUnavailable) {
		t.Fatal(err)
	}
	_, err = h.Do(context.Background(), HTTPRequest{Method: "DELETE", URL: "https://api.example.com/admin"})
	if !errors.Is(err, ErrDenied) {
		t.Fatal("caller mutated live policy")
	}
}

func TestHTTPPublicAddressPolicy(t *testing.T) {
	for _, s := range []string{"8.8.8.8", "1.1.1.1", "2001:4860:4860::8888", "2606:4700:4700::1111"} {
		if !publicAddress(netip.MustParseAddr(s)) {
			t.Fatal("public address refused", s)
		}
	}
	for _, answers := range [][]netip.Addr{nil, make([]netip.Addr, 17), {netip.Addr{}}} {
		h, _ := NewHTTP(testPolicy(), func(context.Context) error { return nil })
		h.lookup = func(context.Context, string) ([]netip.Addr, error) { return answers, nil }
		h.dial = func(context.Context, string, string) (net.Conn, error) {
			t.Fatal("invalid DNS dialed")
			return nil, nil
		}
		if _, err := h.Do(context.Background(), HTTPRequest{Method: "GET", URL: "https://api.example.com/v1"}); !errors.Is(err, ErrDenied) {
			t.Fatal(err)
		}
	}
}
