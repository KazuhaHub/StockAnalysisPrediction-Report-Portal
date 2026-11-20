//go:build linux

package transport

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform/proxyforwarder"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/hostnet"
)

// proxySeams points the runner's proxy at a local TLS server: every name resolves to a public
// address, and every dial lands on the server.
func (f *egrFixture) proxySeams() *httptest.Server {
	f.t.Helper()
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "hello from upstream") }))
	f.t.Cleanup(server.Close)
	f.registry.proxy.Lookup = func(context.Context, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("93.184.216.34")}, nil
	}
	f.registry.proxy.Interfaces = func() ([]hostnet.Interface, error) { return nil, nil }
	f.registry.proxy.Dial = func(ctx context.Context, _, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "tcp", server.Listener.Addr().String())
	}
	return server
}

// proxyOptions are the options of a session that carries the proxy with hosts.
func proxyOptions(t *testing.T, resources []string, hosts ...string) egrOptions {
	t.Helper()
	raw, err := appplatform.CanonicalProxyPolicy(appplatform.ProxyPolicy{Hosts: hosts, DailyBytes: appplatform.ProxyDailyBytesMax})
	if err != nil {
		t.Fatal(err)
	}
	proxy := control.NewServiceEgressProxy(raw)
	return egrOptions{resources: resources, mutate: func(c *control.ServiceEgressSessionCommand) {
		c.Format = control.ServiceEgressSessionProxyFormat
		c.Proxy = &proxy
	}}
}

var proxyAndHTTP = []string{"network.http", "network.proxy", "notifications.publish"}

// tunnel opens a PRXY0001 stream on the installation's broker socket.
func (f *egrFixture) tunnel(generation, host string, port int) (net.Conn, proxyforwarder.Refusal) {
	f.t.Helper()
	l := f.listener(generation)
	if l == nil {
		f.t.Fatal("no listener")
	}
	conn, err := l.dial()
	if err != nil {
		f.t.Fatal(err)
	}
	f.t.Cleanup(func() { conn.Close() })
	frame, err := proxyforwarder.EncodeOpen(host, port)
	if err != nil {
		f.t.Fatal(err)
	}
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := conn.Write(frame); err != nil {
		f.t.Fatal(err)
	}
	refusal, err := proxyforwarder.ReadReply(bufio.NewReader(conn))
	if err != nil {
		f.t.Fatalf("no answer to the open: %v", err)
	}
	_ = conn.SetDeadline(time.Time{})
	return conn, refusal
}

func tunnelGet(t *testing.T, conn net.Conn, host string) string {
	t.Helper()
	c := tls.Client(conn, &tls.Config{ServerName: host, InsecureSkipVerify: true, MinVersion: tls.VersionTLS12})
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := io.WriteString(c, "GET / HTTP/1.1\r\nHost: "+host+"\r\nConnection: close\r\n\r\n"); err != nil {
		t.Fatalf("write through the tunnel: %v", err)
	}
	body, _ := io.ReadAll(c)
	return string(body)
}

func tunnelClosed(conn net.Conn, within time.Duration) bool {
	_ = conn.SetReadDeadline(time.Now().Add(within))
	_, err := io.Copy(io.Discard, conn)
	if ne, ok := err.(net.Error); ok && ne.Timeout() {
		return false
	}
	return true
}

func TestProxyStreamOnTheBrokerSocket(t *testing.T) {
	f := newEgrFixture(t)
	f.proxySeams()
	f.open(proxyOptions(t, proxyAndHTTP, "*.eastmoney.com", "qt.gtimg.cn"))
	conn, refusal := f.tunnel("1", "qt.gtimg.cn", 443)
	if refusal != proxyforwarder.RefusalNone {
		t.Fatalf("refused: %s", refusal)
	}
	if body := tunnelGet(t, conn, "qt.gtimg.cn"); !strings.Contains(body, "hello from upstream") {
		t.Fatalf("the response did not come back: %q", body)
	}
	if _, refusal := f.tunnel("1", "evil.example.com", 443); refusal != proxyforwarder.RefusalHostNotReviewed {
		t.Fatalf("unreviewed host: %q", refusal)
	}
	if _, refusal := f.tunnel("1", "qt.gtimg.cn", 8443); refusal != proxyforwarder.RefusalPortNotPermitted {
		t.Fatalf("other port: %q", refusal)
	}
	// The same socket still serves portal.broker/1.
	r, err := f.request("1", "GET", "/v1/status", "", "")
	if err != nil || r.status != http.StatusOK || !strings.Contains(r.body, `"network.proxy"`) {
		t.Fatalf("status: %+v %v", r, err)
	}
}

