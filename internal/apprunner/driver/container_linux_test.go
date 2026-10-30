//go:build linux

package driver

import (
	"bytes"
	"context"
	"encoding/json"
	"golang.org/x/sys/unix"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestDockerAcceptanceCandidateContainer(t *testing.T) {
	runDockerCandidateAcceptance(t, "PORTAL_APP_RUNNER_ACCEPTANCE_IMAGE", false)
}
func TestDockerAcceptanceCandidateLifecycle(t *testing.T) {
	runDockerCandidateAcceptance(t, "PORTAL_APP_RUNNER_LIFECYCLE_IMAGE", true)
}
func runDockerCandidateAcceptance(t *testing.T, imageEnvironment string, start bool) {
	if os.Getenv("PORTAL_APP_RUNNER_CONTAINER_ACCEPTANCE") != "1" {
		t.Skip("explicit disposable container acceptance not enabled")
	}
	if os.Getenv("GITHUB_ACTIONS") != "true" || os.Getenv("RUNNER_ENVIRONMENT") != "github-hosted" || os.Geteuid() != 0 {
		t.Fatal("container acceptance requires root on a disposable GitHub-hosted Linux runner")
	}
	socket, reference := os.Getenv("PORTAL_APP_RUNNER_ACCEPTANCE_SOCKET"), os.Getenv(imageEnvironment)
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
	// quota/writer admission evidence. Only the explicit lifecycle case starts
	// the official inert fixture; no application health/data access is claimed.
	for _, mount := range policy.HostConfig.Mounts {
		if mount.Target == "/run/portal-app/http" {
			if err := os.MkdirAll(filepath.Dir(mount.Source), 0700); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if err := os.MkdirAll(mount.Source, 0700); err != nil {
			t.Fatal(err)
		}
		if mount.Target == "/run/portal-app/broker" {
			if err := os.Chmod(mount.Source, 0755); err != nil {
				t.Fatal(err)
			}
		}
	}
	provisionNativeIPCFixture(t, policy.HostConfig.Mounts[1].Source, CandidateIdentity(owner))
	var dataStat unix.Stat_t
	if err := unix.Stat(policy.HostConfig.Mounts[0].Source, &dataStat); err != nil {
		t.Fatal(err)
	}
	pins, err := pinCandidateMounts(context.Background(), plan, candidateDataPin{Device: uint64(dataStat.Dev), Inode: dataStat.Ino})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := pins.Close(); err != nil {
			t.Error(err)
		}
	})
	transport := &http.Transport{Proxy: nil, DisableKeepAlives: true, DialContext: dockerDialer(socket)}
	defer transport.CloseIdleConnections()
	client := daemonClient(transport)
	// Only the explicitly disposable, credential-free fixture reports the
	// bounded Docker start error. Production driver errors remain sanitized.
	client.Transport = transportFunc(func(request *http.Request) (*http.Response, error) {
		response, err := transport.RoundTrip(request)
		if err == nil && request.Method == http.MethodPost && strings.HasSuffix(request.URL.Path, "/start") && response.StatusCode >= 400 {
			raw, readErr := io.ReadAll(io.LimitReader(response.Body, 4096))
			response.Body.Close()
			response.Body = io.NopCloser(bytes.NewReader(raw))
			var message struct{ Message string }
			if readErr == nil && json.Unmarshal(raw, &message) == nil {
				t.Logf("disposable candidate start refused (%d): %s", response.StatusCode, message.Message)
			}
		}
		return response, err
	})
	nativeID, err := createPinnedCandidateHTTP(context.Background(), client, plan, pins)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if start {
			if _, err := stopCandidateContainerHTTP(context.Background(), client, nativeID, plan); err != nil {
				t.Error("owned lifecycle stop requires reconciliation", err)
				return
			}
		}
		if err := removeCandidateContainerHTTP(context.Background(), client, nativeID, plan); err != nil {
			t.Error("owned candidate cleanup requires reconciliation", err)
		}
	})
	state, err := inspectCandidateContainerHTTP(context.Background(), client, nativeID, plan)
	if err != nil || state != "created" {
		t.Fatalf("created container policy observation failed: %s %v", state, err)
	}
	if start {
		state, err = startPinnedCandidateHTTP(context.Background(), client, nativeID, plan, pins)
		if err != nil || state != "running" {
			logDisposableCandidateFailure(t, client, nativeID)
			t.Fatalf("candidate start observation: %s %v", state, err)
		}
		if err := removeCandidateContainerHTTP(context.Background(), client, nativeID, plan); err != ErrContainerConflict {
			t.Fatalf("running candidate removal was not refused: %v", err)
		}
		state, err = stopCandidateContainerHTTP(context.Background(), client, nativeID, plan)
		if err != nil || state != "exited" {
			t.Fatalf("candidate stop observation: %s %v", state, err)
		}
		if _, err := startPinnedCandidateHTTP(context.Background(), client, nativeID, plan, pins); err != ErrContainerConflict {
			t.Fatalf("exited candidate restarted without new authority: %v", err)
		}
		t.Log("owned candidate ran, refused removal while running, stopped and refused automatic restart")
	} else {
		t.Log("created and inspected an owned, stopped candidate with the closed container policy")
	}
}

// Observe only the exact owned disposable fixture after a failed start. This
// never retries start or repairs policy; no production container uses this path.
func logDisposableCandidateFailure(t *testing.T, client *http.Client, nativeID string) {
	t.Helper()
	if !containerSHA.MatchString(nativeID) {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	defer cancel()
	var diagnostic struct {
		State struct {
			Status, Error string
			ExitCode      int
		}
	}
	if err := readJSON(ctx, client, "/v"+APIVersion+"/containers/"+nativeID+"/json", &diagnostic); err != nil {
		t.Log("disposable candidate failure state unavailable", err)
		return
	}
	t.Logf("disposable candidate state=%s exit=%d error=%.2048q", diagnostic.State.Status, diagnostic.State.ExitCode, diagnostic.State.Error)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://app-runner-docker.invalid/v"+APIVersion+"/containers/"+nativeID+"/logs?stdout=true&stderr=true&tail=10", nil)
	if err != nil {
		return
	}
	response, err := client.Do(request)
	if err != nil {
		return
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusOK {
		raw, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		t.Logf("disposable candidate output: %q", raw)
	}
}
