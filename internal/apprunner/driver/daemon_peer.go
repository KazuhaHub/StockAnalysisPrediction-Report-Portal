package driver

import (
	"fmt"
	"net"
	"sync"
)

// daemonPeer identifies, for one client built by newDockerClient, the process on the other end of every
// connection to the daemon socket (see verify). The mount-table overlap check reads the runner's own
// /proc/self/mountinfo, which describes the daemon's paths only when the daemon is a local dockerd in the same
// mount namespace; a forwarder to another daemon, another mount namespace and a desktop VM are refused.
type daemonPeer struct {
	mu sync.Mutex
	// vetted are the pids this client has already found to be a dockerd in the runner's mount namespace.
	vetted map[int]struct{}
	// seen are the connections already identified: a connection's peer cannot change while it lives.
	seen map[net.Conn]struct{}
}

// verifyOnce identifies the daemon behind a connection the first time it answers.
func (p *daemonPeer) verifyOnce(conn net.Conn) error {
	p.mu.Lock()
	_, done := p.seen[conn]
	p.mu.Unlock()
	if done {
		return nil
	}
	if err := p.verify(conn); err != nil {
		return err
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
