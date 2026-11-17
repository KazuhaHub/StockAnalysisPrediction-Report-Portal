//go:build linux

package transport

import (
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/journal"
)

func TestReadOnlyEnrollmentObservation(t *testing.T) {
	root, err := os.MkdirTemp("/tmp", "runner-observe-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	data := filepath.Join(root, "journal")
	os.Mkdir(data, 0700)
	store, err := journal.Open(data)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	path := filepath.Join(root, "control.sock")
	server, err := Listen(path, uint32(os.Geteuid()), store)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- server.Serve(ctx) }()
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()
	if obs, err := ObserveEnrollment(ctx, path, uint32(os.Geteuid())); err == nil || !obs.ReceivedAt().IsZero() {
		t.Fatal("empty journal manufactured an identity")
	}
	expected := observationFixture()
	before, created, err := store.EnrollController(ctx, expected)
	if err != nil || !created {
		t.Fatal("observation changed empty enrollment state")
	}
	obs, err := ObserveEnrollment(ctx, path, uint32(os.Geteuid()))
	if err != nil {
		t.Fatal(err)
	}
	c := obs.Controller()
	if !bytes.Equal(c.PublicKey, expected.PublicKey) || c.Fingerprint != expected.Fingerprint || c.ControlRevision != expected.ControlRevision || time.Since(obs.ReceivedAt()) > MaxEnrollmentAge {
		t.Fatal("wrong or stale public observation")
	}
	c.PublicKey[0] ^= 1
	if !bytes.Equal(obs.Controller().PublicKey, expected.PublicKey) {
		t.Fatal("caller mutation changed proof")
	}
	after, err := store.ControllerDeployment(ctx)
	if err != nil || before.Revision != after.Revision || before.UpdatedMS != after.UpdatedMS {
		t.Fatal("probe changed durable enrollment")
	}
}
func TestEnrollmentProbeRefusesReplayedOrMalformedReplies(t *testing.T) {
	for _, kind := range []string{"nonce", "magic", "oversized", "extra", "truncated", "noncanonical"} {
		t.Run(kind, func(t *testing.T) {
			dir, err := os.MkdirTemp("/tmp", "runner-probe-reply-")
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
			os.Chmod(path, 0600)
			done := make(chan struct{})
			go func() {
				defer close(done)
				conn, err := listener.AcceptUnix()
				if err != nil {
					return
				}
				defer conn.Close()
				conn.SetDeadline(time.Now().Add(time.Second))
				request, _ := io.ReadAll(io.LimitReader(conn, 128))
				if len(request) != 40 {
					return
				}
				raw, _ := encodeEnrollment(observationFixture())
				if kind == "noncanonical" {
					raw = append([]byte(" "), raw...)
				}
				reply := make([]byte, 44)
				copy(reply, enrollmentReplyMagic)
				copy(reply[8:40], request[8:40])
				binary.BigEndian.PutUint32(reply[40:], uint32(len(raw)))
				reply = append(reply, raw...)
				switch kind {
				case "nonce":
					reply[8] ^= 1
				case "magic":
					reply[0] ^= 1
				case "oversized":
					binary.BigEndian.PutUint32(reply[40:], maxEnrollmentBytes+1)
				case "extra":
					reply = append(reply, 0)
				case "truncated":
					reply = reply[:len(reply)-1]
				}
				conn.Write(reply)
			}()
			if obs, err := ObserveEnrollment(context.Background(), path, uint32(os.Geteuid())); err == nil || !obs.ReceivedAt().IsZero() {
				t.Fatal("invalid/replayed observation escaped transport")
			}
			<-done
		})
	}
}
