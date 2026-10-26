package appplatform

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestDescriptorDisplayMetadata(t *testing.T) {
	raw, err := os.ReadFile("testdata/descriptors/static.json")
	if err != nil {
		t.Fatal(err)
	}
	var root map[string]any
	if err := json.Unmarshal(raw, &root); err != nil {
		t.Fatal(err)
	}
	presentation := root["presentation"].(map[string]any)
	good := func() map[string]any {
		return map[string]any{"icon": "file-text", "texts": []any{
			map[string]any{"locale": "en-US", "name": "Notes", "description": "Personal notes"},
			map[string]any{"locale": "zh-CN", "name": "笔记", "description": "个人笔记"},
			map[string]any{"locale": "zh-TW", "name": "筆記", "description": "個人筆記"},
		}}
	}
	cases := []struct {
		name   string
		mutate func(map[string]any)
		valid  bool
	}{
		{"complete", func(map[string]any) {}, true},
		{"missing locale", func(d map[string]any) { d["texts"] = d["texts"].([]any)[:2] }, false},
		{"duplicate locale", func(d map[string]any) { d["texts"].([]any)[1].(map[string]any)["locale"] = "en-US" }, false},
		{"external icon", func(d map[string]any) { d["icon"] = "https://example.invalid/icon.svg" }, false},
		{"unknown field", func(d map[string]any) { d["html"] = "<b>Notes</b>" }, false},
		{"whitespace name", func(d map[string]any) { d["texts"].([]any)[0].(map[string]any)["name"] = " Notes" }, false},
		{"bidi name", func(d map[string]any) { d["texts"].([]any)[0].(map[string]any)["name"] = "Notes\u202e" }, false},
		{"long name", func(d map[string]any) { d["texts"].([]any)[0].(map[string]any)["name"] = strings.Repeat("笔", 81) }, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := good()
			c.mutate(d)
			presentation["display"] = d
			b, err := json.Marshal(root)
			if err != nil {
				t.Fatal(err)
			}
			_, err = ParseDescriptor(b)
			if (err == nil) != c.valid {
				t.Fatalf("display validity=%v error=%v", c.valid, err)
			}
		})
	}
}
