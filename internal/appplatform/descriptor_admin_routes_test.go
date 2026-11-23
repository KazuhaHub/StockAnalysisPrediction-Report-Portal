package appplatform

import (
	"fmt"
	"testing"
)

func adminRoute(id, path string) map[string]any {
	return map[string]any{"id": id, "path": path, "layout": "viewport"}
}

func withAdminRoutes(t *testing.T, value any) error {
	t.Helper()
	raw := mutateDescriptor(t, func(d map[string]any) { d["presentation"].(map[string]any)["admin_routes"] = value })
	_, err := ParseDescriptor(raw)
	return err
}

// Absent admin_routes must keep the pre-existing behaviour; a valid declaration is accepted
// and needs no "/" path of its own.
func TestDescriptorAdminRoutesAcceptedAndAbsentUnchanged(t *testing.T) {
	if _, err := ParseDescriptor(descriptorFixture(t, true)); err != nil {
		t.Fatal("descriptor without admin_routes refused", err)
	}
	if err := withAdminRoutes(t, []any{adminRoute("admin", "/admin"), adminRoute("admin-runs", "/admin/runs")}); err != nil {
		t.Fatal("valid admin_routes refused", err)
	}
	parsed, err := ParseDescriptor(mutateDescriptor(t, func(d map[string]any) {
		d["presentation"].(map[string]any)["admin_routes"] = []any{adminRoute("admin", "/admin")}
	}))
	if err != nil || len(parsed.Manifest.Presentation.AdminRoutes) != 1 || parsed.Manifest.Presentation.AdminRoutes[0].Path != "/admin" {
		t.Fatal("admin route not decoded", err)
	}
}

// Fails (accepted) if any of these ambiguous or unbounded declarations is let through.
func TestDescriptorAdminRoutesRefusesCollisionsAndBounds(t *testing.T) {
	many := func(n int) []any {
		var out []any
		for i := range n {
			out = append(out, adminRoute(fmt.Sprintf("admin-%d", i), fmt.Sprintf("/admin/%d", i)))
		}
		return out
	}
	if err := withAdminRoutes(t, many(8)); err != nil {
		t.Fatal("eight admin routes refused", err)
	}
	cases := map[string]any{
		"id collides with a reader route":   []any{adminRoute("home", "/admin")},
		"path collides with a reader route": []any{adminRoute("admin", "/")},
		"duplicate id":                      []any{adminRoute("admin", "/a"), adminRoute("admin", "/b")},
		"duplicate path":                    []any{adminRoute("a", "/admin"), adminRoute("b", "/admin")},
		"nine routes":                       many(9),
		"empty array":                       []any{},
		"bad layout":                        []any{map[string]any{"id": "admin", "path": "/admin", "layout": "popup"}},
		"bad path":                          []any{adminRoute("admin", "/a/../b")},
		"unknown field":                     []any{map[string]any{"id": "admin", "path": "/admin", "layout": "document", "role": "x"}},
	}
	for name, value := range cases {
		if err := withAdminRoutes(t, value); err == nil {
			t.Fatalf("%s: accepted", name)
		}
	}
}
