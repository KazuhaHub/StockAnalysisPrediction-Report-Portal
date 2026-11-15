//go:build linux

package driver

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// BenchmarkRelayedRequestDriverWork is the daemon-facing work of one relayed request against a fake daemon.
// A relayed request is two native steps (handshake, request); each brackets its IO with an inventory pass and
// an inspection before and after, so the request makes four of each. The inventory lists a realistic fleet.
func BenchmarkRelayedRequestDriverWork(b *testing.B) {
	t := &testing.T{}
	raw, digest, reference := managedImageFixture(t)
	owner := containerFixture(t).owner
	owner.WriterFence = "1"
	root, err := os.MkdirTemp("/tmp", "portal-wb-")
	if err != nil {
		b.Fatal(err)
	}
	defer os.RemoveAll(root)
	plan, err := planWritableCandidateContainer(raw, BackendImageInspection{ReleaseSHA256: digest, Reference: reference, ImageID: "sha256:" + strings.Repeat("b", 64), Platform: "linux/amd64"}, owner, containerRoots{Data: filepath.Join(root, "data"), Sockets: filepath.Join(root, "sockets")}, writableBindingFixture())
	if err != nil {
		b.Fatal(err)
	}
	var policy containerCreate
	json.Unmarshal(plan.body, &policy)
	for _, mount := range policy.HostConfig.Mounts {
		if err := os.MkdirAll(mount.Source, 0700); err != nil {
			b.Fatal(err)
		}
	}
	id := strings.Repeat("a", 64)
	list := []map[string]any{}
	mountsOf := func(sources ...string) []map[string]any {
		out := []map[string]any{}
		for _, s := range sources {
			out = append(out, map[string]any{"Type": "bind", "Source": s, "Destination": "/x", "Mode": "rw", "RW": true, "Propagation": "rprivate"})
		}
		return out
	}
	entry := func(id string, sources ...string) map[string]any {
		return map[string]any{"Id": id, "Names": []string{"/app-" + id[:8]}, "Image": "sha256:" + strings.Repeat("b", 64), "ImageID": "sha256:" + strings.Repeat("b", 64), "Command": "/app", "Created": 1760000000, "Ports": []any{}, "Labels": map[string]string{"portal.app/owner": "x", "a": "b"}, "State": "running", "Status": "Up 3 minutes", "HostConfig": map[string]any{"NetworkMode": "none"}, "NetworkSettings": map[string]any{"Networks": map[string]any{"none": map[string]any{"NetworkID": strings.Repeat("0", 64), "EndpointID": ""}}}, "Mounts": mountsOf(sources...)}
	}
	list = append(list, entry(id, policy.HostConfig.Mounts[0].Source, policy.HostConfig.Mounts[1].Source, policy.HostConfig.Mounts[2].Source))
	for i := 0; i < 11; i++ {
		d := filepath.Join(root, "other", string(rune('a'+i)))
		os.MkdirAll(d, 0700)
		list = append(list, entry(fmt.Sprintf("%064x", i+1), d, filepath.Join(d, "sockets"), "/var/run/docker.sock"))
	}
	listing, _ := json.Marshal(list)
	doc := containerInspectFixture(t, plan)
	doc["Id"] = id
	doc["State"] = map[string]any{"Status": "running", "Running": true, "Paused": false, "Restarting": false, "Dead": false, "Pid": 4242, "StartedAt": "2026-11-14T00:00:00.000000000Z"}
	doc["GraphDriver"] = map[string]any{"Name": "overlay2", "Data": map[string]any{"LowerDir": strings.Repeat("/var/lib/docker/overlay2/abc/diff:", 12), "MergedDir": "/m", "UpperDir": "/u", "WorkDir": "/w"}}
	doc["NetworkSettings"] = map[string]any{"Bridge": "", "SandboxID": strings.Repeat("0", 64), "Ports": map[string]any{}, "Networks": map[string]any{"none": map[string]any{"NetworkID": strings.Repeat("0", 64), "EndpointID": ""}}}
	doc["Path"], doc["Args"], doc["Created"] = "/app", []string{}, "2026-11-14T00:00:00.000000000Z"
	inspect, _ := json.Marshal(doc)
	client := daemonClient(transportFunc(func(r *http.Request) (*http.Response, error) {
		body := string(inspect)
		if strings.HasSuffix(r.URL.Path, "/containers/json") {
			body = string(listing)
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	}))
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for step := 0; step < 2; step++ {
			for pass := 0; pass < 2; pass++ {
				if got, err := managedCandidateReference(ctx, client, plan); err != nil || got != id {
					b.Fatal("reference", got, err)
				}
				if state, err := inspectCandidateContainerHTTP(ctx, client, id, plan); err != nil || state != "running" {
					b.Fatal("inspect", state, err)
				}
			}
		}
	}
}
