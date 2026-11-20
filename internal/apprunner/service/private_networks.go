package service

import (
	"net/netip"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/hostnet"
)

// HostInterface is one network interface of this host and the addresses (with
// their masks) assigned to it.
type HostInterface = hostnet.Interface

// InterfaceLister reads the host's interfaces. It is injectable so the startup
// refusal can be tested without a real host.
type InterfaceLister func() ([]HostInterface, error)

// CheckPrivateNetworksAgainstHost refuses an allowlist that would let a reviewed
// target reach this host, judged on the interfaces that exist at start:
//   - any prefix that contains an address assigned to any interface;
//   - any prefix that overlaps the subnet of a bridge interface, by name
//     (docker0, br-*, virbr*, cni*, podman*, lxcbr*, lxdbr*, incusbr*, vmbr*) or
//     by kernel kind (hostnet.Interface.IsBridge).
//
// An interface list that cannot be read refuses too. An empty allowlist needs no
// host reading. Interfaces created after start are not seen here: the executor
// takes its own snapshot (at most 5 s old) on every private call and denies the
// host's addresses and any address inside a bridge's subnet at that time.
func CheckPrivateNetworksAgainstHost(networks []appplatform.PrivateNetwork, list InterfaceLister) error {
	if len(networks) == 0 {
		return nil
	}
	if list == nil {
		return ErrConfig
	}
	interfaces, err := list()
	if err != nil {
		return ErrConfig
	}
	for _, n := range networks {
		prefix, err := netip.ParsePrefix(n.Prefix)
		if err != nil {
			return ErrConfig
		}
		for _, h := range interfaces {
			bridge := h.IsBridge()
			for _, a := range h.Addrs {
				if prefix.Contains(a.Addr().Unmap()) || bridge && prefix.Overlaps(a.Masked()) {
					return ErrConfig
				}
			}
		}
	}
	return nil
}

// SystemInterfaces lists the real host interfaces.
func SystemInterfaces() ([]HostInterface, error) { return hostnet.List() }
