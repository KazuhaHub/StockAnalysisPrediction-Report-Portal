// Package hostnet reads this host's network interfaces and decides which are
// bridges. The runner uses it at start and the broker on every private call, so
// both apply one definition of "a bridge".
package hostnet

import (
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
)

// Interface is one interface and the addresses (with masks) assigned to it.
// Bridge is set by List when the kernel reports the interface as a bridge.
type Interface struct {
	Name   string
	Addrs  []netip.Prefix
	Bridge bool
}

// bridgePrefixes are the name prefixes of container and VM bridges.
var bridgePrefixes = []string{"docker0", "br-", "virbr", "cni", "podman", "lxcbr", "lxdbr", "incusbr", "vmbr"}

// IsBridgeName reports whether the name looks like a container or VM bridge.
func IsBridgeName(name string) bool {
	for _, p := range bridgePrefixes {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

// IsBridge is true for a bridge by name or by kernel kind.
func (i Interface) IsBridge() bool { return i.Bridge || IsBridgeName(i.Name) }

const sysfsNet = "/sys/class/net"

// bridgeKind reports whether <root>/<name>/bridge exists, which the kernel
// creates for bridge devices. It is one stat, cheap enough for each snapshot.
func bridgeKind(root, name string) bool {
	if name == "" || name != filepath.Base(name) || name == "." || name == ".." {
		return false
	}
	info, err := os.Stat(filepath.Join(root, name, "bridge"))
	return err == nil && info.IsDir()
}

// List reads the current interfaces.
func List() ([]Interface, error) {
	all, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	out := make([]Interface, 0, len(all))
	for _, i := range all {
		addrs, err := i.Addrs()
		if err != nil {
			return nil, err
		}
		h := Interface{Name: i.Name, Bridge: bridgeKind(sysfsNet, i.Name)}
		for _, a := range addrs {
			if p, err := netip.ParsePrefix(a.String()); err == nil {
				h.Addrs = append(h.Addrs, p)
			}
		}
		out = append(out, h)
	}
	return out, nil
}
