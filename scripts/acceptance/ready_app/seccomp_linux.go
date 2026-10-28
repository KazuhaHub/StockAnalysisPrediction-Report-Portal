//go:build linux

package main

import (
	"fmt"
	"os"
	"strings"

	"golang.org/x/sys/unix"
)

// The fixture refuses to serve health/registration/viewer requests unless the
// actual entrypoint process has the narrowed filter. An invalid FD distinguishes
// the seccomp EPERM refusal from filesystem/read-only/capability checks: an
// unfiltered ioctl would reach descriptor lookup and return EBADF instead.
func verifyFixtureSeccomp() error {
	if os.Getuid() != 65532 || os.Geteuid() != 65532 || os.Getgid() != 65532 || os.Getegid() != 65532 {
		return fmt.Errorf("unexpected fixture credentials")
	}
	status, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return err
	}
	for _, required := range []string{"NoNewPrivs:\t1\n", "Seccomp:\t2\n", "CapEff:\t0000000000000000\n"} {
		if !strings.Contains(string(status), required) {
			return fmt.Errorf("missing fixture isolation")
		}
	}
	// FS_IOC_SETFLAGS, its 32-bit ABI form, FS_IOC_FSSETXATTR (also XFS),
	// filesystem getters, and unlisted/upper-word ioctl aliases must be denied.
	for _, request := range []uintptr{0x40086602, 0x40046602, 0x401c5820, 0x80086601, 0x801c581f, 0, 0xffffffff, 0x10000541b} {
		_, _, errno := unix.Syscall(unix.SYS_IOCTL, ^uintptr(0), request, 0)
		if errno != unix.EPERM {
			return fmt.Errorf("ioctl %#x reached descriptor lookup: %v", request, errno)
		}
	}
	for _, request := range []uintptr{0x5401, 0x5413, 0x541b, 0x5421} {
		_, _, errno := unix.Syscall(unix.SYS_IOCTL, ^uintptr(0), request, 0)
		if errno != unix.EBADF {
			return fmt.Errorf("allowed ioctl %#x unavailable: %v", request, errno)
		}
	}
	// An alternate asynchronous submission path must remain outside the profile.
	_, _, errno := unix.Syscall(unix.SYS_IO_URING_SETUP, 0, 0, 0)
	if errno != unix.EPERM {
		return fmt.Errorf("io_uring setup not denied: %v", errno)
	}
	return nil
}
