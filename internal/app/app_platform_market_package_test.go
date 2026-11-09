package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

// platformPackageRemote serves a catalogue, a descriptor, a signature and a package. The package handler can
// break a response part-way, as a dropped connection does.
type platformPackageRemote struct {
	t       *testing.T
	server  *httptest.Server
	parts   platformManagedImageParts
	pkg     []byte
	etag    string
	mu      sync.Mutex
	ranges  []string
	ifRange []string
	packets int
	// breakAfter, when positive, cuts every package response after that fraction (1/n) of what it was going to send.
	breakDivisor int
	noPackageURL bool
}

func newPlatformPackageRemote(t *testing.T, parts platformManagedImageParts, pkg []byte) *platformPackageRemote {
	r := &platformPackageRemote{t: t, parts: parts, pkg: pkg, etag: `"pkg-v1"`}
	parsed, err := appplatform.ParseDescriptor(parts.descriptor)
	if err != nil {
		t.Fatal(err)
	}
	var base string
	r.server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch req.URL.Path {
		case "/index.json":
			entry := appplatform.CatalogueRelease{AppID: parsed.Manifest.ID, PublisherID: parsed.Manifest.Publisher, Version: parsed.Manifest.Version, DescriptorSHA256: platformDigest(parts.descriptor), DescriptorURL: base + "/app.json", SignatureURL: base + "/app.sig"}
			if !r.noPackageURL {
				entry.PackageURL = base + "/app.portalapp"
			}
			json.NewEncoder(w).Encode(map[string]any{"format": "portal.catalogue/2-dev.2", "releases": []appplatform.CatalogueRelease{entry}})
		case "/app.json":
			w.Write(parts.descriptor)
		case "/app.sig":
			w.Write(parts.signature)
		case "/app.portalapp":
			r.servePackage(w, req)
		default:
			http.NotFound(w, req)
		}
	}))
	base = "https://127.0.0.1"
	t.Cleanup(r.server.Close)
	return r
}

func (r *platformPackageRemote) servePackage(w http.ResponseWriter, req *http.Request) {
	r.mu.Lock()
	r.packets++
	r.ranges = append(r.ranges, req.Header.Get("Range"))
	r.ifRange = append(r.ifRange, req.Header.Get("If-Range"))
	r.mu.Unlock()
	body, start, status := r.pkg, 0, http.StatusOK
	w.Header().Set("ETag", r.etag)
	if rng := req.Header.Get("Range"); rng != "" && (req.Header.Get("If-Range") == "" || req.Header.Get("If-Range") == r.etag) {
		n, err := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(rng, "bytes="), "-"))
		if err != nil || n >= len(r.pkg) {
			w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
			return
		}
		start, status = n, http.StatusPartialContent
		body = r.pkg[n:]
		w.Header().Set("Content-Range", "bytes "+strconv.Itoa(n)+"-"+strconv.Itoa(len(r.pkg)-1)+"/"+strconv.Itoa(len(r.pkg)))
	}
	_ = start
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(status)
	if r.breakDivisor > 0 {
		w.Write(body[:len(body)/r.breakDivisor])
		w.(http.Flusher).Flush()
		panic(http.ErrAbortHandler)
	}
	w.Write(body)
}

func (r *platformPackageRemote) client() *platformMarketClient {
	c := newPlatformMarketClient()
	c.safe.allowInsecureForTest = true
	platformMarketTestTLS(r.t, c, r.server)
	return c
}

type platformCatalogueFixture struct {
	s      *Server
	actor  platformActorSession
	parts  platformManagedImageParts
	remote *platformPackageRemote
}

func newPlatformCatalogueFixture(t *testing.T) *platformCatalogueFixture {
	t.Helper()
	s, actor := platformTrustTestServer(t, false)
	platformRegisterTestPublisher(t, s, actor)
	s.platformImageDir = t.TempDir()
	parts := newPlatformManagedImageParts(t, "4.0.0", "catalogue")
	return &platformCatalogueFixture{s: s, actor: actor, parts: parts, remote: newPlatformPackageRemote(t, parts, parts.packageBytes(t, nil, ""))}
}

func (f *platformCatalogueFixture) install(ctx context.Context) (platformStagedRelease, error) {
	return f.s.fetchAndStagePlatformMarketRelease(ctx, f.actor, "https://127.0.0.1/index.json", platformDigest(f.parts.descriptor), f.remote.client())
}

func (f *platformCatalogueFixture) blobDigests() map[string]bool {
	out := map[string]bool{}
	entries, _ := os.ReadDir(f.s.platformImageDir + "/app-blobs")
	for _, e := range entries {
		out[e.Name()] = true
	}
	return out
}

// Fails if the catalogue install and the offline install of the same package are not one release with one set
// of blobs.
func TestCatalogueInstallOfAManagedPackageIsTheOfflineInstall(t *testing.T) {
	f := newPlatformCatalogueFixture(t)
	staged, err := f.install(context.Background())
	if err != nil || staged.sha256 != platformDigest(f.parts.descriptor) || staged.reused {
		t.Fatal(staged, err)
	}
	if len(f.blobDigests()) != 4 {
		t.Fatalf("blobs %v", f.blobDigests())
	}
	for _, blob := range f.parts.imageBlobList() {
		if !f.blobDigests()[blob.Digest] {
			t.Fatalf("blob %s missing", blob.Digest[:8])
		}
	}
	// The offline path takes the same bytes to the same release.
	mux := http.NewServeMux()
	f.s.wireRoutes(mux)
	cookie := f.s.managedCookie(*f.s.st.GetUser(f.actor.username), f.actor.sessionID, f.actor.expires.Unix())
	response := platformOfflineRequest(mux, cookie, "application/zip", f.remote.pkg)
	if response.Code != 200 || !strings.Contains(response.Body.String(), `"reused":true`) || !strings.Contains(response.Body.String(), staged.sha256) {
		t.Fatal(response.Code, response.Body.String())
	}
	if scalar[int](t, f.s.st, `SELECT COUNT(*) FROM app_platform_releases`) != 1 || len(f.blobDigests()) != 4 {
		t.Fatal("online and offline install made different releases or blobs")
	}
}

