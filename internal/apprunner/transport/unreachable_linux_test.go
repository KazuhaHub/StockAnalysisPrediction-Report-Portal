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

// A dial that fails only because the caller's context is already finished says nothing about
// the runner: it is not tagged unreachable, and it does not report to the witness.
func TestConnectEndpointDoesNotBlameTheRunnerForAnExhaustedContext(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	profile := privateProfile(uint32(os.Getuid()))
	path := filepath.Join(dir, "runner.sock")
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	listener.SetUnlinkOnClose(false)
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	listener.Close() // a refusing socket, as a runner mid-restart leaves
	ctx, witness := WithUnreachableWitness(context.Background())
	expired, cancel := context.WithDeadline(ctx, time.Now().Add(-time.Second))
	defer cancel()
	_, err = connectEndpoint(expired, path, profile)
	if err == nil || errors.Is(err, ErrRunnerUnreachable) || !errors.Is(err, context.DeadlineExceeded) || witness.Seen() {
		t.Fatal("an exhausted context was blamed on the runner:", err, witness.Seen())
	}
	// With a live context the same socket is unreachable, and the witness says so.
	if _, err := connectEndpoint(ctx, path, profile); !errors.Is(err, ErrRunnerUnreachable) || !witness.Seen() {
		t.Fatal("refusing socket not reported:", err)
	}
}

// The witness is scoped to its context: a failed dial under another context, as a concurrent
// request in another goroutine makes, never changes it.
func TestUnreachableWitnessIsScopedToItsContext(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	profile := privateProfile(uint32(os.Getuid()))
	missing := filepath.Join(dir, "absent.sock")
	ctxA, a := WithUnreachableWitness(context.Background())
	ctxB, b := WithUnreachableWitness(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		connectEndpoint(ctxB, missing, profile)
	}()
	<-done
	if !b.Seen() || a.Seen() {
		t.Fatal("witness leaked across contexts", a.Seen(), b.Seen())
	}
	_ = ctxA
	if _, err := connectEndpoint(context.Background(), missing, profile); !errors.Is(err, ErrRunnerUnreachable) {
		t.Fatal("a context without a witness must still classify:", err)
	}
}
