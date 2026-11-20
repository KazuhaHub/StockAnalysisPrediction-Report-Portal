package appplatform

import (
	"strings"
	"testing"
)

func publicBlock() map[string]any {
	return map[string]any{
		"routes": []any{
			map[string]any{"id": "history", "path": "/v1/public/history", "query": []any{"from", "to"}, "cache_seconds": 10, "response_bytes": 262144},
			map[string]any{"id": "status", "path": "/v1/public/status", "cache_seconds": 10, "response_bytes": 1048576},
		},
		"document": "public/index.html",
	}
}

func declarePublic(mutate func(map[string]any)) func(map[string]any) {
	return func(d map[string]any) {
		d["capabilities"] = map[string]any{"optional": []string{"public.projection"}}
		block := publicBlock()
		if mutate != nil {
			mutate(block)
		}
		d["public"] = block
	}
}

func routeAt(b map[string]any, i int) map[string]any { return b["routes"].([]any)[i].(map[string]any) }

func TestDescriptorAcceptsPublicProjection(t *testing.T) {
	parsed, err := ParseDescriptor(mutateDescriptor(t, declarePublic(nil)))
	if err != nil {
		t.Fatal(err)
	}
	p := parsed.Manifest.Public
	if p == nil || len(p.Routes) != 2 || p.Routes[0].ID != "history" || p.Routes[0].Query[1] != "to" || p.Document != "public/index.html" {
		t.Fatalf("public block was not decoded: %+v", p)
	}
	if _, err := ParseDescriptor(mutateDescriptor(t, declarePublic(func(b map[string]any) { delete(b, "document") }))); err != nil {
		t.Fatal("the document is optional", err)
	}
}

func TestDescriptorRefusesMalformedPublicProjection(t *testing.T) {
	cases := map[string]func(map[string]any){
		"unsorted ids":      func(b map[string]any) { b["routes"] = []any{routeAt(b, 1), routeAt(b, 0)} },
		"duplicate id":      func(b map[string]any) { routeAt(b, 1)["id"] = "history" },
		"duplicate path":    func(b map[string]any) { routeAt(b, 1)["path"] = "/v1/public/history" },
		"outside public":    func(b map[string]any) { routeAt(b, 0)["path"] = "/v1/admin/history" },
		"handshake":         func(b map[string]any) { routeAt(b, 0)["path"] = "/v1/public/../handshake" },
		"query in path":     func(b map[string]any) { routeAt(b, 0)["path"] = "/v1/public/history?x=1" },
		"unsorted query":    func(b map[string]any) { routeAt(b, 0)["query"] = []any{"to", "from"} },
		"duplicate query":   func(b map[string]any) { routeAt(b, 0)["query"] = []any{"from", "from"} },
		"bad query name":    func(b map[string]any) { routeAt(b, 0)["query"] = []any{"From"} },
		"cache zero":        func(b map[string]any) { routeAt(b, 0)["cache_seconds"] = 0 },
		"cache too long":    func(b map[string]any) { routeAt(b, 0)["cache_seconds"] = 301 },
		"response too big":  func(b map[string]any) { routeAt(b, 0)["response_bytes"] = 1048577 },
		"response zero":     func(b map[string]any) { routeAt(b, 0)["response_bytes"] = 0 },
		"no routes":         func(b map[string]any) { b["routes"] = []any{} },
		"document outside":  func(b map[string]any) { b["document"] = "index.html" },
		"document traverse": func(b map[string]any) { b["document"] = "public/../index.html" },
		"unknown field":     func(b map[string]any) { b["methods"] = []any{"POST"} },
		"unknown route key": func(b map[string]any) { routeAt(b, 0)["method"] = "POST" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseDescriptor(mutateDescriptor(t, declarePublic(mutate))); err == nil {
				t.Fatal("accepted")
			}
		})
	}
	tooMany := declarePublic(func(b map[string]any) {
		var routes []any
		for _, id := range []string{"a", "b", "c", "d", "e", "f", "g", "h", "i"} {
			routes = append(routes, map[string]any{"id": id, "path": "/v1/public/" + id, "cache_seconds": 1, "response_bytes": 1})
		}
		b["routes"] = routes
	})
	if _, err := ParseDescriptor(mutateDescriptor(t, tooMany)); err == nil {
		t.Fatal("nine routes accepted")
	}
}

func TestDescriptorPublicProjectionNeedsBackendAndCapabilityTogether(t *testing.T) {
	cases := map[string]func(map[string]any){
		"block without capability": func(d map[string]any) {
			declarePublic(nil)(d)
			d["capabilities"] = map[string]any{"optional": []string{"reports.read"}}
		},
		"capability without block": func(d map[string]any) {
			d["capabilities"] = map[string]any{"optional": []string{"public.projection"}}
		},
		"required capability": func(d map[string]any) {
			declarePublic(nil)(d)
			d["capabilities"] = map[string]any{"required": []string{"public.projection"}}
		},
		"no backend": func(d map[string]any) {
			declarePublic(nil)(d)
			delete(d, "backend")
			delete(d, "data")
		},
		"null block": func(d map[string]any) {
			d["capabilities"] = map[string]any{"optional": []string{"public.projection"}}
			d["public"] = nil
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

func TestDescriptorSchemaListsPublicProjectionAsOptionalOnly(t *testing.T) {
	raw := mutateDescriptor(t, declarePublic(nil))
	if !strings.Contains(string(raw), "public.projection") {
		t.Fatal("fixture")
	}
	if descriptorKnownCapabilities["public.projection"] != true {
		t.Fatal("capability is not in the generated domain")
	}
}
