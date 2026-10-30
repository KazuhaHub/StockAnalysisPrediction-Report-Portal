//go:build linux && (amd64 || arm64)

package app

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"golang.org/x/net/dns/dnsmessage"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// Re-enter only in a new network namespace. Keep the runtime and Docker daemon
// in the same mount namespace so their owned IPC tmpfs mounts remain visible.
// A local DNS fixture serves the unchanged system resolver configuration.
func platformNetworkNamespaceFixture(t *testing.T, fixture string) bool {
	t.Helper()
	if os.Getenv("APP_CANDIDATE_DOCKER_SOCKET") == "" || os.Getenv("PORTAL_NETWORK_NAMESPACE") == "1" {
		return false
	}
	dnsIPs := platformNetworkDNSAddresses(t)
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	script := `set -eu
ip link set lo up
ip addr add 8.8.8.8/32 dev lo
for address in $PORTAL_NETWORK_DNS; do
  if [ "$address" != "8.8.8.8" ]; then ip addr add "$address/32" dev lo; fi
done
exec "$PORTAL_NETWORK_BINARY" -test.v -test.run='^TestPlatformInstallDataEndpointAcceptance$' -test.timeout=135s
`
	cmd := exec.Command("unshare", "--net", "--fork", "--", "sh", "-c", script)
	cmd.Env = append(os.Environ(), "PORTAL_NETWORK_NAMESPACE=1", "PORTAL_NETWORK_DNS="+strings.Join(dnsIPs, " "), "PORTAL_NETWORK_BINARY="+binary, "GODEBUG=netdns=go")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("Portal isolated HTTPS lifecycle: %v\n%s", err, output)
	}
	for _, marker := range []string{"PORTAL_NETWORK_HTTPS_CURRENT_VIEWER", "PORTAL_NETWORK_HTTPS_PRE_IO_REVOKED", "PORTAL_NETWORK_HTTPS_POST_IO_REVOKED"} {
		if !bytes.Contains(output, []byte(marker)) {
			t.Fatalf("missing isolated HTTPS assertion %s\n%s", marker, output)
		}
	}
	t.Log(string(output))
	return true
}

func platformNetworkCertificate(t *testing.T) (tls.Certificate, []byte) {
	t.Helper()
	caPublic, caKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	ca := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "Disposable Portal network acceptance CA"}, NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature}
	caDER, err := x509.CreateCertificate(rand.Reader, ca, ca, caPublic, caKey)
	if err != nil {
		t.Fatal(err)
	}
	public, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	leaf := &x509.Certificate{SerialNumber: big.NewInt(2), DNSNames: []string{"api.example.com"}, NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	leafDER, err := x509.CreateCertificate(rand.Reader, leaf, ca, public, caKey)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{leafDER, caDER}, PrivateKey: key}, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})
}

