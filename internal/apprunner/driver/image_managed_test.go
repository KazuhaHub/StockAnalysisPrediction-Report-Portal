package driver

import (
	"context"
	"encoding/json"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

const (
	managedManifestDigest = "1111111111111111111111111111111111111111111111111111111111111111"
	managedConfigDigest   = "2222222222222222222222222222222222222222222222222222222222222222"
)

// managedImagesDescriptor is the managed fixture with a registry reference replaced by image content.
func managedImagesDescriptor(t *testing.T) ([]byte, appplatform.ParsedDescriptor) {
	t.Helper()
	raw, _, _ := managedImageFixture(t)
	var d map[string]any
	if err := json.Unmarshal(raw, &d); err != nil {
		t.Fatal(err)
	}
	backend := d["backend"].(map[string]any)
	delete(backend, "image")
	backend["platforms"] = []any{"linux/amd64", "linux/arm64"}
	var images []any
	for _, platform := range []string{"linux/amd64", "linux/arm64"} {
		images = append(images, map[string]any{
			"platform": platform, "manifest_digest": managedManifestDigest, "config_digest": managedConfigDigest,
			"layers": []any{map[string]any{"diff_id": strings.Repeat("3", 64), "uncompressed_bytes": 4096}},
		})
	}
	backend["images"] = images
	encoded, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := appplatform.ParseDescriptor(encoded)
	if err != nil {
		t.Fatal(err)
	}
	return encoded, parsed
}

// dockerRouter answers by method and path and records every request it receives. An unrouted request fails the
// test: the runner may only issue the calls the test expects.
type dockerRouter struct {
	t        *testing.T
	routes   map[string]func(*http.Request) (int, any)
	requests []string
}

func (d *dockerRouter) client() *http.Client {
	return daemonClient(transportFunc(func(r *http.Request) (*http.Response, error) {
		key := r.Method + " " + r.URL.EscapedPath()
		d.requests = append(d.requests, key)
		handler, ok := d.routes[key]
		if !ok {
			d.t.Fatalf("unexpected daemon request %s", key)
		}
		status, body := handler(r)
		raw, _ := json.Marshal(body)
		return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(raw)))}, nil
	}))
}

func infoFor(store ImageStore) map[string]any {
	status := [][]string{{"Backing Filesystem", "extfs"}}
	if store == ImageStoreContainerd {
		status = [][]string{{"driver-type", containerdSnapshotterDriverType}}
	}
	return map[string]any{"DriverStatus": status}
}

func imageBody(id, arch string) map[string]any {
	return map[string]any{"Id": id, "Os": "linux", "Architecture": arch, "Config": map[string]any{"Volumes": nil}}
}

func TestInspectManagedImageUsesTheStoreSpecificID(t *testing.T) {
	raw, parsed := managedImagesDescriptor(t)
	for name, tc := range map[string]struct {
		store ImageStore
		id    string
	}{
		"classic store addresses the config digest":      {ImageStoreClassic, "sha256:" + managedConfigDigest},
		"containerd store addresses the manifest digest": {ImageStoreContainerd, "sha256:" + managedManifestDigest},
	} {
		t.Run(name, func(t *testing.T) {
			router := &dockerRouter{t: t, routes: map[string]func(*http.Request) (int, any){
				"GET /v1.47/info":                      func(*http.Request) (int, any) { return 200, infoFor(tc.store) },
				"GET /v1.47/images/" + tc.id + "/json": func(*http.Request) (int, any) { return 200, imageBody(tc.id, "amd64") },
			}}
			got, err := inspectBackendImageHTTP(context.Background(), router.client(), raw, parsed.SHA256, "amd64", ImagePolicy{})
			if err != nil || got.ImageID != tc.id || got.Reference != "sha256:"+managedManifestDigest || got.Platform != "linux/amd64" {
				t.Fatalf("inspection: %+v %v", got, err)
			}
		})
	}
}

// A loaded image carries no tag and no RepoDigests; refusing it for that reason would make every managed image
// unstartable.
func TestInspectManagedImageNeedsNoRepoDigests(t *testing.T) {
	raw, parsed := managedImagesDescriptor(t)
	id := "sha256:" + managedConfigDigest
	body := imageBody(id, "amd64")
	body["RepoDigests"] = []string{}
	router := &dockerRouter{t: t, routes: map[string]func(*http.Request) (int, any){
		"GET /v1.47/info":                   func(*http.Request) (int, any) { return 200, infoFor(ImageStoreClassic) },
		"GET /v1.47/images/" + id + "/json": func(*http.Request) (int, any) { return 200, body },
	}}
	if _, err := inspectBackendImageHTTP(context.Background(), router.client(), raw, parsed.SHA256, "amd64", ImagePolicy{}); err != nil {
		t.Fatal(err)
	}
}

