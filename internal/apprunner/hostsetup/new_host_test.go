package hostsetup

import (
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/broker"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/service"
)

const (
	dataImage       = "/var/lib/portal-app-runner/volumes/data.img"
	checkpointImage = "/var/lib/portal-app-runner/volumes/checkpoint.img"
)

func mustLoad(t *testing.T, h *host, path string) service.Config {
	t.Helper()
	cfg, err := service.LoadConfig(h.p(path))
	if err != nil {
		t.Fatalf("%s does not load: %v", path, err)
	}
	return cfg
}

// Doc 187 section 11, new host: the plan changes nothing. The host state is hashed before and after, and no
// command that changes a host is issued.
func TestPlanOnlyChangesNothing(t *testing.T) {
	h := newHost(t)
	h.enrollmentFile(enrollmentDoc(1))
	before := h.snapshot()
	opts := h.options(false)
	opts.Enrollment = "/root/enrollment.json"
	code, out := h.run(opts)
	if code != ExitOK {
		t.Fatalf("plan exit %d:\n%s", code, out)
	}
	if h.snapshot() != before {
		t.Fatal("the plan changed the host")
	}
	if cmds := h.mutating(); len(cmds) != 0 {
		t.Fatalf("the plan ran %v", cmds)
	}
	for _, want := range []string{"plan only", "create", "journal directory /var/lib/portal-app-runner/journal", "write /etc/portal-app-runner/service.json", "fstab: add"} {
		if !strings.Contains(out, want) {
			t.Errorf("the plan does not say %q:\n%s", want, out)
		}
	}
	if h.ran("mount") != 0 || h.deployment != nil {
		t.Fatal("the plan mounted or enrolled")
	}
}

