package app

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

// JSON string escaping can expand each byte of the 64 KiB body sixfold.
const platformBackendHTTPBodyLimit = 400 << 10

// Trusted development bootstrap only. Production deliberately does not invoke
// this binder. No session, app package or HTTP payload can select an endpoint.
// A Server's endpoint cannot be replaced while requests are in flight.
func (s *Server) bindPlatformBackendHTTP(endpoint platformRuntimeEndpoint) error {
	if s == nil || s.st == nil || runtime.GOOS != "linux" || !filepath.IsAbs(endpoint.path) || filepath.Clean(endpoint.path) != endpoint.path || len(endpoint.path) > 107 || strings.ContainsAny(endpoint.path, "\x00\r\n") || endpoint.controllerUID == 0 || endpoint.controllerGID == 0 || endpoint.controllerUID == ^uint32(0) || endpoint.controllerGID == ^uint32(0) || uint32(os.Geteuid()) != endpoint.controllerUID || !s.platformBackendEndpoint.CompareAndSwap(nil, &endpoint) {
		return errAppPlatformRuntimeConflict
	}
	return nil
}

type platformBackendHTTPRequest struct {
	binding            platformBackendRequestBinding
	method, path, body string
}

func readPlatformBackendHTTPRequest(w http.ResponseWriter, r *http.Request) (platformBackendHTTPRequest, error) {
	var result platformBackendHTTPRequest
	// The server permits long-lived SSE responses globally. Bound this small
	// request body independently; clear the deadline before native execution.
	controller := http.NewResponseController(w)
	if err := controller.SetReadDeadline(time.Now().Add(5 * time.Second)); err == nil {
		defer controller.SetReadDeadline(time.Time{})
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, platformBackendHTTPBodyLimit))
	if err != nil {
		return result, errPlatformManagementInput
	}
	defer clear(raw)
	fields, err := platformManagementObject(raw, "binding", "method", "path", "body")
	if err != nil || len(fields) != 4 {
		return result, errPlatformManagementInput
	}
	bound, err := platformManagementObject(fields["binding"], "generation", "policyRevision", "releaseSha256")
	if err != nil || len(bound) != 3 {
		return result, errPlatformManagementInput
	}
	for key, dst := range map[string]*string{"generation": &result.binding.Generation, "policyRevision": &result.binding.PolicyRevision, "releaseSha256": &result.binding.ReleaseSHA256} {
		*dst, err = platformManagementString(bound, key)
		if err != nil {
			return platformBackendHTTPRequest{}, err
		}
	}
	for key, dst := range map[string]*string{"method": &result.method, "path": &result.path, "body": &result.body} {
		*dst, err = platformManagementString(fields, key)
		if err != nil {
			return platformBackendHTTPRequest{}, err
		}
	}
	if !result.binding.valid() || !appplatform.ValidBackendRequestRoute(result.method, result.path) || len(result.body) > appplatform.MaxBackendRequestBody || (len(result.body) > 0 && (result.method == "GET" || !json.Valid([]byte(result.body)))) {
		return platformBackendHTTPRequest{}, errPlatformManagementInput
	}
	return result, nil
}

func (s *Server) apiPlatformBackend(w http.ResponseWriter, r *http.Request, user string) {
	if !platformHTTPPath(r) || !platformValidUUID(r.PathValue("installation")) {
		platformDocumentFailure(w)
		return
	}
	if platformManagementOrigin.Check(r) != nil {
		jsonErrorCode(w, 403, "app_platform_origin_denied", "cross-origin application request denied")
		return
	}
	kind, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || kind != "application/json" {
		jsonErrorCode(w, 415, "app_platform_json_required", "application/json required")
		return
	}
	session, err := s.platformHTTPReader(r, user)
	if err != nil {
		jsonErrorCode(w, 401, "session_expired", "session unavailable")
		return
	}
	release, ok := s.platformBackendRequests.acquire()
	if !ok {
		w.Header().Set("Retry-After", "1")
		jsonErrorCode(w, 429, "app_platform_busy", "application busy")
		return
	}
	defer release()
	request, err := readPlatformBackendHTTPRequest(w, r)
	if err != nil {
		jsonErrorCode(w, 400, "app_platform_invalid_request", "invalid application request")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	installation := r.PathValue("installation")
	// Check current authority before exposing whether a runtime was configured.
	access, err := s.withPlatformWritableBackendAccess(ctx, session, installation, func(a platformWritableBackendAccess, _ []byte) error {
		if !request.binding.matches(a) {
			return errAppPlatformLaunchUnavailable
		}
		return nil
	})
	if err != nil {
		platformDocumentFailure(w)
		return
	}
	access.Close()
	endpoint := s.platformBackendEndpoint.Load()
	if endpoint == nil {
		jsonErrorCode(w, 503, "app_platform_runtime_unavailable", "application runtime unavailable")
		return
	}
	response, err := s.requestPlatformBoundWritableBackend(ctx, session, installation, request.binding, request.method, request.path, []byte(request.body), *endpoint)
	if err != nil {
		if errors.Is(err, errAppPlatformBackendRequestUncertain) {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(502)
			json.NewEncoder(w).Encode(map[string]any{"code": "app_platform_request_uncertain", "message": "application request outcome uncertain", "retryable": false})
		} else {
			platformDocumentFailure(w)
		}
		return
	}
	// Never forward application cookies, redirects, authentication status or
	// headers into the Portal session. Preserve exact JSON bytes as a string.
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	json.NewEncoder(w).Encode(map[string]any{"status": response.Status, "body": string(response.Body)})
}
