package app

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

func platformNetworkHTTPPayload(t *testing.T, binding platformBackendRequestBinding, body []byte) string {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"binding": binding, "method": "POST", "url": "https://api.example.com/v1/check?q=%E4%B8%AD", "accept": "application/json", "contentType": "application/octet-stream", "bodyBase64": base64.StdEncoding.EncodeToString(body)})
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}
func TestPlatformNetworkHTTPDecoder(t *testing.T) {
	binding := platformBackendRequestBinding{Generation: "1", PolicyRevision: "1", ReleaseSHA256: strings.Repeat("a", 64)}
	body := bytes.Repeat([]byte{0, 255, 128, 10}, appplatform.MaxHTTPRequestBytes/4)
	valid := platformNetworkHTTPPayload(t, binding, body)
	decode := func(raw string) (platformNetworkHTTPRequest, error) {
		return readPlatformNetworkHTTPRequest(httptest.NewRecorder(), httptest.NewRequest("POST", "/", strings.NewReader(raw)))
	}
	parsed, err := decode(valid)
	if err != nil || parsed.binding != binding || !bytes.Equal(parsed.body, body) || parsed.target != "https://api.example.com/v1/check?q=%E4%B8%AD" || parsed.method != "POST" || parsed.kind != "application/octet-stream" {
		t.Fatal("bounded binary request changed", err)
	}
	clear(parsed.body)
	credentialed := platformNetworkHTTPPayload(t, binding, nil)
	var credentialFields map[string]any
	if json.Unmarshal([]byte(credentialed), &credentialFields) != nil {
		t.Fatal("credential fixture")
	}
	credentialFields["credential"] = "upstream-api"
	credentialedRaw, _ := json.Marshal(credentialFields)
	credentialed = string(credentialedRaw)
	parsed, err = decode(credentialed)
	if err != nil || parsed.credential != "upstream-api" {
		t.Fatal("credential binding name", err)
	}
	for _, bad := range []string{
		`null`, valid + `{}`,
		strings.Replace(valid, `"method":"POST"`, `"method":"POST","method":"GET"`, 1),
		strings.Replace(valid, `"method":"POST"`, `"Method":"POST"`, 1),
		strings.Replace(valid, `"method":"POST"`, `"method":"CONNECT"`, 1),
		strings.Replace(valid, `"accept":"application/json"`, `"accept":"bad\r\nvalue"`, 1),
		strings.Replace(valid, `"binding":{`, `"binding":{"viewer":"admin",`, 1),
		strings.Replace(valid, `"generation":"1"`, `"generation":1`, 1),
		strings.Replace(valid, `"url":`, `"headers":{"Authorization":"untrusted"},"url":`, 1),
		strings.Replace(credentialed, `"credential":"upstream-api"`, `"credential":{}`, 1),
		strings.Replace(credentialed, `"credential":"upstream-api"`, `"credential":"Bad"`, 1),
		strings.Replace(credentialed, `"credential":"upstream-api"`, `"credential":"upstream-api","credentialValue":"secret"`, 1),
		platformNetworkHTTPPayload(t, binding, append(bytes.Clone(body), 1)),
		strings.Repeat(" ", platformNetworkHTTPBodyLimit) + valid,
	} {
		r, err := decode(bad)
		if err == nil || len(r.body) != 0 || r.binding != (platformBackendRequestBinding{}) {
			t.Fatal("open schema or partial decoded input")
		}
	}
	empty := platformNetworkHTTPPayload(t, binding, nil)
	for _, bad := range []string{"YQ", "YQ==\n", "YR==", "!!!!"} {
		replacement, _ := json.Marshal(bad)
		raw := strings.Replace(empty, `"bodyBase64":""`, `"bodyBase64":`+string(replacement), 1)
		if r, err := decode(raw); err == nil || len(r.body) != 0 {
			t.Fatal("noncanonical base64 accepted", bad)
		}
	}
}
func testPlatformNetworkHTTPAdmission(t *testing.T, pg bool) {
	s, mux, actor, backendPath, cookie, _ := platformBackendHTTPFixture(t, pg)
	installation := strings.Split(backendPath, "/")[4]
	path := strings.TrimSuffix(backendPath, "/backend") + "/network"
	binding := platformBackendRequestBinding{Generation: "1", PolicyRevision: "1", ReleaseSHA256: scalar[string](t, s.st, `SELECT release_sha256 FROM app_platform_activations WHERE installation_id=?`, installation)}
	payload := platformNetworkHTTPPayload(t, binding, nil)
	call := func(credential, data string, headers map[string]string) *httptest.ResponseRecorder {
		return platformBackendHTTPCall(mux, path, credential, data, headers)
	}
	if w := call("", payload, nil); w.Code != 401 {
		t.Fatal("anonymous network execution", w.Code)
	}
	for _, h := range []map[string]string{{"Origin": "null"}, {"Origin": "https://other.example"}, {"Sec-Fetch-Site": "cross-site"}} {
		if w := call(cookie, payload, h); w.Code != 403 {
			t.Fatal("cross-origin network execution", w.Code)
		}
	}
	if w := call(cookie, payload, map[string]string{"Content-Type": "text/plain"}); w.Code != 415 {
		t.Fatal("non-JSON network execution", w.Code)
	}
	if w := call(cookie, payload+`{}`, nil); w.Code != 400 {
		t.Fatal("invalid network schema", w.Code)
	}
	if w := platformBackendHTTPCall(mux, path+"?token=ignored", cookie, payload, nil); w.Code != 404 {
		t.Fatal("query-selected network call", w.Code)
	}
	// This signed historical fixture deliberately has no selected network grant.
	// Denial must hide whether a trusted runtime was configured.
	if w := call(cookie, payload, nil); w.Code != 404 || strings.Contains(w.Body.String(), "runtime") || strings.Contains(w.Body.String(), "bodyBase64") || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("denied grant exposed response/runtime", w.Code, w.Body.String())
	}
	one, _ := s.platformNetworkRequests.acquire()
	defer one()
	two, _ := s.platformNetworkRequests.acquire()
	defer two()
	if w := call(cookie, payload, nil); w.Code != 429 || w.Header().Get("Retry-After") != "1" {
		t.Fatal("unbounded network admission", w.Code)
	}
	one()
	two()
	// Busy own-backend traffic cannot occupy this gateway's bounded slots.
	backendOne, _ := s.platformBackendRequests.acquire()
	defer backendOne()
	backendTwo, _ := s.platformBackendRequests.acquire()
	defer backendTwo()
	if w := call(cookie, payload, nil); w.Code != 404 {
		t.Fatal("backend slots consumed network budget", w.Code)
	}
	mustExec(t, s.st, `UPDATE user_sessions SET revoked=1 WHERE id=?`, actor.sessionID)
	if w := call(cookie, payload, nil); w.Code != 401 {
		t.Fatal("revoked session reached network", w.Code)
	}
}
func TestPlatformNetworkHTTPAdmissionSQLite(t *testing.T) { testPlatformNetworkHTTPAdmission(t, false) }
func TestPlatformNetworkHTTPAdmissionPostgres(t *testing.T) {
	testPlatformNetworkHTTPAdmission(t, true)
}
func TestPlatformNetworkHTTPEnrolment(t *testing.T) {
	s, mux, _, backendPath, cookie, _ := platformBackendHTTPFixture(t, false)
	id := scalar[int64](t, s.st, `SELECT id FROM user_groups WHERE is_default=1`)
	if err := s.st.SetGroupRequire2FA(id, true); err != nil {
		t.Fatal(err)
	}
	if w := platformBackendHTTPCall(mux, strings.TrimSuffix(backendPath, "/backend")+"/network", cookie, `{}`, nil); w.Code != http.StatusForbidden {
		t.Fatal("network bypassed enrolment wall", w.Code)
	}
}
