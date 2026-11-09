package driver

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

func hexSum(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

type memorySource map[string][]byte

func (m memorySource) Open(digest string) (io.ReadCloser, error) {
	body, ok := m[digest]
	if !ok {
		return nil, os.ErrNotExist
	}
	return io.NopCloser(bytes.NewReader(body)), nil
}

type loadFixture struct {
	raw      []byte
	parsed   appplatform.ParsedDescriptor
	entry    appplatform.BackendImage
	source   memorySource
	layers   [][]byte
	manifest []byte
	config   []byte
}

// newLoadFixture builds a real two-layer image and the descriptor that binds it.
func newLoadFixture(t *testing.T) loadFixture {
	t.Helper()
	fixture := loadFixture{source: memorySource{}}
	var diffIDs []string
	var layerDescriptors []map[string]any
	var entryLayers []map[string]any
	for _, name := range []string{"etc/one", "etc/two"} {
		var tarball bytes.Buffer
		tw := tar.NewWriter(&tarball)
		_ = tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: 3, Typeflag: tar.TypeReg})
		_, _ = tw.Write([]byte(name[len(name)-3:]))
		_ = tw.Close()
		var gz bytes.Buffer
		zw := gzip.NewWriter(&gz)
		_, _ = zw.Write(tarball.Bytes())
		_ = zw.Close()
		fixture.layers = append(fixture.layers, gz.Bytes())
		fixture.source[hexSum(gz.Bytes())] = gz.Bytes()
		diffIDs = append(diffIDs, "sha256:"+hexSum(tarball.Bytes()))
		layerDescriptors = append(layerDescriptors, map[string]any{"mediaType": appplatform.MediaTypeOCIManifest[:0] + "application/vnd.oci.image.layer.v1.tar+gzip", "digest": "sha256:" + hexSum(gz.Bytes()), "size": gz.Len()})
		entryLayers = append(entryLayers, map[string]any{"diff_id": hexSum(tarball.Bytes()), "uncompressed_bytes": tarball.Len()})
	}
	config, _ := json.Marshal(map[string]any{"architecture": "amd64", "os": "linux", "rootfs": map[string]any{"type": "layers", "diff_ids": diffIDs}})
	manifest, _ := json.Marshal(map[string]any{
		"schemaVersion": 2, "mediaType": appplatform.MediaTypeOCIManifest,
		"config": map[string]any{"mediaType": appplatform.MediaTypeOCIConfig, "digest": "sha256:" + hexSum(config), "size": len(config)},
		"layers": layerDescriptors,
	})
	fixture.config, fixture.manifest = config, manifest
	fixture.source[hexSum(config)], fixture.source[hexSum(manifest)] = config, manifest
	raw, _, _ := managedImageFixture(t)
	var d map[string]any
	if err := json.Unmarshal(raw, &d); err != nil {
		t.Fatal(err)
	}
	backend := d["backend"].(map[string]any)
	delete(backend, "image")
	backend["platforms"] = []any{"linux/amd64"}
	backend["images"] = []any{map[string]any{"platform": "linux/amd64", "manifest_digest": hexSum(manifest), "config_digest": hexSum(config), "layers": entryLayers}}
	encoded, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	fixture.raw = encoded
	fixture.parsed, err = appplatform.ParseDescriptor(encoded)
	if err != nil {
		t.Fatal(err)
	}
	fixture.entry = fixture.parsed.Manifest.Backend.Images[0]
	return fixture
}

func (f loadFixture) request() LoadImageRequest {
	return LoadImageRequest{Descriptor: f.raw, ReleaseSHA256: f.parsed.SHA256, Platform: "linux/amd64", Source: f.source}
}

// fakeDaemon is a small Docker image API. Each scenario overrides what a load does to the image list.
type fakeDaemon struct {
	t         *testing.T
	store     ImageStore
	images    map[string][]string // id -> repo tags
	arch      string
	loads     int
	deletes   []string
	archive   map[string][]byte
	archiveOK bool
	onLoad    func(d *fakeDaemon, expected string) (int, string)
	failPost  error
}

func newFakeDaemon(t *testing.T, store ImageStore) *fakeDaemon {
	return &fakeDaemon{t: t, store: store, images: map[string][]string{"sha256:" + strings.Repeat("e", 64): {"portal:latest"}}, arch: "amd64"}
}

