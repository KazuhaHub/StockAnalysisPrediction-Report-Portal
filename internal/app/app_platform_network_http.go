package app

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"hash"
	"io"
	"mime"
	"net/http"
	"slices"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/broker"
)

// Canonical base64 body (87,384 bytes maximum) plus bounded JSON metadata,
// including six-byte JSON escapes for the URL and two header values.
const platformNetworkHTTPBodyLimit = 128 << 10

type platformNetworkHTTPRequest struct {
	binding                      platformBackendRequestBinding
	method, target, accept, kind string
	credential                   string
	body                         []byte
}

func readPlatformNetworkHTTPRequest(w http.ResponseWriter, r *http.Request) (platformNetworkHTTPRequest, error) {
	var result platformNetworkHTTPRequest
	controller := http.NewResponseController(w)
	if err := controller.SetReadDeadline(time.Now().Add(5 * time.Second)); err == nil {
		defer controller.SetReadDeadline(time.Time{})
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, platformNetworkHTTPBodyLimit))
	if err != nil {
		return result, errPlatformManagementInput
	}
	defer clear(raw)
	fields, err := platformManagementObject(raw, "binding", "method", "url", "accept", "contentType", "bodyBase64", "credential")
	if err != nil || (len(fields) != 6 && len(fields) != 7) {
		return result, errPlatformManagementInput
	}
	bound, err := platformManagementObject(fields["binding"], "generation", "policyRevision", "releaseSha256")
	if err != nil || len(bound) != 3 {
		return result, errPlatformManagementInput
	}
	for key, dst := range map[string]*string{"generation": &result.binding.Generation, "policyRevision": &result.binding.PolicyRevision, "releaseSha256": &result.binding.ReleaseSHA256} {
		*dst, err = platformManagementString(bound, key)
		if err != nil {
			return platformNetworkHTTPRequest{}, errPlatformManagementInput
		}
	}
	var body string
	for key, dst := range map[string]*string{"method": &result.method, "url": &result.target, "accept": &result.accept, "contentType": &result.kind, "bodyBase64": &body} {
		*dst, err = platformManagementString(fields, key)
		if err != nil {
			return platformNetworkHTTPRequest{}, errPlatformManagementInput
		}
	}
	if rawCredential, ok := fields["credential"]; ok {
		result.credential, err = platformManagementString(fields, "credential")
		if err != nil || !platformSecretBindingNamePattern.MatchString(result.credential) || len(rawCredential) == 0 {
			return platformNetworkHTTPRequest{}, errPlatformManagementInput
		}
	}
	if !result.binding.valid() || !slices.Contains([]string{"GET", "HEAD", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"}, result.method) || len(result.target) == 0 || len(result.target) > 4096 || !appplatform.ValidHTTPPolicyHeaderValue(result.accept) || !appplatform.ValidHTTPPolicyHeaderValue(result.kind) || len(body) > base64.StdEncoding.EncodedLen(appplatform.MaxHTTPRequestBytes) {
		return platformNetworkHTTPRequest{}, errPlatformManagementInput
	}
	decoded, err := base64.StdEncoding.Strict().DecodeString(body)
	if err != nil || len(decoded) > appplatform.MaxHTTPRequestBytes || base64.StdEncoding.EncodeToString(decoded) != body {
		clear(decoded)
		return platformNetworkHTTPRequest{}, errPlatformManagementInput
	}
	result.body = decoded
	return result, nil
}

// Uses the same trusted development bootstrap endpoint as the own-backend
// gateway. The request never selects a runner, actor, policy or credential.
func (s *Server) apiPlatformNetwork(w http.ResponseWriter, r *http.Request, user string) {
	controller := http.NewResponseController(w)
	if err := controller.SetWriteDeadline(time.Now().Add(30 * time.Second)); err == nil {
		defer controller.SetWriteDeadline(time.Time{})
	}
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
	release, ok := s.platformNetworkRequests.acquire()
	if !ok {
		platformJSONBusy(w)
		return
	}
	defer release()
	request, err := readPlatformNetworkHTTPRequest(w, r)
	if err != nil {
		jsonErrorCode(w, 400, "app_platform_invalid_request", "invalid application request")
		return
	}
	defer clear(request.body)
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	installation := r.PathValue("installation")
	// The complete current grant and reviewed target are checked before revealing
	// runtime configuration. This preflight creates no durable execution or IO.
	intent, err := s.preparePlatformNetworkRequest(ctx, session, installation, request.binding, request.method, request.target, request.accept, request.kind, request.credential, request.body)
	if err != nil {
		platformDocumentFailure(w)
		return
	}
	intent.Close()
	endpoint := s.platformBackendEndpoint.Load()
	if endpoint == nil {
		jsonErrorCode(w, 503, "app_platform_runtime_unavailable", "application runtime unavailable")
		return
	}
	response, err := s.requestPlatformNetwork(ctx, session, installation, request.binding, request.method, request.target, request.accept, request.kind, request.credential, request.body, *endpoint)
	if err != nil {
		if errors.Is(err, errAppPlatformNetworkUncertain) {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(502)
			_ = json.NewEncoder(w).Encode(map[string]any{"code": "app_platform_request_uncertain", "message": "application request outcome uncertain", "retryable": false})
		} else {
			platformDocumentFailure(w)
		}
		return
	}
	defer clear(response.Body)
	// Preserve binary bytes exactly; never forward remote cookies, authentication,
	// redirects or arbitrary headers into a Portal response/session.
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(map[string]any{"status": response.Status, "contentType": response.ContentType, "bodyBase64": base64.StdEncoding.EncodeToString(response.Body)})
}

