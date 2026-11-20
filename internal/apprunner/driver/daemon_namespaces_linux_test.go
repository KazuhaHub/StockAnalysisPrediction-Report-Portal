//go:build linux

package driver

import (
	"context"
	"errors"
	"testing"
)

func fakeMountNamespace(t *testing.T, shared func(pid int) bool) {
	t.Helper()
	old := daemonInRunnerMountNamespace
	daemonInRunnerMountNamespace = shared
	t.Cleanup(func() { daemonInRunnerMountNamespace = old })
}

// Doc 187 section 11, fail fast on the mount and network namespaces. Every case is a refusal, never a fallback.
func TestCheckDaemonNamespacesAtStart(t *testing.T) {
	socket := unixDaemon(t)
	acceptExecutable(t, func(string) bool { return true })

	fakeMountNamespace(t, func(int) bool { return true })
	fakeNetworkNamespace(t, func(int) bool { return true })
	if err := checkDaemonNamespaces(context.Background(), realPeerClient(socket)); err != nil {
		t.Fatalf("a daemon in both of the runner's namespaces was refused: %v", err)
	}

	// A plain privileged container: the daemon is in the host's mount namespace, the runner is not.
	fakeMountNamespace(t, func(int) bool { return false })
	err := checkDaemonNamespaces(context.Background(), realPeerClient(socket))
	if !errors.Is(err, ErrUnavailable) || errors.Is(err, ErrHostNetworkNamespace) {
		t.Fatalf("another mount namespace must be refused as such: %v", err)
	}

	// dockerd in another network namespace than the runner.
	fakeMountNamespace(t, func(int) bool { return true })
	fakeNetworkNamespace(t, func(int) bool { return false })
	if err := checkDaemonNamespaces(context.Background(), realPeerClient(socket)); !errors.Is(err, ErrHostNetworkNamespace) {
		t.Fatalf("another network namespace: %v", err)
	}

	// A holder that is not a dockerd cannot be identified, so it is refused too.
	fakeNetworkNamespace(t, func(int) bool { return true })
	acceptExecutable(t, func(string) bool { return false })
	if err := checkDaemonNamespaces(context.Background(), realPeerClient(socket)); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("an unidentified daemon: %v", err)
	}

	// No daemon at all is a failure, never a pass.
	if err := CheckDaemonNamespaces(context.Background(), socket+".missing"); err == nil {
		t.Fatal("a missing socket passed")
	}
}
