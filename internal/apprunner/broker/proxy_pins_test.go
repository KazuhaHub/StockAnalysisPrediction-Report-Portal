package broker

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform/proxyforwarder"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/hostnet"
)

const quotesHost = "quotes.internal.anchan.kazuhahub.com"

var quotesPin = appplatform.ProxyPin{Host: quotesHost, Address: "127.0.0.1", Port: 20443}

// applyPinned makes a policy with pins the installation's authority.
func (e *proxyEnv) applyPinned(hosts []string, pins ...appplatform.ProxyPin) appplatform.ParsedProxyPolicy {
	e.t.Helper()
	raw, err := appplatform.CanonicalProxyPolicy(appplatform.ProxyPolicy{Hosts: hosts, Pins: pins, DailyBytes: appplatform.ProxyDailyBytesMax})
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

// forbidLookup makes any resolution fail the test, which is how a pinned tunnel proves it never resolves.
func (e *proxyEnv) forbidLookup() {
	e.runner.Lookup = func(ctx context.Context, host string) ([]netip.Addr, error) {
		e.t.Errorf("a pinned name was resolved: %s", host)
		return nil, errors.New("must not resolve")
	}
}

// A pinned name in the operator allowlist connects to the pinned address and port, never resolves, and skips
// the host-interface and public-address rules for that entry. Fails closed: the dial is only the pin.
func TestProxyPinnedNameDialsThePinWithoutResolving(t *testing.T) {
	e := newProxyEnv(t, nil)
	e.forbidLookup()
	// Loopback is one of the host's own addresses: for an ordinary host this would be denied.
	e.runner.Interfaces = func() ([]hostnet.Interface, error) {
		return []hostnet.Interface{{Name: "lo", Addrs: []netip.Prefix{netip.MustParsePrefix("127.0.0.1/8")}}}, nil
	}
	e.runner.SetPins([]appplatform.ProxyPin{quotesPin})
	e.applyPinned(nil, quotesPin)
	conn, refusal, done := e.open(quotesHost, 443)
	if refusal != proxyforwarder.RefusalNone {
		t.Fatalf("refused: %s", refusal)
	}
	if body := e.get(conn, quotesHost); !strings.Contains(body, "hello from upstream") {
		t.Fatalf("no response through the pinned tunnel: %q", body)
	}
	<-done
	if dialed := e.dialedAddresses(); len(dialed) != 1 || dialed[0] != "127.0.0.1:20443" {
		t.Fatalf("dialled %v, want the pin", dialed)
	}
	if e.lookups.Load() != 0 {
		t.Fatalf("%d lookups", e.lookups.Load())
	}
}

func TestProxyPinnedIPv6PinIsDialledBracketed(t *testing.T) {
	e := newProxyEnv(t, nil)
	e.forbidLookup()
	pin := appplatform.ProxyPin{Host: quotesHost, Address: "::1", Port: 20443}
	e.runner.SetPins([]appplatform.ProxyPin{pin})
	e.applyPinned(nil, pin)
	if got := e.refusalOf(quotesHost, 443); got != proxyforwarder.RefusalNone {
		t.Fatalf("refused: %q", got)
	}
	if dialed := e.dialedAddresses(); len(dialed) != 1 || dialed[0] != "[::1]:20443" {
		t.Fatalf("dialled %v", dialed)
	}
}

// A pin that is not exactly an entry of the runner's allowlist is address_denied and nothing is dialled.
// Fails closed.
func TestProxyPinNotInTheOperatorAllowlistIsDenied(t *testing.T) {
	cases := map[string][]appplatform.ProxyPin{
		"empty allowlist":   nil,
		"other port":        {{Host: quotesHost, Address: "127.0.0.1", Port: 20444}},
		"other address":     {{Host: quotesHost, Address: "127.0.0.2", Port: 20443}},
		"other host":        {{Host: "other.internal.anchan.kazuhahub.com", Address: "127.0.0.1", Port: 20443}},
		"another name only": {{Host: "a.example.com", Address: "127.0.0.1", Port: 20443}, {Host: "b.example.com", Address: "127.0.0.1", Port: 20443}},
	}
	for name, list := range cases {
		t.Run(name, func(t *testing.T) {
			e := newProxyEnv(t, nil)
			e.forbidLookup()
			e.runner.SetPins(list)
			e.applyPinned(nil, quotesPin)
			if got := e.refusalOf(quotesHost, 443); got != proxyforwarder.RefusalAddressDenied {
				t.Fatalf("refused as %q", got)
			}
			if d := e.dialedAddresses(); len(d) != 0 {
				t.Fatalf("dialled %v", d)
			}
		})
	}
}

// The operator removing a pin (a runner restart with a shorter list) is felt at the next dial.
func TestProxyPinRemovedFromTheAllowlistIsDeniedAtTheNextDial(t *testing.T) {
	e := newProxyEnv(t, nil)
	e.runner.SetPins([]appplatform.ProxyPin{quotesPin})
	e.applyPinned(nil, quotesPin)
	if got := e.refusalOf(quotesHost, 443); got != proxyforwarder.RefusalNone {
		t.Fatalf("before: %q", got)
	}
	e.runner.SetPins(nil)
	if got := e.refusalOf(quotesHost, 443); got != proxyforwarder.RefusalAddressDenied {
		t.Fatalf("after: %q", got)
	}
}

// A denied address is refused at every dial even if a corrupt policy and a corrupt allowlist both name it.
// Fails closed.
func TestProxyDeniedPinAddressIsRefusedAtTheDial(t *testing.T) {
	for _, address := range []string{"169.254.169.254", "100.100.100.200", "192.0.0.192", "168.63.129.16", "fd20:ce::254", "224.0.0.1", "0.0.0.0"} {
		t.Run(address, func(t *testing.T) {
			e := newProxyEnv(t, nil)
			bad := appplatform.ProxyPin{Host: quotesHost, Address: address, Port: 443}
			e.runner.SetPins([]appplatform.ProxyPin{bad})
			// A policy that parsing would refuse, applied directly: the dial must still refuse it.
			e.in.Apply(appplatform.ParsedProxyPolicy{
				Policy: appplatform.ProxyPolicy{Pins: []appplatform.ProxyPin{bad}, DailyBytes: appplatform.ProxyDailyBytesMax}, SHA256: strings.Repeat("a", 64)})
			if got := e.refusalOf(quotesHost, 443); got != proxyforwarder.RefusalAddressDenied {
				t.Fatalf("refused as %q", got)
			}
			if d := e.dialedAddresses(); len(d) != 0 {
				t.Fatalf("dialled %v", d)
			}
		})
	}
}

// With Dial refusing, the result is connect_failed and no 200 is sent. Fails closed.
func TestProxyPinnedDialFailureIsConnectFailed(t *testing.T) {
	e := newProxyEnv(t, nil)
	e.runner.SetPins([]appplatform.ProxyPin{quotesPin})
	e.applyPinned(nil, quotesPin)
	e.runner.Dial = func(context.Context, string, string) (net.Conn, error) { return nil, errors.New("connection refused") }
	if got := e.refusalOf(quotesHost, 443); got != proxyforwarder.RefusalConnectFailed {
		t.Fatalf("refused as %q", got)
	}
}

// With a wildcard covering the pinned name, the pinned name dials the pin, and a sibling that resolves to
// loopback meets the full guard. A pin never falls back to DNS, and DNS never falls back to a pin.
func TestProxyWildcardDoesNotShadowOrFallBackToAPin(t *testing.T) {
	e := newProxyEnv(t, nil)
	e.runner.SetPins([]appplatform.ProxyPin{quotesPin})
	e.applyPinned([]string{"*.internal.anchan.kazuhahub.com"}, quotesPin)
	e.mu.Lock()
	e.answers["other.internal.anchan.kazuhahub.com"] = []netip.Addr{netip.MustParseAddr("127.0.0.1")}
	e.mu.Unlock()
	if got := e.refusalOf(quotesHost, 443); got != proxyforwarder.RefusalNone {
		t.Fatalf("pinned name: %q", got)
	}
	if e.lookups.Load() != 0 {
		t.Fatal("the pinned name was resolved")
	}
	if got := e.refusalOf("other.internal.anchan.kazuhahub.com", 443); got != proxyforwarder.RefusalAddressDenied {
		t.Fatalf("sibling name: %q", got)
	}
	if e.lookups.Load() != 1 {
		t.Fatalf("the sibling must resolve once, got %d", e.lookups.Load())
	}
	// A pin that is refused does not fall back to the wildcard's DNS path.
	e.runner.SetPins(nil)
	before := e.lookups.Load()
	if got := e.refusalOf(quotesHost, 443); got != proxyforwarder.RefusalAddressDenied {
		t.Fatalf("unpermitted pin: %q", got)
	}
	if e.lookups.Load() != before {
		t.Fatal("a refused pin fell back to DNS")
	}
}

// An SNI other than the pinned name closes the tunnel after the 200, with nothing forwarded. Fails closed.
func TestProxyPinnedSNIMustBeThePinnedName(t *testing.T) {
	e := newProxyEnv(t, nil)
	e.runner.SetPins([]appplatform.ProxyPin{quotesPin})
	e.applyPinned(nil, quotesPin)
	e.setRaw(true)
	conn, refusal, done := e.open(quotesHost, 443)
	if refusal != proxyforwarder.RefusalNone {
		t.Fatalf("refused: %s", refusal)
	}
	_, _ = conn.Write(buildHello(helloOptions{sni: "portal.anchan.kazuhahub.com"}))
	waitClosed(t, conn, 5*time.Second)
	<-done
	if got := e.rawGot.Load(); got != 0 {
		t.Fatalf("%d bytes reached the pinned listener", got)
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
}

// The application's port stays 443: the pinned port is only where the broker dials. Fails closed.
func TestProxyPinnedNameOnAnyOtherPortIsNotPermitted(t *testing.T) {
	e := newProxyEnv(t, nil)
	e.runner.SetPins([]appplatform.ProxyPin{quotesPin})
	e.applyPinned(nil, quotesPin)
	for _, port := range []int{20443, 80, 8443, 1} {
		if got := e.refusalOf(quotesHost, port); got != proxyforwarder.RefusalPortNotPermitted {
			t.Errorf("port %d: %q", port, got)
		}
	}
	if d := e.dialedAddresses(); len(d) != 0 {
		t.Fatalf("dialled %v", d)
	}
}

// A policy change that removes or moves a pin cuts open pinned tunnels; the next open uses the new pin or is
// host_not_reviewed. Fails closed.
func TestProxyPinChangeCutsOpenPinnedTunnels(t *testing.T) {
	moved := appplatform.ProxyPin{Host: quotesHost, Address: "127.0.0.1", Port: 20444}
	for name, change := range map[string]struct {
		apply func(*proxyEnv)
		next  proxyforwarder.Refusal
		dial  string
	}{
		"removed": {func(e *proxyEnv) { e.applyPinned([]string{"qt.gtimg.cn"}) }, proxyforwarder.RefusalHostNotReviewed, ""},
		"moved":   {func(e *proxyEnv) { e.applyPinned(nil, moved) }, proxyforwarder.RefusalNone, "127.0.0.1:20444"},
	} {
		t.Run(name, func(t *testing.T) {
			e := newProxyEnv(t, nil)
			e.runner.SetPins([]appplatform.ProxyPin{quotesPin, moved})
			e.applyPinned(nil, quotesPin)
			e.setRaw(true)
			conn, refusal, done := e.open(quotesHost, 443)
			if refusal != proxyforwarder.RefusalNone {
				t.Fatalf("refused: %s", refusal)
			}
			if _, err := conn.Write(buildHello(helloOptions{sni: quotesHost})); err != nil {
				t.Fatal(err)
			}
			time.Sleep(50 * time.Millisecond)
			change.apply(e)
			waitClosed(t, conn, 5*time.Second)
			<-done
			if e.in.Open() != 0 || e.runner.Open() != 0 {
				t.Fatalf("slots leaked: %d %d", e.in.Open(), e.runner.Open())
			}
			if got := e.refusalOf(quotesHost, 443); got != change.next {
				t.Fatalf("next open: %q", got)
			}
			if change.dial != "" {
				d := e.dialedAddresses()
				if d[len(d)-1] != change.dial {
					t.Fatalf("dialled %v, want %s last", d, change.dial)
				}
			}
		})
	}
}

// Pinned tunnels count against the same caps, bucket and budget as any other. Fails closed.
func TestProxyPinnedTunnelsCountAgainstTheLimits(t *testing.T) {
	t.Run("open tunnel cap", func(t *testing.T) {
		e := newProxyEnv(t, nil)
		e.runner.SetPins([]appplatform.ProxyPin{quotesPin})
		e.applyPinned(nil, quotesPin)
		e.setRaw(true)
		var held []net.Conn
		for i := 0; i < 16; i++ {
			conn, refusal, _ := e.open(quotesHost, 443)
			if refusal != proxyforwarder.RefusalNone {
				t.Fatalf("tunnel %d refused: %s", i+1, refusal)
			}
			held = append(held, conn)
		}
		if got := e.refusalOf(quotesHost, 443); got != proxyforwarder.RefusalBusy {
			t.Fatalf("the 17th pinned tunnel: %q", got)
		}
		for _, c := range held {
			c.Close()
		}
	})
	t.Run("new tunnel bucket", func(t *testing.T) {
		e := newProxyEnv(t, nil)
		e.runner.SetPins([]appplatform.ProxyPin{quotesPin})
		e.applyPinned(nil, quotesPin)
		for i := 0; i < 50; i++ {
			if got := e.refusalOf(quotesHost, 443); got != proxyforwarder.RefusalNone {
				t.Fatalf("open %d: %q", i+1, got)
			}
		}
		if got := e.refusalOf(quotesHost, 443); got != proxyforwarder.RefusalBusy {
			t.Fatalf("the 51st burst open: %q", got)
		}
	})
	t.Run("daily budget", func(t *testing.T) {
		e := newProxyEnv(t, nil)
		e.runner.SetPins([]appplatform.ProxyPin{quotesPin})
		raw, err := appplatform.CanonicalProxyPolicy(appplatform.ProxyPolicy{Pins: []appplatform.ProxyPin{quotesPin}, DailyBytes: appplatform.ProxyDailyBytesMin})
		if err != nil {
			t.Fatal(err)
		}
		parsed, err := appplatform.ParseProxyPolicy(raw)
		if err != nil {
			t.Fatal(err)
		}
		e.in.Apply(parsed)
		e.setRaw(true)
		conn, refusal, done := e.open(quotesHost, 443)
		if refusal != proxyforwarder.RefusalNone {
			t.Fatalf("refused: %s", refusal)
		}
		if _, err := conn.Write(buildHello(helloOptions{sni: quotesHost})); err != nil {
			t.Fatal(err)
		}
		chunk := appData(64 * 1024)
		go func() {
			for i := 0; i < 64; i++ {
				if _, err := conn.Write(chunk); err != nil {
					return
				}
			}
		}()
		waitClosed(t, conn, 5*time.Second)
		<-done
		if got := e.refusalOf(quotesHost, 443); got != proxyforwarder.RefusalBudgetSpent {
			t.Fatalf("a spent budget: %q", got)
		}
	})
}

// A policy without pins is served exactly as before, whatever the operator's allowlist holds. Fails open only
// for what 1-dev.1 already granted.
func TestProxyPolicyWithoutPinsIgnoresTheAllowlist(t *testing.T) {
	e := newProxyEnv(t, nil)
	e.runner.SetPins([]appplatform.ProxyPin{quotesPin})
	e.apply(appplatform.ProxyDailyBytesMax, quotesHost)
	// The name is an ordinary reviewed host here: it resolves, and a loopback answer is denied.
	e.mu.Lock()
	e.answers[quotesHost] = []netip.Addr{netip.MustParseAddr("127.0.0.1")}
	e.mu.Unlock()
	if got := e.refusalOf(quotesHost, 443); got != proxyforwarder.RefusalAddressDenied {
		t.Fatalf("an operator pin granted an unpinned host: %q", got)
	}
	if e.lookups.Load() != 1 {
		t.Fatalf("lookups = %d", e.lookups.Load())
	}
}
