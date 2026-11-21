package hostsetup

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/journal"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/provision"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/service"
)

// resolved is the layout this run works to: the existing service.json when adopting, the defaults and flags on
// a new host. Only the fields host-setup owns.
type resolved struct {
	dockerSocket                                    string
	journalDir, imageRoot, ipcRoot                  string
	controlRoot, controlSocket                      string
	dataRoot, checkpointRoot                        string
	imageDir                                        string
	uid, gid                                        uint32
	controller                                      service.Controller
	dataSize, checkpointSize                        int64
	dataMount, checkpointMount, dataImg, checkpoint string
}

// enrollment is the parsed --enrollment document.
type enrollment struct {
	doc service.BootstrapConfig
	key []byte
}

func cleanAbs(path string) bool {
	return path != "" && len(path) <= 4096 && filepath.IsAbs(path) && filepath.Clean(path) == path && !strings.ContainsAny(path, "\x00\r\n")
}

func overlaps(a, b string) bool { return under(a, b) || under(b, a) }

func orString(value, fallback string) string {
	if value != "" {
		return value
	}
	return fallback
}

func orID(value, fallback uint32) uint32 {
	if value != 0 {
		return value
	}
	return fallback
}

func (r *run) servicePath() string   { return filepath.Join(r.opts.ConfigDir, "service.json") }
func (r *run) bootstrapPath() string { return filepath.Join(r.opts.ConfigDir, "bootstrap.json") }

func (r *run) plan(ctx context.Context) error {
	r.opts.ConfigDir = orString(r.opts.ConfigDir, DefaultConfigDir)
	if !cleanAbs(r.opts.ConfigDir) {
		return refuse("the configuration directory must be a clean absolute path")
	}
	if r.opts.Enrollment != "" {
		raw, err := os.ReadFile(r.env.path(r.opts.Enrollment))
		if err != nil {
			return refuse("the enrollment document cannot be read: %v", err)
		}
		doc, err := service.ParseEnrollment(raw)
		if err != nil {
			return refuse("the enrollment document is not a valid public enrollment (portal.app-runner.bootstrap document)")
		}
		key, err := service.EnrollmentPublicKey(doc)
		if err != nil {
			return refuse("the enrollment document carries an invalid controller key")
		}
		r.enroll = &enrollment{doc: doc, key: key}
	}
	if _, err := os.Lstat(r.env.path(r.servicePath())); err == nil {
		r.adopt = true
		if err := r.resolveExisting(); err != nil {
			return err
		}
	} else if errors.Is(err, os.ErrNotExist) {
		if err := r.resolveNew(); err != nil {
			return err
		}
	} else {
		return refuse("service.json cannot be read: %v", err)
	}
	if err := r.env.Docker(ctx, r.cfg.dockerSocket); err != nil {
		return refuse("the Docker daemon does not pass the runner's profile check or is not identified in the runner's namespaces: %v", err)
	}
	if !r.adopt {
		r.measure(ctx)
	}
	r.planConfigDir()
	r.planDirs()
	if r.adopt {
		if err := r.planAdoptVolumes(ctx); err != nil {
			return err
		}
	} else if err := r.planNewVolumes(ctx); err != nil {
		return err
	}
	if err := r.planEnrollment(ctx); err != nil {
		return err
	}
	r.planOperatorFields()
	return nil
}

// ---- resolving the layout -------------------------------------------------------------------------------

