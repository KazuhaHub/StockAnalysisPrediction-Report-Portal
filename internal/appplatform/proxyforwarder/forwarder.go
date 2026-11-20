package proxyforwarder

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"sync"
	"time"
)

const (
	// EnvBrokerSocket names the unix socket path of the runner's broker.
	EnvBrokerSocket = "PORTAL_APP_BROKER_SOCKET"
	// EnvProxyListen names the loopback address the forwarder listens on.
	EnvProxyListen = "PORTAL_APP_PROXY_LISTEN"
	// DefaultListen is used when EnvProxyListen is unset.
	DefaultListen = "127.0.0.1:3128"

	maxHeaderBytes = 8 << 10
	replyDeadline  = 20 * time.Second
	writeDeadline  = 5 * time.Second
)

// headerDeadline bounds reading the request line and headers.
var headerDeadline = 5 * time.Second

// Run reads its configuration from the environment, listens on a loopback
// address and serves until ctx is done.
func Run(ctx context.Context) error {
	sock := os.Getenv(EnvBrokerSocket)
	if sock == "" {
		return fmt.Errorf("%s is required", EnvBrokerSocket)
	}
	listen := os.Getenv(EnvProxyListen)
	if listen == "" {
		listen = DefaultListen
	}
	host, _, err := net.SplitHostPort(listen)
	if err != nil {
		return fmt.Errorf("%s: %w", EnvProxyListen, err)
	}
	if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("%s must be a loopback address, got %q", EnvProxyListen, listen)
	}
	l, err := net.Listen("tcp", listen)
	if err != nil {
		return err
	}
	return Serve(ctx, l, func(ctx context.Context) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "unix", sock)
	})
}

type tracker struct {
	mu     sync.Mutex
	closed bool
	conns  map[net.Conn]struct{}
}

func (t *tracker) add(c net.Conn) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return false
	}
	t.conns[c] = struct{}{}
	return true
}

func (t *tracker) remove(c net.Conn) {
	t.mu.Lock()
	delete(t.conns, c)
	t.mu.Unlock()
}

func (t *tracker) closeAll() {
	t.mu.Lock()
	t.closed = true
	for c := range t.conns {
		c.Close()
	}
	t.mu.Unlock()
}

// Serve accepts CONNECT requests on l and relays each tunnel over a connection
// from dial. It returns nil once ctx is done, or the listener's error; either
// way it closes the listener and every open tunnel and waits for them.
func Serve(ctx context.Context, l net.Listener, dial func(context.Context) (net.Conn, error)) error {
	tr := &tracker{conns: map[net.Conn]struct{}{}}
	var wg sync.WaitGroup
	stop := make(chan struct{})
	wg.Add(1)
	go func() {
		defer wg.Done()
		select {
		case <-ctx.Done():
		case <-stop:
		}
		l.Close()
		tr.closeAll()
	}()
	for {
		c, err := l.Accept()
		if err != nil {
			close(stop)
			wg.Wait()
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			handle(ctx, c, dial, tr)
		}()
	}
}

