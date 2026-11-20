//go:build linux

package transport

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

func quotesPin() appplatform.ProxyPin {
	return appplatform.ProxyPin{Host: "quotes.internal.anchan.kazuhahub.com", Address: "127.0.0.1", Port: 20443}
}

// largestPins is the largest valid allowlist: 16 entries with 253-byte names and full-length IPv6 addresses.
func largestPins() []appplatform.ProxyPin {
	var pins []appplatform.ProxyPin
	for i := 0; i < appplatform.MaxProxyOperatorPins; i++ {
		host := strings.Repeat("a", 63) + "." + strings.Repeat("b", 63) + "." + strings.Repeat("c", 63) + ".x" + string(rune('a'+i)) + strings.Repeat("d", 58)
		pins = append(pins, appplatform.ProxyPin{Host: host, Address: "2606:4700:4700:1111:2222:3333:4444:5555", Port: 65535})
	}
	return pins
}

// A new Portal asks PRPX0002 and gets the fixed profile and the operator's pins.
func TestProxyPinsProfileQuery(t *testing.T) {
	f := newEgrFixture(t)
	f.provider.proxyPins = []appplatform.ProxyPin{quotesPin()}
	path, _ := egressEndpoint(t, f, true)
	state, err := queryProxyState(context.Background(), func(ctx context.Context) (*net.UnixConn, error) {
		return connectEndpoint(ctx, path, privateProfile(uint32(os.Geteuid())))
	})
	if err != nil {
		t.Fatal(err)
	}
	if !state.PinsSupported || state.Profile != NewProxyProfile() || len(state.Pins) != 1 || state.Pins[0] != quotesPin() {
		t.Fatalf("%+v", state)
	}
}

// Empty proxy_pins still answers PRPY0002: pins are supported, there are none.
func TestProxyPinsProfileWithNoPins(t *testing.T) {
	f := newEgrFixture(t)
	path, _ := egressEndpoint(t, f, true)
	state, err := queryProxyState(context.Background(), func(ctx context.Context) (*net.UnixConn, error) {
		return connectEndpoint(ctx, path, privateProfile(uint32(os.Geteuid())))
	})
	if err != nil || !state.PinsSupported || len(state.Pins) != 0 || state.Profile != NewProxyProfile() {
		t.Fatalf("%+v %v", state, err)
	}
}

// New runner, old Portal: PRPX0001 answers the exact 1-dev.1 bytes whatever proxy_pins holds, including the
// largest valid list, and an old Portal's parse and equality still accept them. Fails open only for 1-dev.1
// policies, which the old Portal can grant anyway.
func TestProxyProfileV1IsFrozenWhateverThePins(t *testing.T) {
	want, err := encodeProxyProfile(NewProxyProfile())
	if err != nil {
		t.Fatal(err)
	}
	for name, pins := range map[string][]appplatform.ProxyPin{"none": nil, "one": {quotesPin()}, "largest": largestPins()} {
		t.Run(name, func(t *testing.T) {
			f := newEgrFixture(t)
			f.provider.proxyPins = pins
			path, _ := egressEndpoint(t, f, true)
			conn, err := connectEndpoint(context.Background(), path, privateProfile(uint32(os.Geteuid())))
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			raw, err := proxyQueryExchange(conn, ProxyQueryMagic, ProxyReplyMagic, maxProxyProfileBytes)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(raw, want) {
				t.Fatalf("PRPY0001 changed: %s", raw)
			}
			profile, err := ParseProxyProfile(raw)
			if err != nil || profile != NewProxyProfile() {
				t.Fatalf("an old Portal would refuse it: %v", err)
			}
		})
	}
}