func (r *run) resolveNew() error {
	o := r.opts
	state := DefaultStateDir
	c := resolved{
		dockerSocket:   orString(o.DockerSocket, DefaultDockerSocket),
		journalDir:     orString(o.JournalDir, filepath.Join(state, "journal")),
		imageRoot:      orString(o.ImageRoot, filepath.Join(state, "images")),
		ipcRoot:        orString(o.IPCRoot, DefaultIPCRoot),
		controlRoot:    orString(o.ControlRoot, filepath.Join(state, "control")),
		dataRoot:       orString(o.DataRoot, DefaultDataRoot),
		imageDir:       orString(o.ImageDir, filepath.Join(state, "volumes")),
		uid:            orID(o.ControllerUID, DefaultControllerID),
		gid:            orID(o.ControllerGID, DefaultControllerID),
		dataSize:       o.DataSize,
		checkpointSize: o.CheckpointSize,
	}
	c.checkpointRoot = orString(o.CheckpointRoot, DefaultCheckpointRoot)
	if c.dataSize == 0 {
		c.dataSize = DefaultDataSize
	}
	if c.checkpointSize == 0 {
		c.checkpointSize = DefaultCheckpointSize
	}
	c.controlSocket = filepath.Join(c.controlRoot, "runner.sock")
	for name, path := range map[string]string{"--docker-socket": c.dockerSocket, "--journal-dir": c.journalDir, "--image-root": c.imageRoot,
		"--ipc-root": c.ipcRoot, "--control-root": c.controlRoot, "--data-root": c.dataRoot, "--checkpoint-root": c.checkpointRoot, "--image-dir": c.imageDir} {
		if !cleanAbs(path) {
			return refuse("%s must be a clean absolute path", name)
		}
	}
	if filepath.Base(c.dataRoot) != provision.RootDirName || filepath.Base(c.checkpointRoot) != provision.RootDirName {
		return refuse("on a new host --data-root and --checkpoint-root must be <mount point>/%s, because each XFS volume's own root stays an ordinary mount point", provision.RootDirName)
	}
	c.dataMount, c.checkpointMount = filepath.Dir(c.dataRoot), filepath.Dir(c.checkpointRoot)
	c.dataImg, c.checkpoint = filepath.Join(c.imageDir, "data.img"), filepath.Join(c.imageDir, "checkpoint.img")
	if c.checkpointSize < productionCheckpointBytes && !o.BelowProduction {
		return refuse("the checkpoint volume is %d GiB, below the production minimum of 16 GiB (doc 185); give --checkpoint-size 16G or more, or --checkpoint-size-below-production for a test host", c.checkpointSize/gib)
	}
	set := []struct{ name, path string }{{"journal_dir", c.journalDir}, {"image_root", c.imageRoot}, {"ipc_root", c.ipcRoot}, {"control_root", c.controlRoot},
		{"data volume", c.dataMount}, {"checkpoint volume", c.checkpointMount}, {"image directory", c.imageDir}}
	for i := range set {
		for j := i + 1; j < len(set); j++ {
			if overlaps(set[i].path, set[j].path) {
				return refuse("%s and %s overlap; the runner requires its roots to be distinct and not nested", set[i].name, set[j].name)
			}
		}
	}
	r.cfg = c
	return r.checkIDs()
}

// checkIDs is section 8.4 for a new host: an ID that no host account uses, or a group named portal-app-runner
// that already holds the GID. Nothing in /etc/passwd or /etc/group is ever written.
func (r *run) checkIDs() error {
	users, groups, err := r.env.Accounts()
	if err != nil {
		return refuse("the host's /etc/passwd and /etc/group cannot be read: %v", err)
	}
	for _, u := range users {
		if u.ID == r.cfg.uid {
			return refuse("controller UID %d is held by the host user %q; choose another with --controller-uid", r.cfg.uid, u.Name)
		}
	}
	for _, g := range groups {
		if g.ID == r.cfg.gid && g.Name != GroupName {
			return refuse("controller GID %d is held by the host group %q; choose another with --controller-gid", r.cfg.gid, g.Name)
		}
	}
	r.note("controller IDs %d:%d are unused on this host (only /etc/passwd and /etc/group are read; LDAP and other NSS sources are not seen)", r.cfg.uid, r.cfg.gid)
	return nil
}

