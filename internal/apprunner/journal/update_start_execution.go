package journal

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/driver"
)

var ErrUpdateStartExecutionUnavailable = errors.New("managed update start execution is unavailable")
var ErrUpdateStartExecutionUncertain = errors.New("managed update start execution requires reconciliation")

const updateStartRunningFormat = "portal.runner.update-start-running-receipt/1-dev.1"
const updateStartPreflightFormat = "portal.runner.update-start-preflight/1-dev.1"

type UpdateStartPreflightEvidence struct {
	Format             string `json:"format"`
	ImageID            string `json:"image_id"`
	Platform           string `json:"platform"`
	PolicySHA256       string `json:"policy_sha256"`
	MountsSHA256       string `json:"mounts_sha256"`
	RootFilesystemUUID string `json:"root_filesystem_uuid"`
	RootInode          string `json:"root_inode"`
	ProjectID          string `json:"project_id"`
	Device             string `json:"device"`
	NamespaceInode     string `json:"namespace_inode"`
	QuotaBytes         string `json:"quota_bytes"`
	QuotaInodes        string `json:"quota_inodes"`
	SeccompSHA256      string `json:"seccomp_sha256"`
}

type UpdateStartClaimMarker struct {
	Format                  string                        `json:"format"`
	ClaimID                 string                        `json:"claim_id"`
	EffectID                string                        `json:"effect_id"`
	ResourceID              string                        `json:"resource_id"`
	PayloadSHA256           string                        `json:"payload_sha256"`
	OwnershipSHA256         string                        `json:"ownership_sha256"`
	CheckpointReceiptSHA256 string                        `json:"checkpoint_receipt_sha256"`
	ControlRevision         string                        `json:"control_revision"`
	AdmittedFence           string                        `json:"admitted_fence"`
	SandboxProfile          string                        `json:"sandbox_profile"`
	ClaimedMS               string                        `json:"claimed_ms"`
	ExpiresMS               string                        `json:"expires_ms"`
	Preflight               *UpdateStartPreflightEvidence `json:"preflight,omitempty"`
	PreflightBoundMS        string                        `json:"preflight_bound_ms,omitempty"`
}

type UpdateStartRunningReceipt struct {
	Format              string                 `json:"format"`
	Claim               UpdateStartClaimMarker `json:"claim"`
	DeploymentID        string                 `json:"deployment_id"`
	ControllerEpoch     string                 `json:"controller_epoch"`
	InstallationID      string                 `json:"installation_id"`
	OperationID         string                 `json:"operation_id"`
	NamespaceID         string                 `json:"namespace_id"`
	SourceReleaseSHA256 string                 `json:"source_release_sha256"`
	TargetReleaseSHA256 string                 `json:"target_release_sha256"`
	SourceGeneration    string                 `json:"source_generation"`
	TargetGeneration    string                 `json:"target_generation"`
	ControlFence        string                 `json:"control_fence"`
	SourceWriterFence   string                 `json:"source_writer_fence"`
	TargetWriterFence   string                 `json:"target_writer_fence"`
	DataFormat          string                 `json:"data_format"`
	NativeID            string                 `json:"native_id"`
	NativeObservedMS    string                 `json:"native_observed_ms"`
	SettledMS           string                 `json:"settled_ms"`
}

type UpdateStartClaim struct{ state *updateStartClaimState }

type updateStartClaimState struct {
	owner          *Store
	raw, signature []byte
	marker         string
	deadline       time.Time
	used           atomic.Bool
	bound          *UpdateStartPreflightEvidence
}

func (UpdateStartClaim) MarshalJSON() ([]byte, error) {
	return nil, ErrUpdateStartExecutionUnavailable
}
func (c *UpdateStartClaim) UnmarshalJSON([]byte) error {
	c.state = nil
	return ErrUpdateStartExecutionUnavailable
}

func updateStartPreflight(snapshot driver.UpdateStartLaunchSnapshot) UpdateStartPreflightEvidence {
	native := candidatePreflight(snapshot.Launch)
	return UpdateStartPreflightEvidence{
		Format: updateStartPreflightFormat, ImageID: native.ImageID,
		Platform: native.Platform, PolicySHA256: native.PolicySHA256,
		MountsSHA256: native.MountsSHA256, RootFilesystemUUID: native.RootFilesystemUUID,
		RootInode: native.RootInode, ProjectID: native.ProjectID,
		Device: native.Device, NamespaceInode: native.NamespaceInode,
		QuotaBytes: native.QuotaBytes, QuotaInodes: native.QuotaInodes,
		SeccompSHA256: snapshot.SeccompSHA256,
	}
}

func (p UpdateStartPreflightEvidence) native() candidatePreflightEvidence {
	return candidatePreflightEvidence{
		Format: candidatePreflightFormat, ImageID: p.ImageID, Platform: p.Platform,
		PolicySHA256: p.PolicySHA256, MountsSHA256: p.MountsSHA256,
		RootFilesystemUUID: p.RootFilesystemUUID, RootInode: p.RootInode,
		ProjectID: p.ProjectID, Device: p.Device, NamespaceInode: p.NamespaceInode,
		QuotaBytes: p.QuotaBytes, QuotaInodes: p.QuotaInodes,
	}
}

