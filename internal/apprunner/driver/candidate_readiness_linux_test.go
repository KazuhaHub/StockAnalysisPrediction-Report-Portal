//go:build linux

package driver

import (
	"context"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Same-user socket tests prove bounded HTTP/framing only. The owned Linux
// container fixture separately establishes the actual app/native boundary.
func TestCandidateReadinessHTTP(t *testing.T) {
	for _, kind := range []string{"ready", "not_ready", "duplicate", "protocol", "oversize", "redirect", "permissions", "wrong_peer", "slow"} {
		t.Run(kind, func(t *testing.T) {
			dir, err := os.MkdirTemp("/tmp", "pr-")
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
				if r.Method != "GET" || r.URL.Path != "/health/ready" || r.Header.Get("Cookie") != "" || r.Header.Get("Authorization") != "" {
					t.Error("unexpected app probe authority")
				}
				w.Header().Set("Content-Type", "application/json")
				body := `{"protocol":"portal.backend/1","ready":true,"data_format":"1"}`
				switch kind {
				case "not_ready":
					body = strings.Replace(body, `true`, `false`, 1)
				case "duplicate":
					body = `{"protocol":"portal.backend/1","ready":true,"ready":false,"data_format":"1"}`
				case "protocol":
					body = strings.Replace(body, "portal.backend/1", "other", 1)
				case "oversize":
					body = strings.Repeat(" ", 4097)
				case "redirect":
					w.Header().Set("Location", "https://example.invalid/")
					w.WriteHeader(302)
					return
				case "slow":
					<-r.Context().Done()
					return
				}
				w.Write([]byte(body))
			})}
			go server.Serve(listener)
			defer server.Close()
			if kind == "permissions" {
				if err := os.Chmod(path, 0666); err != nil {
					t.Fatal(err)
				}
			}
			uid, gid := uint32(os.Geteuid()), uint32(os.Getegid())
			if kind == "wrong_peer" {
				uid++
			}
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()
			response, err := probeCandidateReadiness(ctx, path, uid, gid)
			if kind == "ready" {
				if err != nil || !response.Ready {
					t.Fatal("ready socket", err)
				}
			} else if kind == "not_ready" {
				if err != nil || response.Ready {
					t.Fatal("false readiness changed", err)
				}
			} else if err == nil {
				t.Fatal("invalid readiness accepted", kind)
			}
		})
	}
}