func (r *run) resolveExisting() error {
	cfg, err := service.LoadConfig(r.env.path(r.servicePath()))
	if err != nil {
		return refuse("the existing service.json is not a valid runner configuration (it must be private, strict JSON); host-setup adopts a working layout and does not repair one")
	}
	c := resolved{
		dockerSocket: cfg.DockerSocket, journalDir: cfg.JournalDir, imageRoot: cfg.ImageRoot, ipcRoot: cfg.IPCRoot,
		controlRoot: cfg.ControlRoot, controlSocket: cfg.ControlSocket, dataRoot: cfg.DataRoot, checkpointRoot: cfg.CheckpointRoot,
		uid: cfg.ControllerUID, gid: cfg.ControllerGID, controller: cfg.Controller,
	}
	r.cfg = c
	o := r.opts
	// A flag that is given and disagrees with the parsed existing value refuses. A flag that agrees, or is left
	// out, adopts the existing value.
	for _, f := range []struct {
		name, given, have string
	}{
		{"--docker-socket", o.DockerSocket, c.dockerSocket}, {"--journal-dir", o.JournalDir, c.journalDir}, {"--image-root", o.ImageRoot, c.imageRoot},
		{"--ipc-root", o.IPCRoot, c.ipcRoot}, {"--control-root", o.ControlRoot, c.controlRoot}, {"--data-root", o.DataRoot, c.dataRoot},
		{"--checkpoint-root", o.CheckpointRoot, c.checkpointRoot},
	} {
		if f.given != "" && f.given != f.have {
			r.conflict("%s %s disagrees with service.json (%s); an adopted host keeps its configured layout", f.name, f.given, f.have)
		}
	}
	if o.ControllerUID != 0 && o.ControllerUID != c.uid {
		r.conflict("--controller-uid %d disagrees with service.json (%d)", o.ControllerUID, c.uid)
	}
	if o.ControllerGID != 0 && o.ControllerGID != c.gid {
		r.conflict("--controller-gid %d disagrees with service.json (%d)", o.ControllerGID, c.gid)
	}
	r.note("controller IDs %d:%d come from service.json and are accepted whatever the host's user database says", c.uid, c.gid)
	if strings.HasPrefix(c.controlRoot, "/run/") {
		r.note("control_root %s is under /run, which is rebuilt at every boot: edit control_root and control_socket in service.json to a durable path (default %s/control) before switching to the compose runner", c.controlRoot, DefaultStateDir)
	}
	return nil
}

// ---- directories ---------------------------------------------------------------------------------------

type dirWant struct {
	role string
	path string
	gid  uint32
	mode os.FileMode
	// exact: the leaf must have exactly this owner and mode. Otherwise an existing leaf is accepted under the
	// ancestor rule (root-owned, not group- or world-writable).
	exact bool
}

func (r *run) planConfigDir() {
	r.planDir(dirWant{role: "configuration directory", path: r.opts.ConfigDir, mode: 0o700})
}

func (r *run) planDirs() {
	c := r.cfg
	r.planDir(dirWant{role: "journal directory", path: c.journalDir, mode: 0o700, exact: true})
	r.planDir(dirWant{role: "image scratch (image_root)", path: c.imageRoot, mode: 0o700, exact: true})
	r.planDir(dirWant{role: "control root", path: c.controlRoot, gid: c.gid, mode: 0o750, exact: true})
	if !r.adopt {
		r.planDir(dirWant{role: "XFS image directory", path: c.imageDir, mode: 0o700, exact: true})
		// The mount points are made by provision; their parents must exist and be safe first.
		for _, mount := range []string{c.dataMount, c.checkpointMount} {
			r.planDir(dirWant{role: "directory above a volume mount point", path: filepath.Dir(mount), mode: 0o755})
		}
	}
}

func (r *run) lstat(path string) (info os.FileInfo, uid, gid uint32, err error) {
	info, err = os.Lstat(r.env.path(path))
	if err != nil {
		return nil, 0, 0, err
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return nil, 0, 0, errors.New("no ownership information")
	}
	return info, st.Uid, st.Gid, nil
}

func (r *run) isRoot(uid uint32) bool { return uid == r.env.RootUID }

