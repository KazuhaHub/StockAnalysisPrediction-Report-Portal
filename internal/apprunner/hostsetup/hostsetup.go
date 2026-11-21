// Package hostsetup is `app-runner host-setup` (doc 187 section 8): one idempotent command that prepares a host
// for the compose runner, or adopts the layout a systemd runner already uses, and changes nothing without
// --apply.
//
// It owns only the service.json fields that name paths, the controller IDs, the controller block and the whole
// of bootstrap.json. Every other field of an existing service.json is the operator's and is never read for
// comparison or rewritten. Existing state is compared by parsed value, never by bytes. It never deletes,
// unmounts or detaches anything it did not create in the same run, and it never edits /etc/passwd,
// /etc/group, /etc/docker, unit files, ufw or packages.
//
// Every effect on the host goes through Env, so the whole flow runs in tests against a temporary root with
// fakes for mounts, tools, Docker and the journal.
package hostsetup

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/journal"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/provision"
)

// Defaults for a new host (doc 187 section 7).
const (
	DefaultControllerID   uint32 = 41001
	DefaultConfigDir             = "/etc/portal-app-runner"
	DefaultStateDir              = "/var/lib/portal-app-runner"
	DefaultDockerSocket          = "/run/docker.sock"
	DefaultDataRoot              = "/srv/portal-runner/data/root"
	DefaultCheckpointRoot        = "/srv/portal-runner/checkpoint/root"
	DefaultIPCRoot               = "/run/portal-app-runner/ipc"
	// GroupName is doc 146's controller group. A group of this name that holds the requested GID is accepted.
	GroupName = "portal-app-runner"

	gib                       = int64(1) << 30
	DefaultDataSize           = 8 * gib
	DefaultCheckpointSize     = 16 * gib
	productionCheckpointBytes = 16 * gib

	minKernelMajor = 5
	minKernelMinor = 14

	fstabPath = "/etc/fstab"
)

// Exit statuses. 2 (usage) is the command's own.
const (
	ExitOK      = 0
	ExitFailed  = 1
	ExitPending = 3
)

// Options is one invocation. A zero string or ID means "not given": a new host takes the default, an adopted
// host keeps its existing value, and a value that is given and disagrees with the existing one refuses.
type Options struct {
	Apply bool
	// ConfigDir holds service.json and bootstrap.json.
	ConfigDir string
	// Enrollment is the path of the public enrollment document, read on the host.
	Enrollment string
	// BelowProduction allows a checkpoint volume under 16 GiB (doc 185 Q8).
	BelowProduction bool
	// Release is the tag printed in the .env report; empty prints a placeholder.
	Release string

	ControllerUID, ControllerGID uint32
	DockerSocket                 string
	JournalDir, ImageRoot        string
	IPCRoot, ControlRoot         string
	DataRoot, CheckpointRoot     string
	// ImageDir is where the two XFS image files go on a new host (default <state>/volumes).
	ImageDir                 string
	DataSize, CheckpointSize int64
}

// Account is one user or group of the host database.
type Account struct {
	Name string
	ID   uint32
}

// Journal is the journal as host-setup uses it: opened with its exclusive owner lock held, so a running runner
// refuses it with journal.ErrInUse.
type Journal interface {
	Deployment(ctx context.Context) (journal.Deployment, error)
	Enroll(ctx context.Context, request journal.ControllerEnrollment) (created bool, err error)
	Close() error
}

// Env bundles every external effect. In production NewSystemEnv fills it; paths are the host's. Tests set Prefix
// so that the files host-setup itself reads and writes live in a temporary directory, and RootUID/RootGID to
// the test user so ownership checks run unprivileged; every function field then receives logical paths.
type Env struct {
	Prefix           string
	RootUID, RootGID uint32

	// Kernel returns the release string of the running kernel.
	Kernel func() (string, error)
	// Namespaces refuses unless the process shares PID 1's mount, network and PID namespaces.
	Namespaces func() error
	// Docker refuses unless the daemon passes the runner's profile check and is identified in the runner's
	// namespaces.
	Docker func(ctx context.Context, socket string) error
	// HasTool reports whether a tool is present in the host's /usr/sbin:/usr/bin:/sbin:/bin.
	HasTool func(name string) bool
	// Accounts reads the host's /etc/passwd and /etc/group as plain files (no NSS).
	Accounts func() (users, groups []Account, err error)
	// LoopBacking returns the backing file of a loop device.
	LoopBacking func(device string) (string, error)
	// ReadDeployment reads the enrolled identity without any lock.
	ReadDeployment func(ctx context.Context, dir string) (journal.Deployment, error)
	// OpenJournal opens the journal and takes its owner lock.
	OpenJournal func(dir string) (Journal, error)
	// LockDataRoot opens a data root with the runner's own checks and holds its lock until released. A runner
	// that is serving makes it fail with an error for which IsDataRootBusy is true.
	LockDataRoot func(dir string) (release func(), err error)
	// IsDataRootBusy tells a held lock from any other failure of LockDataRoot or Provision.Verify.
	IsDataRootBusy func(error) bool
	// Provision is doc 182's environment: tools, file system, mount table and the runner's root checks.
	Provision provision.Env
}

