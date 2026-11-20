//go:build linux

package provision

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/data"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/rootfs"
	"golang.org/x/sys/unix"
)

// toolDirs is where the tools are looked up. The caller's PATH is deliberately not consulted: this runs as root.
var toolDirs = []string{"/usr/sbin", "/usr/bin", "/sbin", "/bin"}

type systemExec struct{}

func (systemExec) Run(ctx context.Context, name string, args ...string) (string, error) {
	var path string
	for _, dir := range toolDirs {
		candidate := filepath.Join(dir, name)
		if info, err := os.Stat(candidate); err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0o111 != 0 {
			path = candidate
			break
		}
	}
	if path == "" {
		return "", fmt.Errorf("%s is not installed (looked in %s)", name, strings.Join(toolDirs, ", "))
	}
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Env = []string{"PATH=" + strings.Join(toolDirs, ":"), "LC_ALL=C"}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		detail := strings.TrimSpace(stderr.String())
		if i := strings.IndexByte(detail, '\n'); i >= 0 {
			detail = detail[:i]
		}
		return stdout.String(), fmt.Errorf("%s failed: %v: %s", name, err, detail)
	}
	return stdout.String(), nil
}

type systemFS struct{}

func (systemFS) Stat(path string) (FileInfo, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return FileInfo{}, nil
	}
	if err != nil {
		return FileInfo{}, err
	}
	out := FileInfo{Exists: true, Dir: info.IsDir(), Regular: info.Mode().IsRegular(), Perm: uint32(info.Mode().Perm()), Sticky: info.Mode()&os.ModeSticky != 0, Size: info.Size()}
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		out.UID = st.Uid
		out.Allocated = st.Blocks * 512
	}
	return out, nil
}

func (systemFS) DirEmpty(path string) (bool, error) {
	entries, err := os.ReadDir(path)
	return len(entries) == 0, err
}

func (systemFS) Resolve(path string) (string, error) { return filepath.EvalSymlinks(path) }

func (systemFS) CreateExclusive(path string, perm uint32) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, os.FileMode(perm))
	if err != nil {
		return err
	}
	if err := f.Chmod(os.FileMode(perm)); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

func (systemFS) Mkdir(path string, perm uint32) error {
	if err := os.Mkdir(path, os.FileMode(perm)); err != nil {
		return err
	}
	return os.Chmod(path, os.FileMode(perm))
}

func (systemFS) Chmod(path string, perm uint32) error { return os.Chmod(path, os.FileMode(perm)) }
func (systemFS) Remove(path string) error             { return os.Remove(path) }

func (systemFS) FSKind(path string) (string, error) {
	info, err := rootfs.Inspect(path)
	return info.Kind, err
}

// verifyRoot applies the runner's own data-root checks: the directory must be a root-owned 0700 directory on an
// XFS mount with nodev, nosuid, noexec and project quota enforcement on, and must yield a root identity.
func verifyRoot(ctx context.Context, dir string) (VerifyResult, error) {
	file, err := os.OpenFile(dir, os.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return VerifyResult{}, err
	}
	defer file.Close()
	root, err := data.Open(file)
	if err != nil {
		return VerifyResult{}, err
	}
	defer root.Close()
	if _, err := root.Identity(ctx); err != nil {
		return VerifyResult{}, err
	}
	var st unix.Statfs_t
	if err := unix.Statfs(dir, &st); err != nil || st.Frsize <= 0 {
		return VerifyResult{}, errors.New("the volume size could not be read")
	}
	return VerifyResult{UsableBytes: int64(st.Blocks) * int64(st.Frsize)}, nil
}

func readMounts() ([]Mount, error) {
	file, err := os.Open("/proc/self/mountinfo")
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return ParseMountInfo(file)
}

// SystemEnv is the real host: fixed tool directories, the real mount table and the runner's own data-root checks.
func SystemEnv() (Env, error) {
	return Env{Exec: systemExec{}, FS: systemFS{}, Mounts: readMounts, Verify: verifyRoot, Geteuid: os.Geteuid}, nil
}
