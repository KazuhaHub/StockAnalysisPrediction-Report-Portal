//go:build linux

package transport

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

// fakeTargetsRunner closes the first `drop` connections right after the magic,
// as a runner that is busy or refusing would, then answers properly.
func fakeTargetsRunner(t *testing.T, drop int32) (string, *atomic.Int32) {
	t.Helper()
	path := filepath.Join(privateDir(t), "runner.sock")
	l, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	good, _ := NewNetworkTargets(targetsNetworks())
	body, _ := EncodeNetworkTargets(good)
	var attempts atomic.Int32
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			n := attempts.Add(1)
			go func() {
				defer c.Close()
				request := make([]byte, 40)
				if n <= drop {
					_, _ = io.ReadFull(c, request[:8])
					return
				}
				if _, err := io.ReadFull(c, request); err != nil {
					return
				}
				_, _ = c.Write(reply(t, NetworkTargetsReplyMagic, request[8:], body))
			}()
		}
	}()
	return path, &attempts
}

func TestNetworkTargetsQueryRetriesOnceBeforeConcludingUnsupported(t *testing.T) {
	old := networkTargetsRetryDelay
	networkTargetsRetryDelay = time.Millisecond
	t.Cleanup(func() { networkTargetsRetryDelay = old })
	profile := privateProfile(uint32(os.Geteuid()))

	// A runner that drops only the first connection is not an old runner.
	path, attempts := fakeTargetsRunner(t, 1)
	got, err := queryNetworkTargets(context.Background(), path, profile)
	if err != nil || len(got.PrivateNetworks) != 2 || attempts.Load() != 2 {
		t.Fatalf("one dropped connection: %+v err=%v attempts=%d", got, err, attempts.Load())
	}

	// One that drops both is concluded unsupported, after exactly two attempts.
	path, attempts = fakeTargetsRunner(t, 2)
	if _, err := queryNetworkTargets(context.Background(), path, profile); !errors.Is(err, ErrNetworkTargetsUnsupported) || attempts.Load() != 2 {
		t.Fatalf("two dropped connections: err=%v attempts=%d", err, attempts.Load())
	}

	// A good answer is not retried.
	path, attempts = fakeTargetsRunner(t, 0)
	if _, err := queryNetworkTargets(context.Background(), path, profile); err != nil || attempts.Load() != 1 {
		t.Fatalf("good answer: err=%v attempts=%d", err, attempts.Load())
	}

	// A malformed reply is a protocol answer, not a drop: no retry.
	bad := filepath.Join(privateDir(t), "bad.sock")
	l, err := net.Listen("unix", bad)
	if err != nil {
		t.Fatal(err)
	}
	_ = os.Chmod(bad, 0600)
	t.Cleanup(func() { l.Close() })
	var badAttempts atomic.Int32
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			badAttempts.Add(1)
			request := make([]byte, 40)
			_, _ = io.ReadFull(c, request)
			_, _ = c.Write(reply(t, "PRNX0001", request[8:], []byte("{}")))
			c.Close()
		}
	}()
	if _, err := queryNetworkTargets(context.Background(), bad, profile); !errors.Is(err, ErrNetworkTargetsUnsupported) || badAttempts.Load() != 1 {
		t.Fatalf("malformed reply: err=%v attempts=%d", err, badAttempts.Load())
	}
}
