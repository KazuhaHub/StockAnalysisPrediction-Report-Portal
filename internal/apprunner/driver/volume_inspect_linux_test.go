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
		"bind device is the namespace":         {`{"type":"none","o":"bind","device":"` + f.data + `"}`, ErrContainerConflict},
		"bind device is an ancestor":           {`{"type":"none","o":"bind","device":"` + filepath.Dir(f.data) + `"}`, ErrContainerConflict},
		"bind device is below the namespace":   {`{"type":"none","o":"bind","device":"` + filepath.Join(f.data, "sub") + `"}`, ErrContainerConflict},
		"rbind with other options":             {`{"type":"none","o":"ro,rbind","device":"` + f.data + `"}`, ErrContainerConflict},
		"no type":                              {`{"o":"bind","device":"` + f.data + `"}`, ErrContainerConflict},
		"bind device elsewhere":                {`{"type":"none","o":"bind","device":"` + filepath.Join(f.root, "data", "ns-b") + `"}`, nil},
		"bind device absent elsewhere":         {`{"type":"none","o":"bind","device":"` + filepath.Join(f.root, "nowhere") + `"}`, nil},
		"not a bind (tmpfs options)":           {`{"type":"tmpfs","device":"tmpfs","o":"size=64m"}`, nil},
		"not a bind (size only)":               {`{"size":"10g"}`, nil},
		"no options (null)":                    {`null`, nil},
		"no options (empty object)":            {`{}`, nil},
		"not a bind but names the namespace":   {`{"type":"none","o":"uid=1","device":"` + f.data + `"}`, nil},
		"bind without a device":                {`{"type":"none","o":"bind"}`, ErrInvalidResponse},
		"bind with an empty device":            {`{"type":"none","o":"bind","device":""}`, ErrInvalidResponse},
		"bind with a relative device":          {`{"type":"none","o":"bind","device":"relative/dir"}`, ErrInvalidResponse},
		"bind with another filesystem type":    {`{"type":"nfs","o":"bind","device":"` + f.data + `"}`, ErrInvalidResponse},
		"options is an array":                  {`["bind"]`, ErrInvalidResponse},
		"an option value that is not a string": {`{"o":"bind","device":5}`, ErrInvalidResponse},
		"duplicate option keys":                {`{"o":"bind","o":"x","device":"` + f.data + `"}`, ErrInvalidResponse},
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
