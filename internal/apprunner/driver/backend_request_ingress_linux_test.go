//go:build linux

package driver

import (
	"context"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	protocol "github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal-Apps/packages/extensionprotocol"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

// An ingress token reaches only its own header, with the body unchanged, and never carries a cookie
// or the viewer header (doc 180 section 3.3 step 7).
func TestForwardBackendRequestSendsIngressTokenToItsOwnHeader(t *testing.T) {
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
	type seenRequest struct {
		header http.Header
		method string
		target string
		body   string
	}
	seen := make(chan seenRequest, 1)
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		seen <- seenRequest{r.Header.Clone(), r.Method, r.URL.RequestURI(), string(body)}
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
	body := []byte(`{"timestamp":1,"nonce":"3f9c0000000000000000"}`)
	if err := call("POST", "/v1/ingress/heartbeat", body, "ing.a.b"); err != nil {
		t.Fatal(err)
	}
	got := <-seen
	if got.header.Get(appplatform.IngressAssertionHeader) != "ing.a.b" || got.header.Get(protocol.Header) != "" || got.header.Get(appplatform.PublicAssertionHeader) != "" || got.header.Get("Cookie") != "" {
		t.Fatalf("ingress token headers: %v", got.header)
	}
	if got.method != "POST" || got.target != "/v1/ingress/heartbeat" || got.body != string(body) {
		t.Fatalf("request %+v", got)
	}
	for name, run := range map[string]func() error{
		"ingress read":      func() error { return call("GET", "/v1/ingress/heartbeat", nil, "ing.a.b") },
		"ingress elsewhere": func() error { return call("POST", "/v1/documents", body, "ing.a.b") },
		"viewer on ingress": func() error { return call("POST", "/v1/ingress/heartbeat", body, "a.b") },
		"public on ingress": func() error { return call("POST", "/v1/ingress/heartbeat", body, "pub.a.b") },
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
