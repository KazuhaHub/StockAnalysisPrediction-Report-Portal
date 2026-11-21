package hostsetup

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/journal"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/provision"
)

func mountOf(point, source string) provision.Mount {
	return provision.Mount{Point: point, FSType: "xfs", Source: source,
		Options: map[string]bool{"prjquota": true, "nodev": true, "nosuid": true, "noexec": true}}
}

const (
	exampleData       = "/data/portal-apps-data.img"
	exampleCheckpoint = "/data/portal-apps-checkpoint.img"
)

// adopted is a host that runs app-runner under systemd with the layout of deploy/app-runner/service.example.json
// (doc 146): a group portal-app-runner, an enrolled journal, two loop-mounted XFS volumes with their fstab lines.
// The example is read from the repository, so a change to it that adoption cannot take fails here.
func adopted(t *testing.T) (*host, identity, map[string]any) {
	t.Helper()
	h := newHost(t)
	id := enrollmentDoc(1)
	raw, err := os.ReadFile("../../../deploy/app-runner/service.example.json")
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	cfg["controller_gid"] = h.gid
	ctrl, _ := json.Marshal(id.ctrl)
	var block map[string]any
	json.Unmarshal(ctrl, &block)
	cfg["controller"] = block
	h.writeConfig(cfg)

	str := func(key string) string { return cfg[key].(string) }
	h.mkdir("/etc/portal-app-runner", 0o700)
	h.mkdir(str("journal_dir"), 0o700)
	h.mkdir(str("image_root"), 0o700)
	// doc 146: every directory of the control path is root:gid 0750.
	for dir := str("control_root"); dir != "/run" && dir != "/var/lib" && dir != "/"; dir = filepath.Dir(dir) {
		h.mkdir(dir, 0o750)
	}
	for _, root := range []string{str("data_root"), str("checkpoint_root")} {
		h.mkdir(root, 0o700)
	}
	h.write(exampleData, "", 0o600)
	h.write(exampleCheckpoint, "", 0o600)
	h.mounts = []provision.Mount{mountOf(str("data_root"), "/dev/loop3"), mountOf(str("checkpoint_root"), "/dev/loop4")}
	h.loops["/dev/loop3"], h.loops["/dev/loop4"] = exampleData, exampleCheckpoint
	h.write("/etc/fstab", "/dev/sda1 / ext4 defaults 0 1\n"+
		exampleData+" "+str("data_root")+" xfs loop,prjquota,nodev,nosuid,noexec,nofail 0 0\n"+
		exampleCheckpoint+" "+str("checkpoint_root")+" xfs loop,prjquota,nodev,nosuid,noexec,nofail 0 0\n", 0o644)
	h.groups = []Account{{Name: GroupName, ID: h.gid}}
	h.deployment = id.deployment()
	return h, id, cfg
}

func (h *host) writeConfig(cfg map[string]any) {
	raw, _ := json.MarshalIndent(cfg, "", "  ")
	h.write("/etc/portal-app-runner/service.json", string(raw), 0o600)
}

func (h *host) adoptOptions(apply bool) Options { return Options{Apply: apply, Release: "v2026.47.1"} }

// Section 11, adopt: on the shipped example layout the plan reports every item as matching, and --apply without
// --enrollment exits 0 and changes nothing.
func TestAdoptTheShippedExampleLayout(t *testing.T) {
	h, id, _ := adopted(t)
	before := h.snapshot()
	code, out := h.run(h.adoptOptions(false))
	if code != ExitOK {
		t.Fatalf("plan exit %d:\n%s", code, out)
	}
	if strings.Contains(out, "  create ") {
		t.Fatalf("the plan wants to create something on an adopted host:\n%s", out)
	}
	for _, want := range []string{"adopting the existing layout", "the journal is enrolled with the controller of service.json", "fstab line", "loop-mounted XFS image", "operator"} {
		if !strings.Contains(out, want) {
			t.Errorf("the plan does not say %q:\n%s", want, out)
		}
	}
	if h.snapshot() != before || len(h.mutating()) != 0 {
		t.Fatal("the plan changed the host")
	}
	code, out = h.run(h.adoptOptions(true))
	if code != ExitOK || !strings.Contains(out, "no changes") {
		t.Fatalf("apply exit %d:\n%s", code, out)
	}
	if h.snapshot() != before || len(h.mutating()) != 0 {
		t.Fatalf("an apply on a matching host changed it: %v", h.mutating())
	}
	// A matching enrollment document is recognised too.
	opts := h.adoptOptions(true)
	opts.Enrollment = h.enrollmentFile(id)
	if code, out := h.run(opts); code != ExitOK {
		t.Fatalf("with the matching enrollment %d:\n%s", code, out)
	}
	if h.deployment == nil || h.dataLocks == 0 || h.dataLocks != h.dataReleased {
		t.Error("the locks were not taken and released")
	}
}