// Section 7: the first --apply creates every item of the table, and section 8.3 step 8 prints the .env lines.
func TestFirstApplyCreatesEveryItem(t *testing.T) {
	h := newHost(t)
	id := enrollmentDoc(1)
	opts := h.options(true)
	opts.Enrollment = h.enrollmentFile(id)
	code, out := h.run(opts)
	if code != ExitOK {
		t.Fatalf("apply exit %d:\n%s", code, out)
	}
	root := h.env.RootUID
	for _, d := range []struct {
		path string
		mode os.FileMode
		gid  uint32
	}{
		{"/etc/portal-app-runner", 0o700, h.gid}, {"/var/lib/portal-app-runner", 0o755, h.gid},
		{"/var/lib/portal-app-runner/journal", 0o700, h.gid}, {"/var/lib/portal-app-runner/images", 0o700, h.gid},
		{"/var/lib/portal-app-runner/control", 0o750, h.gid}, {"/var/lib/portal-app-runner/volumes", 0o700, h.gid},
		{"/srv/portal-runner", 0o755, h.gid}, {"/srv/portal-runner/data", 0o700, h.gid}, {"/srv/portal-runner/checkpoint", 0o700, h.gid},
		{"/srv/portal-runner/data/root", 0o700, h.gid}, {"/srv/portal-runner/checkpoint/root", 0o700, h.gid},
	} {
		mode, uid, gid := h.stat(d.path)
		if mode != d.mode || uid != root || gid != d.gid {
			t.Errorf("%s is %04o %d:%d, want %04o", d.path, mode, uid, gid, d.mode)
		}
	}
	for _, f := range []string{"/etc/portal-app-runner/service.json", "/etc/portal-app-runner/bootstrap.json", dataImage, checkpointImage} {
		if mode, uid, _ := h.stat(f); mode != 0o600 || uid != root {
			t.Errorf("%s is %04o owned by %d, want 0600 root", f, mode, uid)
		}
	}
	if len(h.mounts) != 2 {
		t.Fatalf("mounts: %+v", h.mounts)
	}
	fstab := h.read("/etc/fstab")
	for _, want := range []string{dataImage + " /srv/portal-runner/data xfs loop,prjquota,nodev,nosuid,noexec,nofail,X-fstrim.notrim 0 0",
		checkpointImage + " /srv/portal-runner/checkpoint xfs loop,prjquota,nodev,nosuid,noexec,nofail,X-fstrim.notrim 0 0"} {
		if !strings.Contains(fstab, want) {
			t.Errorf("fstab lacks %q:\n%s", want, fstab)
		}
	}
	if h.read("/etc/fstab.before-portal-app-runner") != "# /etc/fstab\n/dev/sda1 / ext4 defaults 0 1\n" {
		t.Error("the first-time backup of fstab is not the original")
	}
	if h.ran("systemctl") != 1 {
		t.Errorf("daemon-reload ran %d times", h.ran("systemctl"))
	}
	cfg := mustLoad(t, h, "/etc/portal-app-runner/service.json")
	if cfg.ControllerUID != fixtureUID || cfg.ControllerGID != h.gid || cfg.Controller != id.ctrl || cfg.DataRoot != "/srv/portal-runner/data/root" ||
		cfg.CheckpointRoot != "/srv/portal-runner/checkpoint/root" || cfg.ControlSocket != "/var/lib/portal-app-runner/control/runner.sock" ||
		cfg.IPCRoot != "/run/portal-app-runner/ipc" || cfg.JournalDir != "/var/lib/portal-app-runner/journal" || cfg.Capacity == nil || cfg.Capacity.DiskBytes == nil || *cfg.Capacity.DiskBytes < 1<<30 {
		t.Fatalf("service.json: %+v", cfg)
	}
	// The broker's pools refuse more than their maximum at start, although LoadConfig allows more.
	if cfg.NetworkActive > broker.MaxNetworkPoolActive || cfg.NetworkEntries > broker.MaxNetworkPoolEntries {
		t.Fatalf("network_active %d / network_entries %d exceed what the broker's pools accept", cfg.NetworkActive, cfg.NetworkEntries)
	}
	if _, err := service.LoadBootstrapConfig(h.p("/etc/portal-app-runner/bootstrap.json")); err != nil {
		t.Fatalf("bootstrap.json: %v", err)
	}
	if h.deployment == nil || h.deployment.DeploymentID != id.ctrl.DeploymentID {
		t.Fatal("the journal was not enrolled")
	}
	for _, line := range []string{"COMPOSE_PROFILES=app-runner", "PORTAL_VERSION=v2026.47.1", "PORTAL_UID=41001", "PORTAL_GID=" + itoa(h.gid),
		"RP_APP_RUNNER_CONTROL_DIR=/var/lib/portal-app-runner/control", "RP_APP_RUNNER_SOCKET=/var/lib/portal-app-runner/control/runner.sock",
		"RP_APP_RUNNER_CONTROLLER_UID=41001", "RP_APP_RUNNER_CONTROLLER_GID=" + itoa(h.gid)} {
		if !strings.Contains(out, "\n"+line+"\n") {
			t.Errorf("the .env report lacks %q:\n%s", line, out)
		}
	}
	if h.dataLocks == 0 || h.dataLocks != h.dataReleased {
		t.Errorf("data-root locks held %d, released %d", h.dataLocks, h.dataReleased)
	}
}

func itoa(n uint32) string { return strconv.FormatUint(uint64(n), 10) }

// A second --apply reports no changes, exits 0 and issues no command that changes a host.
func TestSecondApplyIsANoOp(t *testing.T) {
	h := newHost(t)
	opts := h.options(true)
	opts.Enrollment = h.enrollmentFile(enrollmentDoc(1))
	if code, out := h.run(opts); code != ExitOK {
		t.Fatalf("first apply %d:\n%s", code, out)
	}
	before := h.snapshot()
	h.exec = nil
	code, out := h.run(opts)
	if code != ExitOK {
		t.Fatalf("second apply %d:\n%s", code, out)
	}
	if !strings.Contains(out, "no changes") {
		t.Errorf("the second run does not say so:\n%s", out)
	}
	if h.snapshot() != before || len(h.mutating()) != 0 {
		t.Fatalf("the second apply changed the host: %v", h.mutating())
	}
	// The same without the enrollment: the host is already enrolled and adopted.
	opts.Enrollment = ""
	if code, out := h.run(opts); code != ExitOK || !strings.Contains(out, "no changes") {
		t.Fatalf("third apply %d:\n%s", code, out)
	}
}

