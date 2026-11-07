package driver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"reflect"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

// MigrationBinding is reconstructed from the verified, durably claimed migrate-data effect. It is
// metadata, not execution authority.
type MigrationBinding struct {
	EffectID, ClaimID, PayloadSHA256, OwnershipSHA256, CheckpointReceiptSHA256 string
}

// MigrationDefaultWallClock is the limit when the descriptor step declares none.
const MigrationDefaultWallClock = 10 * time.Minute

// MigrationGrace is added to the wall clock for the kill, removal and absence proofs.
const MigrationGrace = 2 * time.Minute

// MigrationWallClock is the hard limit of the job for a declared step.
func MigrationWallClock(step appplatform.DataMigration) time.Duration {
	if step.WallClockSeconds == nil {
		return MigrationDefaultWallClock
	}
	return time.Duration(*step.WallClockSeconds) * time.Second
}

func validMigrationBinding(b MigrationBinding) bool {
	for _, id := range []string{b.EffectID, b.ClaimID} {
		if !containerUUID.MatchString(id) || id == "00000000-0000-0000-0000-000000000000" {
			return false
		}
	}
	for _, sha := range []string{b.PayloadSHA256, b.OwnershipSHA256, b.CheckpointReceiptSHA256} {
		if !containerSHA.MatchString(sha) {
			return false
		}
	}
	return true
}

func migrationLabels(owner containerOwner, b MigrationBinding) map[string]string {
	labels := ownerContainerLabels(owner)
	labels[containerLabelPrefix+"format"] = "migration/1-dev.1"
	labels[containerLabelPrefix+"sandbox-profile"] = WritableCandidateSandboxProfile
	labels[containerLabelPrefix+"migration-effect"] = b.EffectID
	labels[containerLabelPrefix+"migration-claim"] = b.ClaimID
	labels[containerLabelPrefix+"migration-payload"] = b.PayloadSHA256
	labels[containerLabelPrefix+"migration-ownership"] = b.OwnershipSHA256
	labels[containerLabelPrefix+"checkpoint-receipt"] = b.CheckpointReceiptSHA256
	return labels
}

// validMigrationPlan is the closed shape of a migration job: one read-write data mount and nothing
// else (no socket, broker or environment), no network, a pinned entrypoint, no restart.
func validMigrationPlan(plan containerPlan, request containerCreate) bool {
	h := request.HostConfig
	return validMigrationBinding(*plan.migration) &&
		positiveContainerCounter(plan.owner.Generation) && plan.owner.Generation != "1" &&
		positiveContainerCounter(plan.owner.WriterFence) && plan.owner.WriterFence != "1" &&
		plan.profile == WritableCandidateSandboxProfile &&
		plan.migration.EffectID != plan.owner.ResourceID && plan.migration.EffectID != plan.owner.NamespaceID &&
		len(h.Mounts) == 1 && h.Mounts[0].Type == "bind" && h.Mounts[0].Target == "/data" && !h.Mounts[0].ReadOnly &&
		h.Mounts[0].BindOptions.Propagation == "rprivate" &&
		len(request.Env) == 0 && h.NetworkMode == "none" && request.NetworkDisabled && h.ReadonlyRootfs &&
		!h.Privileged && !h.AutoRemove && h.RestartPolicy.Name == "no" && len(h.Tmpfs) == 1 &&
		len(request.Entrypoint) == 1 && len(h.SecurityOpt) == 2 && h.SecurityOpt[1] == candidateSeccompOption &&
		request.Labels[containerLabelPrefix+"sandbox-profile"] == WritableCandidateSandboxProfile
}

// planMigrationContainer derives the job from the candidate builder, so every network, resource,
// syscall and read-only-root constraint of the backend is unchanged, then removes the sockets and
// environment, opens the data mount for writing and pins the declared entrypoint. The step must be
// one the signed descriptor declares.
func planMigrationContainer(raw []byte, image BackendImageInspection, owner containerOwner, roots containerRoots, binding MigrationBinding, step appplatform.DataMigration) (containerPlan, error) {
	parsed, err := appplatform.ParseDescriptor(raw)
	if err != nil || !validMigrationBinding(binding) || !positiveContainerCounter(owner.Generation) || owner.Generation == "1" ||
		!positiveContainerCounter(owner.WriterFence) || owner.WriterFence == "1" ||
		binding.EffectID == owner.ResourceID || binding.EffectID == owner.NamespaceID || candidateSeccompError != nil {
		return containerPlan{}, ErrInvalidResponse
	}
	declared := false
	for _, candidate := range parsed.Manifest.Data.Migrations {
		if reflect.DeepEqual(candidate, step) {
			declared = true
		}
	}
	if !declared {
		return containerPlan{}, ErrInvalidResponse
	}
	plan, err := planCandidateContainer(raw, image, owner, roots)
	if err != nil {
		return containerPlan{}, err
	}
	var request containerCreate
	if json.Unmarshal(plan.body, &request) != nil || len(request.HostConfig.Mounts) != 3 || request.HostConfig.Mounts[0].Target != "/data" {
		return containerPlan{}, ErrInvalidResponse
	}
	request.HostConfig.Mounts = request.HostConfig.Mounts[:1]
	request.HostConfig.Mounts[0].ReadOnly = false
	request.Env = nil
	request.Entrypoint = []string{step.Entrypoint[0]}
	request.Cmd = append([]string(nil), step.Entrypoint[1:]...)
	request.Labels = migrationLabels(owner, binding)
	policy, _ := json.Marshal(request)
	request.Labels[containerLabelPrefix+"policy-sha256"] = containerHash(policy)
	plan.body, err = json.Marshal(request)
	if err != nil {
		return containerPlan{}, ErrInvalidResponse
	}
	plan.bodySHA = containerHash(plan.body)
	copied := binding
	plan.migration = &copied
	plan.profile = WritableCandidateSandboxProfile
	if !validContainerPlan(plan) {
		return containerPlan{}, ErrInvalidResponse
	}
	return plan, nil
}