// Operator edits to the fields host-setup does not own never matter: after raising capacity.disk_bytes and
// adding doc 186's pinned destinations the next run still reports no changes.
func TestOperatorEditsAreNeverCompared(t *testing.T) {
	h, _, cfg := adopted(t)
	cfg["capacity"].(map[string]any)["disk_bytes"] = float64(64 << 30)
	cfg["proxy_pins"] = []any{map[string]any{"host": "quotes.internal.anchan.kazuhahub.com", "address": "127.0.0.1", "port": 20443}}
	cfg["private_networks"] = []any{}
	cfg["network_active"] = 8
	cfg["allow_local_reference_images"] = false
	h.writeConfig(cfg)
	before := h.snapshot()
	code, out := h.run(h.adoptOptions(true))
	if code != ExitOK || !strings.Contains(out, "no changes") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if h.snapshot() != before {
		t.Fatal("host-setup rewrote an operator-owned field or file")
	}
}

// A path or ID flag that disagrees with the parsed existing value refuses; one that agrees, or none, adopts.
func TestAdoptRefusesADisagreeingFlag(t *testing.T) {
	h, _, cfg := adopted(t)
	before := h.snapshot()
	for name, mutate := range map[string]func(o *Options){
		"journal":    func(o *Options) { o.JournalDir = "/elsewhere/journal" },
		"data root":  func(o *Options) { o.DataRoot = "/srv/other" },
		"socket":     func(o *Options) { o.DockerSocket = "/var/run/other.sock" },
		"control":    func(o *Options) { o.ControlRoot = "/var/lib/other" },
		"controller": func(o *Options) { o.ControllerUID = 41002 },
		"group":      func(o *Options) { o.ControllerGID = h.gid + 5 },
	} {
		opts := h.adoptOptions(true)
		mutate(&opts)
		code, out := h.run(opts)
		if code != ExitFailed || !strings.Contains(out, "disagrees") {
			t.Errorf("%s: exit %d:\n%s", name, code, out)
		}
		if h.snapshot() != before {
			t.Errorf("%s: a refused run changed the host", name)
		}
	}
	opts := h.adoptOptions(true)
	opts.JournalDir = cfg["journal_dir"].(string)
	opts.ControllerUID = 41001
	opts.ControllerGID = h.gid
	if code, out := h.run(opts); code != ExitOK {
		t.Fatalf("flags that agree: exit %d:\n%s", code, out)
	}
}

// An --enrollment with another identity than the enrolled one conflicts, as bootstrap does.
func TestAdoptEnrollmentOfAnotherIdentityConflicts(t *testing.T) {
	h, _, _ := adopted(t)
	opts := h.adoptOptions(true)
	opts.Enrollment = h.enrollmentFile(enrollmentDoc(2))
	before := h.snapshot()
	code, out := h.run(opts)
	if code != ExitFailed || !strings.Contains(out, "different controller") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if h.snapshot() != before {
		t.Fatal("a refused run changed the host")
	}
	// service.json and the journal disagreeing is a conflict too.
	h, _, _ = adopted(t)
	h.deployment = enrollmentDoc(3).deployment()
	if code, out := h.run(h.adoptOptions(false)); code != ExitFailed || !strings.Contains(out, "different identity") {
		t.Fatalf("journal and service.json disagree: exit %d:\n%s", code, out)
	}
}