// Each conflict refuses, names the item and changes nothing.
func TestNewHostConflictsRefuseAndChangeNothing(t *testing.T) {
	cases := map[string]struct {
		setup func(h *host)
		want  string
	}{
		"a 0755 journal directory": {func(h *host) { h.mkdir("/var/lib/portal-app-runner/journal", 0o755) }, "journal directory"},
		"an fstab line with other options": {func(h *host) {
			h.write("/etc/fstab", "/dev/sdb1 /srv/portal-runner/data xfs defaults 0 0\n", 0o644)
		}, "fstab line"},
		"an image of another size": {func(h *host) {
			h.mkdir("/var/lib/portal-app-runner/volumes", 0o700)
			h.write(dataImage, "", 0o600)
			os.Truncate(h.p(dataImage), 3<<30)
		}, "size"},
		"a UID held by another host account": {func(h *host) { h.users = []Account{{Name: "mallory", ID: fixtureUID}} }, "mallory"},
		"a GID held by another host group":   {func(h *host) { h.groups = []Account{{Name: "docker", ID: 0}} }, ""},
		"a mount point that is busy": {func(h *host) {
			h.mkdir("/srv/portal-runner", 0o755)
			h.mkdir("/srv/portal-runner/data", 0o700)
			h.mounts = append(h.mounts, mountOf("/srv/portal-runner/data", "/dev/sdc1"))
		}, "already a mount point"},
		"a group-writable parent": {func(h *host) { h.mkdir("/srv", 0o775) }, "/srv"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			h := newHost(t)
			c.setup(h)
			if name == "a GID held by another host group" {
				h.groups = []Account{{Name: "docker", ID: h.gid}}
				c.want = "docker"
			}
			opts := h.options(true)
			opts.Enrollment = h.enrollmentFile(enrollmentDoc(1))
			before := h.snapshot()
			code, out := h.run(opts)
			if code != ExitFailed || !strings.Contains(out, "refused") || !strings.Contains(out, c.want) {
				t.Fatalf("exit %d, want refusal naming %q:\n%s", code, c.want, out)
			}
			if h.snapshot() != before || len(h.mutating()) != 0 {
				t.Fatalf("a refused run changed the host: %v", h.mutating())
			}
		})
	}
}

// A group named portal-app-runner that holds the GID is doc 146's own and is accepted.
func TestMatchingControllerGroupIsAccepted(t *testing.T) {
	h := newHost(t)
	h.groups = []Account{{Name: GroupName, ID: h.gid}}
	opts := h.options(true)
	opts.Enrollment = h.enrollmentFile(enrollmentDoc(1))
	if code, out := h.run(opts); code != ExitOK {
		t.Fatalf("exit %d:\n%s", code, out)
	}
}

// Below 16 GiB the checkpoint volume refuses unless the override is given (doc 185 Q8).
func TestSmallCheckpointRefusesWithoutTheOverride(t *testing.T) {
	h := newHost(t)
	opts := h.options(true)
	opts.BelowProduction = false
	opts.Enrollment = h.enrollmentFile(enrollmentDoc(1))
	before := h.snapshot()
	code, out := h.run(opts)
	if code != ExitFailed || !strings.Contains(out, "16 GiB") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if h.snapshot() != before {
		t.Fatal("a refused run changed the host")
	}
	opts.BelowProduction = true
	if code, out := h.run(opts); code != ExitOK {
		t.Fatalf("with the override: exit %d:\n%s", code, out)
	}
}

// A missing host tool refuses in preflight, before any change, and names the package.
func TestMissingHostToolRefusesBeforeAnyChange(t *testing.T) {
	for tool, pkg := range map[string]string{"mkfs.xfs": "xfsprogs", "losetup": "util-linux", "findmnt": "util-linux", "blkid": "util-linux"} {
		t.Run(tool, func(t *testing.T) {
			h := newHost(t)
			h.tools[tool] = false
			before := h.snapshot()
			code, out := h.run(h.options(true))
			if code != ExitFailed || !strings.Contains(out, tool) || !strings.Contains(out, pkg) {
				t.Fatalf("exit %d:\n%s", code, out)
			}
			if h.snapshot() != before || len(h.exec) != 0 {
				t.Fatal("preflight changed or probed the host")
			}
		})
	}
}