// The largest valid allowlist fits PRPY0002 under its cap and a new Portal parses it. Fails closed: one byte
// over the cap is unsupported.
func TestProxyPinsProfileLargestFitsAndOneByteOverDoesNot(t *testing.T) {
	pins := largestPins()
	profile, err := NewProxyPinsProfile(pins)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := encodeProxyPinsProfile(profile)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) >= maxProxyPinsProfileBytes || len(raw) < 5000 {
		t.Fatalf("largest profile is %d bytes", len(raw))
	}
	parsed, err := ParseProxyPinsProfile(raw)
	if err != nil || len(parsed.Pins) != 16 {
		t.Fatalf("parse: %v", err)
	}
	if maxProxyPinsProfileBytes != 8192 {
		t.Fatalf("cap is %d, the document says 8,192", maxProxyPinsProfileBytes)
	}
	padded := append(bytes.Clone(raw), bytes.Repeat([]byte(" "), maxProxyPinsProfileBytes-len(raw)+1)...)
	if len(padded) != maxProxyPinsProfileBytes+1 {
		t.Fatal("test arithmetic")
	}
	if _, err := ParseProxyPinsProfile(padded); !errors.Is(err, ErrProxyUnsupported) {
		t.Fatalf("one byte over the cap: %v", err)
	}
	// And a header that announces one byte over the cap is refused before any read.
	server := fakeRunner(t, func(conn *net.UnixConn, query string) {
		var nonce [32]byte
		_, _ = io.ReadFull(conn, nonce[:])
		header := make([]byte, 44)
		copy(header, ProxyPinsReplyMagic)
		copy(header[8:], nonce[:])
		binary.BigEndian.PutUint32(header[40:], maxProxyPinsProfileBytes+1)
		_, _ = conn.Write(header)
	})
	if _, err := queryProxyState(context.Background(), server.dial); !errors.Is(err, ErrProxyUnsupported) {
		t.Fatalf("an oversized announcement: %v", err)
	}
}

// The profile codec is closed: only the canonical encoding, with the fixed part equal to this Portal's and a
// valid sorted allowlist, is read. Fails closed.
func TestProxyPinsProfileCodecIsClosed(t *testing.T) {
	good, err := NewProxyPinsProfile([]appplatform.ProxyPin{quotesPin()})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := encodeProxyPinsProfile(good)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseProxyPinsProfile(raw); err != nil {
		t.Fatal(err)
	}
	want := `{"format":"portal.runner.proxy-profile/1-dev.2","fixed":` + string(mustProfileJSON(t)) + `,"pins":[{"host":"quotes.internal.anchan.kazuhahub.com","address":"127.0.0.1","port":20443}]}`
	if string(raw) != want {
		t.Fatalf("encoding = %s", raw)
	}
	empty, _ := encodeProxyPinsProfile(ProxyPinsProfile{Format: ProxyPinsProfileFormat, Fixed: NewProxyProfile(), Pins: []appplatform.ProxyPin{}})
	if !strings.HasSuffix(string(empty), `"pins":[]}`) {
		t.Fatalf("empty allowlist spelling: %s", empty)
	}
	if _, err := ParseProxyPinsProfile(empty); err != nil {
		t.Fatal(err)
	}
	for name, bad := range map[string]string{
		"empty":         ``,
		"null pins":     strings.Replace(want, `[{"host":"quotes.internal.anchan.kazuhahub.com","address":"127.0.0.1","port":20443}]`, `null`, 1),
		"omitted pins":  `{"format":"portal.runner.proxy-profile/1-dev.2","fixed":` + string(mustProfileJSON(t)) + `}`,
		"other format":  strings.Replace(want, "1-dev.2", "1-dev.3", 1),
		"extra member":  strings.TrimSuffix(want, "}") + `,"x":1}`,
		"trailing":      want + " ",
		"other fixed":   strings.Replace(want, `"tunnels":16`, `"tunnels":17`, 1),
		"denied pin":    strings.Replace(want, `127.0.0.1`, `169.254.169.254`, 1),
		"port zero":     strings.Replace(want, `"port":20443`, `"port":0`, 1),
		"v1 profile":    string(mustProfileJSON(t)),
		"wildcard host": strings.Replace(want, `quotes.internal`, `*.internal`, 1),
	} {
		if _, err := ParseProxyPinsProfile([]byte(bad)); !errors.Is(err, ErrProxyUnsupported) {
			t.Errorf("%s accepted: %v", name, err)
		}
	}
	// A runner never encodes what Portal would refuse.
	if _, err := NewProxyPinsProfile([]appplatform.ProxyPin{{Host: "a.example.com", Address: "169.254.169.254", Port: 443}}); err == nil {
		t.Fatal("a denied pin was encoded")
	}
	two := []appplatform.ProxyPin{{Host: "b.example.com", Address: "127.0.0.1", Port: 443}, {Host: "a.example.com", Address: "127.0.0.1", Port: 443}}
	if _, err := NewProxyPinsProfile(two); err == nil {
		t.Fatal("an unsorted list was encoded")
	}
}

