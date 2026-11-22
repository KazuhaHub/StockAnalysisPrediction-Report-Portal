package app

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

var frameAncestorsDirective = regexp.MustCompile(`(?:^|; )frame-ancestors ('self'|'none')(?:;|$)`)

// The bridge client derives its parent origin from its own document URL. That is
// only safe while no /app-documents/ or public document response, success or
// failure, lets an origin other than the Portal embed it.
func TestPlatformDocumentResponsesRestrictFrameAncestors(t *testing.T) {
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

// The success path end to end: an installed frontend served through the real mux.
func TestPlatformDocumentSuccessResponseRestrictsFrameAncestors(t *testing.T) {
	var archive bytes.Buffer
	zw := zip.NewWriter(&archive)
	f, err := zw.Create("index.html")
	if err != nil {
		t.Fatal(err)
	}
	f.Write([]byte(`<!doctype html><html><head><meta charset="utf-8"></head><body><div id="app"></div></body></html>`))
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	s, reader, id := platformFrontendBrowserInstallFixture(t, archive.Bytes())
	s.st.SetSetting("timezone", "UTC")
	mux := http.NewServeMux()
	s.wireRoutes(mux)
	cookie := &http.Cookie{Name: cookieName, Value: s.managedCookie(*s.st.GetUser(reader.username), reader.sessionID, reader.expires.Unix())}
	get := func(path string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", path, nil)
		r.AddCookie(cookie)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w
	}
	launch := get("/api/app-platform/installations/" + id + "/launch")
	var body struct {
		Source string `json:"source"`
	}
	if launch.Code != 200 || json.Unmarshal(launch.Body.Bytes(), &body) != nil || body.Source == "" {
		t.Fatalf("launch failed: %d %s", launch.Code, launch.Body.String())
	}
	ok := get(body.Source)
	if ok.Code != 200 {
		t.Fatalf("document status %d", ok.Code)
	}
	if got := ok.Header().Get("Content-Security-Policy"); !strings.Contains(got, "sandbox allow-scripts") || !frameAncestorsDirective.MatchString(got) {
		t.Fatalf("success CSP %q", got)
	}
	bad := get(strings.Replace(body.Source, "/index.html", "/missing.html", 1))
	if bad.Code == 200 || !frameAncestorsDirective.MatchString(bad.Header().Get("Content-Security-Policy")) {
		t.Fatalf("failure %d CSP %q", bad.Code, bad.Header().Get("Content-Security-Policy"))
	}
}
