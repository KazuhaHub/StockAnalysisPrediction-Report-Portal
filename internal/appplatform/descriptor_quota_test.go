package appplatform

import "testing"

func declareQuota(bytes, inodes any) func(map[string]any) {
	return func(d map[string]any) {
		d["data"].(map[string]any)["quota"] = map[string]any{"bytes": bytes, "inodes": inodes}
	}
}

func TestDescriptorQuotaDefaultsAndBounds(t *testing.T) {
	parsed, err := ParseDescriptor(mutateDescriptor(t, func(map[string]any) {}))
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Manifest.Data.Quota != nil {
		t.Fatal("an absent block must stay absent")
	}
	if b, i := parsed.Manifest.Data.QuotaOrDefault(); b != 256<<20 || i != 65536 {
		t.Fatalf("defaults = %d %d", b, i)
	}
	for _, c := range []struct {
		name         string
		bytes, nodes any
		ok           bool
		wantB, wantI int64
	}{
		{"market-monitor", 3 << 30, 100000, true, 3 << 30, 100000},
		{"minimum", 256 << 20, 65536, true, 256 << 20, 65536},
		{"maximum", 16 << 30, 1000000, true, 16 << 30, 1000000},
		{"below minimum bytes", 192 << 20, 65536, false, 0, 0},
		{"above maximum bytes", 17 << 30, 65536, false, 0, 0},
		{"not a 64 MiB multiple", 300 << 20, 65536, false, 0, 0},
		{"inodes below minimum", 256 << 20, 65535, false, 0, 0},
		{"inodes above maximum", 256 << 20, 1000001, false, 0, 0},
		{"fractional bytes", 268435456.5, 65536, false, 0, 0},
		{"string bytes", "268435456", 65536, false, 0, 0},
	} {
		parsed, err := ParseDescriptor(mutateDescriptor(t, declareQuota(c.bytes, c.nodes)))
		if (err == nil) != c.ok {
			t.Fatalf("%s: err = %v", c.name, err)
		}
		if c.ok {
			if b, i := parsed.Manifest.Data.QuotaOrDefault(); b != c.wantB || i != c.wantI {
				t.Fatalf("%s: got %d %d", c.name, b, i)
			}
		}
	}
	for name, block := range map[string]any{"missing inodes": map[string]any{"bytes": 256 << 20}, "extra": map[string]any{"bytes": 256 << 20, "inodes": 65536, "x": 1}} {
		if _, err := ParseDescriptor(mutateDescriptor(t, func(d map[string]any) { d["data"].(map[string]any)["quota"] = block })); err == nil {
			t.Fatalf("%s accepted", name)
		}
	}
}
