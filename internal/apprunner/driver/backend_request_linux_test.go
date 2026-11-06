//go:build linux

package driver

import (
	"context"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	protocol "github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal-Apps/packages/extensionprotocol"
)

func TestBackendRequestHTTPBoundaries(t *testing.T) {
	for _, mode := range []string{"ok", "forbidden", "redirect", "cookie", "compressed", "type", "oversize", "html", "status", "peer", "permissions", "slow"} {
		t.Run(mode, func(t *testing.T) {
			dir, err := os.MkdirTemp("/tmp", "br-")
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
			server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, err := io.ReadAll(r.Body)
				if err != nil || string(body) != `{"value":1}` || r.Method != "POST" || r.URL.RequestURI() != "/v1/echo?q=x" || r.Header.Get(protocol.Header) != "a.b" || r.Header.Get("Cookie") != "" || r.Header.Get("Authorization") != "" {
					t.Error("request/authority changed")
				}
				w.Header().Set("Content-Type", "application/json")
				reply := `{"ok":true}`
				switch mode {
				case "forbidden":
					w.WriteHeader(403)
				case "redirect":
					w.Header().Set("Location", "https://example.invalid")
					w.WriteHeader(302)
					return
				case "cookie":
					w.Header().Set("Set-Cookie", "must-not-escape=1")
				case "compressed":
					w.Header().Set("Content-Encoding", "gzip")
				case "type":
					w.Header().Set("Content-Type", "text/plain")
				case "oversize":
					reply = strings.Repeat("x", 1048577)
				case "html":
					reply = "<html>"
				case "status":
					w.WriteHeader(500)
				case "slow":
					<-r.Context().Done()
					return
				}
				w.Write([]byte(reply))
			})}
			go server.Serve(listener)
			defer server.Close()
			uid, gid := uint32(os.Geteuid()), uint32(os.Getegid())
			if mode == "peer" {
				uid++
			}
			if mode == "permissions" {
				if err := os.Chmod(path, 0666); err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
			defer cancel()
			response, err := forwardBackendRequest(ctx, path, uid, gid, "POST", "/v1/echo?q=x", []byte(`{"value":1}`), "a.b")
			if mode == "ok" || mode == "cookie" || mode == "forbidden" {
				if err != nil || string(response.Body) != `{"ok":true}` {
					t.Fatal("valid response", err)
				}
			} else if err == nil {
				t.Fatal("unsafe response accepted", mode)
			}
		})
	}
}