func TestProxyStreamNeedsTheProxyResource(t *testing.T) {
	f := newEgrFixture(t)
	f.proxySeams()
	f.open(egrOptions{}) // network.http and notifications.publish only
	if _, refusal := f.tunnel("1", "qt.gtimg.cn", 443); refusal != proxyforwarder.RefusalNotGranted {
		t.Fatalf("a session without the proxy: %q", refusal)
	}
	// No session at all: the listener does not exist, so there is nothing to ask.
	g := newEgrFixture(t)
	if g.listener("1") != nil {
		t.Fatal("a listener without a session")
	}
}

func TestProxyStreamAfterTheSessionEnds(t *testing.T) {
	f := newEgrFixture(t)
	f.proxySeams()
	p := f.open(proxyOptions(t, proxyAndHTTP, "qt.gtimg.cn"))
	conn, refusal := f.tunnel("1", "qt.gtimg.cn", 443)
	if refusal != proxyforwarder.RefusalNone {
		t.Fatalf("refused: %s", refusal)
	}
	// Portal closes the session (revocation, drift, a restore pause): every tunnel is cut and the
	// broker answers not_granted until a new session arrives.
	p.mux.Close(EgressCloseDrift)
	if !tunnelClosed(conn, 5*time.Second) {
		t.Fatal("the tunnel outlived its session")
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		c, refusal := f.tunnel("1", "qt.gtimg.cn", 443)
		c.Close()
		if refusal == proxyforwarder.RefusalNotGranted {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("after the session ended: %q", refusal)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestProxyRenewalKeepsTunnelsOnlyWhenThePolicyIsTheSame(t *testing.T) {
	for name, tc := range map[string]struct {
		next   func(*testing.T) egrOptions
		stays  bool
		reason string
	}{
		"same policy":     {func(t *testing.T) egrOptions { return proxyOptions(t, proxyAndHTTP, "qt.gtimg.cn") }, true, ""},
		"changed policy":  {func(t *testing.T) egrOptions { return proxyOptions(t, proxyAndHTTP, "qt.gtimg.cn", "x.example.com") }, false, "policy"},
		"proxy withdrawn": {func(t *testing.T) egrOptions { return egrOptions{} }, false, "session"},
		"a different mode": {func(t *testing.T) egrOptions {
			return proxyOptions(t, []string{"network.proxy"}, "qt.gtimg.cn", "y.example.com")
		}, false, "policy"},
	} {
		t.Run(name, func(t *testing.T) {
			f := newEgrFixture(t)
			f.proxySeams()
			f.open(proxyOptions(t, proxyAndHTTP, "qt.gtimg.cn"))
			conn, refusal := f.tunnel("1", "qt.gtimg.cn", 443)
			if refusal != proxyforwarder.RefusalNone {
				t.Fatalf("refused: %s", refusal)
			}
			// A first record the broker accepts, so the tunnel is in its splice.
			if _, err := conn.Write(helloFor(t, "qt.gtimg.cn")); err != nil {
				t.Fatal(err)
			}
			f.open(tc.next(t))
			closed := tunnelClosed(conn, 700*time.Millisecond)
			if closed == tc.stays {
				t.Fatalf("tunnel closed=%v after the renewal, want stays=%v", closed, tc.stays)
			}
		})
	}
}

// helloFor is a ClientHello a real TLS client produces for host, read off a pipe.
func helloFor(t *testing.T, host string) []byte {
	t.Helper()
	client, server := net.Pipe()
	defer server.Close()
	go func() {
		c := tls.Client(client, &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12})
		_ = client.SetDeadline(time.Now().Add(time.Second))
		_ = c.Handshake()
		client.Close()
	}()
	header := make([]byte, 5)
	_ = server.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := io.ReadFull(server, header); err != nil {
		t.Fatal(err)
	}
	body := make([]byte, int(header[3])<<8|int(header[4]))
	if _, err := io.ReadFull(server, body); err != nil {
		t.Fatal(err)
	}
	return append(header, body...)
}

func TestProxyTunnelsCloseWhenTheRunnerShutsDown(t *testing.T) {
	f := newEgrFixture(t)
	f.proxySeams()
	f.open(proxyOptions(t, proxyAndHTTP, "qt.gtimg.cn"))
	conn, refusal := f.tunnel("1", "qt.gtimg.cn", 443)
	if refusal != proxyforwarder.RefusalNone {
		t.Fatalf("refused: %s", refusal)
	}
	if _, err := conn.Write(helloFor(t, "qt.gtimg.cn")); err != nil {
		t.Fatal(err)
	}
	f.registry.close()
	if !tunnelClosed(conn, 5*time.Second) {
		t.Fatal("the tunnel outlived the runner's shutdown")
	}
}

func TestProxyTunnelsCloseWhenTheSessionExpires(t *testing.T) {
	f := newEgrFixture(t)
	f.proxySeams()
	f.open(proxyOptions(t, proxyAndHTTP, "qt.gtimg.cn"))
	conn, refusal := f.tunnel("1", "qt.gtimg.cn", 443)
	if refusal != proxyforwarder.RefusalNone {
		t.Fatalf("refused: %s", refusal)
	}
	if _, err := conn.Write(helloFor(t, "qt.gtimg.cn")); err != nil {
		t.Fatal(err)
	}
	start := f.registry.now()
	f.registry.now = func() time.Time { return start.Add(10 * time.Minute) }
	if !tunnelClosed(conn, 5*time.Second) {
		t.Fatal("the tunnel outlived its session's expiry")
	}
}

func TestProxyReportsReachPortal(t *testing.T) {
	f := newEgrFixture(t)
	f.proxySeams()
	p := f.open(proxyOptions(t, proxyAndHTTP, "qt.gtimg.cn"))
	for i := 0; i < 3; i++ {
		c, refusal := f.tunnel("1", "evil.example.com", 443)
		c.Close()
		if refusal != proxyforwarder.RefusalHostNotReviewed {
			t.Fatalf("%q", refusal)
		}
	}
	select {
	case report := <-p.reports:
		if report.Kind != "refusal" || report.Host != "evil.example.com" || report.Code != "host_not_reviewed" || report.Count != 3 {
			t.Fatalf("report: %+v", report)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no refusal report reached Portal")
	}
}

// The Portal-side reference forwarder against the runner's real broker: an HTTPS client that only knows
// HTTPS_PROXY reaches a reviewed host through CONNECT, the broker's tunnel and a TLS server, and a host that
// was not reviewed is a 403 with the refusal code.
func TestProxyForwarderEndToEndAgainstTheBroker(t *testing.T) {
	f := newEgrFixture(t)
	server := f.proxySeams()
	f.open(proxyOptions(t, proxyAndHTTP, "qt.gtimg.cn"))
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- proxyforwarder.Serve(ctx, listener, func(context.Context) (net.Conn, error) { return f.listener("1").dial() })
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	proxyURL, _ := url.Parse("http://" + listener.Addr().String())
	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())
	client := &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{
		Proxy: http.ProxyURL(proxyURL),
		// The test server certificate does not name the host; the point here is the path. The SNI the client sends is the CONNECT host.
		TLSClientConfig: &tls.Config{RootCAs: roots, InsecureSkipVerify: true, MinVersion: tls.VersionTLS12},
	}}
	for i := 0; i < 3; i++ {
		resp, err := client.Get("https://qt.gtimg.cn/q=sh000001")
		if err != nil {
			t.Fatalf("request %d: %v", i, err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 200 || !strings.Contains(string(body), "hello from upstream") {
			t.Fatalf("request %d: %d %q", i, resp.StatusCode, body)
		}
	}
	// An unreviewed host is refused by the broker and the forwarder says why.
	raw, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	_ = raw.SetDeadline(time.Now().Add(5 * time.Second))
	_, _ = io.WriteString(raw, "CONNECT evil.example.com:443 HTTP/1.1\r\nHost: evil.example.com:443\r\n\r\n")
	reply, _ := io.ReadAll(raw)
	if !strings.HasPrefix(string(reply), "HTTP/1.1 403") || !strings.Contains(string(reply), "X-Portal-Proxy-Refusal: host_not_reviewed") {
		t.Fatalf("refusal: %q", reply)
	}
	// Plain HTTP is not proxied at all: the forwarder only speaks CONNECT.
	plain := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL)}}
	if resp, err := plain.Get("http://qt.gtimg.cn/q=sh000001"); err == nil {
		resp.Body.Close()
		if resp.StatusCode != http.StatusMethodNotAllowed {
			t.Fatalf("plain HTTP through the proxy: %d", resp.StatusCode)
		}
	}
}