// planDir records what it takes to have path as wanted, creating missing ancestors as root:root 0755.
func (r *run) planDir(w dirWant) {
	if !cleanAbs(w.path) {
		r.conflict("%s %q is not a clean absolute path", w.role, w.path)
		return
	}
	parts := strings.Split(strings.TrimPrefix(w.path, "/"), "/")
	current := "/"
	for i, part := range parts {
		current = filepath.Join(current, part)
		leaf := i == len(parts)-1
		if r.planned[current] {
			continue
		}
		info, uid, gid, err := r.lstat(current)
		switch {
		case err == nil:
			if !info.IsDir() {
				r.conflict("%s: %s is not a real directory (a file or a symbolic link)", w.role, current)
				return
			}
			if leaf && w.exact {
				wantGID := w.gid
				if info.Mode().Perm() != w.mode || info.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 ||
					!r.isRoot(uid) || gid != r.env.physicalGID(wantGID) {
					r.conflict("%s %s is %s owned %d:%d, host-setup needs root:%s mode %04o", w.role, current, info.Mode().Perm(), uid, gid, groupName(wantGID), w.mode)
					return
				}
				r.match("%s %s (root:%s %04o)", w.role, current, groupName(w.gid), w.mode)
				continue
			}
			if !r.isRoot(uid) || (info.Mode().Perm()&0o022 != 0 && !(info.Mode()&os.ModeSticky != 0)) {
				r.conflict("%s: %s must be owned by root and not group- or world-writable", w.role, current)
				return
			}
			if leaf {
				r.match("%s %s (root-owned, not group- or world-writable)", w.role, current)
			}
		case errors.Is(err, os.ErrNotExist):
			path, mode, gid := current, os.FileMode(0o755), uint32(0)
			if leaf {
				mode, gid = w.mode, w.gid
			}
			text := fmt.Sprintf("directory %s (root:%s %04o)", path, groupName(gid), mode)
			if leaf {
				text = fmt.Sprintf("%s %s (root:%s %04o)", w.role, path, groupName(gid), mode)
			}
			r.planned[path] = true
			r.create(text, func(ctx context.Context) error { return r.makeDir(path, gid, mode) })
		default:
			r.conflict("%s: %s cannot be read: %v", w.role, current, err)
			return
		}
	}
}

func groupName(gid uint32) string {
	if gid == 0 {
		return "root"
	}
	return fmt.Sprintf("%d", gid)
}

func (r *run) makeDir(path string, gid uint32, mode os.FileMode) error {
	physical := r.env.path(path)
	if err := os.Mkdir(physical, mode); err != nil {
		return err
	}
	// The process umask (0077 under the entry) must not decide the mode.
	if err := os.Chown(physical, int(r.env.physicalUID(0)), int(r.env.physicalGID(gid))); err != nil {
		return err
	}
	return os.Chmod(physical, mode)
}

// ---- volumes -------------------------------------------------------------------------------------------

// plannedFS shows provision's dry run the directories this run will have created by the time it applies.
type plannedFS struct {
	provision.FS
	planned map[string]bool
	run     *run
}

func (p plannedFS) Stat(path string) (provision.FileInfo, error) {
	info, err := p.FS.Stat(path)
	if err == nil && !info.Exists && p.planned[path] {
		return provision.FileInfo{Exists: true, Dir: true, Perm: 0o755}, nil
	}
	return info, err
}

func (p plannedFS) Resolve(path string) (string, error) {
	if p.planned[path] {
		return path, nil
	}
	return p.FS.Resolve(path)
}

func (p plannedFS) FSKind(path string) (string, error) {
	// A directory that does not exist yet lives on the filesystem of its nearest existing ancestor.
	for dir := path; ; dir = filepath.Dir(dir) {
		if p.planned[dir] {
			continue
		}
		if info, err := p.FS.Stat(dir); err == nil && info.Exists {
			return p.FS.FSKind(dir)
		}
		if dir == "/" {
			return p.FS.FSKind("/")
		}
	}
}

func (p plannedFS) DirEmpty(path string) (bool, error) {
	if p.planned[path] {
		return true, nil
	}
	return p.FS.DirEmpty(path)
}

var wouldChange = regexp.MustCompile(`: would (create|attach|re-preallocate)`)

func (r *run) spec() provision.Spec {
	c := r.cfg
	return provision.Spec{DataImage: c.dataImg, CheckpointImage: c.checkpoint, DataSize: c.dataSize, CheckpointSize: c.checkpointSize,
		DataMount: c.dataMount, CheckpointMount: c.checkpointMount}
}

