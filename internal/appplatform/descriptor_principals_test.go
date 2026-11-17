package appplatform

import (
	"os"
	"strings"
	"testing"
)

// principals.read is optional and needs no backend (doc 181 section 3.1): a
// frontend-only descriptor may declare it. A required declaration is refused,
// because an administrator must enable it explicitly after seeing the disclosure.
func TestDescriptorAcceptsPrincipalsRead(t *testing.T) {
	for name, declare := range map[string]func(map[string]any){
		"optional": func(d map[string]any) { d["capabilities"] = map[string]any{"optional": []string{"principals.read"}} },
		"static optional with others": func(d map[string]any) {
			d["capabilities"] = map[string]any{"required": []string{"reports.read"}, "optional": []string{"principals.read"}}
		},
	} {
		t.Run(name, func(t *testing.T) {
			parsed, err := ParseDescriptor(mutateDescriptor(t, declare))
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, capability := range parsed.Manifest.Capabilities.Optional {
				found = found || capability == "principals.read"
			}
			if !found {
				t.Fatal("capability was dropped")
			}
		})
	}
}

func TestDescriptorRefusesRequiredPrincipalsRead(t *testing.T) {
	for name, declare := range map[string]func(map[string]any){
		"required": func(d map[string]any) { d["capabilities"] = map[string]any{"required": []string{"principals.read"}} },
		"required with others": func(d map[string]any) {
			d["capabilities"] = map[string]any{"required": []string{"reports.read", "principals.read"}}
		},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseDescriptor(mutateDescriptor(t, declare)); err == nil {
				t.Fatal("a required principals.read was accepted")
			}
		})
	}
}

func TestDescriptorSchemaListsPrincipalsRead(t *testing.T) {
	raw, err := os.ReadFile("contract/descriptor-v2.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(raw), `"principals.read"`) != 2 || strings.Contains(string(raw), `"maxItems": 11`) {
		t.Fatal("both capability lists must name principals.read and allow twelve entries")
	}
}