// migrationJobResult is a native observation of a finished job, before removal.
type migrationJobResult struct {
	NativeID string
	ExitCode int
	Killed   bool
}

// runMigrationJobHTTP creates, starts and waits for the job, killing it when the wall clock passes. It
// never retries a create or start, and only returns once the job is observed exited, or on an error
// that leaves its fate uncertain. The caller must hold the claim and provide a context that outlives
// its connection.
func runMigrationJobHTTP(ctx context.Context, client *http.Client, plan containerPlan, wall, poll time.Duration) (migrationJobResult, error) {
	var zero migrationJobResult
	if plan.migration == nil || !validContainerPlan(plan) || wall <= 0 || poll <= 0 {
		return zero, ErrInvalidResponse
	}
	id, err := createCandidateContainerHTTP(ctx, client, plan)
	if err != nil {
		return zero, err
	}
	if _, err := startCandidateContainerHTTP(ctx, client, id, plan); err != nil {
		return migrationJobResult{NativeID: id}, err
	}
	deadline := time.Now().Add(wall)
	result := migrationJobResult{NativeID: id}
	for {
		state, err := inspectContainerStateHTTP(ctx, client, id, plan)
		if err != nil {
			return result, errors.Join(ErrContainerUncertain, err)
		}
		if state.Status == "exited" {
			result.ExitCode = state.ExitCode
			return result, nil
		}
		if !result.Killed && !time.Now().Before(deadline) {
			if err := killContainerHTTP(ctx, client, id); err != nil {
				return result, err
			}
			result.Killed = true
			continue
		}
		select {
		case <-ctx.Done():
			return result, errors.Join(ErrContainerUncertain, ctx.Err())
		case <-time.After(poll):
		}
	}
}

