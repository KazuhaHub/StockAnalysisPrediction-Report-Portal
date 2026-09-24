package app

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// apps/index.json is not a fixture. Every portal left on the default market reads it, and the
// bundles it names, straight from main (defaultAppMarketIndexURL), so a change to it reaches
// installed portals the moment it merges, with no release in between. The other market tests
// serve synthetic JSON; this one holds the published files to the install path they will meet:
// the index must decode, each entry must resolve to a bundle committed next to it, the bundle
// must pass parseAppBundle, and what the market shows before install (id, version, and the
// scopes an admin is asked to approve) must be what the bundle's manifest declares. The bundle
// is rebuilt by hand (apps/README.md), so it must also still be exactly apps/<id>/.
func TestPublishedAppMarketMatchesItsBundles(t *testing.T) {
	const appsDir = "../../apps"
	raw, err := os.ReadFile(filepath.Join(appsDir, "index.json"))
	if err != nil {
		t.Fatal(err)
	}
	// Stricter than fetchAppMarket on purpose: a misspelt key there is silently dropped, and the
	// market then lists an app with no scopes or no bundle.
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var idx struct {
		Apps []appMarketEntry `json:"apps"`
	}
	if err := dec.Decode(&idx); err != nil {
		t.Fatalf("apps/index.json: %v", err)
	}
	if len(idx.Apps) == 0 {
		t.Fatal("apps/index.json lists no apps")
	}
	base, err := url.Parse(defaultAppMarketIndexURL)
	if err != nil {
		t.Fatal(err)
	}
	dirURL := base.ResolveReference(&url.URL{Path: "."}).String()
	seen := map[string]bool{}
	for _, e := range idx.Apps {
		t.Run(e.ID, func(t *testing.T) {
			if seen[e.ID] {
				t.Fatalf("id %q is listed twice; install picks the first", e.ID)
			}
			seen[e.ID] = true
			if e.URL != "" {
				t.Fatalf("url %q points outside this repository, so nothing here can check it", e.URL)
			}
			// Resolve the path the way apiAppMarketInstall does, against the index's own URL.
			ref, err := url.Parse(e.Path)
			if err != nil || e.Path == "" {
				t.Fatalf("path %q: %v", e.Path, err)
			}
			rel, ok := strings.CutPrefix(base.ResolveReference(ref).String(), dirURL)
			if !ok || rel == "" {
				t.Fatalf("path %q resolves outside apps/", e.Path)
			}
			bundle, err := os.ReadFile(filepath.Join(appsDir, filepath.FromSlash(rel)))
			if err != nil {
				t.Fatalf("bundle is not committed: %v", err)
			}
			if len(bundle) > maxBundleUpload {
				t.Fatalf("bundle is %d bytes; install refuses more than %d", len(bundle), maxBundleUpload)
			}
			app, _, err := parseAppBundle(bundle)
			if err != nil {
				t.Fatalf("install would refuse %s: %v", rel, err)
			}
			if app.ID != e.ID || app.Version != e.Version {
				t.Errorf("index lists %s %s, bundle manifest is %s %s", e.ID, e.Version, app.ID, app.Version)
			}
			if want := splitCSV(strings.Join(e.Scopes, ",")); !slices.Equal(app.Scopes, want) {
				t.Errorf("index asks an admin to approve scopes %v, bundle declares %v", want, app.Scopes)
			}

			zipped := readZipFiles(t, bundle)
			src := readDirFiles(t, filepath.Join(appsDir, app.ID))
			for name, content := range src {
				if got, ok := zipped[name]; !ok {
					t.Errorf("apps/%s/%s is not in %s; rebuild it (apps/README.md)", app.ID, name, rel)
				} else if !bytes.Equal(got, content) {
					t.Errorf("%s in %s differs from apps/%s/%s; rebuild it (apps/README.md)", name, rel, app.ID, name)
				}
			}
			for name := range zipped {
				if _, ok := src[name]; !ok {
					t.Errorf("%s in %s has no source under apps/%s/", name, rel, app.ID)
				}
			}
		})
	}
}

func readZipFiles(t *testing.T, raw []byte) map[string][]byte {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		t.Fatal(err)
	}
	out := map[string][]byte{}
	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		b, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			t.Fatal(err)
		}
		out[f.Name] = b
	}
	return out
}

func readDirFiles(t *testing.T, dir string) map[string][]byte {
	t.Helper()
	out := map[string][]byte{}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, p)
		out[filepath.ToSlash(rel)] = b
		return err
	})
	if err != nil {
		t.Fatalf("app sources: %v", err)
	}
	return out
}
