package broker

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"sync/atomic"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/hostnet"
)

func bridge(name, addr string) hostnet.Interface {
	return hostnet.Interface{Name: name, Addrs: []netip.Prefix{netip.MustParsePrefix(addr)}}
}

func TestBridgeCreatedAfterStartDeniesItsWholeSubnet(t *testing.T) {
	nets := []appplatform.PrivateNetwork{lan("10.20.0.0/16", false, 443)}
	f := newTargetFixture(t, targetPolicy(privateTarget("lan", "https://nas.lan.example.com")), nets, true, ok)
	call := func(answer string) error {
		f.answers = []string{answer}
		_, err := f.h.Do(context.Background(), HTTPRequest{Method: "GET", URL: "https://nas.lan.example.com/v1"})
		return err
	}
	if err := call("10.20.5.9"); err != nil {
		t.Fatalf("before the bridge: %v", err)
	}
	// A bridge appears inside the allowlisted prefix after start. Once the cached
	// snapshot expires every address of its subnet is refused, not only the host's.
	f.mu.Lock()
	f.bridges = []hostnet.Interface{bridge("br-0123456789ab", "10.20.5.1/24")}
	f.mu.Unlock()
	f.clock.Add(int64(6 * time.Second))
	dials := f.dials.Load()
	for _, answer := range []string{"10.20.5.9", "10.20.5.1", "10.20.5.254"} {
		if err := call(answer); !errors.Is(err, ErrDenied) {
			t.Errorf("%s inside the bridge subnet: %v", answer, err)
		}
	}
	if f.dials.Load() != dials {
		t.Fatal("a bridge address was dialed")
	}
	// The rest of the prefix, and a mixed answer that touches the bridge, behave as expected.
	if err := call("10.20.6.9"); err != nil {
		t.Fatalf("outside the bridge subnet: %v", err)
	}
	f.answers = []string{"10.20.6.9", "10.20.5.9"}
	if _, err := f.h.Do(context.Background(), HTTPRequest{Method: "GET", URL: "https://nas.lan.example.com/v1"}); !errors.Is(err, ErrDenied) {
		t.Fatalf("mixed answer touching the bridge: %v", err)
	}
}

