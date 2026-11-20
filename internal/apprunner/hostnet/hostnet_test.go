package hostnet

import (
	"net/netip"
	"os"
	"path/filepath"
	"testing"
)

func TestIsBridgeName(t *testing.T) {
	for _, name := range []string{"docker0", "br-1a2b3c4d5e6f", "br-lan", "virbr0", "virbr1-nic", "cni0", "cni-podman0", "podman0", "podman1", "lxcbr0", "lxdbr0", "incusbr0", "vmbr0", "vmbr1"} {
		if !IsBridgeName(name) {
			t.Errorf("%s not a bridge name", name)
		}
	}
	for _, name := range []string{"lo", "eth0", "ens3", "wlan0", "tailscale0", "veth1234", "bridge0x", "brx", "docker1"} {
		if IsBridgeName(name) {
			t.Errorf("%s is a bridge name", name)
		}
	}
}

func TestBridgeKindFromSysfs(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "mybridge", "bridge"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "eth9"), 0o755); err != nil {
		t.Fatal(err)
	}
	if !bridgeKind(root, "mybridge") || bridgeKind(root, "eth9") || bridgeKind(root, "missing") || bridgeKind(root, "../mybridge") || bridgeKind(root, "") {
		t.Fatal("sysfs bridge detection")
	}
}

func TestInterfaceIsBridge(t *testing.T) {
	if !(Interface{Name: "mybridge", Bridge: true}).IsBridge() || !(Interface{Name: "vmbr0"}).IsBridge() || (Interface{Name: "eth0"}).IsBridge() {
		t.Fatal("IsBridge")
	}
}

func TestListReadsTheHost(t *testing.T) {
	all, err := List()
	if err != nil || len(all) == 0 {
		t.Skipf("no interfaces readable: %v", err)
	}
	for _, i := range all {
		for _, a := range i.Addrs {
			if !a.IsValid() || a.Addr() == (netip.Addr{}) {
				t.Fatalf("%s: invalid address", i.Name)
			}
		}
	}
}