func (d *fakeDaemon) client(f loadFixture) *http.Client {
	expected := ExpectedImageID(d.store, f.entry)
	return daemonClient(transportFunc(func(r *http.Request) (*http.Response, error) {
		path := r.URL.EscapedPath()
		reply := func(status int, body any) (*http.Response, error) {
			var raw []byte
			switch v := body.(type) {
			case string:
				raw = []byte(v)
			default:
				raw, _ = json.Marshal(v)
			}
			return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(raw))}, nil
		}
		switch {
		case r.Method == "GET" && path == "/v1.47/info":
			return reply(200, infoFor(d.store))
		case r.Method == "GET" && path == "/v1.47/images/json":
			var list []map[string]any
			ids := make([]string, 0, len(d.images))
			for id := range d.images {
				ids = append(ids, id)
			}
			sort.Strings(ids)
			for _, id := range ids {
				list = append(list, map[string]any{"Id": id, "RepoTags": d.images[id]})
			}
			return reply(200, list)
		case r.Method == "GET" && strings.HasPrefix(path, "/v1.47/images/sha256:") && strings.HasSuffix(path, "/json"):
			id := strings.TrimSuffix(strings.TrimPrefix(path, "/v1.47/images/"), "/json")
			if _, ok := d.images[id]; !ok {
				return reply(404, map[string]string{"message": "no such image"})
			}
			return reply(200, imageBody(id, d.arch))
		case r.Method == "POST" && path == "/v1.47/images/load":
			if d.failPost != nil {
				return nil, d.failPost
			}
			d.loads++
			d.archive = map[string][]byte{}
			tr := tar.NewReader(r.Body)
			d.archiveOK = true
			for {
				header, err := tr.Next()
				if err == io.EOF {
					break
				}
				if err != nil {
					d.archiveOK = false
					return nil, err
				}
				body, err := io.ReadAll(tr)
				if err != nil {
					d.archiveOK = false
					return nil, err
				}
				d.archive[header.Name] = body
			}
			if d.onLoad != nil {
				return reply(d.onLoad(d, expected))
			}
			d.images[expected] = nil
			return reply(200, `{"stream":"Loaded image ID: `+expected+`"}`)
		case r.Method == "DELETE" && strings.HasPrefix(path, "/v1.47/images/sha256:"):
			id := strings.TrimPrefix(path, "/v1.47/images/")
			d.deletes = append(d.deletes, id)
			delete(d.images, id)
			return reply(200, []any{})
		}
		d.t.Fatalf("unexpected daemon request %s %s", r.Method, path)
		return nil, nil
	}))
}

func TestLoadImageBuildsARunnerOwnedLayoutAndAcceptsTheExpectedImage(t *testing.T) {
	for _, store := range []ImageStore{ImageStoreClassic, ImageStoreContainerd} {
		t.Run(string(store), func(t *testing.T) {
			f, d := newLoadFixture(t), newFakeDaemon(t, store)
			got, err := loadImageHTTP(context.Background(), d.client(f), f.request(), "amd64")
			if err != nil || got.State != LoadStateLoaded || got.ImageID != ExpectedImageID(store, f.entry) || got.Store != store {
				t.Fatalf("load: %+v %v", got, err)
			}
			if d.loads != 1 || !d.archiveOK {
				t.Fatalf("loads=%d archive complete=%v", d.loads, d.archiveOK)
			}
			names := make([]string, 0, len(d.archive))
			for name := range d.archive {
				names = append(names, name)
			}
			sort.Strings(names)
			want := []string{"blobs/sha256/" + hexSum(f.config), "blobs/sha256/" + hexSum(f.manifest), "blobs/sha256/" + hexSum(f.layers[0]), "blobs/sha256/" + hexSum(f.layers[1]), "index.json", "oci-layout"}
			sort.Strings(want)
			if strings.Join(names, ",") != strings.Join(want, ",") {
				t.Fatalf("archive members %v", names)
			}
			var index map[string]any
			if err := json.Unmarshal(d.archive["index.json"], &index); err != nil {
				t.Fatal(err)
			}
			if _, has := index["annotations"]; has || strings.Contains(string(d.archive["index.json"]), "annotations") || strings.Contains(string(d.archive["index.json"]), "name") {
				t.Fatalf("the runner-written index carries naming: %s", d.archive["index.json"])
			}
			for _, name := range []string{"manifest.json", "repositories"} {
				if _, has := d.archive[name]; has {
					t.Fatalf("archive carries %s", name)
				}
			}
			for digest, body := range f.source {
				if !bytes.Equal(d.archive["blobs/sha256/"+digest], body) {
					t.Fatal("archive blob differs from scratch")
				}
			}
		})
	}
}

func TestLoadImageAlreadyPresentLoadsNothing(t *testing.T) {
	f, d := newLoadFixture(t), newFakeDaemon(t, ImageStoreClassic)
	d.images[ExpectedImageID(ImageStoreClassic, f.entry)] = nil
	request := f.request()
	request.Source = memorySource{} // no blobs at all: a present image needs no upload
	got, err := loadImageHTTP(context.Background(), d.client(f), request, "amd64")
	if err != nil || got.State != LoadStatePresent || d.loads != 0 {
		t.Fatalf("present: %+v %v loads=%d", got, err, d.loads)
	}
}

