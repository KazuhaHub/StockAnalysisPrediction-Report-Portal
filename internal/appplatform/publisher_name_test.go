package appplatform

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestDescriptorPublisherName(t *testing.T) {
	raw, err := os.ReadFile("testdata/descriptors/static.json")
	if err != nil {
		t.Fatal(err)
	}
	var root map[string]any
	if err := json.Unmarshal(raw, &root); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name  string
		value any
		valid bool
	}{
		{"absent", nil, true},
		{"plain", "Kazuha Hub", true},
		{"cjk", "和叶工作室", true},
		{"precomposed", "Café", true},
		{"max length", strings.Repeat("a", 64), true},
		{"decomposed", "Café", false},
		{"zero width joiner", "Ka‍zuha", false},
		{"rtl override", "Kazuha‮", false},
		{"too long", strings.Repeat("a", 65), false},
		{"leading space", " Kazuha", false},
		{"trailing space", "Kazuha ", false},
		{"double space", "Kazuha  Hub", false},
		{"tab", "Kazuha\tHub", false},
		{"empty", "", false},
		{"invalid utf8", "Kaz\xffuha", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if c.value == nil {
				delete(root, "publisher_name")
			} else {
				root["publisher_name"] = c.value
			}
			b, err := json.Marshal(root)
			if err != nil {
				t.Fatal(err)
			}
			d, err := ParseDescriptor(b)
			if (err == nil) != c.valid {
				t.Fatalf("valid=%v error=%v", c.valid, err)
			}
			if err == nil && c.value != nil && (d.Manifest.PublisherName == nil || *d.Manifest.PublisherName != c.value) {
				t.Fatalf("name %v", d.Manifest.PublisherName)
			}
		})
	}
}
