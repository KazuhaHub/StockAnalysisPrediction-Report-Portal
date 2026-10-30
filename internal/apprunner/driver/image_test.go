package driver

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

func managedImageFixture(t *testing.T) ([]byte, string, string) {
	t.Helper()
	raw, err := os.ReadFile("../../appplatform/testdata/descriptors/managed.json")
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := appplatform.ParseDescriptor(raw)
	if err != nil {
		t.Fatal(err)
	}
	return raw, parsed.SHA256, parsed.Manifest.Backend.Image
}
func imageResponseFixture(reference string) map[string]any {
	return map[string]any{"Id": "sha256:" + strings.Repeat("b", 64), "RepoDigests": []string{reference}, "Os": "linux", "Architecture": "amd64", "Variant": "", "Config": map[string]any{"Volumes": nil, "Env": []string{"IMAGE_ONLY=fixture-sensitive"}, "Entrypoint": []string{"/app/server"}}, "GraphDriver": map[string]any{"Data": map[string]string{"UpperDir": "fixture-sensitive"}}}
}
func imageFixtureClient(t *testing.T, response any, reference string, calls *int) *http.Client {
	t.Helper()
	return daemonClient(transportFunc(func(r *http.Request) (*http.Response, error) {
		*calls++
		if r.Method != "GET" || r.URL.Host != "app-runner-docker.invalid" || r.URL.EscapedPath() != "/v1.47/images/"+url.PathEscape(reference)+"/json" || r.URL.RawQuery != "" || r.Body != nil || r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
			t.Fatalf("unexpected image action: %s %s", r.Method, r.URL)
		}
		raw, err := json.Marshal(response)
		if err != nil {
			t.Fatal(err)
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(raw)))}, nil
	}))
}
func TestBackendImageInspectionBindsDescriptorDigestAndNativeImage(t *testing.T) {
	raw, digest, reference := managedImageFixture(t)
	calls := 0
	got, err := inspectBackendImageHTTP(context.Background(), imageFixtureClient(t, imageResponseFixture(reference), reference, &calls), raw, digest, "amd64")
	if err != nil || calls != 1 || got.ReleaseSHA256 != digest || got.Reference != reference || got.ImageID != "sha256:"+strings.Repeat("b", 64) || got.Platform != "linux/amd64" {
		t.Fatalf("image inspection: %+v %v (%d requests)", got, err, calls)
	}
	encoded, _ := json.Marshal(got)
	if strings.Contains(string(encoded), "fixture-sensitive") || strings.Contains(string(encoded), "Entrypoint") {
		t.Fatal("inspection leaked daemon/image configuration")
	}
}
func TestBackendImageInspectionRefusesUnsafeMetadata(t *testing.T) {
	raw, digest, reference := managedImageFixture(t)
	cases := map[string]func(map[string]any){
		"wrong digest": func(v map[string]any) {
			v["RepoDigests"] = []string{strings.Replace(reference, "sha256:", "sha256:f", 1)}
		},
		"missing digest":     func(v map[string]any) { delete(v, "RepoDigests") },
		"tag alias":          func(v map[string]any) { v["RepoDigests"] = []string{"ghcr.io/example/backend:latest"} },
		"wrong OS":           func(v map[string]any) { v["Os"] = "windows" },
		"wrong architecture": func(v map[string]any) { v["Architecture"] = "arm64" },
		"architecture alias": func(v map[string]any) { v["Architecture"] = "x86_64" },
		"invalid variant":    func(v map[string]any) { v["Variant"] = "v9" },
		"missing config":     func(v map[string]any) { delete(v, "Config") },
		"null config":        func(v map[string]any) { v["Config"] = nil },
		"anonymous volume": func(v map[string]any) {
			v["Config"] = map[string]any{"Volumes": map[string]any{"/data": map[string]any{}}}
		},
		"volume alias":    func(v map[string]any) { v["Config"] = map[string]any{"volumes": nil} },
		"native ID alias": func(v map[string]any) { v["ID"] = v["Id"]; delete(v, "Id") },
		"native ID tag":   func(v map[string]any) { v["Id"] = "backend:latest" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			v := imageResponseFixture(reference)
			mutate(v)
			calls := 0
			got, err := inspectBackendImageHTTP(context.Background(), imageFixtureClient(t, v, reference, &calls), raw, digest, "amd64")
			if !errors.Is(err, ErrInvalidResponse) || got != (BackendImageInspection{}) {
				t.Fatalf("unsafe image accepted: %+v %v", got, err)
			}
		})
	}
}
func TestBackendImageInspectionRefusesBeforeDaemonAccess(t *testing.T) {
	raw, digest, _ := managedImageFixture(t)
	cases := []struct {
		name         string
		raw          []byte
		digest, arch string
	}{
		{"changed descriptor", append(append([]byte{}, raw...), byte(' ')), digest, "amd64"},
		{"invalid descriptor", []byte(`{}`), digest, "amd64"},
		{"unsupported host", raw, digest, "386"},
		{"upper digest", raw, strings.ToUpper(digest), "amd64"},
	}
	static, err := os.ReadFile("../../appplatform/testdata/descriptors/static.json")
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := appplatform.ParseDescriptor(static)
	if err != nil {
		t.Fatal(err)
	}
	cases = append(cases, struct {
		name         string
		raw          []byte
		digest, arch string
	}{"static descriptor", static, parsed.SHA256, "amd64"})
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			client := daemonClient(transportFunc(func(*http.Request) (*http.Response, error) { calls++; return nil, errors.New("unexpected access") }))
			got, err := inspectBackendImageHTTP(context.Background(), client, tc.raw, tc.digest, tc.arch)
			if err == nil || calls != 0 || got != (BackendImageInspection{}) {
				t.Fatalf("invalid request escaped: %+v %v calls=%d", got, err, calls)
			}
		})
	}
}

