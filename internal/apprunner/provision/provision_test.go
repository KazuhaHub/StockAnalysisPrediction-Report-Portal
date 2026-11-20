package provision

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

const (
	dataImage  = "/srv/portal/data.img"
	ckptImage  = "/srv/portal/checkpoint.img"
	dataMount  = "/srv/portal/mnt-data"
	ckptMount  = "/srv/portal/mnt-checkpoint"
	eightGiB   = int64(8) << 30
	usableSize = int64(8<<30) - 64<<20
)

func spec() Spec {
	return Spec{DataImage: dataImage, CheckpointImage: ckptImage, DataSize: eightGiB, CheckpointSize: eightGiB, DataMount: dataMount, CheckpointMount: ckptMount}
}

// fakeHost simulates just enough of a Linux host for the provisioning flow.
type fakeHost struct {
	files     map[string]FileInfo
	nonEmpty  map[string]bool
	kind      string
	mounts    []Mount
	loops     map[string]string // device -> image
	imageType map[string]string
	log       []string
	failOn    string
	verifyErr error
	euid      int
	verified  []string
	nextLoop  int
}

func newHost() *fakeHost {
	return &fakeHost{
		files: map[string]FileInfo{
			"/srv/portal": {Exists: true, Dir: true, Perm: 0o755},
		},
		nonEmpty: map[string]bool{}, kind: "ext4", loops: map[string]string{}, imageType: map[string]string{}, nextLoop: 3,
	}
}

func (h *fakeHost) env() Env {
	return Env{Exec: h, FS: h, Geteuid: func() int { return h.euid },
		Mounts: func() ([]Mount, error) { return h.mounts, nil },
		Verify: func(_ context.Context, dir string) (VerifyResult, error) {
			h.verified = append(h.verified, dir)
			return VerifyResult{UsableBytes: usableSize}, h.verifyErr
		}}
}

func (h *fakeHost) mutations() []string {
	var out []string
	for _, line := range h.log {
		switch {
		case strings.HasPrefix(line, "fallocate"), strings.HasPrefix(line, "mkfs.xfs"), strings.HasPrefix(line, "mount "),
			strings.HasPrefix(line, "umount"), strings.HasPrefix(line, "chattr"), strings.HasPrefix(line, "losetup --find"), strings.HasPrefix(line, "losetup --detach"):
			out = append(out, line)
		}
	}
	return out
}

func (h *fakeHost) Run(_ context.Context, name string, args ...string) (string, error) {
	line := strings.TrimSpace(name + " " + strings.Join(args, " "))
	h.log = append(h.log, line)
	if h.failOn == name {
		return "", errors.New(name + " failed")
	}
	switch name {
	case "blkid":
		typ := h.imageType[args[len(args)-1]]
		if typ == "" {
			return "", errors.New("exit status 2")
		}
		return typ + "\n", nil
	case "losetup":
		switch args[0] {
		case "--json":
			var list []map[string]string
			for dev, image := range h.loops {
				if image == args[3] {
					list = append(list, map[string]string{"name": dev, "back-file": image})
				}
			}
			if len(list) == 0 {
				return "", nil
			}
			raw, _ := json.Marshal(map[string]any{"loopdevices": list})
			return string(raw), nil
		case "--find":
			dev := fmt.Sprintf("/dev/loop%d", h.nextLoop)
			h.nextLoop++
			h.loops[dev] = args[len(args)-1]
			return dev + "\n", nil
		case "--detach":
			delete(h.loops, args[1])
		}
	case "fallocate":
		info := h.files[args[2]]
		fmt.Sscan(args[1], &info.Size)
		info.Allocated = info.Size
		h.files[args[2]] = info
	case "mkfs.xfs":
		h.imageType[h.loops[args[0]]] = "xfs"
	case "mount":
		opts := map[string]bool{}
		for _, o := range strings.Split(args[3], ",") {
			opts[o] = true
		}
		h.mounts = append(h.mounts, Mount{Point: args[5], FSType: "xfs", Source: args[4], Options: opts})
		h.files[args[5]] = FileInfo{Exists: true, Dir: true, Perm: 0o755}
	case "umount":
		for i, m := range h.mounts {
			if m.Point == args[0] {
				h.mounts = append(h.mounts[:i], h.mounts[i+1:]...)
				break
			}
		}
	}
	return "", nil
}

