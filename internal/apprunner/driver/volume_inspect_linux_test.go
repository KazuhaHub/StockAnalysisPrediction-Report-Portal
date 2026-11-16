//go:build linux

package driver

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Exactly one volume route opens: a read of a volume by name. Every other verb and every other volume path stays
// closed, so the runner can never create, remove, list or prune a volume.
func TestOnlyVolumeInspectIsAllowed(t *testing.T) {
	for _, name := range []string{"v", "my-volume_1.x", strings.Repeat("a", 64)} {
		if !dockerRequestAllowed("GET", "/v1.47/volumes/"+name) {
			t.Fatalf("a volume inspect for %q is refused", name)
		}
	}
	for _, bad := range []struct{ method, path string }{
		{"POST", "/v1.47/volumes/create"}, {"POST", "/v1.47/volumes/v"}, {"PUT", "/v1.47/volumes/v"},
		{"DELETE", "/v1.47/volumes/v"}, {"POST", "/v1.47/volumes/prune"}, {"DELETE", "/v1.47/volumes/v?force=1"},
		{"GET", "/v1.47/volumes"}, {"GET", "/v1.47/volumes/"}, {"GET", "/v1.47/volumes/v/extra"},
		{"GET", "/v1.47/volumes/a%2Fb"}, {"GET", "/v1.47/volumes/../containers/json"}, {"GET", "/v1.47/volumes/-v"},
		{"GET", "/v1.46/volumes/v"}, {"GET", "/volumes/v"}, {"HEAD", "/v1.47/volumes/v"}, {"POST", "/v1.47/volumes"},
		{"GET", "/v1.47/volumes/" + strings.Repeat("a", 300)},
	} {
		if dockerRequestAllowed(bad.method, bad.path) {
			t.Fatalf("a volume request outside the read of one volume is allowed: %s %s", bad.method, bad.path)
		}
	}
}

// inspectFixture serves a container listing and a volume inspect from fixed bodies.
type inspectFixture struct {
	t       *testing.T
	data    string // the namespace
	root    string
	volumes map[string]string // name -> body; a value starting with "ERR:" is the status code
	asked   []string
}

func newInspectFixture(t *testing.T) *inspectFixture {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f := &inspectFixture{t: t, root: root, data: filepath.Join(root, "data", "ns-a"), volumes: map[string]string{}}
	if err := os.MkdirAll(f.data, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "data", "ns-b"), 0700); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *inspectFixture) listing(name, driver string) string {
	// The daemon's own directory for the volume: a path that names no overlap.
	return `[{"Id":"` + strings.Repeat("9", 64) + `","Mounts":[{"Type":"volume","Name":"` + name + `","Driver":"` + driver + `","Source":"` + filepath.Join(f.root, "docker", name, "_data") + `","Destination":"/v"}]}]`
}

func (f *inspectFixture) check(listing string) error {
	client := daemonClient(transportFunc(func(r *http.Request) (*http.Response, error) {
		f.asked = append(f.asked, r.Method+" "+r.URL.Path)
		if strings.HasSuffix(r.URL.Path, "/containers/json") {
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(listing))}, nil
		}
		name := strings.TrimPrefix(r.URL.Path, "/v"+APIVersion+"/volumes/")
		body, ok := f.volumes[name]
		if !ok {
			return &http.Response{StatusCode: 404, Body: io.NopCloser(strings.NewReader("{}"))}, nil
		}
		if strings.HasPrefix(body, "ERR:") {
			return &http.Response{StatusCode: 500, Body: io.NopCloser(strings.NewReader("{}"))}, nil
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}, nil
	}))
	return requireNoContainerMountReferences(context.Background(), client, f.data)
}

func volumeBody(options string) string {
	return `{"Name":"v","Driver":"local","Mountpoint":"/x","Options":` + options + `}`
}

