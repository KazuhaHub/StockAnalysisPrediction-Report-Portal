package appplatform

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSharedDescriptorCorpus(t *testing.T) {
	raw, err := os.ReadFile("testdata/descriptor-corpus.json")
	if err != nil {
		t.Fatal(err)
	}
	var corpus struct {
		Format string `json:"format"`
		Cases  []struct {
			Name  string `json:"name"`
			Input string `json:"input_base64"`
			Valid bool   `json:"valid"`
		} `json:"cases"`
	}
	sum := sha256.Sum256(raw)
	if hex.EncodeToString(sum[:]) != "fd840edca492f2e0a1ffab89b13ae0da8c626ccd7a24860f8b7f1c8a0c39e3ec" {
		t.Fatal("shared corpus revision differs from SDK")
	}
	if err := json.Unmarshal(raw, &corpus); err != nil {
		t.Fatal(err)
	}
	if corpus.Format != "portal.descriptor.corpus/1" || len(corpus.Cases) != 56 {
		t.Fatal("unexpected shared corpus")
	}
	for _, item := range corpus.Cases {
		t.Run(item.Name, func(t *testing.T) {
			input, err := base64.StdEncoding.Strict().DecodeString(item.Input)
			if err != nil {
				t.Fatal(err)
			}
			_, err = ParseDescriptor(input)
			if (err == nil) != item.Valid {
				t.Fatalf("acceptance differs: valid=%v error=%v", item.Valid, err)
			}
		})
	}
}

func descriptorFixture(t *testing.T, managed bool) []byte {
	t.Helper()
	name := "static.json"
	if managed {
		name = "managed.json"
	}
	raw, err := os.ReadFile(filepath.Join("testdata", "descriptors", name))
	if err != nil {
		t.Fatal(err)
	}
	// Normalize only fixture whitespace so mutation cases have stable targets.
	var compact bytes.Buffer
	if err := json.Compact(&compact, raw); err != nil {
		t.Fatal(err)
	}
	return compact.Bytes()
}

func mutateDescriptor(t *testing.T, fn func(map[string]any)) []byte {
	t.Helper()
	var d map[string]any
	if err := json.Unmarshal(descriptorFixture(t, true), &d); err != nil {
		t.Fatal(err)
	}
	fn(d)
	raw, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestDescriptorAcceptsStaticAndManagedAndBindsExactBytes(t *testing.T) {
	for _, managed := range []bool{false, true} {
		raw := descriptorFixture(t, managed)
		d, err := ParseDescriptor(raw)
		if err != nil {
			t.Fatal(err)
		}
		if d.Manifest.ID != "notes-sample" || (d.Manifest.Backend != nil) != managed {
			t.Fatalf("unexpected manifest: %+v", d.Manifest)
		}
		sum := sha256.Sum256(raw)
		if d.SHA256 != hex.EncodeToString(sum[:]) {
			t.Fatal("descriptor digest is not bound to exact input bytes")
		}
		other, err := ParseDescriptor(append([]byte("\n"), raw...))
		if err != nil || other.SHA256 == d.SHA256 {
			t.Fatal("distinct descriptor bytes reused the same review identity")
		}
	}
}

func TestDescriptorRejectsAmbiguousAndUnboundedJSON(t *testing.T) {
	raw := descriptorFixture(t, true)
	cases := map[string][]byte{
		"empty":             nil,
		"array":             []byte("[]"),
		"duplicate":         bytes.Replace(raw, []byte(`"id":"notes-sample"`), []byte(`"id":"notes-sample","id":"other-app"`), 1),
		"escaped duplicate": bytes.Replace(raw, []byte(`"id":"notes-sample"`), []byte(`"id":"notes-sample","\u0069d":"other-app"`), 1),
		"nested duplicate":  bytes.Replace(raw, []byte(`"kind":"oci"`), []byte(`"kind":"oci","kind":"other"`), 1),
		"case alias":        bytes.Replace(raw, []byte(`"format":`), []byte(`"Format":`), 1),
		"unknown nested":    bytes.Replace(raw, []byte(`"kind":"oci"`), []byte(`"kind":"oci","command":"shell"`), 1),
		"trailing object":   append(append([]byte{}, raw...), []byte(" {}")...),
		"too large":         bytes.Repeat([]byte(" "), MaxDescriptorBytes+1),
		"too deep":          []byte(strings.Repeat(`{"x":`, 40) + "0" + strings.Repeat("}", 40)),
		"invalid utf8":      append([]byte{0xff}, raw...),
	}
	for name, input := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseDescriptor(input); !errors.Is(err, ErrInvalidDescriptor) {
				t.Fatalf("expected bounded invalid-descriptor error, got %v", err)
			}
		})
	}
}

