//go:build linux

package transport

import (
	"bytes"
	"net"
	"testing"
	"time"
)

type retainingConn struct {
	net.Conn
	written [][]byte
}

func (c *retainingConn) Write(p []byte) (int, error) { c.written = append(c.written, p); return len(p), nil }
func (c *retainingConn) SetWriteDeadline(time.Time) error { return nil }

// The frame a mux encodes for a write holds a copy of the payload (a command
// carries the credential value); it is wiped once written.
func TestServiceEgressMuxWipesTheFrameItWrote(t *testing.T) {
	conn := &retainingConn{}
	m := NewEgressMux(conn, EgressPortal)
	var id EgressCallID
	id[0] = 1
	if err := m.write(EgressFrameCommand, id, bytes.Repeat([]byte("secret"), 20)); err != nil {
		t.Fatal(err)
	}
	if len(conn.written) != 1 || len(conn.written[0]) == 0 {
		t.Fatal("nothing was written")
	}
	if !bytes.Equal(conn.written[0], make([]byte, len(conn.written[0]))) {
		t.Fatal("the written frame was not wiped")
	}
}

// The runner wipes the signed command bytes it was handed once the call is over:
// they carry the credential value Portal placed.
func TestServiceEgressRunnerWipesTheCommandItHeld(t *testing.T) {
	f := newEgrFixture(t)
	p := f.open(egrOptions{resources: egrCredentialResources})
	p.mu.Lock()
	p.mutate = egrPlaceCredential("upstream", "Authorization", "Bearer s3cret-token-value")
	p.mu.Unlock()
	if r := f.network(t, egrCredentialRequest); r.status != 200 {
		t.Fatalf("call failed: %+v", r)
	}
	raw, _ := f.lastRaw.Load().([]byte)
	if len(raw) == 0 || !bytes.Equal(raw, make([]byte, len(raw))) {
		t.Fatal("the command bytes survived the call", len(raw))
	}
}
