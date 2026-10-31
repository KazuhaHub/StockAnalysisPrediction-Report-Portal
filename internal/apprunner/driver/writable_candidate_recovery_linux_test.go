//go:build linux

package driver

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWritableRecoveryInventoryRequiresUniquePrivateReference(t *testing.T) {
	raw, digest, reference := managedImageFixture(t)
	owner := containerFixture(t).owner
	owner.WriterFence = "1"
	// The complete Unix socket path, including the installation UUID, must fit
	// the production 107-byte bound. Test names make t.TempDir paths too long.
	root, err := os.MkdirTemp("/tmp", "portal-wr-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	plan, err := planWritableCandidateContainer(raw, BackendImageInspection{ReleaseSHA256: digest, Reference: reference, ImageID: "sha256:" + strings.Repeat("b", 64), Platform: "linux/amd64"}, owner, containerRoots{Data: filepath.Join(root, "data"), Sockets: filepath.Join(root, "sockets")}, writableBindingFixture())
	if err != nil {
		t.Fatal(err)
	}
	var policy containerCreate
	json.Unmarshal(plan.body, &policy)
	for _, mount := range policy.HostConfig.Mounts {
		if err := os.MkdirAll(mount.Source, 0700); err != nil {
			t.Fatal(err)
		}
	}
	target := policy.HostConfig.Mounts[0].Source
	alias := filepath.Join(root, "alias")
	if err := os.Symlink(target, alias); err != nil {
		t.Fatal(err)
	}
	id := strings.Repeat("a", 64)
	entry := func(id, source string) map[string]any {
		return map[string]any{"Id": id, "Mounts": []map[string]any{{"Type": "bind", "Source": source}}, "State": "exited"}
	}
	cases := []struct {
		name    string
		entries []map[string]any
		want    bool
	}{
		{"single", []map[string]any{entry(id, target)}, true},
		{"none", []map[string]any{}, false},
		{"duplicate", []map[string]any{entry(id, target), entry(id, target)}, false},
		{"other-data", []map[string]any{entry(id, target), entry(strings.Repeat("c", 64), target)}, false},
		{"other-alias", []map[string]any{entry(id, target), entry(strings.Repeat("c", 64), alias)}, false},
		{"other-parent", []map[string]any{entry(id, target), entry(strings.Repeat("c", 64), filepath.Dir(target))}, false},
		{"other-http", []map[string]any{entry(id, target), entry(strings.Repeat("c", 64), policy.HostConfig.Mounts[1].Source)}, false},
		{"missing-source", []map[string]any{entry(id, filepath.Join(root, "missing"))}, false},
		{"invalid-id", []map[string]any{entry("name-only", target)}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			encoded, _ := json.Marshal(tc.entries)
			calls := 0
			client := daemonClient(transportFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				if r.Method != "GET" || r.URL.Path != "/v"+APIVersion+"/containers/json" || r.URL.Query().Get("all") != "1" {
					t.Fatal("recovery inventory mutated or omitted stopped entries")
				}
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(encoded)))}, nil
			}))
			got, err := writableCandidateReference(context.Background(), client, plan)
			if (err == nil) != tc.want || (tc.want && got != id) || calls != 1 {
				t.Fatal("inventory result", got, err, calls)
			}
		})
	}
}
