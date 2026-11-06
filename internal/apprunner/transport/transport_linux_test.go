//go:build linux

package transport

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/journal"
)

type recorder struct {
	calls  atomic.Int32
	refuse bool
}

func (r *recorder) AdmitControlCommand(context.Context, []byte, []byte) (journal.CommandAdmission, error) {
	r.calls.Add(1)
	if r.refuse {
		return journal.CommandAdmission{}, errors.New("private database error")
	}
	return journal.CommandAdmission{State: "prepared"}, nil
}
func fixture(t *testing.T, uid uint32, r *recorder) (string, context.CancelFunc) {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "runner-transport-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	path := filepath.Join(dir, "control.sock")
	server, err := Listen(path, uid, r)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := server.Serve(ctx); err != nil {
			t.Error(err)
		}
	}()
	stop := func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("server did not stop")
		}
	}
	t.Cleanup(stop)
	return path, stop
}
func TestExchangeAdmissionAndRefusal(t *testing.T) {
	for _, refuse := range []bool{false, true} {
		t.Run(map[bool]string{false: "admit", true: "refuse"}[refuse], func(t *testing.T) {
			r := &recorder{refuse: refuse}
			path, _ := fixture(t, uint32(os.Geteuid()), r)
			err := Exchange(context.Background(), path, uint32(os.Geteuid()), []byte("{}"), make([]byte, 64))
			if refuse && !errors.Is(err, ErrRefused) || !refuse && err != nil {
				t.Fatalf("exchange: %v", err)
			}
			if r.calls.Load() != 1 {
				t.Fatal("incorrect admission count")
			}
		})
	}
}
func TestWrongPeerBothDirections(t *testing.T) {
	r := &recorder{}
	wrong := uint32(os.Geteuid()) + 1
	path, _ := fixture(t, wrong, r)
	if err := Exchange(context.Background(), path, uint32(os.Geteuid()), []byte("{}"), make([]byte, 64)); err == nil {
		t.Fatal("wrong controller admitted")
	}
	if r.calls.Load() != 0 {
		t.Fatal("read request before authenticating")
	}
	path2, _ := fixture(t, uint32(os.Geteuid()), r)
	if err := Exchange(context.Background(), path2, wrong, []byte("{}"), make([]byte, 64)); err == nil {
		t.Fatal("wrong runner accepted")
	}
	if r.calls.Load() != 0 {
		t.Fatal("sent request to wrong runner")
	}
}
func TestMalformedFramesNeverAdmitted(t *testing.T) {
	for _, kind := range []string{"magic", "oversized", "truncated", "extra", "no-half-close"} {
		t.Run(kind, func(t *testing.T) {
			r := &recorder{}
			path, _ := fixture(t, uint32(os.Geteuid()), r)
			conn, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: path, Net: "unix"})
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			conn.SetDeadline(time.Now().Add(5 * time.Second))
			frame := requestFrame([]byte("{}"), make([]byte, 64))
			switch kind {
			case "magic":
				frame[0] ^= 1
			case "oversized":
				binary.BigEndian.PutUint32(frame[8:12], 65537)
			case "truncated":
				frame = frame[:len(frame)-1]
			case "extra":
				frame = append(frame, 0)
			}
			conn.Write(frame)
			if kind != "no-half-close" {
				conn.CloseWrite()
			}
			var buf [100]byte
			conn.Read(buf[:])
			if r.calls.Load() != 0 {
				t.Fatal("malformed request admitted")
			}
		})
	}
}
func TestSocketPathsAndCleanup(t *testing.T) {
	r := &recorder{}
	path, cancel := fixture(t, uint32(os.Geteuid()), r)
	if _, err := Listen(path, uint32(os.Geteuid()), r); err == nil {
		t.Fatal("overwrote live socket")
	}
	alias := filepath.Join(filepath.Dir(path), "alias.sock")
	if err := os.Symlink(path, alias); err != nil {
		t.Fatal(err)
	}
	if err := Exchange(context.Background(), alias, uint32(os.Geteuid()), []byte("{}"), make([]byte, 64)); err == nil {
		t.Fatal("followed socket symlink")
	}
	if err := os.Chmod(path, 0666); err != nil {
		t.Fatal(err)
	}
	if err := Exchange(context.Background(), path, uint32(os.Geteuid()), []byte("{}"), make([]byte, 64)); err == nil {
		t.Fatal("world-writable socket accepted")
	}
	os.Chmod(path, 0600)
	// Cleanup must not unlink a replacement installed at the old pathname.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("replacement"), 0600); err != nil {
		t.Fatal(err)
	}
	cancel()
	if raw, err := os.ReadFile(path); err != nil || string(raw) != "replacement" {
		t.Fatal("cleanup removed replacement")
	}
}

