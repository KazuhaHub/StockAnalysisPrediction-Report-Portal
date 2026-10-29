package app

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
)

func platformBackendHTTPFixture(t *testing.T, pg bool) (*Server, *http.ServeMux, platformActorSession, string, string, string) {
	t.Helper()
	s, actor, lease := platformWritableAccessFixture(t, pg)
	a, err := s.preparePlatformWritableBackendAccess(context.Background(), actor, lease.installationID)
	if err != nil {
		t.Fatal(err)
	}
	binding := platformBackendRequestBinding{Generation: "1", PolicyRevision: "1", ReleaseSHA256: platformDigest(a.intent.start.writer.descriptor)}
	a.Close()
	request, _ := json.Marshal(map[string]any{"binding": binding, "method": "POST", "path": "/v1/echo", "body": `{"value":"fixture"}`})
	cookie := s.managedCookie(*s.st.GetUser(actor.username), actor.sessionID, actor.expires.Unix())
	mux := http.NewServeMux()
	s.wireRoutes(mux)
	return s, mux, actor, "/api/app-platform/installations/" + lease.installationID + "/backend", cookie, string(request)
}

func platformBackendHTTPCall(mux *http.ServeMux, path, cookie, body string, headers map[string]string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("POST", path, strings.NewReader(body))
	if cookie != "" {
		r.AddCookie(&http.Cookie{Name: cookieName, Value: cookie})
	}
	r.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	return w
}

func testPlatformBackendHTTPAdmission(t *testing.T, pg bool) {
	s, mux, actor, path, cookie, body := platformBackendHTTPFixture(t, pg)
	call := func(target, credential, data string, headers map[string]string) *httptest.ResponseRecorder {
		return platformBackendHTTPCall(mux, target, credential, data, headers)
	}
	if w := call(path, "", body, nil); w.Code != 401 {
		t.Fatal("anonymous execution", w.Code)
	}
	if w := call(path, cookie, body, map[string]string{"Sec-Fetch-Site": "cross-site"}); w.Code != 403 {
		t.Fatal("cross-site execution", w.Code)
	}
	if w := call(path, cookie, body, map[string]string{"Origin": "null"}); w.Code != 403 {
		t.Fatal("opaque iframe execution", w.Code)
	}
	if w := call(path, cookie, body, map[string]string{"Content-Type": "text/plain"}); w.Code != 415 {
		t.Fatal("non-JSON execution", w.Code)
	}
	if w := call(path+"?token=ignored", cookie, body, nil); w.Code != 404 {
		t.Fatal("query execution", w.Code)
	}
	for _, bad := range []string{`null`, body + `{}`, strings.Replace(body, `"method":"POST"`, `"method":"POST","method":"GET"`, 1), strings.Replace(body, `"method":"POST"`, `"Method":"POST"`, 1), strings.Replace(body, `"path":"/v1/echo"`, `"path":"https://example.test/v1/echo"`, 1), strings.Replace(body, `"binding":{`, `"binding":{"viewer":"admin",`, 1), strings.Repeat(" ", platformBackendHTTPBodyLimit+1) + body} {
		if w := call(path, cookie, bad, nil); w.Code != 400 {
			t.Fatal("open request schema", w.Code, w.Body.String())
		}
	}
	if w := call(path, cookie, body, nil); w.Code != 503 || w.Header().Get("Retry-After") != "" || strings.Contains(w.Body.String(), "fixture") {
		t.Fatal("unconfigured runtime did not fail closed", w.Code, w.Body.String())
	}
	stale := strings.Replace(body, `"generation":"1"`, `"generation":"2"`, 1)
	if w := call(path, cookie, stale, nil); w.Code != 404 {
		t.Fatal("stale binding exposed runtime configuration", w.Code)
	}
	// Requests and document compilation have independent bounded admission.
	one, _ := s.platformBackendRequests.acquire()
	defer one()
	two, _ := s.platformBackendRequests.acquire()
	defer two()
	if w := call(path, cookie, body, nil); w.Code != 429 || w.Header().Get("Retry-After") != "1" {
		t.Fatal("unbounded backend requests", w.Code)
	}
	installation := strings.Split(path, "/")[4]
	mustExec(t, s.st, `DELETE FROM app_platform_access_grants WHERE installation_id=?`, installation)
	// Release the occupied slots before checking current authorization.
	one()
	two()
	if w := call(path, cookie, body, nil); w.Code != 404 {
		t.Fatal("revoked audience exposed runtime configuration", w.Code)
	}
	mustExec(t, s.st, `UPDATE user_sessions SET revoked=1 WHERE id=?`, actor.sessionID)
	if w := call(path, cookie, body, nil); w.Code != 401 {
		t.Fatal("revoked session executed", w.Code)
	}
}

func TestPlatformBackendHTTPAdmissionSQLite(t *testing.T) { testPlatformBackendHTTPAdmission(t, false) }
func TestPlatformBackendHTTPAdmissionPostgres(t *testing.T) {
	testPlatformBackendHTTPAdmission(t, true)
}