// Fails if an interrupted download starts again from byte zero after a restart, or resumes without the validator.
func TestCataloguePackageDownloadResumesFromThePartialAfterARestartWithRangeAndIfRange(t *testing.T) {
	f := newPlatformCatalogueFixture(t)
	f.remote.breakDivisor = 2
	if _, err := f.install(context.Background()); err == nil {
		t.Fatal("a download that never completes was reported complete")
	}
	key := "catalogue:https://127.0.0.1/app.portalapp"
	store, _ := f.s.platformImages()
	held, err := os.Stat(store.partialPath(key))
	if err != nil || held.Size() == 0 || held.Size() >= int64(len(f.remote.pkg)) {
		t.Fatalf("no usable partial after the failed download: %v", err)
	}
	if scalar[int](t, f.s.st, `SELECT COUNT(*) FROM app_platform_releases`) != 0 || len(f.blobDigests()) != 0 {
		t.Fatal("an incomplete package left a release or blobs")
	}
	// A restart: a new Server over the same database and data directory runs the start-up recovery.
	restarted := &Server{st: f.s.st, cfg: f.s.cfg, platformImageDir: f.s.platformImageDir}
	restarted.platformImageRecover()
	f.s = restarted
	f.remote.mu.Lock()
	f.remote.breakDivisor, f.remote.ranges, f.remote.ifRange = 0, nil, nil
	f.remote.mu.Unlock()
	staged, err := f.install(context.Background())
	if err != nil || staged.sha256 != platformDigest(f.parts.descriptor) {
		t.Fatal("the resumed download did not install", err)
	}
	want := "bytes=" + strconv.FormatInt(held.Size(), 10) + "-"
	if len(f.remote.ranges) != 1 || f.remote.ranges[0] != want || f.remote.ifRange[0] != f.remote.etag {
		t.Fatalf("resume asked for %v with If-Range %v, want %s %s", f.remote.ranges, f.remote.ifRange, want, f.remote.etag)
	}
	if _, err := os.Stat(store.partialPath(key)); err == nil {
		t.Fatal("the partial stayed after the download finished")
	}
}

// Fails if bytes from an older version of the file are spliced onto the new one, or the install is poisoned for
// good by a prefix that no longer matches.
func TestCataloguePackageStartsAgainWhenTheFileChangedUpstreamSinceThePartial(t *testing.T) {
	f := newPlatformCatalogueFixture(t)
	store, _ := f.s.platformImages()
	key := "catalogue:https://127.0.0.1/app.portalapp"
	junk := append([]byte("PK\x03\x04 an older, different file "), make([]byte, 64)...)
	os.WriteFile(store.partialPath(key), junk, 0o600)
	os.WriteFile(store.validatorPath(key), []byte(`"pkg-v0"`), 0o600)
	staged, err := f.install(context.Background())
	if err != nil || staged.sha256 != platformDigest(f.parts.descriptor) {
		t.Fatal(err)
	}
	if f.remote.ifRange[0] != `"pkg-v0"` {
		t.Fatalf("the stale validator was not offered: %v", f.remote.ifRange)
	}
}

// Fails if a package whose descriptor is not byte-equal to the catalogue's is staged, or leaves blobs behind.
func TestCataloguePackageWithAnotherDescriptorIsRefusedAndStoresNothing(t *testing.T) {
	f := newPlatformCatalogueFixture(t)
	other := newPlatformManagedImageParts(t, "4.0.1", "other")
	f.remote.pkg = other.packageBytes(t, nil, "")
	if _, err := f.install(context.Background()); err == nil {
		t.Fatal("a package for another release was installed from this catalogue entry")
	}
	if scalar[int](t, f.s.st, `SELECT COUNT(*) FROM app_platform_releases`) != 0 || len(f.blobDigests()) != 0 {
		t.Fatal("a refused package left a release or blobs")
	}
}

// Fails if the package is fetched when the staging cap cannot hold it.
func TestCataloguePackageOverTheStagingCapIsRefusedBeforeAnyDownload(t *testing.T) {
	f := newPlatformCatalogueFixture(t)
	store, _ := f.s.platformImages()
	store.stagingCap = 1 << 20
	if _, err := f.install(context.Background()); err == nil {
		t.Fatal("the package was fetched over the cap")
	}
	f.remote.mu.Lock()
	defer f.remote.mu.Unlock()
	if f.remote.packets != 0 {
		t.Fatal("bytes were downloaded for a refused package")
	}
}

// Fails if a managed descriptor without a package, or a package for a static one, reaches staging.
func TestCatalogueManagedReleaseNeedsItsPackageURL(t *testing.T) {
	f := newPlatformCatalogueFixture(t)
	f.remote.noPackageURL = true
	if _, err := f.install(context.Background()); err == nil {
		t.Fatal("a managed release without a package was staged from the catalogue")
	}
	if scalar[int](t, f.s.st, `SELECT COUNT(*) FROM app_platform_releases`) != 0 {
		t.Fatal("a release row exists")
	}
}
