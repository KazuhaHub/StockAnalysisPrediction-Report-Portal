package app

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestPlatformMarketHTTPAuthorityAndInput(t *testing.T) {
	s, mux, actor, _, cookie := platformManagementFixture(t, false)
	for _, endpoint := range []string{"catalogue", "stage"} {
		path := "/api/admin/app-platform/market/" + endpoint
		body := `{"catalogue_url":"https://127.0.0.1/index.json"}`
		if endpoint == "stage" {
			body = fmt.Sprintf(`{"catalogue_url":"https://127.0.0.1/index.json","release_sha256":%q}`, strings.Repeat("a", 64))
		}
		if got := sessionTestRequest(mux, "POST", path, "", body); got.Code != 401 {
			t.Fatal("anonymous remote fetch allowed", got.Code)
		}
		if got := sessionTestRequest(mux, "POST", path, cookie, body); got.Code != 503 || strings.Contains(got.Body.String(), "127.0.0.1") {
			t.Fatal("private fetch allowed or exposed target", got.Code, got.Body.String())
		}
		for _, bad := range []string{`null`, `{}`, strings.Replace(body, `https://127.0.0.1/index.json`, `http://127.0.0.1/index.json`, 1), strings.Replace(body, `"catalogue_url":`, `"Catalogue_url":`, 1), strings.Replace(body, `"catalogue_url":`, `"endpoint":"/tmp/socket","catalogue_url":`, 1)} {
			if got := sessionTestRequest(mux, "POST", path, cookie, bad); got.Code != 400 {
				t.Fatal("bad remote fetch input accepted", got.Code, got.Body.String())
			}
		}
		mustExec(t, s.st, `UPDATE users SET role='user' WHERE username=?`, actor.username)
		if got := sessionTestRequest(mux, "POST", path, cookie, body); got.Code != 403 {
			t.Fatal("ordinary user remote fetch allowed", got.Code)
		}
		mustExec(t, s.st, `UPDATE users SET role='admin' WHERE username=?`, actor.username)
	}
	if scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_operations`) != 0 {
		t.Fatal("remote fetch route admitted installation")
	}
}

func TestPlatformMarketFetchCancellation(t *testing.T) {
	requested := make(chan struct{})
	finished := make(chan struct{})
	remote := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(requested)
		<-r.Context().Done()
		close(finished)
	}))
	defer remote.Close()
	client := newPlatformMarketClient()
	client.safe.allowInsecureForTest = true
	platformMarketTestTLS(t, client, remote)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := client.fetch(ctx, "https://127.0.0.1/index.json", 1024); done <- err }()
	select {
	case <-requested:
	case <-time.After(3 * time.Second):
		t.Fatal("TLS fetch did not reach owned fixture")
	}
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("canceled download succeeded")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("download ignored cancellation")
	}
	select {
	case <-finished:
	case <-time.After(2 * time.Second):
		t.Fatal("canceled fetch did not close upstream request")
	}
}