func TestPlatformBackendHTTPDecoder(t *testing.T) {
	binding := platformBackendRequestBinding{Generation: "1", PolicyRevision: "1", ReleaseSHA256: strings.Repeat("a", 64)}
	encode := func(method, path, body string) string {
		raw, _ := json.Marshal(map[string]any{"binding": binding, "method": method, "path": path, "body": body})
		return string(raw)
	}
	decode := func(raw string) (platformBackendHTTPRequest, error) {
		return readPlatformBackendHTTPRequest(httptest.NewRecorder(), httptest.NewRequest("POST", "/", strings.NewReader(raw)))
	}
	body := `"` + strings.Repeat("<", 65534) + `"`
	valid := encode("POST", "/v1/echo?value=%E4%B8%AD", body)
	request, err := decode(valid)
	if err != nil || request.binding != binding || request.body != body || request.method != "POST" || request.path != "/v1/echo?value=%E4%B8%AD" {
		t.Fatal("bounded escaped JSON bytes changed", err)
	}
	for _, raw := range []string{
		encode("POST", "/v1/echo", body+" "),
		encode("GET", "/v1/echo", `{}`),
		encode("PATCH", "/v1/echo", `{}`),
		encode("POST", "/v1/handshake", `{}`),
		encode("POST", "/v1/%2e%2e/admin", `{}`),
		encode("POST", "/v1/echo", "not JSON"),
		strings.Replace(valid, `"body":`, `"headers":{},"body":`, 1),
		strings.Replace(valid, `"policyRevision":"1"`, `"policyRevision":"1","policyRevision":"1"`, 1),
		strings.Replace(valid, `"generation":"1"`, `"generation":null`, 1),
		strings.Replace(valid, `"generation":"1"`, `"generation":1`, 1),
	} {
		if request, err := decode(raw); err == nil || request != (platformBackendHTTPRequest{}) {
			t.Fatal("invalid request returned partial dispatch input", err)
		}
	}
}

func TestPlatformBackendHTTPBootstrapRefusal(t *testing.T) {
	s, _, _, _, _, _ := platformBackendHTTPFixture(t, false)
	before := dumpOf(t, s.st)
	for _, endpoint := range []platformRuntimeEndpoint{{}, {path: "relative", controllerUID: 41001, controllerGID: 41001}, {path: "/tmp/a/../runner.sock", controllerUID: 41001, controllerGID: 41001}, {path: "/tmp/runner.sock", controllerUID: 0, controllerGID: 41001}} {
		if s.bindPlatformBackendHTTP(endpoint) == nil || s.platformBackendEndpoint.Load() != nil {
			t.Fatal("invalid bootstrap endpoint accepted")
		}
	}
	if !reflect.DeepEqual(stripHeader(before), stripHeader(dumpOf(t, s.st))) {
		t.Fatal("bootstrap refusal mutated control records")
	}
}

func TestPlatformBackendHTTPEnrolment(t *testing.T) {
	s, mux, _, path, cookie, body := platformBackendHTTPFixture(t, false)
	id := scalar[int64](t, s.st, `SELECT id FROM user_groups WHERE is_default=1`)
	if err := s.st.SetGroupRequire2FA(id, true); err != nil {
		t.Fatal(err)
	}
	if response := platformBackendHTTPCall(mux, path, cookie, body, nil); response.Code != 403 {
		t.Fatal("backend gateway bypassed enrolment", response.Code)
	}
}

func TestPlatformBackendHTTPReadDeadline(t *testing.T) {
	// Exercise the production writer wrappers against a real connection. A
	// stalled body must release the gateway slot even though global SSE writes
	// have no timeout. Recorder-only tests cannot establish this property.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		logged := &statusRecorder{ResponseWriter: w}
		gz := gzip.NewWriter(logged)
		defer gz.Close()
		wrapped := &gzipResponseWriter{ResponseWriter: logged, gz: gz}
		_, err := readPlatformBackendHTTPRequest(wrapped, r)
		if err == nil {
			wrapped.WriteHeader(200)
		} else {
			wrapped.WriteHeader(400)
		}
	}))
	defer server.Close()
	reader, writer := io.Pipe()
	defer reader.Close()
	defer writer.Close()
	request, err := http.NewRequest("POST", server.URL, reader)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Timeout: 10 * time.Second}
	done := make(chan error, 1)
	go func() {
		response, err := client.Do(request)
		if err == nil {
			defer response.Body.Close()
			if response.StatusCode != 400 {
				err = fmt.Errorf("stalled body status %d", response.StatusCode)
			}
		}
		done <- err
	}()
	if _, err := writer.Write([]byte(`{"binding":`)); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(8 * time.Second):
		t.Fatal("stalled body did not reach read deadline")
	}
}

type platformDeadlineRecorder struct {
	*httptest.ResponseRecorder
	writes []time.Time
}

func (w *platformDeadlineRecorder) SetWriteDeadline(deadline time.Time) error {
	w.writes = append(w.writes, deadline)
	return nil
}

func TestPlatformBackendHTTPWriteDeadline(t *testing.T) {
	s := &Server{}
	w := &platformDeadlineRecorder{ResponseRecorder: httptest.NewRecorder()}
	r := httptest.NewRequest("POST", "/", nil)
	before := time.Now()
	s.apiPlatformBackend(w, r, "") // Invalid route, before any database access.
	if w.Code != 404 || len(w.writes) != 2 || w.writes[0].Before(before.Add(30*time.Second)) || w.writes[0].After(time.Now().Add(30*time.Second)) || !w.writes[1].IsZero() {
		t.Fatal("bounded write deadline was missing or leaked to keep-alive request", w.writes)
	}
}