func TestBackendImageInspectionArm64Variant(t *testing.T) {
	raw, _, reference := managedImageFixture(t)
	var manifest map[string]any
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	manifest["backend"].(map[string]any)["platforms"] = []string{"linux/arm64"}
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := appplatform.ParseDescriptor(raw)
	if err != nil {
		t.Fatal(err)
	}
	for _, variant := range []string{"", "v8", "v7", "v9"} {
		t.Run("variant_"+variant, func(t *testing.T) {
			response := imageResponseFixture(reference)
			response["Architecture"] = "arm64"
			response["Variant"] = variant
			calls := 0
			got, err := inspectBackendImageHTTP(context.Background(), imageFixtureClient(t, response, reference, &calls), raw, parsed.SHA256, "arm64")
			if variant == "" || variant == "v8" {
				if err != nil || got.Platform != "linux/arm64" {
					t.Fatalf("valid native variant: %+v %v", got, err)
				}
			} else if !errors.Is(err, ErrInvalidResponse) {
				t.Fatal("unadmitted variant accepted", err)
			}
		})
	}
	calls := 0
	_, err = inspectBackendImageHTTP(context.Background(), imageFixtureClient(t, imageResponseFixture(reference), reference, &calls), raw, parsed.SHA256, "amd64")
	if !errors.Is(err, ErrUnsupported) || calls != 0 {
		t.Fatal("undeclared platform queried daemon", err, calls)
	}
}

func TestBackendImageInspectionWireRefusals(t *testing.T) {
	raw, digest, reference := managedImageFixture(t)
	cases := map[string]struct {
		status int
		body   string
	}{
		"missing local image": {404, `{"message":"fixture-sensitive"}`},
		"daemon failure":      {500, `{"message":"fixture-sensitive"}`},
		"redirect":            {302, `{}`},
		"oversized":           {200, strings.Repeat(" ", maxResponseBytes+1)},
		"root duplicate":      {200, `{"Id":"sha256:` + strings.Repeat("b", 64) + `","Id":"sha256:` + strings.Repeat("c", 64) + `"}`},
		"nested duplicate":    {200, `{"Config":{"Volumes":null,"Volumes":{}}}`},
		"trailing":            {200, `{} {}`},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			calls := 0
			client := daemonClient(transportFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				return &http.Response{StatusCode: tc.status, Header: http.Header{"Location": []string{"https://fixture.invalid/escape"}}, Body: io.NopCloser(strings.NewReader(tc.body))}, nil
			}))
			got, err := inspectBackendImageHTTP(context.Background(), client, raw, digest, "amd64")
			if err == nil || got != (BackendImageInspection{}) || calls != 1 || strings.Contains(err.Error(), "fixture-sensitive") {
				t.Fatalf("unsafe wire response: %+v %v calls=%d reference=%s", got, err, calls, reference)
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	calls := 0
	_, err := inspectBackendImageHTTP(ctx, imageFixtureClient(t, imageResponseFixture(reference), reference, &calls), raw, digest, "amd64")
	if !errors.Is(err, context.Canceled) || calls != 0 {
		t.Fatal("canceled request queried daemon", err, calls)
	}
}

func TestBackendImageInspectionDiscardsCanceledResponse(t *testing.T) {
	raw, digest, reference := managedImageFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client := daemonClient(transportFunc(func(r *http.Request) (*http.Response, error) {
		encoded, err := json.Marshal(imageResponseFixture(reference))
		if err != nil {
			t.Fatal(err)
		}
		cancel()
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(encoded)))}, nil
	}))
	got, err := inspectBackendImageHTTP(ctx, client, raw, digest, "amd64")
	if !errors.Is(err, context.Canceled) || got != (BackendImageInspection{}) {
		t.Fatalf("late observation escaped canceled request: %+v %v", got, err)
	}
}

func TestBackendImageDockerHubReferenceForms(t *testing.T) {
	digest := "@sha256:" + strings.Repeat("a", 64)
	for _, observed := range []string{"docker.io/library/busybox", "library/busybox", "busybox"} {
		if !matchesImageReference(observed+digest, "docker.io/library/busybox"+digest) {
			t.Fatal("Docker familiar form refused", observed)
		}
	}
	for _, observed := range []string{"busybox", "library/busybox", "docker.io/library/other", "evil.invalid/library/busybox", "docker.io/library/busybox:latest"} {
		wanted := "ghcr.io/example/backend" + digest
		if matchesImageReference(observed+digest, wanted) {
			t.Fatal("foreign repository adopted", observed)
		}
	}
	if matchesImageReference("busybox@sha256:"+strings.Repeat("b", 64), "docker.io/library/busybox"+digest) {
		t.Fatal("familiar form changed digest")
	}
}
