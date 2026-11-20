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

// The header follows the token kind, so a viewer token never reaches the public header and a public
// token never reaches the viewer header (doc 179, amendment C1).
func TestForwardBackendRequestSelectsHeaderFromTokenKind(t *testing.T) {
	// A unix socket path is limited to 107 bytes, so the directory is short rather than a t.TempDir
	// named after this test.
	dir, err := os.MkdirTemp("", "pb-")
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
	if err := call("GET", "/v1/public/status?limit=1", nil, "pub.a.b"); err != nil {
		t.Fatal(err)
	}
	h := <-seen
	if h.Get(appplatform.PublicAssertionHeader) != "pub.a.b" || h.Get(protocol.Header) != "" || h.Get("Cookie") != "" {
		t.Fatalf("public token headers: %v", h)
	}
	if err := call("GET", "/v1/status", nil, "a.b"); err != nil {
		t.Fatal(err)
	}
	h = <-seen
	if h.Get(protocol.Header) != "a.b" || h.Get(appplatform.PublicAssertionHeader) != "" {
		t.Fatalf("viewer token headers: %v", h)
	}
	for name, run := range map[string]func() error{
		"public write":   func() error { return call("POST", "/v1/public/status", []byte(`{}`), "pub.a.b") },
		"public private": func() error { return call("GET", "/v1/admin", nil, "pub.a.b") },
		"public body":    func() error { return call("GET", "/v1/public/status", []byte(`{}`), "pub.a.b") },
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