func handle(ctx context.Context, client net.Conn, dial func(context.Context) (net.Conn, error), tr *tracker) {
	defer client.Close()
	if !tr.add(client) {
		return
	}
	defer tr.remove(client)

	br := bufio.NewReaderSize(client, maxHeaderBytes)
	client.SetReadDeadline(time.Now().Add(headerDeadline))
	method, version, target, ok := readRequestHead(br)
	if !ok {
		respond(client, 400, "Bad Request", nil)
		return
	}
	client.SetReadDeadline(time.Time{})
	if method != "CONNECT" {
		respond(client, 405, "Method Not Allowed", []string{"Allow: CONNECT"})
		return
	}
	host, port, err := parseTarget(target)
	if err != nil || (version != "HTTP/1.1" && version != "HTTP/1.0") {
		respond(client, 400, "Bad Request", nil)
		return
	}
	open, err := EncodeOpen(host, port)
	if err != nil {
		respond(client, 400, "Bad Request", nil)
		return
	}

	broker, err := dial(ctx)
	if err != nil {
		refuse(client, RefusalUnavailable)
		return
	}
	defer broker.Close()
	if !tr.add(broker) {
		return
	}
	defer tr.remove(broker)

	broker.SetWriteDeadline(time.Now().Add(writeDeadline))
	if _, err := broker.Write(open); err != nil {
		refuse(client, RefusalUnavailable)
		return
	}
	broker.SetWriteDeadline(time.Time{})
	broker.SetReadDeadline(time.Now().Add(replyDeadline))
	refusal, err := ReadReply(broker)
	broker.SetReadDeadline(time.Time{})
	if err != nil {
		refuse(client, RefusalUnavailable)
		return
	}
	if refusal != RefusalNone {
		refuse(client, refusal)
		return
	}

	client.SetWriteDeadline(time.Now().Add(writeDeadline))
	_, err = io.WriteString(client, "HTTP/1.1 200 Connection established\r\n\r\n")
	client.SetWriteDeadline(time.Time{})
	if err != nil {
		return
	}
	splice(client, br, broker)
}

// readRequestHead reads the request line and headers, at most maxHeaderBytes in
// all. The headers are consumed and ignored.
func readRequestHead(br *bufio.Reader) (method, version, target string, ok bool) {
	total := 0
	first := true
	for {
		line, err := br.ReadSlice('\n')
		if err != nil {
			return "", "", "", false
		}
		total += len(line)
		if total > maxHeaderBytes {
			return "", "", "", false
		}
		text := strings.TrimRight(string(line), "\r\n")
		if first {
			parts := strings.Split(text, " ")
			if len(parts) != 3 {
				return "", "", "", false
			}
			method, target, version = parts[0], parts[1], parts[2]
			first = false
			continue
		}
		if text == "" {
			return method, version, target, true
		}
	}
}

// parseTarget checks the syntax of an authority-form target. The host is kept
// exactly as written, brackets included: whether it is acceptable is the
// broker's decision.
func parseTarget(target string) (string, int, error) {
	for i := 0; i < len(target); i++ {
		if target[i] <= ' ' || target[i] == 0x7f {
			return "", 0, ErrProtocol
		}
	}
	return splitTarget(target)
}

func respond(c net.Conn, code int, text string, extra []string) {
	var b strings.Builder
	fmt.Fprintf(&b, "HTTP/1.1 %d %s\r\n", code, text)
	for _, h := range extra {
		b.WriteString(h + "\r\n")
	}
	b.WriteString("Content-Length: 0\r\nConnection: close\r\n\r\n")
	c.SetWriteDeadline(time.Now().Add(writeDeadline))
	io.WriteString(c, b.String())
}

func refuse(c net.Conn, r Refusal) {
	respond(c, 403, "Forbidden", []string{"X-Portal-Proxy-Refusal: " + string(r)})
}

// splice carries bytes both ways, passing a half-close on to the other side, and
// returns once both directions have ended. Bytes the client sent behind its
// headers are already in br and go first.
func splice(client net.Conn, br *bufio.Reader, broker net.Conn) {
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, err := io.Copy(broker, br)
		finish(broker, client, err)
	}()
	go func() {
		defer wg.Done()
		_, err := io.Copy(client, broker)
		finish(client, broker, err)
	}()
	wg.Wait()
}

// finish ends one direction: a clean EOF becomes a half-close of dst, any error
// closes both sides so the other direction cannot hang.
func finish(dst, other net.Conn, err error) {
	if err != nil && !errors.Is(err, io.EOF) {
		dst.Close()
		other.Close()
		return
	}
	if cw, ok := dst.(interface{ CloseWrite() error }); ok {
		if cw.CloseWrite() == nil {
			return
		}
	}
	dst.Close()
}