// Fails open if a pruned image were silently re-pulled; the only acceptable outcome is the typed refusal and no
// request that could fetch anything.
func TestInspectManagedImageMissingIsTypedAndNeverPulled(t *testing.T) {
	raw, parsed := managedImagesDescriptor(t)
	id := "sha256:" + managedConfigDigest
	router := &dockerRouter{t: t, routes: map[string]func(*http.Request) (int, any){
		"GET /v1.47/info":                   func(*http.Request) (int, any) { return 200, infoFor(ImageStoreClassic) },
		"GET /v1.47/images/" + id + "/json": func(*http.Request) (int, any) { return 404, map[string]string{"message": "no such image"} },
	}}
	got, err := inspectBackendImageHTTP(context.Background(), router.client(), raw, parsed.SHA256, "amd64", ImagePolicy{})
	if !errors.Is(err, ErrImageMissing) || got != (BackendImageInspection{}) {
		t.Fatalf("missing image: %+v %v", got, err)
	}
	for _, request := range router.requests {
		if strings.Contains(request, "/images/create") || strings.HasPrefix(request, "POST") {
			t.Fatalf("a mutating request followed a missing image: %v", router.requests)
		}
	}
	// A caller that joins the error, as every start path does, still reports the typed reason.
	if !errors.Is(errors.Join(ErrInvalidResponse, err), ErrImageMissing) {
		t.Fatal("typed reason lost through the callers' join")
	}
}

func TestInspectManagedImageRefusesAMismatchedImage(t *testing.T) {
	raw, parsed := managedImagesDescriptor(t)
	id := "sha256:" + managedConfigDigest
	cases := map[string]func(map[string]any){
		"different id returned": func(b map[string]any) { b["Id"] = "sha256:" + strings.Repeat("9", 64) },
		"wrong architecture":    func(b map[string]any) { b["Architecture"] = "arm64" },
		"windows image":         func(b map[string]any) { b["Os"] = "windows" },
		"image declares volumes": func(b map[string]any) {
			b["Config"] = map[string]any{"Volumes": map[string]any{"/data": map[string]any{}}}
		},
		"missing config": func(b map[string]any) { delete(b, "Config") },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			body := imageBody(id, "amd64")
			mutate(body)
			router := &dockerRouter{t: t, routes: map[string]func(*http.Request) (int, any){
				"GET /v1.47/info":                   func(*http.Request) (int, any) { return 200, infoFor(ImageStoreClassic) },
				"GET /v1.47/images/" + id + "/json": func(*http.Request) (int, any) { return 200, body },
			}}
			got, err := inspectBackendImageHTTP(context.Background(), router.client(), raw, parsed.SHA256, "amd64", ImagePolicy{})
			if !errors.Is(err, ErrInvalidResponse) || got != (BackendImageInspection{}) {
				t.Fatalf("accepted: %+v %v", got, err)
			}
		})
	}
}

// Production must refuse a registry reference before it asks the daemon anything.
func TestProductionProfileRefusesRegistryReferenceBeforeAnyDaemonRequest(t *testing.T) {
	raw, digest, reference := managedImageFixture(t)
	calls := 0
	got, err := inspectBackendImageHTTP(context.Background(), imageFixtureClient(t, imageResponseFixture(reference), reference, &calls), raw, digest, "amd64", ImagePolicy{})
	if !errors.Is(err, ErrReferenceImageRefused) || got != (BackendImageInspection{}) || calls != 0 {
		t.Fatalf("production accepted a registry reference: %+v %v (%d requests)", got, err, calls)
	}
}

func TestExpectedImageIDPerStore(t *testing.T) {
	entry := appplatform.BackendImage{ManifestDigest: managedManifestDigest, ConfigDigest: managedConfigDigest}
	if ExpectedImageID(ImageStoreClassic, entry) != "sha256:"+managedConfigDigest || ExpectedImageID(ImageStoreContainerd, entry) != "sha256:"+managedManifestDigest {
		t.Fatal("expected image ID differs from the store's identity")
	}
	for name, info := range map[string]infoResponse{
		"no status":      {},
		"overlay2":       {DriverStatus: [][]string{{"Backing Filesystem", "extfs"}}},
		"similar status": {DriverStatus: [][]string{{"driver-type", "io.containerd.snapshotter.v2"}}},
		"short pair":     {DriverStatus: [][]string{{"driver-type"}}},
	} {
		if imageStoreFromInfo(info) != ImageStoreClassic {
			t.Fatalf("%s detected as containerd", name)
		}
	}
	if imageStoreFromInfo(infoResponse{DriverStatus: [][]string{{"driver-type", containerdSnapshotterDriverType}}}) != ImageStoreContainerd {
		t.Fatal("containerd store not detected")
	}
}

