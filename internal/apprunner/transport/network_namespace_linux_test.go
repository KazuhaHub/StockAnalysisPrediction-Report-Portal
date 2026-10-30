//go:build linux

package transport

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
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
	"syscall"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/broker"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/journal"
)

// All mount/route changes happen only in the child's new namespaces. No host
// resolver, route, interface, trusted CA or account is modified.
func testNetworkNamespaceControllerBoundary(t *testing.T, root, binary string) {
	t.Helper()
	dir := filepath.Join(root, "network-namespace")
	if err := os.Mkdir(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "hosts"), []byte("127.0.0.1 localhost\n8.8.8.8 api.example.com\n"), 0644); err != nil {
		t.Fatal(err)
	}
	script := `set -eu
mount --make-rprivate /
ip link set lo up
ip addr add 8.8.8.8/32 dev lo
mount --bind "$NETWORK_NAMESPACE_ROOT/hosts" /etc/hosts
exec "$NETWORK_NAMESPACE_BINARY" -test.v -test.run='^TestNetworkNamespaceChild$' -test.timeout=25s
`
	cmd := exec.Command("unshare", "--mount", "--net", "--fork", "--", "sh", "-c", script)
	cmd.Env = []string{"PATH=/usr/sbin:/sbin:/usr/bin:/bin", "GOMAXPROCS=2", "GITHUB_ACTIONS=true", "RUNNER_ENVIRONMENT=github-hosted", "NETWORK_NAMESPACE_CHILD=1", "NETWORK_NAMESPACE_ROOT=" + dir, "NETWORK_NAMESPACE_BINARY=" + binary, "GODEBUG=netdns=go"}
	out, err := cmd.CombinedOutput()
	if err != nil || !bytes.Contains(out, []byte("NETWORK_REAL_TLS_CROSS_UID_REVOCATION_VERIFIED")) {
		t.Fatalf("isolated network acceptance: %v\n%s", err, out)
	}
	t.Log("NETWORK_REAL_TLS_CROSS_UID_REVOCATION_VERIFIED")
}