func TestCancellationClosesBlockedSessions(t *testing.T) {
	r := &recorder{}
	path, cancel := fixture(t, uint32(os.Geteuid()), r)
	conn, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(time.Second))
	conn.Write([]byte(requestMagic))
	cancel()
	var b [1]byte
	if _, err := conn.Read(b[:]); err == nil {
		t.Fatal("cancelled session remained open")
	}
	if r.calls.Load() != 0 {
		t.Fatal("cancelled incomplete command admitted")
	}
}

func TestUnsafeParentAndExistingFile(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "runner-parent-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "control.sock")
	if err := os.Chmod(dir, 0770); err != nil {
		t.Fatal(err)
	}
	if server, err := Listen(path, uint32(os.Geteuid()), &recorder{}); err == nil {
		server.Close()
		t.Fatal("shared writable parent accepted")
	}
	os.Chmod(dir, 0700)
	os.WriteFile(path, []byte("keep"), 0600)
	if server, err := Listen(path, uint32(os.Geteuid()), &recorder{}); err == nil {
		server.Close()
		t.Fatal("existing file replaced")
	}
	if raw, err := os.ReadFile(path); err != nil || string(raw) != "keep" {
		t.Fatal("existing file damaged")
	}
}

func TestForgedOrExtendedAcknowledgementRefused(t *testing.T) {
	for _, kind := range []string{"digest", "status", "extra", "truncated"} {
		t.Run(kind, func(t *testing.T) {
			dir, err := os.MkdirTemp("/tmp", "runner-reply-")
			if err != nil {
				t.Fatal(err)
			}
			defer os.RemoveAll(dir)
			path := filepath.Join(dir, "control.sock")
			listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			if err := os.Chmod(path, 0600); err != nil {
				t.Fatal(err)
			}
			done := make(chan struct{})
			go func() {
				defer close(done)
				conn, err := listener.AcceptUnix()
				if err != nil {
					return
				}
				defer conn.Close()
				conn.SetDeadline(time.Now().Add(time.Second))
				frame, _ := io.ReadAll(io.LimitReader(conn, 100000))
				digest := sha256.Sum256(frame)
				reply := append([]byte(replyMagic), digest[:]...)
				reply = append(reply, 1)
				switch kind {
				case "digest":
					reply[8] ^= 1
				case "status":
					reply[40] = 2
				case "extra":
					reply = append(reply, 0)
				case "truncated":
					reply = reply[:40]
				}
				conn.Write(reply)
			}()
			if err := Exchange(context.Background(), path, uint32(os.Geteuid()), []byte("{}"), make([]byte, 64)); err == nil {
				t.Fatal("unbound or malformed acknowledgement accepted")
			}
			<-done
		})
	}
}

type waitingJournal struct{ entered chan struct{} }

func (j *waitingJournal) AdmitControlCommand(ctx context.Context, _ []byte, _ []byte) (journal.CommandAdmission, error) {
	j.entered <- struct{}{}
	<-ctx.Done()
	return journal.CommandAdmission{}, ctx.Err()
}
func TestEightSessionLimit(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "runner-limit-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "control.sock")
	store := &waitingJournal{entered: make(chan struct{}, 8)}
	server, err := Listen(path, uint32(os.Geteuid()), store)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- server.Serve(ctx) }()
	clients := make(chan error, 8)
	for i := 0; i < 8; i++ {
		go func() { clients <- Exchange(ctx, path, uint32(os.Geteuid()), []byte("{}"), make([]byte, 64)) }()
	}
	for i := 0; i < 8; i++ {
		select {
		case <-store.entered:
		case <-time.After(time.Second):
			cancel()
			t.Fatal("expected eight in-flight admissions")
		}
	}
	overflow, stop := context.WithTimeout(context.Background(), time.Second)
	defer stop()
	if err := Exchange(overflow, path, uint32(os.Geteuid()), []byte("{}"), make([]byte, 64)); err == nil {
		t.Error("ninth session admitted")
	}
	if len(store.entered) != 0 {
		t.Error("ninth session reached journal")
	}
	cancel()
	for i := 0; i < 8; i++ {
		if err := <-clients; err == nil {
			t.Error("cancelled intent acknowledged")
		}
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
