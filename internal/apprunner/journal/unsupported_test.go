//go:build !linux && !darwin

package journal

import (
	"errors"
	"os"
	"testing"
)

func TestUnsupportedPlatformDoesNotCreateJournal(t *testing.T) {
	directory := t.TempDir()
	if _, err := Open(directory); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("unsupported filesystem profile accepted: %v", err)
	}
	files, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 0 {
		t.Fatal("unsupported profile created infrastructure files")
	}
}
