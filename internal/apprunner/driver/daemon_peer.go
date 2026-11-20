package driver

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"time"
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
	// requireHostNet is set when the runner holds pinned proxy destinations (doc 186): they are addresses in
	// the runner's own network namespace, so a daemon in another one, or one that cannot be identified, makes
	// every request fail.
	requireHostNet bool
}

// ErrHostNetworkNamespace is returned when pinned proxy destinations are configured and the daemon is not
// identified as being in the runner's network namespace.
var ErrHostNetworkNamespace = fmt.Errorf("%w: the container daemon is not in the runner's network namespace (proxy_pins need the host network namespace)", ErrUnavailable)

// requireHostNetworkNamespace makes client refuse, from the first answer on, any connection whose daemon is in
// another network namespace than the runner or cannot be identified. The refusal is remembered, so every later
// request fails too until the runner restarts.
func requireHostNetworkNamespace(client *http.Client) {
	if transport, ok := client.Transport.(allowListTransport); ok && transport.peer != nil {
		transport.peer.mu.Lock()
		transport.peer.requireHostNet = true
		transport.peer.mu.Unlock()
	}
}

// hostNetworkViolation is the refusal a client that requires the host network namespace applies.
func (p *daemonPeer) hostNetworkViolation() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.requireHostNet && (p.netDiffers || p.refused != nil) {
		return ErrHostNetworkNamespace
	}
	return nil
}

// CheckHostNetworkNamespace is the start-time check of the runner's proxy_pins: it asks the daemon on socket
// one read-only question and refuses unless the daemon is identified as a local dockerd in the runner's
// network namespace. A namespace that cannot be read is not shared, and no answer is a failure.
func CheckHostNetworkNamespace(ctx context.Context, socket string) error {
	client, closeClient := newDockerClient(socket, time.Second, probeTimeout)
	defer closeClient()
	return checkHostNetworkNamespace(ctx, client)
}

func checkHostNetworkNamespace(ctx context.Context, client *http.Client) error {
	requireHostNetworkNamespace(client)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+dockerRequestHost+"/version", nil)
	if err != nil {
		return ErrHostNetworkNamespace
	}
	response, err := client.Do(request)
	if err != nil {
		if errors.Is(err, ErrHostNetworkNamespace) {
			return ErrHostNetworkNamespace
		}
		return fmt.Errorf("%w: %w", ErrHostNetworkNamespace, err)
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1<<20))
	response.Body.Close()
	return nil
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
