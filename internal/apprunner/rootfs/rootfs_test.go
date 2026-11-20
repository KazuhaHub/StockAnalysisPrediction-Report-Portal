package rootfs

import "testing"

func TestKindNamesAndDescription(t *testing.T) {
	cases := map[int64]string{0x58465342: "xfs", 0xef53: "ext4", 0x9123683e: "btrfs", 0x123456: "other(0x123456)"}
	for magic, want := range cases {
		if got := KindName(magic); got != want {
			t.Fatalf("KindName(%#x) = %q, want %q", magic, got, want)
		}
	}
	if got := (Info{Kind: "xfs"}).Describe(); got != "xfs" {
		t.Fatal(got)
	}
	if got := (Info{Kind: "xfs", LoopBacked: true, BackingKind: "ext4"}).Describe(); got != "xfs loop-backed on ext4" {
		t.Fatal(got)
	}
	if got := (Info{Kind: "xfs", LoopBacked: true}).Describe(); got != "xfs loop-backed on unknown" {
		t.Fatal(got)
	}
}

func TestLoopBackingPath(t *testing.T) {
	if got := loopBackingPath("/sys/", 7, 3); got != "/sys/dev/block/7:3/loop/backing_file" {
		t.Fatal(got)
	}
}
