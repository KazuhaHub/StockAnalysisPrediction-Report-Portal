//go:build linux

package driver

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func TestBackendRegistrationFileObservation(t *testing.T) {
	for _, mode := range []string{"valid", "symlink", "directory", "hardlink", "permissions", "content", "binding", "missing_binding", "fifo", "oversize", "missing"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			parent, err := unix.Open(dir, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
			if err != nil {
				t.Fatal(err)
			}
			defer unix.Close(parent)
			path := filepath.Join(dir, backendRegistrationFilename)
			document := []byte("private-test-registration")
			binding := strings.Repeat("a", 64)
			switch mode {
			case "symlink":
				err = os.Symlink("outside", path)
			case "directory":
				err = os.Mkdir(path, 0400)
			case "fifo":
				err = unix.Mkfifo(path, 0400)
			case "missing":
			default:
				err = os.WriteFile(path, document, 0600)
				if err == nil && mode != "missing_binding" {
					err = unix.Setxattr(path, "user.portal.registration", []byte(binding), 0)
				}
				if err == nil {
					err = os.Chmod(path, 0400)
				}
				switch mode {
				case "hardlink":
					err = os.Link(path, filepath.Join(dir, "other"))
				case "permissions":
					err = os.Chmod(path, 0600)
				case "content":
					err = os.Chmod(path, 0600)
					if err == nil {
						err = os.WriteFile(path, []byte(strings.Repeat("x", len(document))), 0600)
					}
					if err == nil {
						err = os.Chmod(path, 0400)
					}
				case "binding":
					err = os.Chmod(path, 0600)
					if err == nil {
						err = unix.Setxattr(path, "user.portal.registration", []byte(strings.Repeat("b", 64)), 0)
					}
					if err == nil {
						err = os.Chmod(path, 0400)
					}
				case "oversize":
					err = os.Chmod(path, 0600)
					if err == nil {
						err = os.WriteFile(path, make([]byte, 4097), 0600)
					}
					if err == nil {
						err = os.Chmod(path, 0400)
					}
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			_, err = observeBackendRegistrationFile(context.Background(), parent, document, binding, os.Geteuid(), os.Getegid())
			if (err == nil) != (mode == "valid") {
				t.Fatal("observation", mode, err)
			}
		})
	}
}
func TestBackendRegistrationAtomicPublish(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("anonymous root publication covered by disposable native acceptance")
	}
	dir := t.TempDir()
	parent, err := unix.Open(dir, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(parent)
	document := []byte("private-test-registration")
	binding := strings.Repeat("a", 64)
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if createBackendRegistrationFile(cancelled, parent, document, binding, 65532, 65532) == nil {
		t.Fatal("cancelled write accepted")
	}
	if _, err := os.Stat(filepath.Join(dir, backendRegistrationFilename)); !os.IsNotExist(err) {
		t.Fatal("cancelled write published a file")
	}
	if err := createBackendRegistrationFile(context.Background(), parent, document, binding, 65532, 65532); err != nil {
		t.Fatal(err)
	}
	before, err := observeBackendRegistrationFile(context.Background(), parent, document, binding, 65532, 65532)
	if err != nil {
		t.Fatal(err)
	}
	if createBackendRegistrationFile(context.Background(), parent, []byte("replacement"), strings.Repeat("b", 64), 65532, 65532) == nil {
		t.Fatal("existing registration overwritten")
	}
	after, err := observeBackendRegistrationFile(context.Background(), parent, document, binding, 65532, 65532)
	if err != nil || !sameBackendRegistrationStat(before, after) {
		t.Fatal("existing file changed", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 {
		t.Fatal("temporary private file remained", err)
	}
}
