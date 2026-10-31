//go:build linux

package driver

import (
	"bytes"
	"context"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"path/filepath"
	"time"
)

// Exactly one fixed-socket HTTP exchange. An error after dispatch can follow an
// app-side write; no retry, container start, file repair or permission change.
func (r *CandidateRuntime) ForwardWritableCandidateRequest(ctx context.Context, request WritableCandidateLaunchRequest, receipt WritableBackendRegistrationSnapshot, method, path string, body []byte, assertion string) (WritableBackendRequestObservation, error) {
	var zero WritableBackendRequestObservation
	if r == nil || !appplatform.ValidBackendRequestData(method, path, body, assertion) || receipt.Inode == 0 || !containerSHA.MatchString(receipt.BindingSHA256) || !containerSHA.MatchString(receipt.DocumentSHA256) || !containerSHA.MatchString(receipt.NativeID) {
		return zero, ErrContainerUncertain
	}
	body = bytes.Clone(body)
	defer clear(body)
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	var response BackendResponse
	actual, err := r.withVerifiedWritableCandidate(ctx, request, receipt.Launch, receipt.NativeID, func(ctx context.Context, pins *candidateMountPins) error {
		if err := r.checkWritableBackendFile(ctx, pins, receipt); err != nil {
			return err
		}
		socket := filepath.Join(r.roots.Sockets, request.Identity.InstallationID, request.Identity.Generation, "http", "app.sock")
		var err error
		response, err = forwardBackendRequest(ctx, socket, 65532, 65532, method, path, body, assertion)
		if err != nil {
			return err
		}
		return r.checkWritableBackendFile(ctx, pins, receipt)
	})
	if err != nil {
		clear(response.Body)
		return zero, err
	}
	result := WritableBackendRequestObservation{valid: true, snapshot: WritableBackendRequestSnapshot{Registration: receipt, Method: method, Path: path, BodySHA256: containerHash(body), AssertionSHA256: containerHash([]byte(assertion)), Response: response, ObservedMS: actual.ObservedMS}}
	if _, err := result.Snapshot(); err != nil {
		clear(response.Body)
		return zero, err
	}
	return result, nil
}
