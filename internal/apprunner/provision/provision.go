// Package provision creates the two preallocated, loop-mounted XFS volumes a backend runner needs on a host whose
// filesystem has no project quota (doc 182 section 3.2). It is an operator tool, run once as root: it never
// edits /etc, never touches a mount it did not create, never runs mkfs on an image that already holds content,
// and checks the result with the runner's own data-root checks. Every external effect goes through Env so the
// flow is tested against a fake executor.
package provision

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

const (
	// MountOptions are the options every provisioned volume is mounted with.
	MountOptions = "prjquota,nodev,nosuid,noexec"
	// MinImageBytes and MaxImageBytes bound a requested image.
	MinImageBytes int64 = 2 << 30
	MaxImageBytes int64 = 16 << 40
	// RootDirName is the directory inside each volume the runner is configured to use as its root. The volume's
	// own root stays an ordinary mount point.
	RootDirName = "root"

	gib = int64(1) << 30
)

var (
	// ErrInvalid is a malformed request: nothing was inspected or changed.
	ErrInvalid = errors.New("invalid provisioning request")
	// ErrUnsupported is a platform without the Linux tools this needs.
	ErrUnsupported = errors.New("XFS image provisioning requires Linux")
	// ErrNotRoot is a run without root: loop devices and mounts cannot be changed.
	ErrNotRoot = errors.New("provisioning must run as root")
)

// RefusalError is a state this tool will not change: existing content or a foreign mount. Nothing was modified.
type RefusalError struct{ Volume, Reason string }

func (e *RefusalError) Error() string {
	return "refused: " + e.Volume + " volume: " + e.Reason
}

// Spec is one request: two images, their sizes and where they are mounted.
type Spec struct {
	DataImage, CheckpointImage string
	DataSize, CheckpointSize   int64
	DataMount, CheckpointMount string
}

type volume struct {
	Name, Image, Mount string
	Size               int64
}

func (s Spec) volumes() []volume {
	return []volume{
		{"data", s.DataImage, s.DataMount, s.DataSize},
		{"checkpoint", s.CheckpointImage, s.CheckpointMount, s.CheckpointSize},
	}
}

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, args...))
}

func inside(parent, child string) bool {
	return child == parent || strings.HasPrefix(child, strings.TrimSuffix(parent, "/")+"/")
}

// Validate checks the request without touching the host.
func (s Spec) Validate() error {
	paths := map[string]string{
		"--data-image": s.DataImage, "--checkpoint-image": s.CheckpointImage,
		"--mount-data": s.DataMount, "--mount-checkpoint": s.CheckpointMount,
	}
	for name, path := range paths {
		if path == "" {
			return invalid("%s is required", name)
		}
		if !filepath.IsAbs(path) || filepath.Clean(path) != path || path == "/" {
			return invalid("%s must be an absolute, clean path other than /", name)
		}
		if strings.ContainsAny(path, " \t\n\\,") {
			return invalid("%s must not contain whitespace, backslash or comma", name)
		}
	}
	for _, v := range s.volumes() {
		if v.Size < MinImageBytes || v.Size > MaxImageBytes || v.Size%(1<<20) != 0 {
			return invalid("the %s size must be a whole number of MiB between 2 GiB and 16 TiB", v.Name)
		}
	}
	all := []string{s.DataImage, s.CheckpointImage, s.DataMount, s.CheckpointMount}
	for i := range all {
		for j := range all {
			if i != j && inside(all[i], all[j]) {
				return invalid("the four paths must be distinct and none may lie inside another")
			}
		}
	}
	return nil
}

