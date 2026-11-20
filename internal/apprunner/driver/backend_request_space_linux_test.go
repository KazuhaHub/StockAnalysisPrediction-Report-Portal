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
)

// No spelling of a reserved path puts a token into a space that is not its own: the runner judges the
// decoded path, and the backend is never reached.
func TestForwardBackendRequestRefusesEncodedAndDotSegmentSpaces(t *testing.T) {
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
	reached := make(chan string, 8)
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached <- r.URL.RequestURI()
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
	body := []byte(`{"a":1}`)
	for name, run := range map[string]func() error{
		"viewer, encoded ingress":    func() error { return call("POST", "/v1/%69ngress/heartbeat", body, "a.b") },
		"viewer, encoded slash":      func() error { return call("POST", "/v1/ingress%2fheartbeat", body, "a.b") },
		"viewer, dot segment":        func() error { return call("POST", "/v1/x/../ingress/heartbeat", body, "a.b") },
		"viewer, encoded public":     func() error { return call("GET", "/v1/%70ublic/status", nil, "a.b") },
		"viewer, double slash":       func() error { return call("GET", "/v1//public/status", nil, "a.b") },
		"ingress, encoded":           func() error { return call("POST", "/v1/%69ngress/heartbeat", body, "ing.a.b") },
		"ingress, dot segment":       func() error { return call("POST", "/v1/ingress/./heartbeat", body, "ing.a.b") },
		"ingress, escaped segment":   func() error { return call("POST", "/v1/ingress/heart%62eat", body, "ing.a.b") },
		"public, encoded":            func() error { return call("GET", "/v1/%70ublic/status", nil, "pub.a.b") },
		"public, dot segment":        func() error { return call("GET", "/v1/public/../public/status", nil, "pub.a.b") },
		"viewer, upper-case ingress": func() error { return call("POST", "/v1/INGRESS/heartbeat", body, "a.b") },
		"viewer, semicolon":          func() error { return call("POST", "/v1/ingress;x/heartbeat", body, "a.b") },
		"viewer, upper-case public":  func() error { return call("GET", "/v1/PUBLIC/status", nil, "a.b") },
		"ingress, upper-case":        func() error { return call("POST", "/v1/INGRESS/heartbeat", body, "ing.a.b") },
		"public, upper-case":         func() error { return call("GET", "/v1/Public/status", nil, "pub.a.b") },
		"public on the ingress path": func() error { return call("GET", "/v1/%69ngress/heartbeat", nil, "pub.a.b") },
	} {
		if run() == nil {
			t.Fatal("forwarded:", name)
		}
		select {
		case uri := <-reached:
			t.Fatal("reached the backend:", name, uri)
		default:
		}
	}
	if err := call("POST", "/v1/ingress/heartbeat", body, "ing.a.b"); err != nil {
		t.Fatal(err)
	}
	<-reached
}