// Rejections are definite: the receipt says the image was not accepted, and the daemon holds nothing new.
func TestLoadImageRejectionsAreTypedAndLeaveNoNewImage(t *testing.T) {
	cases := map[string]struct {
		outcome   string
		removed   bool
		configure func(*fakeDaemon, *loadFixture)
	}{
		"a load that retags another image is refused and the new image removed": {LoadOutcomeRetagDetected, true, func(d *fakeDaemon, f *loadFixture) {
			d.onLoad = func(d *fakeDaemon, expected string) (int, string) {
				d.images[expected] = nil
				d.images["sha256:"+strings.Repeat("e", 64)] = []string{"portal:latest", "portal:stolen"}
				return 200, `{"stream":"ok"}`
			}
		}},
		"a load that untags another image is refused": {LoadOutcomeRetagDetected, true, func(d *fakeDaemon, f *loadFixture) {
			d.onLoad = func(d *fakeDaemon, expected string) (int, string) {
				d.images[expected] = nil
				d.images["sha256:"+strings.Repeat("e", 64)] = nil
				return 200, `{"stream":"ok"}`
			}
		}},
		"a load that adds an unexpected image is refused": {LoadOutcomeRetagDetected, true, func(d *fakeDaemon, f *loadFixture) {
			d.onLoad = func(d *fakeDaemon, expected string) (int, string) {
				d.images[expected] = nil
				d.images["sha256:"+strings.Repeat("d", 64)] = nil
				return 200, `{"stream":"ok"}`
			}
		}},
		"a daemon error in the stream is a rejection": {LoadOutcomeLoadRejected, false, func(d *fakeDaemon, f *loadFixture) {
			d.onLoad = func(d *fakeDaemon, expected string) (int, string) { return 200, `{"error":"invalid archive"}` }
		}},
		"a daemon error status is a rejection": {LoadOutcomeLoadRejected, false, func(d *fakeDaemon, f *loadFixture) {
			d.onLoad = func(d *fakeDaemon, expected string) (int, string) { return 500, `{"message":"boom"}` }
		}},
		"a loaded image of another architecture is refused and removed": {LoadOutcomeInspectMismatch, true, func(d *fakeDaemon, f *loadFixture) {
			d.arch = "arm64"
		}},
		"a load that does not produce the expected image is refused": {LoadOutcomeInspectMismatch, false, func(d *fakeDaemon, f *loadFixture) {
			d.onLoad = func(d *fakeDaemon, expected string) (int, string) { return 200, `{"stream":"ok"}` }
		}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			f, d := newLoadFixture(t), newFakeDaemon(t, ImageStoreClassic)
			tc.configure(d, &f)
			got, err := loadImageHTTP(context.Background(), d.client(f), f.request(), "amd64")
			if err != nil || got.State != LoadStateRejected || got.Outcome != tc.outcome || got.Removed != tc.removed {
				t.Fatalf("rejection: %+v %v", got, err)
			}
			expected := ExpectedImageID(ImageStoreClassic, f.entry)
			if _, left := d.images[expected]; left {
				t.Fatal("a rejected load left its image in the daemon")
			}
			if tc.removed != (len(d.deletes) == 1 && d.deletes[0] == expected) {
				t.Fatalf("removal calls: %v", d.deletes)
			}
		})
	}
}

// The archive writer hashes the bytes it writes. A scratch blob that changed after upload must never reach the
// daemon as a complete archive.
func TestLoadImageRefusesACorruptScratchBlobBeforeTheArchiveIsComplete(t *testing.T) {
	for name, corrupt := range map[string]func(f *loadFixture){
		"layer bytes changed": func(f *loadFixture) {
			digest := hexSum(f.layers[1])
			f.source[digest] = append(append([]byte(nil), f.layers[1][:len(f.layers[1])-1]...), f.layers[1][len(f.layers[1])-1]^1)
		},
		"layer grew by one byte": func(f *loadFixture) {
			digest := hexSum(f.layers[1])
			f.source[digest] = append(append([]byte(nil), f.layers[1]...), 0)
		},
		"layer truncated": func(f *loadFixture) {
			digest := hexSum(f.layers[1])
			f.source[digest] = f.layers[1][:len(f.layers[1])-1]
		},
	} {
		t.Run(name, func(t *testing.T) {
			f, d := newLoadFixture(t), newFakeDaemon(t, ImageStoreClassic)
			corrupt(&f)
			got, err := loadImageHTTP(context.Background(), d.client(f), f.request(), "amd64")
			if err != nil || got.State != LoadStateRejected || got.Outcome != LoadOutcomeArchiveMismatch {
				t.Fatalf("corrupt blob: %+v %v", got, err)
			}
			if d.archiveOK {
				t.Fatal("the daemon received a complete archive containing an unchecked blob")
			}
			if _, ok := d.images[ExpectedImageID(ImageStoreClassic, f.entry)]; ok {
				t.Fatal("a corrupt load introduced an image")
			}
		})
	}
}

