package hostsetup

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"testing"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/journal"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/provision"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/service"
)

var errBusy = errors.New("data root is locked")

const (
	fixtureSize = 2 << 30
	fixtureUID  = 41001
)

// host is a fake host: a temporary directory stands for the file system, and everything privileged (mounts,
// tools, Docker, the journal, the data-root lock) is a fake that records what it was asked to do.
type host struct {
	t      *testing.T
	root   string
	gid    uint32
	env    Env
	mounts []provision.Mount
	loops  map[string]string // loop device -> backing file
	exec   [][]string        // every command, in order
	tools  map[string]bool
	users  []Account
	groups []Account

	dockerErr    error
	findmntErr   error
	findmntOut   string
	deployment   *journal.Deployment
	journalErr   error
	journalOpen  int
	dataBusy     bool
	dataLocks    int
	dataReleased int
	verifyErr    map[string]error
	fsKinds      map[string]string
	nextLoop     int
	verified     []string // every table findmnt --verify was given
}

func newHost(t *testing.T) *host {
	t.Helper()
	dir, err := os.MkdirTemp("", "hostsetup-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	if dir, err = filepath.EvalSymlinks(dir); err != nil {
		t.Fatal(err)
	}
	h := &host{t: t, root: dir, gid: uint32(os.Getegid()), loops: map[string]string{}, verifyErr: map[string]error{}, fsKinds: map[string]string{},
		tools: map[string]bool{"losetup": true, "mkfs.xfs": true, "mount": true, "umount": true, "fallocate": true, "blkid": true, "findmnt": true, "chattr": true}}
	h.build()
	for _, p := range []string{"/etc", "/var", "/var/lib", "/srv", "/run", "/run/systemd", "/run/systemd/system", "/root"} {
		h.mkdir(p, 0o755)
	}
	h.write("/etc/fstab", "# /etc/fstab\n/dev/sda1 / ext4 defaults 0 1\n", 0o644)
	return h
}

func (h *host) build() {
	h.env = Env{
		Prefix: h.root, RootUID: uint32(os.Geteuid()), RootGID: h.gid,
		Kernel:     func() (string, error) { return "6.8.0-45-generic", nil },
		Namespaces: func() error { return nil },
		Docker:     func(context.Context, string) error { return h.dockerErr },
		HasTool:    func(name string) bool { return h.tools[name] },
		Accounts:   func() ([]Account, []Account, error) { return h.users, h.groups, nil },
		LoopBacking: func(device string) (string, error) {
			if image, ok := h.loops[device]; ok {
				return image, nil
			}
			return "", errors.New("no such loop device")
		},
		ReadDeployment: func(context.Context, string) (journal.Deployment, error) {
			if h.deployment == nil {
				return journal.Deployment{}, journal.ErrNotEnrolled
			}
			return *h.deployment, nil
		},
		OpenJournal: func(string) (Journal, error) {
			if h.journalErr != nil {
				return nil, h.journalErr
			}
			h.journalOpen++
			return &fakeJournal{h: h}, nil
		},
		LockDataRoot: func(dir string) (func(), error) {
			if h.dataBusy {
				return nil, errBusy
			}
			h.dataLocks++
			return func() { h.dataReleased++ }, nil
		},
		IsDataRootBusy: func(err error) bool { return errors.Is(err, errBusy) },
		Provision: provision.Env{
			Exec:    fakeExec{h},
			FS:      rootedFS{h},
			Mounts:  func() ([]provision.Mount, error) { return append([]provision.Mount(nil), h.mounts...), nil },
			Verify:  h.verify,
			Geteuid: func() int { return 0 },
		},
	}
}