// chattr is required only where the images land on btrfs.
func TestChattrIsRequiredOnlyOnBtrfs(t *testing.T) {
	h := newHost(t)
	h.tools["chattr"] = false
	opts := h.options(false)
	if code, out := h.run(opts); code != ExitOK {
		t.Fatalf("ext4 without chattr: %d\n%s", code, out)
	}
	h.fsKinds["/var/lib"] = "btrfs"
	if code, out := h.run(opts); code != ExitFailed || !strings.Contains(out, "e2fsprogs") {
		t.Fatalf("btrfs without chattr: %d\n%s", code, out)
	}
}

// Preflight: the host namespaces, root, the kernel and Docker.
func TestPreflightRefusals(t *testing.T) {
	h := newHost(t)
	h.env.Namespaces = func() error { return os.ErrPermission }
	if code, out := h.run(h.options(false)); code != ExitFailed || !strings.Contains(out, "namespaces") {
		t.Fatalf("another namespace: %d\n%s", code, out)
	}
	h = newHost(t)
	h.env.Kernel = func() (string, error) { return "5.10.0", nil }
	if code, out := h.run(h.options(false)); code != ExitFailed || !strings.Contains(out, "5.14") {
		t.Fatalf("old kernel: %d\n%s", code, out)
	}
	h = newHost(t)
	h.env.Provision.Geteuid = func() int { return 1000 }
	if code, out := h.run(h.options(false)); code != ExitFailed || !strings.Contains(out, "root") {
		t.Fatalf("not root: %d\n%s", code, out)
	}
	h = newHost(t)
	h.dockerErr = os.ErrDeadlineExceeded
	if code, out := h.run(h.options(false)); code != ExitFailed || !strings.Contains(out, "Docker") {
		t.Fatalf("docker: %d\n%s", code, out)
	}
}

// Without --enrollment the host is prepared, no configuration is written and the status is 3.
func TestNoEnrollmentExitsThreeAndWritesNoConfig(t *testing.T) {
	h := newHost(t)
	code, out := h.run(h.options(true))
	if code != ExitPending || !strings.Contains(out, "enrollment pending") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if h.exists("/etc/portal-app-runner/service.json") || h.exists("/etc/portal-app-runner/bootstrap.json") || h.deployment != nil {
		t.Fatal("a configuration or enrollment was written without an enrollment document")
	}
	if len(h.mounts) != 2 || !h.exists("/var/lib/portal-app-runner/journal") {
		t.Fatal("the rest of the host was not prepared")
	}
	// The plan alone is not pending-failure: it exits 0.
	if code, _ := h.run(h.options(false)); code != ExitOK {
		t.Fatalf("plan exit %d", code)
	}
	// Providing the document later completes the host from where it stopped.
	opts := h.options(true)
	opts.Enrollment = h.enrollmentFile(enrollmentDoc(1))
	if code, out := h.run(opts); code != ExitOK {
		t.Fatalf("second run %d:\n%s", code, out)
	}
	mustLoad(t, h, "/etc/portal-app-runner/service.json")
}

// A failed `findmnt --verify` leaves /etc/fstab untouched and no reload happens.
func TestFailedFindmntVerifyLeavesFstabUntouched(t *testing.T) {
	h := newHost(t)
	h.findmntErr = os.ErrInvalid
	h.findmntOut = "1 parse error, 0 errors, 0 warnings\n"
	original := h.read("/etc/fstab")
	opts := h.options(true)
	opts.Enrollment = h.enrollmentFile(enrollmentDoc(1))
	code, out := h.run(opts)
	if code != ExitFailed || !strings.Contains(out, "findmnt") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if h.read("/etc/fstab") != original || h.exists("/etc/fstab.before-portal-app-runner") || h.exists("/etc/fstab.portal-app-runner.new") {
		t.Fatal("fstab, its backup or the temporary file changed")
	}
	if h.ran("systemctl") != 0 {
		t.Fatal("daemon-reload ran after a failed fstab change")
	}
	// Warnings alone do not fail the check.
	h.findmntOut = "0 parse errors, 0 errors, 2 warnings\n"
	if code, out := h.run(opts); code != ExitOK {
		t.Fatalf("warnings only: exit %d:\n%s", code, out)
	}
}

