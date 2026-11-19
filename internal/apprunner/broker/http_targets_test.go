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
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

type targetFixture struct {
	h        *HTTP
	lookups  atomic.Int32
	dials    atomic.Int32
	requests atomic.Int32
	mu       sync.Mutex
	answers  []string
	dialed   string
	hostIPs  []netip.Addr
	hostErr  error
	hostRead atomic.Int32
	clock    atomic.Int64
}

func (f *targetFixture) dialedAddress() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.dialed
}

func targetPolicy(targets ...appplatform.HTTPTarget) appplatform.HTTPPolicyV2 {
	return appplatform.HTTPPolicyV2{Targets: targets, RequestBytes: 64, ResponseBytes: 64, Deadline: time.Second, Concurrency: 2}
}

func publicTarget(id, origin string) appplatform.HTTPTarget {
	return appplatform.HTTPTarget{ID: id, Origin: origin, Paths: []string{"/v1"}, Methods: []string{"GET", "POST"}, Network: "public"}
}

func privateTarget(id, origin string) appplatform.HTTPTarget {
	return appplatform.HTTPTarget{ID: id, Origin: origin, Paths: []string{"/v1"}, Methods: []string{"GET", "POST"}, Network: "private"}
}

func lan(prefix string, plain bool, ports ...int) appplatform.PrivateNetwork {
	return appplatform.PrivateNetwork{Prefix: prefix, Ports: ports, PlainHTTP: plain}
}

// newTargetFixture serves every dial from one local server; only tests may
// redirect a verified destination to a local fixture.
func newTargetFixture(t *testing.T, p appplatform.HTTPPolicyV2, networks []appplatform.PrivateNetwork, useTLS bool, handler http.HandlerFunc) *targetFixture {
	t.Helper()
	f := &targetFixture{}
	counted := func(w http.ResponseWriter, r *http.Request) {
		f.requests.Add(1)
		handler(w, r)
	}
	var s *httptest.Server
	if useTLS {
		s = httptest.NewTLSServer(http.HandlerFunc(counted))
	} else {
		s = httptest.NewServer(http.HandlerFunc(counted))
	}
	t.Cleanup(s.Close)
	raw, err := appplatform.CanonicalHTTPPolicyV2(p)
	if err != nil {
		t.Fatal(err)
	}
	h, err := NewHTTPPolicy(raw, networks, func(context.Context) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if useTLS {
		roots := x509.NewCertPool()
		roots.AddCert(s.Certificate())
		h.tlsConfig = &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12, ServerName: "example.com"}
	}
	f.clock.Store(time.Unix(1000, 0).UnixNano())
	h.now = func() time.Time { return time.Unix(0, f.clock.Load()) }
	h.lookup = func(context.Context, string) ([]netip.Addr, error) {
		f.lookups.Add(1)
		f.mu.Lock()
		defer f.mu.Unlock()
		var out []netip.Addr
		for _, a := range f.answers {
			out = append(out, netip.MustParseAddr(a))
		}
		return out, nil
	}
	h.dial = func(ctx context.Context, network, address string) (net.Conn, error) {
		f.dials.Add(1)
		f.mu.Lock()
		f.dialed = address
		f.mu.Unlock()
		return (&net.Dialer{}).DialContext(ctx, network, s.Listener.Addr().String())
	}
	h.interfaceAddrs = func() ([]netip.Addr, error) {
		f.hostRead.Add(1)
		f.mu.Lock()
		defer f.mu.Unlock()
		return f.hostIPs, f.hostErr
	}
	f.h = h
	return f
}

