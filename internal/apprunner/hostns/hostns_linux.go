//go:build linux

package hostns

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"golang.org/x/sys/unix"
)

// maxExecFD bounds the descriptor number the entry may name. The entry always uses 3.
const maxExecFD = 1 << 12

// ConsumeExecFD handles PORTAL_APP_RUNNER_EXEC_FD. When the variable is absent (the runner was not started by
// the entry) it does nothing. Otherwise the named descriptor must be the runner's own executable (fstat equals
// stat of /proc/self/exe); it is then marked close-on-exec, so nothing the runner starts later inherits it,
// and the variable is removed. No other descriptor is touched. Any other value is an error and the runner
// must exit: a descriptor that is not our own executable means the entry did not do what it promises.
func ConsumeExecFD() error {
	value, set := os.LookupEnv(ExecFDEnv)
	if !set {
		return nil
	}
	err := consumeExecFD(value, "/proc/self/exe")
	os.Unsetenv(ExecFDEnv)
	return err
}

func consumeExecFD(value, exe string) error {
	fd, err := strconv.ParseUint(value, 10, 32)
	if err != nil || fd < 3 || fd > maxExecFD || strconv.FormatUint(fd, 10) != value {
		return fmt.Errorf("%w: %q is not a descriptor number", ErrExecFD, value)
	}
	var held, self unix.Stat_t
	if err := unix.Fstat(int(fd), &held); err != nil {
		return fmt.Errorf("%w: descriptor %d is not open", ErrExecFD, fd)
	}
	if err := unix.Stat(exe, &self); err != nil {
		return fmt.Errorf("%w: the executable cannot be read", ErrExecFD)
	}
	if held.Mode&unix.S_IFMT != unix.S_IFREG || held.Dev != self.Dev || held.Ino != self.Ino {
		return fmt.Errorf("%w: descriptor %d is another file", ErrExecFD, fd)
	}
	if _, err := unix.FcntlInt(uintptr(fd), unix.F_SETFD, unix.FD_CLOEXEC); err != nil {
		return fmt.Errorf("%w: cannot mark descriptor %d close-on-exec", ErrExecFD, fd)
	}
	return nil
}

// RequireHostNamespaces refuses unless the calling process shares PID 1's mount, network and PID namespaces.
// procRoot is /proc in production. A namespace file that cannot be read counts as not shared.
func RequireHostNamespaces(procRoot string) error {
	for _, ns := range []string{"mnt", "net", "pid"} {
		ours, err := os.Stat(filepath.Join(procRoot, "self", "ns", ns))
		if err != nil {
			return fmt.Errorf("%w: own %s namespace unreadable", ErrNamespace, ns)
		}
		init, err := os.Stat(filepath.Join(procRoot, "1", "ns", ns))
		if err != nil {
			return fmt.Errorf("%w: PID 1's %s namespace unreadable", ErrNamespace, ns)
		}
		if !os.SameFile(ours, init) {
			return fmt.Errorf("%w: the %s namespace differs from PID 1's", ErrNamespace, ns)
		}
	}
	return nil
}