func mustProfileJSON(t *testing.T) []byte {
	t.Helper()
	raw, err := encodeProxyProfile(NewProxyProfile())
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// fakeRunnerServer is a unix endpoint that answers each connection as the test says, standing in for a runner
// of another generation.
type fakeRunnerServer struct {
	path string
	seen chan string
}

func (s *fakeRunnerServer) dial(ctx context.Context) (*net.UnixConn, error) {
	c, err := (&net.Dialer{}).DialContext(ctx, "unix", s.path)
	if err != nil {
		return nil, err
	}
	return c.(*net.UnixConn), nil
}

func fakeRunner(t *testing.T, handle func(conn *net.UnixConn, query string)) *fakeRunnerServer {
	t.Helper()
	path := filepath.Join(privateDir(t), "fake.sock")
	l, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	s := &fakeRunnerServer{path: path, seen: make(chan string, 8)}
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				magic := make([]byte, 8)
				if _, err := io.ReadFull(c, magic); err != nil {
					return
				}
				select {
				case s.seen <- string(magic):
				default:
				}
				handle(c.(*net.UnixConn), string(magic))
			}()
		}
	}()
	return s
}

func answerV1(t *testing.T) func(conn *net.UnixConn, query string) {
	raw := mustProfileJSON(t)
	return func(conn *net.UnixConn, query string) {
		if query != ProxyQueryMagic {
			return // a runner that predates pins closes at the unknown magic
		}
		var nonce [32]byte
		_, _ = io.ReadFull(conn, nonce[:])
		header := make([]byte, 44)
		copy(header, ProxyReplyMagic)
		copy(header[8:], nonce[:])
		binary.BigEndian.PutUint32(header[40:], uint32(len(raw)))
		_, _ = conn.Write(append(header, raw...))
	}
}

// New Portal, old runner: PRPX0002 closes at the unknown magic, the PRPX0001 fallback answers, and the state
// says pins are not supported, so unpinned grants keep working and pinned ones are refused. Fails closed for
// pins only.
func TestProxyStateFallsBackToV1OnARunnerThatPredatesPins(t *testing.T) {
	server := fakeRunner(t, answerV1(t))
	state, err := queryProxyState(context.Background(), server.dial)
	if err != nil {
		t.Fatal(err)
	}
	if state.PinsSupported || len(state.Pins) != 0 || state.Profile != NewProxyProfile() {
		t.Fatalf("%+v", state)
	}
	first, second := <-server.seen, <-server.seen
	if first != ProxyPinsQueryMagic || second != ProxyQueryMagic {
		t.Fatalf("asked %q then %q", first, second)
	}
}

