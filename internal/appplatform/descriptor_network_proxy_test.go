package appplatform

import "testing"

func declareProxy(mutate func(map[string]any)) func(map[string]any) {
	return func(d map[string]any) {
		d["capabilities"] = map[string]any{"required": []string{"background.run"}, "optional": []string{"network.proxy"}}
		block := map[string]any{"hosts": []any{"*.eastmoney.com", "qt.gtimg.cn"}}
		if mutate != nil {
			mutate(block)
		}
		d["network_proxy"] = block
	}
}

func TestDescriptorAcceptsNetworkProxy(t *testing.T) {
	parsed, err := ParseDescriptor(mutateDescriptor(t, declareProxy(nil)))
	if err != nil {
		t.Fatal(err)
	}
	p := parsed.Manifest.NetworkProxy
	if p == nil || len(p.Hosts) != 2 || p.Hosts[0] != "*.eastmoney.com" {
		t.Fatalf("network_proxy block was not decoded: %+v", p)
	}
	both := func(d map[string]any) {
		declareProxy(nil)(d)
		d["capabilities"] = map[string]any{"optional": []string{"background.run", "network.proxy"}}
	}
	if _, err := ParseDescriptor(mutateDescriptor(t, both)); err != nil {
		t.Fatal("background.run may be optional too:", err)
	}
}

func TestDescriptorRefusesMalformedNetworkProxy(t *testing.T) {
	cases := map[string]func(map[string]any){
		"unsorted":        func(b map[string]any) { b["hosts"] = []any{"qt.gtimg.cn", "*.eastmoney.com"} },
		"duplicate":       func(b map[string]any) { b["hosts"] = []any{"qt.gtimg.cn", "qt.gtimg.cn"} },
		"public suffix":   func(b map[string]any) { b["hosts"] = []any{"*.com"} },
		"github.io":       func(b map[string]any) { b["hosts"] = []any{"*.github.io"} },
		"ip literal":      func(b map[string]any) { b["hosts"] = []any{"1.2.3.4"} },
		"upper case":      func(b map[string]any) { b["hosts"] = []any{"QT.gtimg.cn"} },
		"empty":           func(b map[string]any) { b["hosts"] = []any{} },
		"null":            func(b map[string]any) { b["hosts"] = nil },
		"unknown field":   func(b map[string]any) { b["ports"] = []any{8443} },
		"too many hosts":  func(b map[string]any) { b["hosts"] = manyProxyHosts(65) },
		"missing hosts":   func(b map[string]any) { delete(b, "hosts") },
		"non string host": func(b map[string]any) { b["hosts"] = []any{443} },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseDescriptor(mutateDescriptor(t, declareProxy(mutate))); err == nil {
				t.Fatal("accepted")
			}
		})
	}
	if _, err := ParseDescriptor(mutateDescriptor(t, declareProxy(func(b map[string]any) { b["hosts"] = manyProxyHosts(64) }))); err != nil {
		t.Fatal("64 hosts must be accepted:", err)
	}
}

func manyProxyHosts(n int) []any {
	var hosts []any
	for i := 0; i < n; i++ {
		hosts = append(hosts, "h"+string(rune('a'+i/26))+string(rune('a'+i%26))+".example.com")
	}
	return hosts
}

func TestDescriptorNetworkProxyNeedsBackendBackgroundAndCapabilityTogether(t *testing.T) {
	cases := map[string]func(map[string]any){
		"block without capability": func(d map[string]any) {
			declareProxy(nil)(d)
			d["capabilities"] = map[string]any{"required": []string{"background.run"}}
		},
		"capability without block": func(d map[string]any) {
			d["capabilities"] = map[string]any{"required": []string{"background.run"}, "optional": []string{"network.proxy"}}
		},
		"required capability": func(d map[string]any) {
			declareProxy(nil)(d)
			d["capabilities"] = map[string]any{"required": []string{"background.run", "network.proxy"}}
		},
		"without background.run": func(d map[string]any) {
			declareProxy(nil)(d)
			d["capabilities"] = map[string]any{"optional": []string{"network.proxy"}}
		},
		"no backend": func(d map[string]any) {
			declareProxy(nil)(d)
			delete(d, "backend")
			delete(d, "data")
		},
		"null block": func(d map[string]any) {
			d["capabilities"] = map[string]any{"required": []string{"background.run"}, "optional": []string{"network.proxy"}}
			d["network_proxy"] = nil
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseDescriptor(mutateDescriptor(t, mutate)); err == nil {
				t.Fatal("accepted")
			}
		})
	}
}