// ParseSize reads a size such as 8G, 8GiB, 512M or a plain byte count. Suffixes are binary (1G is 1 GiB).
func ParseSize(text string) (int64, error) {
	t := strings.ToLower(strings.TrimSpace(text))
	t = strings.TrimSuffix(strings.TrimSuffix(t, "b"), "i")
	scale := int64(1)
	if t != "" {
		switch t[len(t)-1] {
		case 'k':
			scale, t = 1<<10, t[:len(t)-1]
		case 'm':
			scale, t = 1<<20, t[:len(t)-1]
		case 'g':
			scale, t = 1<<30, t[:len(t)-1]
		case 't':
			scale, t = 1<<40, t[:len(t)-1]
		}
	}
	n, err := strconv.ParseInt(t, 10, 64)
	if err != nil || n <= 0 || n > MaxImageBytes/scale {
		return 0, invalid("size %q is not a positive size such as 8G", text)
	}
	return n * scale, nil
}

// Executor runs an external tool and returns its standard output.
type Executor interface {
	Run(ctx context.Context, name string, args ...string) (string, error)
}

// FileInfo is what the flow needs to know about one path (no symlink following).
type FileInfo struct {
	Exists, Dir, Regular bool
	Perm, UID            uint32
	Sticky               bool
	Size, Allocated      int64
}

// FS is the file-system seam.
type FS interface {
	Stat(path string) (FileInfo, error)
	DirEmpty(path string) (bool, error)
	Resolve(path string) (string, error)
	CreateExclusive(path string, perm uint32) error
	Mkdir(path string, perm uint32) error
	Chmod(path string, perm uint32) error
	Remove(path string) error
	// FSKind is the kind of the filesystem holding path ("ext4", "btrfs", ...).
	FSKind(path string) (string, error)
}

// Mount is one line of the mount table.
type Mount struct {
	Point, FSType, Source string
	Options               map[string]bool
}

// VerifyResult is what the runner's data-root checks observed.
type VerifyResult struct{ UsableBytes int64 }

// Env bundles every external effect.
type Env struct {
	Exec    Executor
	FS      FS
	Mounts  func() ([]Mount, error)
	Verify  func(ctx context.Context, dir string) (VerifyResult, error)
	Geteuid func() int
}

// VolumeResult describes one provisioned or already-provisioned volume.
type VolumeResult struct {
	Name, Image, Mount, RootDir, Loop string
	Action                            string // created, attached, already-provisioned
	UsableBytes                       int64
	Warning                           string
}

type action string

const (
	actCreate action = "create"
	actAttach action = "attach"
	actVerify action = "verify"
)

type plan struct {
	vol          volume
	action       action
	loop         string
	cow          bool
	sparse       bool
	mountExisted bool
}

var loopDevice = regexp.MustCompile(`^/dev/loop[0-9]+$`)

func refuse(v volume, format string, args ...any) error {
	return &RefusalError{Volume: v.Name, Reason: fmt.Sprintf(format, args...)}
}

// Run provisions both volumes. Both are inspected before either is changed, so a refusal on the second never
// leaves the first half-made. With dryRun it only reports what it would do. Re-running after success only
// re-verifies.
func Run(ctx context.Context, env Env, spec Spec, dryRun bool, out io.Writer) error {
	if err := spec.Validate(); err != nil {
		return err
	}
	if env.Exec == nil || env.FS == nil || env.Mounts == nil || env.Verify == nil || env.Geteuid == nil {
		return ErrUnsupported
	}
	if !dryRun && env.Geteuid() != 0 {
		return ErrNotRoot
	}
	var plans []plan
	for _, v := range spec.volumes() {
		p, err := inspect(ctx, env, v)
		if err != nil {
			return err
		}
		plans = append(plans, p)
	}
	if dryRun {
		for _, p := range plans {
			fmt.Fprintf(out, "%s: would %s\n", p.vol.Name, describe(p))
		}
		fmt.Fprintln(out, "dry run: nothing was changed")
		return nil
	}
	var results []VolumeResult
	for _, p := range plans {
		r, err := apply(ctx, env, p)
		if err != nil {
			if len(results) > 0 {
				fmt.Fprintf(out, "the %s volume is complete; fix the cause and run the same command again to continue\n", results[0].Name)
			}
			return fmt.Errorf("%s volume: %w", p.vol.Name, err)
		}
		results = append(results, r)
		fmt.Fprintf(out, "%s: %s image %s, mounted at %s on %s, usable %d bytes\n", r.Name, r.Action, r.Image, r.Mount, r.Loop, r.UsableBytes)
		if r.Warning != "" {
			fmt.Fprintf(out, "%s: warning: %s\n", r.Name, r.Warning)
		}
	}
	render(out, spec, results)
	return nil
}

