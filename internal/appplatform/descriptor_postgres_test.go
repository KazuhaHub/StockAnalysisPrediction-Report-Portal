package appplatform

import "testing"

// network.postgres (doc 178) is optional only and needs a backend: an administrator grants it by
// its own review, so a descriptor can neither require it nor declare it without one.
func TestDescriptorNetworkPostgres(t *testing.T) {
	if _, err := ParseDescriptor(mutateDescriptor(t, func(d map[string]any) {
		d["capabilities"] = map[string]any{"optional": []string{"network.postgres"}}
	})); err != nil {
		t.Fatal(err)
	}
	for name, declare := range map[string]func(map[string]any){
		"required": func(d map[string]any) { d["capabilities"] = map[string]any{"required": []string{"network.postgres"}} },
		"without a backend": func(d map[string]any) {
			d["capabilities"] = map[string]any{"optional": []string{"network.postgres"}}
			delete(d, "backend")
		},
	} {
		if _, err := ParseDescriptor(mutateDescriptor(t, declare)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