func (h *fakeHost) Stat(path string) (FileInfo, error) { return h.files[path], nil }
func (h *fakeHost) DirEmpty(path string) (bool, error) { return !h.nonEmpty[path], nil }
func (h *fakeHost) Resolve(path string) (string, error) {
	return path, nil
}
func (h *fakeHost) CreateExclusive(path string, perm uint32) error {
	if h.files[path].Exists {
		return errors.New("exists")
	}
	h.files[path] = FileInfo{Exists: true, Regular: true, Perm: perm}
	h.log = append(h.log, "create "+path)
	return nil
}
func (h *fakeHost) Mkdir(path string, perm uint32) error {
	h.files[path] = FileInfo{Exists: true, Dir: true, Perm: perm}
	h.log = append(h.log, "mkdir "+path)
	return nil
}
func (h *fakeHost) Chmod(path string, perm uint32) error {
	info := h.files[path]
	info.Perm = perm
	h.files[path] = info
	return nil
}
func (h *fakeHost) Remove(path string) error {
	delete(h.files, path)
	h.log = append(h.log, "remove "+path)
	return nil
}
func (h *fakeHost) FSKind(string) (string, error) { return h.kind, nil }

func (h *fakeHost) place(path string, info FileInfo) { h.files[path] = info }

func run(h *fakeHost, s Spec, dry bool) (string, error) {
	var out bytes.Buffer
	err := Run(context.Background(), h.env(), s, dry, &out)
	return out.String(), err
}

func indexOf(log []string, prefix string) int {
	for i, line := range log {
		if strings.HasPrefix(line, prefix) {
			return i
		}
	}
	return -1
}

func TestSpecValidationRefusesBeforeAnythingIsInspected(t *testing.T) {
	cases := map[string]func(*Spec){
		"relative image":       func(s *Spec) { s.DataImage = "data.img" },
		"unclean path":         func(s *Spec) { s.DataMount = "/srv/portal/../portal/mnt" },
		"root path":            func(s *Spec) { s.CheckpointMount = "/" },
		"empty path":           func(s *Spec) { s.CheckpointImage = "" },
		"comma in path":        func(s *Spec) { s.DataMount = "/srv/a,b" },
		"same image":           func(s *Spec) { s.CheckpointImage = s.DataImage },
		"same mount":           func(s *Spec) { s.CheckpointMount = s.DataMount },
		"image inside mount":   func(s *Spec) { s.DataImage = dataMount + "/data.img" },
		"mount inside mount":   func(s *Spec) { s.CheckpointMount = dataMount + "/ckpt" },
		"image is a mount dir": func(s *Spec) { s.DataImage = ckptMount },
		"too small":            func(s *Spec) { s.DataSize = 512 << 20 },
		"too large":            func(s *Spec) { s.DataSize = MaxImageBytes + 1<<20 },
		"not a whole MiB":      func(s *Spec) { s.CheckpointSize = eightGiB + 1 },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			h := newHost()
			s := spec()
			mutate(&s)
			_, err := run(h, s, false)
			if !errors.Is(err, ErrInvalid) || len(h.log) != 0 {
				t.Fatalf("accepted or touched the host: err=%v log=%v", err, h.log)
			}
		})
	}
}

