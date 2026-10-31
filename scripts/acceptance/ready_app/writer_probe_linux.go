//go:build linux

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"unsafe"

	"golang.org/x/sys/unix"
)

// Detect only the fixed writable fixture mount profile. This lets the real
// closed builder run the probe without adding any test-selected environment.
func fixtureHasWritableData() bool {
	var st unix.Stat_t
	return unix.Stat("/data", &st) == nil && st.Uid == 0 && st.Gid == 65532 && st.Mode&07777 == 02770
}

// Only the disposable writer fixture enables this before readiness. It never
// accepts paths, limits, syscall requests or writer authority from a viewer.
func verifyFixtureWritableData() error {
	dir, err := os.Open("/data")
	if err != nil {
		return err
	}
	defer dir.Close()
	var st unix.Stat_t
	if err := unix.Fstat(int(dir.Fd()), &st); err != nil || st.Uid != 0 || st.Gid != 65532 || st.Mode&07777 != 02770 {
		return fmt.Errorf("write probe lost root-owned data profile: %v", err)
	}
	if err := unix.Fchmod(int(dir.Fd()), 0777); !errors.Is(err, unix.EPERM) {
		return fmt.Errorf("app chmod of namespace root not refused: %v", err)
	}
	if err := unix.Fchown(int(dir.Fd()), 65532, 65532); !errors.Is(err, unix.EPERM) {
		return fmt.Errorf("app chown of namespace root not refused: %v", err)
	}
	proof, err := os.OpenFile("/data/writer-proof.json", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer proof.Close()
	if err := os.Mkdir("/data/children", 0700); err != nil {
		return err
	}
	child, err := os.Open("/data/children")
	if err != nil {
		return err
	}
	defer child.Close()
	// Use an actual app-owned directory and a real attribute buffer. Clearing
	// project inheritance or changing project ID must fail before reaching XFS.
	var attr [28]byte
	for _, request := range []uintptr{0x40086602, 0x40046602, 0x401c5820, 0x100000000 | 0x401c5820} {
		_, _, errno := unix.Syscall(unix.SYS_IOCTL, child.Fd(), request, uintptr(unsafe.Pointer(&attr[0])))
		if errno != unix.EPERM {
			return fmt.Errorf("owned-directory attribute attempt %#x not refused: %v", request, errno)
		}
	}
	file, err := os.OpenFile("/data/bytes", os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer file.Close()
	const block = 64 << 10
	allocated := int64(0)
	byteDenied := false
	for allocated <= 256<<20 {
		err := unix.Fallocate(int(file.Fd()), 0, allocated, block)
		if errors.Is(err, unix.EDQUOT) || errors.Is(err, unix.ENOSPC) {
			byteDenied = true
			break
		}
		if err != nil {
			return err
		}
		allocated += block
	}
	// Two fixed disposable profiles: low-level native tests use 8MiB/128;
	// real Portal installation keeps its reviewed 256MiB/65536 limits. Observe
	// the actual hard byte boundary; no environment/viewer selects a quota.
	expectedInodes, err := fixtureQuotaInodeLimit(allocated, byteDenied)
	if err != nil {
		return err
	}
	// Release allocated extents before the independent inode test. Keep the
	// original files and proof FD so reporting does not require a new inode.
	if err := file.Truncate(0); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	inodes, inodeDenied := 0, false
	for inodes <= expectedInodes {
		name := fmt.Sprintf("/data/children/f-%03d", inodes)
		file, err := os.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if errors.Is(err, unix.EDQUOT) || errors.Is(err, unix.ENOSPC) {
			inodeDenied = true
			break
		}
		if err != nil {
			return err
		}
		if err := file.Close(); err != nil {
			return err
		}
		inodes++
	}
	if !inodeDenied || inodes < expectedInodes-max(18, expectedInodes/512) || inodes >= expectedInodes {
		return fmt.Errorf("inode quota boundary not enforced: created=%d denied=%v", inodes, inodeDenied)
	}
	if err := json.NewEncoder(proof).Encode(struct {
		Format        string `json:"format"`
		Allocated     int64  `json:"allocated_bytes"`
		Inodes        int    `json:"created_inodes"`
		ByteDenied    bool   `json:"byte_denied"`
		InodeDenied   bool   `json:"inode_denied"`
		AttrDenied    bool   `json:"attribute_changes_denied"`
		RootProtected bool   `json:"root_owner_protected"`
	}{"portal.fixture.write-probe/1-dev.1", allocated, inodes, byteDenied, inodeDenied, true, true}); err != nil {
		return err
	}
	return proof.Sync()
}

func fixtureQuotaInodeLimit(allocated int64, denied bool) (int, error) {
	if denied && allocated >= 7<<20 && allocated <= 8<<20 {
		return 128, nil
	}
	if denied && allocated >= 255<<20 && allocated <= 256<<20 {
		return 65536, nil
	}
	return 0, fmt.Errorf("unsupported or unenforced fixed fixture byte quota: allocated=%d denied=%v", allocated, denied)
}
