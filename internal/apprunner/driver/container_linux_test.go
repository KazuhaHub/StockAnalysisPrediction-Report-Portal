//go:build linux

package driver

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestDockerAcceptanceCandidateContainer(t *testing.T) {
	if os.Getenv("PORTAL_APP_RUNNER_CONTAINER_ACCEPTANCE") != "1" {
		t.Skip("explicit disposable container acceptance not enabled")
	}
	if os.Getenv("GITHUB_ACTIONS") != "true" || os.Getenv("RUNNER_ENVIRONMENT") != "github-hosted" || os.Geteuid() != 0 {
		t.Fatal("container acceptance requires root on a disposable GitHub-hosted Linux runner")
	}
	socket, reference := os.Getenv("PORTAL_APP_RUNNER_ACCEPTANCE_SOCKET"), os.Getenv("PORTAL_APP_RUNNER_ACCEPTANCE_IMAGE")
	if socket == "" || reference == "" {
		t.Fatal("explicit safe daemon and digest-pinned fixture image required")
	}
	root, err := os.MkdirTemp("/run", "portal-candidate-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(root); err != nil {
			t.Error(err)
		}
	})
	raw, _, _ := managedImageFixture(t)
	var manifest map[string]any
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	backend := manifest["backend"].(map[string]any)
	backend["image"], backend["platforms"] = reference, []string{"linux/" + runtime.GOARCH}
	raw, err = json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	// Reuse only synthetic identity fields, then rebind the exact fixture bytes.
	fixture := containerFixture(t)
	owner := fixture.owner
	owner.ReleaseSHA256 = containerHash(raw)
	image, err := InspectBackendImage(context.Background(), socket, raw, owner.ReleaseSHA256)
	if err != nil {
		t.Fatal(err)
	}
	roots := containerRoots{Data: filepath.Join(root, "data"), Sockets: filepath.Join(root, "sockets")}
	plan, err := planCandidateContainer(raw, image, owner, roots)
	if err != nil {
		t.Fatal(err)
	}
	var policy containerCreate
	if err := json.Unmarshal(plan.body, &policy); err != nil {
		t.Fatal(err)
	}
	// These empty private mounts are disposable create/inspect fixtures, not
	// quota/writer admission evidence. This test never starts the container.
	for _, mount := range policy.HostConfig.Mounts {
		if err := os.MkdirAll(mount.Source, 0700); err != nil {
			t.Fatal(err)
		}
	}
	transport := &http.Transport{Proxy: nil, DisableKeepAlives: true, DialContext: dockerDialer(socket)}
	defer transport.CloseIdleConnections()
	client := daemonClient(transport)
	nativeID, err := createCandidateContainerHTTP(context.Background(), client, plan)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := removeCandidateContainerHTTP(context.Background(), client, nativeID, plan); err != nil {
			t.Error("owned candidate cleanup requires reconciliation", err)
		}
	})
	state, err := inspectCandidateContainerHTTP(context.Background(), client, nativeID, plan)
	if err != nil || state != "created" {
		t.Fatalf("created container policy observation failed: %s %v", state, err)
	}
	t.Log("created and inspected an owned, stopped candidate with the closed container policy")
}