func (h *host) verify(ctx context.Context, dir string) (provision.VerifyResult, error) {
	if err, ok := h.verifyErr[dir]; ok {
		return provision.VerifyResult{}, err
	}
	if h.dataBusy {
		return provision.VerifyResult{}, errBusy
	}
	if !isDir(h.env.path(dir)) {
		return provision.VerifyResult{}, errors.New("no such directory")
	}
	for _, m := range h.mounts {
		if under(dir, m.Point) && m.FSType == "xfs" {
			return provision.VerifyResult{UsableBytes: fixtureSize - 64<<20}, nil
		}
	}
	return provision.VerifyResult{}, errors.New("not on an XFS mount")
}

// ---- file system helpers ------------------------------------------------------------------------------

func (h *host) p(path string) string { return filepath.Join(h.root, path) }

func (h *host) mkdir(path string, mode os.FileMode) {
	h.t.Helper()
	if err := os.MkdirAll(h.p(path), 0o755); err != nil {
		h.t.Fatal(err)
	}
	if err := os.Chmod(h.p(path), mode); err != nil {
		h.t.Fatal(err)
	}
}

func (h *host) write(path, content string, mode os.FileMode) {
	h.t.Helper()
	if err := os.MkdirAll(filepath.Dir(h.p(path)), 0o755); err != nil {
		h.t.Fatal(err)
	}
	if err := os.WriteFile(h.p(path), []byte(content), mode); err != nil {
		h.t.Fatal(err)
	}
	if err := os.Chmod(h.p(path), mode); err != nil {
		h.t.Fatal(err)
	}
}

func (h *host) read(path string) string {
	raw, err := os.ReadFile(h.p(path))
	if err != nil {
		h.t.Fatal(err)
	}
	return string(raw)
}

func (h *host) exists(path string) bool {
	_, err := os.Lstat(h.p(path))
	return err == nil
}

func (h *host) stat(path string) (mode os.FileMode, uid, gid uint32) {
	h.t.Helper()
	info, err := os.Lstat(h.p(path))
	if err != nil {
		h.t.Fatalf("%s: %v", path, err)
	}
	st := info.Sys().(*syscall.Stat_t)
	return info.Mode().Perm(), st.Uid, st.Gid
}

// snapshot hashes every entry under the temporary root: path, type, mode, owner, size and content.
func (h *host) snapshot() string {
	sum := sha256.New()
	var lines []string
	filepath.WalkDir(h.root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		info, _ := d.Info()
		st := info.Sys().(*syscall.Stat_t)
		line := fmt.Sprintf("%s|%v|%d|%d|%d", strings.TrimPrefix(path, h.root), info.Mode(), st.Uid, st.Gid, info.Size())
		if info.Mode().IsRegular() && info.Size() < 1<<20 {
			raw, _ := os.ReadFile(path)
			c := sha256.Sum256(raw)
			line += "|" + hex.EncodeToString(c[:])
		}
		lines = append(lines, line)
		return nil
	})
	sort.Strings(lines)
	sum.Write([]byte(strings.Join(lines, "\n")))
	state := fmt.Sprintf("%x|mounts=%d|loops=%d|deployment=%v", sum.Sum(nil), len(h.mounts), len(h.loops), h.deployment != nil)
	return state
}

// mutating lists the commands that change a host. A plan, or a refused run, must issue none of them.
func (h *host) mutating() []string {
	var found []string
	for _, c := range h.exec {
		switch c[0] {
		case "mount", "umount", "mkfs.xfs", "fallocate", "chattr", "systemctl":
			found = append(found, strings.Join(c, " "))
		case "losetup":
			if len(c) > 1 && (c[1] == "--find" || c[1] == "--detach") {
				found = append(found, strings.Join(c, " "))
			}
		case "findmnt":
			found = append(found, strings.Join(c, " "))
		}
	}
	return found
}

func (h *host) ran(name string) int {
	n := 0
	for _, c := range h.exec {
		if c[0] == name {
			n++
		}
	}
	return n
}

// ---- fakes ---------------------------------------------------------------------------------------------

type fakeJournal struct{ h *host }

