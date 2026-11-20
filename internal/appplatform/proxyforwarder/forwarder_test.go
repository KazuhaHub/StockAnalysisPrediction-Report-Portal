package proxyforwarder

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// startTarget runs a plain TCP server that hands each connection to handle.
func startTarget(t *testing.T, handle func(net.Conn)) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer c.Close()
				handle(c)
			}()
		}
	}()
	t.Cleanup(func() { l.Close(); wg.Wait() })
	return l.Addr().String()
}

func closeWriteOf(c net.Conn) {
	if cw, ok := c.(interface{ CloseWrite() error }); ok {
		cw.CloseWrite()
	}
}

func shortSocket(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "pf")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return filepath.Join(dir, "b.sock")
}

// startBroker is the broker side of the wire protocol: it reads the open frame,
// applies a fake allow-list (host -> local address), dials and splices.
func startBroker(t *testing.T, allow map[string]string) string {
	t.Helper()
	path := shortSocket(t)
	l, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer c.Close()
				c.SetReadDeadline(time.Now().Add(5 * time.Second))
				host, _, err := ReadOpen(c, true)
				c.SetReadDeadline(time.Time{})
				if err != nil {
					return
				}
				reply := func(r Refusal) {
					b, _ := EncodeReply(r)
					c.Write(b)
				}
				addr, ok := allow[host]
				if !ok {
					reply(RefusalHostNotReviewed)
					return
				}
				up, err := net.Dial("tcp", addr)
				if err != nil {
					reply(RefusalConnectFailed)
					return
				}
				defer up.Close()
				reply(RefusalNone)
				var sw sync.WaitGroup
				sw.Add(2)
				go func() { defer sw.Done(); io.Copy(up, c); closeWriteOf(up) }()
				go func() { defer sw.Done(); io.Copy(c, up); closeWriteOf(c) }()
				sw.Wait()
			}()
		}
	}()
	t.Cleanup(func() { l.Close(); wg.Wait() })
	return path
}

// startFixedBroker answers every open with the given raw reply bytes.
func startFixedBroker(t *testing.T, raw []byte) string {
	t.Helper()
	path := shortSocket(t)
	l, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer c.Close()
				c.SetReadDeadline(time.Now().Add(5 * time.Second))
				if _, _, err := ReadOpen(c, true); err != nil {
					return
				}
				c.Write(raw)
			}()
		}
	}()
	t.Cleanup(func() { l.Close(); wg.Wait() })
	return path
}

func unixDialer(path string) func(context.Context) (net.Conn, error) {
	return func(ctx context.Context) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "unix", path)
	}
}

// startForwarder serves on a loopback listener and stops it at cleanup.
func startForwarder(t *testing.T, dial func(context.Context) (net.Conn, error)) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Serve(ctx, l, dial) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("Serve did not return after cancel")
		}
	})
	return l.Addr().String()
}

func rawConnect(t *testing.T, fwd, request string) (net.Conn, *bufio.Reader, *http.Response) {
	t.Helper()
	c, err := net.Dial("tcp", fwd)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	c.SetDeadline(time.Now().Add(10 * time.Second))
	if _, err := io.WriteString(c, request); err != nil {
		t.Fatal(err)
	}
	br := bufio.NewReader(c)
	resp, err := http.ReadResponse(br, &http.Request{Method: "CONNECT"})
	if err != nil {
		t.Fatal(err)
	}
	return c, br, resp
}

func connectReq(target string) string {
	return "CONNECT " + target + " HTTP/1.1\r\nHost: " + target + "\r\n\r\n"
}

func TestNonConnectMethodsGet405(t *testing.T) {
	fwd := startForwarder(t, unixDialer("/nonexistent"))
	for _, m := range []string{"GET", "POST", "OPTIONS", "HEAD", "PUT", "DELETE"} {
		_, _, resp := rawConnect(t, fwd, m+" http://example.com/ HTTP/1.1\r\nHost: example.com\r\nContent-Length: 4\r\n\r\nbody")
		body, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != 405 || resp.Header.Get("Allow") != "CONNECT" || len(body) != 0 {
			t.Errorf("%s: status %d allow %q body %q", m, resp.StatusCode, resp.Header.Get("Allow"), body)
		}
	}
}