func validUpdateStartPreflight(p UpdateStartPreflightEvidence) bool {
	return p.Format == updateStartPreflightFormat && validCandidatePreflight(p.native()) &&
		dataSHA(p.SeccompSHA256)
}

func validUpdateStartClaimMarker(m UpdateStartClaimMarker) bool {
	if m.Format != "portal.runner.update-start-claim/1-dev.1" ||
		!dataSHA(m.PayloadSHA256) || !dataSHA(m.OwnershipSHA256) ||
		!dataSHA(m.CheckpointReceiptSHA256) || !dataNumber(m.ControlRevision, true) ||
		!dataNumber(m.AdmittedFence, true) || !dataNumber(m.ClaimedMS, false) ||
		!dataNumber(m.ExpiresMS, true) ||
		(m.SandboxProfile != control.CandidateSandboxProfile &&
			m.SandboxProfile != control.WritableCandidateSandboxProfile) {
		return false
	}
	for _, id := range []string{m.ClaimID, m.EffectID, m.ResourceID} {
		if !enrollmentUUID.MatchString(id) || id == "00000000-0000-0000-0000-000000000000" {
			return false
		}
	}
	claimed, _ := strconv.ParseInt(m.ClaimedMS, 10, 64)
	expires, _ := strconv.ParseInt(m.ExpiresMS, 10, 64)
	if expires <= claimed || expires-claimed > 30000 {
		return false
	}
	if m.Preflight == nil {
		return m.PreflightBoundMS == ""
	}
	if !validUpdateStartPreflight(*m.Preflight) || !dataNumber(m.PreflightBoundMS, false) {
		return false
	}
	if m.Preflight.SeccompSHA256 != driver.UpdateStartSeccompSHA256(m.SandboxProfile) {
		return false
	}
	bound, _ := strconv.ParseInt(m.PreflightBoundMS, 10, 64)
	return bound >= claimed && bound < expires
}

func updateStartClaimCore(m UpdateStartClaimMarker) UpdateStartClaimMarker {
	m.Preflight = nil
	m.PreflightBoundMS = ""
	return m
}

func updateStartClaimMatches(f updateStartFacts, m UpdateStartClaimMarker) bool {
	c := f.command
	claimed, _ := strconv.ParseInt(m.ClaimedMS, 10, 64)
	return validUpdateStartClaimMarker(m) &&
		m.EffectID == c.EffectID && m.ResourceID == c.Payload.ResourceID &&
		m.PayloadSHA256 == c.PayloadSHA256 &&
		m.OwnershipSHA256 == updateStartOwnership(c, f.stored.fence) &&
		m.CheckpointReceiptSHA256 == c.Payload.SourceCheckpointReceiptSHA256 &&
		m.ControlRevision == c.ControlRevision &&
		m.AdmittedFence == strconv.FormatInt(f.stored.fence, 10) &&
		claimed >= f.created && claimed <= f.now
}

func updateStartRunningMatches(f updateStartFacts, resource *dataResource, r UpdateStartRunningReceipt) bool {
	if r.Format != updateStartRunningFormat || !dataSHA(r.NativeID) ||
		!dataSHA(r.SourceReleaseSHA256) || !dataSHA(r.TargetReleaseSHA256) ||
		!validUpdateStartClaimMarker(r.Claim) || r.Claim.Preflight == nil ||
		!updateStartClaimMatches(f, r.Claim) {
		return false
	}
	for _, id := range []string{r.DeploymentID, r.ControllerEpoch, r.InstallationID,
		r.OperationID, r.NamespaceID} {
		if !enrollmentUUID.MatchString(id) || id == "00000000-0000-0000-0000-000000000000" {
			return false
		}
	}
	for _, value := range []string{r.SourceGeneration, r.TargetGeneration,
		r.ControlFence, r.SourceWriterFence, r.TargetWriterFence, r.DataFormat} {
		if !dataNumber(value, true) {
			return false
		}
	}
	c := f.command
	if r.DeploymentID != c.DeploymentID || r.ControllerEpoch != c.ControllerEpoch ||
		r.InstallationID != c.InstallationID || r.OperationID != c.OperationID ||
		r.NamespaceID != c.Payload.NamespaceID ||
		r.SourceReleaseSHA256 != c.Payload.SourceReleaseSHA256 ||
		r.TargetReleaseSHA256 != c.Payload.TargetReleaseSHA256 ||
		r.SourceGeneration != c.Payload.SourceGeneration ||
		r.TargetGeneration != c.TargetGeneration ||
		r.ControlFence != c.InstallationFence ||
		r.SourceWriterFence != c.Payload.SourceWriterFence ||
		r.TargetWriterFence != c.Payload.TargetWriterFence ||
		r.DataFormat != c.Payload.DataFormat {
		return false
	}
	observed, observedErr := strconv.ParseInt(r.NativeObservedMS, 10, 64)
	settled, settledErr := strconv.ParseInt(r.SettledMS, 10, 64)
	bound, boundErr := strconv.ParseInt(r.Claim.PreflightBoundMS, 10, 64)
	if observedErr != nil || settledErr != nil || boundErr != nil ||
		observed < bound || settled < observed || settled > f.now {
		return false
	}
	return resource != nil && resource.id == r.Claim.ResourceID &&
		resource.namespace.Valid && resource.namespace.String == f.command.Payload.NamespaceID &&
		resource.kind == "container" && resource.native == r.NativeID &&
		resource.release.Valid && resource.release.String == f.command.Payload.TargetReleaseSHA256 &&
		resource.generation == mustDataInt(f.command.TargetGeneration) &&
		resource.epoch == f.command.ControllerEpoch &&
		resource.fence == f.stored.fence &&
		resource.writer == mustDataInt(f.command.Payload.TargetWriterFence) &&
		resource.desired == "present" && resource.observed == "running" &&
		resource.ownership == r.Claim.OwnershipSHA256 &&
		resource.revision == 2 && resource.observedMS.Valid &&
		resource.observedMS.Int64 == observed && resource.created <= settled &&
		resource.updated == settled
}

