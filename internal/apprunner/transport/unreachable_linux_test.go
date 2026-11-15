//go:build linux

package transport

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A runner that is down (no socket, or a stale socket nobody listens on) is reported as
// unreachable, distinct from a runner verdict, and the count moves so a caller whose error
// chain was flattened can still tell.
func TestConnectEndpointReportsADownRunnerAsUnreachable(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	profile := privateProfile(uint32(os.Getuid()))
	path := filepath.Join(dir, "runner.sock")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	before := UnreachableDials()
	if _, err := connectEndpoint(ctx, path, profile); !errors.Is(err, ErrRunnerUnreachable) || !errors.Is(err, ErrUnsafeEndpoint) {
		t.Fatal("absent socket:", err)
	}
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	listener.SetUnlinkOnClose(false)
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	listener.Close()
	if _, err := connectEndpoint(ctx, path, profile); !errors.Is(err, ErrRunnerUnreachable) || !errors.Is(err, ErrRefused) {
		t.Fatal("refusing socket:", err)
	}
	if UnreachableDials() != before+2 {
		t.Fatal("unreachable count", UnreachableDials()-before)
	}
	// A runner that took its runtime directory with it is unreachable too, not "unsafe".
	if _, err := connectEndpoint(ctx, filepath.Join(dir, "gone", "runner.sock"), profile); !errors.Is(err, ErrRunnerUnreachable) {
		t.Fatal("absent runtime directory:", err)
	}
	// An unsafe endpoint that exists is a definite refusal, never unreachable.
	if err := os.Chmod(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := connectEndpoint(ctx, path, profile); errors.Is(err, ErrRunnerUnreachable) || !errors.Is(err, ErrUnsafeEndpoint) {
		t.Fatal("unsafe parent mode:", err)
	}
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	// A live runner is reachable.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	live, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer live.Close()
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	conn, err := connectEndpoint(ctx, path, profile)
	if err != nil {
		t.Fatal("live socket:", err)
	}
	conn.Close()
}
