//go:build linux && (amd64 || arm64)

package app

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

const platformSDKBrowserOrigin = "http://127.0.0.1:31935"

func platformSDKBrowserArchive(t *testing.T, original []byte) []byte {
	t.Helper()
	dir := os.Getenv("APP_SDK_BROWSER_DIR")
	if dir == "" {
		return original
	}
	if !filepath.IsAbs(dir) || filepath.Clean(dir) != dir {
		t.Fatal("invalid SDK browser bootstrap directory")
	}
	f, err := os.Open(filepath.Join(dir, "frontend.zip"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, 8<<20+1))
	if err != nil || len(raw) > 8<<20 {
		t.Fatal("SDK browser archive unavailable or oversized", err)
	}
	return raw
}

// Runs under the real non-root controller after the background scheduler has
// completed the signed SDK package. No candidate, boot or browser response is
// fabricated; only the browser/session bootstrap and revoke control are fixture.
func testNativeSDKBackendBrowser(t *testing.T, s *Server, actor platformActorSession, installation string, mux *http.ServeMux) {
	t.Helper()
	dir := os.Getenv("APP_SDK_BROWSER_DIR")
	if dir == "" {
		return
	}
	if os.Geteuid() != 41001 {
		t.Fatal("SDK browser did not run as controller")
	}
	node, runner, pkg, browsers := os.Getenv("APP_SDK_BROWSER_NODE"), os.Getenv("APP_SDK_BROWSER_RUNNER"), os.Getenv("SDK_BROWSER_PACKAGE"), os.Getenv("PLAYWRIGHT_BROWSERS_PATH")
	for _, path := range []string{node, runner, pkg, browsers} {
		if !filepath.IsAbs(path) || filepath.Clean(path) != path {
			t.Fatal("missing trusted SDK browser bootstrap")
		}
	}
	cookie := s.managedCookie(*s.st.GetUser(actor.username), actor.sessionID, actor.expires.Unix())
	fixture := http.NewServeMux()
	fixture.Handle("/", mux)
	launch := "/api/app-platform/installations/" + installation + "/launch"
	fixture.HandleFunc("GET /__sdk_fixture/info", func(w http.ResponseWriter, r *http.Request) {
		if _, err := s.platformHTTPReader(r, actor.username); err != nil {
			http.Error(w, "fixture session unavailable", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"launch": launch})
	})
	fixture.HandleFunc("POST /__sdk_fixture/revoke", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Origin") != platformSDKBrowserOrigin {
			http.Error(w, "fixture origin denied", http.StatusForbidden)
			return
		}
		if _, err := s.platformHTTPReader(r, actor.username); err != nil {
			http.Error(w, "fixture session unavailable", http.StatusUnauthorized)
			return
		}
		if _, err := s.st.exec(`DELETE FROM app_platform_access_grants WHERE installation_id=?`, installation); err != nil {
			http.Error(w, "fixture unavailable", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	for _, name := range []string{"host.js", "style.css"} {
		fixture.HandleFunc("GET /__sdk_fixture/"+name, func(w http.ResponseWriter, r *http.Request) {
			file := name
			if file == "style.css" {
				w.Header().Set("Content-Type", "text/css")
			} else {
				w.Header().Set("Content-Type", "text/javascript")
			}
			data, err := os.ReadFile(filepath.Join(dir, "host", file))
			if err != nil {
				http.Error(w, "fixture asset unavailable", http.StatusInternalServerError)
				return
			}
			w.Header().Set("Cache-Control", "no-store")
			w.Write(data)
		})
	}
	fixture.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			mux.ServeHTTP(w, r)
			return
		}
		http.SetCookie(w, &http.Cookie{Name: cookieName, Value: cookie, Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode})
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; frame-src 'self'; object-src 'none'; base-uri 'none'; form-action 'none'")
		io.WriteString(w, `<!doctype html><meta charset="utf-8"><title>SDK native backend fixture</title><link rel="stylesheet" href="/__sdk_fixture/style.css"><div id="host"></div><script src="/__sdk_fixture/host.js"></script>`)
	})
	listener, err := net.Listen("tcp", "127.0.0.1:31935")
	if err != nil {
		t.Fatal("exclusive SDK browser listener", err)
	}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/app-platform/installations/"+installation+"/backend" {
			recorded := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
			fixture.ServeHTTP(recorded, r)
			log.Printf("SDK fixture backend HTTP status=%d", recorded.status)
			return
		}
		fixture.ServeHTTP(w, r)
	})
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 10 * time.Second}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	defer func() {
		server.Close()
		if err := <-done; err != http.ErrServerClosed {
			t.Error(err)
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, node, runner)
	command.Cancel = func() error { return command.Process.Signal(syscall.SIGTERM) }
	command.WaitDelay = 5 * time.Second
	command.Env = []string{"PATH=" + filepath.Dir(node) + ":/usr/bin:/bin", "HOME=" + os.Getenv("TMPDIR"), "TMPDIR=" + os.Getenv("TMPDIR"), "SDK_BROWSER_PACKAGE=" + pkg, "PLAYWRIGHT_BROWSERS_PATH=" + browsers}
	var output bytes.Buffer
	command.Stdout = &output
	command.Stderr = &output
	if err := command.Run(); err != nil {
		t.Fatalf("SDK native browser failed: %v\n%s", err, output.String())
	}
	for _, marker := range []string{"SDK_NATIVE_BROWSER_NAVIGATION", "SDK_NATIVE_BROWSER_DRAFT_GUARD", "SDK_NATIVE_BROWSER_DOCUMENT_LAYOUT", "SDK_NATIVE_BROWSER_CURRENT_VIEWER", "SDK_NATIVE_BROWSER_UNCERTAIN_NO_RETRY", "SDK_NATIVE_BROWSER_REVOKED"} {
		if !strings.Contains(output.String(), marker) {
			t.Fatal("SDK browser marker missing", marker)
		}
		t.Log(marker)
	}
}