func ParseUpdateStartRunningReceipt(raw []byte) (UpdateStartRunningReceipt, error) {
	var receipt UpdateStartRunningReceipt
	if len(raw) == 0 || len(raw) > 16384 || !canonicalDataJSON(string(raw), &receipt) {
		return UpdateStartRunningReceipt{}, fmt.Errorf("%w: running receipt JSON", ErrFenceReconciliation)
	}
	if receipt.Format != updateStartRunningFormat {
		return UpdateStartRunningReceipt{}, fmt.Errorf("%w: running receipt format", ErrFenceReconciliation)
	}
	if !dataSHA(receipt.NativeID) ||
		!dataSHA(receipt.SourceReleaseSHA256) || !dataSHA(receipt.TargetReleaseSHA256) ||
		receipt.Claim.Preflight == nil {
		return UpdateStartRunningReceipt{}, fmt.Errorf("%w: running receipt envelope", ErrFenceReconciliation)
	}
	if !validUpdateStartClaimMarker(receipt.Claim) {
		return UpdateStartRunningReceipt{}, fmt.Errorf("%w: running receipt claim", ErrFenceReconciliation)
	}
	if !dataNumber(receipt.NativeObservedMS, false) || !dataNumber(receipt.SettledMS, false) {
		return UpdateStartRunningReceipt{}, fmt.Errorf("%w: running receipt timestamps", ErrFenceReconciliation)
	}
	return receipt, nil
}

func (s *Store) beginUpdateStartExecution(ctx context.Context, raw, signature []byte, state string) (*sql.Tx, updateStartFacts, error) {
	var zero updateStartFacts
	if s.closing.Load() || len(raw) == 0 || len(raw) > control.MaxCommandBytes ||
		len(signature) != 64 || (state != "prepared" && state != "unknown" && state != "applied") {
		return nil, zero, ErrUpdateStartExecutionUnavailable
	}
	raw, signature = bytes.Clone(raw), bytes.Clone(signature)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, zero, err
	}
	ok := false
	defer func() {
		if !ok {
			tx.Rollback()
		}
	}()
	if _, err := tx.ExecContext(ctx, `UPDATE runner_deployment SET revision=revision WHERE singleton=1`); err != nil {
		return nil, zero, err
	}
	deployment, err := readControllerDeployment(ctx, tx)
	if err != nil {
		return nil, zero, err
	}
	authority := control.Authority{
		DeploymentID: deployment.DeploymentID, ControllerEpoch: deployment.ControllerEpoch,
		ControlRevision: deployment.ControlRevision, PublicKey: deployment.PublicKey,
		Fingerprint: deployment.Fingerprint,
	}
	started := time.Now()
	now := started.UnixMilli()
	verified, err := control.VerifyUpdateStart(raw, signature, authority, now)
	if err != nil {
		return nil, zero, err
	}
	command := verified.Command()
	stored, err := loadAdmittedCommand(ctx, tx, command.EffectID)
	if err != nil {
		return nil, zero, err
	}
	if !sameLogicalCommand(stored, updateStartIndex(command)) || stored.state != state {
		return nil, zero, ErrFenceReconciliation
	}
	fence, _ := strconv.ParseInt(command.InstallationFence, 10, 64)
	floor, err := commandResourceFloor(ctx, tx, updateStartIndex(command))
	if err != nil || fence < floor {
		return nil, zero, ErrFenceReconciliation
	}
	var receipt string
	var ack sql.NullInt64
	var created, updated int64
	if err := tx.QueryRowContext(ctx, `SELECT receipt_json,ack_attempted_ms,created_ms,updated_ms
		FROM runner_commands WHERE effect_id=?`, command.EffectID).
		Scan(&receipt, &ack, &created, &updated); err != nil {
		return nil, zero, err
	}
	if ack.Valid || created < 0 || updated < created || updated > now {
		return nil, zero, ErrFenceReconciliation
	}
	source, err := loadUpdateStartSource(ctx, tx, command, now)
	if err != nil {
		return nil, zero, err
	}
	facts := updateStartFacts{
		command: command, stored: stored, authority: authority, source: source,
		receipt: receipt, created: created, updated: updated,
		raw: raw, signature: signature, now: now,
	}
	switch state {
	case "prepared":
		if receipt != "{}" || updated != created {
			return nil, zero, ErrFenceReconciliation
		}
	case "unknown":
		var marker UpdateStartClaimMarker
		if !canonicalDataJSON(receipt, &marker) {
			return nil, zero, ErrFenceReconciliation
		}
		expectedUpdated := marker.ClaimedMS
		if marker.Preflight != nil {
			expectedUpdated = marker.PreflightBoundMS
		}
		if !updateStartClaimMatches(facts, marker) || updated != mustDataInt(expectedUpdated) {
			return nil, zero, ErrFenceReconciliation
		}
	case "applied":
		receiptValue, err := ParseUpdateStartRunningReceipt([]byte(receipt))
		if err != nil {
			return nil, zero, err
		}
		resource, err := existingCandidateResourcePrefix(ctx, tx, updateStartIndex(command), updateStartPendingPrefix, command.Payload.ResourceID)
		if err != nil || !updateStartRunningMatches(facts, resource, receiptValue) ||
			updated != mustDataInt(receiptValue.SettledMS) {
			return nil, zero, ErrFenceReconciliation
		}
	}
	if err := finalUpdateStartCheck(ctx, facts); err != nil {
		return nil, zero, err
	}
	ok = true
	return tx, facts, nil
}

