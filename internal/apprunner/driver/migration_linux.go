//go:build linux

package driver

import (
	"bytes"
	"context"
	"errors"
	"runtime"
	"time"
)

const migrationPollInterval = 500 * time.Millisecond

func (r *CandidateRuntime) migrationPlan(ctx context.Context, request MigrationJobRequest) (containerPlan, error) {
	image, err := InspectBackendImage(ctx, r.socket, request.Descriptor, request.Identity.ReleaseSHA256)
	if err != nil || image.Platform != request.Platform {
		return containerPlan{}, errors.Join(ErrInvalidResponse, err)
	}
	return planMigrationContainer(request.Descriptor, image, candidateOwner(request.Identity), r.roots, request.Binding, request.Step)
}

// RunMigrationJob runs one claimed migration job to a definite end: created, started, waited for,
// killed at the step's wall clock, inspected for its exit code, removed and proven absent by name and
// label. The caller owns the one-shot journal claim and passes a context that outlives its connection
// and covers the wall clock plus MigrationGrace. No retry is made: any error after the create is
// uncertain, and a job that cannot be removed stays an unknown effect.
func (r *CandidateRuntime) RunMigrationJob(ctx context.Context, request MigrationJobRequest) (MigrationJobObservation, error) {
	var zero MigrationJobObservation
	if r == nil {
		return zero, ErrUnsupported
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed || r.root == nil || !migrationRequestValid(request) || request.Platform != "linux/"+runtime.GOARCH || candidateSeccompError != nil {
		return zero, ErrInvalidResponse
	}
	request.Descriptor = bytes.Clone(request.Descriptor)
	if err := r.checkCandidateDataProfile(ctx, request.Identity, request.Data, true); err != nil {
		return zero, err
	}
	plan, err := r.migrationPlan(ctx, request)
	if err != nil {
		return zero, err
	}
	client, closeClient := r.client()
	defer closeClient()
	if err := proveMigrationAbsentHTTP(ctx, client, plan); err != nil {
		return zero, err
	}
	result, err := runMigrationJobHTTP(ctx, client, plan, MigrationWallClock(request.Step), migrationPollInterval)
	if err != nil {
		return zero, errors.Join(ErrContainerUncertain, err)
	}
	if err := removeAndProveMigrationAbsentHTTP(ctx, client, plan, result.NativeID); err != nil {
		return zero, errors.Join(ErrContainerUncertain, err)
	}
	exit := result.ExitCode
	if exit < 0 {
		exit = 0
	}
	observation := MigrationJobObservation{valid: true, snapshot: MigrationJobSnapshot{
		Binding: request.Binding, Identity: request.Identity, NativeID: result.NativeID,
		ImageID: plan.imageID(), Platform: request.Platform, ExitCode: exit, Killed: result.Killed,
		ObservedMS: time.Now().UnixMilli(),
	}}
	if _, err := observation.Snapshot(); err != nil {
		return zero, errors.Join(ErrContainerUncertain, err)
	}
	return observation, nil
}

// ProveMigrationAbsent finds no container by the job's name or label, and removes an exited leftover
// first. A running job is stopped by kill, never forced away. It makes no claim about the data.
func (r *CandidateRuntime) ProveMigrationAbsent(ctx context.Context, request MigrationJobRequest) error {
	if r == nil {
		return ErrUnsupported
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed || !migrationRequestValid(request) || candidateSeccompError != nil {
		return ErrInvalidResponse
	}
	plan, err := r.migrationPlan(ctx, request)
	if err != nil {
		return err
	}
	client, closeClient := r.client()
	defer closeClient()
	return discardMigrationJobHTTP(ctx, client, plan)
}
