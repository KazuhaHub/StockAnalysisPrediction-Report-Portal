//go:build linux

package driver

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"
)

func TestNativeCandidateMountPinsBoundedIPC(t *testing.T) {
	plan, _, request := nativeMountFixture(t)
	path := request.HostConfig.Mounts[1].Source
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	file := os.NewFile(uintptr(fd), "owned-ipc")
	defer file.Close()
	identity := CandidateIdentity(plan.owner)
	if err := validateCandidateHTTPStorage(fd, identity, true); err != nil {
		t.Fatal(err)
	}
	foreign := identity
	foreign.ResourceID = "00000000-0000-4000-8000-000000009999"
	if validateCandidateHTTPStorage(fd, foreign, true) == nil {
		t.Fatal("foreign IPC identity accepted")
	}
	parent, err := unix.Open(filepath.Dir(path), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(parent)
	if err := provisionCandidateHTTPStorage(context.Background(), parent, identity); err != nil {
		t.Fatal("exact mounted storage replay", err)
	}
	if err := provisionCandidateHTTPStorage(context.Background(), parent, foreign); err == nil {
		t.Fatal("foreign storage was repaired/replaced")
	}
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	child := exec.Command(binary, "-test.run=^TestCandidateIPCUnprivilegedChild$", "-test.v", "-test.timeout=20s")
	child.Env = []string{"PORTAL_APP_IPC_CHILD=1", "GOMAXPROCS=2", "GITHUB_ACTIONS=true", "RUNNER_ENVIRONMENT=github-hosted"}
	child.ExtraFiles = []*os.File{file}
	child.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: 65532, Gid: 65532, Groups: []uint32{65532}}}
	output, err := child.CombinedOutput()
	if err != nil || !strings.Contains(string(output), "IPC_BYTE_AND_INODE_LIMITS_ENFORCED") {
		t.Fatal("unprivileged IPC quota enforcement", err, string(output))
	}
	if err := validateCandidateHTTPStorage(fd, identity, true); err != nil {
		t.Fatal("app changed trusted IPC storage metadata", err)
	}
	t.Log("bounded IPC storage enforced bytes/inodes for UID 65532 and protected its trusted identity")
}

func TestCandidateIPCUnprivilegedChild(t *testing.T) {
	if os.Getenv("PORTAL_APP_IPC_CHILD") != "1" {
		t.Skip("owned IPC child only")
	}
	if os.Geteuid() != 65532 || os.Getegid() != 65532 || os.Getenv("GITHUB_ACTIONS") != "true" || os.Getenv("RUNNER_ENVIRONMENT") != "github-hosted" {
		t.Fatal("disposable unprivileged IPC child required")
	}
	fd := 3
	var fs unix.Statfs_t
	if unix.Fstatfs(fd, &fs) != nil || fs.Type != unix.TMPFS_MAGIC || fs.Blocks*uint64(fs.Bsize) != uint64(candidateIPCBytes) || fs.Files != candidateIPCInodes {
		t.Fatal("unexpected owned IPC limits")
	}
	if _, err := unix.Fgetxattr(fd, candidateIPCMarker, make([]byte, 2048)); err == nil {
		t.Fatal("app could read trusted identity")
	}
	if err := unix.Fsetxattr(fd, candidateIPCMarker, []byte("tamper"), 0); err == nil {
		t.Fatal("app could change trusted identity")
	}
	data, err := unix.Openat(fd, "byte-probe", unix.O_CREAT|unix.O_EXCL|unix.O_WRONLY|unix.O_CLOEXEC, 0600)
	if err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, 65536)
	written := 0
	for written < int(candidateIPCBytes)*2 {
		n, e := unix.Write(data, buffer)
		if n > 0 {
			written += n
		}
		if e != nil {
			err = e
			break
		}
		if n == 0 {
			t.Fatal("zero write before quota refusal")
		}
	}
	unix.Close(data)
	if !errors.Is(err, unix.ENOSPC) && !errors.Is(err, unix.EDQUOT) {
		t.Fatal("byte limit was not enforced", written, err)
	}
	if written <= 0 || written > int(candidateIPCBytes) {
		t.Fatal("byte quota escaped", written)
	}
	if err := unix.Unlinkat(fd, "byte-probe", 0); err != nil {
		t.Fatal(err)
	}
	created := 0
	err = nil
	for created < int(candidateIPCInodes)*2 {
		entry, e := unix.Openat(fd, fmt.Sprintf("inode-probe-%d", created), unix.O_CREAT|unix.O_EXCL|unix.O_WRONLY|unix.O_CLOEXEC, 0600)
		if e != nil {
			err = e
			break
		}
		unix.Close(entry)
		created++
	}
	if !errors.Is(err, unix.ENOSPC) && !errors.Is(err, unix.EDQUOT) {
		t.Fatal("inode limit was not enforced", created, err)
	}
	if created < 1 || created >= int(candidateIPCInodes) {
		t.Fatal("inode quota escaped", created)
	}
	for i := 0; i < created; i++ {
		if err := unix.Unlinkat(fd, fmt.Sprintf("inode-probe-%d", i), 0); err != nil {
			t.Fatal(err)
		}
	}
	t.Log("IPC_BYTE_AND_INODE_LIMITS_ENFORCED")
}

