package transport

import (
	"bufio"
	"net"
	"sync"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/broker"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
)

// The reviewed forward proxy on the broker socket (doc 184). The socket speaks two
// protocols on one listener: portal.broker/1 (HTTP) and, when a connection opens
// with the eight bytes PRXY0001, a raw tunnel stream. A short sniff of those eight
// bytes tells them apart before any HTTP parsing happens.

const (
	// egressProxyResource is the session resource that carries the proxy policy.
	egressProxyResource = "network.proxy"
	// egressSniffTimeout bounds how long a fresh connection may stay silent before it is
	// handed to the HTTP server, which then applies its own header timeout.
	egressSniffTimeout = 5 * time.Second
	// egressMaxSniffers bounds the connections whose first bytes are being read.
	egressMaxSniffers = 64
)

// proxyPolicyOf is the parsed policy of a session that carries one, or false.
func proxyPolicyOf(c control.ServiceEgressSessionCommand) (appplatform.ParsedProxyPolicy, bool) {
	if c.Proxy == nil {
		return appplatform.ParsedProxyPolicy{}, false
	}
	parsed, err := appplatform.ParseProxyPolicy([]byte(c.Proxy.Policy))
	return parsed, err == nil
}

// proxyUsable reports whether the installation holds a current, unexpired,
// undrained session that carries the proxy resource and whose socket is intact.
// It is what a stream is admitted under, and is read again for every stream.
func (in *egressInstall) proxyUsable() bool {
	in.mu.Lock()
	defer in.mu.Unlock()
	s := in.current
	return !in.closed && s != nil && !s.draining && in.registry.now().Before(s.expires) && s.allows(egressProxyResource) && (in.intact == nil || in.intact() == nil)
}

// applyProxy sets the installation's proxy authority from the session that just
// became current: its policy, or none. The same policy digest keeps open tunnels,
// so a renewal does not cut them; any other policy, or none, does.
func (in *egressInstall) applyProxy(s *egressSession) {
	if s != nil && s.proxy != nil {
		in.proxy.Apply(*s.proxy)
		return
	}
	in.proxy.Revoke()
}

// egressSniffListener hands out the connections that are not proxy streams. It
// accepts on the raw socket listener, applies the peer check to every connection
// before reading a byte, reads the first eight bytes with a deadline and sends
// proxy streams to the installation's proxy; everything else goes to the HTTP server.
type egressSniffListener struct {
	net.Listener
	in       *egressInstall
	peerOK   func(net.Conn) bool
	conns    chan net.Conn
	done     chan struct{}
	once     sync.Once
	sniffers chan struct{}
}

func newEgressSniffListener(in *egressInstall) *egressSniffListener {
	return &egressSniffListener{
		Listener: in.listener, in: in, peerOK: in.peerOK, conns: make(chan net.Conn), done: make(chan struct{}),
		sniffers: make(chan struct{}, egressMaxSniffers),
	}
}

// run accepts until the raw listener closes.
func (l *egressSniffListener) run(wg *sync.WaitGroup) {
	defer l.once.Do(func() { close(l.done) })
	for {
		conn, err := l.Listener.Accept()
		if err != nil {
			return
		}
		if l.peerOK != nil && !l.peerOK(conn) {
			_ = conn.Close()
			continue
		}
		select {
		case l.sniffers <- struct{}{}:
		default:
			_ = conn.Close()
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			var once sync.Once
			release := func() { once.Do(func() { <-l.sniffers }) }
			defer release()
			l.route(conn, release)
		}()
	}
}

// route reads the first bytes of a connection and sends it where they say. release gives
// back the sniffer slot as soon as the decision is made, so a long tunnel or a slow
// HTTP exchange never holds one.
func (l *egressSniffListener) route(conn net.Conn, release func()) {
	r := bufio.NewReaderSize(conn, 4096)
	_ = conn.SetReadDeadline(time.Now().Add(egressSniffTimeout))
	head, err := r.Peek(len(broker.ProxyStreamMagic))
	_ = conn.SetReadDeadline(time.Time{})
	release()
	if err == nil && string(head) == broker.ProxyStreamMagic {
		l.in.proxy.Serve(conn, r, l.in.proxyUsable)
		return
	}
	if err != nil && len(head) == 0 {
		_ = conn.Close()
		return
	}
	select {
	case l.conns <- &peekedConn{Conn: conn, r: r}:
	case <-l.done:
		_ = conn.Close()
	}
}

// Accept returns the next connection that is not a proxy stream.
func (l *egressSniffListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.conns:
		return c, nil
	case <-l.done:
		return nil, net.ErrClosed
	}
}

func (l *egressSniffListener) Close() error {
	err := l.Listener.Close()
	l.once.Do(func() { close(l.done) })
	return err
}

// peekedConn reads through the buffer the sniff filled, so the HTTP server sees the
// bytes that were peeked.
type peekedConn struct {
	net.Conn
	r *bufio.Reader
}

func (c *peekedConn) Read(p []byte) (int, error) { return c.r.Read(p) }

// flushProxyReports sends what each installation's proxy has due to Portal over the
// installation's current session. A report that cannot be sent is dropped: the
// next one covers the same ground, and nothing here is authority.
func (r *egressRegistry) flushProxyReports() {
	type target struct {
		in      *egressInstall
		session *egressSession
	}
	var targets []target
	r.mu.Lock()
	for _, in := range r.installs {
		in.mu.Lock()
		if s := in.current; s != nil && !s.draining && s.allows(egressProxyResource) {
			targets = append(targets, target{in, s})
		}
		in.mu.Unlock()
	}
	r.mu.Unlock()
	now := r.now()
	for _, t := range targets {
		for _, report := range t.in.proxy.Reports(now) {
			payload, err := EncodeEgressProxyReport(report)
			if err != nil {
				continue
			}
			call, err := t.session.mux.NewCall()
			if err != nil {
				break
			}
			_ = call.Send(EgressFrameProxyReport, payload)
			call.Finish()
		}
	}
}