func (r *run) planNewVolumes(ctx context.Context) error {
	c := r.cfg
	spec := r.spec()
	if err := spec.Validate(); err != nil {
		return refuse("%v", err)
	}
	// chattr is needed only when the images land on btrfs (Q2).
	if kind, err := (plannedFS{FS: r.env.Provision.FS, planned: r.planned}).FSKind(c.imageDir); err == nil && kind == "btrfs" && !r.env.HasTool("chattr") {
		return refuse("chattr is not installed on the host (package %s), and the image directory is on btrfs; install it and run host-setup again", packageOf("chattr"))
	}
	penv := r.env.Provision
	penv.FS = plannedFS{FS: penv.FS, planned: r.planned, run: r}
	var out bytes.Buffer
	if err := provision.Run(ctx, penv, spec, true, &out); err != nil {
		var ref *provision.RefusalError
		if errors.As(err, &ref) {
			r.conflict("%v", err)
			return nil
		}
		return refuse("%v", err)
	}
	changes := false
	for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		if strings.HasPrefix(line, "dry run:") {
			continue
		}
		if wouldChange.MatchString(line) {
			changes = true
			r.add("create", "volume "+strings.Replace(line, ": would ", ": ", 1), nil)
		} else {
			r.match("volume %s", strings.Replace(line, ": find ", ": ", 1))
		}
	}
	if changes {
		// One real step runs provision for both volumes (it inspects both before changing either).
		first := -1
		for i, s := range r.steps {
			if s.kind == "create" && s.do == nil && strings.HasPrefix(s.text, "volume ") {
				if first < 0 {
					first = i
					r.steps[i].do = func(ctx context.Context) error { return r.provisionVolumes(ctx) }
				}
			}
		}
	}
	r.planFstab([][2]string{{c.dataImg, c.dataMount}, {c.checkpoint, c.checkpointMount}})
	return nil
}

func (r *run) provisionVolumes(ctx context.Context) error {
	var out bytes.Buffer
	err := provision.Run(ctx, r.env.Provision, r.spec(), false, &out)
	r.printf("%s", summarizeProvision(out.String()))
	if err != nil {
		return err
	}
	r.measure(ctx)
	// The data-root lock is taken as soon as the root exists: a runner started now would exit "in use".
	return r.lockData(r.cfg.dataRoot)
}

// summarizeProvision keeps the per-volume lines of provision's report and drops its fstab and systemd
// instructions, which host-setup carries out itself.
func summarizeProvision(report string) string {
	var kept []string
	for _, line := range strings.Split(report, "\n") {
		if strings.HasPrefix(line, "data:") || strings.HasPrefix(line, "checkpoint:") {
			kept = append(kept, "  "+line)
		}
		if strings.HasPrefix(line, "Runner configuration") {
			break
		}
	}
	if len(kept) == 0 {
		return ""
	}
	return strings.Join(kept, "\n") + "\n"
}

var loopDevice = regexp.MustCompile(`^/dev/loop[0-9]+$`)

func (r *run) planAdoptVolumes(ctx context.Context) error {
	mounts, err := r.env.Provision.Mounts()
	if err != nil {
		return refuse("the mount table cannot be read: %v", err)
	}
	var wanted [][2]string
	for _, v := range []struct{ name, root string }{{"data root", r.cfg.dataRoot}, {"checkpoint root", r.cfg.checkpointRoot}} {
		if !isDir(r.env.path(v.root)) {
			r.conflict("the configured %s %s does not exist: it is not mounted. host-setup does not guess which image belongs to it", v.name, v.root)
			continue
		}
		if _, err := r.env.Provision.Verify(ctx, v.root); err != nil {
			if r.env.IsDataRootBusy != nil && r.env.IsDataRootBusy(err) {
				// A serving runner holds the root's lock: it passed these checks when it started.
				r.note("the %s %s is held by a running runner, which passed the runner's own checks when it started", v.name, v.root)
			} else {
				r.conflict("the configured %s %s is not mounted, or fails the runner's data-root checks (XFS mounted prjquota,nodev,nosuid,noexec; root-owned 0700; quota enforcement): %v", v.name, v.root, err)
				continue
			}
		} else {
			r.match("%s %s passes the runner's data-root checks", v.name, v.root)
		}
		var cover *provision.Mount
		for i := range mounts {
			m := &mounts[i]
			if under(v.root, m.Point) && (cover == nil || len(m.Point) > len(cover.Point)) {
				cover = m
			}
		}
		if cover == nil || !loopDevice.MatchString(cover.Source) {
			source := "no mount"
			if cover != nil {
				source = cover.Source
			}
			r.match("%s is on %s, a volume the operator mounted: accepted as it is, no fstab change", v.name, source)
			continue
		}
		image, err := r.env.LoopBacking(cover.Source)
		if err != nil || !cleanAbs(image) {
			r.conflict("the backing file of %s (%s) cannot be read", cover.Source, v.name)
			continue
		}
		if problem := r.checkImage(ctx, image); problem != "" {
			r.conflict("%s image %s: %s", v.name, image, problem)
			continue
		}
		r.match("%s is the loop-mounted XFS image %s at %s", v.name, image, cover.Point)
		wanted = append(wanted, [2]string{image, cover.Point})
	}
	r.planFstab(wanted)
	return nil
}

