package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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

const ociTestRepoPath = "kazuhahub/apps/notes"

func ociTestDigest(b []byte) string {
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func ociTestManifest(layerDigest string, size int, edit func(map[string]any)) []byte {
	m := map[string]any{
		"schemaVersion": 2,
		"mediaType":     appplatform.OCIManifestMediaType,
		"artifactType":  appplatform.OCIPackageArtifactType,
		"config":        map[string]any{"mediaType": "application/vnd.oci.empty.v1+json", "digest": "sha256:44136fa355b3678a1146ad16f7e8649e94fb4fc21fe77e8310c060f61caaff8a", "size": 2, "data": "e30="},
		"layers":        []any{map[string]any{"mediaType": appplatform.OCIPackageLayerMediaType, "digest": layerDigest, "size": size, "annotations": map[string]string{"org.opencontainers.image.title": "app.portalapp"}}},
	}
	if edit != nil {
		edit(m)
	}
	raw, _ := json.Marshal(m)
	return raw
}

// platformOCIRegistry is a fake public registry: anonymous Bearer tokens, a manifest by digest, and a blob that
// may redirect to a CDN host.
type platformOCIRegistry struct {
	t                *testing.T
	server           *httptest.Server
	parts            platformManagedImageParts
	pkg              []byte
	manifest         []byte
	layerBody        []byte
	repository       string
	catalogueDigest  string
	realm            string
	token            string
	cdnRedirect      bool
	sameHostRedirect bool
	mu               sync.Mutex
	tokenQueries     []string
	tokenAuth        []string
	manifestRequests int
	blobRequests     int
	cdnAuth          []string
}

func newPlatformOCIRegistry(t *testing.T, parts platformManagedImageParts, pkg []byte) *platformOCIRegistry {
	r := &platformOCIRegistry{t: t, parts: parts, pkg: pkg, layerBody: pkg, repository: "127.0.0.1/" + ociTestRepoPath, realm: "https://127.0.0.1/token", token: "anon.Token-1="}
	r.manifest = ociTestManifest(ociTestDigest(pkg), len(pkg), nil)
	parsed, err := appplatform.ParseDescriptor(parts.descriptor)
	if err != nil {
		t.Fatal(err)
	}
	manifestPath := "/v2/" + ociTestRepoPath + "/manifests/"
	blobPath := "/v2/" + ociTestRepoPath + "/blobs/"
	r.server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		r.mu.Lock()
		defer r.mu.Unlock()
		authorized := req.Header.Get("Authorization") == "Bearer "+r.token
		challenge := func() {
			w.Header().Set("WWW-Authenticate", `Bearer realm="`+r.realm+`",service="registry.test",scope="repository:`+ociTestRepoPath+`:pull,push"`)
			w.WriteHeader(http.StatusUnauthorized)
		}
		switch {
		case req.URL.Path == "/index.json":
			digest := r.catalogueDigest
			if digest == "" {
				digest = ociTestDigest(r.manifest)
			}
			entry := appplatform.CatalogueRelease{AppID: parsed.Manifest.ID, PublisherID: parsed.Manifest.Publisher, Version: parsed.Manifest.Version, DescriptorSHA256: platformDigest(parts.descriptor), DescriptorURL: "https://127.0.0.1/app.json", SignatureURL: "https://127.0.0.1/app.sig", PackageOCI: &appplatform.CataloguePackageOCI{Repository: r.repository, Digest: digest}}
			json.NewEncoder(w).Encode(map[string]any{"format": "portal.catalogue/2-dev.3", "releases": []appplatform.CatalogueRelease{entry}})
		case req.URL.Path == "/app.json":
			w.Write(parts.descriptor)
		case req.URL.Path == "/app.sig":
			w.Write(parts.signature)
		case req.URL.Path == "/token":
			r.tokenQueries = append(r.tokenQueries, req.URL.RawQuery)
			r.tokenAuth = append(r.tokenAuth, req.Header.Get("Authorization"))
			json.NewEncoder(w).Encode(map[string]string{"token": r.token})
		case strings.HasPrefix(req.URL.Path, manifestPath):
			r.manifestRequests++
			if !authorized {
				challenge()
				return
			}
			if req.Header.Get("Accept") != appplatform.OCIManifestMediaType || req.URL.Path != manifestPath+ociTestDigest(r.manifest) && r.catalogueDigest == "" {
				http.NotFound(w, req)
				return
			}
			w.Header().Set("Content-Type", appplatform.OCIManifestMediaType)
			w.Write(r.manifest)
		case strings.HasPrefix(req.URL.Path, blobPath):
			r.blobRequests++
			if !authorized {
				challenge()
				return
			}
			if r.sameHostRedirect && !strings.HasSuffix(req.URL.Path, "/moved") {
				http.Redirect(w, req, req.URL.Path+"/moved", http.StatusTemporaryRedirect)
				return
			}
			if r.cdnRedirect {
				http.Redirect(w, req, "https://example.com/cdn/blob?sig=signed", http.StatusTemporaryRedirect)
				return
			}
			w.Header().Set("Content-Length", strconv.Itoa(len(r.layerBody)))
			w.Write(r.layerBody)
		case req.URL.Path == "/cdn/blob" && req.Host == "example.com" && req.URL.RawQuery == "sig=signed":
			r.cdnAuth = append(r.cdnAuth, req.Header.Get("Authorization"))
			w.Header().Set("Content-Length", strconv.Itoa(len(r.layerBody)))
			w.Write(r.layerBody)
		default:
			http.NotFound(w, req)
		}
	}))
	t.Cleanup(r.server.Close)
	return r
}

