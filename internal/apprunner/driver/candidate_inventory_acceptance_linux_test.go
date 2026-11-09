//go:build linux

package driver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func disposableDockerClient(
	socket string,
) (*http.Client, func()) {
	transport := &http.Transport{
		Proxy:                 nil,
		DisableKeepAlives:     true,
		ResponseHeaderTimeout: probeTimeout,
		DialContext:           dockerDialer(socket),
	}
	return daemonClient(transport), transport.CloseIdleConnections
}

func createDisposableInventoryContainer(
	t *testing.T,
	client *http.Client,
	reference, source, suffix string,
) string {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"Image": reference,
		"Labels": map[string]string{
			"fixture.portal.reconciliation": suffix,
		},
		"HostConfig": map[string]any{
			"Binds": []string{source + ":/fixture:ro"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	name := "portal-reconcile-" + suffix
	request, err := http.NewRequestWithContext(
		context.Background(), http.MethodPost,
		"http://app-runner-docker.invalid/v"+APIVersion+
			"/containers/create?name="+url.QueryEscape(name),
		bytes.NewReader(body),
	)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("create fixture container: %d", response.StatusCode)
	}
	var created struct {
		ID string `json:"Id"`
	}
	if json.NewDecoder(response.Body).Decode(&created) != nil ||
		!containerSHA.MatchString(created.ID) {
		t.Fatal("invalid fixture container identity")
	}
	t.Cleanup(func() {
		request, err := http.NewRequest(
			http.MethodDelete,
			"http://app-runner-docker.invalid/v"+APIVersion+
				"/containers/"+created.ID+"?force=false&v=false",
			nil,
		)
		if err != nil {
			t.Error(err)
			return
		}
		response, err := client.Do(request)
		if err != nil {
			t.Errorf("delete fixture container: %v", err)
			return
		}
		response.Body.Close()
		if response.StatusCode != http.StatusNoContent {
			t.Errorf("delete fixture container: %d", response.StatusCode)
		}
	})
	return created.ID
}

func disposableInventoryReferenceCount(
	t *testing.T,
	client *http.Client,
	source string,
) int {
	t.Helper()
	entries, err := readCandidateIPCInventory(
		context.Background(), client)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, entry := range entries {
		sources, err := candidateIPCMountSources(entry.Mounts)
		if err != nil {
			t.Fatal(err)
		}
		for _, mounted := range sources {
			matches, err := candidateIPCReferenced(
				context.Background(), source, mounted)
			if err != nil {
				t.Fatal(err)
			}
			if matches {
				count++
				break
			}
		}
	}
	return count
}

func TestDockerAcceptanceCandidateInventoryReconciliation(
	t *testing.T,
) {
	if os.Getenv("PORTAL_APP_RUNNER_CONTAINER_ACCEPTANCE") != "1" {
		t.Skip("explicit disposable container acceptance not enabled")
	}
	if os.Getenv("GITHUB_ACTIONS") != "true" ||
		os.Getenv("RUNNER_ENVIRONMENT") != "github-hosted" ||
		os.Geteuid() != 0 {
		t.Fatal("container acceptance requires root on a disposable GitHub-hosted Linux runner")
	}
	socket := os.Getenv("PORTAL_APP_RUNNER_ACCEPTANCE_SOCKET")
	reference := os.Getenv("PORTAL_APP_RUNNER_LIFECYCLE_IMAGE")
	if socket == "" || reference == "" {
		t.Fatal("explicit safe daemon and fixture image required")
	}
	raw, _, _ := managedImageFixture(t)
	var manifest map[string]any
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	backend := manifest["backend"].(map[string]any)
	backend["image"], backend["platforms"] = reference,
		[]string{"linux/" + runtime.GOARCH}
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	owner := containerFixture(t).owner
	owner.WriterFence = "1"
	owner.ReleaseSHA256 = containerHash(raw)
	image, err := InspectBackendImage(
		context.Background(), socket, raw, owner.ReleaseSHA256, ImagePolicy{AllowLocalReference: true})
	if err != nil {
		t.Fatal(err)
	}
	root, err := os.MkdirTemp("/run", "portal-reconcile-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(root); err != nil {
			t.Error(err)
		}
	})
	plan, err := planWritableCandidateContainer(
		raw, image, owner,
		containerRoots{
			Data:    filepath.Join(root, "data"),
			Sockets: filepath.Join(root, "sockets"),
		},
		writableBindingFixture(),
	)
	if err != nil {
		t.Fatal(err)
	}
	var policy containerCreate
	if json.Unmarshal(plan.body, &policy) != nil ||
		len(policy.HostConfig.Mounts) != 3 {
		t.Fatal("invalid writable candidate plan")
	}
	source := policy.HostConfig.Mounts[0].Source
	if err := os.MkdirAll(source, 0700); err != nil {
		t.Fatal(err)
	}

	client, closeClient := disposableDockerClient(socket)
	defer closeClient()
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	stale := createDisposableInventoryContainer(
		t, client, reference, source, suffix+"-stale")

	// A restart may discover a resource that references the exact private
	// mount, but the reference alone is not adoption authority.
	restarted, closeRestarted := disposableDockerClient(socket)
	defer closeRestarted()
	native, err := managedCandidateReference(
		context.Background(), restarted, plan)
	if err != nil || native != stale {
		t.Fatalf("stale resource reference was not observed exactly: %q %v",
			native, err)
	}
	if _, err := inspectCandidateContainerHTTP(
		context.Background(), restarted, stale, plan,
	); !errors.Is(err, ErrContainerConflict) {
		t.Fatalf("stale resource policy was adopted: %v", err)
	}
	if got := disposableInventoryReferenceCount(
		t, restarted, source,
	); got != 1 {
		t.Fatalf("stale observation changed resource count: %d", got)
	}

	duplicate := createDisposableInventoryContainer(
		t, restarted, reference, source, suffix+"-duplicate")
	if _, err := managedCandidateReference(
		context.Background(), restarted, plan,
	); !errors.Is(err, ErrContainerConflict) {
		t.Fatalf("duplicate resource reference was not ambiguous: %v", err)
	}
	if got := disposableInventoryReferenceCount(
		t, restarted, source,
	); got != 2 {
		t.Fatalf("duplicate reconciliation changed resource count: %d", got)
	}

	secondRestart, closeSecond := disposableDockerClient(socket)
	defer closeSecond()
	if _, err := managedCandidateReference(
		context.Background(), secondRestart, plan,
	); !errors.Is(err, ErrContainerConflict) {
		t.Fatalf("restart guessed among duplicate resources: %v", err)
	}
	entries, err := readCandidateIPCInventory(
		context.Background(), secondRestart)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, entry := range entries {
		sources, err := candidateIPCMountSources(entry.Mounts)
		if err != nil {
			t.Fatal(err)
		}
		for _, mounted := range sources {
			matches, err := candidateIPCReferenced(
				context.Background(), source, mounted)
			if err != nil {
				t.Fatal(err)
			}
			if matches {
				seen[entry.ID] = true
			}
		}
	}
	if !seen[stale] || !seen[duplicate] || len(seen) != 2 {
		t.Fatalf("failed reconciliation deleted or rewrote test-owned resources: %v",
			seen)
	}
	if strings.Contains(stale, duplicate) ||
		!containerSHA.MatchString(stale) ||
		!containerSHA.MatchString(duplicate) {
		t.Fatal("fixture resource identity was not distinct")
	}
	t.Log("CANDIDATE_STALE_ORPHAN_DUPLICATE_RECONCILIATION_VERIFIED")
}
