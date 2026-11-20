package appplatform

import "testing"

func TestSortPrivateNetworks(t *testing.T) {
	n := []PrivateNetwork{pn("192.168.1.0/24", true, 80), pn("10.20.0.0/24", false, 443, 8080), pn("fd00:1234:5678::/48", false, 443)}
	got, err := SortPrivateNetworks(n)
	if err != nil || len(got) != 3 || got[0].Prefix != "10.20.0.0/24" || got[1].Prefix != "192.168.1.0/24" || got[2].Prefix != "fd00:1234:5678::/48" {
		t.Fatalf("%+v %v", got, err)
	}
	got[0].Ports[0] = 1
	if n[1].Ports[0] != 443 || n[0].Prefix != "192.168.1.0/24" {
		t.Fatal("caller state shared with the sorted copy")
	}
	empty, err := SortPrivateNetworks(nil)
	if err != nil || empty == nil || len(empty) != 0 {
		t.Fatalf("empty must be a non-nil empty list: %#v %v", empty, err)
	}
	if _, err := SortPrivateNetworks([]PrivateNetwork{pn("8.8.0.0/16", false, 443)}); err == nil {
		t.Fatal("invalid list sorted")
	}
}
