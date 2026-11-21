//go:build linux || darwin

package journal

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// Doc 187 section 8.3: the plan reads the enrollment without the owner lock, so it works while a runner serves.
func TestReadControllerDeploymentWhileTheOwnerHoldsTheLock(t *testing.T) {
	ctx := context.Background()
	directory := privateDirectory(t)

	// No database yet: not enrolled, and nothing was created.
	if _, err := ReadControllerDeployment(ctx, directory); !errors.Is(err, ErrNotEnrolled) {
		t.Fatalf("no database: %v", err)
	}
	if entries, _ := os.ReadDir(directory); len(entries) != 0 {
		t.Fatalf("the read created %d entries", len(entries))
	}

	store, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := ReadControllerDeployment(ctx, directory); !errors.Is(err, ErrNotEnrolled) {
		t.Fatalf("empty journal: %v", err)
	}
	expected, _, err := store.EnrollController(ctx, enrollmentFixture(21))
	if err != nil {
		t.Fatal(err)
	}
	// The store still holds the exclusive lock: a second Open is refused, a read is not.
	if _, err := Open(directory); !errors.Is(err, ErrInUse) {
		t.Fatalf("second owner: %v", err)
	}
	got, err := ReadControllerDeployment(ctx, directory)
	if err != nil || !reflect.DeepEqual(got, expected) {
		t.Fatalf("read while serving: %+v %v", got, err)
	}
}

// Fails closed: a journal directory with other permissions is not read, and a database that is not a private
// regular file is not opened.
func TestReadControllerDeploymentRefusesAnUnsafeDirectory(t *testing.T) {
	ctx := context.Background()
	directory := privateDirectory(t)
	if err := os.Chmod(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadControllerDeployment(ctx, directory); !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("a 0755 directory: %v", err)
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, journalName), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadControllerDeployment(ctx, directory); !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("a world-readable database: %v", err)
	}
	if _, err := ReadControllerDeployment(ctx, filepath.Join(directory, "missing")); err == nil {
		t.Fatal("a missing directory was read")
	}
}