// A configured root that is not mounted refuses: host-setup does not guess which image belongs to it.
func TestAdoptRefusesAnUnmountedRoot(t *testing.T) {
	h, _, cfg := adopted(t)
	os.RemoveAll(h.p(cfg["data_root"].(string)))
	before := h.snapshot()
	code, out := h.run(h.adoptOptions(true))
	if code != ExitFailed || !strings.Contains(out, "not mounted") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if h.snapshot() != before {
		t.Fatal("a refused run changed the host")
	}
	// A directory that exists but fails the runner's checks is refused the same way.
	h, _, cfg = adopted(t)
	h.verifyErr[cfg["checkpoint_root"].(string)] = os.ErrInvalid
	if code, out := h.run(h.adoptOptions(true)); code != ExitFailed || !strings.Contains(out, "not mounted, or fails") {
		t.Fatalf("a root that fails the checks: exit %d:\n%s", code, out)
	}
}

// With the runner running, --apply refuses with the "runner is running" message before any change, and the
// plan still works. A runner that starts during --apply is excluded by the locks host-setup holds.
func TestAdoptWithARunningRunner(t *testing.T) {
	h, _, cfg := adopted(t)
	// A change is planned, so a refusal after it would be visible: the control root is missing.
	os.RemoveAll(h.p(cfg["control_root"].(string)))
	h.dataBusy = true
	h.journalErr = journal.ErrInUse
	before := h.snapshot()
	code, out := h.run(h.adoptOptions(false))
	if code != ExitOK || !strings.Contains(out, "held by a running runner") {
		t.Fatalf("the plan while the runner serves: exit %d:\n%s", code, out)
	}
	code, out = h.run(h.adoptOptions(true))
	if code != ExitFailed || !strings.Contains(out, "the runner is running: stop it first (docker compose stop app-runner, or systemctl stop portal-app-runner)") {
		t.Fatalf("apply while the runner serves: exit %d:\n%s", code, out)
	}
	if h.snapshot() != before || len(h.mutating()) != 0 {
		t.Fatal("a refused apply changed the host")
	}
	// Only the data-root lock held: the same message.
	h.journalErr = nil
	if code, out := h.run(h.adoptOptions(true)); code != ExitFailed || !strings.Contains(out, "the runner is running") {
		t.Fatalf("data root busy: exit %d:\n%s", code, out)
	}
	if h.dataLocks != h.dataReleased {
		t.Fatal("a lock was leaked")
	}
}

// The systemd migration: the control root moved to a durable path is the one thing missing, and --apply
// creates it as root:gid 0750 and nothing else. No --enrollment is needed because the enrollment is recognised.
func TestAdoptMigrationCreatesOnlyTheNewControlRoot(t *testing.T) {
	h, _, cfg := adopted(t)
	// The example already names the durable control path; the host has not been given the directory yet.
	if cfg["control_root"] != "/var/lib/portal-app-runner/control" || cfg["control_socket"] != "/var/lib/portal-app-runner/control/runner.sock" {
		t.Fatalf("the shipped example does not name the durable control path: %v %v", cfg["control_root"], cfg["control_socket"])
	}
	os.RemoveAll(h.p("/var/lib/portal-app-runner/control"))
	code, plan := h.run(h.adoptOptions(false))
	if code != ExitOK || strings.Count(plan, "  create ") != 1 || !strings.Contains(plan, "control root /var/lib/portal-app-runner/control") {
		t.Fatalf("plan exit %d, want exactly the control root:\n%s", code, plan)
	}
	before := h.snapshot()
	code, out := h.run(h.adoptOptions(true))
	if code != ExitOK {
		t.Fatalf("apply exit %d:\n%s", code, out)
	}
	if mode, uid, gid := h.stat("/var/lib/portal-app-runner/control"); mode != 0o750 || uid != h.env.RootUID || gid != h.gid {
		t.Fatalf("control root is %04o %d:%d", mode, uid, gid)
	}
	if h.snapshot() == before || len(h.mutating()) != 0 {
		t.Fatalf("expected one new directory and no command: %v", h.mutating())
	}
	for _, line := range []string{"RP_APP_RUNNER_CONTROL_DIR=/var/lib/portal-app-runner/control", "RP_APP_RUNNER_CONTROLLER_UID=41001"} {
		if !strings.Contains(out, "\n"+line+"\n") {
			t.Errorf("the report lacks %q:\n%s", line, out)
		}
	}
	// Run again: nothing left to do.
	if code, out := h.run(h.adoptOptions(true)); code != ExitOK || !strings.Contains(out, "no changes") {
		t.Fatalf("second apply %d:\n%s", code, out)
	}
}

