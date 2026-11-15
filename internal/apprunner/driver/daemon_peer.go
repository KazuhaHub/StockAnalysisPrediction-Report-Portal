package driver

import (
	"fmt"
	"net/http"
	"sync/atomic"
)

// daemonPeer records, for one client built by newDockerClient, whether the process on the other end of the
// daemon socket is in the runner's mount namespace. The mount-table overlap check reads the runner's own
// /proc/self/mountinfo, which describes the daemon's paths only when both share a mount namespace.
type daemonPeer struct{ state atomic.Int32 }

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
		return fmt.Errorf("%w: the container daemon is not in the runner's mount namespace, so mount sources cannot be compared", ErrUnavailable)
	}
	return nil
}
