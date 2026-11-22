package appplatform

import "testing"

func TestCompareVersions(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want int
	}{
		{"1.0.0", "1.0.0", 0},
		{"1.0.0", "2.0.0", -1},
		{"1.10.0", "1.2.0", 1},
		{"1.2.10", "1.2.9", 1},
		{"0.1.0-beta.2", "0.1.0-beta.1", 1},
		{"0.1.0", "0.1.0-beta.2", 1},
		{"0.1.0-beta.1", "0.1.0", -1},
		{"1.0.0-beta.10", "1.0.0-beta.2", 1},
		{"1.0.0-alpha", "1.0.0-alpha.1", -1},
		{"1.0.0-alpha.1", "1.0.0-alpha.beta", -1},
		{"1.0.0-alpha.beta", "1.0.0-beta", -1},
		{"1.0.0-1", "1.0.0-alpha", -1},
		{"1.0.0+build.1", "1.0.0+build.2", 0},
		{"1.0.0-rc.1+a", "1.0.0-rc.1+b", 0},
		{"1.0.0-rc.1", "1.0.0-rc.1.0", -1},
		{"1.0.0-2", "1.0.0-11", -1},
	} {
		if got := CompareVersions(c.a, c.b); got != c.want {
			t.Errorf("CompareVersions(%q, %q) = %d, want %d", c.a, c.b, got, c.want)
		}
		if got := CompareVersions(c.b, c.a); got != -c.want {
			t.Errorf("CompareVersions(%q, %q) = %d, want %d", c.b, c.a, got, -c.want)
		}
	}
}
