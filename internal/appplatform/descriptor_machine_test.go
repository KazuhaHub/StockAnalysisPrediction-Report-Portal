package appplatform

import "testing"

func machineBlock() map[string]any {
	return map[string]any{
		"routes": []any{
			map[string]any{"id": "margin-latest", "path": "/v1/machine/margin/latest", "query": []any{"date"}, "response_bytes": 262144, "timeout_ms": 5000, "description": "Latest margin figures"},
			map[string]any{"id": "sentiment", "path": "/v1/machine/sentiment", "query": []any{"from", "to"}, "response_bytes": 1048576, "timeout_ms": 10000},
		},
	}
}

func declareMachine(mutate func(map[string]any)) func(map[string]any) {
	return func(d map[string]any) {
		d["capabilities"] = map[string]any{"optional": []string{"machine.read"}}
		block := machineBlock()
		if mutate != nil {
			mutate(block)
		}
		d["machine"] = block
	}
}

func TestDescriptorAcceptsMachineRead(t *testing.T) {
	parsed, err := ParseDescriptor(mutateDescriptor(t, declareMachine(nil)))
	if err != nil {
		t.Fatal(err)
	}
	m := parsed.Manifest.Machine
	if m == nil || len(m.Routes) != 2 || m.Routes[0].TimeoutMS != 5000 || m.Routes[0].Description == "" || m.Routes[1].Query[1] != "to" {
		t.Fatalf("machine block was not decoded: %+v", m)
	}
	// Both audiences may be declared together; the path spaces stay separate.
	both := func(d map[string]any) {
		declareMachine(nil)(d)
		d["capabilities"] = map[string]any{"optional": []string{"machine.read", "public.projection"}}
		d["public"] = publicBlock()
	}
	if _, err := ParseDescriptor(mutateDescriptor(t, both)); err != nil {
		t.Fatal(err)
	}
}

func TestDescriptorRefusesMalformedMachineRead(t *testing.T) {
	cases := map[string]func(map[string]any){
		"unsorted ids":    func(b map[string]any) { b["routes"] = []any{routeAt(b, 1), routeAt(b, 0)} },
		"duplicate id":    func(b map[string]any) { routeAt(b, 1)["id"] = "margin-latest" },
		"duplicate path":  func(b map[string]any) { routeAt(b, 1)["path"] = "/v1/machine/margin/latest" },
		"outside machine": func(b map[string]any) { routeAt(b, 0)["path"] = "/v1/public/margin" },
		"handshake":       func(b map[string]any) { routeAt(b, 0)["path"] = "/v1/machine/../handshake" },
		"traversal":       func(b map[string]any) { routeAt(b, 0)["path"] = "/v1/machine/a/../b" },
		"query in path":   func(b map[string]any) { routeAt(b, 0)["path"] = "/v1/machine/margin?x=1" },
		"unsorted query":  func(b map[string]any) { routeAt(b, 1)["query"] = []any{"to", "from"} },
		"bad query name":  func(b map[string]any) { routeAt(b, 0)["query"] = []any{"Date"} },
		"too many queries": func(b map[string]any) {
			routeAt(b, 0)["query"] = []any{"a", "b", "c", "d", "e", "f", "g", "h", "i", "j", "k", "l", "m", "n", "o", "p", "q"}
		},
		"response too big": func(b map[string]any) { routeAt(b, 0)["response_bytes"] = 1048577 },
		"response zero":    func(b map[string]any) { routeAt(b, 0)["response_bytes"] = 0 },
		"timeout short":    func(b map[string]any) { routeAt(b, 0)["timeout_ms"] = 99 },
		"timeout long":     func(b map[string]any) { routeAt(b, 0)["timeout_ms"] = 30001 },
		"no routes":        func(b map[string]any) { b["routes"] = []any{} },
		"long description": func(b map[string]any) { routeAt(b, 0)["description"] = string(make([]byte, 201)) },
		"control in text":  func(b map[string]any) { routeAt(b, 0)["description"] = "a\nb" },
		"unknown field":    func(b map[string]any) { b["methods"] = []any{"POST"} },
		"unknown route":    func(b map[string]any) { routeAt(b, 0)["method"] = "POST" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseDescriptor(mutateDescriptor(t, declareMachine(mutate))); err == nil {
				t.Fatal("accepted")
			}
		})
	}
	var many []any
	for i := range 33 {
		id := "r" + string(rune('a'+i/26)) + string(rune('a'+i%26))
		many = append(many, map[string]any{"id": id, "path": "/v1/machine/" + id, "response_bytes": 1, "timeout_ms": 100})
	}
	if _, err := ParseDescriptor(mutateDescriptor(t, declareMachine(func(b map[string]any) { b["routes"] = many }))); err == nil {
		t.Fatal("33 routes accepted")
	}
}

func TestDescriptorMachineReadNeedsBackendAndCapabilityTogether(t *testing.T) {
	cases := map[string]func(map[string]any){
		"block without capability": func(d map[string]any) {
			declareMachine(nil)(d)
			d["capabilities"] = map[string]any{"optional": []string{"reports.read"}}
		},
		"capability without block": func(d map[string]any) {
			d["capabilities"] = map[string]any{"optional": []string{"machine.read"}}
		},
		"required capability": func(d map[string]any) {
			declareMachine(nil)(d)
			d["capabilities"] = map[string]any{"required": []string{"machine.read"}}
		},
		"no backend": func(d map[string]any) {
			declareMachine(nil)(d)
			delete(d, "backend")
			delete(d, "data")
		},
		"null block": func(d map[string]any) {
			d["capabilities"] = map[string]any{"optional": []string{"machine.read"}}
			d["machine"] = nil
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