func TestPlanCandidateContainerBindsManagedImageByManifestIdentity(t *testing.T) {
	raw, parsed := managedImagesDescriptor(t)
	owner := containerFixture(t).owner
	owner.ReleaseSHA256 = parsed.SHA256
	image := BackendImageInspection{ReleaseSHA256: parsed.SHA256, Reference: "sha256:" + managedManifestDigest, ImageID: "sha256:" + managedConfigDigest, Platform: "linux/amd64"}
	roots := containerRoots{Data: "/srv/portal-data", Sockets: "/run/portal-apps"}
	plan, err := planCandidateContainer(raw, image, owner, roots)
	if err != nil {
		t.Fatal(err)
	}
	var request containerCreate
	if json.Unmarshal(plan.body, &request) != nil || request.Image != image.ImageID {
		t.Fatalf("create does not address the loaded image by ID: %q", request.Image)
	}
	image.Reference = "ghcr.io/example/backend@sha256:" + strings.Repeat("a", 64)
	if _, err := planCandidateContainer(raw, image, owner, roots); err == nil {
		t.Fatal("a plan accepted an inspection that is not the descriptor's managed image")
	}
}

func TestDockerRequestAllowListIsClosed(t *testing.T) {
	id := strings.Repeat("a", 64)
	image := "sha256:" + strings.Repeat("b", 64)
	for _, ok := range []struct{ method, path string }{
		{"GET", "/version"}, {"GET", "/v1.47/info"}, {"GET", "/v1.47/images/json"}, {"GET", "/v1.47/images/" + image + "/json"},
		{"GET", "/v1.47/images/ghcr.io%2Fexample%2Fapp@sha256:" + strings.Repeat("a", 64) + "/json"},
		{"POST", "/v1.47/images/load"}, {"DELETE", "/v1.47/images/" + image},
		{"GET", "/v1.47/containers/json"}, {"GET", "/v1.47/containers/" + id + "/json"}, {"GET", "/v1.47/containers/" + id + "/logs"},
		{"POST", "/v1.47/containers/create"}, {"POST", "/v1.47/containers/" + id + "/start"}, {"POST", "/v1.47/containers/" + id + "/stop"},
		{"POST", "/v1.47/containers/" + id + "/kill"}, {"DELETE", "/v1.47/containers/" + id},
	} {
		if !dockerRequestAllowed(ok.method, ok.path) {
			t.Fatalf("a request the runner needs is refused: %s %s", ok.method, ok.path)
		}
	}
	for _, bad := range []struct{ method, path string }{
		{"POST", "/v1.47/images/create"},                    // pull
		{"GET", "/v1.47/images/create"},                     // pull, wrong verb
		{"POST", "/v1.47/images/" + image + "/push"},        // push
		{"POST", "/v1.47/images/" + image + "/tag"},         // retag
		{"DELETE", "/v1.47/images/ghcr.io%2Fexample%2Fapp"}, // removal by name
		{"POST", "/v1.47/build"}, {"POST", "/v1.47/commit"},
		{"POST", "/v1.47/networks/create"}, {"POST", "/v1.47/volumes/create"},
		{"POST", "/v1.47/containers/" + id + "/exec"}, {"POST", "/v1.47/containers/" + id + "/attach"},
		{"POST", "/v1.47/containers/" + id + "/restart"}, {"POST", "/v1.47/containers/" + id + "/rename"},
		{"POST", "/v1.47/containers/prune"}, {"POST", "/v1.47/images/prune"}, {"POST", "/v1.47/system/prune"},
		{"GET", "/v1.47/auth"}, {"POST", "/v1.47/auth"},
		{"GET", "/v1.46/info"}, {"GET", "/info"},
		{"PUT", "/v1.47/images/load"}, {"POST", "/v1.47/images/load/extra"},
	} {
		if dockerRequestAllowed(bad.method, bad.path) {
			t.Fatalf("a request outside the closed set is allowed: %s %s", bad.method, bad.path)
		}
	}
}