func (e Env) path(p string) string {
	if e.Prefix == "" {
		return p
	}
	return filepath.Join(e.Prefix, p)
}

// physicalUID and physicalGID map the logical root identity to the one the files really carry.
func (e Env) physicalUID(id uint32) uint32 {
	if id == 0 {
		return e.RootUID
	}
	return id
}
func (e Env) physicalGID(id uint32) uint32 {
	if id == 0 {
		return e.RootGID
	}
	return id
}

// refusal is a state host-setup will not change or proceed from. Nothing was modified.
type refusal struct{ msg string }

func (r *refusal) Error() string { return r.msg }

func refuse(format string, args ...any) error { return &refusal{fmt.Sprintf(format, args...)} }

type step struct {
	kind string // create, match, operator, note
	text string
	do   func(ctx context.Context) error
}

// run is the state of one invocation.
type run struct {
	env  Env
	opts Options
	out  io.Writer

	steps     []step
	conflicts []string
	planned   map[string]bool

	adopt  bool
	cfg    resolved
	enroll *enrollment

	// pending is set when the host would be complete except for the enrollment document.
	pending bool
	// fstabChanged is set by the fstab step and read by the reload step.
	fstabChanged bool

	journal     Journal
	releaseData []func()
	lockedData  []string

	// usableData and usableCheckpoint are the usable bytes of the two volume roots, measured before the
	// data-root lock is taken (the runner's own checks lock the root).
	usableData, usableCheckpoint int64
}

// Run executes host-setup and returns its exit status: 0, 1 for a refusal or failure, 3 for enrollment pending.
func Run(ctx context.Context, env Env, opts Options, out io.Writer) int {
	r := &run{env: env, opts: opts, out: out, planned: map[string]bool{}}
	defer r.release()
	code, err := r.execute(ctx)
	if err != nil {
		var ref *refusal
		if errors.As(err, &ref) {
			fmt.Fprintf(out, "refused: %s\n", ref.msg)
		} else {
			fmt.Fprintf(out, "failed: %v\n", err)
		}
		return ExitFailed
	}
	return code
}

func (r *run) release() {
	for i := len(r.releaseData) - 1; i >= 0; i-- {
		r.releaseData[i]()
	}
	r.releaseData = nil
	if r.journal != nil {
		r.journal.Close()
		r.journal = nil
	}
}

func (r *run) printf(format string, args ...any) { fmt.Fprintf(r.out, format, args...) }

func (r *run) add(kind, text string, do func(context.Context) error) {
	r.steps = append(r.steps, step{kind: kind, text: text, do: do})
}
func (r *run) match(format string, args ...any) { r.add("match", fmt.Sprintf(format, args...), nil) }
func (r *run) note(format string, args ...any)  { r.add("note", fmt.Sprintf(format, args...), nil) }
func (r *run) create(text string, do func(context.Context) error) {
	r.add("create", text, do)
}
func (r *run) conflict(format string, args ...any) {
	r.conflicts = append(r.conflicts, fmt.Sprintf(format, args...))
}

func (r *run) changes() int {
	n := 0
	for _, s := range r.steps {
		if s.kind == "create" {
			n++
		}
	}
	return n
}

func (r *run) execute(ctx context.Context) (int, error) {
	if err := r.preflight(ctx); err != nil {
		return 0, err
	}
	if err := r.plan(ctx); err != nil {
		return 0, err
	}
	r.printPlan()
	if len(r.conflicts) > 0 {
		for _, c := range r.conflicts {
			r.printf("conflict: %s\n", c)
		}
		return 0, refuse("%d conflict(s) with what host-setup owns; nothing was changed", len(r.conflicts))
	}
	if !r.opts.Apply {
		r.printf("plan only: nothing was changed. Run again with --apply to make these changes.\n")
		return ExitOK, nil
	}
	if err := r.lockExisting(ctx); err != nil {
		return 0, err
	}
	for _, s := range r.steps {
		if s.do == nil {
			continue
		}
		r.printf("applying: %s\n", s.text)
		if err := s.do(ctx); err != nil {
			return 0, fmt.Errorf("%s: %w (the steps before it are complete; fix the cause and run the same command again)", s.text, err)
		}
	}
	if r.changes() == 0 {
		r.printf("no changes: everything host-setup owns already matches.\n")
	}
	if r.pending {
		r.printf("enrollment pending: the host is prepared, but no service.json or bootstrap.json was written and no enrollment was made.\n")
		r.printf("Prepare the runtime binding in the Portal administrator console, save its enrollment document on this host and run host-setup --apply --enrollment <file>.\n")
		return ExitPending, nil
	}
	r.report()
	return ExitOK, nil
}

