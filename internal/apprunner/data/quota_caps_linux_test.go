package data

import (
	"archive/tar"
	"io"
	"math"
	"strings"
	"testing"
)

func TestArchiveCapFollowsQuotaAndEntries(t *testing.T) {
	const mib = int64(1) << 20
	if got := ArchiveCapFor(256*mib, 65536); got != 512*mib {
		t.Fatalf("the default namespace keeps the 512 MiB floor, got %d", got)
	}
	// The floor keeps every checkpoint that was valid under the old fixed cap valid.
	if ArchiveCapFor(256*mib, 65536) < 512*mib || ArchiveCapFor(64*mib, 1) < 512*mib {
		t.Fatal("cap below the old fixed cap")
	}
	if got, want := ArchiveCapFor(3<<30, 100000), 3<<30+100000*1536+64*mib; got != want {
		t.Fatalf("3 GiB / 100,000 inodes cap = %d, want %d", got, want)
	}
	// 700,000 empty files under a 256 MiB / 1,000,000 inode quota: a 512-byte header each.
	if got := ArchiveCapFor(256*mib, 1000000); got < 700000*512+256*mib {
		t.Fatalf("700,000 empty files do not fit a cap of %d", got)
	}
	for _, bad := range [][2]int64{{0, 1}, {-1, 1}, {1, 0}, {1, -1}, {math.MaxInt64, 65536}, {1 << 30, math.MaxInt64}} {
		if got := ArchiveCapFor(bad[0], bad[1]); got != 0 {
			t.Fatalf("cap for %v = %d, want none", bad, got)
		}
	}
	if got := ArchiveCapFor(16<<30, 1000000); got <= 16<<30 || got > 1<<35 {
		t.Fatalf("cap at the platform maximum = %d", got)
	}
}

// The per-entry allowance must hold the worst real entry: a 255-byte non-ASCII name needs a PAX header.
func TestArchiveCapHoldsLongChineseNames(t *testing.T) {
	const entries = 3000
	var written countingWriter
	w := tar.NewWriter(&written)
	name := strings.Repeat("数据", 42) // 252 bytes of UTF-8
	for i := 0; i < entries; i++ {
		n := name + string(rune('a'+i%26)) + strings.Repeat("0", 3)
		header := &tar.Header{Name: n, Typeflag: tar.TypeReg, Mode: 0640, Size: 0, Format: tar.FormatPAX, PAXRecords: map[string]string{"path": n}}
		if err := w.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if perEntry := int64(written) / entries; perEntry > 1536 {
		t.Fatalf("a long Chinese name costs %d bytes of tar, the allowance is 1536", perEntry)
	}
	if int64(written) > ArchiveCapFor(256<<20, entries)-256<<20 {
		t.Fatal("the cap does not cover the headers of the entries it allows")
	}
}

type countingWriter int64

func (c *countingWriter) Write(p []byte) (int, error) {
	*c += countingWriter(len(p))
	return len(p), nil
}

var _ io.Writer = (*countingWriter)(nil)

func TestCheckpointEntryCapsFollowTheRequest(t *testing.T) {
	if (CheckpointRequest{MaxEntries: 65536}).entryCap() != 65536 || (RestoreRequest{MaxEntries: 1000000}).entryCap() != 1000000 {
		t.Fatal("the entry cap must be the request's")
	}
	if (CheckpointRequest{}).entryCap() != legacyMaxCheckpointEntries || (RestoreRequest{}).entryCap() != legacyMaxCheckpointEntries {
		t.Fatal("a request without a cap keeps the cap every request had before the quota was per installation")
	}
	r := ImportRequest{ArchiveBytes: 1, MaxArchiveBytes: 2, QuotaInodes: 70000}
	if r.restoreRequest().MaxEntries != 70000 {
		t.Fatalf("import entry cap = %d, want the inode quota", r.restoreRequest().MaxEntries)
	}
}

// The checkpoint the restore re-verifies must carry the namespace's caps, not the legacy 100,000.
func TestRestoreReverifiesWithItsOwnEntryCap(t *testing.T) {
	r := RestoreRequest{CheckpointID: "c", NamespaceID: "n", ProjectID: 7, DataFormat: 2, MaxArchiveBytes: 1 << 30, MaxEntries: 200000}
	c := r.checkpointRequest()
	if c.MaxEntries != 200000 || c.entryCap() != 200000 || c.MaxArchiveBytes != 1<<30 || c.ProjectID != 7 || c.DataFormat != 2 {
		t.Fatalf("re-verify request %+v", c)
	}
}