// Any other failure of PRPX0002 is not "unsupported": it is returned as it is, with no fallback, so a granted
// proxy is never dropped from a session on a transient failure (doc 184 item 8). Fails closed.
func TestProxyStateDoesNotFallBackOnOtherFailures(t *testing.T) {
	// The answer is cut short after the header: a failure to ask.
	server := fakeRunner(t, func(conn *net.UnixConn, query string) {
		var nonce [32]byte
		_, _ = io.ReadFull(conn, nonce[:])
		header := make([]byte, 44)
		copy(header, ProxyPinsReplyMagic)
		copy(header[8:], nonce[:])
		binary.BigEndian.PutUint32(header[40:], 100)
		_, _ = conn.Write(header)
	})
	if _, err := queryProxyState(context.Background(), server.dial); err == nil || errors.Is(err, ErrProxyUnsupported) {
		t.Fatalf("a truncated answer: %v", err)
	}
	select {
	case first := <-server.seen:
		if first != ProxyPinsQueryMagic {
			t.Fatalf("asked %q", first)
		}
	default:
		t.Fatal("nothing was asked")
	}
	select {
	case again := <-server.seen:
		t.Fatalf("fell back and asked %q", again)
	default:
	}
	// A garbage answer to PRPX0002 is the runner saying something it should not: unsupported, no fallback.
	garbage := fakeRunner(t, func(conn *net.UnixConn, query string) {
		var nonce [32]byte
		_, _ = io.ReadFull(conn, nonce[:])
		header := make([]byte, 44)
		copy(header, ProxyPinsReplyMagic)
		copy(header[8:], nonce[:])
		binary.BigEndian.PutUint32(header[40:], 2)
		_, _ = conn.Write(append(header, '{', '}'))
	})
	if _, err := queryProxyState(context.Background(), garbage.dial); !errors.Is(err, ErrProxyUnsupported) {
		t.Fatalf("garbage: %v", err)
	}
}

// A runner built without egress does not answer either query.
func TestProxyStateUnsupportedOnNarrowRunners(t *testing.T) {
	f := newEgrFixture(t)
	path, _ := egressEndpoint(t, f, false)
	_, err := queryProxyState(context.Background(), func(ctx context.Context) (*net.UnixConn, error) {
		return connectEndpoint(ctx, path, privateProfile(uint32(os.Geteuid())))
	})
	if !errors.Is(err, ErrProxyUnsupported) {
		t.Fatalf("query on a narrow runner: %v", err)
	}
}

// The runner gives the operator's allowlist to the broker's runner-wide state, so a session's pinned policy is
// judged against it at every dial. Fails closed: without this wiring every pin would be denied.
func TestRegistryBrokerHoldsTheOperatorPins(t *testing.T) {
	f := newEgrFixture(t)
	f.provider.proxyPins = []appplatform.ProxyPin{quotesPin()}
	f.registry.applyProxyPins(f.provider.proxyPins)
	if !f.registry.proxy.PinPermitted(quotesPin()) {
		t.Fatal("the broker does not hold the operator's pin")
	}
	if f.registry.proxy.PinPermitted(appplatform.ProxyPin{Host: quotesPin().Host, Address: "127.0.0.1", Port: 1}) {
		t.Fatal("the broker permits another destination")
	}
}

// ListenNetworkManagedController is the one production constructor: its option must reach both the provider
// (the query) and the registry (the broker).
func TestManagedControllerOptionReachesProviderAndBroker(t *testing.T) {
	var settings managedControllerOptions
	WithProxyPins([]appplatform.ProxyPin{quotesPin()})(&settings)
	if len(settings.proxyPins) != 1 || settings.proxyPins[0] != quotesPin() {
		t.Fatalf("%+v", settings)
	}
	f := newEgrFixture(t)
	provider, err := newNetworkProvider(f.enrollment, 4, 2, WithProxyPins([]appplatform.ProxyPin{quotesPin()}))
	if err != nil {
		t.Fatal(err)
	}
	if len(provider.proxyPins) != 1 || provider.proxyPins[0] != quotesPin() {
		t.Fatalf("provider pins: %+v", provider.proxyPins)
	}
	if _, err := newNetworkProvider(f.enrollment, 4, 2, WithProxyPins([]appplatform.ProxyPin{{Host: "a.example.com", Address: "169.254.169.254", Port: 443}})); err == nil {
		t.Fatal("a denied pin was accepted by the provider")
	}
}