func TestNetworkNamespaceChild(t *testing.T) {
	if os.Getenv("NETWORK_NAMESPACE_CHILD") != "1" {
		t.Skip("isolated network namespace subprocess only")
	}
	if os.Geteuid() != 0 || os.Getenv("GITHUB_ACTIONS") != "true" || os.Getenv("RUNNER_ENVIRONMENT") != "github-hosted" {
		t.Fatal("disposable root namespace required")
	}
	root := os.Getenv("NETWORK_NAMESPACE_ROOT")
	binary := os.Getenv("NETWORK_NAMESPACE_BINARY")
	if !filepath.IsAbs(root) || !filepath.IsAbs(binary) {
		t.Fatal("missing namespace fixture")
	}
	certificate, ca := networkTLSFixture(t)
	caFile := filepath.Join(root, "fixture-ca.pem")
	if err := os.WriteFile(caFile, ca, 0644); err != nil {
		t.Fatal(err)
	}
	caDir := filepath.Join(root, "empty-ca-directory")
	if err := os.Mkdir(caDir, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SSL_CERT_FILE", caFile)
	t.Setenv("SSL_CERT_DIR", caDir)
	listener, err := net.Listen("tcp", "8.8.8.8:0")
	if err != nil {
		t.Fatal(err)
	}
	var requests atomic.Int32
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		body, err := io.ReadAll(io.LimitReader(r.Body, 65))
		if err != nil || string(body) != `{}` || r.Method != "POST" || r.URL.Path != "/v1/check" || r.TLS == nil || r.Header.Get("Cookie") != "" || r.Header.Get("Authorization") != "" {
			t.Error("invalid actual HTTPS request")
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Set-Cookie", "fixture=must-not-forward")
		w.WriteHeader(201)
		w.Write([]byte(`{"ok":true}`))
	}), ReadHeaderTimeout: time.Second}
	served := make(chan error, 1)
	go func() {
		served <- server.Serve(tls.NewListener(listener, &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS12}))
	}()
	defer func() {
		server.Close()
		if err := <-served; err != nil && !errors.Is(err, http.ErrServerClosed) {
			t.Error(err)
		}
	}()
	port := listener.Addr().(*net.TCPAddr).Port
	policy, err := appplatform.CanonicalHTTPPolicy(appplatform.HTTPPolicy{Origin: "https://api.example.com:" + strconv.Itoa(port), Paths: []string{"/v1"}, Methods: []string{"POST"}, RequestBytes: 64, ResponseBytes: 64, Deadline: 2 * time.Second, Concurrency: 1})
	if err != nil {
		t.Fatal(err)
	}
	parsed, _ := appplatform.ParseHTTPPolicy(policy)
	raw, _, body, a, key := networkSessionFixture(t)
	var command control.NetworkHTTPRequestCommand
	if json.Unmarshal(raw, &command) != nil {
		t.Fatal("fixture command")
	}
	command.PolicyRaw = policy
	command.PolicySHA256 = parsed.SHA256
	command.URL = "https://api.example.com:" + strconv.Itoa(port) + "/v1/check"
	endpoint := filepath.Join(root, "control")
	if err := os.Mkdir(endpoint, 0750); err != nil {
		t.Fatal(err)
	}
	if err := os.Chown(endpoint, 0, int(controllerTestGID)); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(endpoint, 0750); err != nil {
		t.Fatal(err)
	}
	journalDir := filepath.Join(root, "journal")
	if err := os.Mkdir(journalDir, 0700); err != nil {
		t.Fatal(err)
	}
	store, err := journal.Open(journalDir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, _, err := store.EnrollController(context.Background(), journal.ControllerEnrollment{DeploymentID: a.DeploymentID, ControllerEpoch: a.ControllerEpoch, ControlRevision: a.ControlRevision, PublicKey: a.PublicKey, Fingerprint: a.Fingerprint}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	path := filepath.Join(endpoint, "network.sock")
	runtime, err := ListenNetworkController(ctx, path, controllerTestUID, controllerTestGID, store, 2, 2)
	if err != nil {
		t.Fatal(err)
	}
	stopped := make(chan error, 1)
	go func() { stopped <- runtime.Serve(ctx) }()
	defer func() {
		cancel()
		if err := <-stopped; err != nil {
			t.Error(err)
		}
	}()
	authority, _ := json.Marshal(a)
	for index, mode := range []string{"success", "pre_dispatch", "post_io"} {
		before := requests.Load()
		command.RequestID = "30000000-0000-4000-8000-" + fmtNetworkFixtureID(index+1)
		now := time.Now()
		command.IssuedMS = strconv.FormatInt(now.UnixMilli(), 10)
		command.ExpiresMS = strconv.FormatInt(now.Add(30*time.Second).UnixMilli(), 10)
		packet, sig, err := control.SignNetworkHTTPRequest(command, key, a, now.UnixMilli())
		if err != nil {
			t.Fatal(err)
		}
		child := exec.Command(binary, "-test.run=^TestNetworkTLSControllerChild$", "-test.timeout=8s")
		child.Env = []string{"GOMAXPROCS=2", "NETWORK_TLS_CHILD=" + mode, "NETWORK_SOCKET=" + path, "NETWORK_PACKET=" + base64.RawStdEncoding.EncodeToString(packet), "NETWORK_SIGNATURE=" + base64.RawStdEncoding.EncodeToString(sig), "NETWORK_BODY=" + base64.RawStdEncoding.EncodeToString(body), "NETWORK_AUTHORITY=" + base64.RawStdEncoding.EncodeToString(authority), "NETWORK_FIXTURE_KEY=" + base64.RawStdEncoding.EncodeToString(key)}
		child.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: controllerTestUID, Gid: controllerTestGID, Groups: []uint32{controllerTestGID}}}
		if out, err := child.CombinedOutput(); err != nil {
			t.Fatalf("actual HTTPS controller %s: %v\n%s", mode, err, out)
		}
		want := int32(1)
		if mode == "pre_dispatch" {
			want = 0
		}
		if requests.Load()-before != want {
			t.Fatalf("HTTP request count %s: %d", mode, requests.Load()-before)
		}
		end := time.Now().Add(time.Second)
		for {
			runtime.network.mu.Lock()
			remaining := len(runtime.network.sessions)
			runtime.network.mu.Unlock()
			if remaining == 0 {
				break
			}
			if time.Now().After(end) {
				t.Fatal("network session not released")
			}
			time.Sleep(time.Millisecond)
		}
	}
	t.Log("NETWORK_REAL_TLS_CROSS_UID_REVOCATION_VERIFIED")
}

