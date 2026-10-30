//go:build linux

package main

import "testing"

func TestFixedFixtureQuotaProfiles(t *testing.T) {
	for _, c := range []struct {
		bytes  int64
		denied bool
		want   int
	}{
		{7 << 20, true, 128}, {8 << 20, true, 128}, {255 << 20, true, 65536}, {256 << 20, true, 65536},
		{0, true, 0}, {6 << 20, true, 0}, {9 << 20, true, 0}, {16 << 20, true, 0}, {254 << 20, true, 0}, {257 << 20, true, 0}, {8 << 20, false, 0}, {256 << 20, false, 0},
	} {
		got, err := fixtureQuotaInodeLimit(c.bytes, c.denied)
		if c.want == 0 {
			if err == nil {
				t.Fatalf("unrecognized actual hard limit accepted: %+v", c)
			}
		} else if err != nil || got != c.want {
			t.Fatalf("fixed quota profile %+v: %d %v", c, got, err)
		}
	}
}
