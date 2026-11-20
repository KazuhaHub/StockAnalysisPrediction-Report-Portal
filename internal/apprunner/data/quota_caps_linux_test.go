package data

import "testing"

func TestCheckpointCapsFollowTheQuota(t *testing.T) {
	if got := ArchiveCapForQuota(256 << 20); got != 320<<20 {
		t.Fatalf("archive cap of the default quota = %d, want quota plus 64 MiB", got)
	}
	if got := ArchiveCapForQuota(3 << 30); got != 3<<30+64<<20 {
		t.Fatalf("archive cap of 3 GiB = %d", got)
	}
	if got := ArchiveCapForQuota(0); got != 0 {
		t.Fatalf("a missing quota must give no cap, got %d", got)
	}
	if (CheckpointRequest{MaxEntries: 100000}).entryCap() != 100000 || (CheckpointRequest{MaxEntries: 65536}).entryCap() != 65536 {
		t.Fatal("the entry cap must be the request's")
	}
	if (RestoreRequest{MaxEntries: 1000000}).entryCap() != 1000000 {
		t.Fatal("the restore entry cap must be the request's")
	}
	if (CheckpointRequest{}).entryCap() != legacyMaxCheckpointEntries || (RestoreRequest{}).entryCap() != legacyMaxCheckpointEntries {
		t.Fatal("a request without a cap keeps the cap every request had before the quota was per installation")
	}
	r := ImportRequest{ArchiveBytes: 1, MaxArchiveBytes: 2, QuotaInodes: 70000}
	if r.restoreRequest().MaxEntries != 70000 {
		t.Fatalf("import entry cap = %d, want the inode quota", r.restoreRequest().MaxEntries)
	}
}
