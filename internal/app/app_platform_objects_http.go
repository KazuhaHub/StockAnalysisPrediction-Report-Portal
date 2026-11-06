package app

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
)

func platformObjectHTTPFailure(w http.ResponseWriter, err error) {
	status, code := http.StatusNotFound, "app_platform_object_unavailable"
	switch {
	case errors.Is(err, errPlatformObjectInvalid):
		status, code = http.StatusBadRequest, "app_platform_object_invalid_request"
	case errors.Is(err, errPlatformObjectConflict):
		status, code = http.StatusConflict, "app_platform_object_conflict"
	case errors.Is(err, errPlatformObjectStorageFull):
		status, code = http.StatusRequestEntityTooLarge, "app_platform_object_storage_full"
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		status, code = http.StatusBadGateway, "app_platform_object_uncertain"
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	writeJSON(w, map[string]any{"ok": false, "error": map[string]string{"code": code, "message": "object unavailable"}})
}

func decodePlatformObjectHTTPRequest(r *http.Request) (platformObjectRequest, error) {
	if r.ContentLength > maxPlatformObjectRequestBytes || !strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")), "application/json") {
		return platformObjectRequest{}, errPlatformObjectInvalid
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, maxPlatformObjectRequestBytes+1))
	if err != nil || len(raw) == 0 || len(raw) > maxPlatformObjectRequestBytes {
		return platformObjectRequest{}, errPlatformObjectInvalid
	}
	return parsePlatformObjectRequest(raw)
}

func (s *Server) apiPlatformObjects(w http.ResponseWriter, r *http.Request, user string) {
	if !platformHTTPPath(r) || !platformValidUUID(r.PathValue("installation")) {
		platformObjectHTTPFailure(w, errPlatformObjectNotFound)
		return
	}
	session, release, ok := s.platformHTTPAdmission(w, r, user)
	if !ok {
		return
	}
	defer release()
	request, err := decodePlatformObjectHTTPRequest(r)
	if err != nil {
		platformObjectHTTPFailure(w, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), platformDocumentTimeout)
	defer cancel()
	result, err := s.executePlatformObjectRequest(ctx, session, r.PathValue("installation"), request.binding, request)
	if err != nil {
		platformObjectHTTPFailure(w, err)
		return
	}
	response := map[string]any{"ok": true, "action": result.Action}
	switch result.Action {
	case "list":
		response["items"] = result.Items
		if result.NextCursor != "" {
			response["nextCursor"] = result.NextCursor
		}
	case "get":
		if result.Object == nil {
			platformObjectHTTPFailure(w, errPlatformObjectNotFound)
			return
		}
		response["object"] = result.Object.platformObjectSummary
		response["contentBase64"] = base64.StdEncoding.EncodeToString(result.Object.Content)
	case "create", "update":
		if result.Object == nil {
			platformObjectHTTPFailure(w, errPlatformObjectNotFound)
			return
		}
		response["object"] = result.Object.platformObjectSummary
	case "delete":
		response["id"] = result.DeletedID
	default:
		platformObjectHTTPFailure(w, errPlatformObjectInvalid)
		return
	}
	raw, err := json.Marshal(response)
	if err != nil {
		platformObjectHTTPFailure(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(raw)
}