func describe(p plan) string {
	switch p.action {
	case actCreate:
		return fmt.Sprintf("create %s (%d bytes, preallocated), format XFS, attach a loop device and mount at %s", p.vol.Image, p.vol.Size, p.vol.Mount)
	case actAttach:
		if p.sparse {
			return fmt.Sprintf("re-preallocate the sparse XFS image %s in place (fallocate, data kept), attach it and mount it at %s", p.vol.Image, p.vol.Mount)
		}
		return fmt.Sprintf("attach the existing XFS image %s and mount it at %s", p.vol.Image, p.vol.Mount)
	default:
		return fmt.Sprintf("find %s already provisioned at %s and re-verify it", p.vol.Image, p.vol.Mount)
	}
}

// parentCheck requires every directory from the path's parent up to / to be a real directory (no symbolic link)
// owned by root and not group- or world-writable. A root-owned sticky directory such as /tmp is allowed as an
// ancestor, because nobody else can replace the root-owned entry below it.
func parentCheck(env Env, v volume, label, path string) error {
	for dir := filepath.Dir(path); ; dir = filepath.Dir(dir) {
		info, err := env.FS.Stat(dir)
		if err != nil {
			return refuse(v, "cannot read the directory %s above the %s: %v", dir, label, err)
		}
		if !info.Exists || !info.Dir {
			return refuse(v, "the directory %s above the %s does not exist; create it first", dir, label)
		}
		sticky := info.Sticky
		if info.UID != 0 || (info.Perm&0o022 != 0 && !sticky) {
			return refuse(v, "the directory %s above the %s must be owned by root and not group- or world-writable", dir, label)
		}
		resolved, err := env.FS.Resolve(dir)
		if err != nil || resolved != dir {
			return refuse(v, "the directory %s above the %s must not be or pass through a symbolic link", dir, label)
		}
		if dir == "/" {
			return nil
		}
	}
}

