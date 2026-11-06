package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestPostgresPlatformHTTPDocumentCancelsBlockedQuery(t *testing.T) {
	s, reader, id := platformLaunchFixture(t, true)
	platformGrantLaunchUser(t, s, reader, id)
	binding, err := s.inspectPlatformLaunchAccess(reader, id)
	if err != nil {
		t.Fatal(err)
	}
	cookie := s.managedCookie(*s.st.GetUser(reader.username), reader.sessionID, reader.expires.Unix())
	mux := http.NewServeMux()
	s.wireRoutes(mux)
	holder, err := s.st.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Rollback()
	if _, err := holder.Exec(`SELECT sha256 FROM app_platform_releases FOR UPDATE`); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	request := httptest.NewRequest("GET", platformDocumentSource(binding), nil).WithContext(ctx)
	request.AddCookie(&http.Cookie{Name: cookieName, Value: cookie})
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { w := httptest.NewRecorder(); mux.ServeHTTP(w, request); done <- w }()
	observed := false
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		var n int
		if err := s.st.queryRow(`SELECT COUNT(*) FROM pg_stat_activity WHERE datname=current_database() AND pid<>pg_backend_pid() AND wait_event_type='Lock' AND query LIKE '%FOR SHARE OF r,f%'`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n > 0 {
			observed = true
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !observed {
		cancel()
		holder.Rollback()
		t.Fatalf("request did not wait on the release lock: %d", (<-done).Code)
	}
	cancel()
	select {
	case result := <-done:
		if result.Code != 404 || result.Body.String() != "application unavailable\n" {
			t.Fatalf("canceled request exposed document: %d %s", result.Code, result.Body.String())
		}
	case <-time.After(2 * time.Second):
		holder.Rollback()
		<-done
		t.Fatal("request cancellation did not interrupt SQL lock wait")
	}
	// The holder remains locked until after the response: cancellation, rather
	// than releasing the resource, is what allowed the reader to return.
}
