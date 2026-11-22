package appplatform

import (
	"bytes"
	"testing"
)

func TestAccountCreatedMarkerNormalisesOnlyEmpty(t *testing.T) {
	if got := AccountCreatedMarker(""); got != LegacyAccountCreated || got == "" {
		t.Fatalf("empty created mapped to %q", got)
	}
	for _, real := range []string{"2026-01-02 03:04:05", "2026-01-02T03:04:05Z", "x"} {
		if got := AccountCreatedMarker(real); got != real {
			t.Fatalf("real value %q changed to %q", real, got)
		}
	}
	seed := bytes.Repeat([]byte{31}, 32)
	id := "00000000-0000-4000-8000-000000000003"
	chain := []ViewerOUIdentity{{1, "created"}}
	if _, err := DeriveViewerIdentity("notes-sample", id, seed, ViewerAccountIdentity{Name: "u", Created: "", Role: "user"}, chain); err == nil {
		t.Fatal("derivation accepted an empty creation marker")
	}
	if _, err := DeriveViewerIdentity("notes-sample", id, seed, ViewerAccountIdentity{Name: "u", Created: AccountCreatedMarker(""), Role: "user"}, chain); err != nil {
		t.Fatal(err)
	}
}