// checkImage applies provision's rules to an existing image: a root-owned regular file with mode 0600 that
// holds XFS.
func (r *run) checkImage(ctx context.Context, image string) string {
	info, err := r.env.Provision.FS.Stat(image)
	if err != nil || !info.Exists {
		return "cannot be read"
	}
	if !info.Regular || info.UID != 0 || info.Perm != 0o600 {
		return "is not a root-owned regular file with mode 0600"
	}
	typ, err := r.env.Provision.Exec.Run(ctx, "blkid", "-p", "-o", "value", "-s", "TYPE", image)
	if err != nil || strings.TrimSpace(typ) != "xfs" {
		return "does not hold an XFS filesystem"
	}
	return ""
}

func (r *run) planFstab(wanted [][2]string) {
	data, err := os.ReadFile(r.env.path(fstabPath))
	if err != nil && !os.IsNotExist(err) {
		r.conflict("/etc/fstab cannot be read: %v", err)
		return
	}
	lines := parseFstab(data)
	var missing [][2]string
	for _, w := range wanted {
		for _, path := range w {
			if !fstabSafePath(path) {
				r.conflict("%q contains whitespace or a character that needs fstab escaping (backslash, #, quotes, control or non-ASCII characters); host-setup does not write such a path into /etc/fstab", path)
				return
			}
		}
		switch state, detail := fstabState(lines, w[0], w[1]); state {
		case "conflict":
			r.conflict("%s", detail)
		case "match":
			r.match("fstab line for %s", w[1])
		default:
			missing = append(missing, w)
		}
	}
	if len(missing) == 0 {
		return
	}
	if r.fstabIsSymlink() {
		r.conflict("/etc/fstab is a symbolic link; host-setup would replace it with a regular file, so it leaves it alone")
		return
	}
	var text []string
	for _, w := range missing {
		text = append(text, volumeLine(w[0], w[1]))
	}
	r.create("fstab: add "+strings.Join(text, " ; "), func(ctx context.Context) error { return r.appendFstab(ctx, missing) })
	r.create("systemctl daemon-reload, so systemd's generated mount units match the new fstab", func(ctx context.Context) error {
		if !r.fstabChanged || !isDir(r.env.path("/run/systemd/system")) {
			return nil
		}
		_, err := r.env.Provision.Exec.Run(ctx, "systemctl", "daemon-reload")
		return err
	})
}

// ---- enrollment and configuration files -----------------------------------------------------------------

func sameController(a, b service.Controller) bool { return a == b }

func controllerOf(d journal.Deployment) service.Controller {
	return service.Controller{DeploymentID: d.DeploymentID, ControllerEpoch: d.ControllerEpoch, ControlRevision: d.ControlRevision, Fingerprint: d.Fingerprint}
}

func (r *run) planEnrollment(ctx context.Context) error {
	var enrolled *journal.Deployment
	if isDir(r.env.path(r.cfg.journalDir)) {
		d, err := r.env.ReadDeployment(ctx, r.cfg.journalDir)
		switch {
		case err == nil:
			enrolled = &d
		case errors.Is(err, journal.ErrNotEnrolled):
		default:
			return refuse("the journal's enrollment cannot be read: %v", err)
		}
	}
	if r.adopt {
		return r.planAdoptEnrollment(enrolled)
	}
	return r.planNewEnrollment(enrolled)
}

