package service

import (
	"encoding/json"
	"errors"
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

func TestConfigPrivateNetworks(t *testing.T) {
	dir := canonicalTempDir(t)
	path := filepath.Join(dir, "service.json")
	cfg := configFixture(dir)
	writePrivate(t, path, cfg)
	loaded, err := LoadConfig(path)
	if err != nil || len(loaded.PrivateNetworks) != 0 {
		t.Fatalf("absent block must mean an empty allowlist: %+v %v", loaded.PrivateNetworks, err)
	}
	cfg.PrivateNetworks = []appplatform.PrivateNetwork{{Prefix: "10.20.0.0/24", Ports: []int{443, 8080}, PlainHTTP: true}}
	writePrivate(t, path, cfg)
	loaded, err = LoadConfig(path)
	if err != nil || !reflect.DeepEqual(loaded.PrivateNetworks, cfg.PrivateNetworks) {
		t.Fatalf("valid allowlist: %+v %v", loaded.PrivateNetworks, err)
	}
	base, _ := json.Marshal(configFixture(dir))
	with := func(block string) string { return string(base[:len(base)-1]) + `,"private_networks":` + block + `}` }
	for name, raw := range map[string]string{
		"unknown entry key": with(`[{"prefix":"10.20.0.0/24","ports":[443],"plain_http":false,"allow_all":true}]`),
		"duplicate key":     with(`[{"prefix":"10.20.0.0/24","prefix":"10.21.0.0/24","ports":[443],"plain_http":false}]`),
		"upper case key":    with(`[{"Prefix":"10.20.0.0/24","ports":[443],"plain_http":false}]`),
		"not an array":      with(`{"prefix":"10.20.0.0/24"}`),
		"string port":       with(`[{"prefix":"10.20.0.0/24","ports":["443"],"plain_http":false}]`),
		"public prefix":     with(`[{"prefix":"8.8.0.0/16","ports":[443],"plain_http":false}]`),
		"too wide":          with(`[{"prefix":"10.0.0.0/8","ports":[443],"plain_http":false}]`),
		"loopback":          with(`[{"prefix":"127.0.0.0/16","ports":[443],"plain_http":false}]`),
		"overlap":           with(`[{"prefix":"10.20.0.0/24","ports":[443],"plain_http":false},{"prefix":"10.20.0.0/25","ports":[443],"plain_http":false}]`),
		"no ports":          with(`[{"prefix":"10.20.0.0/24","ports":[],"plain_http":false}]`),
		"null entry":        with(`[null]`),
	} {
		t.Run(name, func(t *testing.T) {
			if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadConfig(path); err == nil {
				t.Fatal("invalid private_networks accepted")
			}
		})
	}
}

func lister(ifaces ...HostInterface) InterfaceLister {
	return func() ([]HostInterface, error) { return ifaces, nil }
}

func iface(name string, prefixes ...string) HostInterface {
	h := HostInterface{Name: name}
	for _, p := range prefixes {
		h.Addrs = append(h.Addrs, netip.MustParsePrefix(p))
	}
	return h
}

func TestCheckPrivateNetworksAgainstHost(t *testing.T) {
	net24 := []appplatform.PrivateNetwork{{Prefix: "10.20.0.0/24", Ports: []int{443}}}
	host := []HostInterface{iface("lo", "127.0.0.1/8", "::1/128"), iface("eth0", "147.135.70.140/24", "fe80::1/64")}
	if err := CheckPrivateNetworksAgainstHost(net24, lister(host...)); err != nil {
		t.Fatalf("clean host refused: %v", err)
	}
	called := false
	if err := CheckPrivateNetworksAgainstHost(nil, func() ([]HostInterface, error) { called = true; return nil, errors.New("boom") }); err != nil || called {
		t.Fatalf("an empty allowlist must not consult the host: %v %v", err, called)
	}
	refused := map[string][]HostInterface{
		"owns an address in the prefix":   {iface("eth1", "10.20.0.7/16")},
		"prefix equals an interface /32":  {iface("eth1", "10.20.0.0/32")},
		"docker0 overlaps":                {iface("docker0", "10.20.0.1/25")},
		"docker0 wider than the prefix":   {iface("docker0", "10.0.0.1/8")},
		"user bridge":                     {iface("br-1a2b3c4d5e6f", "10.20.0.129/25")},
		"libvirt bridge":                  {iface("virbr0", "10.20.0.1/24")},
		"cni":                             {iface("cni0", "10.20.0.1/24")},
		"cni with suffix":                 {iface("cni-podman0", "10.20.0.1/24")},
		"podman":                          {iface("podman0", "10.20.0.1/24")},
		"lxc":                             {iface("lxcbr0", "10.20.0.1/24")},
		"lxd":                             {iface("lxdbr0", "10.20.0.1/24")},
		"incus":                           {iface("incusbr0", "10.20.0.1/24")},
		"proxmox":                         {iface("vmbr0", "10.20.0.1/24")},
		"bridge by kernel kind":           {HostInterface{Name: "mynet0", Bridge: true, Addrs: []netip.Prefix{netip.MustParsePrefix("10.20.0.1/24")}}},
		"second interface is the culprit": {iface("lo", "127.0.0.1/8"), iface("eth0", "10.20.0.200/24")},
	}
	for name, hosts := range refused {
		hosts := hosts
		if err := CheckPrivateNetworksAgainstHost(net24, lister(hosts...)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	// A bridge that does not touch the prefix, and a non-bridge interface whose
	// network overlaps but whose own address is outside the prefix, are fine.
	net6 := []appplatform.PrivateNetwork{{Prefix: "fd00:1234:5678::/48", Ports: []int{443}}}
	if err := CheckPrivateNetworksAgainstHost(net6, lister(iface("eth1", "fd00:1234:5678::1/64"))); err == nil {
		t.Error("v6 interface address inside the prefix accepted")
	}
	if err := CheckPrivateNetworksAgainstHost(net6, lister(iface("eth1", "fd00:9999::1/64"))); err != nil {
		t.Errorf("unrelated v6 interface refused: %v", err)
	}
	// The usual case: the LAN the prefix belongs to is the host's own network.
	if err := CheckPrivateNetworksAgainstHost(net24, lister(iface("docker0", "172.17.0.1/16"), iface("eth1", "10.20.1.5/16"))); err != nil {
		t.Errorf("LAN interface whose own address is outside the prefix refused: %v", err)
	}
	if err := CheckPrivateNetworksAgainstHost(net24, lister(iface("docker0", "172.17.0.1/16"), iface("eth1", "10.30.1.5/16"))); err != nil {
		t.Errorf("unrelated interfaces refused: %v", err)
	}
	if err := CheckPrivateNetworksAgainstHost(net24, func() ([]HostInterface, error) { return nil, errors.New("no netlink") }); err == nil {
		t.Error("an unreadable interface list must refuse start")
	}
}
