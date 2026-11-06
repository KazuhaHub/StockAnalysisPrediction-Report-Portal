//go:build linux

package transport

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/driver"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/journal"
)

// A restore-start read-receipt gets the same treatment as an import read: the client turns a valid absence reply into
// journal.ErrNoSuchEffect and nothing else: a closed
// connection, a bad echo digest, an absence for another effect, or an absence answering an execute
// stays an uncertain failure.
func TestWritableLifecycleRestoreStartAbsenceRoundTrip(t *testing.T) {
	for _, mode := range []string{"absent", "closed", "nonce", "other effect", "execute"} {
		t.Run(mode, func(t *testing.T) {
			a, command, raw, sig, descriptor := restoreStartTransportFixture(t)
			dir, err := os.MkdirTemp("/tmp", "wla-")
			if err != nil {
				t.Fatal(err)
			}
			defer os.RemoveAll(dir)
			path := filepath.Join(dir, "w.sock")
			listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path})
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
				request, err := io.ReadAll(io.LimitReader(conn, 150000))
				if err != nil || len(request) < 45 || string(request[:8]) != writableLifecycleQueryMagic {
					return
				}
				if mode == "closed" {
					return
				}
				absence := updateStartAbsenceFor(command)
				if mode == "other effect" {
					absence.EffectID = "11111111-1111-4111-8111-111111119a64"
				}
				body, _ := json.Marshal(absence)
				reply := make([]byte, 44)
				copy(reply, writableLifecycleReplyMagic)
				digest := sha256.Sum256(request)
				copy(reply[8:40], digest[:])
				binary.BigEndian.PutUint32(reply[40:], uint32(len(body)))
				if mode == "nonce" {
					reply[8] ^= 1
				}
				conn.Write(append(reply, body...))
			}()
			action := UpdateStartReceipt
			if mode == "execute" {
				action = UpdateStartExecute
			}
			_, err = writableLifecycleFrame(context.Background(), path, privateProfile(uint32(os.Geteuid())), action, raw, lifecycleSignature(t, action, a, raw, sig), a, descriptor, nil)
			<-done
			if err == nil {
				t.Fatal("an absence reply produced an observation")
			}
			if definite := errors.Is(err, journal.ErrNoSuchEffect); definite != (mode == "absent") {
				t.Fatalf("definite=%v for %s: %v", definite, mode, err)
			}
			if mode != "absent" && !errors.Is(err, driver.ErrContainerUncertain) {
				t.Fatalf("%s must stay uncertain: %v", mode, err)
			}
		})
	}
}