func TestParseSize(t *testing.T) {
	good := map[string]int64{"8G": 8 << 30, "8GiB": 8 << 30, "512m": 512 << 20, "1T": 1 << 40, "1073741824": 1 << 30, " 2g ": 2 << 30}
	for in, want := range good {
		if got, err := ParseSize(in); err != nil || got != want {
			t.Fatalf("ParseSize(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	for _, in := range []string{"", "G", "0", "-1G", "8X", "1.5G", "99999999T", "eightG"} {
		if _, err := ParseSize(in); !errors.Is(err, ErrInvalid) {
			t.Fatalf("ParseSize(%q) accepted", in)
		}
	}
}

func TestNonRootIsRefusedButDryRunNeedsNoRootAndChangesNothing(t *testing.T) {
	h := newHost()
	h.euid = 1000
	if _, err := run(h, spec(), false); !errors.Is(err, ErrNotRoot) || len(h.log) != 0 {
		t.Fatalf("non-root run: %v %v", err, h.log)
	}
	out, err := run(h, spec(), true)
	if err != nil || !strings.Contains(out, "data: would create "+dataImage) || !strings.Contains(out, "nothing was changed") {
		t.Fatalf("dry run: %v\n%s", err, out)
	}
	if len(h.mutations()) != 0 || indexOf(h.log, "create ") >= 0 || indexOf(h.log, "mkdir ") >= 0 || len(h.verified) != 0 {
		t.Fatalf("dry run changed the host: %v", h.log)
	}
}

func TestFreshProvisioningRunsTheStepsInOrderAndPrintsPersistence(t *testing.T) {
	h := newHost()
	out, err := run(h, spec(), false)
	if err != nil {
		t.Fatal(err)
	}
	// Per volume: preallocate, then attach with direct I/O, then format, then mount with the quota options.
	order := []string{
		"fallocate -l 8589934592 " + dataImage,
		"losetup --find --show --direct-io=on " + dataImage,
		"mkfs.xfs /dev/loop3",
		"mount -t xfs -o prjquota,nodev,nosuid,noexec /dev/loop3 " + dataMount,
	}
	last := -1
	for _, want := range order {
		i := indexOf(h.log, want)
		if i <= last {
			t.Fatalf("%q missing or out of order (%d after %d) in %v", want, i, last, h.log)
		}
		last = i
	}
	if indexOf(h.log, "mkfs.xfs -f") >= 0 || indexOf(h.log, "mount -t xfs -o prjquota,nodev,nosuid,noexec /dev/loop4 "+ckptMount) < 0 {
		t.Fatalf("unexpected mkfs force or missing checkpoint mount: %v", h.log)
	}
	if h.files[dataImage].Perm != 0o600 || h.files[dataMount].Perm != 0o700 || h.files[dataMount+"/root"].Perm != 0o700 {
		t.Fatalf("modes: %+v %+v", h.files[dataImage], h.files[dataMount])
	}
	if got := strings.Join(h.verified, ","); got != dataMount+"/root,"+ckptMount+"/root" {
		t.Fatalf("the runner's checks ran on %q", got)
	}
	for _, want := range []string{
		`"data_root": "` + dataMount + `/root"`, `"checkpoint_root": "` + ckptMount + `/root"`,
		`"disk_bytes": 7516192768`, // 7 GiB: below 8 GiB minus the XFS log, as the runner requires
		dataImage + " " + dataMount + " xfs loop,prjquota,nodev,nosuid,noexec 0 0",
		"# /etc/systemd/system/srv-portal-mnt\\x2ddata.mount", "RequiresMountsFor=" + dataMount + " " + ckptMount,
		"nothing under /etc was changed",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("output lacks %q:\n%s", want, out)
		}
	}
}

func TestRerunAfterSuccessOnlyReverifies(t *testing.T) {
	h := newHost()
	if _, err := run(h, spec(), false); err != nil {
		t.Fatal(err)
	}
	h.log, h.verified = nil, nil
	out, err := run(h, spec(), false)
	if err != nil {
		t.Fatalf("idempotent rerun: %v", err)
	}
	if len(h.mutations()) != 0 || indexOf(h.log, "create ") >= 0 || len(h.verified) != 2 || !strings.Contains(out, "already-provisioned") {
		t.Fatalf("rerun changed the host or skipped verification: %v %v\n%s", h.log, h.verified, out)
	}
}

func TestAnExistingXFSImageIsReattachedWithoutFormatting(t *testing.T) {
	h := newHost()
	for _, image := range []string{dataImage, ckptImage} {
		h.place(image, FileInfo{Exists: true, Regular: true, Perm: 0o600, Size: eightGiB, Allocated: eightGiB})
		h.imageType[image] = "xfs"
	}
	if _, err := run(h, spec(), false); err != nil {
		t.Fatal(err)
	}
	if indexOf(h.log, "fallocate") >= 0 || indexOf(h.log, "mkfs.xfs") >= 0 || indexOf(h.log, "losetup --find") < 0 || indexOf(h.log, "mount -t xfs") < 0 {
		t.Fatalf("an existing XFS image was recreated or not mounted: %v", h.log)
	}
}

func TestExistingContentIsRefusedAndNothingIsChanged(t *testing.T) {
	good := FileInfo{Exists: true, Regular: true, Perm: 0o600, Size: eightGiB, Allocated: eightGiB}
	cases := map[string]func(h *fakeHost){
		"image is not XFS": func(h *fakeHost) { h.place(dataImage, good); h.imageType[dataImage] = "ext4" },
		"image has no filesystem": func(h *fakeHost) {
			h.place(dataImage, good)
		},
		"image has another size": func(h *fakeHost) {
			g := good
			g.Size = 4 << 30
			h.place(dataImage, g)
			h.imageType[dataImage] = "xfs"
		},
		"image is sparse": func(h *fakeHost) {
			g := good
			g.Allocated = 1 << 20
			h.place(dataImage, g)
			h.imageType[dataImage] = "xfs"
		},
		"image has loose permissions": func(h *fakeHost) {
			g := good
			g.Perm = 0o644
			h.place(dataImage, g)
			h.imageType[dataImage] = "xfs"
		},
		"image is a directory": func(h *fakeHost) { h.place(dataImage, FileInfo{Exists: true, Dir: true, Perm: 0o600}) },
		"mount dir has files": func(h *fakeHost) {
			h.place(dataMount, FileInfo{Exists: true, Dir: true, Perm: 0o700})
			h.nonEmpty[dataMount] = true
		},
		"mount point is a file": func(h *fakeHost) {
			h.place(dataMount, FileInfo{Exists: true, Regular: true, Perm: 0o600})
		},
		"foreign mount on the mount point": func(h *fakeHost) {
			h.place(dataMount, FileInfo{Exists: true, Dir: true, Perm: 0o700})
			h.mounts = []Mount{{Point: dataMount, FSType: "ext4", Source: "/dev/sdb1", Options: map[string]bool{"rw": true}}}
		},
		"xfs mount of another source": func(h *fakeHost) {
			h.mounts = []Mount{{Point: dataMount, FSType: "xfs", Source: "/dev/loop9", Options: map[string]bool{"prjquota": true, "nodev": true, "nosuid": true, "noexec": true}}}
		},
		"our image mounted without quota": func(h *fakeHost) {
			h.place(dataImage, good)
			h.imageType[dataImage] = "xfs"
			h.loops["/dev/loop5"] = dataImage
			h.mounts = []Mount{{Point: dataMount, FSType: "xfs", Source: "/dev/loop5", Options: map[string]bool{"nodev": true, "nosuid": true, "noexec": true}}}
		},
		"mount below the mount point": func(h *fakeHost) {
			h.mounts = []Mount{{Point: dataMount + "/sub", FSType: "ext4", Source: "/dev/sdb1"}}
		},
		"image mounted elsewhere": func(h *fakeHost) {
			h.place(dataImage, good)
			h.imageType[dataImage] = "xfs"
			h.loops["/dev/loop5"] = dataImage
			h.mounts = []Mount{{Point: "/mnt/other", FSType: "xfs", Source: "/dev/loop5"}}
		},
		"image attached but not mounted": func(h *fakeHost) {
			h.place(dataImage, good)
			h.imageType[dataImage] = "xfs"
			h.loops["/dev/loop5"] = dataImage
		},
		"checkpoint image is foreign": func(h *fakeHost) { h.place(ckptImage, good); h.imageType[ckptImage] = "ext4" },
		"parent is world writable":    func(h *fakeHost) { h.place("/srv/portal", FileInfo{Exists: true, Dir: true, Perm: 0o777}) },
		"parent is not root-owned":    func(h *fakeHost) { h.place("/srv/portal", FileInfo{Exists: true, Dir: true, Perm: 0o755, UID: 1000}) },
		"parent is missing":           func(h *fakeHost) { delete(h.files, "/srv/portal") },
		"image directory on tmpfs":    func(h *fakeHost) { h.kind = "tmpfs" },
		"image directory on zfs":      func(h *fakeHost) { h.kind = "zfs" },
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			h := newHost()
			setup(h)
			beforeMounts, beforeLoops := len(h.mounts), len(h.loops)
			_, err := run(h, spec(), false)
			var refusal *RefusalError
			if !errors.As(err, &refusal) {
				t.Fatalf("not refused: %v", err)
			}
			if len(h.mutations()) != 0 || indexOf(h.log, "create ") >= 0 || indexOf(h.log, "mkdir ") >= 0 || indexOf(h.log, "remove ") >= 0 ||
				len(h.mounts) != beforeMounts || len(h.loops) != beforeLoops || len(h.verified) != 0 {
				t.Fatalf("a refusal changed the host: %v", h.log)
			}
		})
	}
}

