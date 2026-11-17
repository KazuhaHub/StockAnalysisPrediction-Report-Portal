//go:build linux

package transport

import (
	"bytes"
	"encoding/base64"
	"log"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

type retainingConn struct {
	net.Conn
	written [][]byte
}

func (c *retainingConn) Write(p []byte) (int, error) {
	c.written = append(c.written, p)
	return len(p), nil
}
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

type logCapture struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (l *logCapture) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(p)
}

func (l *logCapture) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.String()
}

// Credential calls through the real egress registry and broker, accepted and
// refused, leave no value in anything the runner side logs or replies.
func TestServiceEgressCredentialCallsLeaveNoValueInLogsOrReplies(t *testing.T) {
	const token = "s3cret-token-value"
	var logs logCapture
	previousOutput := log.Writer()
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(previousOutput) })
	f := newEgrFixture(t)
	p := f.open(egrOptions{resources: egrCredentialResources})
	var replies []string
	p.mu.Lock()
	p.mutate = egrPlaceCredential("upstream", "Authorization", "Bearer "+token)
	p.mu.Unlock()
	replies = append(replies, f.network(t, egrCredentialRequest).body)
	// A mismatched command, a missing credential and a malformed request.
	p.mu.Lock()
	p.mutate = egrPlaceCredential("other", "Authorization", "Bearer "+token)
	p.mu.Unlock()
	replies = append(replies, f.network(t, egrCredentialRequest).body)
	p.mu.Lock()
	p.mutate = nil
	p.mu.Unlock()
	replies = append(replies, f.network(t, egrCredentialRequest).body)
	replies = append(replies, f.network(t, `{"credential":"upstream"}`).body)
	f.registry.close()
	for _, text := range append(replies, logs.String()) {
		if strings.Contains(text, token) || strings.Contains(text, "Bearer") || strings.Contains(text, base64.StdEncoding.EncodeToString([]byte("Bearer "+token))) {
			t.Fatalf("credential material in %q", text)
		}
	}
	// The per-call path does not touch the runner journal: it is written only
	// when a session is bound (the registry's binder), never with a call.
	if f.binds.Load() > 1 {
		t.Fatal("a call caused a journal binding", f.binds.Load())
	}
}