// A burst of refusals to many hosts must not be sent at once: Portal holds each report's call until it has
// written its audit row, and a session has a bounded number of calls open, so the runner paces them and the
// session survives.
func TestProxyReportBurstIsPacedAndTheSessionSurvives(t *testing.T) {
	f := newEgrFixture(t)
	f.proxySeams()
	p := f.open(proxyOptions(t, proxyAndHTTP, "qt.gtimg.cn"))
	const hosts = 40
	for i := 0; i < hosts; i++ {
		c, refusal := f.tunnel("1", fmt.Sprintf("h%d.example.com", i), 443)
		c.Close()
		if refusal != proxyforwarder.RefusalHostNotReviewed {
			t.Fatalf("%q", refusal)
		}
	}
	seen := map[string]bool{}
	deadline := time.After(30 * time.Second)
	for len(seen) < hosts {
		select {
		case report := <-p.reports:
			seen[report.Host] = true
		case <-p.mux.Done():
			t.Fatal("the session ended during a burst of reports")
		case <-deadline:
			t.Fatalf("only %d of %d refusal reports arrived", len(seen), hosts)
		}
	}
}

// storeOfBudget is the journal's budget table in memory: what survives a registry.
type storeOfBudget struct {
	mu   sync.Mutex
	rows map[string]int64
}