func TestMalformedConnectGets400(t *testing.T) {
	fwd := startForwarder(t, unixDialer("/nonexistent"))
	for _, req := range []string{
		"CONNECT example.com HTTP/1.1\r\n\r\n",
		"CONNECT example.com:0 HTTP/1.1\r\n\r\n",
		"CONNECT example.com:99999 HTTP/1.1\r\n\r\n",
		"CONNECT :443 HTTP/1.1\r\n\r\n",
		"CONNECT example.com:443 HTTP/2.0\r\n\r\n",
		"CONNECT example.com:443\r\n\r\n",
		"CONNECT exa\x01mple.com:443 HTTP/1.1\r\n\r\n",
		"CONNECT " + strings.Repeat("a", 300) + ":443 HTTP/1.1\r\n\r\n",
	} {
		_, _, resp := rawConnect(t, fwd, req)
		if resp.StatusCode != 400 {
			t.Errorf("%q: status %d", req, resp.StatusCode)
		}
	}
}

func TestOversizedHeadersAreRejected(t *testing.T) {
	fwd := startForwarder(t, unixDialer("/nonexistent"))
	req := "CONNECT example.com:443 HTTP/1.1\r\nX-Pad: " + strings.Repeat("a", 9000) + "\r\n\r\n"
	_, _, resp := rawConnect(t, fwd, req)
	if resp.StatusCode != 400 {
		t.Errorf("status %d", resp.StatusCode)
	}
}

func TestSlowHeadersAreDropped(t *testing.T) {
	old := headerDeadline
	headerDeadline = 200 * time.Millisecond
	defer func() { headerDeadline = old }()
	fwd := startForwarder(t, unixDialer("/nonexistent"))
	c, err := net.Dial("tcp", fwd)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	io.WriteString(c, "CONNECT example.com:443 HTTP/1.1\r\n")
	c.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := io.ReadAll(c); err != nil {
		t.Fatalf("connection was not closed by the forwarder: %v", err)
	}
}

func TestEveryRefusalMapsTo403(t *testing.T) {
	for refusal := range refusalCodes {
		reply, _ := EncodeReply(refusal)
		sock := startFixedBroker(t, reply)
		fwd := startForwarder(t, unixDialer(sock))
		_, _, resp := rawConnect(t, fwd, connectReq("example.com:443"))
		body, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != 403 || resp.Header.Get("X-Portal-Proxy-Refusal") != string(refusal) || len(body) != 0 {
			t.Errorf("%s: status %d header %q body %q", refusal, resp.StatusCode, resp.Header.Get("X-Portal-Proxy-Refusal"), body)
		}
	}
}

func TestBrokerUnreachableIsUnavailable(t *testing.T) {
	fwd := startForwarder(t, unixDialer(filepath.Join(t.TempDir(), "none.sock")))
	_, _, resp := rawConnect(t, fwd, connectReq("example.com:443"))
	if resp.StatusCode != 403 || resp.Header.Get("X-Portal-Proxy-Refusal") != "unavailable" {
		t.Errorf("status %d header %q", resp.StatusCode, resp.Header.Get("X-Portal-Proxy-Refusal"))
	}
}

func TestProtocolViolationIsUnavailable(t *testing.T) {
	sock := startFixedBroker(t, []byte("garbage-garbage!"))
	fwd := startForwarder(t, unixDialer(sock))
	_, _, resp := rawConnect(t, fwd, connectReq("example.com:443"))
	if resp.StatusCode != 403 || resp.Header.Get("X-Portal-Proxy-Refusal") != "unavailable" {
		t.Errorf("status %d header %q", resp.StatusCode, resp.Header.Get("X-Portal-Proxy-Refusal"))
	}
}

func TestBracketedIPv6TargetIsPassedVerbatim(t *testing.T) {
	sock := startBroker(t, map[string]string{})
	fwd := startForwarder(t, unixDialer(sock))
	_, _, resp := rawConnect(t, fwd, connectReq("[::1]:443"))
	if resp.StatusCode != 403 || resp.Header.Get("X-Portal-Proxy-Refusal") != "host_not_reviewed" {
		t.Errorf("status %d header %q", resp.StatusCode, resp.Header.Get("X-Portal-Proxy-Refusal"))
	}
}

func TestHalfCloseClientToTarget(t *testing.T) {
	target := startTarget(t, func(c net.Conn) {
		n, _ := io.Copy(io.Discard, c)
		fmt.Fprintf(c, "got %d", n)
	})
	fwd := startForwarder(t, unixDialer(startBroker(t, map[string]string{"example.com": target})))
	c, br, resp := rawConnect(t, fwd, connectReq("example.com:443"))
	if resp.StatusCode != 200 {
		t.Fatalf("status %d", resp.StatusCode)
	}
	io.WriteString(c, "0123456789")
	closeWriteOf(c)
	got, err := io.ReadAll(br)
	if err != nil || string(got) != "got 10" {
		t.Fatalf("got %q err %v", got, err)
	}
}

