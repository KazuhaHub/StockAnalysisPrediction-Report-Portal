//go:build linux

package driver

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
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

// ProveTargetAbsent finds no container by the name or the label a target's resource would have given it. A target
// that was reserved and never created has none; one that exists is a conflict, never removed here: this proves an
// absence and changes nothing.
func (r *CandidateRuntime) ProveTargetAbsent(ctx context.Context, resourceID string) error {
	if r == nil {
		return ErrUnsupported
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed || !containerUUID.MatchString(resourceID) || resourceID == "00000000-0000-0000-0000-000000000000" {
		return ErrInvalidResponse
	}
	client, closeClient := r.client()
	defer closeClient()
	for _, filter := range []map[string][]string{
		{"name": {"^/portal-app-" + resourceID + "$"}},
		{"label": {containerLabelPrefix + "resource=" + resourceID}},
	} {
		found, err := listMigrationContainersHTTP(ctx, client, filter)
		if err != nil {
			return err
		}
		if len(found) != 0 {
			return ErrContainerConflict
		}
	}
	return ctx.Err()
}

// InspectTarget reports the container a target's resource would own, found by name and by label. Both must agree: a
// renamed container still carries the label, and neither is ever created, started, stopped or removed here.
func (r *CandidateRuntime) InspectTarget(ctx context.Context, resourceID string) (TargetInspection, error) {
	if r == nil {
		return TargetInspection{}, ErrUnsupported
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed || !containerUUID.MatchString(resourceID) || resourceID == "00000000-0000-0000-0000-000000000000" {
		return TargetInspection{}, ErrInvalidResponse
	}
	client, closeClient := r.client()
	defer closeClient()
	byName, err := listMigrationContainersHTTP(ctx, client, map[string][]string{"name": {"^/portal-app-" + resourceID + "$"}})
	if err != nil {
		return TargetInspection{}, err
	}
	byLabel, err := listMigrationContainersHTTP(ctx, client, map[string][]string{"label": {containerLabelPrefix + "resource=" + resourceID}})
	if err != nil {
		return TargetInspection{}, err
	}
	if len(byName) != len(byLabel) || len(byName) > 1 || (len(byName) == 1 && byName[0].ID != byLabel[0].ID) {
		return TargetInspection{}, ErrContainerConflict
	}
	if len(byName) == 0 {
		return TargetInspection{}, ctx.Err()
	}
	if byName[0].State == "" || len(byName[0].State) > 24 {
		return TargetInspection{}, ErrInvalidResponse
	}
	return TargetInspection{Found: true, NativeID: byName[0].ID, State: byName[0].State}, ctx.Err()
}

// ListNamespaceContainers lists every container labeled for the data namespace, whatever its state. It changes
// nothing.
func (r *CandidateRuntime) ListNamespaceContainers(ctx context.Context, namespaceID string) ([]NamespaceContainer, error) {
	if r == nil {
		return nil, ErrUnsupported
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed || !containerUUID.MatchString(namespaceID) || namespaceID == "00000000-0000-0000-0000-000000000000" {
		return nil, ErrInvalidResponse
	}
	client, closeClient := r.client()
	defer closeClient()
	found, err := listMigrationContainersHTTP(ctx, client, map[string][]string{"label": {containerLabelPrefix + "namespace=" + namespaceID}})
	if err != nil {
		return nil, err
	}
	result := make([]NamespaceContainer, 0, len(found))
	for _, listed := range found {
		resource := listed.Labels[containerLabelPrefix+"resource"]
		if listed.State == "" || len(listed.State) > 24 || !containerUUID.MatchString(resource) {
			return nil, ErrInvalidResponse
		}
		result = append(result, NamespaceContainer{NativeID: listed.ID, State: listed.State, ResourceID: resource})
	}
	return result, ctx.Err()
}

// RemoveStrayContainer kills the container if it is running and removes it, never forced and keeping its volumes. It
// is meant for a container no live resource accounts for; a container that does not go away stays reported by the
// next listing, and the caller tries again.
func (r *CandidateRuntime) RemoveStrayContainer(ctx context.Context, nativeID string) error {
	if r == nil {
		return ErrUnsupported
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed || !containerSHA.MatchString(nativeID) {
		return ErrInvalidResponse
	}
	client, closeClient := r.client()
	defer closeClient()
	if err := killContainerHTTP(ctx, client, nativeID); err != nil {
		return err
	}
	for range 50 {
		request, err := http.NewRequestWithContext(ctx, http.MethodDelete, "http://app-runner-docker.invalid/v"+APIVersion+"/containers/"+nativeID+"?force=false&v=false", nil)
		if err != nil {
			return ErrInvalidResponse
		}
		response, err := client.Do(request)
		if err != nil {
			return errors.Join(ErrContainerUncertain, ctx.Err())
		}
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		response.Body.Close()
		switch response.StatusCode {
		case http.StatusNoContent, http.StatusNotFound:
			return ctx.Err()
		case http.StatusConflict:
			// Still stopping after the kill: look again shortly.
			select {
			case <-ctx.Done():
				return errors.Join(ErrContainerUncertain, ctx.Err())
			case <-time.After(100 * time.Millisecond):
			}
		default:
			return ErrContainerUncertain
		}
	}
	return ErrContainerUncertain
}
