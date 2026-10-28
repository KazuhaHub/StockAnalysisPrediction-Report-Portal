package driver

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

func containerFixture(t *testing.T) containerPlan {
	t.Helper()
	raw, digest, reference := managedImageFixture(t)
	id := func(s string) string { return s + "0000000-0000-4000-8000-000000000000" }
	owner := containerOwner{DeploymentID: id("1"), ControllerEpoch: id("2"), InstallationID: id("3"), OperationID: id("4"), ResourceID: id("5"), NamespaceID: id("6"), ReleaseSHA256: digest, Generation: "1", ControlFence: "2", WriterFence: "3"}
	image := BackendImageInspection{ReleaseSHA256: digest, Reference: reference, ImageID: "sha256:" + strings.Repeat("b", 64), Platform: "linux/amd64"}
	p, err := planCandidateContainer(raw, image, owner, containerRoots{Data: "/srv/portal-data", Sockets: "/run/portal-apps"})
	if err != nil {
		t.Fatal(err)
	}
	return p
}
func TestContainerCreatePolicyIsClosed(t *testing.T) {
	p := containerFixture(t)
	var v map[string]any
	if err := json.Unmarshal(p.body, &v); err != nil {
		t.Fatal(err)
	}
	if v["Image"] != "sha256:"+strings.Repeat("b", 64) || v["User"] != "65532:65532" || v["NetworkDisabled"] != true {
		t.Fatal("image/user/network policy", v)
	}
	h := v["HostConfig"].(map[string]any)
	if h["NetworkMode"] != "none" || h["ReadonlyRootfs"] != true || h["Privileged"] != false || h["AutoRemove"] != false || h["CgroupnsMode"] != "private" {
		t.Fatal("host policy", h)
	}
	if h["Memory"] != float64(256<<20) || h["MemorySwap"] != h["Memory"] || h["NanoCpus"] != float64(500000000) || h["PidsLimit"] != float64(64) {
		t.Fatal("reviewed limits changed", h)
	}
	if strings.Join(toStrings(h["CapDrop"]), ",") != "ALL" || strings.Join(toStrings(h["SecurityOpt"]), ",") != "no-new-privileges:true,"+candidateSeccompOption {
		t.Fatal("capabilities changed", h)
	}
	mounts := h["Mounts"].([]any)
	if len(mounts) != 3 {
		t.Fatal("unexpected mounts", mounts)
	}
	for i, m := range mounts {
		mount := m.(map[string]any)
		if mount["Type"] != "bind" || mount["ReadOnly"] != (i != 1) {
			t.Fatal("mount authority", mount)
		}
	}
	for _, key := range []string{"Binds", "Devices", "PortBindings", "VolumesFrom", "ExtraHosts", "DeviceRequests"} {
		if _, exists := h[key]; exists {
			t.Fatal("open host option", key)
		}
	}
	if strings.Contains(string(p.body), "docker.sock") || !strings.HasPrefix(p.name, "portal-app-") {
		t.Fatal("unsafe control exposure")
	}
	if v["Healthcheck"].(map[string]any)["Test"].([]any)[0] != "NONE" {
		t.Fatal("inherited image healthcheck")
	}
}
func toStrings(v any) []string {
	result := []string{}
	for _, item := range v.([]any) {
		result = append(result, item.(string))
	}
	return result
}
func TestContainerCreateRequestAndAmbiguity(t *testing.T) {
	p := containerFixture(t)
	cases := []struct {
		name   string
		status int
		body   string
		want   error
	}{
		{"created", 201, `{"Id":"` + strings.Repeat("c", 64) + `","Warnings":[]}`, nil},
		{"conflict", 409, `{"message":"fixture-sensitive"}`, ErrContainerConflict},
		{"warning", 201, `{"Id":"` + strings.Repeat("c", 64) + `","Warnings":["limit ignored"]}`, ErrContainerUncertain},
		{"bad ID", 201, `{"Id":"name-only"}`, ErrContainerUncertain},
		{"duplicate", 201, `{"Id":"a","Id":"b"}`, ErrContainerUncertain},
		{"error", 500, `{"message":"fixture-sensitive"}`, ErrContainerUncertain},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			client := daemonClient(transportFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				if r.Method != "POST" || r.URL.Path != "/v1.47/containers/create" || r.URL.Query().Get("name") != p.name || r.Header.Get("Content-Type") != "application/json" || r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
					t.Fatal("unexpected create request", r.Method, r.URL)
				}
				body, err := io.ReadAll(r.Body)
				if err != nil || string(body) != string(p.body) {
					t.Fatal("create policy changed", err)
				}
				return &http.Response{StatusCode: tc.status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(tc.body))}, nil
			}))
			id, err := createCandidateContainerHTTP(context.Background(), client, p)
			if !errors.Is(err, tc.want) || calls != 1 {
				t.Fatalf("create result: %s %v calls=%d", id, err, calls)
			}
			if tc.want != nil && (id != "" || strings.Contains(err.Error(), "fixture-sensitive")) {
				t.Fatal("uncertain result leaked/adopted")
			}
		})
	}
}

