//go:build linux

package driver

import (
	"bytes"
	"context"
	"io"
	"mime"
	"net/http"
	"path/filepath"
	"time"

	protocol "github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal-Apps/packages/extensionprotocol"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

// ForwardCandidateRequest sends one exact control-authorized request. It never
// retries; any error after dispatch may follow an application side effect.
func (r *CandidateRuntime) ForwardCandidateRequest(ctx context.Context, launch CandidateLaunch, binding BackendRegistrationSnapshot, method, path string, body []byte, assertion string) (BackendRequestObservation, error) {
	var zero BackendRequestObservation
	if r == nil || launch.state == nil || launch.state.owner != r || !containerSHA.MatchString(binding.NativeID) || !containerSHA.MatchString(binding.BindingSHA256) || !containerSHA.MatchString(binding.DocumentSHA256) || binding.Inode == 0 || !appplatform.ValidBackendRequestData(method, path, body, assertion) {
		return zero, ErrContainerUncertain
	}
	body = bytes.Clone(body)
	defer clear(body)
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	r.mu.Lock()
	s := launch.state
	s.mu.Lock()
	if r.closed || s.closed || s.used || binding.Launch != s.snapshot {
		s.mu.Unlock()
		r.mu.Unlock()
		return zero, ErrContainerUncertain
	}
	// Keep this launch pinned while allowing unrelated applications to run.
	// Runtime Close takes these same locks and waits for this IO to settle.
	r.mu.Unlock()
	defer s.mu.Unlock()
	if err := r.checkBackendBindingLocked(ctx, s, binding); err != nil {
		return zero, err
	}
	socket := filepath.Join(r.roots.Sockets, s.snapshot.Identity.InstallationID, s.snapshot.Identity.Generation, "http", "app.sock")
	response, err := forwardBackendRequest(ctx, socket, 65532, 65532, method, path, body, assertion)
	if err != nil {
		return zero, err
	}
	if err := r.checkBackendBindingLocked(ctx, s, binding); err != nil {
		return zero, err
	}
	return BackendRequestObservation{snapshot: BackendRequestSnapshot{Binding: binding, Method: method, Path: path, BodySHA256: containerHash(body), AssertionSHA256: containerHash([]byte(assertion)), Response: response, ObservedMS: time.Now().UnixMilli()}, valid: true}, nil
}
func forwardBackendRequest(ctx context.Context, socket string, uid, gid uint32, method, path string, body []byte, assertion string) (BackendResponse, error) {
	var zero BackendResponse
	if !appplatform.ValidBackendRequestData(method, path, body, assertion) {
		return zero, ErrContainerUncertain
	}
	transport := &http.Transport{Proxy: nil, DisableCompression: true, DisableKeepAlives: true, ResponseHeaderTimeout: 5 * time.Second, MaxResponseHeaderBytes: 4096, DialContext: readinessSocketDialer(socket, uid, gid)}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	request, err := http.NewRequestWithContext(ctx, method, "http://portal-app.invalid"+path, bytes.NewReader(body))
	if err != nil {
		return zero, ErrContainerUncertain
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	request.Header.Set(protocol.Header, assertion)
	response, err := client.Do(request)
	if err != nil {
		return zero, ErrContainerUncertain
	}
	defer response.Body.Close()
	media, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || media != "application/json" || response.Header.Get("Content-Encoding") != "" {
		return zero, ErrContainerUncertain
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, appplatform.MaxBackendResponseBody+1))
	if err != nil || ctx.Err() != nil || !appplatform.ValidBackendResponse(response.StatusCode, raw) {
		clear(raw)
		return zero, ErrContainerUncertain
	}
	// No upstream header, cookie, redirect target or content-disposition is returned.
	return BackendResponse{Status: response.StatusCode, Body: raw}, nil
}
