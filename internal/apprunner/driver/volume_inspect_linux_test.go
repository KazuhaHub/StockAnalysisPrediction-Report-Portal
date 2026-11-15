//go:build linux

package driver

import (
	"context"
	"errors"
	"io"
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
	t.Helper()
	file := filepath.Join(t.TempDir(), "mountinfo")
	body := "1 0 8:1 / / rw - " + nsFSType + " /dev/vda1 rw\n"
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

// Network filesystem volumes elsewhere on the host do not refuse an app; they overlap only a namespace that
// itself sits on a network filesystem.
func TestNetworkFilesystemVolumesDoNotRefuseEveryApp(t *testing.T) {
	f := newInspectFixture(t)
	for name, options := range map[string]string{
		"nfs by type":                 `{"type":"nfs","o":"addr=10.0.0.5,rw","device":":/export/data"}`,
		"nfs4 by type":                `{"type":"nfs4","o":"addr=10.0.0.5","device":"10.0.0.5:/export"}`,
		"nfs by a leading colon":      `{"o":"addr=10.0.0.5","device":":/export/data"}`,
		"cifs by type":                `{"type":"cifs","o":"username=u,password=p","device":"//host/share"}`,
		"smb3 by a double slash":      `{"type":"smb3","device":"//host/share/sub"}`,
		"cifs by a double slash only": `{"device":"//host/share"}`,
	} {
		t.Run(name, func(t *testing.T) {
			f.volumes["v"] = volumeBody(options)
			fakeMountsFor(t, f, "xfs")
			if err := f.check(f.listing("v", "local")); err != nil {
				t.Fatalf("a network volume refused the writer on a local data root: %v", err)
			}
			fakeMountsFor(t, f, "nfs4")
			if err := f.check(f.listing("v", "local")); !errors.Is(err, ErrContainerConflict) {
				t.Fatalf("a network volume beside a namespace on a network filesystem passed: %v", err)
			}
		})
	}
}

// A block device volume overlaps a namespace only when the namespace is on that device (major:minor).
func TestBlockDeviceVolumesAreJudgedByDeviceNumber(t *testing.T) {
	f := newInspectFixture(t)
	fakeMountsFor(t, f, "xfs") // the namespace is on 8:1
	f.volumes["v"] = volumeBody(`{"type":"ext4","device":"/dev/vdb1"}`)
	fakeBlockDevice(t, map[string]string{"/dev/vdb1": "8:17", "/dev/vda1": "8:1"})
	if err := f.check(f.listing("v", "local")); err != nil {
		t.Fatalf("an ext4 device volume elsewhere on the host refused the writer: %v", err)
	}
	f.volumes["v"] = volumeBody(`{"type":"xfs","o":"nouuid","device":"/dev/vda1"}`)
	if err := f.check(f.listing("v", "local")); !errors.Is(err, ErrContainerConflict) {
		t.Fatalf("a volume on the device that holds the namespace passed: %v", err)
	}
	f.volumes["v"] = volumeBody(`{"type":"ext4","device":"/dev/missing"}`)
	if err := f.check(f.listing("v", "local")); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("an unstattable device passed: %v", err)
	}
	// A device path that is not under /dev is not a block device and keeps failing closed.
	f.volumes["v"] = volumeBody(`{"type":"ext4","device":"disk.img"}`)
	if err := f.check(f.listing("v", "local")); !errors.Is(err, ErrContainerConflict) {
		t.Fatalf("a relative device passed: %v", err)
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