func TestBindBackedLocalVolumeIsJudgedByItsDevice(t *testing.T) {
	f := newInspectFixture(t)
	for name, tc := range map[string]struct {
		options string
		want    error // nil means no conflict
	}{
		"bind device is the namespace":              {`{"type":"none","o":"bind","device":"` + f.data + `"}`, ErrContainerConflict},
		"bind device is an ancestor":                {`{"type":"none","o":"bind","device":"` + filepath.Dir(f.data) + `"}`, ErrContainerConflict},
		"bind device is below the namespace":        {`{"type":"none","o":"bind","device":"` + filepath.Join(f.data, "sub") + `"}`, ErrContainerConflict},
		"rbind with other options":                  {`{"type":"none","o":"ro,rbind","device":"` + f.data + `"}`, ErrContainerConflict},
		"no type":                                   {`{"o":"bind","device":"` + f.data + `"}`, ErrContainerConflict},
		"bind device elsewhere":                     {`{"type":"none","o":"bind","device":"` + filepath.Join(f.root, "data", "ns-b") + `"}`, nil},
		"bind device absent elsewhere":              {`{"type":"none","o":"bind","device":"` + filepath.Join(f.root, "nowhere") + `"}`, nil},
		"not a bind (tmpfs options)":                {`{"type":"tmpfs","device":"tmpfs","o":"size=64m"}`, nil},
		"not a bind (size only)":                    {`{"size":"10g"}`, nil},
		"no options (null)":                         {`null`, nil},
		"no options (empty object)":                 {`{}`, nil},
		"not a bind but names the namespace":        {`{"type":"none","o":"uid=1","device":"` + f.data + `"}`, ErrContainerConflict},
		"overlay with an upperdir in the namespace": {`{"type":"overlay","device":"overlay","o":"lowerdir=/a,upperdir=` + f.data + `/up,workdir=` + f.data + `/w"}`, ErrContainerConflict},
		"overlay with directories elsewhere":        {`{"type":"overlay","device":"overlay","o":"lowerdir=/a,upperdir=/b,workdir=/c"}`, ErrContainerConflict},
		"overlay with relative directories":         {`{"type":"overlay","device":"overlay","o":"lowerdir=a,upperdir=b,workdir=c"}`, ErrContainerConflict},
		"overlay with only an upperdir in o":        {`{"type":"overlay","o":"rw,upperdir=u"}`, ErrContainerConflict},
		"an upperdir key":                           {`{"type":"overlay","upperdir":"` + f.data + `"}`, ErrContainerConflict},
		"a device by label":                         {`{"type":"ext4","device":"LABEL=data"}`, ErrContainerConflict},
		"an unknown option whose value is a path":   {`{"somekey":"/srv/x"}`, ErrContainerConflict},
		"an unknown option with a relative path":    {`{"somekey":"a/b"}`, ErrContainerConflict},
		"a plain volume with an unrelated option":   {`{"somekey":"on"}`, nil},
		"bind without a device":                     {`{"type":"none","o":"bind"}`, ErrInvalidResponse},
		"bind with an empty device":                 {`{"type":"none","o":"bind","device":""}`, ErrInvalidResponse},
		"bind with a relative device":               {`{"type":"none","o":"bind","device":"relative/dir"}`, ErrInvalidResponse},
		"bind with another filesystem type":         {`{"type":"nfs","o":"bind","device":"` + f.data + `"}`, ErrInvalidResponse},
		"options is an array":                       {`["bind"]`, ErrInvalidResponse},
		"an option value that is not a string":      {`{"o":"bind","device":5}`, ErrInvalidResponse},
		"duplicate option keys":                     {`{"o":"bind","o":"x","device":"` + f.data + `"}`, ErrInvalidResponse},
	} {
		t.Run(name, func(t *testing.T) {
			f.volumes["v"] = volumeBody(tc.options)
			err := f.check(f.listing("v", "local"))
			if tc.want == nil && err != nil || tc.want != nil && !errors.Is(err, tc.want) {
				t.Fatalf("want %v, got %v", tc.want, err)
			}
		})
	}
}

func TestBindBackedVolumeWithAbsentDeviceUnderTheNamespaceConflicts(t *testing.T) {
	f := newInspectFixture(t)
	f.volumes["v"] = volumeBody(`{"type":"none","o":"bind","device":"` + filepath.Join(f.data, "not-yet") + `"}`)
	if err := f.check(f.listing("v", "local")); !errors.Is(err, ErrContainerConflict) {
		t.Fatal("an absent device under the namespace did not conflict", err)
	}
	// A symlink device is followed to where it lands.
	link := filepath.Join(f.root, "device-link")
	if err := os.Symlink(filepath.Join(f.data, "later"), link); err != nil {
		t.Fatal(err)
	}
	f.volumes["v"] = volumeBody(`{"type":"none","o":"bind","device":"` + link + `"}`)
	if err := f.check(f.listing("v", "local")); !errors.Is(err, ErrContainerConflict) {
		t.Fatal("a dangling symlink device into the namespace did not conflict", err)
	}
}

