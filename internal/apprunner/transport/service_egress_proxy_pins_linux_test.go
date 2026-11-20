//go:build linux

package transport

import (
	"context"
	"net"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform/proxyforwarder"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
)

// pinnedProxyOptions are the options of a session whose policy holds hosts and one pin.
func pinnedProxyOptions(t *testing.T, resources []string, pin appplatform.ProxyPin, hosts ...string) egrOptions {
	t.Helper()
	raw, err := appplatform.CanonicalProxyPolicy(appplatform.ProxyPolicy{Hosts: hosts, Pins: []appplatform.ProxyPin{pin}, DailyBytes: appplatform.ProxyDailyBytesMax})
	if err != nil {
		t.Fatal(err)
	}
	proxy := control.NewServiceEgressProxy(raw)
	return egrOptions{resources: resources, mutate: func(c *control.ServiceEgressSessionCommand) {
		c.Format = control.ServiceEgressSessionProxyFormat
		c.Proxy = &proxy
	}}
}

// A session whose signed policy carries a pin opens a tunnel to the pinned address with no resolution, when the
// runner's allowlist holds the exact entry: the path from the session command through the registry to the
// broker, which is how production reaches it. Fails closed without the allowlist entry.
func TestPinnedPolicyInASessionReachesThePinnedAddress(t *testing.T) {
	pin := appplatform.ProxyPin{Host: "quotes.internal.anchan.kazuhahub.com", Address: "127.0.0.1", Port: 20443}
	f := newEgrFixture(t)
	f.proxySeams()
	var lookups atomic.Int32
	f.registry.proxy.Lookup = func(context.Context, string) ([]netip.Addr, error) {
		lookups.Add(1)
		return []netip.Addr{netip.MustParseAddr("93.184.216.34")}, nil
	}
	var dialed atomic.Value
	dial := f.registry.proxy.Dial
	f.registry.proxy.Dial = func(ctx context.Context, network, address string) (net.Conn, error) {
		dialed.Store(address)
		return dial(ctx, network, address)
	}
	f.registry.applyProxyPins([]appplatform.ProxyPin{pin})
	f.open(pinnedProxyOptions(t, proxyAndHTTP, pin, "qt.gtimg.cn"))

	conn, refusal := f.tunnel("1", pin.Host, 443)
	if refusal != proxyforwarder.RefusalNone {
		t.Fatalf("refused: %s", refusal)
	}
	if body := tunnelGet(t, conn, pin.Host); !strings.Contains(body, "hello from upstream") {
		t.Fatalf("no response through the pinned tunnel: %q", body)
	}
	if got, _ := dialed.Load().(string); got != "127.0.0.1:20443" {
		t.Fatalf("dialled %q", got)
	}
	if lookups.Load() != 0 {
		t.Fatalf("the pinned name was resolved %d times", lookups.Load())
	}
	// An ordinary host of the same policy still resolves.
	if _, refusal := f.tunnel("1", "qt.gtimg.cn", 443); refusal != proxyforwarder.RefusalNone || lookups.Load() != 1 {
		t.Fatalf("ordinary host: %q, %d lookups", refusal, lookups.Load())
	}
	// The operator's list shrinks (a restart): the same signed policy now meets address_denied.
	f.registry.applyProxyPins(nil)
	if _, refusal := f.tunnel("1", pin.Host, 443); refusal != proxyforwarder.RefusalAddressDenied {
		t.Fatalf("a pin the operator removed: %q", refusal)
	}
}
