package appplatform

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
)

func TestDescriptorContractSourceAndTypes(t *testing.T) {
	raw, err := os.ReadFile("contract/descriptor-v2.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	if hex.EncodeToString(sum[:]) != descriptorContractSHA256 {
		t.Fatal("generated Go contract differs from the source schema")
	}
	var schema struct {
		Definitions map[string]struct {
			Properties map[string]struct {
				OmitEmpty bool `json:"x-go-omitempty"`
			} `json:"properties"`
		} `json:"$defs"`
	}
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatal(err)
	}
	types := map[string]reflect.Type{
		"Manifest": reflect.TypeFor[Manifest](), "Compatibility": reflect.TypeFor[Compatibility](),
		"AppDisplay": reflect.TypeFor[AppDisplay](), "AppDisplayText": reflect.TypeFor[AppDisplayText](),
		"Presentation": reflect.TypeFor[Presentation](), "Route": reflect.TypeFor[Route](),
		"Artifact": reflect.TypeFor[Artifact](), "Backend": reflect.TypeFor[Backend](),
		"Resources": reflect.TypeFor[Resources](), "Capabilities": reflect.TypeFor[Capabilities](),
		"DataCompatibility": reflect.TypeFor[DataCompatibility](),
	}
	if len(types) != len(schema.Definitions) {
		t.Fatal("named structural definitions differ")
	}
	for name, definition := range schema.Definitions {
		typ, ok := types[name]
		if !ok || typ.NumField() != len(definition.Properties) {
			t.Fatalf("Go fields differ from %s", name)
		}
		for i := range typ.NumField() {
			field := typ.Field(i)
			key, option, _ := strings.Cut(field.Tag.Get("json"), ",")
			property, ok := definition.Properties[key]
			if !ok || (option == "omitempty") != property.OmitEmpty {
				t.Fatalf("Go serialization differs for %s.%s", name, field.Name)
			}
		}
	}
}