func inspect(ctx context.Context, env Env, v volume) (plan, error) {
	p := plan{vol: v}
	if err := parentCheck(env, v, "image", v.Image); err != nil {
		return p, err
	}
	if err := parentCheck(env, v, "mount point", v.Mount); err != nil {
		return p, err
	}
	kind, err := env.FS.FSKind(filepath.Dir(v.Image))
	if err != nil {
		return p, refuse(v, "cannot read the filesystem under the image: %v", err)
	}
	switch kind {
	case "ext4", "xfs":
	case "btrfs":
		p.cow = true
	default:
		return p, refuse(v, "the image directory is on %s; only ext4, XFS and btrfs are supported backing filesystems (for ZFS, put XFS on a zvol instead)", kind)
	}
	image, err := env.FS.Stat(v.Image)
	if err != nil {
		return p, refuse(v, "cannot read the image: %v", err)
	}
	var loops []string
	if image.Exists {
		if !image.Regular || image.UID != 0 || image.Perm != 0o600 {
			return p, refuse(v, "%s exists but is not a root-owned regular file with mode 0600", v.Image)
		}
		if image.Size != v.Size {
			return p, refuse(v, "%s exists with size %d bytes, not the requested %d; growth is a separate procedure (handbook)", v.Image, image.Size, v.Size)
		}
		// A sparse image is degraded, not wrong: it is re-preallocated in place below, while it is unmounted.
		p.sparse = image.Allocated < image.Size
		typ, err := env.Exec.Run(ctx, "blkid", "-p", "-o", "value", "-s", "TYPE", v.Image)
		if err != nil {
			return p, refuse(v, "%s exists and holds no recognised filesystem; it is not touched", v.Image)
		}
		if strings.TrimSpace(typ) != "xfs" {
			return p, refuse(v, "%s holds a %s filesystem, not XFS; it is not touched", v.Image, strings.TrimSpace(typ))
		}
		if loops, err = attachedLoops(ctx, env, v.Image); err != nil {
			return p, refuse(v, "cannot list the loop devices of %s: %v", v.Image, err)
		}
	}
	point, err := env.FS.Stat(v.Mount)
	if err != nil {
		return p, refuse(v, "cannot read the mount point: %v", err)
	}
	if point.Exists && !point.Dir {
		return p, refuse(v, "%s exists and is not a directory", v.Mount)
	}
	mounts, err := env.Mounts()
	if err != nil {
		return p, refuse(v, "cannot read the mount table: %v", err)
	}
	var at *Mount
	for i := range mounts {
		m := &mounts[i]
		if m.Point == v.Mount {
			at = m
		} else if strings.HasPrefix(m.Point, v.Mount+"/") {
			return p, refuse(v, "another filesystem is mounted below %s", v.Mount)
		}
	}
	if at != nil {
		ours := image.Exists && contains(loops, at.Source) && at.FSType == "xfs" &&
			(at.Options["prjquota"] || at.Options["pquota"]) && at.Options["nodev"] && at.Options["nosuid"] && at.Options["noexec"]
		if !ours {
			return p, refuse(v, "%s is already a mount point of %s (%s) that is not this image mounted with %s; it is not touched", v.Mount, at.Source, at.FSType, MountOptions)
		}
		p.action, p.loop, p.mountExisted = actVerify, at.Source, true
		return p, nil
	}
	for _, m := range mounts {
		if contains(loops, m.Source) {
			return p, refuse(v, "%s is already mounted at %s; it is not touched", v.Image, m.Point)
		}
	}
	if len(loops) > 0 {
		return p, refuse(v, "%s is attached to %s but not mounted at %s; detach it or mount it yourself, it is not touched", v.Image, strings.Join(loops, ", "), v.Mount)
	}
	if point.Exists {
		empty, err := env.FS.DirEmpty(v.Mount)
		if err != nil || !empty {
			return p, refuse(v, "%s exists and is not an empty directory", v.Mount)
		}
	}
	p.mountExisted = point.Exists
	if image.Exists {
		p.action = actAttach
	} else {
		p.action = actCreate
	}
	return p, nil
}

func contains(list []string, item string) bool {
	for _, e := range list {
		if e == item {
			return true
		}
	}
	return false
}