// Real standard-TLS endpoint in the disposable namespace. A single explicit
// filesystem handshake lets the non-root Portal withdraw its own SQL grant
// after HTTPS has arrived but before the response leaves this fixture.
func startPlatformNetworkHTTPSFixture(t *testing.T, dir, childData string) {
	t.Helper()
	if os.Getenv("PORTAL_NETWORK_NAMESPACE") != "1" || os.Geteuid() != 0 {
		t.Fatal("isolated root network namespace required")
	}
	startPlatformNetworkDNSFixture(t)
	cert, ca := platformNetworkCertificate(t)
	caFile := filepath.Join(dir, "network-ca.pem")
	if err := os.WriteFile(caFile, ca, 0644); err != nil {
		t.Fatal(err)
	}
	caDir := filepath.Join(dir, "empty-ca")
	if err := os.Mkdir(caDir, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SSL_CERT_FILE", caFile)
	t.Setenv("SSL_CERT_DIR", caDir)
	listener, err := net.Listen("tcp", "8.8.8.8:443")
	if err != nil {
		t.Fatal(err)
	}
	var count atomic.Int32
	server := &http.Server{ReadHeaderTimeout: time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := count.Add(1)
		if err := os.WriteFile(filepath.Join(childData, "network-count"), []byte(strconv.Itoa(int(n))), 0444); err != nil {
			t.Error(err)
			w.WriteHeader(500)
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, 2))
		if err != nil || len(body) != 0 || r.Method != "GET" || r.TLS == nil || r.Host != "api.example.com" || r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" || r.Header.Get("Accept") != "application/json" {
			t.Error("invalid actual Portal HTTPS request")
			w.WriteHeader(400)
			return
		}
		switch r.URL.Path {
		case "/v1/success":
		case "/v1/revoke":
			if err := os.WriteFile(filepath.Join(childData, "network-arrived"), []byte("1"), 0444); err != nil {
				t.Error(err)
				w.WriteHeader(500)
				return
			}
			ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
			defer cancel()
			if err := waitPlatformNetworkFixtureFile(ctx, filepath.Join(childData, "network-revoked")); err != nil {
				t.Error("SQL withdrawal acknowledgement", err)
				w.WriteHeader(500)
				return
			}
		default:
			t.Error("unexpected network fixture path")
			w.WriteHeader(404)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Set-Cookie", "fixture=must-not-be-forwarded")
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"ok":true}`))
	})}
	done := make(chan error, 1)
	go func() {
		done <- server.Serve(tls.NewListener(listener, &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}))
	}()
	t.Cleanup(func() {
		server.Close()
		if err := <-done; err != nil && !errors.Is(err, http.ErrServerClosed) {
			t.Error(err)
		}
	})
}

func waitPlatformNetworkFixtureFile(ctx context.Context, path string) error {
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		if raw, err := os.ReadFile(path); err == nil {
			if string(raw) != "1" {
				return errors.New("invalid fixture acknowledgement")
			}
			return nil
		} else if !os.IsNotExist(err) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
func platformNetworkFixtureCount(t *testing.T) int {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(os.Getenv("TMPDIR"), "network-count"))
	if os.IsNotExist(err) {
		return 0
	}
	if err != nil {
		t.Fatal(err)
	}
	count, err := strconv.Atoi(string(raw))
	if err != nil || count < 1 || count > 2 {
		t.Fatal("invalid HTTPS fixture count", err)
	}
	return count
}

func testNativePlatformNetworkRoundtrip(t *testing.T, s *Server, actor platformActorSession, installation string, binding platformBackendRequestBinding, endpoint platformRuntimeEndpoint) {
	t.Helper()
	if os.Getenv("PORTAL_NETWORK_NAMESPACE") != "1" {
		t.Fatal("actual HTTPS fixture is mandatory")
	}
	ctx := context.Background()
	request := func(path string) (int, []byte, error) {
		r, err := s.requestPlatformNetwork(ctx, actor, installation, binding, "GET", "https://api.example.com"+path, "application/json", "", nil, endpoint)
		return r.Status, r.Body, err
	}
	if platformNetworkFixtureCount(t) != 0 {
		t.Fatal("HTTPS before foreground request")
	}
	status, body, err := request("/v1/success")
	if err != nil || status != 200 || string(body) != `{"ok":true}` || platformNetworkFixtureCount(t) != 1 {
		t.Fatal("actual Portal current-viewer HTTPS", status, string(body), err)
	}
	t.Log("PORTAL_NETWORK_HTTPS_CURRENT_VIEWER")
	mustExec(t, s.st, `UPDATE app_platform_capability_grants SET granted=0 WHERE installation_id=? AND capability='network.http'`, installation)
	status, body, err = request("/v1/success")
	mustExec(t, s.st, `UPDATE app_platform_capability_grants SET granted=1 WHERE installation_id=? AND capability='network.http'`, installation)
	if !errors.Is(err, errAppPlatformNetworkUnavailable) || status != 0 || len(body) != 0 || platformNetworkFixtureCount(t) != 1 {
		t.Fatal("withdrawn grant reached HTTPS", err)
	}
	t.Log("PORTAL_NETWORK_HTTPS_PRE_IO_REVOKED")
	// No SQL lock is held across request IO. The server waits for this exact
	// withdrawal before returning, making the post-IO denial deterministic.
	revokeCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		err := waitPlatformNetworkFixtureFile(revokeCtx, filepath.Join(os.Getenv("TMPDIR"), "network-arrived"))
		if err == nil {
			var resultErr error
			result, resultErr := s.st.db.ExecContext(revokeCtx, `UPDATE app_platform_capability_grants SET granted=0 WHERE installation_id=? AND capability='network.http'`, installation)
			err = resultErr
			if err == nil {
				n, e := result.RowsAffected()
				if e != nil || n != 1 {
					err = fmt.Errorf("withdrawal affected %d rows: %v", n, e)
				}
			}
		}
		if err == nil {
			err = os.WriteFile(filepath.Join(os.Getenv("TMPDIR"), "network-revoked"), []byte("1"), 0600)
		}
		done <- err
	}()
	status, body, err = request("/v1/revoke")
	cancel()
	withdrawalErr := <-done
	mustExec(t, s.st, `UPDATE app_platform_capability_grants SET granted=1 WHERE installation_id=? AND capability='network.http'`, installation)
	if withdrawalErr != nil || !errors.Is(err, errAppPlatformNetworkUncertain) || status != 0 || len(body) != 0 || platformNetworkFixtureCount(t) != 2 {
		t.Fatal("post-IO withdrawal released response or retried", withdrawalErr, err)
	}
	t.Log("PORTAL_NETWORK_HTTPS_POST_IO_REVOKED")
}

// Namespace-local DNS answers only the fixed fixture name. Nameserver addresses
// come from the unchanged system resolver file and are validated before shell
// arguments or listeners are created. No packet can leave this namespace.
func platformNetworkDNSAddresses(t *testing.T) []string {
	t.Helper()
	raw, err := os.ReadFile("/etc/resolv.conf")
	if err != nil || len(raw) > 8192 {
		t.Fatal("system resolver fixture unavailable", err)
	}
	var ips []string
	for _, line := range strings.Split(string(raw), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || fields[0] != "nameserver" {
			continue
		}
		ip := net.ParseIP(fields[1])
		if ip == nil || ip.To4() == nil || ip.IsUnspecified() || ip.IsMulticast() {
			continue
		}
		value := ip.To4().String()
		found := false
		for _, prior := range ips {
			if prior == value {
				found = true
			}
		}
		if !found {
			ips = append(ips, value)
		}
	}
	if len(ips) == 0 || len(ips) > 3 {
		t.Fatal("fixture requires one to three IPv4 system nameservers")
	}
	return ips
}
func platformNetworkDNSReply(raw []byte) ([]byte, error) {
	var query dnsmessage.Message
	if len(raw) > 1024 || query.Unpack(raw) != nil || query.Response || len(query.Questions) != 1 {
		return nil, errors.New("invalid fixture DNS question")
	}
	q := query.Questions[0]
	response := dnsmessage.Message{Header: dnsmessage.Header{ID: query.ID, Response: true, Authoritative: true, RecursionDesired: query.RecursionDesired, RecursionAvailable: true}, Questions: query.Questions}
	if strings.ToLower(q.Name.String()) != "api.example.com." || q.Class != dnsmessage.ClassINET {
		response.RCode = dnsmessage.RCodeNameError
	} else if q.Type == dnsmessage.TypeA {
		response.Answers = []dnsmessage.Resource{{Header: dnsmessage.ResourceHeader{Name: q.Name, Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET, TTL: 0}, Body: &dnsmessage.AResource{A: [4]byte{8, 8, 8, 8}}}}
	}
	return response.Pack()
}
func startPlatformNetworkDNSFixture(t *testing.T) {
	t.Helper()
	addresses := platformNetworkDNSAddresses(t)
	if strings.Join(addresses, " ") != os.Getenv("PORTAL_NETWORK_DNS") {
		t.Fatal("system resolver changed before fixture startup")
	}
	for _, address := range addresses {
		address := net.JoinHostPort(address, "53")
		udp, err := net.ListenPacket("udp4", address)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { udp.Close() })
		go func() {
			var buf [1024]byte
			for {
				n, peer, err := udp.ReadFrom(buf[:])
				if err != nil {
					return
				}
				answer, err := platformNetworkDNSReply(buf[:n])
				if err == nil {
					_, _ = udp.WriteTo(answer, peer)
				}
			}
		}()
		tcp, err := net.Listen("tcp4", address)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { tcp.Close() })
		slots := make(chan struct{}, 8)
		go func() {
			for {
				c, err := tcp.Accept()
				if err != nil {
					return
				}
				select {
				case slots <- struct{}{}:
				default:
					c.Close()
					continue
				}
				go func() {
					defer c.Close()
					defer func() { <-slots }()
					_ = c.SetDeadline(time.Now().Add(2 * time.Second))
					var prefix [2]byte
					if _, err := io.ReadFull(c, prefix[:]); err != nil {
						return
					}
					n := int(prefix[0])<<8 | int(prefix[1])
					if n < 12 || n > 1024 {
						return
					}
					raw := make([]byte, n)
					if _, err := io.ReadFull(c, raw); err != nil {
						return
					}
					answer, err := platformNetworkDNSReply(raw)
					if err != nil {
						return
					}
					packet := append([]byte{byte(len(answer) >> 8), byte(len(answer))}, answer...)
					_, _ = c.Write(packet)
				}()
			}
		}()
	}
}