func TestDescriptorRejectsUnsafeDeclarations(t *testing.T) {
	cases := map[string]func(map[string]any){
		"legacy format":         func(d map[string]any) { d["format"] = "portal.app/v1" },
		"invalid identity":      func(d map[string]any) { d["id"] = "../another" },
		"nonversion":            func(d map[string]any) { d["version"] = "latest" },
		"leading zero":          func(d map[string]any) { d["version"] = "01.0.0" },
		"missing compatibility": func(d map[string]any) { delete(d, "compatibility") },
		"bad hash":              func(d map[string]any) { d["frontend"].(map[string]any)["sha256"] = "not-a-digest" },
		"path escape":           func(d map[string]any) { d["presentation"].(map[string]any)["entry"] = "../index.html" },
		"encoded path":          func(d map[string]any) { d["frontend"].(map[string]any)["artifact"] = "%2e%2e/ui.zip" },
		"missing locale":        func(d map[string]any) { d["presentation"].(map[string]any)["locales"] = []string{"en-US"} },
		"route escape": func(d map[string]any) {
			d["presentation"].(map[string]any)["routes"] = []any{map[string]any{"id": "home", "path": "//outside", "layout": "document"}}
		},
		"duplicate route": func(d map[string]any) {
			p := d["presentation"].(map[string]any)
			p["routes"] = []any{map[string]any{"id": "home", "path": "/", "layout": "document"}, map[string]any{"id": "home", "path": "/edit", "layout": "viewport"}}
		},
		"unknown layout": func(d map[string]any) {
			d["presentation"].(map[string]any)["routes"] = []any{map[string]any{"id": "home", "path": "/", "layout": "host-dom"}}
		},
		"mutable image":              func(d map[string]any) { d["backend"].(map[string]any)["image"] = "ghcr.io/example/digest:beta" },
		"unknown backend":            func(d map[string]any) { d["backend"].(map[string]any)["kind"] = "native-shell" },
		"unsupported platform":       func(d map[string]any) { d["backend"].(map[string]any)["platforms"] = []string{"linux/unknown"} },
		"unbounded memory":           func(d map[string]any) { d["backend"].(map[string]any)["resources"].(map[string]any)["memory_mib"] = 0 },
		"unknown capability":         func(d map[string]any) { d["capabilities"].(map[string]any)["required"] = []string{"host.exec"} },
		"duplicate capability":       func(d map[string]any) { d["capabilities"].(map[string]any)["optional"] = []string{"storage.files"} },
		"background without backend": func(d map[string]any) { delete(d, "backend") },
		"required job optional background": func(d map[string]any) {
			d["capabilities"] = map[string]any{"required": []string{"tasks.schedule"}, "optional": []string{"background.run"}}
		},
		"startup migration":     func(d map[string]any) { d["data"].(map[string]any)["writes_on_startup"] = true },
		"unreadable own schema": func(d map[string]any) { d["data"].(map[string]any)["readable_schemas"] = []int{2} },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := ParseDescriptor(mutateDescriptor(t, mutate))
			if !errors.Is(err, ErrInvalidDescriptor) {
				t.Fatalf("expected refusal, got %v", err)
			}
		})
	}
}

func FuzzDescriptorParser(f *testing.F) {
	for _, name := range []string{"static.json", "managed.json"} {
		raw, err := os.ReadFile(filepath.Join("testdata", "descriptors", name))
		if err != nil {
			f.Fatal(err)
		}
		f.Add(raw)
	}
	f.Add([]byte(`{"format":"portal.app/v2"}`))
	f.Add([]byte(`{"id":"first","\u0069d":"second"}`))
	f.Add([]byte(`{"backend":{"resources":[[[null]]]}}`))
	f.Fuzz(func(t *testing.T, raw []byte) {
		parsed, err := ParseDescriptor(raw)
		if err != nil {
			if !errors.Is(err, ErrInvalidDescriptor) || parsed.Manifest.ID != "" || parsed.SHA256 != "" {
				t.Fatal("refusal exposed a partial descriptor or unstable error")
			}
			return
		}
		sum := sha256.Sum256(raw)
		if parsed.SHA256 != hex.EncodeToString(sum[:]) {
			t.Fatal("accepted descriptor identity diverged")
		}
	})
}

func TestDescriptorTypedRouteQueryRules(t *testing.T) {
	var fixture map[string]any
	if err := json.Unmarshal(descriptorFixture(t, false), &fixture); err != nil {
		t.Fatal(err)
	}
	routes := fixture["presentation"].(map[string]any)["routes"].([]any)
	valid := []any{map[string]any{"name": "page", "type": "integer", "min": 1, "max": 100}, map[string]any{"name": "q", "type": "text", "max_bytes": 64}, map[string]any{"name": "active", "type": "boolean"}, map[string]any{"name": "sort", "type": "enum", "values": []string{"newest", "oldest"}}}
	routes[0].(map[string]any)["query"] = valid
	raw, _ := json.Marshal(fixture)
	if _, err := ParseDescriptor(raw); err != nil {
		t.Fatal("valid signed query declaration refused", err)
	}
	for _, bad := range []any{[]any{valid[0], valid[0]}, []any{map[string]any{"name": "x", "type": "integer", "min": 2, "max": 1}}, []any{map[string]any{"name": "x", "type": "boolean", "min": 0}}, []any{map[string]any{"name": "x", "type": "text", "max_bytes": nil}}, []any{map[string]any{"name": "x", "type": "enum", "values": []string{"a", "a"}}}} {
		routes[0].(map[string]any)["query"] = bad
		raw, _ := json.Marshal(fixture)
		if _, err := ParseDescriptor(raw); err == nil {
			t.Fatalf("invalid query declaration accepted: %s", raw)
		}
	}
}