func fmtNetworkFixtureID(index int) string {
	s := strconv.Itoa(index)
	return strings.Repeat("0", 12-len(s)) + s
}

func networkTLSFixture(t *testing.T) (tls.Certificate, []byte) {
	t.Helper()
	caPublic, caKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	root := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "Disposable network acceptance CA"}, NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature}
	rootDER, err := x509.CreateCertificate(rand.Reader, root, root, caPublic, caKey)
	if err != nil {
		t.Fatal(err)
	}
	public, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	leaf := &x509.Certificate{SerialNumber: big.NewInt(2), DNSNames: []string{"api.example.com"}, NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	leafDER, err := x509.CreateCertificate(rand.Reader, leaf, root, public, caKey)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{leafDER, rootDER}, PrivateKey: key}, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: rootDER})
}

func TestNetworkTLSControllerChild(t *testing.T) {
	mode := os.Getenv("NETWORK_TLS_CHILD")
	if mode == "" {
		t.Skip("isolated HTTPS controller subprocess only")
	}
	if uint32(os.Geteuid()) != controllerTestUID {
		t.Fatal("wrong controller identity")
	}
	decode := func(name string) []byte {
		b, err := base64.RawStdEncoding.DecodeString(os.Getenv(name))
		if err != nil {
			t.Fatal("public acceptance fixture")
		}
		return b
	}
	var a control.Authority
	if json.Unmarshal(decode("NETWORK_AUTHORITY"), &a) != nil {
		t.Fatal("fixture authority")
	}
	key := ed25519.PrivateKey(decode("NETWORK_FIXTURE_KEY"))
	if len(key) != ed25519.PrivateKeySize {
		t.Fatal("fixture key")
	}
	if mode == "success" {
		profile, err := ObserveControllerNetwork(context.Background(), os.Getenv("NETWORK_SOCKET"), controllerTestUID, controllerTestGID)
		if err != nil || profile.Controller().Fingerprint != a.Fingerprint || profile.Profile().ActiveRequests != 2 || profile.Profile().CacheEntries != 2 || profile.Profile().WritableBackend || !profile.Endpoint().RootController {
			t.Fatal("actual root network profile unavailable", err)
		}
	}
	raw := decode("NETWORK_PACKET")
	calls := 0
	observed, err := RequestControllerNetwork(context.Background(), os.Getenv("NETWORK_SOCKET"), controllerTestUID, controllerTestGID, raw, decode("NETWORK_SIGNATURE"), decode("NETWORK_BODY"), a, func(ctx context.Context, c control.NetworkHTTPRequestCommand, nonce string) ([]byte, []byte, error) {
		calls++
		if mode == "pre_dispatch" && calls == 3 || mode == "post_io" && calls == 4 {
			return nil, nil, errors.New("fixture grant revoked")
		}
		return control.SignNetworkHTTPCheck(c, nonce, key, a, time.Now().UnixMilli())
	})
	if mode == "success" {
		digest := sha256.Sum256(raw)
		r := observed.Response()
		if err != nil || r.Status != 201 || string(r.Body) != `{"ok":true}` || r.ContentType != "application/json" || calls != 4 || observed.RequestSHA256() != hex.EncodeToString(digest[:]) || !observed.Endpoint().RootController || observed.Controller().Fingerprint != a.Fingerprint {
			t.Fatalf("actual root HTTPS failed: %v calls=%d", err, calls)
		}
	} else if !errors.Is(err, broker.ErrUncertain) || !observed.ReceivedAt().IsZero() || len(observed.Response().Body) != 0 {
		t.Fatal("revoked response leaked", err)
	}
}
