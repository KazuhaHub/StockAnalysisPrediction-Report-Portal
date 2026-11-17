package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"
)

// The public document runs in a real browser with a live Portal session cookie in its jar. It must
// not be able to read that cookie, and a credentialed request it makes to the Portal's API must not
// carry it (doc 179 test 5). Set PUBLIC_PROJECTION_CHROME to a Chrome binary to run it.
func TestPublicDocumentCannotReachThePortalSessionInABrowser(t *testing.T) {
	chrome := os.Getenv("PUBLIC_PROJECTION_CHROME")
	if chrome == "" {
		t.Skip("PUBLIC_PROJECTION_CHROME not set")
	}
	platformPublicFixtureFiles = map[string]string{
		"public/index.html": `<!doctype html><title>start</title><script src="app.js"></script>`,
		"public/app.js": `const out = {};
try { out.cookie = document.cookie; } catch (e) { out.cookieError = e.name; }
fetch('/api/me', {credentials: 'include'}).then(r => { out.me = r.status }).catch(e => { out.meError = e.name }).finally(() => {
  fetch('data/status').then(r => r.text()).then(x => { out.data = x }).catch(e => { out.dataError = e.name }).finally(() => { new Image().src = '/__report?r=' + encodeURIComponent(JSON.stringify(out)) })
})`,
	}
	t.Cleanup(func() { platformPublicFixtureFiles = nil })
	e := platformPublicFixture(t, false)
	e.enable(t)
	reports := make(chan string, 4)
	var mu sync.Mutex
	var apiCookies []string
	front := http.NewServeMux()
	front.HandleFunc("/__login", func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: cookieName, Value: e.cookie, Path: "/", SameSite: http.SameSiteLaxMode})
		http.Redirect(w, r, "/public/apps/"+e.id+"/", http.StatusFound)
	})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/__login" {
			front.ServeHTTP(w, r)
			return
		}
		if r.URL.Path == "/__report" {
			select {
			case reports <- r.URL.Query().Get("r"):
			default:
			}
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/") {
			mu.Lock()
			apiCookies = append(apiCookies, r.Header.Get("Cookie"))
			mu.Unlock()
		}
		e.mux.ServeHTTP(w, r)
	}))
	defer server.Close()
	profile, err := os.MkdirTemp("", "pp-chrome-")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	// The browser stays open until the page has reported (or the budget runs out).
	cmd := exec.CommandContext(ctx, chrome, "--headless=new", "--no-sandbox", "--disable-gpu", "--user-data-dir="+profile, "--remote-debugging-port=0", server.URL+"/__login")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { cancel(); cmd.Wait(); os.RemoveAll(profile) }()
	var raw string
	select {
	case raw = <-reports:
	case <-time.After(40 * time.Second):
		t.Fatal("the page did not report")
	}
	var result map[string]string
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		t.Fatal(err, raw)
	}
	if result["cookieError"] != "SecurityError" || result["cookie"] != "" {
		t.Fatal("the document could read cookies", result)
	}
	if result["me"] != "" || result["meError"] == "" {
		t.Fatal("a credentialed request to the Portal API was answered to the document", result)
	}
	if result["data"] == "" || result["dataError"] != "" {
		t.Fatal("the document could not read its own data", result)
	}
	mu.Lock()
	defer mu.Unlock()
	for _, c := range apiCookies {
		if c != "" {
			t.Fatal("the session cookie was sent with a request of the public document", c)
		}
	}
}