func updateStartNativeRequest(f updateStartFacts, marker UpdateStartClaimMarker, descriptor []byte) (driver.UpdateStartLaunchRequest, error) {
	var zero driver.UpdateStartLaunchRequest
	c := f.command
	if len(descriptor) > 0 {
		parsed, err := appplatform.ParseDescriptor(descriptor)
		if err != nil || parsed.Manifest.Backend == nil ||
			parsed.SHA256 != c.Payload.TargetReleaseSHA256 ||
			strconv.Itoa(parsed.Manifest.Data.Schema) != c.Payload.DataFormat {
			return zero, ErrFenceReconciliation
		}
		found := false
		for _, platform := range parsed.Manifest.Backend.Platforms {
			if platform == c.Payload.Platform {
				found = true
			}
		}
		if !found {
			return zero, ErrFenceReconciliation
		}
	}
	project, projectErr := strconv.ParseUint(c.Payload.ProjectID, 10, 32)
	device, deviceErr := strconv.ParseUint(c.Payload.Device, 10, 64)
	inode, inodeErr := strconv.ParseUint(c.Payload.NamespaceInode, 10, 64)
	rootInode, rootInodeErr := strconv.ParseUint(c.Payload.RootInode, 10, 64)
	quotaBytes, quotaBytesErr := strconv.ParseInt(c.Payload.QuotaBytes, 10, 64)
	quotaInodes, quotaInodesErr := strconv.ParseInt(c.Payload.QuotaInodes, 10, 64)
	source := f.source.source.source
	data := f.source.source.data
	if projectErr != nil || deviceErr != nil || inodeErr != nil || rootInodeErr != nil ||
		quotaBytesErr != nil || quotaInodesErr != nil ||
		c.Payload.RootFilesystemUUID != data.Claim.RootFilesystemUUID ||
		markerRoot(data.Claim).Inode != rootInode ||
		c.Payload.NamespaceInode != data.NamespaceInode ||
		c.Payload.Device != data.Device || c.Payload.ProjectID != data.ProjectID ||
		c.Payload.QuotaBytes != data.QuotaBytes || c.Payload.QuotaInodes != data.QuotaInodes ||
		source.Claim.Preflight == nil ||
		source.Claim.Preflight.RootFilesystemUUID != c.Payload.RootFilesystemUUID ||
		source.Claim.Preflight.RootInode != c.Payload.RootInode {
		return zero, ErrFenceReconciliation
	}
	request := driver.UpdateStartLaunchRequest{
		CandidateLaunchRequest: driver.CandidateLaunchRequest{
			Identity: driver.CandidateIdentity{
				DeploymentID: c.DeploymentID, ControllerEpoch: c.ControllerEpoch,
				InstallationID: c.InstallationID, OperationID: c.OperationID,
				ResourceID: c.Payload.ResourceID, NamespaceID: c.Payload.NamespaceID,
				ReleaseSHA256: c.Payload.TargetReleaseSHA256,
				Generation:    c.TargetGeneration, ControlFence: c.InstallationFence,
				WriterFence: c.Payload.TargetWriterFence,
			},
			Descriptor: bytes.Clone(descriptor),
			Platform:   c.Payload.Platform,
			Data: driver.CandidateDataEvidence{
				Root: markerRoot(data.Claim), ProjectID: uint32(project),
				Device: device, NamespaceInode: inode,
				QuotaBytes: quotaBytes, QuotaInodes: quotaInodes,
			},
		},
		Binding: driver.UpdateStartBinding{
			EffectID: c.EffectID, ClaimID: marker.ClaimID,
			PayloadSHA256:           c.PayloadSHA256,
			OwnershipSHA256:         marker.OwnershipSHA256,
			CheckpointReceiptSHA256: c.Payload.SourceCheckpointReceiptSHA256,
		},
		SandboxProfile: c.Payload.SandboxProfile,
	}
	if !validUpdateStartClaimMarker(marker) || !updateStartClaimMatches(f, marker) {
		return zero, ErrFenceReconciliation
	}
	return request, nil
}