func TestContainerPlanAndCreateRejectChangedInputs(t *testing.T) {
	p := containerFixture(t)
	raw, digest, reference := managedImageFixture(t)
	image := BackendImageInspection{ReleaseSHA256: digest, Reference: reference, ImageID: "sha256:" + strings.Repeat("b", 64), Platform: "linux/amd64"}
	for _, tc := range []struct {
		name  string
		owner containerOwner
		roots containerRoots
		image BackendImageInspection
	}{
		{"traversal namespace", func() containerOwner { o := p.owner; o.NamespaceID = "../other"; return o }(), containerRoots{Data: "/srv/data", Sockets: "/run/apps"}, image},
		{"zero fence", func() containerOwner { o := p.owner; o.ControlFence = "0"; return o }(), containerRoots{Data: "/srv/data", Sockets: "/run/apps"}, image},
		{"unreviewed bytes", func() containerOwner { o := p.owner; o.ReleaseSHA256 = strings.Repeat("f", 64); return o }(), containerRoots{Data: "/srv/data", Sockets: "/run/apps"}, image},
		{"root mount", p.owner, containerRoots{Data: "/", Sockets: "/run/apps"}, image},
		{"overlap roots", p.owner, containerRoots{Data: "/srv/data", Sockets: "/srv/data/sockets"}, image},
		{"ambiguous root", p.owner, containerRoots{Data: "/srv/../data", Sockets: "/run/apps"}, image},
		{"socket length", p.owner, containerRoots{Data: "/srv/data", Sockets: "/run/" + strings.Repeat("a", 100)}, image},
		{"unbound image", p.owner, containerRoots{Data: "/srv/data", Sockets: "/run/apps"}, func() BackendImageInspection { i := image; i.ReleaseSHA256 = strings.Repeat("f", 64); return i }()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := planCandidateContainer(raw, tc.image, tc.owner, tc.roots); err == nil {
				t.Fatal("unsafe candidate plan accepted")
			}
		})
	}
	for _, mutate := range []func(*containerPlan){func(p *containerPlan) { p.body = append(p.body, ' ') }, func(p *containerPlan) { p.name = "foreign" }, func(p *containerPlan) { p.owner.InstallationID = "90000000-0000-4000-8000-000000000000" }} {
		changed := containerFixture(t)
		mutate(&changed)
		calls := 0
		client := daemonClient(transportFunc(func(*http.Request) (*http.Response, error) {
			calls++
			return nil, errors.New("unexpected daemon request")
		}))
		if _, err := createCandidateContainerHTTP(context.Background(), client, changed); err == nil || calls != 0 {
			t.Fatal("changed plan dispatched", err, calls)
		}
	}
}
