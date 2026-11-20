package broker

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform/proxyforwarder"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/hostnet"
)

// proxyEnv is one installation's proxy against a local upstream. Lookup answers
// from a table, and every dial lands on the local upstream whatever address was
// asked for, which is recorded.
type proxyEnv struct {
	t       *testing.T
	runner  *ProxyRunner
	in      *ProxyInstall
	budget  ProxyBudget
	clock   *proxyClock
	tls     *httptest.Server
	raw     net.Listener
	useRaw  bool
	rawGot  atomic.Int64
	lookups atomic.Int32
	mu      sync.Mutex
	dialed  []string
	answers map[string][]netip.Addr
	usable  atomic.Bool
}

type proxyClock struct {
	mu  sync.Mutex
	now time.Time
	// live makes the clock follow the real one, as the timers need.
	live bool
}

func (c *proxyClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.live {
		return time.Now()
	}
	return c.now
}

func (c *proxyClock) advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

var publicAnswer = []netip.Addr{netip.MustParseAddr("93.184.216.34")}

func newProxyEnv(t *testing.T, tune func(*ProxyLimits)) *proxyEnv {
	t.Helper()
	e := &proxyEnv{t: t, answers: map[string][]netip.Addr{}, clock: &proxyClock{now: time.Date(2026, 11, 19, 10, 0, 0, 0, time.UTC)}}
	e.usable.Store(true)
	e.tls = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "hello from upstream")
	}))
	t.Cleanup(e.tls.Close)
	raw, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	e.raw = raw
	t.Cleanup(func() { raw.Close() })
	go func() {
		for {
			c, err := raw.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				n, _ := io.Copy(io.Discard, c)
				e.rawGot.Add(n)
			}()
		}
	}()
	e.budget = NewMemoryProxyBudget()
	e.runner = NewProxyRunner(64, e.budget)
	e.runner.Now = e.clock.Now
	e.runner.Interfaces = func() ([]hostnet.Interface, error) { return nil, nil }
	e.runner.Lookup = func(ctx context.Context, host string) ([]netip.Addr, error) {
		e.lookups.Add(1)
		e.mu.Lock()
		defer e.mu.Unlock()
		if a, ok := e.answers[host]; ok {
			return a, nil
		}
		return publicAnswer, nil
	}
	e.runner.Dial = func(ctx context.Context, network, address string) (net.Conn, error) {
		e.mu.Lock()
		e.dialed = append(e.dialed, address)
		useRaw := e.useRaw
		e.mu.Unlock()
		target := e.tls.Listener.Addr().String()
		if useRaw {
			target = e.raw.Addr().String()
		}
		var d net.Dialer
		return d.DialContext(ctx, "tcp", target)
	}
	limits := DefaultProxyLimits()
	if tune != nil {
		tune(&limits)
	}
	e.in = NewProxyInstall("10000000-0000-4000-8000-000000000001", e.runner, limits)
	return e
}

func (e *proxyEnv) apply(daily int64, hosts ...string) appplatform.ParsedProxyPolicy {
	e.t.Helper()
	raw, err := appplatform.CanonicalProxyPolicy(appplatform.ProxyPolicy{Hosts: hosts, DailyBytes: daily})
	if err != nil {
		e.t.Fatal(err)
	}
	parsed, err := appplatform.ParseProxyPolicy(raw)
	if err != nil {
		e.t.Fatal(err)
	}
	e.in.Apply(parsed)
	return parsed
}

func (e *proxyEnv) setRaw(on bool) {
	e.mu.Lock()
	e.useRaw = on
	e.mu.Unlock()
}

func (e *proxyEnv) dialedAddresses() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.dialed...)
}