type platformOCIFixture struct {
	s        *Server
	actor    platformActorSession
	parts    platformManagedImageParts
	registry *platformOCIRegistry
}

func newPlatformOCIFixture(t *testing.T) *platformOCIFixture {
	t.Helper()
	s, actor := platformTrustTestServer(t, false)
	platformRegisterTestPublisher(t, s, actor)
	s.platformImageDir = t.TempDir()
	parts := newPlatformManagedImageParts(t, "4.0.0", "catalogue")
	return &platformOCIFixture{s: s, actor: actor, parts: parts, registry: newPlatformOCIRegistry(t, parts, parts.packageBytes(t, nil, ""))}
}

func (f *platformOCIFixture) install() (platformStagedRelease, error) {
	c := newPlatformMarketClient()
	c.safe.allowInsecureForTest = true
	platformMarketTestTLS(f.registry.t, c, f.registry.server)
	return f.s.fetchAndStagePlatformMarketRelease(context.Background(), f.actor, "https://127.0.0.1/index.json", platformDigest(f.parts.descriptor), c)
}

func (f *platformOCIFixture) refused(t *testing.T) {
	t.Helper()
	if _, err := f.install(); err == nil {
		t.Fatal("installed")
	}
	if scalar[int](t, f.s.st, `SELECT COUNT(*) FROM app_platform_releases`) != 0 {
		t.Fatal("a refused package left a release")
	}
	entries, _ := os.ReadDir(f.s.platformImageDir + "/app-blobs")
	if len(entries) != 0 {
		t.Fatal("a refused package left blobs")
	}
}

// Fails if the anonymous token flow, the computed pull scope, or the CDN redirect breaks, or if the registry
// token reaches the CDN host.
func TestOCICatalogueInstallTakesAnAnonymousTokenAndKeepsItOffTheCDN(t *testing.T) {
	f := newPlatformOCIFixture(t)
	f.registry.cdnRedirect = true
	staged, err := f.install()
	if err != nil || staged.sha256 != platformDigest(f.parts.descriptor) {
		t.Fatal(staged, err)
	}
	r := f.registry
	if len(r.tokenQueries) != 1 || r.tokenQueries[0] != "scope=repository%3A"+strings.ReplaceAll(ociTestRepoPath, "/", "%2F")+"%3Apull&service=registry.test" || r.tokenAuth[0] != "" {
		t.Fatalf("token requests %v auth %v", r.tokenQueries, r.tokenAuth)
	}
	if len(r.cdnAuth) != 1 || r.cdnAuth[0] != "" {
		t.Fatalf("CDN saw Authorization %q", r.cdnAuth)
	}
	if scalar[int](t, f.s.st, `SELECT COUNT(*) FROM app_platform_releases`) != 1 {
		t.Fatal("no release")
	}
}

// Fails if a same-host blob redirect loses the token the registry issued for that host.
func TestOCICatalogueKeepsTheTokenOnASameHostRedirect(t *testing.T) {
	f := newPlatformOCIFixture(t)
	f.registry.sameHostRedirect = true
	if _, err := f.install(); err != nil {
		t.Fatal(err)
	}
}