func TestACheckpointRefusalLeavesTheDataVolumeUntouched(t *testing.T) {
	h := newHost()
	h.place(ckptMount, FileInfo{Exists: true, Dir: true, Perm: 0o700})
	h.nonEmpty[ckptMount] = true
	if _, err := run(h, spec(), false); err == nil || len(h.mutations()) != 0 || indexOf(h.log, "create ") >= 0 {
		t.Fatalf("the data volume was created before the checkpoint volume was inspected: %v %v", err, h.log)
	}
}

func TestBtrfsImageGetsNoCopyOnWriteBeforePreallocation(t *testing.T) {
	h := newHost()
	h.kind = "btrfs"
	if _, err := run(h, spec(), false); err != nil {
		t.Fatal(err)
	}
	if c, f := indexOf(h.log, "chattr +C "+dataImage), indexOf(h.log, "fallocate -l 8589934592 "+dataImage); c < 0 || f < 0 || c > f {
		t.Fatalf("chattr +C must precede fallocate: %v", h.log)
	}
}

func TestFailuresRollBackOnlyWhatThisRunMade(t *testing.T) {
	for _, failure := range []string{"fallocate", "losetup", "mkfs.xfs", "mount"} {
		t.Run(failure, func(t *testing.T) {
			h := newHost()
			h.failOn = failure
			if _, err := run(h, spec(), false); err == nil {
				t.Fatal("a failing tool succeeded")
			}
			if len(h.mounts) != 0 || len(h.loops) != 0 || h.files[dataImage].Exists || h.files[dataMount].Exists {
				t.Fatalf("leftovers: mounts=%v loops=%v files=%v", h.mounts, h.loops, h.files)
			}
		})
	}
}

