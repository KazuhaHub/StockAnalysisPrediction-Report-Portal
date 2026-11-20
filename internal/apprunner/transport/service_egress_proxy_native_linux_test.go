//go:build linux

package transport

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform/proxyforwarder"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/driver"
)

// The native acceptance of the forward proxy (doc 184 section 10, "Native"): a process with no network but
// its own loopback, as in a container whose NetworkMode is none, runs the reference forwarder and reaches a
// TLS fixture through the real broker socket of a real root-owned socket directory.
//
// It needs root, so it runs only in the disposable acceptance environment (PORTAL_APP_RUNNER_EGRESS_ACCEPTANCE=1
// on a GitHub-hosted runner), the gate of TestServiceEgressEndpointAcceptance, and skips everywhere else. It
// stands a fresh network namespace in for the container's: Docker offers no more here than that, an interface
// list holding only loopback. The resolver, the dial and the interface list are the fixture's seams, as in
// every other egress test; the address guard has its own tests.
const (
	proxyAcceptanceChildEnv  = "PORTAL_PROXY_ACCEPTANCE_CHILD"
	proxyAcceptanceSocketEnv = "PORTAL_PROXY_ACCEPTANCE_SOCKET"
	proxyAcceptanceCertEnv   = "PORTAL_PROXY_ACCEPTANCE_CERT"
	proxyAcceptanceMarker    = "PORTAL_PROXY_ACCEPTANCE_OK"
)

func TestProxyEndpointAcceptance(t *testing.T) {
	if os.Getenv(proxyAcceptanceChildEnv) == "1" {
		proxyAcceptanceChild(t)
		return
	}
	if os.Getenv("PORTAL_APP_RUNNER_EGRESS_ACCEPTANCE") != "1" {
		t.Skip("explicit disposable egress acceptance not enabled")
	}
	if os.Getenv("GITHUB_ACTIONS") != "true" || os.Getenv("RUNNER_ENVIRONMENT") != "github-hosted" || os.Geteuid() != 0 {
		t.Fatal("proxy acceptance requires root on a disposable GitHub-hosted Linux runner")
	}
	f := newEgrFixture(t)
	server := f.proxySeams()
	dir, err := os.MkdirTemp("/run", "portal-proxy-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(dir, driver.ServiceBrokerSocketName)
	f.registry.bind = func(ctx context.Context, v control.VerifiedServiceEgressSession, open EgressOpen, listen bool) (EgressListener, error) {
		if !listen {
			return EgressListener{}, nil
		}
		broker, err := driver.BindServiceBrokerInDir(dir)
		if err != nil {
			return EgressListener{}, err
		}
		return EgressListener{Listener: broker.Listener, PeerOK: egressPeerUID(EgressAppUID), Intact: broker.Intact}, nil
	}
	path, _ := egressEndpoint(t, f, true)
	command := f.sessionCommand(proxyOptions(t, proxyAndHTTP, "qt.gtimg.cn"))
	raw, sig, err := control.SignServiceEgressSession(command, f.key, f.a, time.Now().UnixMilli())
	if err != nil {
		t.Fatal(err)
	}
	mux, err := openServiceEgress(context.Background(), path, privateProfile(uint32(os.Geteuid())), raw, sig, f.a, f.descriptor)
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = mux.Run(context.Background()) }()
	defer mux.Close(EgressCloseShutdown)
	cert := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
	child := exec.Command(os.Args[0], "-test.run=^TestProxyEndpointAcceptance$")
	child.Env = append(os.Environ(), proxyAcceptanceChildEnv+"=1", proxyAcceptanceSocketEnv+"="+socket, proxyAcceptanceCertEnv+"="+string(cert),
		"HTTPS_PROXY=http://127.0.0.1:3128", "https_proxy=http://127.0.0.1:3128", "NO_PROXY=127.0.0.1,localhost", "PORTAL_APP_PROXY_LISTEN=127.0.0.1:3128")
	// The child starts as root in a new network namespace and drops to the application's user itself, after it
	// has brought its loopback up, which is what a container runtime does before it starts the application.
	child.SysProcAttr = &syscall.SysProcAttr{Cloneflags: syscall.CLONE_NEWNET}
	out, err := child.CombinedOutput()
	if err != nil || !strings.Contains(string(out), proxyAcceptanceMarker) {
		t.Fatalf("the network-less application did not reach the fixture through the proxy: %v\n%s", err, out)
	}
}

// proxyAcceptanceChild runs as root in an empty network namespace.
func proxyAcceptanceChild(t *testing.T) {
	if err := bringLoopbackUp(); err != nil {
		t.Fatal("loopback:", err)
	}
	if err := unix.Setgid(EgressAppUID); err != nil {
		t.Fatal(err)
	}
	if err := unix.Setuid(EgressAppUID); err != nil {
		t.Fatal(err)
	}
	// No network but loopback, as with NetworkMode none.
	interfaces, err := net.Interfaces()
	if err != nil {
		t.Fatal(err)
	}
	for _, i := range interfaces {
		if i.Flags&net.FlagLoopback == 0 {
			t.Fatalf("the namespace has a non-loopback interface %s", i.Name)
		}
	}
	if conn, err := net.DialTimeout("tcp", "93.184.216.34:443", 2*time.Second); err == nil {
		conn.Close()
		t.Fatal("the application reached the internet directly")
	}
	socket := os.Getenv(proxyAcceptanceSocketEnv)
	listener, err := net.Listen("tcp", os.Getenv("PORTAL_APP_PROXY_LISTEN"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		_ = proxyforwarder.Serve(ctx, listener, func(ctx context.Context) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", socket)
		})
	}()
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM([]byte(os.Getenv(proxyAcceptanceCertEnv))) {
		t.Fatal("no fixture certificate")
	}
	proxyURL, _ := url.Parse(os.Getenv("HTTPS_PROXY"))
	// The fixture's certificate does not name the reviewed host, so the chain is checked and the name is not:
	// the point is the path, and the SNI the client sends is still the CONNECT host.
	client := &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{
		Proxy:           http.ProxyURL(proxyURL),
		TLSClientConfig: &tls.Config{RootCAs: roots, InsecureSkipVerify: true, MinVersion: tls.VersionTLS12},
	}}
	resp, err := client.Get("https://qt.gtimg.cn/q=sh000001")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(body), "hello from upstream") {
		t.Fatalf("through the proxy: %d %q", resp.StatusCode, body)
	}
	if _, err := client.Get("https://evil.example.com/"); err == nil {
		t.Fatal("an unreviewed host was reached")
	}
	fmt.Println(proxyAcceptanceMarker)
}

// bringLoopbackUp sets IFF_UP on lo of the current network namespace.
func bringLoopbackUp() error {
	fd, err := unix.Socket(unix.AF_INET, unix.SOCK_DGRAM, 0)
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	req, err := unix.NewIfreq("lo")
	if err != nil {
		return err
	}
	if err := unix.IoctlIfreq(fd, unix.SIOCGIFFLAGS, req); err != nil {
		return err
	}
	req.SetUint16(req.Uint16() | unix.IFF_UP | unix.IFF_RUNNING)
	return unix.IoctlIfreq(fd, unix.SIOCSIFFLAGS, req)
}