func TestLoadImageRefusesACorruptManifestWithoutContactingTheLoadRoute(t *testing.T) {
	f, d := newLoadFixture(t), newFakeDaemon(t, ImageStoreClassic)
	f.source[f.entry.ManifestDigest] = append([]byte(" "), f.manifest...)
	got, err := loadImageHTTP(context.Background(), d.client(f), f.request(), "amd64")
	if err != nil || got.State != LoadStateRejected || got.Outcome != LoadOutcomeArchiveMismatch || d.loads != 0 {
		t.Fatalf("corrupt manifest: %+v %v loads=%d", got, err, d.loads)
	}
}

// An error is uncertain, never a verdict: the unknown claim stays and a later run completes it.
func TestLoadImageErrorsAreUncertainNotRejections(t *testing.T) {
	t.Run("missing blob", func(t *testing.T) {
		f, d := newLoadFixture(t), newFakeDaemon(t, ImageStoreClassic)
		delete(f.source, hexSum(f.layers[0]))
		got, err := loadImageHTTP(context.Background(), d.client(f), f.request(), "amd64")
		if !errors.Is(err, ErrImageBlobUnavailable) || got != (LoadImageSnapshot{}) {
			t.Fatalf("missing blob: %+v %v", got, err)
		}
	})
	t.Run("connection lost during the load", func(t *testing.T) {
		f, d := newLoadFixture(t), newFakeDaemon(t, ImageStoreClassic)
		d.failPost = errors.New("connection reset")
		got, err := loadImageHTTP(context.Background(), d.client(f), f.request(), "amd64")
		if !errors.Is(err, ErrContainerUncertain) || got != (LoadImageSnapshot{}) {
			t.Fatalf("lost connection: %+v %v", got, err)
		}
	})
	t.Run("release does not match the descriptor", func(t *testing.T) {
		f, d := newLoadFixture(t), newFakeDaemon(t, ImageStoreClassic)
		request := f.request()
		request.ReleaseSHA256 = strings.Repeat("0", 64)
		if _, err := loadImageHTTP(context.Background(), d.client(f), request, "amd64"); !errors.Is(err, ErrInvalidResponse) {
			t.Fatal(err)
		}
	})
	t.Run("platform is not the host", func(t *testing.T) {
		f, d := newLoadFixture(t), newFakeDaemon(t, ImageStoreClassic)
		if _, err := loadImageHTTP(context.Background(), d.client(f), f.request(), "arm64"); !errors.Is(err, ErrInvalidResponse) {
			t.Fatal(err)
		}
	})
}

// The load route is the only mutating image route a load uses; nothing in a load can issue a pull.
func TestLoadImageIssuesOnlyAllowListedRequests(t *testing.T) {
	f, d := newLoadFixture(t), newFakeDaemon(t, ImageStoreClassic)
	var seen []string
	base := d.client(f)
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		seen = append(seen, r.Method+" "+r.URL.EscapedPath())
		return base.Transport.RoundTrip(r)
	})}
	if _, err := loadImageHTTP(context.Background(), client, f.request(), "amd64"); err != nil {
		t.Fatal(err)
	}
	for _, request := range seen {
		method, path, _ := strings.Cut(request, " ")
		if !dockerRequestAllowed(method, path) {
			t.Fatalf("load issued a request outside the allow-list: %s", request)
		}
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestLoadedImagePresentIsReadOnly(t *testing.T) {
	f, d := newLoadFixture(t), newFakeDaemon(t, ImageStoreClassic)
	client := d.client(f)
	present, err := loadedImagePresentHTTP(context.Background(), client, f.raw, f.parsed.SHA256, "linux/amd64", "amd64")
	if err != nil || present {
		t.Fatalf("absent image: %v %v", present, err)
	}
	d.images[ExpectedImageID(ImageStoreClassic, f.entry)] = nil
	present, err = loadedImagePresentHTTP(context.Background(), client, f.raw, f.parsed.SHA256, "linux/amd64", "amd64")
	if err != nil || !present || d.loads != 0 || len(d.deletes) != 0 {
		t.Fatalf("present image: %v %v loads=%d deletes=%v", present, err, d.loads, d.deletes)
	}
	if _, err := loadedImagePresentHTTP(context.Background(), client, f.raw, strings.Repeat("0", 64), "linux/amd64", "amd64"); !errors.Is(err, ErrInvalidResponse) {
		t.Fatal(err)
	}
}
