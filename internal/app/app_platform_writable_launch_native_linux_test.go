//go:build linux && (amd64 || arm64)

package app

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
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
	mustExec(t, s.st, `DELETE FROM app_platform_access_grants WHERE installation_id=?`, installation)
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
