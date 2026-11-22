package app

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"testing"
)

var frameAncestorsDirective = regexp.MustCompile(`(?:^|; )frame-ancestors ('self'|'none')(?:;|$)`)

// The bridge client derives its parent origin from its own document URL. That is
// only safe while no /app-documents/ or public document response, success or
// failure, lets an origin other than the Portal embed it.
func TestPlatformDocumentResponsesRestrictFrameAncestors(t *testing.T) {
	t.Run("directive shape", func(t *testing.T) {
		for _, csp := range []string{"frame-ancestors 'none'", "a; frame-ancestors 'self'; b", "a; frame-ancestors 'self'"} {
			if !frameAncestorsDirective.MatchString(csp) {
				t.Fatalf("matcher rejects %q", csp)
			}
		}
		for _, csp := range []string{"default-src 'none'", "frame-ancestors *", "frame-ancestors 'self' https://evil.example"} {
			if frameAncestorsDirective.MatchString(csp) {
				t.Fatalf("matcher accepts %q", csp)
			}
		}
	})
	s := &Server{}
	cases := []struct {
		name string
		call http.HandlerFunc
		path string
	}{
		{"document failure (malformed path)", platformDocumentHeaders(func(w http.ResponseWriter, r *http.Request) { s.platformAppDocument(w, r, "alice") }), "/app-documents/x"},
		{"document failure helper", platformDocumentHeaders(func(w http.ResponseWriter, _ *http.Request) { platformDocumentFailure(w) }), "/app-documents/x"},
		{"public failure", func(w http.ResponseWriter, _ *http.Request) { platformPublicFail(w, 404, "not_found", 0) }, "/public/apps/x"},
		{"public failure with retry", func(w http.ResponseWriter, _ *http.Request) { platformPublicFail(w, 429, "busy", 3) }, "/public/apps/x"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			c.call(w, httptest.NewRequest("GET", c.path, nil))
			if got := w.Header().Get("Content-Security-Policy"); !frameAncestorsDirective.MatchString(got) {
				t.Fatalf("CSP %q has no restrictive frame-ancestors", got)
			}
		})
	}
	for name, csp := range map[string]string{"error document": platformDocumentErrorCSP, "public document": platformPublicDocumentCSP, "public data": platformPublicDataCSP} {
		if m := frameAncestorsDirective.FindStringSubmatch(csp); m == nil || m[1] != "'none'" {
			t.Fatalf("%s CSP %q must forbid framing", name, csp)
		}
	}
}