func TestVolumeInspectFailuresFailClosed(t *testing.T) {
	f := newInspectFixture(t)
	f.volumes["v"] = "ERR:"
	if err := f.check(f.listing("v", "local")); !errors.Is(err, ErrUnavailable) {
		t.Fatal("a failed inspect passed", err)
	}
	delete(f.volumes, "v") // the daemon answers 404
	if err := f.check(f.listing("v", "local")); !errors.Is(err, ErrUnavailable) {
		t.Fatal("a volume that cannot be inspected passed", err)
	}
	f.volumes["v"] = `not json`
	if err := f.check(f.listing("v", "local")); err == nil {
		t.Fatal("a garbled inspect passed")
	}
	f.volumes["v"] = `{"Name":"v","Driver":"other","Options":null}`
	if err := f.check(f.listing("v", "local")); !errors.Is(err, ErrInvalidResponse) {
		t.Fatal("an inspect that disagrees about the driver passed", err)
	}
	// A local volume whose mount names no volume cannot be inspected.
	listing := strings.Replace(f.listing("v", "local"), `"Name":"v",`, "", 1)
	if err := f.check(listing); !errors.Is(err, ErrInvalidResponse) {
		t.Fatal("a local volume mount without a name passed", err)
	}
	for _, name := range []string{"-bad", "a/b", ""} {
		if err := f.check(f.listing(name, "local")); !errors.Is(err, ErrInvalidResponse) {
			t.Fatalf("a volume named %q passed: %v", name, err)
		}
	}
}

func TestOnlyLocalVolumesAreInspected(t *testing.T) {
	f := newInspectFixture(t)
	f.volumes["v"] = volumeBody(`{"o":"bind","device":"` + f.data + `"}`)
	// A plugin volume is resolved through the container inspect as before, never by a volume inspect.
	listing := `[{"Id":"` + strings.Repeat("9", 64) + `","Mounts":[{"Type":"volume","Name":"v","Driver":"rexray","Source":"` + filepath.Join(f.root, "x") + `","Destination":"/v"}]}]`
	if err := f.check(listing); err != nil {
		t.Fatal(err)
	}
	for _, asked := range f.asked {
		if strings.Contains(asked, "/volumes/") {
			t.Fatal("a plugin volume was inspected as a local one:", asked)
		}
	}
	// Bind and tmpfs mounts are never volume inspects either.
	f.asked = nil
	if err := f.check(`[{"Id":"` + strings.Repeat("9", 64) + `","Mounts":[{"Type":"bind","Source":"` + f.root + `/data/ns-b"},{"Type":"tmpfs"}]}]`); err != nil {
		t.Fatal(err)
	}
	for _, asked := range f.asked {
		if strings.Contains(asked, "/volumes/") {
			t.Fatal("a bind or tmpfs mount was inspected as a volume:", asked)
		}
	}
}

func TestLocalVolumeWithAPlainDirectoryKeepsToday(t *testing.T) {
	f := newInspectFixture(t)
	f.volumes["v"] = volumeBody(`null`)
	// The daemon's own directory for a plain volume overlapping the namespace by name still conflicts.
	listing := `[{"Id":"` + strings.Repeat("9", 64) + `","Mounts":[{"Type":"volume","Name":"v","Driver":"local","Source":"` + f.data + `","Destination":"/v"}]}]`
	if err := f.check(listing); !errors.Is(err, ErrContainerConflict) {
		t.Fatal(err)
	}
	if err := f.check(f.listing("v", "local")); err != nil {
		t.Fatal(err)
	}
}

