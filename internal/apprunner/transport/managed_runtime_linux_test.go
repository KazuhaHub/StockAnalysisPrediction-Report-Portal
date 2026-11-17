//go:build linux

package transport

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func TestReadOnlyListenerCannotMintManagedProfile(t *testing.T) {
	path, _ := fixture(t, uint32(os.Geteuid()), &recorder{})
	_, authority := managedRuntimeVector(t)
	proof, err := observeManagedRuntime(context.Background(), path, privateProfile(uint32(os.Geteuid())), authority)
	if err == nil || !proof.ReceivedAt().IsZero() {
		t.Fatal("read-only endpoint fabricated native profile")
	}
}
func TestManagedProfileFramingAndPrivateProvenance(t *testing.T) {
	for _, mode := range []string{"accepted", "nonce", "magic", "oversize", "truncated", "trailing", "stale", "future", "authority", "ready", "invalid"} {
		t.Run(mode, func(t *testing.T) {
			w, a := managedRuntimeVector(t)
			dir, err := os.MkdirTemp("/tmp", "managed-profile-")
			if err != nil {
				t.Fatal(err)
			}
			defer os.RemoveAll(dir)
			path := filepath.Join(dir, "runner.sock")
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
				conn.SetDeadline(time.Now().Add(3 * time.Second))
				request, err := io.ReadAll(io.LimitReader(conn, 41))
				if err != nil || len(request) != 40 {
					return
				}
				w.ObservedMS = strconv.FormatInt(time.Now().UnixMilli(), 10)
				switch mode {
				case "stale":
					w.ObservedMS = "0"
				case "future":
					w.ObservedMS = strconv.FormatInt(time.Now().UnixMilli()+10000, 10)
				case "authority":
					w.Controller.ControllerEpoch = "00000000-0000-4000-8000-000000000998"
				case "ready":
					w.Profile.ExecutionReady = true
				}
				body, _ := json.Marshal(w)
				if mode == "invalid" {
					body = []byte(`{}`)
				}
				reply := make([]byte, 44)
				copy(reply, managedRuntimeReplyMagic)
				copy(reply[8:40], request[8:])
				binary.BigEndian.PutUint32(reply[40:], uint32(len(body)))
				switch mode {
				case "nonce":
					reply[8] ^= 1
				case "magic":
					reply[0] ^= 1
				case "oversize":
					binary.BigEndian.PutUint32(reply[40:], 4097)
				case "truncated":
					body = body[:len(body)-1]
				case "trailing":
					body = append(body, 0)
				}
				io.Copy(conn, bytes.NewReader(append(reply, body...)))
			}()
			proof, err := observeManagedRuntime(context.Background(), path, privateProfile(uint32(os.Geteuid())), a)
			<-done
			if mode == "accepted" {
				if err != nil || proof.Profile() != w.Profile || proof.Endpoint().RootController || proof.ReceivedAt().IsZero() {
					t.Fatal("private framing profile", err)
				}
			} else if err == nil || !proof.ReceivedAt().IsZero() {
				t.Fatal("bad profile minted provenance", mode, err)
			}
		})
	}
}