func ok(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"ok":true}`))
}

func TestPrivateDNSTargetDialsTheAdmittedAddress(t *testing.T) {
	f := newTargetFixture(t, targetPolicy(privateTarget("lan", "https://nas.lan.example.com:8443")), []appplatform.PrivateNetwork{lan("10.20.0.0/24", false, 8443)}, true, func(w http.ResponseWriter, r *http.Request) {
		if r.Host != "nas.lan.example.com:8443" {
			t.Errorf("host %q", r.Host)
		}
		ok(w, r)
	})
	f.answers = []string{"10.20.0.5"}
	got, err := f.h.Do(context.Background(), HTTPRequest{Method: "GET", URL: "https://nas.lan.example.com:8443/v1/info"})
	if err != nil || got.Status != 200 || string(got.Body) != `{"ok":true}` {
		t.Fatalf("%+v %v", got, err)
	}
	if f.dialedAddress() != "10.20.0.5:8443" || f.lookups.Load() != 1 {
		t.Fatalf("dialed %q lookups %d", f.dialedAddress(), f.lookups.Load())
	}
}

func TestPrivateTargetAddressChecks(t *testing.T) {
	nets := []appplatform.PrivateNetwork{lan("10.20.0.0/24", false, 8443)}
	policy := targetPolicy(privateTarget("lan", "https://nas.lan.example.com:8443"))
	cases := map[string]struct {
		answers []string
		host    []string
	}{
		"outside the allowlist":    {answers: []string{"10.30.0.5"}},
		"public answer":            {answers: []string{"8.8.8.8"}},
		"mixed public and private": {answers: []string{"10.20.0.5", "8.8.8.8"}},
		"mixed private and public": {answers: []string{"8.8.8.8", "10.20.0.5"}},
		"one answer outside":       {answers: []string{"10.20.0.5", "10.20.1.5"}},
		"loopback":                 {answers: []string{"127.0.0.1"}},
		"mapped loopback":          {answers: []string{"::ffff:127.0.0.1"}},
		"link local metadata":      {answers: []string{"169.254.169.254"}},
		"nat64 of an allowed host": {answers: []string{"64:ff9b::a14:5"}},
		"unspecified":              {answers: []string{"0.0.0.0"}},
		"v6 outside":               {answers: []string{"fd00::5"}},
		"host interface address":   {answers: []string{"10.20.0.5"}, host: []string{"10.20.0.5"}},
		"mapped host interface":    {answers: []string{"::ffff:10.20.0.5"}, host: []string{"10.20.0.5"}},
		"host interface in a mix":  {answers: []string{"10.20.0.6", "10.20.0.5"}, host: []string{"10.20.0.5"}},
		"empty answer":             {answers: nil},
		"too many answers":         {answers: []string{"10.20.0.1", "10.20.0.2", "10.20.0.3", "10.20.0.4", "10.20.0.5", "10.20.0.6", "10.20.0.7", "10.20.0.8", "10.20.0.9", "10.20.0.10", "10.20.0.11", "10.20.0.12", "10.20.0.13", "10.20.0.14", "10.20.0.15", "10.20.0.16", "10.20.0.17"}},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			f := newTargetFixture(t, policy, nets, true, ok)
			f.answers = c.answers
			for _, a := range c.host {
				f.hostIPs = append(f.hostIPs, netip.MustParseAddr(a))
			}
			_, err := f.h.Do(context.Background(), HTTPRequest{Method: "GET", URL: "https://nas.lan.example.com:8443/v1"})
			if !errors.Is(err, ErrDenied) || f.dials.Load() != 0 {
				t.Fatalf("err=%v dials=%d", err, f.dials.Load())
			}
		})
	}
	t.Run("port outside the allowlist", func(t *testing.T) {
		f := newTargetFixture(t, targetPolicy(privateTarget("lan", "https://nas.lan.example.com:9443")), nets, true, ok)
		f.answers = []string{"10.20.0.5"}
		if _, err := f.h.Do(context.Background(), HTTPRequest{Method: "GET", URL: "https://nas.lan.example.com:9443/v1"}); !errors.Is(err, ErrDenied) || f.dials.Load() != 0 {
			t.Fatalf("err=%v dials=%d", err, f.dials.Load())
		}
	})
	t.Run("unreadable interface list", func(t *testing.T) {
		f := newTargetFixture(t, policy, nets, true, ok)
		f.answers = []string{"10.20.0.5"}
		f.hostErr = errors.New("netlink")
		if _, err := f.h.Do(context.Background(), HTTPRequest{Method: "GET", URL: "https://nas.lan.example.com:8443/v1"}); !errors.Is(err, ErrDenied) || f.dials.Load() != 0 {
			t.Fatalf("err=%v dials=%d", err, f.dials.Load())
		}
	})
}

func TestPublicTargetStillRefusesPrivateAnswers(t *testing.T) {
	nets := []appplatform.PrivateNetwork{lan("10.20.0.0/24", false, 443)}
	f := newTargetFixture(t, targetPolicy(publicTarget("pub", "https://api.example.com"), privateTarget("lan", "https://nas.lan.example.com")), nets, true, ok)
	for _, answers := range [][]string{{"10.20.0.5"}, {"8.8.8.8", "10.20.0.5"}, {"::ffff:10.20.0.5"}} {
		f.answers = answers
		if _, err := f.h.Do(context.Background(), HTTPRequest{Method: "GET", URL: "https://api.example.com/v1"}); !errors.Is(err, ErrDenied) {
			t.Fatalf("%v: %v", answers, err)
		}
	}
	if f.dials.Load() != 0 {
		t.Fatal("dialed")
	}
	f.answers = []string{"8.8.8.8"}
	if _, err := f.h.Do(context.Background(), HTTPRequest{Method: "GET", URL: "https://api.example.com/v1"}); err != nil || f.dialedAddress() != "8.8.8.8:443" {
		t.Fatalf("%v %q", err, f.dialedAddress())
	}
	// A public answer for a private target is refused too, even with the
	// allowlist non-empty: public space is never in a private allowlist.
	f.answers = []string{"8.8.8.8"}
	if _, err := f.h.Do(context.Background(), HTTPRequest{Method: "GET", URL: "https://nas.lan.example.com/v1"}); !errors.Is(err, ErrDenied) {
		t.Fatal(err)
	}
}

func TestInterfaceSnapshotIsCachedForFiveSeconds(t *testing.T) {
	f := newTargetFixture(t, targetPolicy(privateTarget("lan", "https://nas.lan.example.com")), []appplatform.PrivateNetwork{lan("10.20.0.0/24", false, 443)}, true, ok)
	f.answers = []string{"10.20.0.5"}
	call := func() error {
		_, err := f.h.Do(context.Background(), HTTPRequest{Method: "GET", URL: "https://nas.lan.example.com/v1"})
		return err
	}
	if err := call(); err != nil {
		t.Fatal(err)
	}
	f.clock.Add(int64(4 * time.Second))
	f.mu.Lock()
	f.hostIPs = []netip.Addr{netip.MustParseAddr("10.20.0.5")}
	f.mu.Unlock()
	if err := call(); err != nil || f.hostRead.Load() != 1 {
		t.Fatalf("a fresh snapshot was re-read: %v reads=%d", err, f.hostRead.Load())
	}
	f.clock.Add(int64(2 * time.Second))
	if err := call(); !errors.Is(err, ErrDenied) || f.hostRead.Load() != 2 {
		t.Fatalf("a stale snapshot was kept: %v reads=%d", err, f.hostRead.Load())
	}
}

func TestPlainHTTPNeedsAllowlistAndNeverCarriesACredential(t *testing.T) {
	policy := targetPolicy(privateTarget("plain", "http://10.20.0.5:8080"))
	f := newTargetFixture(t, policy, []appplatform.PrivateNetwork{lan("10.20.0.0/24", false, 8080)}, false, ok)
	if _, err := f.h.Do(context.Background(), HTTPRequest{Method: "GET", URL: "http://10.20.0.5:8080/v1"}); !errors.Is(err, ErrDenied) || f.dials.Load() != 0 {
		t.Fatalf("plain_http not set: err=%v dials=%d", err, f.dials.Load())
	}
	f = newTargetFixture(t, policy, []appplatform.PrivateNetwork{lan("10.20.0.0/24", true, 8080)}, false, func(w http.ResponseWriter, r *http.Request) {
		if r.TLS != nil || r.Header.Get("Authorization") != "" {
			t.Error("unexpected TLS or credential")
		}
		ok(w, r)
	})
	got, err := f.h.Do(context.Background(), HTTPRequest{Method: "GET", URL: "http://10.20.0.5:8080/v1/x"})
	if err != nil || got.Status != 200 || f.dialedAddress() != "10.20.0.5:8080" {
		t.Fatalf("%+v %v %q", got, err, f.dialedAddress())
	}
	if f.lookups.Load() != 0 {
		t.Fatal("an IP literal was looked up")
	}
	var authorized atomic.Int32
	f.h.authorize = func(context.Context) error { authorized.Add(1); return nil }
	dials := f.dials.Load()
	_, err = f.h.Do(context.Background(), HTTPRequest{Method: "GET", URL: "http://10.20.0.5:8080/v1", HeaderName: "Authorization", HeaderValue: []byte("Bearer secret")})
	if !errors.Is(err, ErrDenied) || f.dials.Load() != dials || authorized.Load() != 0 {
		t.Fatalf("credential over http: err=%v dials=%d authorized=%d", err, f.dials.Load()-dials, authorized.Load())
	}
	_, err = f.h.Do(context.Background(), HTTPRequest{Method: "GET", URL: "http://10.20.0.5:8080/v1", HeaderName: "X-API-Key", HeaderValue: []byte("k")})
	if !errors.Is(err, ErrDenied) {
		t.Fatalf("api key over http: %v", err)
	}
}

func TestLiteralTargetsSkipTheLookup(t *testing.T) {
	nets := []appplatform.PrivateNetwork{lan("10.20.0.0/24", false, 8443), lan("fd00:1234:5678::/48", false, 8443)}
	f := newTargetFixture(t, targetPolicy(privateTarget("v4", "https://10.20.0.5:8443"), privateTarget("v6", "https://[fd00:1234:5678::5]:8443")), nets, true, ok)
	f.answers = []string{"8.8.8.8"}
	for url, want := range map[string]string{
		"https://10.20.0.5:8443/v1":           "10.20.0.5:8443",
		"https://[fd00:1234:5678::5]:8443/v1": "[fd00:1234:5678::5]:8443",
	} {
		if _, err := f.h.Do(context.Background(), HTTPRequest{Method: "GET", URL: url}); err != nil || f.dialedAddress() != want {
			t.Fatalf("%s: %v dialed %q", url, err, f.dialedAddress())
		}
	}
	if f.lookups.Load() != 0 {
		t.Fatalf("lookups=%d", f.lookups.Load())
	}
	// A literal on a host interface is refused without any dial.
	f.hostIPs = []netip.Addr{netip.MustParseAddr("10.20.0.5")}
	f.clock.Add(int64(10 * time.Second))
	dials := f.dials.Load()
	if _, err := f.h.Do(context.Background(), HTTPRequest{Method: "GET", URL: "https://10.20.0.5:8443/v1"}); !errors.Is(err, ErrDenied) || f.dials.Load() != dials {
		t.Fatalf("host address literal: %v", err)
	}
}

func TestRedirectsAreNeverFollowed(t *testing.T) {
	f := newTargetFixture(t, targetPolicy(privateTarget("lan", "https://10.20.0.5:8443")), []appplatform.PrivateNetwork{lan("10.20.0.0/24", false, 8443)}, true, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://10.20.0.6:8443/v1/other", http.StatusFound)
	})
	got, err := f.h.Do(context.Background(), HTTPRequest{Method: "GET", URL: "https://10.20.0.5:8443/v1"})
	if !errors.Is(err, ErrUncertain) || got.Status != 0 || len(got.Body) != 0 {
		t.Fatalf("%+v %v", got, err)
	}
	if f.requests.Load() != 1 || f.dials.Load() != 1 {
		t.Fatalf("a redirect was followed: requests=%d dials=%d", f.requests.Load(), f.dials.Load())
	}
}

func TestTargetLimitsNarrowTheSharedBounds(t *testing.T) {
	narrow := privateTarget("lan", "https://10.20.0.5:8443")
	narrow.RequestBytes = 4
	narrow.ResponseBytes = 8
	narrow.Deadline = 150 * time.Millisecond
	wide := publicTarget("pub", "https://api.example.com")
	nets := []appplatform.PrivateNetwork{lan("10.20.0.0/24", false, 8443)}
	f := newTargetFixture(t, targetPolicy(narrow, wide), nets, true, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("mode") {
		case "slow":
			time.Sleep(600 * time.Millisecond)
		case "big":
			_, _ = w.Write([]byte("0123456789"))
			return
		}
		_, _ = w.Write([]byte("ok"))
	})
	f.answers = []string{"8.8.8.8"}
	do := func(url string, body string) (HTTPResponse, error) {
		return f.h.Do(context.Background(), HTTPRequest{Method: "POST", URL: url, Body: []byte(body)})
	}
	if _, err := do("https://10.20.0.5:8443/v1", "12345"); !errors.Is(err, ErrDenied) || f.dials.Load() != 0 {
		t.Fatalf("request over the target bound: %v", err)
	}
	if got, err := do("https://10.20.0.5:8443/v1", "1234"); err != nil || string(got.Body) != "ok" {
		t.Fatalf("%+v %v", got, err)
	}
	if got, err := do("https://10.20.0.5:8443/v1?mode=big", "1"); !errors.Is(err, ErrUncertain) || len(got.Body) != 0 {
		t.Fatalf("response over the target bound: %+v %v", got, err)
	}
	start := time.Now()
	if _, err := do("https://10.20.0.5:8443/v1?mode=slow", "1"); err == nil || time.Since(start) > 500*time.Millisecond {
		t.Fatalf("target deadline not applied: %v after %v", err, time.Since(start))
	}
	// The public target keeps the shared bounds.
	if got, err := do("https://api.example.com/v1?mode=big", "12345678"); err != nil || string(got.Body) != "0123456789" {
		t.Fatalf("shared bounds changed: %+v %v", got, err)
	}
}

func TestSelectRefusesBeforeAnySlotOrAuthority(t *testing.T) {
	var authorized atomic.Int32
	f := newTargetFixture(t, targetPolicy(publicTarget("pub", "https://api.example.com")), nil, true, ok)
	f.h.authorize = func(context.Context) error { authorized.Add(1); return nil }
	f.h.slots <- struct{}{}
	f.h.slots <- struct{}{}
	for _, url := range []string{"https://other.example.com/v1", "http://api.example.com/v1", "https://api.example.com/v2", "https://api.example.com:443/v1", "https://10.20.0.5/v1"} {
		if _, err := f.h.Do(context.Background(), HTTPRequest{Method: "GET", URL: url}); !errors.Is(err, ErrDenied) {
			t.Fatalf("%s: %v (a refused URL must be denied, not report a busy executor)", url, err)
		}
	}
	if _, err := f.h.Do(context.Background(), HTTPRequest{Method: "GET", URL: "https://api.example.com/v1"}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("a full executor must be unavailable: %v", err)
	}
	if authorized.Load() != 0 || f.lookups.Load() != 0 || f.dials.Load() != 0 {
		t.Fatal("work done before selection")
	}
}

func TestNewHTTPPolicyRefusals(t *testing.T) {
	raw, err := appplatform.CanonicalHTTPPolicyV2(targetPolicy(privateTarget("lan", "https://10.20.0.5:8443")))
	if err != nil {
		t.Fatal(err)
	}
	auth := func(context.Context) error { return nil }
	if _, err := NewHTTPPolicy(raw, nil, auth); !errors.Is(err, ErrDenied) {
		t.Fatalf("a private policy needs an allowlist: %v", err)
	}
	if _, err := NewHTTPPolicy(raw, []appplatform.PrivateNetwork{lan("8.8.8.0/24", false, 443)}, auth); !errors.Is(err, ErrDenied) {
		t.Fatalf("an invalid allowlist: %v", err)
	}
	if _, err := NewHTTPPolicy(append(raw[:len(raw):len(raw)], ' '), []appplatform.PrivateNetwork{lan("10.20.0.0/24", false, 8443)}, auth); !errors.Is(err, ErrDenied) {
		t.Fatalf("non-canonical bytes: %v", err)
	}
	if _, err := NewHTTPPolicy(raw, []appplatform.PrivateNetwork{lan("10.20.0.0/24", false, 8443)}, nil); !errors.Is(err, ErrDenied) {
		t.Fatalf("no authority: %v", err)
	}
	h, err := NewHTTPPolicy(raw, []appplatform.PrivateNetwork{lan("10.20.0.0/24", false, 8443)}, auth)
	if err != nil || string(h.raw) != string(raw) {
		t.Fatalf("%v", err)
	}
	// A v1 policy is served as one public target with its own bytes.
	v1 := testPolicy()
	canonical, _ := appplatform.CanonicalHTTPPolicy(v1)
	h, err = NewHTTPPolicy(canonical, nil, auth)
	if err != nil || string(h.raw) != string(canonical) {
		t.Fatalf("v1 raw: %v", err)
	}
	plain, _ := NewHTTP(v1, auth)
	if string(plain.raw) != string(canonical) {
		t.Fatal("NewHTTP raw bytes are not the v1 canonical bytes")
	}
}
