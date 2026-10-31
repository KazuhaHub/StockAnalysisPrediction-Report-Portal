//go:build linux

package driver

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

// CheckUpdateStartReadiness re-reads the exact target container, data profile,
// pinned mounts and private readiness socket. Running is required but is not
// itself readiness evidence.
func (r *CandidateRuntime) CheckUpdateStartReadiness(ctx context.Context, request UpdateStartLaunchRequest, expected UpdateStartLaunchSnapshot, nativeID, dataFormat string) (UpdateStartReadinessObservation, error) {
	var zero UpdateStartReadinessObservation
	if r == nil || !containerSHA.MatchString(nativeID) || !positiveContainerCounter(dataFormat) {
		return zero, ErrCandidateNotReady
	}
	var response appplatform.BackendReadiness
	actual, err := r.withVerifiedUpdateStartCandidate(ctx, request, expected, nativeID, func(ctx context.Context, _ *candidateMountPins) error {
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
	result := UpdateStartReadinessObservation{
		valid: true,
		snapshot: UpdateStartReadinessSnapshot{
			Launch: actual.Launch, NativeID: actual.NativeID,
			Protocol: response.Protocol, DataFormat: response.DataFormat,
			ObservedMS: actual.ObservedMS,
		},
	}
	if _, err := result.Snapshot(); err != nil {
		return zero, err
	}
	return result, nil
}

func (r *CandidateRuntime) withVerifiedUpdateStartCandidate(ctx context.Context, request UpdateStartLaunchRequest, expected UpdateStartLaunchSnapshot, nativeID string, action func(context.Context, *candidateMountPins) error) (UpdateStartRunningSnapshot, error) {
	var zero UpdateStartRunningSnapshot
	if r == nil || os.Geteuid() != 0 {
		return zero, ErrUnsupported
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed || r.root == nil || !validUpdateStartRequest(request) ||
		!validUpdateStartSnapshot(expected) || expected.Binding != request.Binding ||
		expected.Launch.Identity != request.Identity || expected.Launch.Data != request.Data ||
		expected.Launch.Platform != request.Platform ||
		expected.SandboxProfile != request.SandboxProfile {
		return zero, ErrInvalidResponse
	}
	request.Descriptor = bytes.Clone(request.Descriptor)
	for live := range r.launches {
		if live.snapshot.Identity.NamespaceID == request.Identity.NamespaceID {
			return zero, ErrContainerConflict
		}
	}
	report, err := InspectDaemon(ctx, r.socket)
	if err != nil || !report.MetadataCompatible {
		return zero, errors.Join(ErrUnsupported, err)
	}
	accessWritable := request.SandboxProfile == UpdateStartWritableSandboxProfile
	if err := r.checkCandidateDataProfile(ctx, request.Identity, request.Data, accessWritable); err != nil {
		return zero, err
	}
	image, err := InspectBackendImage(ctx, r.socket, request.Descriptor, request.Identity.ReleaseSHA256)
	if err != nil || image.Platform != request.Platform {
		return zero, errors.Join(ErrInvalidResponse, err)
	}
	plan, err := planUpdateStartContainer(request.Descriptor, image, candidateOwner(request.Identity), r.roots, request.Binding, request.SandboxProfile)
	if err != nil {
		return zero, err
	}
	pins, base, err := r.pinCandidatePreflight(ctx, request.CandidateLaunchRequest, image, plan)
	if err != nil {
		return zero, err
	}
	defer pins.Close()
	actual := UpdateStartLaunchSnapshot{
		Launch: base, Binding: request.Binding, SandboxProfile: request.SandboxProfile,
		SeccompSHA256: UpdateStartSeccompSHA256(request.SandboxProfile),
	}
	if actual != expected {
		return zero, ErrContainerConflict
	}
	client, closeClient := r.client()
	defer closeClient()
	native, err := managedCandidateReference(ctx, client, plan)
	if err != nil || native != nativeID {
		return zero, errors.Join(ErrContainerConflict, err)
	}
	state, err := inspectCandidateContainerHTTP(ctx, client, native, plan)
	if err != nil || state != "running" {
		return zero, errors.Join(ErrContainerUncertain, err)
	}
	if err := action(ctx, pins); err != nil {
		return zero, err
	}
	if err := pins.validate(ctx, plan); err != nil {
		return zero, err
	}
	if err := r.checkCandidateDataProfile(ctx, request.Identity, request.Data, accessWritable); err != nil {
		return zero, err
	}
	again, err := managedCandidateReference(ctx, client, plan)
	if err != nil || again != native {
		return zero, errors.Join(ErrContainerConflict, err)
	}
	state, err = inspectCandidateContainerHTTP(ctx, client, native, plan)
	if err != nil || state != "running" || ctx.Err() != nil {
		return zero, errors.Join(ErrContainerUncertain, err)
	}
	if err := pins.validate(ctx, plan); err != nil {
		return zero, err
	}
	return UpdateStartRunningSnapshot{Launch: expected, NativeID: native, ObservedMS: time.Now().UnixMilli()}, nil
}