type platformNetworkStreamWriter struct {
	writer      http.ResponseWriter
	encoder     *json.Encoder
	flusher     http.Flusher
	status      int
	contentType string
	sequence    int
	bytes       int64
	digest      hash.Hash
	started     bool
	ended       bool
}

func newPlatformNetworkStreamWriter(w http.ResponseWriter) (*platformNetworkStreamWriter, error) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		return nil, errAppPlatformNetworkUnavailable
	}
	return &platformNetworkStreamWriter{writer: w, encoder: json.NewEncoder(w), flusher: flusher, digest: sha256.New()}, nil
}

func (w *platformNetworkStreamWriter) Start(status int, contentType string) error {
	if w.started || status < 200 || status >= 300 || len(contentType) > 256 {
		return errAppPlatformNetworkUncertain
	}
	w.writer.Header().Set("Content-Type", "application/x-ndjson; charset=utf-8")
	w.writer.WriteHeader(http.StatusOK)
	w.started = true
	w.status, w.contentType = status, contentType
	if err := w.encode(map[string]any{"type": "start", "status": status, "contentType": contentType}); err != nil {
		return err
	}
	w.flusher.Flush()
	return nil
}

func (w *platformNetworkStreamWriter) Chunk(data []byte) error {
	if !w.started || w.ended || len(data) < 1 || len(data) > appplatform.MaxHTTPStreamFrameBytes || w.sequence >= appplatform.MaxHTTPStreamChecks {
		return errAppPlatformNetworkUncertain
	}
	w.sequence++
	w.bytes += int64(len(data))
	_, _ = w.digest.Write(data)
	if err := w.encode(map[string]any{
		"type": "chunk", "sequence": w.sequence,
		"bodyBase64": base64.StdEncoding.EncodeToString(data),
	}); err != nil {
		return err
	}
	w.flusher.Flush()
	return nil
}

func (w *platformNetworkStreamWriter) End() error {
	if !w.started || w.ended {
		return errAppPlatformNetworkUncertain
	}
	w.ended = true
	if err := w.encode(map[string]any{
		"type": "end", "sequence": w.sequence, "bytes": w.bytes,
		"sha256": hex.EncodeToString(w.digest.Sum(nil)),
	}); err != nil {
		return err
	}
	w.flusher.Flush()
	return nil
}

func (w *platformNetworkStreamWriter) Failure() {
	if !w.started || w.ended {
		return
	}
	w.ended = true
	_ = w.encode(map[string]any{"type": "error", "code": "uncertain", "retryable": false})
	w.flusher.Flush()
}

func (w *platformNetworkStreamWriter) encode(value any) error {
	w.encoder.SetEscapeHTML(false)
	return w.encoder.Encode(value)
}

func (s *Server) apiPlatformNetworkStream(w http.ResponseWriter, r *http.Request, user string) {
	controller := http.NewResponseController(w)
	if err := controller.SetWriteDeadline(time.Now().Add(platformNetworkStreamExecutionTimeout)); err == nil {
		defer controller.SetWriteDeadline(time.Time{})
	}
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
	release, ok := s.platformNetworkStreams.acquire()
	if !ok {
		platformJSONBusy(w)
		return
	}
	defer release()
	request, err := readPlatformNetworkHTTPRequest(w, r)
	if err != nil {
		jsonErrorCode(w, 400, "app_platform_invalid_request", "invalid application request")
		return
	}
	defer clear(request.body)
	streamWriter, err := newPlatformNetworkStreamWriter(w)
	if err != nil {
		platformDocumentFailure(w)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), platformNetworkStreamExecutionTimeout)
	defer cancel()
	_, err = s.requestPlatformNetworkStream(
		ctx, session, r.PathValue("installation"), request.binding,
		request.method, request.target, request.accept, request.kind,
		request.credential, request.body, s.platformBackendEndpoint.Load(),
		broker.HTTPStreamCallbacks{
			Start: streamWriter.Start,
			Chunk: streamWriter.Chunk,
		},
	)
	if err != nil {
		if streamWriter.started {
			streamWriter.Failure()
			return
		}
		if errors.Is(err, errAppPlatformNetworkUncertain) {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(502)
			_ = json.NewEncoder(w).Encode(map[string]any{"code": "app_platform_request_uncertain", "message": "application request outcome uncertain", "retryable": false})
			return
		}
		platformDocumentFailure(w)
		return
	}
	if err := streamWriter.End(); err != nil {
		streamWriter.Failure()
	}
}