// kernelAtLeast reports whether a release string such as "6.8.0-45-generic" is at least major.minor.
func kernelAtLeast(release string, major, minor int) bool {
	var a, b int
	if n, _ := fmt.Sscanf(release, "%d.%d", &a, &b); n != 2 {
		return false
	}
	return a > major || (a == major && b >= minor)
}

func packageOf(tool string) string {
	switch tool {
	case "mkfs.xfs":
		return "xfsprogs"
	case "chattr":
		return "e2fsprogs"
	default:
		return "util-linux"
	}
}

// preflight only reads. It refuses unless the process is in the host's namespaces as root on a supported
// kernel with the host tools present.
func (r *run) preflight(ctx context.Context) error {
	if r.env.Provision.Geteuid == nil || r.env.Provision.Geteuid() != 0 {
		return refuse("host-setup must run as root")
	}
	if r.env.Namespaces == nil || r.env.Namespaces() != nil {
		return refuse("host-setup is not in the host's mount, network and PID namespaces; run it through the app-runner image (docker compose run --rm app-runner host-setup), or directly on the host")
	}
	release, err := r.env.Kernel()
	if err != nil || !kernelAtLeast(release, minKernelMajor, minKernelMinor) {
		return refuse("the kernel must be at least %d.%d (found %q): the data root needs quotactl_fd", minKernelMajor, minKernelMinor, release)
	}
	for _, tool := range []string{"losetup", "mkfs.xfs", "mount", "umount", "fallocate", "blkid", "findmnt"} {
		if !r.env.HasTool(tool) {
			return refuse("%s is not installed on the host (package %s); install it and run host-setup again", tool, packageOf(tool))
		}
	}
	return nil
}

func (r *run) printPlan() {
	mode := "new host"
	if r.adopt {
		mode = "adopting the existing layout of " + filepath.Join(r.opts.ConfigDir, "service.json")
	}
	r.printf("host-setup plan (%s):\n", mode)
	for _, s := range r.steps {
		label := map[string]string{"create": "create  ", "match": "matches ", "operator": "operator", "note": "note    "}[s.kind]
		r.printf("  %s %s\n", label, s.text)
	}
}

// lockExisting takes the journal and data-root locks that can be taken now, without waiting. They are held
// until host-setup exits, so a runner that starts meanwhile exits with "in use".
func (r *run) lockExisting(ctx context.Context) error {
	if r.cfg.journalDir != "" && isDir(r.env.path(r.cfg.journalDir)) {
		if err := r.ensureJournal(); err != nil {
			return err
		}
	}
	for _, root := range []string{r.cfg.dataRoot} {
		if root != "" && isDir(r.env.path(root)) {
			if err := r.lockData(root); err != nil {
				return err
			}
		}
	}
	return nil
}

const runnerRunning = "the runner is running: stop it first (docker compose stop app-runner, or systemctl stop portal-app-runner)"

func (r *run) ensureJournal() error {
	if r.journal != nil {
		return nil
	}
	handle, err := r.env.OpenJournal(r.cfg.journalDir)
	if errors.Is(err, journal.ErrInUse) {
		return refuse("%s", runnerRunning)
	}
	if err != nil {
		return fmt.Errorf("open the journal: %w", err)
	}
	r.journal = handle
	return nil
}

func (r *run) lockData(root string) error {
	if r.releaseDataFor(root) {
		return nil
	}
	release, err := r.env.LockDataRoot(root)
	if err != nil {
		if r.env.IsDataRootBusy != nil && r.env.IsDataRootBusy(err) {
			return refuse("%s", runnerRunning)
		}
		return refuse("%s does not pass the runner's data-root checks: %v", root, err)
	}
	r.releaseData = append(r.releaseData, release)
	r.lockedData = append(r.lockedData, root)
	return nil
}

func (r *run) releaseDataFor(root string) bool {
	for _, held := range r.lockedData {
		if held == root {
			return true
		}
	}
	return false
}

func isDir(path string) bool {
	info, err := os.Lstat(path)
	return err == nil && info.IsDir()
}

func under(path, parent string) bool {
	return path == parent || strings.HasPrefix(path, strings.TrimSuffix(parent, "/")+"/")
}