func TestAFailedVerificationUndoesTheMountAndTheNewImage(t *testing.T) {
	h := newHost()
	h.verifyErr = errors.New("prjquota is off")
	_, err := run(h, spec(), false)
	if err == nil || !strings.Contains(err.Error(), "data-root checks refused") || len(h.mounts) != 0 || len(h.loops) != 0 || h.files[dataImage].Exists || h.files[dataMount].Exists {
		t.Fatalf("not rolled back: %v mounts=%v loops=%v", err, h.mounts, h.loops)
	}
}

func TestAFailedVerificationNeverUnmountsAnExistingProvisionedVolume(t *testing.T) {
	h := newHost()
	if _, err := run(h, spec(), false); err != nil {
		t.Fatal(err)
	}
	h.log, h.verifyErr = nil, errors.New("prjquota is off")
	if _, err := run(h, spec(), false); err == nil {
		t.Fatal("verification failure was ignored")
	}
	if len(h.mutations()) != 0 || len(h.mounts) != 2 {
		t.Fatalf("an existing mount was touched: %v", h.log)
	}
}

func TestCheckpointFailureKeepsTheCompleteDataVolumeAndSaysSo(t *testing.T) {
	h := newHost()
	h.env()
	env := h.env()
	calls := 0
	env.Verify = func(_ context.Context, dir string) (VerifyResult, error) {
		calls++
		if calls == 2 {
			return VerifyResult{}, errors.New("no quota")
		}
		return VerifyResult{UsableBytes: usableSize}, nil
	}
	var out bytes.Buffer
	if err := Run(context.Background(), env, spec(), false, &out); err == nil {
		t.Fatal("expected failure")
	}
	if len(h.mounts) != 1 || h.mounts[0].Point != dataMount || !strings.Contains(out.String(), "the data volume is complete") {
		t.Fatalf("data volume not kept: %v\n%s", h.mounts, out.String())
	}
}

func TestMountInfoParsing(t *testing.T) {
	table := `36 25 7:3 / /srv/portal/mnt\040data rw,nosuid,nodev,noexec,relatime shared:1 - xfs /dev/loop3 rw,attr2,inode64,prjquota
37 25 8:1 / / rw,relatime - ext4 /dev/sda1 rw,errors=remount-ro
`
	mounts, err := ParseMountInfo(strings.NewReader(table))
	if err != nil || len(mounts) != 2 {
		t.Fatal(err, mounts)
	}
	m := mounts[0]
	if m.Point != "/srv/portal/mnt data" || m.FSType != "xfs" || m.Source != "/dev/loop3" || !m.Options["prjquota"] || !m.Options["noexec"] || !m.Options["nodev"] {
		t.Fatalf("%+v", m)
	}
	if _, err := ParseMountInfo(strings.NewReader("garbage line")); err == nil {
		t.Fatal("a malformed table was accepted")
	}
}

func TestUnitNameMatchesSystemdEscape(t *testing.T) {
	cases := map[string]string{
		"/srv/portal/mnt-data":     `srv-portal-mnt\x2ddata.mount`,
		"/data/portal-runner/data": `data-portal\x2drunner-data.mount`,
		"/mnt/a_b.c":               "mnt-a_b.c.mount",
		"/":                        "-.mount",
	}
	for path, want := range cases {
		if got := UnitName(path); got != want {
			t.Fatalf("UnitName(%q) = %q, want %q", path, got, want)
		}
	}
}

func TestRecommendedDiskBytes(t *testing.T) {
	if got := RecommendedDiskBytes(usableSize, usableSize); got != 7<<30 {
		t.Fatalf("8 GiB images recommend %d", got)
	}
	// A small checkpoint volume limits the budget: it must hold two thirds of it.
	if got := RecommendedDiskBytes(100<<30, 6<<30); got != 9<<30 {
		t.Fatalf("checkpoint-limited budget %d", got)
	}
	if got := RecommendedDiskBytes(1<<20, 1<<30); got != 0 {
		t.Fatal(got)
	}
}
