//go:build linux

package driver

import (
	"context"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	protocol "github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal-Apps/packages/extensionprotocol"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

// A machine token reaches only the machine header, a viewer or public token never reaches it, and
// the runner refuses a machine token outside its path space before anything is sent (doc 183).
func TestForwardBackendRequestSelectsMachineHeader(t *testing.T) {
	dir, err := os.MkdirTemp("", "pm-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "app.sock")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	seen := make(chan http.Header, 1)
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen <- r.Header.Clone()
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"ok":true}`))
	})}
	go server.Serve(listener)
	defer server.Close()
	uid, gid := uint32(os.Geteuid()), uint32(os.Getegid())
	call := func(method, target string, body []byte, token string) error {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_, err := forwardBackendRequest(ctx, path, uid, gid, method, target, body, token)
		return err
	}
	if err := call("GET", "/v1/machine/bars?limit=1", nil, "mch.a.b"); err != nil {
		t.Fatal(err)
	}
	h := <-seen
	if h.Get(appplatform.MachineAssertionHeader) != "mch.a.b" || h.Get(protocol.Header) != "" || h.Get(appplatform.PublicAssertionHeader) != "" || h.Get("Cookie") != "" || h.Get("Authorization") != "" {
		t.Fatalf("machine token headers: %v", h)
	}
	if err := call("GET", "/v1/status", nil, "a.b"); err != nil {
		t.Fatal(err)
	}
	if h = <-seen; h.Get(protocol.Header) != "a.b" || h.Get(appplatform.MachineAssertionHeader) != "" {
		t.Fatalf("viewer token headers: %v", h)
	}
	for name, run := range map[string]func() error{
		"machine write":      func() error { return call("POST", "/v1/machine/bars", []byte(`{}`), "mch.a.b") },
		"machine private":    func() error { return call("GET", "/v1/admin", nil, "mch.a.b") },
		"machine public":     func() error { return call("GET", "/v1/public/status", nil, "mch.a.b") },
		"machine body":       func() error { return call("GET", "/v1/machine/bars", []byte(`{}`), "mch.a.b") },
		"machine encoded":    func() error { return call("GET", "/v1/machine/%62ars", nil, "mch.a.b") },
		"viewer in machine":  func() error { return call("GET", "/v1/machine/bars", nil, "a.b") },
		"viewer upper case":  func() error { return call("GET", "/v1/MACHINE/bars", nil, "a.b") },
		"viewer encoded":     func() error { return call("GET", "/v1/%6dachine/bars", nil, "a.b") },
		"viewer in public":   func() error { return call("GET", "/v1/public/status", nil, "a.b") },
		"public in machine":  func() error { return call("GET", "/v1/machine/bars", nil, "pub.a.b") },
		"machine semicolon":  func() error { return call("GET", "/v1/machine/bars;x", nil, "mch.a.b") },
		"viewer semicolon":   func() error { return call("GET", "/v1/machine;x/bars", nil, "a.b") },
		"viewer dot segment": func() error { return call("GET", "/v1/app/../machine/bars", nil, "a.b") },
	} {
		if run() == nil {
			t.Fatal("forwarded:", name)
		}
		select {
		case <-seen:
			t.Fatal("reached the backend:", name)
		default:
		}
	}
}
