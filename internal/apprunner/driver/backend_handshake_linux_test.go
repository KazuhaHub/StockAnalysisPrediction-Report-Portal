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

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

// These same-UID socket tests cover HTTP boundaries only, not native provenance
// or protocol MAC verification. Actual app consumption is a separate root test.
func TestBackendHandshakeHTTPBoundaries(t *testing.T) {
	for _, mode := range []string{"answer", "duplicate", "unknown", "trailing", "oversize", "redirect", "compressed", "content_type", "status", "permissions", "peer", "slow"} {
		t.Run(mode, func(t *testing.T) {
			dir, err := os.MkdirTemp("/tmp", "bh-")
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
				if r.Method != "POST" || r.URL.Path != "/v1/handshake" || r.URL.RawQuery != "" || r.Header.Get("Cookie") != "" || r.Header.Get("Authorization") != "" || r.Header.Get("Content-Type") != "application/json" {
					t.Error("unexpected handshake authority")
				}
				body, err := io.ReadAll(io.LimitReader(r.Body, 8193))
				if err != nil {
					t.Error(err)
				}
				if c, err := appplatform.ParseBackendHandshakeChallenge(body); err != nil || c != "a.b" {
					t.Error("changed challenge")
				}
				w.Header().Set("Content-Type", "application/json")
				answer := `{"answer":"c.d"}`
				switch mode {
				case "duplicate":
					answer = `{"answer":"c.d","answer":"e.f"}`
				case "unknown":
					answer = `{"answer":"c.d","other":true}`
				case "trailing":
					answer += " {}"
				case "oversize":
					answer = strings.Repeat(" ", 8193)
				case "redirect":
					w.Header().Set("Location", "https://example.invalid/")
					w.WriteHeader(302)
					return
				case "compressed":
					w.Header().Set("Content-Encoding", "gzip")
				case "content_type":
					w.Header().Set("Content-Type", "text/plain")
				case "status":
					w.WriteHeader(503)
				case "slow":
					<-r.Context().Done()
					return
				}
				w.Write([]byte(answer))
			})}
			go server.Serve(listener)
			defer server.Close()
			if mode == "permissions" {
				if err := os.Chmod(path, 0666); err != nil {
					t.Fatal(err)
				}
			}
			uid, gid := uint32(os.Geteuid()), uint32(os.Getegid())
			if mode == "peer" {
				uid++
			}
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()
			answer, err := exchangeBackendHandshake(ctx, path, uid, gid, "a.b")
			if mode == "answer" {
				if err != nil || answer != "c.d" {
					t.Fatal("answer", err)
				}
			} else if err == nil {
				t.Fatal("unsafe response accepted", mode)
			}
		})
	}
}