// Fails if a manifest whose bytes are not the catalogue's digest is trusted.
func TestOCICatalogueRefusesAManifestDigestMismatch(t *testing.T) {
	f := newPlatformOCIFixture(t)
	f.registry.catalogueDigest = "sha256:" + strings.Repeat("e", 64)
	f.refused(t)
	if f.registry.blobRequests != 0 {
		t.Fatal("a blob was fetched for an unverified manifest")
	}
}

// Fails if anything other than the Portal package artifact shape is downloaded.
func TestOCICatalogueRefusesOtherArtifactShapes(t *testing.T) {
	for name, edit := range map[string]func(map[string]any){
		"artifact type": func(m map[string]any) { m["artifactType"] = "application/vnd.example.other" },
		"layer type": func(m map[string]any) {
			m["layers"].([]any)[0].(map[string]any)["mediaType"] = "application/vnd.oci.image.layer.v1.tar+gzip"
		},
		"two layers": func(m map[string]any) { m["layers"] = append(m["layers"].([]any), m["layers"].([]any)[0]) },
		"oversized layer": func(m map[string]any) {
			m["layers"].([]any)[0].(map[string]any)["size"] = appplatform.MaxManagedOfflineEnvelopeBytes + 1
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := newPlatformOCIFixture(t)
			f.registry.manifest = ociTestManifest(ociTestDigest(f.registry.pkg), len(f.registry.pkg), edit)
			f.refused(t)
			if f.registry.blobRequests != 0 {
				t.Fatal("a blob was fetched for a refused manifest")
			}
		})
	}
}

// Fails if layer bytes that are not the manifest's digest, or more bytes than its size, are staged.
func TestOCICatalogueRefusesLayerBytesThatAreNotTheManifests(t *testing.T) {
	t.Run("digest", func(t *testing.T) {
		f := newPlatformOCIFixture(t)
		body := append([]byte(nil), f.registry.pkg...)
		body[len(body)/2] ^= 0xff
		f.registry.layerBody = body
		f.refused(t)
		// The digest check refuses before staging and retries once from nothing; without it staging would refuse
		// the damaged envelope and, the layer being pinned, not download again.
		if f.registry.blobRequests != 2 {
			t.Fatalf("blob requests %d, want 2", f.registry.blobRequests)
		}
		store, _ := f.s.platformImages()
		entries, _ := os.ReadDir(store.staging)
		if len(entries) != 0 {
			t.Fatalf("a mismatched layer stayed in staging: %v", entries)
		}
	})
	t.Run("size", func(t *testing.T) {
		f := newPlatformOCIFixture(t)
		f.registry.manifest = ociTestManifest(ociTestDigest(f.registry.pkg), len(f.registry.pkg)-16, nil)
		f.refused(t)
	})
}

// Fails if a repository or token realm at a private address, or a plain-http realm, is contacted.
func TestOCICatalogueRefusesPrivateAndInsecureEndpoints(t *testing.T) {
	t.Run("private repository", func(t *testing.T) {
		f := newPlatformOCIFixture(t)
		f.registry.repository = "10.1.2.3/" + ociTestRepoPath
		f.refused(t)
		if f.registry.manifestRequests != 0 {
			t.Fatal("the private registry was contacted")
		}
	})
	for name, realm := range map[string]string{"private realm": "https://10.1.2.3/token", "http realm": "http://127.0.0.1/token", "realm with query": "https://127.0.0.1/token?x=1"} {
		t.Run(name, func(t *testing.T) {
			f := newPlatformOCIFixture(t)
			f.registry.realm = realm
			f.refused(t)
			if len(f.registry.tokenQueries) != 0 || f.registry.blobRequests != 0 {
				t.Fatal("the refused realm was used")
			}
		})
	}
}

func TestParseOCIBearerChallenge(t *testing.T) {
	realm, service, ok := parseOCIBearerChallenge(`Bearer realm="https://ghcr.io/token",service="ghcr.io",scope="repository:a/b:pull"`)
	if !ok || realm != "https://ghcr.io/token" || service != "ghcr.io" {
		t.Fatal(realm, service, ok)
	}
	for _, bad := range []string{`Basic realm="x"`, `Bearer service="ghcr.io"`, `Bearer realm="a\"b"`, `Bearer realm="x",realm="y"`, `Bearer realm=x`, "Bearer realm=\"x\r\n\""} {
		if _, _, ok := parseOCIBearerChallenge(bad); ok {
			t.Fatal("accepted", bad)
		}
	}
}