func updateStartSnapshotMatches(snapshot driver.UpdateStartLaunchSnapshot, f updateStartFacts, marker UpdateStartClaimMarker) bool {
	request, err := updateStartNativeRequest(f, marker, nil)
	if err != nil {
		return false
	}
	return snapshot.Binding == request.Binding && snapshot.Launch.Identity == request.Identity &&
		snapshot.Launch.Data == request.Data && snapshot.Launch.Platform == request.Platform &&
		snapshot.SandboxProfile == request.SandboxProfile &&
		snapshot.SeccompSHA256 == driver.UpdateStartSeccompSHA256(request.SandboxProfile)
}

func updateStartPreflightMatches(preflight UpdateStartPreflightEvidence, f updateStartFacts, marker UpdateStartClaimMarker) bool {
	request, err := updateStartNativeRequest(f, marker, nil)
	if err != nil || !validUpdateStartPreflight(preflight) {
		return false
	}
	expected := candidatePreflight(driver.CandidateLaunchSnapshot{
		Identity: request.Identity, Data: request.Data, Platform: request.Platform,
		ImageID:      preflight.ImageID,
		PolicySHA256: preflight.PolicySHA256,
		MountsSHA256: preflight.MountsSHA256,
	})
	return expected == preflight.native() &&
		preflight.SeccompSHA256 == driver.UpdateStartSeccompSHA256(request.SandboxProfile)
}