func TestHalfCloseTargetToClient(t *testing.T) {
	late := make(chan string, 1)
	target := startTarget(t, func(c net.Conn) {
		io.WriteString(c, "bye")
		closeWriteOf(c)
		b, _ := io.ReadAll(c)
		late <- string(b)
	})
	fwd := startForwarder(t, unixDialer(startBroker(t, map[string]string{"example.com": target})))
	c, br, resp := rawConnect(t, fwd, connectReq("example.com:443"))
	if resp.StatusCode != 200 {
		t.Fatalf("status %d", resp.StatusCode)
	}
	got, err := io.ReadAll(br)
	if err != nil || string(got) != "bye" {
		t.Fatalf("got %q err %v", got, err)
	}
	io.WriteString(c, "late")
	closeWriteOf(c)
	select {
	case s := <-late:
		if s != "late" {
			t.Fatalf("target saw %q", s)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("bytes sent after the target half-closed never arrived")
	}
}

func TestPipelinedBytesAfterConnectAreForwarded(t *testing.T) {
	target := startTarget(t, func(c net.Conn) { io.Copy(c, c) })
	fwd := startForwarder(t, unixDialer(startBroker(t, map[string]string{"example.com": target})))
	c, br, resp := rawConnect(t, fwd, connectReq("example.com:443")+"HELLO-PIPELINED")
	if resp.StatusCode != 200 {
		t.Fatalf("status %d", resp.StatusCode)
	}
	buf := make([]byte, len("HELLO-PIPELINED"))
	if _, err := io.ReadFull(br, buf); err != nil || string(buf) != "HELLO-PIPELINED" {
		t.Fatalf("echo %q err %v", buf, err)
	}
	c.Close()
}

func TestServeStopsAndLeavesNoGoroutines(t *testing.T) {
	runtime.GC()
	before := runtime.NumGoroutine()
	func() {
		target := startTarget(t, func(c net.Conn) { io.Copy(io.Discard, c) })
		sock := startBroker(t, map[string]string{"example.com": target})
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- Serve(ctx, l, unixDialer(sock)) }()
		c, _, resp := rawConnect(t, l.Addr().String(), connectReq("example.com:443"))
		if resp.StatusCode != 200 {
			t.Fatalf("status %d", resp.StatusCode)
		}
		// The tunnel is still open when the context is cancelled.
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("Serve did not return")
		}
		c.SetReadDeadline(time.Now().Add(5 * time.Second))
		if _, err := io.ReadAll(c); err != nil {
			t.Fatalf("open tunnel was not closed: %v", err)
		}
		c.Close()
	}()
	// startTarget and startBroker clean up in t.Cleanup, which runs after the test
	// body; wait on the forwarder's own goroutines by counting after a settle.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		// Target and broker are still alive (2 accept loops plus their handlers).
		if runtime.NumGoroutine() <= before+8 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("goroutines: before %d, now %d", before, runtime.NumGoroutine())
}

// httpsFixture is a TLS server reachable through broker allow-list name
// example.com, which its certificate covers.
func httpsFixture(t *testing.T) (*httptest.Server, string) {
	t.Helper()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "fixture-ok")
	}))
	t.Cleanup(srv.Close)
	sock := startBroker(t, map[string]string{"example.com": srv.Listener.Addr().String()})
	return srv, sock
}

func TestEndToEndGoClient(t *testing.T) {
	srv, sock := httpsFixture(t)
	fwd := startForwarder(t, unixDialer(sock))
	proxyURL, _ := url.Parse("http://" + fwd)
	pool := x509.NewCertPool()
	pool.AddCert(srv.Certificate())
	tr := &http.Transport{Proxy: http.ProxyURL(proxyURL), TLSClientConfig: &tls.Config{RootCAs: pool}}
	defer tr.CloseIdleConnections()
	client := &http.Client{Transport: tr, Timeout: 10 * time.Second}
	for i := 0; i < 30; i++ {
		resp, err := client.Get("https://example.com/")
		if err != nil {
			t.Fatalf("request %d: %v", i, err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if string(body) != "fixture-ok" {
			t.Fatalf("request %d: body %q", i, body)
		}
	}
	_, err := client.Get("https://unreviewed.example.net/")
	if err == nil || !strings.Contains(err.Error(), "Forbidden") {
		t.Fatalf("refused host: want a proxy error (403 Forbidden), got %v", err)
	}
}

func writeCertPEM(t *testing.T, srv *httptest.Server) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ca.pem")
	data := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw})
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func runPython(t *testing.T, script, fwd string, env ...string) string {
	t.Helper()
	scriptPath := filepath.Join(t.TempDir(), "probe.py")
	if err := os.WriteFile(scriptPath, []byte(script), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "python3", "-I", scriptPath)
	cmd.Env = append(os.Environ(), "HTTPS_PROXY=http://"+fwd, "https_proxy=http://"+fwd, "NO_PROXY=", "no_proxy=")
	cmd.Env = append(cmd.Env, env...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("python: %v\n%s", err, out)
	}
	return string(out)
}