// The rollback leaves the layout under /run: it is reported, not refused, and the plan says what to edit.
func TestAdoptReportsAControlRootUnderRun(t *testing.T) {
	h, _, cfg := adopted(t)
	cfg["control_root"] = "/run/portal-app-runner/control"
	cfg["control_socket"] = "/run/portal-app-runner/control/runner.sock"
	h.writeConfig(cfg)
	h.mkdir("/run/portal-app-runner", 0o750)
	h.mkdir("/run/portal-app-runner/control", 0o750)
	code, out := h.run(h.adoptOptions(false))
	if code != ExitOK || !strings.Contains(out, "is under /run") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
}

// IDs from an existing service.json are accepted whatever the host database says (section 8.4): a group of
// another name that holds the GID never blocks migration or rollback.
func TestAdoptAcceptsConfiguredIDsWhateverTheHostDatabaseSays(t *testing.T) {
	h, _, _ := adopted(t)
	h.groups = []Account{{Name: "someone-else", ID: h.gid}}
	h.users = []Account{{Name: "someone", ID: 41001}}
	if code, out := h.run(h.adoptOptions(true)); code != ExitOK {
		t.Fatalf("exit %d:\n%s", code, out)
	}
}

// Any directory host-setup does not own the shape of is a conflict, never repaired: a control root of another
// mode, owner or a journal directory of mode 0755.
func TestAdoptDirectoryConflicts(t *testing.T) {
	for name, mutate := range map[string]func(h *host, cfg map[string]any){
		"journal 0755":    func(h *host, cfg map[string]any) { h.mkdir(cfg["journal_dir"].(string), 0o755) },
		"control 0770":    func(h *host, cfg map[string]any) { h.mkdir(cfg["control_root"].(string), 0o770) },
		"image root 0750": func(h *host, cfg map[string]any) { h.mkdir(cfg["image_root"].(string), 0o750) },
		"config dir 0777": func(h *host, cfg map[string]any) { h.mkdir("/etc/portal-app-runner", 0o777) },
		"an fstab conflict": func(h *host, cfg map[string]any) {
			h.write("/etc/fstab", exampleData+" "+cfg["data_root"].(string)+" xfs defaults 0 0\n", 0o644)
		},
		"an image that is not 0600": func(h *host, cfg map[string]any) { os.Chmod(h.p(exampleData), 0o644) },
	} {
		t.Run(name, func(t *testing.T) {
			h, _, cfg := adopted(t)
			mutate(h, cfg)
			before := h.snapshot()
			code, out := h.run(h.adoptOptions(true))
			if code != ExitFailed || !strings.Contains(out, "conflict") {
				t.Fatalf("exit %d:\n%s", code, out)
			}
			if h.snapshot() != before {
				t.Fatal("a refused run changed the host")
			}
		})
	}
}