func (s *Store) bindUpdateStartPreflight(ctx context.Context, live *updateStartClaimState, launch driver.UpdateStartLaunch) error {
	if live == nil || live.owner != s || !live.used.Load() || live.bound != nil {
		return ErrUpdateStartExecutionUnavailable
	}
	actual, err := launch.Snapshot()
	if err != nil {
		return err
	}
	tx, f, err := s.beginUpdateStartExecution(ctx, live.raw, live.signature, "unknown")
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var marker UpdateStartClaimMarker
	if !canonicalDataJSON(live.marker, &marker) || marker.Preflight != nil ||
		!updateStartSnapshotMatches(actual, f, marker) {
		return ErrFenceReconciliation
	}
	preflight := updateStartPreflight(actual)
	if !updateStartPreflightMatches(preflight, f, marker) {
		return ErrFenceReconciliation
	}
	marker.Preflight = &preflight
	marker.PreflightBoundMS = strconv.FormatInt(f.now, 10)
	if !updateStartClaimMatches(f, marker) {
		return ErrFenceReconciliation
	}
	encoded, _ := json.Marshal(marker)
	changed, err := tx.ExecContext(ctx, `UPDATE runner_commands SET receipt_json=?,updated_ms=?
		WHERE effect_id=? AND state='unknown' AND receipt_json=? AND ack_attempted_ms IS NULL`,
		string(encoded), f.now, marker.EffectID, live.marker)
	if err != nil {
		return err
	}
	if n, err := changed.RowsAffected(); err != nil || n != 1 {
		return ErrFenceReconciliation
	}
	if err := finalUpdateStartCheck(ctx, f); err != nil {
		return err
	}
	if s.closing.Load() {
		return ErrUpdateStartExecutionUnavailable
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	live.marker = string(encoded)
	live.bound = &preflight
	return finalUpdateStartCheck(ctx, f)
}

func (s *Store) checkBoundUpdateStart(ctx context.Context, live *updateStartClaimState, launch driver.UpdateStartLaunch) error {
	if live == nil || live.owner != s || !live.used.Load() || live.bound == nil {
		return ErrUpdateStartExecutionUnavailable
	}
	actual, err := launch.Snapshot()
	if err != nil {
		return err
	}
	tx, f, err := s.beginUpdateStartExecution(ctx, live.raw, live.signature, "unknown")
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var marker UpdateStartClaimMarker
	if !canonicalDataJSON(live.marker, &marker) || marker.Preflight == nil ||
		*marker.Preflight != *live.bound || *marker.Preflight != updateStartPreflight(actual) ||
		!updateStartSnapshotMatches(actual, f, marker) {
		return ErrFenceReconciliation
	}
	if err := finalUpdateStartCheck(ctx, f); err != nil {
		return err
	}
	if s.closing.Load() {
		return ErrUpdateStartExecutionUnavailable
	}
	return tx.Commit()
}

// ClaimUpdateStart commits the one-shot unknown marker before any native call.
// The returned handle cannot be reconstructed from JSON and is consumed once.
func (s *Store) ClaimUpdateStart(ctx context.Context, raw, signature []byte) (UpdateStartClaim, error) {
	var zero UpdateStartClaim
	tx, f, err := s.beginUpdateStartExecution(ctx, raw, signature, "prepared")
	if err != nil {
		return zero, err
	}
	defer tx.Rollback()
	resource, err := existingCandidateResourcePrefix(ctx, tx, updateStartIndex(f.command), updateStartPendingPrefix, f.command.Payload.ResourceID)
	if err != nil {
		return zero, err
	}
	reservation, err := reservedUpdateStart(f, resource)
	if err != nil {
		return zero, err
	}
	id, err := dataClaimID()
	if err != nil {
		return zero, err
	}
	marker := UpdateStartClaimMarker{
		Format: "portal.runner.update-start-claim/1-dev.1", ClaimID: id,
		EffectID: f.command.EffectID, ResourceID: reservation.ResourceID,
		PayloadSHA256:           f.command.PayloadSHA256,
		OwnershipSHA256:         reservation.OwnershipSHA256,
		CheckpointReceiptSHA256: f.command.Payload.SourceCheckpointReceiptSHA256,
		ControlRevision:         f.command.ControlRevision,
		AdmittedFence:           strconv.FormatInt(f.stored.fence, 10),
		SandboxProfile:          control.WritableCandidateSandboxProfile,
		ClaimedMS:               strconv.FormatInt(f.now, 10), ExpiresMS: f.command.ExpiresMS,
	}
	if !updateStartClaimMatches(f, marker) {
		return zero, ErrFenceReconciliation
	}
	encoded, _ := json.Marshal(marker)
	changed, err := tx.ExecContext(ctx, `UPDATE runner_commands SET state='unknown',receipt_json=?,updated_ms=?
		WHERE effect_id=? AND kind='update-start' AND stage='start' AND state='prepared'
		AND receipt_json='{}' AND ack_attempted_ms IS NULL AND admitted_fence=? AND payload_sha256=?`,
		string(encoded), f.now, f.command.EffectID, f.stored.fence, f.command.PayloadSHA256)
	if err != nil {
		return zero, err
	}
	if n, err := changed.RowsAffected(); err != nil || n != 1 {
		return zero, ErrFenceReconciliation
	}
	if err := finalUpdateStartCheck(ctx, f); err != nil {
		return zero, err
	}
	if s.closing.Load() {
		return zero, ErrUpdateStartExecutionUnavailable
	}
	if err := tx.Commit(); err != nil {
		return zero, err
	}
	return UpdateStartClaim{state: &updateStartClaimState{
		owner: s, raw: f.raw, signature: f.signature, marker: string(encoded),
		deadline: time.UnixMilli(mustDataInt(f.command.ExpiresMS)),
	}}, nil
}

type UpdateStartAttempt struct{ Invoked bool }

func (s *Store) RunUpdateStartOnce(ctx context.Context, claim UpdateStartClaim, run func(context.Context) error) (UpdateStartAttempt, error) {
	var zero UpdateStartAttempt
	live := claim.state
	if live == nil || live.owner != s || run == nil || s.closing.Load() {
		return zero, ErrUpdateStartExecutionUnavailable
	}
	if err := s.acquireDataExecution(ctx); err != nil {
		return zero, err
	}
	defer s.releaseDataExecution()
	if s.closing.Load() || live.used.Load() || !time.Now().Before(live.deadline) {
		return zero, ErrUpdateStartExecutionUnavailable
	}
	tx, f, err := s.beginUpdateStartExecution(ctx, live.raw, live.signature, "unknown")
	if err != nil {
		return zero, err
	}
	defer tx.Rollback()
	var marker UpdateStartClaimMarker
	if !canonicalDataJSON(live.marker, &marker) || marker.Preflight != nil ||
		live.bound != nil || !updateStartClaimMatches(f, marker) {
		return zero, ErrFenceReconciliation
	}
	if err := finalUpdateStartCheck(ctx, f); err != nil {
		return zero, err
	}
	if err := tx.Commit(); err != nil {
		return zero, err
	}
	bounded, cancel := context.WithDeadline(ctx, live.deadline)
	defer cancel()
	if s.closing.Load() || !live.used.CompareAndSwap(false, true) {
		return zero, ErrUpdateStartExecutionUnavailable
	}
	attempt := UpdateStartAttempt{Invoked: true}
	runErr := run(bounded)
	post, after, err := s.beginUpdateStartExecution(bounded, live.raw, live.signature, "unknown")
	if err != nil {
		return attempt, errors.Join(ErrUpdateStartExecutionUncertain, runErr, err)
	}
	defer post.Rollback()
	var current UpdateStartClaimMarker
	if !canonicalDataJSON(after.receipt, &current) ||
		updateStartClaimCore(current) != updateStartClaimCore(marker) ||
		(current.Preflight == nil) != (live.bound == nil) ||
		(current.Preflight != nil && *current.Preflight != *live.bound) {
		return attempt, errors.Join(ErrUpdateStartExecutionUncertain, runErr, ErrFenceReconciliation)
	}
	if err := finalUpdateStartCheck(bounded, after); err != nil {
		return attempt, errors.Join(ErrUpdateStartExecutionUncertain, runErr, err)
	}
	if s.closing.Load() {
		return attempt, errors.Join(ErrUpdateStartExecutionUncertain, runErr, ErrUpdateStartExecutionUnavailable)
	}
	if err := post.Commit(); err != nil {
		return attempt, errors.Join(ErrUpdateStartExecutionUncertain, runErr, err)
	}
	if runErr != nil {
		return attempt, errors.Join(ErrUpdateStartExecutionUncertain, runErr)
	}
	return attempt, nil
}

func (s *Store) settleUpdateStartRunning(ctx context.Context, claim UpdateStartClaim, observed driver.UpdateStartRunningObservation) (UpdateStartRunningReceipt, error) {
	var zero UpdateStartRunningReceipt
	live := claim.state
	if live == nil || live.owner != s || !live.used.Load() {
		return zero, ErrUpdateStartExecutionUnavailable
	}
	bounded, cancel := context.WithDeadline(ctx, live.deadline)
	defer cancel()
	ctx = bounded
	if err := s.acquireDataExecution(ctx); err != nil {
		return zero, err
	}
	defer s.releaseDataExecution()
	if s.closing.Load() || live.bound == nil {
		return zero, ErrUpdateStartExecutionUnavailable
	}
	actual, err := observed.Snapshot()
	if err != nil {
		return zero, err
	}
	tx, f, err := s.beginUpdateStartExecution(ctx, live.raw, live.signature, "unknown")
	if err != nil {
		return zero, err
	}
	defer tx.Rollback()
	var marker UpdateStartClaimMarker
	if !canonicalDataJSON(live.marker, &marker) || marker.Preflight == nil ||
		*marker.Preflight != *live.bound || *marker.Preflight != updateStartPreflight(actual.Launch) ||
		!updateStartSnapshotMatches(actual.Launch, f, marker) || actual.ObservedMS > f.now {
		return zero, ErrFenceReconciliation
	}
	receipt := UpdateStartRunningReceipt{
		Format: updateStartRunningFormat, Claim: marker,
		DeploymentID: f.command.DeploymentID, ControllerEpoch: f.command.ControllerEpoch,
		InstallationID: f.command.InstallationID, OperationID: f.command.OperationID,
		NamespaceID:         f.command.Payload.NamespaceID,
		SourceReleaseSHA256: f.command.Payload.SourceReleaseSHA256,
		TargetReleaseSHA256: f.command.Payload.TargetReleaseSHA256,
		SourceGeneration:    f.command.Payload.SourceGeneration,
		TargetGeneration:    f.command.TargetGeneration,
		ControlFence:        f.command.InstallationFence,
		SourceWriterFence:   f.command.Payload.SourceWriterFence,
		TargetWriterFence:   f.command.Payload.TargetWriterFence,
		DataFormat:          f.command.Payload.DataFormat,
		NativeID:            actual.NativeID,
		NativeObservedMS:    strconv.FormatInt(actual.ObservedMS, 10),
		SettledMS:           strconv.FormatInt(f.now, 10),
	}
	if !validUpdateStartClaimMarker(marker) {
		return zero, ErrFenceReconciliation
	}
	encoded, _ := json.Marshal(receipt)
	changed, err := tx.ExecContext(ctx, `UPDATE runner_resources SET native_id=?,observed_state='running',
		revision=2,observed_ms=?,updated_ms=? WHERE id=? AND kind='container' AND native_id=?
		AND revision=1 AND observed_state='unknown' AND observed_ms IS NULL AND ownership_sha256=?`,
		actual.NativeID, actual.ObservedMS, f.now, marker.ResourceID,
		updateStartPendingPrefix+marker.ResourceID, marker.OwnershipSHA256)
	if err != nil {
		return zero, err
	}
	if n, err := changed.RowsAffected(); err != nil || n != 1 {
		return zero, ErrFenceReconciliation
	}
	changed, err = tx.ExecContext(ctx, `UPDATE runner_commands SET state='applied',receipt_json=?,updated_ms=?
		WHERE effect_id=? AND kind='update-start' AND stage='start' AND state='unknown'
		AND receipt_json=? AND ack_attempted_ms IS NULL`, string(encoded), f.now,
		marker.EffectID, live.marker)
	if err != nil {
		return zero, err
	}
	if n, err := changed.RowsAffected(); err != nil || n != 1 {
		return zero, ErrFenceReconciliation
	}
	if err := finalUpdateStartCheck(ctx, f); err != nil {
		return zero, err
	}
	if s.closing.Load() {
		return zero, ErrUpdateStartExecutionUnavailable
	}
	if err := tx.Commit(); err != nil {
		return zero, err
	}
	return receipt, nil
}

func (s *Store) ExecuteUpdateStart(ctx context.Context, claim UpdateStartClaim, native *driver.CandidateRuntime, descriptor []byte) (UpdateStartRunningReceipt, error) {
	var zero UpdateStartRunningReceipt
	if native == nil || len(descriptor) == 0 || len(descriptor) > appplatform.MaxDescriptorBytes {
		return zero, ErrUpdateStartExecutionUnavailable
	}
	descriptor = bytes.Clone(descriptor)
	var observed driver.UpdateStartRunningObservation
	_, err := s.RunUpdateStartOnce(ctx, claim, func(ctx context.Context) error {
		live := claim.state
		tx, f, err := s.beginUpdateStartExecution(ctx, live.raw, live.signature, "unknown")
		if err != nil {
			return err
		}
		var marker UpdateStartClaimMarker
		if !canonicalDataJSON(live.marker, &marker) {
			tx.Rollback()
			return ErrFenceReconciliation
		}
		request, err := updateStartNativeRequest(f, marker, descriptor)
		tx.Rollback()
		if err != nil {
			return err
		}
		if err := native.ProvisionUpdateStartSockets(ctx, request); err != nil {
			return err
		}
		launch, err := native.PrepareUpdateStart(ctx, request)
		if err != nil {
			return err
		}
		defer launch.Close()
		if err := s.bindUpdateStartPreflight(ctx, live, launch); err != nil {
			return err
		}
		if err := s.checkBoundUpdateStart(ctx, live, launch); err != nil {
			return err
		}
		observed, err = native.StartUpdateStart(ctx, launch)
		return err
	})
	if err != nil {
		return zero, err
	}
	receipt, err := s.settleUpdateStartRunning(ctx, claim, observed)
	if err != nil {
		return zero, errors.Join(ErrUpdateStartExecutionUncertain, err)
	}
	return receipt, nil
}

// DispatchUpdateStart requires an already reserved target resource. Unknown
// outcomes are retained for separate observation and are never re-executed.
func (s *Store) DispatchUpdateStart(ctx context.Context, raw, signature, descriptor []byte, native *driver.CandidateRuntime) (UpdateStartRunningReceipt, error) {
	var zero UpdateStartRunningReceipt
	if native == nil || s.closing.Load() || len(raw) == 0 || len(raw) > control.MaxCommandBytes ||
		len(signature) != 64 || len(descriptor) == 0 || len(descriptor) > appplatform.MaxDescriptorBytes {
		return zero, ErrFenceReconciliation
	}
	raw, signature, descriptor = bytes.Clone(raw), bytes.Clone(signature), bytes.Clone(descriptor)
	a, err := s.writableDispatchAuthority(ctx)
	if err != nil {
		return zero, err
	}
	verified, err := control.VerifyUpdateStart(raw, signature, a, time.Now().UnixMilli())
	if err != nil {
		return zero, err
	}
	parsed, err := appplatform.ParseDescriptor(descriptor)
	if err != nil || parsed.Manifest.Backend == nil ||
		parsed.SHA256 != verified.Command().Payload.TargetReleaseSHA256 {
		return zero, ErrFenceReconciliation
	}
	if receipt, err := s.ReadUpdateStartRunningReceipt(ctx, raw, signature); err == nil {
		return receipt, nil
	}
	if _, err := s.ReadUpdateStartReservation(ctx, raw, signature); err != nil {
		return zero, ErrUpdateStartExecutionUnavailable
	}
	claim, err := s.ClaimUpdateStart(ctx, raw, signature)
	if err != nil {
		return zero, err
	}
	return s.ExecuteUpdateStart(ctx, claim, native, descriptor)
}

func (s *Store) ReadUpdateStartRunningReceipt(ctx context.Context, raw, signature []byte) (UpdateStartRunningReceipt, error) {
	var zero UpdateStartRunningReceipt
	tx, f, err := s.beginUpdateStartExecution(ctx, raw, signature, "applied")
	if err != nil {
		return zero, err
	}
	defer tx.Rollback()
	receipt, err := ParseUpdateStartRunningReceipt([]byte(f.receipt))
	if err != nil {
		return zero, err
	}
	resource, err := existingCandidateResourcePrefix(ctx, tx, updateStartIndex(f.command), updateStartPendingPrefix, f.command.Payload.ResourceID)
	if err != nil || !updateStartRunningMatches(f, resource, receipt) {
		return zero, ErrFenceReconciliation
	}
	if err := finalUpdateStartCheck(ctx, f); err != nil {
		return zero, err
	}
	if s.closing.Load() {
		return zero, ErrUpdateStartExecutionUnavailable
	}
	if err := tx.Commit(); err != nil {
		return zero, err
	}
	return receipt, nil
}
