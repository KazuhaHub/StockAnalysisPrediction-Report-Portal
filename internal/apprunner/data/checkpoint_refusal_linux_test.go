//go:build linux && (amd64 || arm64)

package data

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

// A file that changed while it was archived says the quiesce did not hold. That is uncertain, not a refusal of the
// content, and the error names the cause.
func TestCheckpointChangedDuringTheWalkIsUncertainAndSaysWhy(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "changed")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	var before unix.Stat_t
	if err := unix.Fstat(int(file.Fd()), &before); err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString("grew"); err != nil {
		t.Fatal(err)
	}
	err = checkpointAfter(file, before)
	if err == nil || checkpointRefused(err) || !errors.Is(err, ErrCheckpointIncomplete) || !strings.Contains(err.Error(), "quiesced") {
		t.Fatal("a change during the walk was not reported as an uncertain, unquiesced source", err)
	}
}

// A symlink in the data root is refused by the walker with the bare unsafe error, which the checkpoint treats as a
// definite refusal (the archive is unlinked and the journal settles it), unlike an error joined with
// ErrCheckpointIncomplete.
func TestCheckpointWalkerRefusesSymlinkAsDefiniteRefusal(t *testing.T) {
	allowTestOwner(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "plain.txt"), []byte("plain"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("plain.txt", filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	_, _, err := walkToArchive(t, dir, checkpointRequestForTest())
	if !errors.Is(err, ErrCheckpointUnsafe) || errors.Is(err, ErrCheckpointIncomplete) || !checkpointRefused(err) {
		t.Fatal("a symlink was not refused as a definite refusal", err)
	}
}

func TestCheckpointRefusedSeparatesDefiniteFromUncertain(t *testing.T) {
	for name, tc := range map[string]struct {
		err  error
		want bool
	}{
		"unsafe":            {ErrCheckpointUnsafe, true},
		"too large":         {errors.Join(ErrCheckpointUnsafe, ErrCheckpointTooLarge), true},
		"incomplete":        {checkpointIncomplete(errors.New("sync")), false},
		"incomplete unsafe": {checkpointIncomplete(ErrCheckpointUnsafe), false},
		"nothing":           {nil, false},
		"other":             {ErrConflict, false},
	} {
		if checkpointRefused(tc.err) != tc.want {
			t.Fatal(name, tc.err)
		}
	}
}