func (j *fakeJournal) Deployment(context.Context) (journal.Deployment, error) {
	if j.h.deployment == nil {
		return journal.Deployment{}, journal.ErrNotEnrolled
	}
	return *j.h.deployment, nil
}

func (j *fakeJournal) Enroll(ctx context.Context, request journal.ControllerEnrollment) (bool, error) {
	if d := j.h.deployment; d != nil {
		if d.DeploymentID != request.DeploymentID || d.ControllerEpoch != request.ControllerEpoch || d.ControlRevision != request.ControlRevision ||
			d.Fingerprint != request.Fingerprint || !bytes.Equal(d.PublicKey, request.PublicKey) {
			return false, journal.ErrEnrollmentConflict
		}
		return false, nil
	}
	j.h.deployment = &journal.Deployment{ControllerEnrollment: request, Revision: 1}
	return true, nil
}

func (j *fakeJournal) Close() error { return nil }

type fakeExec struct{ h *host }

func (e fakeExec) Run(ctx context.Context, name string, args ...string) (string, error) {
	h := e.h
	h.exec = append(h.exec, append([]string{name}, args...))
	switch name {
	case "blkid":
		image := args[len(args)-1]
		if h.exists(image) {
			return "xfs\n", nil
		}
		return "", errors.New("no filesystem")
	case "fallocate":
		size := int64(0)
		fmt.Sscanf(args[1], "%d", &size)
		return "", os.Truncate(h.p(args[2]), size)
	case "losetup":
		switch args[0] {
		case "--json":
			image := args[len(args)-3]
			var list []map[string]string
			var devices []string
			for dev := range h.loops {
				devices = append(devices, dev)
			}
			sort.Strings(devices)
			for _, dev := range devices {
				if h.loops[dev] == image {
					list = append(list, map[string]string{"name": dev, "back-file": image})
				}
			}
			if len(list) == 0 {
				return "", nil
			}
			raw, _ := json.Marshal(map[string]any{"loopdevices": list})
			return string(raw), nil
		case "--find":
			h.nextLoop++
			dev := fmt.Sprintf("/dev/loop%d", 10+h.nextLoop)
			h.loops[dev] = args[len(args)-1]
			return dev + "\n", nil
		}
	case "mount":
		// mount -t xfs -o OPTIONS DEVICE POINT
		options := map[string]bool{}
		for _, o := range strings.Split(args[3], ",") {
			options[o] = true
		}
		h.mounts = append(h.mounts, provision.Mount{Point: args[5], FSType: "xfs", Source: args[4], Options: options})
		return "", nil
	case "findmnt":
		// Like the real tool, judge the table it is given: a line starting with BROKEN is an error in it.
		if raw, err := os.ReadFile(h.p(args[len(args)-1])); err == nil {
			h.verified = append(h.verified, string(raw))
			if strings.Contains(string(raw), "BROKEN") {
				return "0 parse errors, 1 error, 4 warnings\n", errors.New("exit status 1")
			}
		}
		return h.findmntOut, h.findmntErr
	}
	return "", nil
}

// rootedFS is provision's file system seam over the temporary root. Files owned by the test user stand for
// root-owned ones, and an allocated size is reported as the file size, so no test allocates real disk.
type rootedFS struct{ h *host }

func (f rootedFS) Stat(path string) (provision.FileInfo, error) {
	info, err := os.Lstat(f.h.p(path))
	if errors.Is(err, os.ErrNotExist) {
		return provision.FileInfo{}, nil
	}
	if err != nil {
		return provision.FileInfo{}, err
	}
	st := info.Sys().(*syscall.Stat_t)
	out := provision.FileInfo{Exists: true, Dir: info.IsDir(), Regular: info.Mode().IsRegular(), Perm: uint32(info.Mode().Perm()),
		Sticky: info.Mode()&os.ModeSticky != 0, Size: info.Size()}
	if st.Uid == f.h.env.RootUID {
		out.UID = 0
	} else {
		out.UID = st.Uid
	}
	if info.Mode().IsRegular() {
		out.Allocated = info.Size()
	}
	return out, nil
}

