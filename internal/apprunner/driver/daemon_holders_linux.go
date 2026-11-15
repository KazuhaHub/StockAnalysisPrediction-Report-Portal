//go:build linux

package driver

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// daemonExecutableAccepted decides whether a process executable is the container daemon. A variable so a test can
// stand in a test binary for it.
var daemonExecutableAccepted = dockerdExecutable

// dockerdExecutable reports whether an executable path is a dockerd binary.
func dockerdExecutable(exe string) bool {
	return filepath.Base(strings.TrimSuffix(exe, " (deleted)")) == "dockerd"
}

// parseProcNetUnixInodes returns the inode of every socket in /proc/net/unix bound to exactly path.
func parseProcNetUnixInodes(raw []byte, path string) []string {
	var inodes []string
	for _, line := range bytes.Split(raw, []byte("\n")) {
		fields := strings.Fields(string(line))
		if len(fields) >= 8 && fields[7] == path {
			inodes = append(inodes, fields[6])
		}
	}
	return inodes
}

// vetDaemonSocketHolders finds the processes that hold the daemon's listening socket: the inode bound to path in
// /proc/net/unix, then every process with a descriptor on it. The socket is trusted only when at least one holder
// runs a dockerd binary and every holder is in the runner's mount namespace. That covers socket activation, where
// systemd and dockerd both hold it, and refuses a forwarder to a remote daemon, which no local dockerd holds. It
// returns the vetted pids. Every dial's listener is then checked for the runner's mount namespace; the listener
// need not be a holder, because under socket activation it is systemd, which may have handed the socket to
// dockerd and closed its own copy. A pid reused between this scan and a dial would be taken for the process it
// was: a residual window this does not close (a pidfd would, and is not used).
func vetDaemonSocketHolders(path string) (map[int]struct{}, error) {
	table, err := os.ReadFile("/proc/net/unix")
	if err != nil {
		return nil, fmt.Errorf("%w: socket table: %v", ErrUnavailable, err)
	}
	wanted := map[string]bool{}
	for _, inode := range parseProcNetUnixInodes(table, path) {
		wanted["socket:["+inode+"]"] = true
	}
	if len(wanted) == 0 {
		return nil, fmt.Errorf("%w: no socket is bound to %s in this namespace", ErrUnavailable, path)
	}
	processes, err := os.ReadDir("/proc")
	if err != nil {
		return nil, fmt.Errorf("%w: process table: %v", ErrUnavailable, err)
	}
	holders := map[int]struct{}{}
	scanned, unreadable := 0, 0
	for _, entry := range processes {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue
		}
		scanned++
		fds, err := os.ReadDir("/proc/" + entry.Name() + "/fd")
		if err != nil {
			unreadable++
			continue // exited, or not readable: a process the runner cannot see cannot be vetted either way
		}
		for _, fd := range fds {
			if target, err := os.Readlink("/proc/" + entry.Name() + "/fd/" + fd.Name()); err == nil && wanted[target] {
				holders[pid] = struct{}{}
				break
			}
		}
	}
	daemon := false
	for pid := range holders {
		if !sharesMountNamespace(pid) {
			return nil, fmt.Errorf("%w: a holder of %s (pid %d) is not in the runner's mount namespace", ErrUnavailable, path, pid)
		}
		if exe, err := os.Readlink("/proc/" + strconv.Itoa(pid) + "/exe"); err == nil && daemonExecutableAccepted(exe) {
			daemon = true
		}
	}
	if !daemon {
		return nil, fmt.Errorf("%w: no dockerd holds %s (%d holders among %d processes, %d unreadable, euid %d, socket inodes %v), so the daemon may be remote", ErrUnavailable, path, len(holders), scanned, unreadable, os.Geteuid(), parseProcNetUnixInodes(table, path))
	}
	return holders, nil
}

// observe records one dial's verdict: the daemon socket was vetted (once per client) and the listener the dial
// reports (SO_PEERCRED, which is systemd under socket activation and not necessarily a holder any more) is in the
// runner's mount namespace. Every dial is checked, and one failure taints the client. The holders are vetted
// once, not per dial, so a socket replaced between dials is not seen again until the next client: a window
// this does not close.
func (p *daemonPeer) observe(path string, pid int) {
	p.mu.Lock()
	if !p.scanned {
		p.holders, p.vetErr = vetDaemonSocketHolders(path)
		p.scanned = true
	}
	err := p.vetErr
	p.mu.Unlock()
	reason := ""
	switch {
	case err != nil:
		reason = err.Error()
	case !sharesMountNamespace(pid):
		reason = fmt.Sprintf("the listener of the socket (pid %d) is not in the runner's mount namespace", pid)
	}
	if reason != "" {
		p.mu.Lock()
		if p.reason == "" {
			p.reason = reason
		}
		p.mu.Unlock()
	}
	p.record(reason == "")
}
