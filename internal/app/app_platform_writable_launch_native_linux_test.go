//go:build linux && (amd64 || arm64)

package app

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	protocol "github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal-Apps/packages/extensionprotocol"
)

// Runs after the real background installation and authenticated business IO.
// This checks the actual session-only HTTP paths; no browser rendering or new
// backend bridge capability is implied by serving the frontend document.
func testNativeWritableLaunchHTTP(t *testing.T, s *Server, actor platformActorSession, installation, release string, endpoint platformRuntimeEndpoint) {
	t.Helper()
	cookie := s.managedCookie(*s.st.GetUser(actor.username), actor.sessionID, actor.expires.Unix())
	mux := http.NewServeMux()
	s.wireRoutes(mux)
	path := "/api/app-platform/installations/" + installation + "/launch"
	response := sessionTestRequest(mux, "GET", path, cookie, "")
	var launch struct {
		Source       string            `json:"source"`
		Binding      map[string]string `json:"binding"`
		Capabilities []string          `json:"capabilities"`
	}
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &launch) != nil || launch.Source == "" || launch.Binding["installationId"] != installation || launch.Binding["releaseSha256"] != release || len(launch.Capabilities) != 0 {
		t.Fatal("native installed writable launch HTTP", response.Code, response.Body.String())
	}
	doc := sessionTestRequest(mux, "GET", launch.Source, cookie, "")
	if doc.Code != 200 || !strings.Contains(doc.Body.String(), "Fixture") || doc.Header().Get("Cache-Control") != "no-store" || !strings.Contains(doc.Header().Get("Content-Security-Policy"), "sandbox allow-scripts") || strings.Contains(doc.Header().Get("Content-Security-Policy"), "allow-same-origin") {
		t.Fatal("native writable document HTTP", doc.Code)
	}
	directory := sessionTestRequest(mux, "GET", "/api/app-platform/apps", cookie, "")
	var apps platformAppDirectory
	if directory.Code != 200 || json.Unmarshal(directory.Body.Bytes(), &apps) != nil {
		t.Fatal("native directory HTTP", directory.Code)
	}
	found := false
	for _, item := range apps.Items {
		if item.InstallationID == installation {
			found = item.Openable && item.State == "installed"
		}
	}
	if !found {
		t.Fatal("native installed writable app missing from directory")
	}
	t.Log("PORTAL_WRITABLE_CURRENT_DOCUMENT_HTTP")
	t.Log("PORTAL_WRITABLE_CURRENT_DIRECTORY_HTTP")
	binding := platformBackendRequestBinding{Generation: launch.Binding["generation"], PolicyRevision: launch.Binding["policyRevision"], ReleaseSHA256: launch.Binding["releaseSha256"]}
	var before struct {
		Count int64 `json:"count"`
		Calls int64 `json:"calls"`
	}
	result, err := s.requestPlatformBoundWritableBackend(context.Background(), actor, installation, binding, "GET", "/v1/echo", nil, endpoint)
	if err != nil || result.Status != 200 || json.Unmarshal(result.Body, &before) != nil {
		t.Fatal("current page binding did not reach native backend", err)
	}
	for _, stale := range []platformBackendRequestBinding{
		{Generation: "2", PolicyRevision: binding.PolicyRevision, ReleaseSHA256: binding.ReleaseSHA256},
		{Generation: binding.Generation, PolicyRevision: "9223372036854775807", ReleaseSHA256: binding.ReleaseSHA256},
		{Generation: binding.Generation, PolicyRevision: binding.PolicyRevision, ReleaseSHA256: strings.Repeat("0", 64)},
	} {
		result, err := s.requestPlatformBoundWritableBackend(context.Background(), actor, installation, stale, "POST", "/v1/echo", []byte(`{"stale":true}`), endpoint)
		if !errors.Is(err, errAppPlatformLaunchUnavailable) || errors.Is(err, errAppPlatformBackendRequestUncertain) || len(result.Body) != 0 {
			t.Fatal("stale page reached native backend", err)
		}
	}
	var after struct {
		Count int64 `json:"count"`
		Calls int64 `json:"calls"`
	}
	result, err = s.requestPlatformBoundWritableBackend(context.Background(), actor, installation, binding, "GET", "/v1/echo", nil, endpoint)
	if err != nil || result.Status != 200 || json.Unmarshal(result.Body, &after) != nil || after.Count != before.Count || after.Calls != before.Calls+1 {
		t.Fatal("stale binding performed business IO", err)
	}
	t.Log("PORTAL_WRITABLE_PAGE_BINDING_NATIVE")
	if err := s.bindPlatformBackendHTTP(endpoint); err != nil {
		t.Fatal("trusted HTTP runtime bootstrap", err)
	}
	if err := s.bindPlatformBackendHTTP(endpoint); err == nil {
		t.Fatal("HTTP runtime endpoint was replaceable")
	}
	metadata := sessionTestRequest(mux, "GET", path, cookie, "")
	var available struct {
		BackendAvailable bool `json:"backendAvailable"`
	}
	if metadata.Code != 200 || json.Unmarshal(metadata.Body.Bytes(), &available) != nil || !available.BackendAvailable {
		t.Fatal("trusted endpoint did not advertise own-backend transport", metadata.Code)
	}
	backendPath := "/api/app-platform/installations/" + installation + "/backend"
	requestBody := func(method, path, body string) string {
		raw, _ := json.Marshal(map[string]any{"binding": binding, "method": method, "path": path, "body": body})
		return string(raw)
	}
	httpBody := requestBody("POST", "/v1/echo", `{"role":"admin","subject":"forged"}`)
	staleHTTP := strings.Replace(httpBody, `"generation":"1"`, `"generation":"2"`, 1)
	if denied := platformBackendHTTPCall(mux, backendPath, cookie, staleHTTP, nil); denied.Code != 404 {
		t.Fatal("stale page passed HTTP admission", denied.Code)
	}
	response = platformBackendHTTPCall(mux, backendPath, cookie, httpBody, map[string]string{"Sec-Fetch-Site": "same-origin"})
	var envelope struct {
		Status int    `json:"status"`
		Body   string `json:"body"`
	}
	var echoed struct {
		Count  int64           `json:"count"`
		Calls  int64           `json:"calls"`
		Viewer protocol.Viewer `json:"viewer"`
	}
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &envelope) != nil || envelope.Status != 200 || json.Unmarshal([]byte(envelope.Body), &echoed) != nil || echoed.Count != after.Count+1 || echoed.Calls != after.Calls+1 || echoed.Viewer.Role != "user" || len(echoed.Viewer.Subject) != 64 || response.Header().Get("Set-Cookie") != "" || response.Header().Get("Location") != "" || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("ordinary session HTTP request did not reach current native viewer", response.Code, response.Body.String())
	}
	t.Log("PORTAL_WRITABLE_BACKEND_SESSION_HTTP")
	uncertain := platformBackendHTTPCall(mux, backendPath, cookie, requestBody("POST", "/v1/echo?disconnect=1", `{}`), nil)
	var failure struct {
		Code      string `json:"code"`
		Retryable *bool  `json:"retryable"`
	}
	if uncertain.Code != 502 || json.Unmarshal(uncertain.Body.Bytes(), &failure) != nil || failure.Code != "app_platform_request_uncertain" || failure.Retryable == nil || *failure.Retryable || uncertain.Header().Get("Retry-After") != "" {
		t.Fatal("HTTP uncertain write was represented as retryable", uncertain.Code, uncertain.Body.String())
	}
	response = platformBackendHTTPCall(mux, backendPath, cookie, requestBody("GET", "/v1/echo", ""), nil)
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &envelope) != nil || envelope.Status != 200 || json.Unmarshal([]byte(envelope.Body), &echoed) != nil || echoed.Count != after.Count+2 || echoed.Calls != after.Calls+3 {
		t.Fatal("uncertain HTTP write was retried or lost", response.Code, response.Body.String())
	}
	t.Log("PORTAL_WRITABLE_BACKEND_HTTP_NO_RETRY")
	testNativeSDKBackendBrowser(t, s, actor, installation, mux, false, false)
	mustExec(t, s.st, `DELETE FROM app_platform_access_grants WHERE installation_id=?`, installation)
	if response := platformBackendHTTPCall(mux, backendPath, cookie, httpBody, nil); response.Code != 404 || strings.Contains(response.Body.String(), "viewer") {
		t.Fatal("HTTP backend remained available after audience revocation", response.Code)
	}
	if result, err := s.requestPlatformBoundWritableBackend(context.Background(), actor, installation, binding, "POST", "/v1/echo", []byte(`{}`), endpoint); err == nil || errors.Is(err, errAppPlatformBackendRequestUncertain) || len(result.Body) != 0 {
		t.Fatal("page binding bypassed revoked audience", err)
	}
	for _, target := range []string{path, launch.Source} {
		if denied := sessionTestRequest(mux, "GET", target, cookie, ""); denied.Code != 404 || strings.Contains(denied.Body.String(), "Fixture") {
			t.Fatal("native old URL survived current grant revocation", denied.Code)
		}
	}
	directory = sessionTestRequest(mux, "GET", "/api/app-platform/apps", cookie, "")
	if directory.Code != 200 || json.Unmarshal(directory.Body.Bytes(), &apps) != nil {
		t.Fatal("native revoked directory HTTP", directory.Code)
	}
	for _, item := range apps.Items {
		if item.InstallationID == installation {
			t.Fatal("native revoked app remained visible")
		}
	}
	t.Log("PORTAL_WRITABLE_DOCUMENT_HTTP_REVOKED")
}