func TestBridgeDetectionMatchesStartupAndSparesOrdinaryInterfaces(t *testing.T) {
	nets := []appplatform.PrivateNetwork{lan("10.20.0.0/16", false, 443)}
	for name, iface := range map[string]hostnet.Interface{
		"lxcbr":       bridge("lxcbr0", "10.20.7.1/24"),
		"lxdbr":       bridge("lxdbr0", "10.20.7.1/24"),
		"incusbr":     bridge("incusbr0", "10.20.7.1/24"),
		"vmbr":        bridge("vmbr0", "10.20.7.1/24"),
		"virbr":       bridge("virbr0", "10.20.7.1/24"),
		"docker0":     bridge("docker0", "10.20.7.1/24"),
		"kernel kind": {Name: "mynet0", Bridge: true, Addrs: []netip.Prefix{netip.MustParsePrefix("10.20.7.1/24")}},
	} {
		f := newTargetFixture(t, targetPolicy(privateTarget("lan", "https://nas.lan.example.com")), nets, true, ok)
		f.bridges = []hostnet.Interface{iface}
		f.answers = []string{"10.20.7.50"}
		if _, err := f.h.Do(context.Background(), HTTPRequest{Method: "GET", URL: "https://nas.lan.example.com/v1"}); !errors.Is(err, ErrDenied) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// A LAN interface whose subnet contains the prefix is not a bridge: only its own address is refused.
	f := newTargetFixture(t, targetPolicy(privateTarget("lan", "https://nas.lan.example.com")), nets, true, ok)
	f.bridges = []hostnet.Interface{{Name: "eth1", Addrs: []netip.Prefix{netip.MustParsePrefix("10.20.0.2/16")}}}
	f.answers = []string{"10.20.7.50"}
	if _, err := f.h.Do(context.Background(), HTTPRequest{Method: "GET", URL: "https://nas.lan.example.com/v1"}); err != nil {
		t.Fatalf("ordinary LAN interface subnet refused: %v", err)
	}
	f.answers = []string{"10.20.0.2"}
	f.clock.Add(int64(6 * time.Second))
	if _, err := f.h.Do(context.Background(), HTTPRequest{Method: "GET", URL: "https://nas.lan.example.com/v1"}); !errors.Is(err, ErrDenied) {
		t.Fatalf("host address on a LAN interface: %v", err)
	}
}

func TestPlainHTTPRefusesEveryCredentialHeaderBeforeAnyWork(t *testing.T) {
	policy := targetPolicy(privateTarget("plain", "http://10.20.0.5:8080"))
	f := newTargetFixture(t, policy, []appplatform.PrivateNetwork{lan("10.20.0.0/24", true, 8080)}, false, ok)
	var authorized atomic.Int32
	f.h.authorize = func(context.Context) error { authorized.Add(1); return nil }
	// Control: the same request without a credential is served.
	if _, err := f.h.Do(context.Background(), HTTPRequest{Method: "GET", URL: "http://10.20.0.5:8080/v1"}); err != nil {
		t.Fatal(err)
	}
	served := authorized.Load()
	dials := f.dials.Load()
	for _, c := range []struct {
		name  string
		value string
	}{{"Authorization", "Bearer secret"}, {"X-API-Key", "key-1234567"}} {
		_, err := f.h.Do(context.Background(), HTTPRequest{Method: "GET", URL: "http://10.20.0.5:8080/v1", HeaderName: c.name, HeaderValue: []byte(c.value)})
		if !errors.Is(err, ErrDenied) || authorized.Load() != served || f.dials.Load() != dials {
			t.Fatalf("%s over http: err=%v authorize calls=%d dials=%d", c.name, err, authorized.Load()-served, f.dials.Load()-dials)
		}
	}
	// Over https the same X-API-Key header is accepted by this rule.
	g := newTargetFixture(t, targetPolicy(privateTarget("tls", "https://10.20.0.5:8443")), []appplatform.PrivateNetwork{lan("10.20.0.0/24", false, 8443)}, true, ok)
	if _, err := g.h.Do(context.Background(), HTTPRequest{Method: "GET", URL: "https://10.20.0.5:8443/v1", HeaderName: "X-API-Key", HeaderValue: []byte("key-1234567")}); err != nil {
		t.Fatalf("X-API-Key over https: %v", err)
	}
}

// certFor issues a self-signed certificate valid only for the given names.
func certFor(t *testing.T, dns []string, ips []string) (tls.Certificate, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "fixture"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, BasicConstraintsValid: true, IsCA: true, DNSNames: dns}
	for _, ip := range ips {
		template.IPAddresses = append(template.IPAddresses, net.ParseIP(ip))
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(leaf)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, pool
}

// The executor must verify the server certificate against the target's own host.
// These tests give h.tlsConfig only roots, so a ServerName that is not the
// target host (or an empty one) cannot pass: the certificate names exactly the host.
func TestTLSServerNameIsTheTargetHost(t *testing.T) {
	for name, c := range map[string]struct {
		origin string
		dns    []string
		ips    []string
		answer string
	}{
		"private DNS target": {origin: "https://nas.lan.example.com:8443", dns: []string{"nas.lan.example.com"}, answer: "10.20.0.5"},
		"IPv4 literal":       {origin: "https://10.20.0.5:8443", ips: []string{"10.20.0.5"}},
		"IPv6 literal":       {origin: "https://[fd00:1234:5678::5]:8443", ips: []string{"fd00:1234:5678::5"}},
	} {
		t.Run(name, func(t *testing.T) {
			cert, roots := certFor(t, c.dns, c.ips)
			var sni atomic.Value
			sni.Store("unset")
			server := httptest.NewUnstartedServer(http.HandlerFunc(ok))
			server.TLS = &tls.Config{Certificates: []tls.Certificate{cert}, GetConfigForClient: func(hello *tls.ClientHelloInfo) (*tls.Config, error) {
				sni.Store(hello.ServerName)
				return nil, nil
			}}
			server.StartTLS()
			defer server.Close()
			networks := []appplatform.PrivateNetwork{lan("10.20.0.0/24", false, 8443), lan("fd00:1234:5678::/48", false, 8443)}
			f := newTargetFixture(t, targetPolicy(privateTarget("t", c.origin)), networks, true, ok)
			f.answers = []string{c.answer}
			f.h.tlsConfig = &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12} // no ServerName
			f.h.dial = func(ctx context.Context, network, address string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, network, server.Listener.Addr().String())
			}
			target := c.origin + "/v1"
			if got, err := f.h.Do(context.Background(), HTTPRequest{Method: "GET", URL: target}); err != nil || got.Status != 200 {
				t.Fatalf("certificate for the target host refused: %+v %v", got, err)
			}
			if len(c.dns) > 0 && sni.Load() != c.dns[0] {
				t.Fatalf("SNI %q", sni.Load())
			}
			if len(c.dns) == 0 && sni.Load() != "" {
				t.Fatalf("an IP literal sent SNI %q", sni.Load())
			}
			// The same server with a certificate for another name is refused.
			other, otherRoots := certFor(t, []string{"other.example.com"}, []string{"10.20.0.99"})
			server.TLS.Certificates = []tls.Certificate{other}
			f.h.tlsConfig = &tls.Config{RootCAs: otherRoots, MinVersion: tls.VersionTLS12}
			if _, err := f.h.Do(context.Background(), HTTPRequest{Method: "GET", URL: target}); !errors.Is(err, ErrUncertain) {
				t.Fatalf("a certificate for another host was accepted: %v", err)
			}
		})
	}
}
