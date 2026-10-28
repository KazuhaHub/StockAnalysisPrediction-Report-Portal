//go:build linux

package driver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"runtime"
	"time"
)

// Inventory includes stopped containers. Exactly one native identity may refer
// to any private mount; the caller must independently inspect its full policy.
// This is lifecycle-scoped observation, not exclusion against host root.
func writableCandidateReference(ctx context.Context, client *http.Client, plan containerPlan) (string, error) {
	if !validContainerPlan(plan) || plan.writable == nil {
		return "", ErrInvalidResponse
	}
	var policy containerCreate
	if json.Unmarshal(plan.body, &policy) != nil {
		return "", ErrInvalidResponse
	}
	entries, err := readCandidateIPCInventory(ctx, client)
	if err != nil {
		return "", err
	}
	found := ""
	seen := map[string]bool{}
	for _, entry := range entries {
		if seen[entry.ID] {
			return "", ErrInvalidResponse
		}
		seen[entry.ID] = true
		sources, err := candidateIPCMountSources(entry.Mounts)
		if err != nil {
			return "", err
		}
		referenced := false
		for _, source := range sources {
			for _, mount := range policy.HostConfig.Mounts {
				matches, err := candidateIPCReferenced(ctx, mount.Source, source)
				if err != nil {
					return "", err
				}
				referenced = referenced || matches
			}
		}
		if referenced {
			if found != "" {
				return "", ErrContainerConflict
			}
			found = entry.ID
		}
	}
	if found == "" {
		return "", ErrContainerUncertain
	}
	return found, ctx.Err()
}

// ObserveWritableCandidate never provisions storage, changes permissions,
// creates/starts/stops a process, or yields a live executable handle. The caller
// must verify fresh recovery authority over the exact expired bound claim.
func (r *CandidateRuntime) ObserveWritableCandidate(ctx context.Context, request WritableCandidateLaunchRequest, expected WritableCandidateLaunchSnapshot) (WritableCandidateRecoveryObservation, error) {
	observed, err := r.observeWritableCandidate(ctx, request, expected, "", nil)
	if err != nil {
		return WritableCandidateRecoveryObservation{}, err
	}
	result := WritableCandidateRecoveryObservation{valid: true, recoveryOnly: true, snapshot: observed}
	if _, err := result.Snapshot(); err != nil {
		return WritableCandidateRecoveryObservation{}, err
	}
	return result, nil
}

// Only trusted internal read-only probes run between the two complete native
// checks. The callback is never exposed to apps, wire callers or the journal.
func (r *CandidateRuntime) observeWritableCandidate(ctx context.Context, request WritableCandidateLaunchRequest, expected WritableCandidateLaunchSnapshot, nativeID string, probe func(context.Context) error) (WritableCandidateRunningSnapshot, error) {
	return r.withVerifiedWritableCandidate(ctx, request, expected, nativeID, func(ctx context.Context, _ *candidateMountPins) error {
		if probe != nil {
			return probe(ctx)
		}
		return nil
	})
}

// Package-private infrastructure action under complete native checks and pinned
// storage. Readiness/recovery supply only reads; registration separately supplies
// its fixed protected-file transition after durable signed journal claiming.
func (r *CandidateRuntime) withVerifiedWritableCandidate(ctx context.Context, request WritableCandidateLaunchRequest, expected WritableCandidateLaunchSnapshot, nativeID string, action func(context.Context, *candidateMountPins) error) (WritableCandidateRunningSnapshot, error) {
	var zero WritableCandidateRunningSnapshot
	if r == nil || os.Geteuid() != 0 {
		return zero, ErrUnsupported
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed || r.root == nil || (nativeID != "" && !containerSHA.MatchString(nativeID)) || !validWritableCandidateRequest(request) || request.Platform != "linux/"+runtime.GOARCH || !validWritableCandidateSnapshot(expected) || expected.Binding != request.Binding || expected.Launch.Identity != request.Identity || expected.Launch.Data != request.Data || expected.Launch.Platform != request.Platform {
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
	if err := r.checkCandidateDataProfile(ctx, request.Identity, request.Data, true); err != nil {
		return zero, err
	}
	image, err := InspectBackendImage(ctx, r.socket, request.Descriptor, request.Identity.ReleaseSHA256)
	if err != nil || image.Platform != request.Platform {
		return zero, errors.Join(ErrInvalidResponse, err)
	}
	plan, err := planWritableCandidateContainer(request.Descriptor, image, candidateOwner(request.Identity), r.roots, request.Binding)
	if err != nil {
		return zero, err
	}
	pins, base, err := r.pinCandidatePreflight(ctx, request.CandidateLaunchRequest, image, plan)
	if err != nil {
		return zero, err
	}
	defer pins.Close()
	actual := WritableCandidateLaunchSnapshot{Launch: base, Binding: request.Binding, SandboxProfile: WritableCandidateSandboxProfile, SeccompSHA256: DataWriterPolicySHA256()}
	if actual != expected {
		return zero, ErrContainerConflict
	}
	client, closeClient := r.client()
	defer closeClient()
	id, err := writableCandidateReference(ctx, client, plan)
	if err != nil {
		return zero, err
	}
	if nativeID != "" && id != nativeID {
		return zero, ErrContainerConflict
	}
	state, err := inspectCandidateContainerHTTP(ctx, client, id, plan)
	if err != nil || state != "running" {
		return zero, errors.Join(ErrContainerUncertain, err)
	}
	if action != nil {
		if err := action(ctx, pins); err != nil {
			return zero, err
		}
	}
	if err := pins.validate(ctx, plan); err != nil {
		return zero, err
	}
	if err := r.checkCandidateDataProfile(ctx, request.Identity, request.Data, true); err != nil {
		return zero, err
	}
	again, err := writableCandidateReference(ctx, client, plan)
	if err != nil || again != id {
		return zero, errors.Join(ErrContainerConflict, err)
	}
	state, err = inspectCandidateContainerHTTP(ctx, client, id, plan)
	if err != nil || state != "running" {
		return zero, errors.Join(ErrContainerUncertain, err)
	}
	if err := pins.validate(ctx, plan); err != nil {
		return zero, err
	}
	if err := ctx.Err(); err != nil {
		return zero, err
	}
	return WritableCandidateRunningSnapshot{Launch: actual, NativeID: id, ObservedMS: time.Now().UnixMilli()}, nil
}