func TestNativeCandidateMountPinsIPCDrift(t *testing.T) {
	for _, change := range []string{"bytes", "inodes", "marker"} {
		t.Run(change, func(t *testing.T) {
			plan, data, request := nativeMountFixture(t)
			path := request.HostConfig.Mounts[1].Source
			pins, err := pinCandidateMounts(context.Background(), plan, data)
			if err != nil {
				t.Fatal(err)
			}
			defer pins.Close()
			if change == "marker" {
				t.Cleanup(func() {
					if err := unix.Setxattr(path, candidateIPCMarker, candidateIPCRecord(CandidateIdentity(plan.owner), "mount"), 0); err != nil {
						t.Error(err)
					}
				})
				if err := unix.Setxattr(path, candidateIPCMarker, []byte("foreign"), 0); err != nil {
					t.Fatal(err)
				}
			} else {
				options := "size=33554432,nr_inodes=1024,mode=0700,uid=65532,gid=65532"
				if change == "inodes" {
					options = "size=16777216,nr_inodes=2048,mode=0700,uid=65532,gid=65532"
				}
				t.Cleanup(func() {
					if err := unix.Mount("", path, "tmpfs", unix.MS_REMOUNT|unix.MS_NODEV|unix.MS_NOSUID|unix.MS_NOEXEC, "size=16777216,nr_inodes=1024,mode=0700,uid=65532,gid=65532"); err != nil {
						t.Error(err)
					}
				})
				if err := unix.Mount("", path, "tmpfs", unix.MS_REMOUNT|unix.MS_NODEV|unix.MS_NOSUID|unix.MS_NOEXEC, options); err != nil {
					t.Fatal(err)
				}
			}
			if err := pins.validate(context.Background(), plan); err == nil {
				t.Fatal("IPC limit/identity drift survived pinned checks")
			}
		})
	}
}

func TestNativeCandidateMountPinsIPCRefusesUnmarkedDirectory(t *testing.T) {
	requireMountAcceptance(t)
	root, err := os.MkdirTemp("/run", "portal-ipc-unmarked-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	parent, err := unix.Open(root, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(parent)
	path := filepath.Join(root, "http")
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chown(path, 65532, 65532); err != nil {
		t.Fatal(err)
	}
	identity := CandidateIdentity(containerFixture(t).owner)
	if provisionCandidateHTTPStorage(context.Background(), parent, identity) == nil {
		t.Fatal("unmarked directory was adopted")
	}
	if releaseCandidateHTTPStorage(context.Background(), parent, path, identity) == nil {
		t.Fatal("unmarked directory was released")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal("refusal removed the unmarked directory", err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if provisionCandidateHTTPStorage(ctx, parent, identity) == nil {
		t.Fatal("cancelled provision continued")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("cancelled provision created a directory")
	}
}

func TestCandidateIPCReferencesResolveAliases(t *testing.T) {
	root := t.TempDir()
	ipc := filepath.Join(root, "owned", "http")
	if err := os.MkdirAll(filepath.Join(ipc, "child"), 0700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(root, "alias")
	if err := os.Symlink(filepath.Dir(ipc), alias); err != nil {
		t.Fatal(err)
	}
	for _, source := range []string{ipc, filepath.Dir(ipc), alias, alias + "/", filepath.Join(ipc, "child"), "/"} {
		if used, err := candidateIPCReferenced(ipc, source); err != nil || !used {
			t.Fatal("mount reference alias missed", err)
		}
	}
	if used, err := candidateIPCReferenced(ipc, filepath.Join(root, "missing")); err == nil || used {
		t.Fatal("unresolved inventory silently treated as no reference")
	}
	if used, err := candidateIPCReferenced(ipc, ""); err != nil || used {
		t.Fatal("empty tmpfs source treated as host bind")
	}
}

func TestCandidateIPCInventoryCannotOmitBindSources(t *testing.T) {
	for _, raw := range []string{"", `{}`, `[{"Type":"bind"}]`, `[{"Type":"volume","Source":"relative"}]`, `[{"Type":"unknown","Source":"/run"}]`, `[{"Type":"tmpfs","Source":"/run"}]`} {
		if _, err := candidateIPCMountSources([]byte(raw)); err == nil {
			t.Fatal("incomplete inventory admitted", raw)
		}
	}
	for _, raw := range []string{`null`, `[]`, `[{"Type":"tmpfs","Source":""}]`, `[{"Type":"bind","Source":"/run"}]`} {
		if _, err := candidateIPCMountSources([]byte(raw)); err != nil {
			t.Fatal("valid projected mount inventory refused", err)
		}
	}
}
