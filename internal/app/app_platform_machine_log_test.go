package app

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A machine read carries a bearer token and comes from a workflow engine's address: even a failure of
// it leaves no access-log line (doc 183 section 6.2: never logged are the secret, the Authorization
// header and response bodies).
func TestAccessLogNeverRecordsMachineReads(t *testing.T) {
	s := &Server{}
	mux := http.NewServeMux()
	mux.HandleFunc("/machine/apps/x/data/y", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(500) })
	h := s.logMiddleware(mux)
	out := captureLog(t, func() {
		r := httptest.NewRequest("GET", "/machine/apps/x/data/y?symbol=1", nil)
		r.RemoteAddr = "203.0.113.7:5555"
		r.Header.Set("Authorization", "Bearer pmr_abcdefghijkl_"+strings.Repeat("S", 43))
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 500 {
			t.Fatal(w.Code)
		}
	})
	if out != "" {
		t.Fatalf("a machine read was logged: %q", out)
	}
}
