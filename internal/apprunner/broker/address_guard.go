package broker

import (
	"net/netip"
	"sync"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/hostnet"
)

// addressGuard is the address policy every runner-side dial shares: the HTTP executor and the
// PostgreSQL liveness probe apply the same class rules to every resolved address.
type addressGuard struct {
	networks []appplatform.PrivateNetwork
	// interfaces lists this host's interfaces. A private call reads a snapshot at
	// most hostAddressTTL old and denies every address assigned to any interface
	// and every address inside the subnet of a bridge interface (hostnet's
	// definition, the same as the startup refusal), so a bridge created after the
	// runner started is covered once the snapshot is refreshed.
	interfaces func() ([]hostnet.Interface, error)
	now        func() time.Time
	hostMu     sync.Mutex
	hostAt     time.Time
	hostIfaces []hostnet.Interface
	hostValid  bool
}

// hostAddressTTL bounds how stale the interface snapshot of a private call is.
const hostAddressTTL = 5 * time.Second

// hostSnapshot is the interface snapshot, reused for hostAddressTTL.
func (g *addressGuard) hostSnapshot() ([]hostnet.Interface, bool) {
	g.hostMu.Lock()
	defer g.hostMu.Unlock()
	if g.hostValid && g.now().Sub(g.hostAt) < hostAddressTTL {
		return g.hostIfaces, true
	}
	if g.interfaces == nil {
		return nil, false
	}
	list, err := g.interfaces()
	if err != nil {
		g.hostValid = false
		return nil, false
	}
	g.hostIfaces, g.hostAt, g.hostValid = list, g.now(), true
	return list, true
}

// onHost reports whether ip is an address of this host or inside a bridge subnet.
func onHost(interfaces []hostnet.Interface, ip netip.Addr) bool {
	for _, i := range interfaces {
		bridge := i.IsBridge()
		for _, a := range i.Addrs {
			if appplatform.Unmap(a.Addr()) == ip || bridge && a.Masked().Contains(ip) {
				return true
			}
		}
	}
	return false
}

// addressesAllowed applies a target's class to every address. Public targets
// keep the public-only rule. Private targets admit only addresses the runner's
// allowlist admits for the port and scheme, never an always-denied address and
// never one of this host's own or inside a bridge's subnet (per call, from the
// snapshot); a public address is in no allowlist, so a mixed answer is denied
// as a whole.
func (g *addressGuard) addressesAllowed(private bool, port uint16, scheme string, addresses []netip.Addr) bool {
	if !private {
		for _, ip := range addresses {
			if !publicAddress(ip) {
				return false
			}
		}
		return true
	}
	host, ok := g.hostSnapshot()
	if !ok {
		return false
	}
	for _, ip := range addresses {
		ip = appplatform.Unmap(ip)
		if appplatform.AlwaysDenied(ip) || !appplatform.Admit(g.networks, ip, port, scheme) || onHost(host, ip) {
			return false
		}
	}
	return true
}
