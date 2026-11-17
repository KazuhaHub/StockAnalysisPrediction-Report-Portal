package data

import "testing"

func TestSchemaMarkerMismatch(t *testing.T) {
	for raw, want := range map[string]int{"3": 3, "3\n": 3, "1000000": 1000000} {
		got, err := parseSchemaMarker([]byte(raw))
		if err != nil || got != want {
			t.Fatalf("%q: %d %v", raw, got, err)
		}
	}
	for _, raw := range []string{"", "\n", "03", "0", "-1", "3 ", " 3", "3\n\n", "3x", "0x3", "1000001", "99999999999999999999", "３", "3\x00"} {
		if _, err := parseSchemaMarker([]byte(raw)); err == nil {
			t.Fatalf("marker %q accepted", raw)
		}
	}
}