// An adopted host whose journal is not enrolled needs the enrollment: without it the status is 3, with it the
// journal is enrolled and the status is 0.
func TestAdoptNeedsEnrollmentWhenTheJournalIsEmpty(t *testing.T) {
	h, id, _ := adopted(t)
	h.deployment = nil
	if code, out := h.run(h.adoptOptions(true)); code != ExitPending || !strings.Contains(out, "enrollment pending") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	opts := h.adoptOptions(true)
	opts.Enrollment = h.enrollmentFile(id)
	if code, out := h.run(opts); code != ExitOK {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if h.deployment == nil || h.deployment.DeploymentID != id.ctrl.DeploymentID {
		t.Fatal("the journal was not enrolled")
	}
}

// A service.json that is not a valid private configuration is not adopted.
func TestAdoptRefusesAnInvalidConfiguration(t *testing.T) {
	h, _, _ := adopted(t)
	h.write("/etc/portal-app-runner/service.json", `{"format":"nope"}`, 0o600)
	if code, out := h.run(h.adoptOptions(false)); code != ExitFailed || !strings.Contains(out, "not a valid runner configuration") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	h.write("/etc/portal-app-runner/service.json", `{}`, 0o644)
	if code, _ := h.run(h.adoptOptions(false)); code != ExitFailed {
		t.Fatalf("a world-readable configuration was adopted")
	}
}

// Missing fstab lines of an adopted loop-mounted volume are added, and only them.
func TestAdoptAddsAMissingFstabLine(t *testing.T) {
	h, _, cfg := adopted(t)
	h.write("/etc/fstab", "/dev/sda1 / ext4 defaults 0 1\n"+
		exampleData+" "+cfg["data_root"].(string)+" xfs loop,prjquota,nodev,nosuid,noexec,nofail 0 0\n", 0o644)
	code, out := h.run(h.adoptOptions(true))
	if code != ExitOK {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	fstab := h.read("/etc/fstab")
	if strings.Count(fstab, exampleCheckpoint) != 1 || strings.Count(fstab, exampleData) != 1 {
		t.Fatalf("fstab:\n%s", fstab)
	}
	if h.ran("systemctl") != 1 || h.ran("findmnt") != 1 {
		t.Fatalf("expected one verify and one reload: %v", h.mutating())
	}
	if len(h.mounts) != 2 || h.ran("mount") != 0 {
		t.Fatal("adoption mounted something")
	}
}

// A volume the operator mounted from a partition is accepted as it is, with no fstab change.
func TestAdoptAcceptsAnOperatorMountedPartition(t *testing.T) {
	h, _, cfg := adopted(t)
	h.mounts = []provision.Mount{mountOf(cfg["data_root"].(string), "/dev/nvme1n1p1"), mountOf(cfg["checkpoint_root"].(string), "/dev/vg0/ckpt")}
	h.write("/etc/fstab", "/dev/sda1 / ext4 defaults 0 1\n", 0o644)
	before := h.snapshot()
	code, out := h.run(h.adoptOptions(true))
	if code != ExitOK || !strings.Contains(out, "accepted as it is") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if h.snapshot() != before {
		t.Fatal("a partition-backed volume caused a change")
	}
}

// daemon-reload follows a fstab change that was made, and only that: lines added by someone else between the plan
// and the apply leave nothing to write and nothing to reload.
func TestNoReloadWhenTheFstabNeedsNothingAtApply(t *testing.T) {
	h, _, cfg := adopted(t)
	complete := "/dev/sda1 / ext4 defaults 0 1\n" +
		exampleData + " " + cfg["data_root"].(string) + " xfs loop,prjquota,nodev,nosuid,noexec,nofail 0 0\n" +
		exampleCheckpoint + " " + cfg["checkpoint_root"].(string) + " xfs loop,prjquota,nodev,nosuid,noexec,nofail 0 0\n"
	h.write("/etc/fstab", "/dev/sda1 / ext4 defaults 0 1\n", 0o644)
	lock := h.env.LockDataRoot
	h.env.LockDataRoot = func(dir string) (func(), error) {
		h.write("/etc/fstab", complete, 0o644) // the operator fixed it while host-setup was starting
		return lock(dir)
	}
	code, out := h.run(h.adoptOptions(true))
	if code != ExitOK {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if h.ran("systemctl") != 0 || h.ran("findmnt") != 0 || h.exists("/etc/fstab.before-portal-app-runner") {
		t.Fatalf("a fstab that needed nothing was rewritten or reloaded: %v", h.mutating())
	}
}

// An adopted loop image whose path would need fstab escaping is refused when its line is missing, never written
// with a guessed escape.
func TestAdoptRefusesAnImagePathThatNeedsFstabEscaping(t *testing.T) {
	h, _, _ := adopted(t)
	spaced := "/data/portal apps data.img"
	h.write(spaced, "", 0o600)
	h.loops["/dev/loop3"] = spaced
	h.write("/etc/fstab", "/dev/sda1 / ext4 defaults 0 1\n", 0o644)
	before := h.snapshot()
	code, out := h.run(h.adoptOptions(true))
	if code != ExitFailed || !strings.Contains(out, "fstab escaping") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if h.snapshot() != before {
		t.Fatal("a refused run changed the host")
	}
}