func (f rootedFS) DirEmpty(path string) (bool, error) {
	entries, err := os.ReadDir(f.h.p(path))
	return len(entries) == 0, err
}

func (f rootedFS) Resolve(path string) (string, error) {
	resolved, err := filepath.EvalSymlinks(f.h.p(path))
	if err != nil {
		return "", err
	}
	return "/" + strings.TrimPrefix(strings.TrimPrefix(resolved, f.h.root), "/"), nil
}

func (f rootedFS) CreateExclusive(path string, perm uint32) error {
	file, err := os.OpenFile(f.h.p(path), os.O_WRONLY|os.O_CREATE|os.O_EXCL, os.FileMode(perm))
	if err != nil {
		return err
	}
	if err := file.Chmod(os.FileMode(perm)); err != nil {
		file.Close()
		return err
	}
	return file.Close()
}

func (f rootedFS) Mkdir(path string, perm uint32) error {
	if err := os.Mkdir(f.h.p(path), os.FileMode(perm)); err != nil {
		return err
	}
	return os.Chmod(f.h.p(path), os.FileMode(perm))
}

func (f rootedFS) Chmod(path string, perm uint32) error {
	return os.Chmod(f.h.p(path), os.FileMode(perm))
}
func (f rootedFS) Remove(path string) error { return os.Remove(f.h.p(path)) }

func (f rootedFS) FSKind(path string) (string, error) {
	for dir := path; ; dir = filepath.Dir(dir) {
		if kind, ok := f.h.fsKinds[dir]; ok {
			return kind, nil
		}
		if dir == "/" {
			return "ext4", nil
		}
	}
}

// ---- enrollment documents -------------------------------------------------------------------------------

type identity struct {
	doc  string
	key  ed25519.PublicKey
	ctrl service.Controller
}

// enrollmentDoc is a valid public enrollment document for the seed.
func enrollmentDoc(seed byte) identity {
	key := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{seed}, 32)).Public().(ed25519.PublicKey)
	sum := sha256.Sum256(key)
	ctrl := service.Controller{DeploymentID: fmt.Sprintf("10000000-0000-4000-8000-0000000000%02x", seed), ControllerEpoch: "20000000-0000-4000-8000-000000000002",
		ControlRevision: 1, Fingerprint: hex.EncodeToString(sum[:])}
	doc, _ := json.Marshal(map[string]any{
		"format": service.BootstrapFormat, "controller_public_key": base64.RawURLEncoding.EncodeToString(key),
		"controller": ctrl,
	})
	return identity{doc: string(doc), key: key, ctrl: ctrl}
}

func (h *host) enrollmentFile(id identity) string {
	h.write("/root/enrollment.json", id.doc, 0o600)
	return "/root/enrollment.json"
}

func (id identity) deployment() *journal.Deployment {
	return &journal.Deployment{ControllerEnrollment: journal.ControllerEnrollment{DeploymentID: id.ctrl.DeploymentID, ControllerEpoch: id.ctrl.ControllerEpoch,
		ControlRevision: id.ctrl.ControlRevision, PublicKey: id.key, Fingerprint: id.ctrl.Fingerprint}, Revision: 1}
}

// options is a new-host invocation with test sizes and the test user's group.
func (h *host) options(apply bool) Options {
	return Options{Apply: apply, DataSize: fixtureSize, CheckpointSize: fixtureSize, BelowProduction: true,
		ControllerUID: fixtureUID, ControllerGID: h.gid, Release: "v2026.47.1"}
}

func (h *host) run(opts Options) (int, string) {
	var out bytes.Buffer
	code := Run(context.Background(), h.env, opts, &out)
	return code, out.String()
}
