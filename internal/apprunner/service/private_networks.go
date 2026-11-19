package service

import (
	"net"
	"net/netip"
	"strings"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

// HostInterface is one network interface of this host and the addresses (with
// their masks) assigned to it.
type HostInterface struct {
	Name  string
	Addrs []netip.Prefix
}

// InterfaceLister reads the host's interfaces. It is injectable so the startup
// refusal can be tested without a real host.
type InterfaceLister func() ([]HostInterface, error)

// bridgeInterface matches the container and VM bridges a private prefix must
// never overlap, because the NAT gateway of such a bridge is the host itself.
func bridgeInterface(name string) bool {
	for _, prefix := range []string{"docker0", "br-", "virbr", "cni", "podman"} {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

// CheckPrivateNetworksAgainstHost refuses an allowlist that would let a reviewed
// target reach this host: a prefix containing an address of any host interface,
// or overlapping the network of a bridge interface (docker0, br-*, virbr*, cni*,
// podman*). An interface list that cannot be read refuses too. Interfaces
// created after start are not seen here; the executor checks a fresh snapshot
// on each call. An empty allowlist needs no host reading.
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
			bridge := bridgeInterface(h.Name)
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
func SystemInterfaces() ([]HostInterface, error) {
	all, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	out := make([]HostInterface, 0, len(all))
	for _, i := range all {
		addrs, err := i.Addrs()
		if err != nil {
			return nil, err
		}
		h := HostInterface{Name: i.Name}
		for _, a := range addrs {
			if p, err := netip.ParsePrefix(a.String()); err == nil {
				h.Addrs = append(h.Addrs, p)
			}
		}
		out = append(out, h)
	}
	return out, nil
}
