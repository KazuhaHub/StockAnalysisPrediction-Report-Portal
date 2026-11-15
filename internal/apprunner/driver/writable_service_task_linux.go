//go:build linux

package driver

import (
	"bytes"
	"context"
	"path/filepath"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

// Exactly one fixed-socket task POST against the generation-1 writable
// candidate. An error after dispatch can follow an app-side write; no retry,
// container start, file repair or permission change.
func (r *CandidateRuntime) ForwardWritableCandidateServiceTask(ctx context.Context, request WritableCandidateLaunchRequest, receipt WritableBackendRegistrationSnapshot, body, serviceIdentity []byte, handler string) (WritableServiceTaskObservation, error) {
	var zero WritableServiceTaskObservation
	path, ok := appplatform.TaskHandlerPath(handler)
	if r == nil || !ok || receipt.Inode == 0 || !containerSHA.MatchString(receipt.BindingSHA256) || !containerSHA.MatchString(receipt.DocumentSHA256) || !containerSHA.MatchString(receipt.NativeID) || len(body) == 0 || len(body) > appplatform.MaxTaskBackendBody || len(serviceIdentity) == 0 || len(serviceIdentity) > 8192 {
		return zero, ErrContainerUncertain
	}
	body, serviceIdentity = bytes.Clone(body), bytes.Clone(serviceIdentity)
	defer clear(body)
	defer clear(serviceIdentity)
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	var response BackendResponse
	actual, err := r.withVerifiedWritableCandidate(ctx, request, receipt.Launch, receipt.NativeID, func(ctx context.Context, pins *candidateMountPins) error {
		if err := r.checkWritableBackendFile(ctx, pins, receipt); err != nil {
			return err
		}
		socket := filepath.Join(r.roots.Sockets, request.Identity.InstallationID, request.Identity.Generation, "http", "app.sock")
		var err error
		response, err = forwardServiceTask(ctx, socket, 65532, 65532, path, body, serviceIdentity)
		if err != nil {
			return err
		}
		return r.checkWritableBackendFile(ctx, pins, receipt)
	})
	if err != nil {
		clear(response.Body)
		return zero, err
	}
	result := WritableServiceTaskObservation{valid: true, snapshot: WritableServiceTaskSnapshot{Registration: receipt, Handler: handler, BodySHA256: containerHash(body), ServiceIdentitySHA256: containerHash(serviceIdentity), Response: response, ObservedMS: actual.ObservedMS}}
	if _, err := result.Snapshot(); err != nil {
		clear(response.Body)
		return zero, err
	}
	return result, nil
}
