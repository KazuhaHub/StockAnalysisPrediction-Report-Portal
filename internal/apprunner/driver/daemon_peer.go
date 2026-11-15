package driver

import (
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
)

// daemonPeer records, for one client built by newDockerClient, whether the daemon behind the socket is a local
// dockerd in the runner's mount namespace. SO_PEERCRED names the process that listened, which under socket
// activation is systemd and behind a forwarder is the proxy, so the socket's holders are vetted instead. The mount-table overlap check reads the runner's own
// /proc/self/mountinfo, which describes the daemon's paths only when both share a mount namespace.
type daemonPeer struct {
	state atomic.Int32

	// The holders of the daemon socket are vetted once per client, on its first dial (see observe).
	mu      sync.Mutex
	scanned bool
	holders map[int]struct{}
	vetErr  error
	reason  string // why the first refused dial was refused
}

const (
	daemonPeerUnknown int32 = iota
	daemonPeerSharesMountNamespace
	daemonPeerOtherMountNamespace
)

// record keeps a "not shared" verdict: one connection to a daemon outside the namespace taints the client.
func (p *daemonPeer) record(shared bool) {
	if !shared {
		p.state.Store(daemonPeerOtherMountNamespace)
		return
	}
	p.state.CompareAndSwap(daemonPeerUnknown, daemonPeerSharesMountNamespace)
}

// requireDaemonSharesMountNamespace refuses an inventory whose daemon was not seen in the runner's mount
// namespace. A client that was not built by newDockerClient has no socket peer to ask (only a test builds one),
// and passes.
func requireDaemonSharesMountNamespace(client *http.Client) error {
	transport, ok := client.Transport.(allowListTransport)
	if !ok || transport.peer == nil {
		return nil
	}
	if transport.peer.state.Load() != daemonPeerSharesMountNamespace {
		transport.peer.mu.Lock()
		reason := transport.peer.reason
		transport.peer.mu.Unlock()
		return fmt.Errorf("%w: the container daemon is not a local dockerd in the runner's mount namespace, so mount sources cannot be compared (%s)", ErrUnavailable, reason)
	}
	return nil
}