func pythonHas(module string) bool {
	return exec.Command("python3", "-c", "import "+module).Run() == nil
}

func TestEndToEndPythonRequests(t *testing.T) {
	if !pythonHas("requests") {
		t.Skip("python3 module requests is not installed")
	}
	srv, sock := httpsFixture(t)
	fwd := startForwarder(t, unixDialer(sock))
	pemPath := writeCertPEM(t, srv)
	out := runPython(t, "import requests\nprint(requests.get('https://example.com/', timeout=20).text)\n", fwd, "REQUESTS_CA_BUNDLE="+pemPath)
	if !strings.Contains(out, "fixture-ok") {
		t.Fatalf("output %q", out)
	}
}

func TestEndToEndPythonCurlCffi(t *testing.T) {
	if !pythonHas("curl_cffi") {
		t.Skip("python3 module curl_cffi is not installed")
	}
	srv, sock := httpsFixture(t)
	fwd := startForwarder(t, unixDialer(sock))
	pemPath := writeCertPEM(t, srv)
	script := "from curl_cffi import requests\n" +
		"r = requests.get('https://example.com/', impersonate='chrome', verify='" + pemPath + "', proxies={'https': 'http://" + fwd + "'}, timeout=20)\n" +
		"print(r.text)\n"
	out := runPython(t, script, fwd)
	if !strings.Contains(out, "fixture-ok") {
		t.Fatalf("output %q", out)
	}
}

func TestRunRefusesBadConfiguration(t *testing.T) {
	for name, env := range map[string]map[string]string{
		"missing socket": {"PORTAL_APP_BROKER_SOCKET": "", "PORTAL_APP_PROXY_LISTEN": "127.0.0.1:0"},
		"non-loopback":   {"PORTAL_APP_BROKER_SOCKET": "/x.sock", "PORTAL_APP_PROXY_LISTEN": "0.0.0.0:3128"},
		"public address": {"PORTAL_APP_BROKER_SOCKET": "/x.sock", "PORTAL_APP_PROXY_LISTEN": "147.135.70.140:3128"},
		"all interfaces": {"PORTAL_APP_BROKER_SOCKET": "/x.sock", "PORTAL_APP_PROXY_LISTEN": ":3128"},
		"hostname":       {"PORTAL_APP_BROKER_SOCKET": "/x.sock", "PORTAL_APP_PROXY_LISTEN": "example.com:3128"},
		"no port":        {"PORTAL_APP_BROKER_SOCKET": "/x.sock", "PORTAL_APP_PROXY_LISTEN": "127.0.0.1"},
	} {
		for k, v := range env {
			t.Setenv(k, v)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		err := Run(ctx)
		expired := ctx.Err() != nil
		cancel()
		if err == nil || expired {
			t.Errorf("%s: want an immediate error, got %v", name, err)
		}
	}
}

func TestRunServesOnLoopback(t *testing.T) {
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := probe.Addr().String()
	probe.Close()
	reply, _ := EncodeReply(RefusalBusy)
	t.Setenv("PORTAL_APP_BROKER_SOCKET", startFixedBroker(t, reply))
	t.Setenv("PORTAL_APP_PROXY_LISTEN", addr)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Run(ctx) }()
	defer func() {
		cancel()
		<-done
	}()
	var resp *http.Response
	for i := 0; i < 100; i++ {
		c, err := net.Dial("tcp", addr)
		if err != nil {
			time.Sleep(20 * time.Millisecond)
			continue
		}
		c.SetDeadline(time.Now().Add(5 * time.Second))
		io.WriteString(c, connectReq("example.com:443"))
		resp, err = http.ReadResponse(bufio.NewReader(c), &http.Request{Method: "CONNECT"})
		c.Close()
		if err != nil {
			t.Fatal(err)
		}
		break
	}
	if resp == nil || resp.StatusCode != 403 || resp.Header.Get("X-Portal-Proxy-Refusal") != "busy" {
		t.Fatalf("response %+v", resp)
	}
}

func TestRunDefaultListenIsLoopback3128(t *testing.T) {
	if DefaultListen != "127.0.0.1:3128" {
		t.Fatalf("default listen %q", DefaultListen)
	}
}