func apply(ctx context.Context, env Env, p plan) (VolumeResult, error) {
	v := p.vol
	result := VolumeResult{Name: v.Name, Image: v.Image, Mount: v.Mount, RootDir: filepath.Join(v.Mount, RootDirName)}
	// Only what this run made is undone, newest first. The first undo step that fails stops the rollback: a
	// failed umount must never be followed by detaching the loop device or deleting the image under it.
	var undo []func() error
	rollback := func(err error) (VolumeResult, error) {
		for i := len(undo) - 1; i >= 0; i-- {
			if uerr := undo[i](); uerr != nil {
				return result, fmt.Errorf("%w (rollback stopped, left in place for the operator: %v)", err, uerr)
			}
		}
		return result, err
	}
	preallocated := func() bool {
		info, err := env.FS.Stat(v.Image)
		return err == nil && info.Exists && info.Allocated >= v.Size
	}
	switch p.action {
	case actVerify:
		result.Action, result.Loop = "already-provisioned", p.loop
		if p.sparse {
			result.Warning = fmt.Sprintf("%s is sparse and mounted; unmount it and run this command again to re-preallocate it in place (data is kept)", v.Image)
		}
	default:
		if !p.mountExisted {
			if err := env.FS.Mkdir(v.Mount, 0o700); err != nil {
				return rollback(fmt.Errorf("create the mount point: %w", err))
			}
			undo = append(undo, func() error { return env.FS.Remove(v.Mount) })
		}
		if p.action == actCreate {
			if err := env.FS.CreateExclusive(v.Image, 0o600); err != nil {
				return rollback(fmt.Errorf("create the image: %w", err))
			}
			undo = append(undo, func() error { return env.FS.Remove(v.Image) })
			if p.cow {
				// Copy-on-write would fragment a database-like image; the flag only works on an empty file.
				if _, err := env.Exec.Run(ctx, "chattr", "+C", v.Image); err != nil {
					return rollback(fmt.Errorf("chattr +C on the image: %w", err))
				}
			}
		}
		if p.action == actCreate || p.sparse {
			// For a sparse existing image this fills the holes in place and keeps every written block.
			if _, err := env.Exec.Run(ctx, "fallocate", "-l", strconv.FormatInt(v.Size, 10), v.Image); err != nil {
				return rollback(fmt.Errorf("preallocate the image: %w", err))
			}
			if !preallocated() {
				return rollback(fmt.Errorf("the image is not fully preallocated; the filesystem under it does not support fallocate"))
			}
			if p.sparse {
				result.Warning = fmt.Sprintf("%s was sparse and has been re-preallocated in place", v.Image)
			}
		}
		out, err := env.Exec.Run(ctx, "losetup", "--find", "--show", "--direct-io=on", v.Image)
		if err != nil {
			return rollback(fmt.Errorf("attach a loop device: %w", err))
		}
		loop := strings.TrimSpace(out)
		if !loopDevice.MatchString(loop) {
			return rollback(fmt.Errorf("losetup returned an unexpected device name"))
		}
		undo = append(undo, func() error {
			_, err := env.Exec.Run(ctx, "losetup", "--detach", loop)
			return err
		})
		if p.action == actCreate {
			// -K: no discard. A discard on a loop device punches holes in the preallocated image. No -f:
			// mkfs.xfs itself refuses a device that already carries a signature.
			if _, err := env.Exec.Run(ctx, "mkfs.xfs", "-K", loop); err != nil {
				return rollback(fmt.Errorf("format XFS: %w", err))
			}
			if !preallocated() {
				return rollback(fmt.Errorf("formatting left the image sparse; the loop device passed a discard down"))
			}
		}
		if _, err := env.Exec.Run(ctx, "mount", "-t", "xfs", "-o", MountOptions, loop, v.Mount); err != nil {
			return rollback(fmt.Errorf("mount: %w", err))
		}
		undo = append(undo, func() error {
			_, err := env.Exec.Run(ctx, "umount", v.Mount)
			return err
		})
		if err := env.FS.Chmod(v.Mount, 0o700); err != nil {
			return rollback(fmt.Errorf("restrict the mount point: %w", err))
		}
		result.Loop = loop
		result.Action = "created"
		if p.action == actAttach {
			result.Action = "attached"
		}
	}
	root, err := env.FS.Stat(result.RootDir)
	if err != nil {
		return rollback(fmt.Errorf("read the runner root directory: %w", err))
	}
	if !root.Exists {
		if err := env.FS.Mkdir(result.RootDir, 0o700); err != nil {
			return rollback(fmt.Errorf("create the runner root directory: %w", err))
		}
	} else if !root.Dir {
		return rollback(fmt.Errorf("%s exists and is not a directory", result.RootDir))
	}
	verified, err := env.Verify(ctx, result.RootDir)
	if err != nil {
		return rollback(fmt.Errorf("the runner's data-root checks refused %s: %w", result.RootDir, err))
	}
	result.UsableBytes = verified.UsableBytes
	return result, nil
}

// attachedLoops lists the loop devices whose backing file is image.
func attachedLoops(ctx context.Context, env Env, image string) ([]string, error) {
	out, err := env.Exec.Run(ctx, "losetup", "--json", "--list", "--associated", image, "--output", "NAME,BACK-FILE")
	if err != nil {
		return nil, err
	}
	return parseLoops(out, image)
}
