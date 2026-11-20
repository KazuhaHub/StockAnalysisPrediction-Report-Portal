//go:build linux

package hostns

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"

	"golang.org/x/sys/unix"
)

func openInheritable(t *testing.T, path string) int {
	t.Helper()
	fd, err := unix.Open(path, unix.O_RDONLY, 0) // no O_CLOEXEC: this is how the entry's `exec 3<` leaves it
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { unix.Close(fd) })
	return fd
}

func closeOnExec(t *testing.T, fd int) bool {
	t.Helper()
	flags, err := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0)
	if err != nil {
		t.Fatal(err)
	}
	return flags&unix.FD_CLOEXEC != 0
}

// The descriptor the entry named is the runner's own executable: it becomes close-on-exec and a child started
// afterwards (host-setup's losetup, mkfs.xfs) does not hold it.
func TestExecFDBecomesCloseOnExecAndChildrenDoNotInheritIt(t *testing.T) {
	fd := openInheritable(t, "/proc/self/exe")
	if closeOnExec(t, fd) {
		t.Fatal("fixture descriptor is already close-on-exec")
	}
	if err := consumeExecFD(strconv.Itoa(fd), "/proc/self/exe"); err != nil {
		t.Fatalf("own executable refused: %v", err)
	}
	if !closeOnExec(t, fd) {
		t.Fatal("the descriptor was not marked close-on-exec")
	}
	// A child started now must not have it: "test -e" on the child's own descriptor table.
	cmd := exec.Command("/bin/sh", "-c", "test -e /proc/$$/fd/"+strconv.Itoa(fd))
	if err := cmd.Run(); err == nil {
		t.Fatalf("a child inherited descriptor %d", fd)
	}
}

// Fails closed: a descriptor that is not the runner's own executable, is not open, or is not a number makes the
// runner refuse to start, and an unrelated descriptor is never altered.
func TestExecFDOtherThanOwnExecutableIsRefused(t *testing.T) {
	other := filepath.Join(t.TempDir(), "other")
	if err := os.WriteFile(other, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	fd := openInheritable(t, other)
	for name, value := range map[string]string{
		"another file":   strconv.Itoa(fd),
		"not a number":   "three",
		"empty":          "",
		"negative":       "-1",
		"stdin":          "0",
		"leading zero":   "03",
		"closed":         "3999",
		"out of range":   "99999999999",
		"plus sign":      "+" + strconv.Itoa(fd),
		"trailing space": strconv.Itoa(fd) + " ",
	} {
		if err := consumeExecFD(value, "/proc/self/exe"); !errors.Is(err, ErrExecFD) {
			t.Errorf("%s (%q): %v", name, value, err)
		}
	}
	if closeOnExec(t, fd) {
		t.Fatal("a refused descriptor was altered")
	}
}

func TestConsumeExecFDWithoutTheVariableIsANoOp(t *testing.T) {
	os.Unsetenv(ExecFDEnv)
	if err := ConsumeExecFD(); err != nil {
		t.Fatal(err)
	}
}

func TestConsumeExecFDRemovesTheVariable(t *testing.T) {
	t.Setenv(ExecFDEnv, "not-a-number")
	if err := ConsumeExecFD(); !errors.Is(err, ErrExecFD) {
		t.Fatalf("bad value: %v", err)
	}
	if _, set := os.LookupEnv(ExecFDEnv); set {
		t.Fatal("the variable was left in the environment")
	}
}

func fakeProc(t *testing.T, shared map[string]bool) string {
	t.Helper()
	root := t.TempDir()
	for _, ns := range []string{"mnt", "net", "pid"} {
		for _, dir := range []string{"1/ns", "self/ns"} {
			if err := os.MkdirAll(filepath.Join(root, dir), 0o755); err != nil {
				t.Fatal(err)
			}
		}
		init := filepath.Join(root, "1", "ns", ns)
		if err := os.WriteFile(init, []byte(ns), 0o600); err != nil {
			t.Fatal(err)
		}
		self := filepath.Join(root, "self", "ns", ns)
		if shared[ns] {
			if err := os.Link(init, self); err != nil {
				t.Fatal(err)
			}
		} else if err := os.WriteFile(self, []byte(ns), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// Fails closed: any one of the three namespaces differing from PID 1's, or unreadable, is a refusal.
func TestRequireHostNamespaces(t *testing.T) {
	all := map[string]bool{"mnt": true, "net": true, "pid": true}
	if err := RequireHostNamespaces(fakeProc(t, all)); err != nil {
		t.Fatalf("all shared: %v", err)
	}
	for _, differ := range []string{"mnt", "net", "pid"} {
		shared := map[string]bool{"mnt": true, "net": true, "pid": true}
		shared[differ] = false
		if err := RequireHostNamespaces(fakeProc(t, shared)); !errors.Is(err, ErrNamespace) {
			t.Errorf("%s differs: %v", differ, err)
		}
	}
	if err := RequireHostNamespaces(filepath.Join(t.TempDir(), "missing")); !errors.Is(err, ErrNamespace) {
		t.Fatalf("unreadable proc: %v", err)
	}
}