// Each volume is inspected once per pass however many containers mount it.
func TestVolumeInspectsAreDedupedWithinAPass(t *testing.T) {
	f := newInspectFixture(t)
	f.volumes["v"] = volumeBody(`null`)
	mount := `{"Type":"volume","Name":"v","Driver":"local","Source":"` + filepath.Join(f.root, "docker", "v", "_data") + `","Destination":"/v"}`
	var entries []string
	for i := 0; i < 5; i++ {
		entries = append(entries, `{"Id":"`+strings.Repeat(string(rune('a'+i)), 64)+`","Mounts":[`+mount+`,`+mount+`]}`)
	}
	if err := f.check("[" + strings.Join(entries, ",") + "]"); err != nil {
		t.Fatal(err)
	}
	reads := 0
	for _, asked := range f.asked {
		if strings.Contains(asked, "/volumes/") {
			reads++
		}
	}
	if reads != 1 {
		t.Fatalf("%d volume inspects for one volume", reads)
	}
}

// fakeMounts gives the namespace the device 8:1 and an xfs filesystem, as the data root has.
func fakeMountsFor(t *testing.T, f *inspectFixture, nsFSType string) {
	fakeMountsOn(t, f, "8:1", nsFSType)
}

func fakeMountsOn(t *testing.T, f *inspectFixture, dev, nsFSType string) {
	t.Helper()
	file := filepath.Join(t.TempDir(), "mountinfo")
	body := "1 0 " + dev + " / / rw - " + nsFSType + " /dev/vda1 rw\n"
	if err := os.WriteFile(file, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	old := candidateIPCMountInfoPath
	candidateIPCMountInfoPath = file
	t.Cleanup(func() { candidateIPCMountInfoPath = old })
}

func fakeBlockDevice(t *testing.T, devices map[string]string) {
	t.Helper()
	old := candidateIPCBlockDevice
	candidateIPCBlockDevice = func(path string) (string, error) {
		if dev, ok := devices[path]; ok {
			return dev, nil
		}
		return "", os.ErrNotExist
	}
	t.Cleanup(func() { candidateIPCBlockDevice = old })
}

// fakeNetwork stands in for DNS and the host's interfaces: the host has 10.1.1.1 and fe80::1 and the names
// resolve as the table says; anything else is unresolvable.
func fakeNetwork(t *testing.T, names map[string][]string) {
	t.Helper()
	oldLookup, oldLocal := candidateIPCLookupHost, candidateIPCLocalAddresses
	candidateIPCLookupHost = func(_ context.Context, host string) ([]net.IP, error) {
		if ip := net.ParseIP(host); ip != nil {
			return []net.IP{ip}, nil
		}
		addrs, ok := names[host]
		if !ok {
			return nil, errors.New("no such host")
		}
		var out []net.IP
		for _, a := range addrs {
			out = append(out, net.ParseIP(a))
		}
		return out, nil
	}
	candidateIPCLocalAddresses = func() ([]net.IP, error) { return []net.IP{net.ParseIP("10.1.1.1"), net.ParseIP("fe80::1")}, nil }
	t.Cleanup(func() { candidateIPCLookupHost, candidateIPCLocalAddresses = oldLookup, oldLocal })
}

// Network filesystem volumes on another host do not refuse an app; they overlap only a namespace that itself
// sits on a network filesystem. One whose server is this host (loopback, one of its addresses, a name that
// resolves to one) exports local data back as a volume and is refused, as is one whose server cannot be told.
func TestNetworkFilesystemVolumesDoNotRefuseEveryApp(t *testing.T) {
	f := newInspectFixture(t)
	fakeNetwork(t, map[string][]string{"nas.example": {"203.0.113.9"}, "me.example": {"10.1.1.1"}, "both.example": {"203.0.113.9", "10.1.1.1"}, "localhost": {"127.0.0.1", "::1"}})
	for name, tc := range map[string]struct {
		options string
		want    error
	}{
		"nfs on a remote address":              {`{"type":"nfs","o":"addr=198.51.100.7,rw","device":":/export/data"}`, nil},
		"nfs4 on a remote host in the device":  {`{"type":"nfs4","o":"vers=4","device":"198.51.100.7:/export"}`, nil},
		"nfs by a leading colon":               {`{"o":"addr=198.51.100.7","device":":/export/data"}`, nil},
		"cifs by a double slash and a name":    {`{"type":"cifs","o":"username=u","device":"//nas.example/share"}`, nil},
		"smb3 with an ip option":               {`{"type":"smb3","o":"ip=198.51.100.7","device":"//nas.example/share/sub"}`, nil},
		"nfs on a remote IPv6 address":         {`{"type":"nfs","o":"addr=2001:db8::5","device":":/export"}`, nil},
		"bracketed IPv6 in the device":         {`{"type":"nfs","device":"[2001:db8::5]:/export"}`, nil},
		"nfs on loopback":                      {`{"type":"nfs","o":"addr=127.0.0.1","device":":/data"}`, ErrContainerConflict},
		"nfs on the IPv6 loopback":             {`{"type":"nfs","o":"addr=::1","device":":/data"}`, ErrContainerConflict},
		"nfs on the unspecified address":       {`{"type":"nfs","o":"addr=0.0.0.0","device":":/data"}`, ErrContainerConflict},
		"nfs on a local interface address":     {`{"type":"nfs","o":"addr=10.1.1.1","device":":/data"}`, ErrContainerConflict},
		"nfs on a local IPv6 address":          {`{"type":"nfs","o":"addr=fe80::1","device":":/data"}`, ErrContainerConflict},
		"nfs on a local address in the device": {`{"type":"nfs","device":"10.1.1.1:/data"}`, ErrContainerConflict},
		"cifs to a name that resolves locally": {`{"type":"cifs","device":"//me.example/share"}`, ErrContainerConflict},
		"cifs to localhost":                    {`{"type":"cifs","device":"//localhost/share"}`, ErrContainerConflict},
		"a name with one local address":        {`{"type":"cifs","device":"//both.example/share"}`, ErrContainerConflict},
		"an unresolvable name":                 {`{"type":"cifs","device":"//nowhere.example/share"}`, ErrUnavailable},
		"no server address at all":             {`{"type":"nfs","device":":/export"}`, ErrContainerConflict},
		"an ambiguous bare IPv6 device":        {`{"type":"nfs","device":"2001:db8::5:/export"}`, ErrContainerConflict},
		"a remote option but a local device":   {`{"type":"nfs","o":"addr=198.51.100.7","device":"127.0.0.1:/data"}`, ErrContainerConflict},
	} {
		t.Run(name, func(t *testing.T) {
			f.volumes["v"] = volumeBody(tc.options)
			fakeMountsFor(t, f, "xfs")
			err := f.check(f.listing("v", "local"))
			if tc.want == nil && err != nil || tc.want != nil && !errors.Is(err, tc.want) {
				t.Fatalf("want %v, got %v", tc.want, err)
			}
			if tc.want == nil {
				fakeMountsFor(t, f, "nfs4")
				if err := f.check(f.listing("v", "local")); !errors.Is(err, ErrContainerConflict) {
					t.Fatalf("a remote network volume beside a namespace on a network filesystem passed: %v", err)
				}
			}
		})
	}
}

// sysfsTree builds a synthetic /sys/dev/block: devices are directories with a dev file, a partition sits in its
// disk's directory with a partition file, slaves are symlinks, and a loop device has loop/backing_file.
type sysfsTree struct {
	t    *testing.T
	root string
}

func newSysfs(t *testing.T) *sysfsTree {
	t.Helper()
	root, _ := filepath.EvalSymlinks(t.TempDir())
	if err := os.MkdirAll(filepath.Join(root, "dev", "block"), 0755); err != nil {
		t.Fatal(err)
	}
	old := candidateIPCSysDevBlock
	candidateIPCSysDevBlock = filepath.Join(root, "dev", "block")
	t.Cleanup(func() { candidateIPCSysDevBlock = old })
	return &sysfsTree{t: t, root: root}
}

func (s *sysfsTree) dir(path string, majmin string) string {
	s.t.Helper()
	full := filepath.Join(s.root, "devices", path)
	if err := os.MkdirAll(full, 0755); err != nil {
		s.t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(full, "dev"), []byte(majmin+"\n"), 0644); err != nil {
		s.t.Fatal(err)
	}
	if err := os.Symlink(full, filepath.Join(s.root, "dev", "block", majmin)); err != nil {
		s.t.Fatal(err)
	}
	return full
}

func (s *sysfsTree) disk(name, majmin string) string { return s.dir(name, majmin) }

func (s *sysfsTree) partition(disk, name, majmin string) string {
	d := s.dir(filepath.Join(disk, name), majmin)
	if err := os.WriteFile(filepath.Join(d, "partition"), []byte("1\n"), 0644); err != nil {
		s.t.Fatal(err)
	}
	return d
}

func (s *sysfsTree) slave(dm, under string) {
	s.t.Helper()
	slaves := filepath.Join(dm, "slaves")
	os.MkdirAll(slaves, 0755)
	if err := os.Symlink(under, filepath.Join(slaves, filepath.Base(under))); err != nil {
		s.t.Fatal(err)
	}
}

func (s *sysfsTree) loop(dev, backing string) {
	s.t.Helper()
	os.MkdirAll(filepath.Join(dev, "loop"), 0755)
	if err := os.WriteFile(filepath.Join(dev, "loop", "backing_file"), []byte(backing+"\n"), 0644); err != nil {
		s.t.Fatal(err)
	}
}

// A block device volume overlaps a namespace only when their ancestry through sysfs shares a device: the same
// node, a slave chain (multipath, device-mapper, LV "-real"), a whole disk and its partition, or a loop device's
// backing file inside the namespace. Two partitions of one disk do not overlap each other.
func TestBlockDeviceVolumesAreJudgedByAncestry(t *testing.T) {
	f := newInspectFixture(t)
	sys := newSysfs(t)
	sda := sys.disk("sda", "8:0")
	sda1 := sys.partition("sda", "sda1", "8:1")
	sys.partition("sda", "sda2", "8:2")
	sdb := sys.disk("sdb", "8:16")
	sdc := sys.disk("sdc", "8:32")
	sys.disk("sdd", "8:48")
	sys.partition("sdb", "sdb1", "8:17")
	mpath := sys.dir("dm-1", "253:1") // multipath over sdb and sdc
	sys.slave(mpath, sdb)
	sys.slave(mpath, sdc)
	dm0 := sys.dir("dm-0", "253:0") // device-mapper over the partition sda1
	sys.slave(dm0, sda1)
	lvReal := sys.dir("dm-4", "253:4") // the "-real" device under an LV, over a partition of sdb
	sys.slave(lvReal, filepath.Join(sdb, "sdb1"))
	lv := sys.dir("dm-3", "253:3")
	sys.slave(lv, lvReal)
	loop0 := sys.dir("loop0", "7:0")
	sys.loop(loop0, filepath.Join(f.data, "disk.img"))
	loop1 := sys.dir("loop1", "7:1")
	sys.loop(loop1, filepath.Join(f.root, "elsewhere.img"))
	_ = sda
	fakeBlockDevice(t, map[string]string{
		"/dev/sda": "8:0", "/dev/sda1": "8:1", "/dev/sda2": "8:2", "/dev/sdb": "8:16", "/dev/sdb1": "8:17", "/dev/sdd": "8:48",
		"/dev/mapper/mpatha": "253:1", "/dev/mapper/vg-lv-real": "253:4", "/dev/loop0": "7:0", "/dev/loop1": "7:1",
	})
	for name, tc := range map[string]struct {
		nsDev, device string
		want          error
	}{
		"the same device":                            {"8:1", "/dev/sda1", ErrContainerConflict},
		"an unrelated disk":                          {"8:1", "/dev/sdd", nil},
		"the namespace's partition and a sibling":    {"8:1", "/dev/sda2", nil},
		"the whole disk of the namespace partition":  {"8:1", "/dev/sda", ErrContainerConflict},
		"a partition of the namespace's whole disk":  {"8:0", "/dev/sda2", ErrContainerConflict},
		"a multipath alias of a path":                {"253:1", "/dev/sdb", ErrContainerConflict},
		"the other path of the multipath":            {"8:32", "/dev/mapper/mpatha", ErrContainerConflict},
		"dm over the partition, volume on it":        {"253:0", "/dev/sda1", ErrContainerConflict},
		"dm over the partition, volume on the disk":  {"253:0", "/dev/sda", ErrContainerConflict},
		"dm over the partition, volume on a sibling": {"253:0", "/dev/sda2", nil},
		"an LV and its -real device":                 {"253:3", "/dev/mapper/vg-lv-real", ErrContainerConflict},
		"an LV and the partition under -real":        {"253:3", "/dev/sdb1", ErrContainerConflict},
		"a namespace unrelated to the LV chain":      {"8:48", "/dev/mapper/vg-lv-real", nil},
		"a loop device backed by a file in the data": {"8:48", "/dev/loop0", ErrContainerConflict},
		"a loop device backed by a file elsewhere":   {"8:48", "/dev/loop1", nil},
	} {
		t.Run(name, func(t *testing.T) {
			fakeMountsOn(t, f, tc.nsDev, "xfs")
			f.volumes["v"] = volumeBody(`{"type":"ext4","device":"` + tc.device + `"}`)
			err := f.check(f.listing("v", "local"))
			if tc.want == nil && err != nil || tc.want != nil && !errors.Is(err, tc.want) {
				t.Fatalf("want %v, got %v", tc.want, err)
			}
		})
	}
	_ = loop0
	_ = loop1
}

func TestBlockDeviceAncestryFailsClosed(t *testing.T) {
	f := newInspectFixture(t)
	sys := newSysfs(t)
	sys.disk("sda", "8:0")
	broken := sys.dir("dm-9", "253:9")
	os.MkdirAll(filepath.Join(broken, "slaves"), 0755)
	os.Symlink(filepath.Join(sys.root, "devices", "vanished"), filepath.Join(broken, "slaves", "vanished"))
	fakeBlockDevice(t, map[string]string{"/dev/sda": "8:0", "/dev/dm9": "253:9", "/dev/ghost": "9:9"})
	fakeMountsOn(t, f, "8:0", "xfs")
	for name, tc := range map[string]struct{ device string }{
		"a device with no sysfs entry": {"/dev/ghost"},
		"a slave that cannot be read":  {"/dev/dm9"},
	} {
		t.Run(name, func(t *testing.T) {
			f.volumes["v"] = volumeBody(`{"type":"ext4","device":"` + tc.device + `"}`)
			if err := f.check(f.listing("v", "local")); !errors.Is(err, ErrUnavailable) {
				t.Fatalf("an ancestry that could not be completed passed: %v", err)
			}
		})
	}
	// The namespace's own device with no sysfs entry, and an unreadable sysfs.
	fakeMountsOn(t, f, "9:8", "xfs")
	f.volumes["v"] = volumeBody(`{"type":"ext4","device":"/dev/sda"}`)
	if err := f.check(f.listing("v", "local")); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("a namespace device with no sysfs entry passed: %v", err)
	}
	candidateIPCSysDevBlock = filepath.Join(t.TempDir(), "missing")
	fakeMountsOn(t, f, "8:0", "xfs")
	if err := f.check(f.listing("v", "local")); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("an unreadable sysfs passed: %v", err)
	}
	// A ring of slaves is a refusal, not a loop.
	sys2 := newSysfs(t)
	a := sys2.dir("dm-1", "253:1")
	b := sys2.dir("dm-2", "253:2")
	sys2.slave(a, b)
	sys2.slave(b, a)
	fakeBlockDevice(t, map[string]string{"/dev/dm1": "253:1"})
	fakeMountsOn(t, f, "253:2", "xfs")
	f.volumes["v"] = volumeBody(`{"type":"ext4","device":"/dev/dm1"}`)
	if err := f.check(f.listing("v", "local")); err != nil && !errors.Is(err, ErrUnavailable) && !errors.Is(err, ErrContainerConflict) {
		t.Fatal(err)
	}
}

func TestRealBlockDeviceStat(t *testing.T) {
	if _, err := candidateIPCBlockDevice("/definitely/not/a/device"); err == nil {
		t.Fatal("a missing device was statted")
	}
	if _, err := candidateIPCBlockDevice(os.TempDir()); err == nil {
		t.Fatal("a directory was taken for a block device")
	}
}

func TestMountInfoCarriesTheFilesystemType(t *testing.T) {
	mounts, err := parseCandidateIPCMountInfo([]byte("36 35 98:0 / /mnt rw,noatime master:1 - nfs4 host:/x rw\n1 0 8:1 / / rw - xfs /dev/vda1 rw\n"))
	if err != nil {
		t.Fatal(err)
	}
	place, err := mounts.place("/mnt/data")
	if err != nil || place.fstype != "nfs4" {
		t.Fatal(place, err)
	}
	if _, err := parseCandidateIPCMountInfo([]byte("1 0 8:1 / / rw shared:1 ext4 /dev/vda1 rw\n")); err == nil {
		t.Fatal("a line without the separator was accepted")
	}
}