// Without systemd's runtime directory there is no reload to run.
func TestNoReloadWithoutSystemd(t *testing.T) {
	h := newHost(t)
	os.RemoveAll(h.p("/run/systemd"))
	opts := h.options(true)
	opts.Enrollment = h.enrollmentFile(enrollmentDoc(1))
	if code, out := h.run(opts); code != ExitOK {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if h.ran("systemctl") != 0 {
		t.Fatal("daemon-reload ran on a host without systemd")
	}
}

// An enrollment document that is not one is refused before anything changes.
func TestInvalidEnrollmentDocumentRefuses(t *testing.T) {
	h := newHost(t)
	h.write("/root/bad.json", `{"format":"portal.app-runner.bootstrap/1-dev.1"}`, 0o600)
	opts := h.options(true)
	opts.Enrollment = "/root/bad.json"
	before := h.snapshot()
	if code, out := h.run(opts); code != ExitFailed || !strings.Contains(out, "enrollment") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if h.snapshot() != before {
		t.Fatal("a refused run changed the host")
	}
}

// A different enrollment identity than the journal already holds conflicts, as bootstrap does.
func TestNewHostEnrollmentConflictsWithAnEnrolledJournal(t *testing.T) {
	h := newHost(t)
	h.mkdir("/var/lib/portal-app-runner", 0o755)
	h.mkdir("/var/lib/portal-app-runner/journal", 0o700)
	h.deployment = enrollmentDoc(2).deployment()
	opts := h.options(true)
	opts.Enrollment = h.enrollmentFile(enrollmentDoc(1))
	before := h.snapshot()
	if code, out := h.run(opts); code != ExitFailed || !strings.Contains(out, "different controller") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if h.snapshot() != before {
		t.Fatal("a refused run changed the host")
	}
}

// Portal sees the control directory at the compose file's fixed bind target, so the socket variable names that
// path even when the host's control root is elsewhere.
func TestEnvReportNamesTheContainerSideSocketForACustomControlRoot(t *testing.T) {
	h := newHost(t)
	opts := h.options(true)
	opts.ControlRoot = "/srv/runner-control"
	opts.Enrollment = h.enrollmentFile(enrollmentDoc(1))
	code, out := h.run(opts)
	if code != ExitOK {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	for _, line := range []string{"RP_APP_RUNNER_CONTROL_DIR=/srv/runner-control", "RP_APP_RUNNER_SOCKET=/var/lib/portal-app-runner/control/runner.sock"} {
		if !strings.Contains(out, "\n"+line+"\n") {
			t.Errorf("the report lacks %q:\n%s", line, out)
		}
	}
	if cfg := mustLoad(t, h, "/etc/portal-app-runner/service.json"); cfg.ControlSocket != "/srv/runner-control/runner.sock" {
		t.Fatalf("the runner's own socket path: %s", cfg.ControlSocket)
	}
}

// A symbolic /etc/fstab would be replaced by the rename, so it is refused (at the plan, before anything changes).
func TestSymlinkedFstabIsRefusedAndLeftAlone(t *testing.T) {
	h := newHost(t)
	h.write("/etc/real-fstab", "/dev/sda1 / ext4 defaults 0 1\n", 0o644)
	os.Remove(h.p("/etc/fstab"))
	if err := os.Symlink(h.p("/etc/real-fstab"), h.p("/etc/fstab")); err != nil {
		t.Fatal(err)
	}
	opts := h.options(true)
	opts.Enrollment = h.enrollmentFile(enrollmentDoc(1))
	before := h.snapshot()
	code, out := h.run(opts)
	if code != ExitFailed || !strings.Contains(out, "symbolic link") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if h.snapshot() != before || len(h.mutating()) != 0 {
		t.Fatal("a refused run changed the host")
	}
}

// A path that would need fstab escaping is refused with the reason, before anything changes.
func TestPathsNeedingFstabEscapingAreRefused(t *testing.T) {
	h := newHost(t)
	opts := h.options(true)
	opts.ImageDir = "/var/lib/portal volumes"
	opts.Enrollment = h.enrollmentFile(enrollmentDoc(1))
	before := h.snapshot()
	code, out := h.run(opts)
	if code != ExitFailed || !strings.Contains(out, "whitespace") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if h.snapshot() != before {
		t.Fatal("a refused run changed the host")
	}
}