func killContainerHTTP(ctx context.Context, client *http.Client, nativeID string) error {
	if !containerSHA.MatchString(nativeID) {
		return ErrInvalidResponse
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://app-runner-docker.invalid/v"+APIVersion+"/containers/"+nativeID+"/kill?signal=SIGKILL", nil)
	if err != nil {
		return ErrInvalidResponse
	}
	response, err := client.Do(request)
	if err != nil {
		return errors.Join(ErrContainerUncertain, ctx.Err())
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
	// 409 means the job stopped between the inspection and the kill; the next inspection decides.
	if response.StatusCode != http.StatusNoContent && response.StatusCode != http.StatusConflict {
		return fmt.Errorf("container kill status %d: %w", response.StatusCode, ErrContainerUncertain)
	}
	return nil
}

type migrationListing struct {
	ID     string `json:"Id"`
	Names  []string
	State  string            `json:"State"`
	Labels map[string]string `json:"Labels"`
}

func listMigrationContainersHTTP(ctx context.Context, client *http.Client, filter map[string][]string) ([]migrationListing, error) {
	encoded, err := json.Marshal(filter)
	if err != nil {
		return nil, ErrInvalidResponse
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://app-runner-docker.invalid/v"+APIVersion+"/containers/json?"+url.Values{"all": []string{"1"}, "filters": []string{string(encoded)}}.Encode(), nil)
	if err != nil {
		return nil, ErrInvalidResponse
	}
	request.Header.Set("Accept", "application/json")
	response, err := client.Do(request)
	if err != nil {
		return nil, errors.Join(ErrContainerUncertain, ctx.Err())
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("container listing status %d: %w", response.StatusCode, ErrContainerUncertain)
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil || len(raw) > maxResponseBytes {
		return nil, ErrContainerUncertain
	}
	var entries []json.RawMessage
	if json.Unmarshal(raw, &entries) != nil || entries == nil {
		return nil, ErrInvalidResponse
	}
	result := make([]migrationListing, 0, len(entries))
	for _, entry := range entries {
		var listed migrationListing
		if !validProjection(entry, &listed) || json.Unmarshal(entry, &listed) != nil || !containerSHA.MatchString(listed.ID) {
			return nil, ErrInvalidResponse
		}
		result = append(result, listed)
	}
	return result, nil
}

// NamespaceContainer is a container labeled for a data namespace.
type NamespaceContainer struct {
	NativeID   string
	State      string
	ResourceID string
}

// TargetInspection is what exists for a resource's container, by name and by label.
type TargetInspection struct {
	Found    bool
	NativeID string
	State    string
}

// proveMigrationAbsentHTTP finds no container, running or stopped, with the job's name and none with
// its resource label. Both must hold: a renamed container still carries the label.
func proveMigrationAbsentHTTP(ctx context.Context, client *http.Client, plan containerPlan) error {
	if plan.migration == nil || !validContainerPlan(plan) {
		return ErrInvalidResponse
	}
	for _, filter := range []map[string][]string{
		{"name": {"^/" + plan.name + "$"}},
		{"label": {containerLabelPrefix + "resource=" + plan.owner.ResourceID}},
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

// removeAndProveMigrationAbsentHTTP removes an exited job (non-forced, volumes kept) and proves it gone.
func removeAndProveMigrationAbsentHTTP(ctx context.Context, client *http.Client, plan containerPlan, nativeID string) error {
	if err := removeCandidateContainerHTTP(ctx, client, nativeID, plan); err != nil {
		return err
	}
	return proveMigrationAbsentHTTP(ctx, client, plan)
}

// MigrationJobRequest is trusted infrastructure input from a verified, durably claimed journal
// attempt. Identity.Generation and Identity.WriterFence are the migration's own (writer fence S+1).
type MigrationJobRequest struct {
	Binding    MigrationBinding
	Identity   CandidateIdentity
	Descriptor []byte
	Platform   string
	Data       CandidateDataEvidence
	Step       appplatform.DataMigration
}

// MigrationJobSnapshot is the native observation of a finished and removed job.
type MigrationJobSnapshot struct {
	Binding    MigrationBinding
	Identity   CandidateIdentity
	NativeID   string
	ImageID    string
	Platform   string
	ExitCode   int
	Killed     bool
	ObservedMS int64
}

// MigrationJobObservation cannot be constructed or decoded outside the concrete native run.
type MigrationJobObservation struct {
	snapshot MigrationJobSnapshot
	valid    bool
}

func (MigrationJobObservation) MarshalJSON() ([]byte, error) { return nil, ErrContainerUncertain }
func (o *MigrationJobObservation) UnmarshalJSON([]byte) error {
	*o = MigrationJobObservation{}
	return ErrContainerUncertain
}
func (o MigrationJobObservation) Snapshot() (MigrationJobSnapshot, error) {
	s := o.snapshot
	if !o.valid || !validMigrationBinding(s.Binding) || !validContainerOwner(candidateOwner(s.Identity)) ||
		!containerSHA.MatchString(s.NativeID) || !nativeImageID.MatchString(s.ImageID) ||
		(s.Platform != "linux/amd64" && s.Platform != "linux/arm64") || s.ExitCode < 0 || s.ObservedMS < 0 {
		return MigrationJobSnapshot{}, ErrContainerUncertain
	}
	return s, nil
}

func migrationRequestValid(r MigrationJobRequest) bool {
	return validMigrationBinding(r.Binding) && validContainerOwner(candidateOwner(r.Identity)) &&
		candidateRequestValid(CandidateLaunchRequest{Identity: r.Identity, Descriptor: r.Descriptor, Platform: r.Platform, Data: r.Data}) &&
		r.Identity.Generation != "1" && r.Identity.WriterFence != "1"
}

// imageID returns the native image the plan was built for.
func (p containerPlan) imageID() string {
	var request containerCreate
	if json.Unmarshal(p.body, &request) != nil {
		return ""
	}
	return request.Image
}

// discardMigrationJobHTTP finds the job by name and label, stops it with a kill when it is still
// running, removes it and proves its absence. It reads no data and trusts no stored native identifier.
func discardMigrationJobHTTP(ctx context.Context, client *http.Client, plan containerPlan) error {
	if plan.migration == nil || !validContainerPlan(plan) {
		return ErrInvalidResponse
	}
	found, err := listMigrationContainersHTTP(ctx, client, map[string][]string{"name": {"^/" + plan.name + "$"}})
	if err != nil {
		return err
	}
	for _, listed := range found {
		state, err := inspectContainerStateHTTP(ctx, client, listed.ID, plan)
		if err != nil {
			return err
		}
		if state.Status == "running" {
			if err := killContainerHTTP(ctx, client, listed.ID); err != nil {
				return err
			}
			for {
				state, err = inspectContainerStateHTTP(ctx, client, listed.ID, plan)
				if err != nil {
					return err
				}
				if state.Status == "exited" {
					break
				}
				select {
				case <-ctx.Done():
					return errors.Join(ErrContainerUncertain, ctx.Err())
				case <-time.After(100 * time.Millisecond):
				}
			}
		}
		if err := removeCandidateContainerHTTP(ctx, client, listed.ID, plan); err != nil {
			return err
		}
	}
	return proveMigrationAbsentHTTP(ctx, client, plan)
}
