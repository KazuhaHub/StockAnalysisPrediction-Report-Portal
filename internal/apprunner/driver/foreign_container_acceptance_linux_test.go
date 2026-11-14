//go:build linux

package driver

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// A host runs containers the runner did not start (a database service, another
// stack). Their presence must not stop the data writer: only a mount that names
// a path under the namespace may conflict.
func TestDockerAcceptanceForeignContainerBesideDataWriterInventory(t *testing.T) {
	if os.Getenv("PORTAL_APP_RUNNER_FOREIGN_ACCEPTANCE") != "1" {
		t.Skip("explicit disposable foreign container acceptance not enabled")
	}
	if os.Getenv("GITHUB_ACTIONS") != "true" || os.Getenv("RUNNER_ENVIRONMENT") != "github-hosted" || os.Geteuid() != 0 {
		t.Fatal("foreign container acceptance requires root on a disposable GitHub-hosted Linux runner")
	}
	socket := os.Getenv("PORTAL_APP_RUNNER_ACCEPTANCE_SOCKET")
	if socket == "" {
		t.Fatal("explicit daemon socket required")
	}
	client, closeClient := disposableDockerClient(socket)
	defer closeClient()
	entries, err := readCandidateIPCInventory(context.Background(), client)
	if err != nil {
		t.Fatalf("inventory with a foreign container: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("the foreign service container is not in the inventory")
	}
	for _, entry := range entries {
		sources, err := candidateIPCMountSources(entry.Mounts)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("foreign container %s mounts %v", shortContainerID(entry.ID), sources)
	}
	root, err := os.MkdirTemp("/run", "portal-foreign-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	path := filepath.Join(root, "data", "00000000-0000-4000-8000-000000700001")
	if err := os.MkdirAll(path, 0700); err != nil {
		t.Fatal(err)
	}
	if err := requireNoContainerMountReferences(context.Background(), client, path); err != nil {
		t.Fatalf("a foreign container that does not mount the namespace refused the writer: %v", err)
	}
}
