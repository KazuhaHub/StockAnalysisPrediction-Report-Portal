//go:build linux

package driver

import (
	"bytes"
	"context"
	"path/filepath"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

// Exactly one fixed-socket HTTP exchange against the generation selected by the
// signed update-start registration. Errors after dispatch may follow a write.
func (r *CandidateRuntime) ForwardUpdateStartCandidateRequest(ctx context.Context, request UpdateStartLaunchRequest, expected UpdateStartLaunchSnapshot, registration UpdateStartRegistrationSnapshot, descriptor []byte, method, path string, body []byte, assertion string) (UpdateStartBackendRequestObservation, error) {
	var zero UpdateStartBackendRequestObservation
	if r == nil ||
		!appplatform.ValidBackendRequestData(method, path, body, assertion) ||
		!containerSHA.MatchString(registration.NativeID) ||
		!containerSHA.MatchString(registration.BindingSHA256) ||
		len(descriptor) == 0 ||
		len(descriptor) > appplatform.MaxDescriptorBytes {
		return zero, ErrContainerUncertain
	}
	body = bytes.Clone(body)
	defer clear(body)
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	var response BackendResponse
	actual, err := r.withVerifiedUpdateStartCandidate(
		ctx, request, expected, registration.NativeID,
		func(ctx context.Context, pins *candidateMountPins) error {
			if err := checkUpdateStartBackendFile(
				ctx, r, pins, registration); err != nil {
				return err
			}
			socket := filepath.Join(
				r.roots.Sockets, request.Identity.InstallationID,
				request.Identity.Generation, "http", "app.sock")
			var err error
			response, err = forwardBackendRequest(
				ctx, socket, 65532, 65532, method, path, body, assertion)
			if err != nil {
				return err
			}
			return checkUpdateStartBackendFile(
				ctx, r, pins, registration)
		})
	if err != nil {
		clear(response.Body)
		return zero, err
	}
	result := UpdateStartBackendRequestObservation{
		valid: true,
		snapshot: UpdateStartBackendRequestSnapshot{
			Registration: registration, Method: method, Path: path,
			BodySHA256:      containerHash(body),
			AssertionSHA256: containerHash([]byte(assertion)),
			Response:        response, ObservedMS: actual.ObservedMS,
		},
	}
	if _, err := result.Snapshot(); err != nil {
		clear(response.Body)
		return zero, err
	}
	return result, nil
}
