//go:build linux

package driver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A local volume created with type=none,o=bind,device=<namespace> is listed by the daemon with a
// /var/lib/docker/volumes/<id>/_data source, which names no overlap. The inventory must still refuse it, and a
// created container whose bind source does not exist yet under the namespace too. Runs against a real daemon as
// root on a disposable runner (the foreign container job of the app runner probe workflow).
func TestDockerAcceptanceBindBackedVolumeIsAConflict(t *testing.T) {
	if os.Getenv("PORTAL_APP_RUNNER_FOREIGN_ACCEPTANCE") != "1" {
		t.Skip("explicit disposable foreign container acceptance not enabled")
	}
	if os.Getenv("GITHUB_ACTIONS") != "true" || os.Getenv("RUNNER_ENVIRONMENT") != "github-hosted" || os.Geteuid() != 0 {
		t.Fatal("bind volume acceptance requires root on a disposable GitHub-hosted Linux runner")
	}
	socket := os.Getenv("PORTAL_APP_RUNNER_ACCEPTANCE_SOCKET")
	if socket == "" {
		t.Fatal("explicit daemon socket required")
	}
	// The fixtures need routes the runner's allow-list refuses, so they use a plain client. The inventory under
	// test uses the runner's own client, which also checks the daemon's mount namespace.
	fixture := &http.Client{Transport: &http.Transport{DisableKeepAlives: true, DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}}, Timeout: 60 * time.Second}
	client, closeClient := newDockerClient(socket, 10*time.Second, 30*time.Second)
	defer closeClient()

	root, err := os.MkdirTemp("/run", "portal-bindvol-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	namespace := filepath.Join(root, "data", "00000000-0000-4000-8000-000000700002")
	sibling := filepath.Join(root, "data", "00000000-0000-4000-8000-000000700003")
	stopped := filepath.Join(root, "data", "00000000-0000-4000-8000-000000700004")
	for _, dir := range []string{namespace, sibling, stopped} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	call := func(method, path string, body any, want ...int) []byte {
		t.Helper()
		var reader io.Reader
		if body != nil {
			raw, _ := json.Marshal(body)
			reader = bytes.NewReader(raw)
		}
		request, err := http.NewRequest(method, "http://docker.invalid/v"+APIVersion+path, reader)
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Content-Type", "application/json")
		response, err := fixture.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		out, _ := io.ReadAll(response.Body)
		for _, code := range want {
			if response.StatusCode == code {
				return out
			}
		}
		t.Fatalf("%s %s: %d %s", method, path, response.StatusCode, out)
		return nil
	}
	// The image is the one the job's service container already pulled; nothing is pulled here.
	const image = "postgres:16-alpine"
	container := func(name string, binds []string, start bool) string {
		t.Helper()
		out := call(http.MethodPost, "/containers/create?name="+url.QueryEscape(name), map[string]any{
			"Image": image, "Entrypoint": []string{"sleep"}, "Cmd": []string{"300"},
			"HostConfig": map[string]any{"Binds": binds},
		}, http.StatusCreated)
		var created struct {
			ID string `json:"Id"`
		}
		if json.Unmarshal(out, &created) != nil || !containerSHA.MatchString(created.ID) {
			t.Fatal("invalid fixture container identity")
		}
		t.Cleanup(func() {
			call(http.MethodDelete, "/containers/"+created.ID+"?force=1&v=0", nil, http.StatusNoContent, http.StatusNotFound)
		})
		if start {
			call(http.MethodPost, "/containers/"+created.ID+"/start", nil, http.StatusNoContent)
		}
		return created.ID
	}

	suffix := strings.ToLower(filepath.Base(root))
	volume := "portal-bindvol-" + suffix
	call(http.MethodPost, "/volumes/create", map[string]any{
		"Name": volume, "Driver": "local",
		"DriverOpts": map[string]string{"type": "none", "o": "bind", "device": namespace},
	}, http.StatusCreated)
	t.Cleanup(func() {
		call(http.MethodDelete, "/volumes/"+volume+"?force=1", nil, http.StatusNoContent, http.StatusNotFound, http.StatusConflict)
	})
	// The volume is mounted on the host while a container uses it, so the container must run.
	id := container("portal-bindvol-run-"+suffix, []string{volume + ":/v"}, true)

	entries, err := readCandidateIPCInventory(context.Background(), client)
	if err != nil {
		t.Fatal(err)
	}
	listed := false
	for _, entry := range entries {
		if entry.ID != id {
			continue
		}
		sources, err := candidateIPCContainerSources(context.Background(), client, entry)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("the daemon lists the bind-backed volume's source as %v", sources)
		for _, source := range sources {
			if source == namespace || strings.HasPrefix(source, namespace+"/") {
				t.Fatal("the daemon listed the namespace itself: the alias is not exercised")
			}
			listed = listed || strings.Contains(source, "/volumes/")
		}
	}
	if !listed {
		t.Fatal("the daemon did not list a volumes path for the bind-backed volume")
	}
	if err := requireNoContainerMountReferences(context.Background(), client, namespace); !errors.Is(err, ErrContainerConflict) {
		t.Fatalf("a container using a bind-backed volume of the namespace did not conflict: %v", err)
	}
	if err := requireNoContainerMountReferences(context.Background(), client, sibling); err != nil {
		t.Fatalf("the same container conflicted with a namespace it does not use: %v", err)
	}

	// A created container that was never started on a bind-backed volume: the daemon has not mounted the volume,
	// so its list shows only the volume's own directory and the mount table cannot see where it leads. The
	// volume's options say it, so the inventory must still conflict.
	idleVolume := "portal-bindvol-idle-" + suffix
	call(http.MethodPost, "/volumes/create", map[string]any{
		"Name": idleVolume, "Driver": "local",
		"DriverOpts": map[string]string{"type": "none", "o": "bind", "device": stopped},
	}, http.StatusCreated)
	t.Cleanup(func() {
		call(http.MethodDelete, "/volumes/"+idleVolume+"?force=1", nil, http.StatusNoContent, http.StatusNotFound, http.StatusConflict)
	})
	container("portal-bindvol-idle-"+suffix, []string{idleVolume + ":/v"}, false)
	if err := requireNoContainerMountReferences(context.Background(), client, stopped); !errors.Is(err, ErrContainerConflict) {
		t.Fatalf("a created, never started container on a bind-backed volume did not conflict: %v", err)
	}

	// A created container whose bind source does not exist yet under the namespace: Docker makes it on start.
	absent := filepath.Join(sibling, "not-yet")
	container("portal-bindvol-absent-"+suffix, []string{absent + ":/v"}, false)
	if err := requireNoContainerMountReferences(context.Background(), client, sibling); !errors.Is(err, ErrContainerConflict) {
		t.Fatalf("an absent bind source under the namespace did not conflict: %v", err)
	}
}
