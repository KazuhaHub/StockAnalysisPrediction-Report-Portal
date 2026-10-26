package data

import "testing"

func TestRequestBounds(t *testing.T) {
	r := Request{"00000000-0000-0000-0000-000000000001", 1, 8 << 20, 128}
	if !r.valid() {
		t.Fatal("valid reservation refused")
	}
	for _, bad := range []Request{{"../other", 1, 512, 1}, {r.NamespaceID, 0, 512, 1}, {r.NamespaceID, 1, 513, 1}, {r.NamespaceID, 1, 0, 1}, {r.NamespaceID, 1, 512, 0}} {
		if bad.valid() {
			t.Fatal("unsafe reservation accepted", bad)
		}
	}
}