func (s *storeOfBudget) key(id string, day int64) string { return fmt.Sprintf("%s/%d", id, day) }
func (s *storeOfBudget) ProxyBudgetUsed(_ context.Context, id string, day int64) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.rows[s.key(id, day)], nil
}
func (s *storeOfBudget) AddProxyBudget(_ context.Context, id string, day, n int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rows[s.key(id, day)] += n
	return nil
}
func (s *storeOfBudget) PruneProxyBudget(context.Context, int64) error { return nil }

// A spent budget is still spent after the runner restarts: a new registry over the same store refuses tunnels.
func TestProxyBudgetSurvivesARunnerRestart(t *testing.T) {
	store := &storeOfBudget{rows: map[string]int64{}}
	first := newEgrFixture(t)
	first.registry.useProxyBudget(store)
	first.proxySeams()
	first.open(proxyOptions(t, proxyAndHTTP, "qt.gtimg.cn"))
	c, refusal := first.tunnel("1", "qt.gtimg.cn", 443)
	c.Close()
	if refusal != proxyforwarder.RefusalNone {
		t.Fatalf("before the budget is spent: %q", refusal)
	}
	day := time.Now().UTC().Unix() / 86400
	first.registry.budget.Add(first.installation, day, appplatform.ProxyDailyBytesMax)
	if c, refusal := first.tunnel("1", "qt.gtimg.cn", 443); refusal != proxyforwarder.RefusalBudgetSpent {
		c.Close()
		t.Fatalf("a spent budget: %q", refusal)
	}
	first.registry.close() // the runner shuts down; the budget is flushed
	second := newEgrFixture(t)
	second.registry.useProxyBudget(store)
	second.proxySeams()
	second.open(proxyOptions(t, proxyAndHTTP, "qt.gtimg.cn"))
	c, refusal = second.tunnel("1", "qt.gtimg.cn", 443)
	c.Close()
	if refusal != proxyforwarder.RefusalBudgetSpent {
		t.Fatalf("after a restart the day's budget was given back: %q", refusal)
	}
}

