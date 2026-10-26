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

func containerInspectFixture(t *testing.T, plan containerPlan) map[string]any {
	t.Helper()
	var request map[string]any
	if err := json.Unmarshal(plan.body, &request); err != nil {
		t.Fatal(err)
	}
	mounts := []any{}
	for _, item := range request["HostConfig"].(map[string]any)["Mounts"].([]any) {
		m := item.(map[string]any)
		mounts = append(mounts, map[string]any{"Type": "bind", "Source": m["Source"], "Destination": m["Target"], "RW": !m["ReadOnly"].(bool), "Propagation": "rprivate"})
	}
	return map[string]any{"Id": strings.Repeat("c", 64), "Name": "/" + plan.name, "Image": request["Image"], "Config": request, "HostConfig": request["HostConfig"], "Mounts": mounts, "State": map[string]any{"Status": "created", "Running": false, "Paused": false, "Restarting": false, "Dead": false}}
}
func TestContainerObservationRejectsOwnershipAndPolicySubstitution(t *testing.T) {
	plan := containerFixture(t)
	cases := map[string]func(map[string]any){
		"native id": func(v map[string]any) { v["Id"] = strings.Repeat("d", 64) },
		"name":      func(v map[string]any) { v["Name"] = "/other" },
		"image":     func(v map[string]any) { v["Image"] = "sha256:" + strings.Repeat("d", 64) },
		"root user": func(v map[string]any) { v["Config"].(map[string]any)["User"] = "0" },
		"stdin":     func(v map[string]any) { v["Config"].(map[string]any)["OpenStdin"] = true },
		"env": func(v map[string]any) {
			v["Config"].(map[string]any)["Env"] = []string{"PORTAL_APP_HTTP_SOCKET=/other"}
		},
		"generation": func(v map[string]any) {
			v["Config"].(map[string]any)["Labels"].(map[string]any)[containerLabelPrefix+"generation"] = "99"
		},
		"unowned label": func(v map[string]any) {
			v["Config"].(map[string]any)["Labels"].(map[string]any)[containerLabelPrefix+"extra"] = "foreign"
		},
		"network":    func(v map[string]any) { v["HostConfig"].(map[string]any)["NetworkMode"] = "host" },
		"privileged": func(v map[string]any) { v["HostConfig"].(map[string]any)["Privileged"] = true },
		"capability": func(v map[string]any) { v["HostConfig"].(map[string]any)["CapAdd"] = []string{"SYS_ADMIN"} },
		"host port": func(v map[string]any) {
			v["HostConfig"].(map[string]any)["PortBindings"] = map[string]any{"80/tcp": []any{map[string]any{"HostPort": "8080"}}}
		},
		"unbounded log": func(v map[string]any) {
			v["HostConfig"].(map[string]any)["LogConfig"] = map[string]any{"Type": "json-file"}
		},
		"missing limit": func(v map[string]any) { delete(v["HostConfig"].(map[string]any), "Memory") },
		"data writable": func(v map[string]any) { v["Mounts"].([]any)[0].(map[string]any)["RW"] = true },
		"other data":    func(v map[string]any) { v["Mounts"].([]any)[0].(map[string]any)["Source"] = "/srv/other" },
		"extra mount": func(v map[string]any) {
			v["Mounts"] = append(v["Mounts"].([]any), map[string]any{"Type": "bind", "Source": "/", "Destination": "/host", "RW": true})
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			v := containerInspectFixture(t, plan)
			mutate(v)
			calls := 0
			client := daemonClient(transportFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				if r.Method != "GET" {
					t.Fatal("unsafe observation caused mutation", r.Method)
				}
				raw, _ := json.Marshal(v)
				return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(raw)))}, nil
			}))
			err := removeCandidateContainerHTTP(context.Background(), client, strings.Repeat("c", 64), plan)
			if !errors.Is(err, ErrContainerConflict) || calls != 1 {
				t.Fatalf("substituted resource adopted/removed: %v calls=%d", err, calls)
			}
		})
	}
}
func TestContainerRemoveRequiresExactStoppedNativeIdentity(t *testing.T) {
	plan := containerFixture(t)
	for _, state := range []string{"created", "exited", "running", "restarting"} {
		t.Run(state, func(t *testing.T) {
			calls := 0
			v := containerInspectFixture(t, plan)
			s := v["State"].(map[string]any)
			s["Status"] = state
			s["Running"] = state == "running"
			s["Restarting"] = state == "restarting"
			client := daemonClient(transportFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				if calls == 1 {
					if r.Method != "GET" || r.URL.Path != "/v1.47/containers/"+strings.Repeat("c", 64)+"/json" {
						t.Fatal("unbound inspection", r.Method, r.URL)
					}
					raw, _ := json.Marshal(v)
					return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(raw)))}, nil
				}
				if r.Method != "DELETE" || r.URL.Path != "/v1.47/containers/"+strings.Repeat("c", 64) || r.URL.Query().Get("force") != "false" || r.URL.Query().Get("v") != "false" {
					t.Fatal("unsafe deletion", r.Method, r.URL)
				}
				return &http.Response{StatusCode: 204, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(""))}, nil
			}))
			err := removeCandidateContainerHTTP(context.Background(), client, strings.Repeat("c", 64), plan)
			if state == "created" || state == "exited" {
				if err != nil || calls != 2 {
					t.Fatalf("owned stopped resource: %v calls=%d", err, calls)
				}
			} else if err == nil || calls != 1 {
				t.Fatal("running/uncertain resource removed", err, calls)
			}
		})
	}
}
