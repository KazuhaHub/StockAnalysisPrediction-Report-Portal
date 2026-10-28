//go:build linux

package driver

import (
	"context"
	"path/filepath"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

// CheckWritableCandidateReadiness freshly verifies an existing running process
// and the protected UID/GID/inode-bound private socket. No launch handle, data
// mutation, provisioning, process restart, registration or activation follows.
func (r *CandidateRuntime) CheckWritableCandidateReadiness(ctx context.Context, request WritableCandidateLaunchRequest, expected WritableCandidateLaunchSnapshot, nativeID, dataFormat string) (WritableCandidateReadinessObservation, error) {
	var zero WritableCandidateReadinessObservation
	if r == nil || !containerSHA.MatchString(nativeID) || !positiveContainerCounter(dataFormat) {
		return zero, ErrCandidateNotReady
	}
	var response appplatform.BackendReadiness
	actual, err := r.observeWritableCandidate(ctx, request, expected, nativeID, func(ctx context.Context) error {
		bounded, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		path := filepath.Join(r.roots.Sockets, request.Identity.InstallationID, request.Identity.Generation, "http", "app.sock")
		var err error
		response, err = probeCandidateReadiness(bounded, path, 65532, 65532)
		if err != nil || !response.Ready || response.DataFormat != dataFormat {
			return ErrCandidateNotReady
		}
		return nil
	})
	if err != nil {
		return zero, err
	}
	result := WritableCandidateReadinessObservation{valid: true, snapshot: WritableCandidateReadinessSnapshot{Launch: actual.Launch, NativeID: actual.NativeID, Protocol: response.Protocol, DataFormat: response.DataFormat, ObservedMS: actual.ObservedMS}}
	if _, err := result.Snapshot(); err != nil {
		return zero, err
	}
	return result, nil
}