func (r *run) planAdoptEnrollment(enrolled *journal.Deployment) error {
	if enrolled != nil && !sameController(controllerOf(*enrolled), r.cfg.controller) {
		r.conflict("the journal is enrolled with a different identity than the controller block of service.json")
		return nil
	}
	if r.enroll != nil {
		if enrolled != nil && (!sameController(r.enroll.doc.Controller, controllerOf(*enrolled)) || !bytes.Equal(r.enroll.key, enrolled.PublicKey)) {
			r.conflict("--enrollment names a different controller than the one enrolled in the journal")
			return nil
		}
		if enrolled == nil && !sameController(r.enroll.doc.Controller, r.cfg.controller) {
			r.conflict("--enrollment names a different controller than the controller block of service.json")
			return nil
		}
	}
	if existing, err := os.ReadFile(r.env.path(r.bootstrapPath())); err == nil {
		doc, perr := service.ParseEnrollment(existing)
		if perr != nil || !sameController(doc.Controller, r.cfg.controller) {
			r.conflict("bootstrap.json does not carry the controller identity of service.json")
			return nil
		}
		r.match("bootstrap.json carries the controller identity of service.json")
	}
	switch {
	case enrolled != nil:
		r.match("the journal is enrolled with the controller of service.json (deployment %s, revision %d)", enrolled.DeploymentID, enrolled.ControlRevision)
	case r.enroll != nil:
		doc := r.enroll
		r.create("enroll the journal with the --enrollment controller (bootstrap path)", func(ctx context.Context) error { return r.enrollJournal(ctx, doc) })
	default:
		r.pending = true
		r.note("enrollment pending: the journal holds no controller and no --enrollment was given")
	}
	return nil
}

func (r *run) planNewEnrollment(enrolled *journal.Deployment) error {
	if r.enroll == nil {
		r.pending = true
		r.note("enrollment pending: no --enrollment document, so service.json and bootstrap.json are not written and exit status 3 follows --apply")
		return nil
	}
	doc := r.enroll
	if enrolled != nil && (!sameController(doc.doc.Controller, controllerOf(*enrolled)) || !bytes.Equal(doc.key, enrolled.PublicKey)) {
		r.conflict("--enrollment names a different controller than the one already enrolled in the journal")
		return nil
	}
	if existing, err := os.ReadFile(r.env.path(r.bootstrapPath())); err == nil {
		have, perr := service.ParseEnrollment(existing)
		if perr != nil || !sameController(have.Controller, doc.doc.Controller) || have.ControllerPublicKey != doc.doc.ControllerPublicKey {
			r.conflict("bootstrap.json exists and names a different controller than --enrollment")
			return nil
		}
		r.match("bootstrap.json carries the --enrollment controller")
	} else {
		r.create(fmt.Sprintf("write %s (0600)", r.bootstrapPath()), func(ctx context.Context) error { return r.writeBootstrap(doc) })
	}
	r.create(fmt.Sprintf("write %s (0600) from the defaults, the provisioned roots and their usable size", r.servicePath()),
		func(ctx context.Context) error { return r.writeService(ctx, doc) })
	if enrolled != nil {
		r.match("the journal is already enrolled with the --enrollment controller")
	} else {
		r.create("enroll the journal with the --enrollment controller (bootstrap path)", func(ctx context.Context) error { return r.enrollJournal(ctx, doc) })
	}
	return nil
}

func (r *run) enrollJournal(ctx context.Context, doc *enrollment) error {
	if err := r.ensureJournal(); err != nil {
		return err
	}
	_, err := r.journal.Enroll(ctx, journal.ControllerEnrollment{
		DeploymentID: doc.doc.Controller.DeploymentID, ControllerEpoch: doc.doc.Controller.ControllerEpoch,
		ControlRevision: doc.doc.Controller.ControlRevision, PublicKey: doc.key, Fingerprint: doc.doc.Controller.Fingerprint,
	})
	if errors.Is(err, journal.ErrEnrollmentConflict) {
		return refuse("the journal is already enrolled with a different identity, or holds work: enrollment conflicts")
	}
	return err
}

func (r *run) planOperatorFields() {
	if r.adopt {
		r.add("operator", "capacity, network_*, minimum_reported_*, private_networks, allow_local_reference_images and proxy_pins of service.json are the operator's: read for nothing, never rewritten", nil)
	}
}