// The transport, not the caller, enforces the set: even a hand-built pull request is stopped before the socket.
func TestAllowListTransportStopsAPullBeforeTheSocket(t *testing.T) {
	reached := false
	client := daemonClient(transportFunc(func(*http.Request) (*http.Response, error) { reached = true; return nil, errors.New("reached") }))
	request, _ := http.NewRequest(http.MethodPost, "http://app-runner-docker.invalid/v1.47/images/create?fromImage=ghcr.io/example/app", nil)
	if _, err := client.Do(request); !errors.Is(err, ErrDockerRequestRefused) || reached {
		t.Fatalf("pull reached the socket: %v", err)
	}
	foreign, _ := http.NewRequest(http.MethodGet, "http://registry.example.org/v1.47/info", nil)
	if _, err := client.Do(foreign); !errors.Is(err, ErrDockerRequestRefused) || reached {
		t.Fatalf("a foreign host was dialed: %v", err)
	}
}

// SEC-01 negatives, stated as source properties of every runner package: no pull route, no registry credential
// source and no Docker client configuration is referenced by any non-test file.
func TestRunnerSourceNeverPullsOrReadsDockerCredentials(t *testing.T) {
	forbidden := []string{"images/create", "fromImage", "DOCKER_CONFIG", ".docker/config", "docker/config.json", "credsStore", "credHelpers", "docker-credential", "X-Registry-Auth", "X-Registry-Config"}
	roots := []string{"..", "../../../cmd/app-runner"}
	scanned := 0
	for _, root := range roots {
		err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return err
			}
			body, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			scanned++
			for _, word := range forbidden {
				if strings.Contains(string(body), word) {
					t.Errorf("%s references %q", path, word)
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if scanned < 50 {
		t.Fatalf("source guard scanned %d files; the walk is not covering the runner", scanned)
	}
}

// Every Docker socket client comes from newDockerClient, which wraps the allow-list. The check is structural, over
// the syntax of the non-test runner code: it fails on any reference to dockerDialer outside the constructor and the
// dialer's own definition, on any unix-network dial outside them (the one app-socket readiness dialer is named), and
// on any http.Transport or http.Client literal that is not the constructor and not an app-socket client.
//
// The unix-dial and literal rules are scoped to the driver and journal packages, the only ones that can reach the
// Docker daemon; the broker's outbound HTTP clients and the transport's control-socket dial are unrelated to Docker
// and are not scanned by them. An app-socket client is recognised by its function naming the app host and never a
// Docker host.
func TestOnlyTheDockerClientConstructorDialsTheSocket(t *testing.T) {
	appSocketDialers := map[string]bool{"readinessSocketDialer": true}
	fset := token.NewFileSet()
	err := filepath.Walk("..", func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return err
		}
		scoped := strings.HasPrefix(path, "../driver/") || strings.HasPrefix(path, "../journal/")
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok {
				continue
			}
			name := fn.Name.Name
			hosts := map[string]bool{}
			ast.Inspect(fn, func(n ast.Node) bool {
				if lit, ok := n.(*ast.BasicLit); ok && lit.Kind == token.STRING {
					hosts["docker"] = hosts["docker"] || strings.Contains(lit.Value, "docker.invalid")
					hosts["app"] = hosts["app"] || strings.Contains(lit.Value, "portal-app.invalid")
				}
				return true
			})
			inConstructor := name == "newDockerClient" || name == "dockerDialer"
			ast.Inspect(fn, func(n ast.Node) bool {
				switch x := n.(type) {
				case *ast.Ident:
					if x.Name == "dockerDialer" && !inConstructor {
						t.Errorf("%s: %s references dockerDialer outside the client constructor", path, name)
					}
				case *ast.CallExpr:
					for _, arg := range x.Args {
						if lit, ok := arg.(*ast.BasicLit); ok && lit.Value == `"unix"` && scoped && !inConstructor && !appSocketDialers[name] {
							t.Errorf("%s: %s dials a unix socket outside the Docker client constructor", path, name)
						}
					}
				case *ast.CompositeLit:
					sel, ok := x.Type.(*ast.SelectorExpr)
					if !ok || !scoped || inConstructor || name == "daemonClient" {
						return true
					}
					if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == "http" && (sel.Sel.Name == "Transport" || sel.Sel.Name == "Client") && !(hosts["app"] && !hosts["docker"]) {
						t.Errorf("%s: %s builds its own http.%s outside the Docker client constructor", path, name, sel.Sel.Name)
					}
				}
				return true
			})
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