func TestEgressProxyReportCodec(t *testing.T) {
	good := []string{
		`{"kind":"refusal","host":"evil.example.com","code":"host_not_reviewed","count":3}`,
		`{"kind":"refusal","host":"-","code":"sni_mismatch","count":1}`,
		`{"kind":"usage","day":"2026-11-19","bytes":1234,"budget":2147483648}`,
		`{"kind":"rollup","hour":"2026-11-19T10:00:00Z","hosts":[{"host":"qt.gtimg.cn","tunnels":2,"bytes_in":10,"bytes_out":5,"closes":{"eof":2}}]}`,
	}
	for _, raw := range good {
		report, err := DecodeEgressProxyReport([]byte(raw))
		if err != nil {
			t.Fatalf("%s: %v", raw, err)
		}
		if again, err := EncodeEgressProxyReport(report); err != nil || string(again) != raw {
			t.Fatalf("%s did not round trip: %s %v", raw, again, err)
		}
	}
	for _, raw := range []string{
		``, `{}`, `{"kind":"other"}`,
		`{"kind":"refusal","host":"Evil.example.com","code":"host_not_reviewed","count":3}`,
		`{"kind":"refusal","host":"evil.example.com","code":"made_up","count":3}`,
		`{"kind":"refusal","host":"evil.example.com","code":"host_not_reviewed","count":0}`,
		`{"kind":"refusal","host":"*","code":"busy","count":1}`,
		`{"kind":"refusal","host":"a b","code":"busy","count":1}`,
		`{"kind":"usage","day":"2026-13-19","bytes":1,"budget":2147483648}`,
		`{"kind":"usage","day":"2026-11-19","bytes":1,"budget":2147483649}`,
		`{"kind":"rollup","hour":"2026-11-19T10:30:00Z","hosts":[{"host":"qt.gtimg.cn","tunnels":1,"bytes_in":0,"bytes_out":0,"closes":{}}]}`,
		`{"kind":"rollup","hour":"2026-11-19T10:00:00+08:00","hosts":[{"host":"qt.gtimg.cn","tunnels":1,"bytes_in":0,"bytes_out":0,"closes":{}}]}`,
		`{"kind":"rollup","hour":"2026-11-19T10:00:00Z","hosts":[]}`,
		`{"kind":"rollup","hour":"2026-11-19T10:00:00Z","hosts":[{"host":"b.example.com","tunnels":1,"bytes_in":0,"bytes_out":0,"closes":{}},{"host":"a.example.com","tunnels":1,"bytes_in":0,"bytes_out":0,"closes":{}}]}`,
		`{"kind":"rollup","hour":"2026-11-19T10:00:00Z","hosts":[{"host":"a.example.com","tunnels":-1,"bytes_in":0,"bytes_out":0,"closes":{}}]}`,
		`{"kind":"rollup","hour":"2026-11-19T10:00:00Z","hosts":[{"host":"a.example.com","tunnels":1,"bytes_in":0,"bytes_out":0,"closes":{"mystery":1}}]}`,
		`{"kind":"usage","day":"2026-11-19","bytes":1,"budget":2147483648,"extra":1}`,
		` {"kind":"usage","day":"2026-11-19","bytes":1,"budget":2147483648}`,
	} {
		if _, err := DecodeEgressProxyReport([]byte(raw)); err == nil {
			t.Errorf("%q was accepted", raw)
		}
	}
}
