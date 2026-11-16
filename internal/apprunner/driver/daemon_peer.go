package driver

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"sync"
)

// daemonPeer identifies, for one client built by newDockerClient, the process on the other end of every
// connection to the daemon socket (see verify). The mount-table overlap check reads the runner's own
// /proc/self/mountinfo, which describes the daemon's paths only when the daemon is a local dockerd in the same
// mount namespace; a forwarder to another daemon, another mount namespace and a desktop VM are refused.
type daemonPeer struct {
	mu sync.Mutex
	// seen are the connections already identified: a connection's peer cannot change while it lives.
	seen map[net.Conn]struct{}
	// refused is the first refusal of a connection. Requests still run (an image load needs no mount table, and
	// a runner without the privilege to read /proc/<pid>/fd can still load one); the overlap check, which does,
	// asks for it through requireDaemonIdentified.
	refused error
	// netDiffers is set when a holder of a connection is in another network namespace than the runner.
	netDiffers bool
}

// verifyOnce identifies the daemon behind a connection the first time it answers and remembers a refusal.
func (p *daemonPeer) verifyOnce(ctx context.Context, conn net.Conn) error {
	p.mu.Lock()
	_, done := p.seen[conn]
	p.mu.Unlock()
	if done {
		return nil
	}
	if err := p.verify(ctx, conn); err != nil {
		p.mu.Lock()
		if p.refused == nil {
			p.refused = err
		}
		p.mu.Unlock()
		return nil
	}
	p.mu.Lock()
	if p.seen == nil || len(p.seen) >= 256 {
		p.seen = map[net.Conn]struct{}{}
	}
	p.seen[conn] = struct{}{}
	p.mu.Unlock()
	return nil
}

func (p *daemonPeer) refuse(format string, args ...any) error {
	return fmt.Errorf("%w: the container daemon is not a local dockerd in the runner's mount namespace, so mount sources cannot be compared (%s)", ErrUnavailable, fmt.Sprintf(format, args...))
}

// requireDaemonIdentified refuses when any connection of the client was not found to be a local dockerd in the
// runner's mount namespace. Every overlap check calls it after its request, because it compares the runner's own
// mount table with the daemon's paths. A client that was not built by newDockerClient has no peer to ask (only a
// test builds one) and passes.
func requireDaemonIdentified(client *http.Client) error {
	transport, ok := client.Transport.(allowListTransport)
	if !ok || transport.peer == nil {
		return nil
	}
	transport.peer.mu.Lock()
	defer transport.peer.mu.Unlock()
	return transport.peer.refused
}

// requireDaemonNetworkNamespace refuses a network volume when the daemon, which mounts it, is in another network
// namespace than the runner: the volume's address and the runner's interfaces then say nothing about each other.
func requireDaemonNetworkNamespace(client *http.Client) error {
	transport, ok := client.Transport.(allowListTransport)
	if !ok || transport.peer == nil {
		return nil
	}
	transport.peer.mu.Lock()
	defer transport.peer.mu.Unlock()
	if transport.peer.netDiffers {
		return fmt.Errorf("%w: a network volume is mounted by a daemon in another network namespace than the runner", ErrUnavailable)
	}
	return nil
}