// tcpPair returns a connected loopback pair.
func tcpPair(t *testing.T) (client, server net.Conn) {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	accepted := make(chan net.Conn, 1)
	go func() {
		c, _ := l.Accept()
		accepted <- c
	}()
	client, err = net.Dial("tcp", l.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	server = <-accepted
	t.Cleanup(func() { client.Close(); server.Close() })
	return client, server
}

// open sends an open frame and returns the application end and the broker's answer.
func (e *proxyEnv) open(host string, port int) (net.Conn, proxyforwarder.Refusal, <-chan struct{}) {
	e.t.Helper()
	client, server := tcpPair(e.t)
	done := make(chan struct{})
	go func() {
		defer close(done)
		e.in.Serve(server, bufio.NewReader(server), e.usable.Load)
	}()
	frame, err := proxyforwarder.EncodeOpen(host, port)
	if err != nil {
		e.t.Fatal(err)
	}
	if _, err := client.Write(frame); err != nil {
		e.t.Fatal(err)
	}
	_ = client.SetReadDeadline(time.Now().Add(5 * time.Second))
	refusal, err := proxyforwarder.ReadReply(client)
	if err != nil {
		e.t.Fatalf("reading the broker's answer for %s:%d: %v", host, port, err)
	}
	_ = client.SetReadDeadline(time.Time{})
	return client, refusal, done
}

// refusalOf opens and returns only the refusal, closing the connection.
func (e *proxyEnv) refusalOf(host string, port int) proxyforwarder.Refusal {
	e.t.Helper()
	c, refusal, done := e.open(host, port)
	c.Close()
	<-done
	return refusal
}

func waitClosed(t *testing.T, c net.Conn, within time.Duration) {
	t.Helper()
	_ = c.SetReadDeadline(time.Now().Add(within))
	if _, err := io.Copy(io.Discard, c); err != nil {
		var ne net.Error
		if errors.As(err, &ne) && ne.Timeout() {
			t.Fatal("the tunnel was not closed in time")
		}
	}
}

func (e *proxyEnv) get(conn net.Conn, host string) string {
	e.t.Helper()
	tlsConn := tls.Client(conn, &tls.Config{ServerName: host, InsecureSkipVerify: true, MinVersion: tls.VersionTLS12})
	_ = tlsConn.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := io.WriteString(tlsConn, "GET / HTTP/1.1\r\nHost: "+host+"\r\nConnection: close\r\n\r\n"); err != nil {
		e.t.Fatalf("write through the tunnel: %v", err)
	}
	body, _ := io.ReadAll(tlsConn)
	conn.Close()
	return string(body)
}

func TestProxyExactHostConnectsEndToEnd(t *testing.T) {
	e := newProxyEnv(t, nil)
	e.apply(appplatform.ProxyDailyBytesMax, "qt.gtimg.cn")
	conn, refusal, done := e.open("qt.gtimg.cn", 443)
	if refusal != proxyforwarder.RefusalNone {
		t.Fatalf("refused: %s", refusal)
	}
	if body := e.get(conn, "qt.gtimg.cn"); !strings.Contains(body, "hello from upstream") {
		t.Fatalf("the response did not come back through the tunnel: %q", body)
	}
	<-done
	if dialed := e.dialedAddresses(); len(dialed) != 1 || dialed[0] != "93.184.216.34:443" {
		t.Fatalf("dialled %v, want the one pinned address", dialed)
	}
}

func TestProxyWildcardMatchesOneLabelOnly(t *testing.T) {
	e := newProxyEnv(t, nil)
	e.apply(appplatform.ProxyDailyBytesMax, "*.eastmoney.com")
	if got := e.refusalOf("push2.eastmoney.com", 443); got != proxyforwarder.RefusalNone {
		t.Fatalf("one label: %q", got)
	}
	for _, host := range []string{"82.push2.eastmoney.com", "eastmoney.com", "evil.com"} {
		if got := e.refusalOf(host, 443); got != proxyforwarder.RefusalHostNotReviewed {
			t.Errorf("%s: %q", host, got)
		}
	}
	e.apply(appplatform.ProxyDailyBytesMax, "*.eastmoney.com", "*.push2.eastmoney.com")
	if got := e.refusalOf("82.push2.eastmoney.com", 443); got != proxyforwarder.RefusalNone {
		t.Fatalf("a second wildcard entry: %q", got)
	}
}

func TestProxyUnreviewedHostIsRefusedBeforeResolution(t *testing.T) {
	e := newProxyEnv(t, nil)
	e.apply(appplatform.ProxyDailyBytesMax, "qt.gtimg.cn")
	for _, host := range []string{"evil.example.com", "1.2.3.4", "[::1]", "::1", "QT.gtimg.cn", "qt.gtimg.cn."} {
		if got := e.refusalOf(host, 443); got != proxyforwarder.RefusalHostNotReviewed {
			t.Errorf("%s: %q", host, got)
		}
	}
	if n := e.lookups.Load(); n != 0 {
		t.Fatalf("%d lookups happened for refused hosts", n)
	}
	if len(e.dialedAddresses()) != 0 {
		t.Fatal("a refused host was dialled")
	}
}

func TestProxyOnlyPort443(t *testing.T) {
	e := newProxyEnv(t, nil)
	e.apply(appplatform.ProxyDailyBytesMax, "qt.gtimg.cn")
	for _, port := range []int{80, 8443, 22, 1} {
		if got := e.refusalOf("qt.gtimg.cn", port); got != proxyforwarder.RefusalPortNotPermitted {
			t.Errorf("port %d: %q", port, got)
		}
	}
	if e.lookups.Load() != 0 {
		t.Fatal("a refused port caused a lookup")
	}
}

func TestProxyAddressGuardJudgesEveryAnswer(t *testing.T) {
	e := newProxyEnv(t, nil)
	e.apply(appplatform.ProxyDailyBytesMax, "*.example.com")
	for name, answer := range map[string]string{
		"loopback": "127.0.0.1", "link local": "169.254.1.1", "metadata": "169.254.169.254", "alibaba metadata": "100.100.100.200",
		"private 10": "10.1.2.3", "private 192": "192.168.1.1", "private 172": "172.20.0.1", "cgnat": "100.64.0.1",
		"unspecified": "0.0.0.0", "multicast": "224.0.0.1", "ipv6 loopback": "::1", "ipv6 ula": "fd00::1", "ipv6 link local": "fe80::1",
		"mapped loopback": "::ffff:127.0.0.1",
	} {
		e.mu.Lock()
		e.answers["a.example.com"] = []netip.Addr{netip.MustParseAddr(answer)}
		e.mu.Unlock()
		if got := e.refusalOf("a.example.com", 443); got != proxyforwarder.RefusalAddressDenied {
			t.Errorf("%s (%s): %q", name, answer, got)
		}
	}
	if len(e.dialedAddresses()) != 0 {
		t.Fatalf("a denied address was dialled: %v", e.dialedAddresses())
	}
}

func TestProxyMixedAnswerDialsOnlyTheAllowedAddress(t *testing.T) {
	e := newProxyEnv(t, nil)
	e.apply(appplatform.ProxyDailyBytesMax, "qt.gtimg.cn")
	e.mu.Lock()
	e.answers["qt.gtimg.cn"] = []netip.Addr{netip.MustParseAddr("10.0.0.5"), netip.MustParseAddr("127.0.0.1"), netip.MustParseAddr("93.184.216.34"), netip.MustParseAddr("8.8.8.8")}
	e.mu.Unlock()
	conn, refusal, done := e.open("qt.gtimg.cn", 443)
	if refusal != proxyforwarder.RefusalNone {
		t.Fatalf("refused: %s", refusal)
	}
	conn.Close()
	<-done
	if dialed := e.dialedAddresses(); len(dialed) != 1 || dialed[0] != "93.184.216.34:443" {
		t.Fatalf("dialled %v, want only the first allowed address", dialed)
	}
}

func TestProxyHostInterfaceAddressesAreDenied(t *testing.T) {
	e := newProxyEnv(t, nil)
	e.apply(appplatform.ProxyDailyBytesMax, "qt.gtimg.cn")
	// An address that is one of the host's own is never dialled, even when it is public.
	e.runner.Interfaces = func() ([]hostnet.Interface, error) {
		return []hostnet.Interface{{Name: "eth0", Addrs: []netip.Prefix{netip.MustParsePrefix("93.184.216.34/24")}}}, nil
	}
	if got := e.refusalOf("qt.gtimg.cn", 443); got != proxyforwarder.RefusalAddressDenied {
		t.Fatalf("a host interface address: %q", got)
	}
	// A neighbour in the same subnet is fine on a plain interface, and denied inside a bridge's subnet.
	e.mu.Lock()
	e.answers["qt.gtimg.cn"] = []netip.Addr{netip.MustParseAddr("93.184.216.99")}
	e.mu.Unlock()
	if got := e.refusalOf("qt.gtimg.cn", 443); got != proxyforwarder.RefusalNone {
		t.Fatalf("a neighbour of a plain interface: %q", got)
	}
	e.runner.Interfaces = func() ([]hostnet.Interface, error) {
		return []hostnet.Interface{{Name: "virbr0", Addrs: []netip.Prefix{netip.MustParsePrefix("93.184.216.1/24")}}}, nil
	}
	if got := e.refusalOf("qt.gtimg.cn", 443); got != proxyforwarder.RefusalAddressDenied {
		t.Fatalf("an address inside a bridge subnet: %q", got)
	}
	e.runner.Interfaces = func() ([]hostnet.Interface, error) { return nil, errors.New("no interfaces") }
	if got := e.refusalOf("qt.gtimg.cn", 443); got != proxyforwarder.RefusalAddressDenied {
		t.Fatalf("an unreadable interface list must deny: %q", got)
	}
}

func TestProxyResolveAndConnectFailures(t *testing.T) {
	e := newProxyEnv(t, nil)
	e.apply(appplatform.ProxyDailyBytesMax, "qt.gtimg.cn")
	e.runner.Lookup = func(context.Context, string) ([]netip.Addr, error) { return nil, errors.New("nxdomain") }
	if got := e.refusalOf("qt.gtimg.cn", 443); got != proxyforwarder.RefusalResolveFailed {
		t.Fatalf("resolve: %q", got)
	}
	e.runner.Lookup = func(context.Context, string) ([]netip.Addr, error) { return nil, nil }
	if got := e.refusalOf("qt.gtimg.cn", 443); got != proxyforwarder.RefusalResolveFailed {
		t.Fatalf("empty answer: %q", got)
	}
	e.runner.Lookup = func(context.Context, string) ([]netip.Addr, error) { return publicAnswer, nil }
	e.runner.Dial = func(context.Context, string, string) (net.Conn, error) { return nil, errors.New("refused") }
	if got := e.refusalOf("qt.gtimg.cn", 443); got != proxyforwarder.RefusalConnectFailed {
		t.Fatalf("connect: %q", got)
	}
}

func TestProxyNotGrantedWithoutAuthorityOrSession(t *testing.T) {
	e := newProxyEnv(t, nil)
	if got := e.refusalOf("qt.gtimg.cn", 443); got != proxyforwarder.RefusalNotGranted {
		t.Fatalf("no policy: %q", got)
	}
	e.apply(appplatform.ProxyDailyBytesMax, "qt.gtimg.cn")
	e.usable.Store(false)
	if got := e.refusalOf("qt.gtimg.cn", 443); got != proxyforwarder.RefusalNotGranted {
		t.Fatalf("no current session: %q", got)
	}
	e.usable.Store(true)
	e.in.Revoke()
	if got := e.refusalOf("qt.gtimg.cn", 443); got != proxyforwarder.RefusalNotGranted {
		t.Fatalf("revoked: %q", got)
	}
}

func TestProxyFirstRecordMustBeAClientHelloForTheConnectHost(t *testing.T) {
	cases := map[string][]byte{
		"different outer name": buildHello(helloOptions{sni: "evil.example.net"}),
		"missing name":         buildHello(helloOptions{}),
		"plain http":           []byte("GET / HTTP/1.1\r\nHost: qt.gtimg.cn\r\n\r\n"),
		"real ech shape":       buildHello(helloOptions{sni: "cover.example.net", extras: [][]byte{greaseECH()}}),
		"trailing dot name":    buildHello(helloOptions{sni: "qt.gtimg.cn."}),
		"subdomain name":       buildHello(helloOptions{sni: "x.qt.gtimg.cn"}),
	}
	for name, first := range cases {
		t.Run(name, func(t *testing.T) {
			e := newProxyEnv(t, nil)
			e.apply(appplatform.ProxyDailyBytesMax, "qt.gtimg.cn")
			e.setRaw(true)
			conn, refusal, done := e.open("qt.gtimg.cn", 443)
			if refusal != proxyforwarder.RefusalNone {
				t.Fatalf("refused: %s", refusal)
			}
			_, _ = conn.Write(first)
			waitClosed(t, conn, 5*time.Second)
			<-done
			if got := e.rawGot.Load(); got != 0 {
				t.Fatalf("%d bytes reached the dialled host", got)
			}
			e.in.mu.Lock()
			var mismatches int64
			for _, hosts := range e.in.stats {
				for _, s := range hosts {
					mismatches += s.closes[ProxyCloseSNIMismatch]
				}
			}
			e.in.mu.Unlock()
			if mismatches != 1 {
				t.Fatalf("sni_mismatch counted %d times", mismatches)
			}
		})
	}
}

func TestProxyGreaseECHWithTheRightOuterNamePasses(t *testing.T) {
	e := newProxyEnv(t, nil)
	e.apply(appplatform.ProxyDailyBytesMax, "qt.gtimg.cn")
	e.setRaw(true)
	hello := buildHello(helloOptions{sni: "qt.gtimg.cn", extras: [][]byte{greaseECH()}})
	conn, refusal, _ := e.open("qt.gtimg.cn", 443)
	if refusal != proxyforwarder.RefusalNone {
		t.Fatalf("refused: %s", refusal)
	}
	defer conn.Close()
	if _, err := conn.Write(hello); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for e.rawGot.Load() == 0 {
		// The raw upstream counts bytes when its connection ends; close ours to flush the count.
		if time.Now().After(deadline) {
			break
		}
		time.Sleep(10 * time.Millisecond)
		if e.rawGot.Load() == 0 {
			_ = conn.(*net.TCPConn).CloseWrite()
		}
	}
	if got := e.rawGot.Load(); got != int64(len(hello)) {
		t.Fatalf("upstream received %d bytes, want the whole %d byte hello", got, len(hello))
	}
}

func TestProxyFirstRecordTimeout(t *testing.T) {
	e := newProxyEnv(t, func(l *ProxyLimits) { l.FirstRecord = 100 * time.Millisecond })
	e.clock.live = true
	e.apply(appplatform.ProxyDailyBytesMax, "qt.gtimg.cn")
	e.setRaw(true)
	conn, refusal, done := e.open("qt.gtimg.cn", 443)
	if refusal != proxyforwarder.RefusalNone {
		t.Fatalf("refused: %s", refusal)
	}
	waitClosed(t, conn, 3*time.Second)
	<-done
	if e.rawGot.Load() != 0 {
		t.Fatal("bytes reached the upstream")
	}
}

func TestProxyOpenTunnelCap(t *testing.T) {
	e := newProxyEnv(t, nil)
	e.apply(appplatform.ProxyDailyBytesMax, "qt.gtimg.cn")
	e.setRaw(true)
	var held []net.Conn
	for i := 0; i < 16; i++ {
		conn, refusal, _ := e.open("qt.gtimg.cn", 443)
		if refusal != proxyforwarder.RefusalNone {
			t.Fatalf("tunnel %d refused: %s", i+1, refusal)
		}
		held = append(held, conn)
	}
	if got := e.refusalOf("qt.gtimg.cn", 443); got != proxyforwarder.RefusalBusy {
		t.Fatalf("the 17th open tunnel: %q", got)
	}
	// Closing one frees a slot.
	held[0].Close()
	deadline := time.Now().Add(5 * time.Second)
	for e.in.Open() >= 16 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if got := e.refusalOf("qt.gtimg.cn", 443); got != proxyforwarder.RefusalNone {
		t.Fatalf("after a close: %q", got)
	}
	for _, c := range held[1:] {
		c.Close()
	}
}

func TestProxyNewTunnelBurstAndRefill(t *testing.T) {
	e := newProxyEnv(t, nil)
	e.apply(appplatform.ProxyDailyBytesMax, "qt.gtimg.cn")
	for i := 0; i < 50; i++ {
		if got := e.refusalOf("qt.gtimg.cn", 443); got != proxyforwarder.RefusalNone {
			t.Fatalf("open %d of the burst: %q", i+1, got)
		}
	}
	if got := e.refusalOf("qt.gtimg.cn", 443); got != proxyforwarder.RefusalBusy {
		t.Fatalf("the 51st burst open: %q", got)
	}
	e.clock.advance(time.Second) // 300 a minute is five a second
	if got := e.refusalOf("qt.gtimg.cn", 443); got != proxyforwarder.RefusalNone {
		t.Fatalf("after a refill: %q", got)
	}
}

func TestProxyRunnerWideCap(t *testing.T) {
	e := newProxyEnv(t, nil)
	e.runner.max = 2
	e.apply(appplatform.ProxyDailyBytesMax, "qt.gtimg.cn")
	e.setRaw(true)
	a, r1, _ := e.open("qt.gtimg.cn", 443)
	b, r2, _ := e.open("qt.gtimg.cn", 443)
	if r1 != proxyforwarder.RefusalNone || r2 != proxyforwarder.RefusalNone {
		t.Fatalf("%q %q", r1, r2)
	}
	if got := e.refusalOf("qt.gtimg.cn", 443); got != proxyforwarder.RefusalBusy {
		t.Fatalf("beyond the runner cap: %q", got)
	}
	// The cap is shared: another installation meets it too.
	other := NewProxyInstall("20000000-0000-4000-8000-000000000002", e.runner, DefaultProxyLimits())
	parsed := e.apply(appplatform.ProxyDailyBytesMax, "qt.gtimg.cn")
	other.Apply(parsed)
	client, server := tcpPair(t)
	go other.Serve(server, bufio.NewReader(server), nil)
	frame, _ := proxyforwarder.EncodeOpen("qt.gtimg.cn", 443)
	_, _ = client.Write(frame)
	_ = client.SetReadDeadline(time.Now().Add(5 * time.Second))
	if got, err := proxyforwarder.ReadReply(client); err != nil || got != proxyforwarder.RefusalBusy {
		t.Fatalf("other installation: %q %v", got, err)
	}
	a.Close()
	b.Close()
}

func TestProxyIdleAndLifetimeTimersCloseTheTunnel(t *testing.T) {
	for name, tune := range map[string]func(*ProxyLimits){
		"idle":     func(l *ProxyLimits) { l.Idle = 300 * time.Millisecond },
		"lifetime": func(l *ProxyLimits) { l.Lifetime = 400 * time.Millisecond },
	} {
		t.Run(name, func(t *testing.T) {
			e := newProxyEnv(t, tune)
			e.clock.live = true
			e.apply(appplatform.ProxyDailyBytesMax, "qt.gtimg.cn")
			e.setRaw(true)
			conn, refusal, done := e.open("qt.gtimg.cn", 443)
			if refusal != proxyforwarder.RefusalNone {
				t.Fatalf("refused: %s", refusal)
			}
			if _, err := conn.Write(buildHello(helloOptions{sni: "qt.gtimg.cn"})); err != nil {
				t.Fatal(err)
			}
			if name == "lifetime" {
				// Traffic keeps the tunnel from going idle; only the lifetime may end it.
				stop := make(chan struct{})
				defer close(stop)
				go func() {
					for {
						select {
						case <-stop:
							return
						case <-time.After(50 * time.Millisecond):
							_, _ = conn.Write([]byte("x"))
						}
					}
				}()
			}
			waitClosed(t, conn, 5*time.Second)
			<-done
			e.in.mu.Lock()
			defer e.in.mu.Unlock()
			var got int64
			for _, hosts := range e.in.stats {
				for _, s := range hosts {
					got += s.closes[name]
				}
			}
			if got != 1 {
				t.Fatalf("close reason %q counted %d times: %+v", name, got, e.in.stats)
			}
		})
	}
}

func TestProxyBudgetClosesMidStreamAndRefusesUntilTheUTCDayChanges(t *testing.T) {
	e := newProxyEnv(t, nil)
	const daily = int64(1) << 20
	e.apply(daily, "qt.gtimg.cn")
	e.setRaw(true)
	conn, refusal, done := e.open("qt.gtimg.cn", 443)
	if refusal != proxyforwarder.RefusalNone {
		t.Fatalf("refused: %s", refusal)
	}
	if _, err := conn.Write(buildHello(helloOptions{sni: "qt.gtimg.cn"})); err != nil {
		t.Fatal(err)
	}
	chunk := make([]byte, 64*1024)
	go func() {
		for i := 0; i < 64; i++ {
			if _, err := conn.Write(chunk); err != nil {
				return
			}
		}
	}()
	waitClosed(t, conn, 5*time.Second)
	<-done
	if got := e.refusalOf("qt.gtimg.cn", 443); got != proxyforwarder.RefusalBudgetSpent {
		t.Fatalf("a spent budget: %q", got)
	}
	e.clock.advance(14 * time.Hour) // 10:00 UTC to the next day
	if got := e.refusalOf("qt.gtimg.cn", 443); got != proxyforwarder.RefusalNone {
		t.Fatalf("the next UTC day: %q", got)
	}
}

// A lowered reviewed budget is what counts, not the platform's.
func TestProxyReviewedLowerBudgetApplies(t *testing.T) {
	e := newProxyEnv(t, nil)
	e.apply(1<<20, "qt.gtimg.cn")
	e.budget.Add(e.in.id, e.in.day(), 1<<20)
	if got := e.refusalOf("qt.gtimg.cn", 443); got != proxyforwarder.RefusalBudgetSpent {
		t.Fatalf("%q", got)
	}
	e.apply(2<<20, "qt.gtimg.cn")
	if got := e.refusalOf("qt.gtimg.cn", 443); got != proxyforwarder.RefusalNone {
		t.Fatalf("a raised budget must apply to new tunnels: %q", got)
	}
}

func TestProxyRevokeAndPolicyChangeCutOpenTunnels(t *testing.T) {
	for name, cut := range map[string]func(*proxyEnv){
		"revoke":  func(e *proxyEnv) { e.in.Revoke() },
		"changed": func(e *proxyEnv) { e.apply(appplatform.ProxyDailyBytesMax, "qt.gtimg.cn", "x.example.com") },
	} {
		t.Run(name, func(t *testing.T) {
			e := newProxyEnv(t, nil)
			e.apply(appplatform.ProxyDailyBytesMax, "qt.gtimg.cn")
			e.setRaw(true)
			conn, refusal, done := e.open("qt.gtimg.cn", 443)
			if refusal != proxyforwarder.RefusalNone {
				t.Fatalf("refused: %s", refusal)
			}
			if _, err := conn.Write(buildHello(helloOptions{sni: "qt.gtimg.cn"})); err != nil {
				t.Fatal(err)
			}
			time.Sleep(50 * time.Millisecond)
			cut(e)
			waitClosed(t, conn, 5*time.Second)
			<-done
			if e.in.Open() != 0 || e.runner.Open() != 0 {
				t.Fatalf("slots leaked: %d %d", e.in.Open(), e.runner.Open())
			}
		})
	}
}

func TestProxySamePolicyKeepsOpenTunnels(t *testing.T) {
	e := newProxyEnv(t, nil)
	e.apply(appplatform.ProxyDailyBytesMax, "qt.gtimg.cn")
	e.setRaw(true)
	conn, refusal, _ := e.open("qt.gtimg.cn", 443)
	if refusal != proxyforwarder.RefusalNone {
		t.Fatalf("refused: %s", refusal)
	}
	defer conn.Close()
	if _, err := conn.Write(buildHello(helloOptions{sni: "qt.gtimg.cn"})); err != nil {
		t.Fatal(err)
	}
	e.apply(appplatform.ProxyDailyBytesMax, "qt.gtimg.cn") // a renewal carrying the same policy
	time.Sleep(100 * time.Millisecond)
	if e.in.Open() != 1 {
		t.Fatal("a renewal with the same policy cut the tunnel")
	}
}

func TestProxyThroughputCap(t *testing.T) {
	e := newProxyEnv(t, func(l *ProxyLimits) { l.Mbit = 8 }) // one MB a second
	e.clock.live = true
	e.apply(appplatform.ProxyDailyBytesMax, "qt.gtimg.cn")
	e.setRaw(true)
	conn, refusal, done := e.open("qt.gtimg.cn", 443)
	if refusal != proxyforwarder.RefusalNone {
		t.Fatalf("refused: %s", refusal)
	}
	if _, err := conn.Write(buildHello(helloOptions{sni: "qt.gtimg.cn"})); err != nil {
		t.Fatal(err)
	}
	payload := make([]byte, 1536*1024) // above the 200 KB burst: the rest is paced at 1 MB/s
	start := time.Now()
	if _, err := conn.Write(payload); err != nil {
		t.Fatal(err)
	}
	_ = conn.(*net.TCPConn).CloseWrite()
	waitClosed(t, conn, 20*time.Second)
	<-done
	if elapsed := time.Since(start); elapsed < 1200*time.Millisecond {
		t.Fatalf("1.5 MB passed in %v at a 1 MB/s cap", elapsed)
	}
}

func TestProxyHalfClosePropagates(t *testing.T) {
	e := newProxyEnv(t, nil)
	e.apply(appplatform.ProxyDailyBytesMax, "qt.gtimg.cn")
	conn, refusal, done := e.open("qt.gtimg.cn", 443)
	if refusal != proxyforwarder.RefusalNone {
		t.Fatalf("refused: %s", refusal)
	}
	// A TLS client that finishes its request and half-closes still reads the whole answer.
	tlsConn := tls.Client(conn, &tls.Config{ServerName: "qt.gtimg.cn", InsecureSkipVerify: true, MinVersion: tls.VersionTLS12})
	_ = tlsConn.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := io.WriteString(tlsConn, "GET / HTTP/1.1\r\nHost: qt.gtimg.cn\r\nConnection: close\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(tlsConn)
	conn.Close()
	if !strings.Contains(string(body), "hello from upstream") {
		t.Fatalf("%q", body)
	}
	<-done
}

func TestProxyReportsRollupRefusalsAndUsage(t *testing.T) {
	e := newProxyEnv(t, nil)
	e.apply(appplatform.ProxyDailyBytesMax, "qt.gtimg.cn")
	conn, refusal, done := e.open("qt.gtimg.cn", 443)
	if refusal != proxyforwarder.RefusalNone {
		t.Fatalf("refused: %s", refusal)
	}
	if body := e.get(conn, "qt.gtimg.cn"); !strings.Contains(body, "hello") {
		t.Fatalf("%q", body)
	}
	<-done
	for i := 0; i < 5; i++ {
		_ = e.refusalOf("evil.example.com", 443)
	}
	_ = e.refusalOf("not a host\x00", 443)
	// Nothing is due in the hour that is still open, except the first refusal row and usage.
	first := e.in.Reports(e.clock.Now())
	var refusals, rollups, usage int
	for _, r := range first {
		switch r.Kind {
		case ProxyReportRefusal:
			refusals++
		case ProxyReportRollup:
			rollups++
		case ProxyReportUsage:
			usage++
		}
	}
	if rollups != 0 || usage != 1 || refusals != 2 {
		t.Fatalf("first reports: %+v", first)
	}
	for _, r := range first {
		if r.Kind == ProxyReportRefusal && r.Host == "evil.example.com" && (r.Count != 5 || r.Code != "host_not_reviewed") {
			t.Fatalf("refusal row: %+v", r)
		}
		if r.Kind == ProxyReportRefusal && r.Host != "evil.example.com" && r.Host != ProxyUnknownHost {
			t.Fatalf("application text reached a report: %+v", r)
		}
	}
	// At most one row per host and code per minute.
	_ = e.refusalOf("evil.example.com", 443)
	if again := e.in.Reports(e.clock.Now().Add(30 * time.Second)); len(again) != 0 {
		t.Fatalf("a refusal was reported twice within a minute: %+v", again)
	}
	e.clock.advance(61 * time.Second)
	if later := e.in.Reports(e.clock.Now()); len(later) != 1 || later[0].Count != 1 {
		t.Fatalf("the coalesced refusal: %+v", later)
	}
	// The hour completes: one rollup with the tunnel and its bytes.
	e.clock.advance(time.Hour)
	var rollup *ProxyReport
	for _, r := range e.in.Reports(e.clock.Now()) {
		if r.Kind == ProxyReportRollup {
			r := r
			rollup = &r
		}
	}
	if rollup == nil || len(rollup.Hosts) != 1 || rollup.Hosts[0].Host != "qt.gtimg.cn" || rollup.Hosts[0].Tunnels != 1 ||
		rollup.Hosts[0].BytesIn == 0 || rollup.Hosts[0].BytesOut == 0 || rollup.Hosts[0].Closes[ProxyCloseEOF] != 1 {
		t.Fatalf("rollup: %+v", rollup)
	}
	if rollup.Hour != "2026-11-19T10:00:00Z" {
		t.Fatalf("rollup hour %q", rollup.Hour)
	}
}

// A stream that has not sent its open frame holds no tunnel slot, so it is capped on its own: an application
// that opens many connections and stalls cannot hold the runner's goroutines and descriptors.
func TestProxyPendingOpensAreCapped(t *testing.T) {
	e := newProxyEnv(t, nil)
	e.apply(appplatform.ProxyDailyBytesMax, "qt.gtimg.cn")
	var stalled []net.Conn
	for i := 0; i < ProxyMaxPendingOpens; i++ {
		client, server := tcpPair(t)
		go e.in.Serve(server, bufio.NewReader(server), e.usable.Load)
		// The magic and nothing more: the open frame never completes.
		if _, err := client.Write([]byte(ProxyStreamMagic)); err != nil {
			t.Fatal(err)
		}
		stalled = append(stalled, client)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		e.in.mu.Lock()
		pending := e.in.pending
		e.in.mu.Unlock()
		if pending == ProxyMaxPendingOpens {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("pending %d", pending)
		}
		time.Sleep(5 * time.Millisecond)
	}
	extra, server := tcpPair(t)
	served := make(chan struct{})
	go func() {
		defer close(served)
		e.in.Serve(server, bufio.NewReader(server), e.usable.Load)
	}()
	select {
	case <-served:
	case <-time.After(2 * time.Second):
		t.Fatal("a stream beyond the pending cap was not dropped at once")
	}
	waitClosed(t, extra, 2*time.Second)
	// A well-formed stream is served again once the stalled ones are gone.
	for _, c := range stalled {
		c.Close()
	}
	deadline = time.Now().Add(5 * time.Second)
	for {
		e.in.mu.Lock()
		pending := e.in.pending
		e.in.mu.Unlock()
		if pending == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("pending %d after the stalled streams closed", pending)
		}
		time.Sleep(5 * time.Millisecond)
	}
	if got := e.refusalOf("evil.example.com", 443); got != proxyforwarder.RefusalHostNotReviewed {
		t.Fatalf("after the stalled streams: %q", got)
	}
}

// The lifetime counts from admission: a tunnel stuck resolving is cut at the lifetime, not 10 s later.
func TestProxyLifetimeCountsFromAdmission(t *testing.T) {
	e := newProxyEnv(t, func(l *ProxyLimits) { l.Lifetime = 200 * time.Millisecond })
	e.clock.live = true
	e.apply(appplatform.ProxyDailyBytesMax, "qt.gtimg.cn")
	e.runner.Lookup = func(ctx context.Context, _ string) ([]netip.Addr, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	start := time.Now()
	client, server := tcpPair(t)
	done := make(chan struct{})
	go func() {
		defer close(done)
		e.in.Serve(server, bufio.NewReader(server), e.usable.Load)
	}()
	frame, _ := proxyforwarder.EncodeOpen("qt.gtimg.cn", 443)
	if _, err := client.Write(frame); err != nil {
		t.Fatal(err)
	}
	// A tunnel cut by its lifetime is closed; whether the refusal gets out before the close is a race, and
	// either is a closed tunnel to the forwarder.
	_ = client.SetReadDeadline(time.Now().Add(5 * time.Second))
	if refusal, err := proxyforwarder.ReadReply(client); err == nil && refusal != proxyforwarder.RefusalUnavailable {
		t.Fatalf("a resolve cut by the lifetime: %q", refusal)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the tunnel was not ended by its lifetime")
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("the lifetime did not bound the resolve: %v", elapsed)
	}
	if e.in.Open() != 0 || e.runner.Open() != 0 {
		t.Fatal("slots leaked")
	}
}

// A new generation of an installation takes over the admission bucket instead of starting with a fresh burst.
func TestProxyInstallInheritsTheBucket(t *testing.T) {
	e := newProxyEnv(t, nil)
	e.apply(appplatform.ProxyDailyBytesMax, "qt.gtimg.cn")
	for i := 0; i < 50; i++ {
		if got := e.refusalOf("qt.gtimg.cn", 443); got != proxyforwarder.RefusalNone {
			t.Fatalf("open %d: %q", i, got)
		}
	}
	next := NewProxyInstall(e.in.id, e.runner, DefaultProxyLimits())
	next.Inherit(e.in)
	e.in = next
	e.apply(appplatform.ProxyDailyBytesMax, "qt.gtimg.cn")
	if got := e.refusalOf("qt.gtimg.cn", 443); got != proxyforwarder.RefusalBusy {
		t.Fatalf("a new generation got a fresh burst: %q", got)
	}
}

func TestProxyRollupBoundsDistinctHosts(t *testing.T) {
	e := newProxyEnv(t, nil)
	for i := 0; i < ProxyMaxRollupHosts+10; i++ {
		e.in.opened("h" + strings.Repeat("a", i%20) + string(rune('a'+i%26)) + ".example.com" + strings.Repeat("x", i/26))
	}
	e.clock.advance(time.Hour)
	reports := e.in.Reports(e.clock.Now())
	if len(reports) != 1 || len(reports[0].Hosts) > ProxyMaxRollupHosts {
		t.Fatalf("hosts %d", len(reports[0].Hosts))
	}
}
