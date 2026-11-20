//go:build linux

package driver

import (
	"context"
	"errors"
	"os"
	"testing"
)

func fakeNetworkNamespace(t *testing.T, shared func(pid int) bool) {
	t.Helper()
	old := daemonSharesNetworkNamespace
	daemonSharesNetworkNamespace = shared
	t.Cleanup(func() { daemonSharesNetworkNamespace = old })
}

// Fails closed: with pins configured, a daemon in another network namespace than the runner refuses every
// request, so a pinned address can never be read against the wrong namespace.
func TestHostNetworkRequirementRefusesADaemonInAnotherNamespace(t *testing.T) {
	socket := unixDaemon(t)
	acceptExecutable(t, func(string) bool { return true })

	fakeNetworkNamespace(t, func(int) bool { return true })
	client := realPeerClient(socket)
	requireHostNetworkNamespace(client)
	if err := inventoryRequest(t, client); err != nil {
		t.Fatalf("a daemon in the runner's namespace was refused: %v", err)
	}

	fakeNetworkNamespace(t, func(int) bool { return false })
	client = realPeerClient(socket)
	requireHostNetworkNamespace(client)
	if err := inventoryRequest(t, client); !errors.Is(err, ErrHostNetworkNamespace) || !errors.Is(err, ErrUnavailable) {
		t.Fatalf("a daemon in another namespace: %v", err)
	}
	// Once seen, the violation sticks to the client: no later request passes.
	if err := inventoryRequest(t, client); !errors.Is(err, ErrHostNetworkNamespace) {
		t.Fatalf("a later request: %v", err)
	}
}

// Without the requirement a daemon in another namespace is served as before: only the network-volume check
// asks about it. Fails open by design for runners with no pins.
func TestWithoutTheRequirementAnotherNamespaceStillServes(t *testing.T) {
	socket := unixDaemon(t)
	acceptExecutable(t, func(string) bool { return true })
	fakeNetworkNamespace(t, func(int) bool { return false })
	if err := inventoryRequest(t, realPeerClient(socket)); err != nil {
		t.Fatalf("a client without the requirement was refused: %v", err)
	}
}

// Fails closed: a daemon that cannot be identified (not a dockerd, so its namespace is unknown) is refused too.
func TestHostNetworkRequirementRefusesAnUnidentifiedDaemon(t *testing.T) {
	socket := unixDaemon(t)
	acceptExecutable(t, func(string) bool { return false })
	fakeNetworkNamespace(t, func(int) bool { return true })
	client := realPeerClient(socket)
	requireHostNetworkNamespace(client)
	if err := inventoryRequest(t, client); !errors.Is(err, ErrHostNetworkNamespace) {
		t.Fatalf("an unidentified daemon: %v", err)
	}
}

// The start-time check: it passes in the runner's namespace and refuses in another, with the namespace file
// unreadable counting as another. Fails closed.
func TestCheckHostNetworkNamespaceAtStart(t *testing.T) {
	socket := unixDaemon(t)
	acceptExecutable(t, func(string) bool { return true })

	fakeNetworkNamespace(t, func(int) bool { return true })
	if err := checkHostNetworkNamespace(context.Background(), realPeerClient(socket)); err != nil {
		t.Fatalf("shared namespace: %v", err)
	}
	fakeNetworkNamespace(t, func(int) bool { return false })
	if err := checkHostNetworkNamespace(context.Background(), realPeerClient(socket)); !errors.Is(err, ErrHostNetworkNamespace) {
		t.Fatalf("another namespace: %v", err)
	}
	// No daemon at all is a failure, never a pass.
	if err := CheckHostNetworkNamespace(context.Background(), socket+".missing"); err == nil {
		t.Fatal("a missing socket passed")
	}
}

func TestSharesNetworkNamespaceFailsClosedOnAnUnreadableNamespace(t *testing.T) {
	if !sharesNetworkNamespace(os.Getpid()) {
		t.Fatal("the runner's own namespace is not its own")
	}
	if sharesNetworkNamespace(1 << 30) {
		t.Fatal("a process that cannot be read shares the namespace")
	}
}

// The runtime's own client carries the requirement when the option is set, and only then. Fails closed: the
// option is what ties the pinned destinations to the daemon's namespace on every connection.
func TestRuntimeClientCarriesTheHostNetworkRequirement(t *testing.T) {
	var configuration runtimeOptions
	WithHostNetworkNamespace()(&configuration)
	if !configuration.requireHostNet {
		t.Fatal("the option does not set the requirement")
	}
	for _, want := range []bool{true, false} {
		client, closeClient := (&CandidateRuntime{socket: "/nonexistent.sock", requireHostNet: want}).client()
		peer := client.Transport.(allowListTransport).peer
		peer.mu.Lock()
		got := peer.requireHostNet
		peer.mu.Unlock()
		closeClient()
		if got != want {
			t.Fatalf("requireHostNet = %v, want %v", got, want)
		}
	}
}
