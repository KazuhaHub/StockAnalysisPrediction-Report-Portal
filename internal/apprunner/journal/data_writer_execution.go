package journal

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/driver"
)

type dataWriterNativeEvidence struct {
	Format             string `json:"format"`
	ImageID            string `json:"image_id"`
	Platform           string `json:"platform"`
	Profile            string `json:"profile"`
	SeccompSHA256      string `json:"seccomp_sha256"`
	RootFilesystemUUID string `json:"root_filesystem_uuid"`
	RootInode          string `json:"root_inode"`
	ProjectID          string `json:"project_id"`
	Device             string `json:"device"`
	NamespaceInode     string `json:"namespace_inode"`
	QuotaBytes         string `json:"quota_bytes"`
	QuotaInodes        string `json:"quota_inodes"`
	UsedBytes          string `json:"used_bytes"`
	UsedInodes         string `json:"used_inodes"`
	ObservedMS         string `json:"observed_ms"`
}

// DataWriterReceipt is historical physical permission evidence. It is not an
// active writer lease, a container-start permission or current app readiness.
type DataWriterReceipt struct {
	Format                 string                   `json:"format"`
	Claim                  dataWriterClaimMarker    `json:"claim"`
	ControlRevision        string                   `json:"control_revision"`
	Native                 dataWriterNativeEvidence `json:"native"`
	BindingSHA256          string                   `json:"binding_sha256"`
	SettledMS              string                   `json:"settled_ms"`
	RecoveryEnvelopeSHA256 string                   `json:"recovery_envelope_sha256,omitempty"`
	RecoveryIssuedMS       string                   `json:"recovery_issued_ms,omitempty"`
	RecoveryExpiresMS      string                   `json:"recovery_expires_ms,omitempty"`
}

const dataWriterReceiptFormat = "portal.runner.data-writer-receipt/1-dev.1"
const dataWriterRecoveredReceiptFormat = "portal.runner.reconciled-data-writer-receipt/1-dev.1"

func dataWriterReceiptBinding(r DataWriterReceipt) string {
	if r.Format == dataWriterRecoveredReceiptFormat {
		raw, _ := json.Marshal(struct {
			Purpose  string                   `json:"purpose"`
			Claim    dataWriterClaimMarker    `json:"claim"`
			Revision string                   `json:"control_revision"`
			Native   dataWriterNativeEvidence `json:"native"`
			Envelope string                   `json:"recovery_envelope_sha256"`
			Issued   string                   `json:"recovery_issued_ms"`
			Expires  string                   `json:"recovery_expires_ms"`
		}{"portal.runner.reconciled-data-writer-binding/1-dev.1", r.Claim, r.ControlRevision, r.Native, r.RecoveryEnvelopeSHA256, r.RecoveryIssuedMS, r.RecoveryExpiresMS})
		return candidateHash(raw)
	}
	raw, _ := json.Marshal(struct {
		Purpose  string                   `json:"purpose"`
		Claim    dataWriterClaimMarker    `json:"claim"`
		Revision string                   `json:"control_revision"`
		Native   dataWriterNativeEvidence `json:"native"`
	}{"portal.runner.data-writer-binding/1-dev.1", r.Claim, r.ControlRevision, r.Native})
	return candidateHash(raw)
}

func ParseDataWriterReceipt(raw []byte) (DataWriterReceipt, error) {
	var r DataWriterReceipt
	if len(raw) == 0 || len(raw) > 16384 || !canonicalDataJSON(string(raw), &r) || (r.Format != dataWriterReceiptFormat && r.Format != dataWriterRecoveredReceiptFormat) || !validDataWriterReceipt(r) {
		return DataWriterReceipt{}, ErrFenceReconciliation
	}
	return r, nil
}
func validDataWriterReceipt(r DataWriterReceipt) bool {
	m, n := r.Claim, r.Native
	if m.Format != "portal.runner.data-writer-claim/1-dev.1" || !enrollmentUUID.MatchString(m.ClaimID) || m.ClaimID == "00000000-0000-0000-0000-000000000000" || !enrollmentUUID.MatchString(m.EffectID) || m.EffectID == "00000000-0000-0000-0000-000000000000" || !enrollmentUUID.MatchString(m.ResourceID) || m.ResourceID == "00000000-0000-0000-0000-000000000000" || !dataSHA(m.PayloadSHA256) || !dataSHA(m.DataReceiptSHA256) || !dataNumber(m.AdmittedFence, true) || !dataNumber(m.ClaimedMS, false) || !dataNumber(m.ExpiresMS, true) || !dataNumber(r.ControlRevision, true) || !dataNumber(r.SettledMS, false) || n.Format != "portal.runner.data-writer-native/1-dev.1" || n.Profile != control.DataWriterSandboxProfile || !dataSHA(n.SeccompSHA256) || len(n.ImageID) != 71 || n.ImageID[:7] != "sha256:" || !dataSHA(n.ImageID[7:]) || (n.Platform != "linux/amd64" && n.Platform != "linux/arm64") || !enrollmentUUID.MatchString(n.RootFilesystemUUID) || n.RootFilesystemUUID == "00000000-0000-0000-0000-000000000000" {
		return false
	}
	for _, value := range []string{n.RootInode, n.NamespaceInode} {
		if !nativeNumber(value, true) {
			return false
		}
	}
	if !nativeNumber(n.ProjectID, true) || !nativeNumber(n.Device, false) || !dataNumber(n.QuotaBytes, true) || !dataNumber(n.QuotaInodes, true) || !dataNumber(n.UsedBytes, false) || !dataNumber(n.UsedInodes, false) || !dataNumber(n.ObservedMS, false) {
		return false
	}
	project, err := strconv.ParseUint(n.ProjectID, 10, 32)
	quota, _ := strconv.ParseInt(n.QuotaBytes, 10, 64)
	used, _ := strconv.ParseInt(n.UsedBytes, 10, 64)
	inodes, _ := strconv.ParseInt(n.QuotaInodes, 10, 64)
	usedInodes, _ := strconv.ParseInt(n.UsedInodes, 10, 64)
	claimed, _ := strconv.ParseInt(m.ClaimedMS, 10, 64)
	expires, _ := strconv.ParseInt(m.ExpiresMS, 10, 64)
	observed, _ := strconv.ParseInt(n.ObservedMS, 10, 64)
	settled, _ := strconv.ParseInt(r.SettledMS, 10, 64)
	if err != nil || project > uint64(maxDataProjectID) || quota%512 != 0 || used > quota || usedInodes > inodes || expires <= claimed || expires-claimed > 30000 || observed < claimed || settled < observed || r.BindingSHA256 != dataWriterReceiptBinding(r) {
		return false
	}
	switch r.Format {
	case dataWriterReceiptFormat:
		return observed < expires && settled < expires && r.RecoveryEnvelopeSHA256 == "" && r.RecoveryIssuedMS == "" && r.RecoveryExpiresMS == ""
	case dataWriterRecoveredReceiptFormat:
		if !dataSHA(r.RecoveryEnvelopeSHA256) || !dataNumber(r.RecoveryIssuedMS, false) || !dataNumber(r.RecoveryExpiresMS, true) {
			return false
		}
		issued, _ := strconv.ParseInt(r.RecoveryIssuedMS, 10, 64)
		recoveryExpires, _ := strconv.ParseInt(r.RecoveryExpiresMS, 10, 64)
		return issued > expires && recoveryExpires > issued && recoveryExpires-issued <= 30000 && observed >= issued && settled < recoveryExpires
	default:
		return false
	}
}

func dataWriterNativeRequest(c control.DataWriterCommand, source DataPreparationReceipt, marker dataWriterClaimMarker, descriptor []byte) (driver.DataWriterRequest, error) {
	var zero driver.DataWriterRequest
	parsed, err := appplatform.ParseDescriptor(descriptor)
	if err != nil || c.SandboxProfile != driver.DataWriterSandboxProfile || parsed.Manifest.Backend == nil || parsed.SHA256 != source.ReleaseSHA256 || strconv.Itoa(parsed.Manifest.Data.Schema) != source.DataFormat {
		return zero, ErrFenceReconciliation
	}
	var ref control.Command
	if json.Unmarshal(c.DataRaw, &ref) != nil {
		return zero, ErrFenceReconciliation
	}
	declared := false
	for _, platform := range parsed.Manifest.Backend.Platforms {
		if platform == c.Platform {
			declared = true
		}
	}
	if !declared {
		return zero, ErrFenceReconciliation
	}
	project, _ := strconv.ParseUint(source.ProjectID, 10, 32)
	device, _ := strconv.ParseUint(source.Device, 10, 64)
	inode, _ := strconv.ParseUint(source.NamespaceInode, 10, 64)
	quota, _ := strconv.ParseInt(source.QuotaBytes, 10, 64)
	inodes, _ := strconv.ParseInt(source.QuotaInodes, 10, 64)
	return driver.DataWriterRequest{
		Binding:    driver.DataWriterBinding{EffectID: marker.EffectID, ClaimID: marker.ClaimID, PayloadSHA256: marker.PayloadSHA256, DataReceiptSHA256: marker.DataReceiptSHA256},
		Identity:   driver.CandidateIdentity{DeploymentID: ref.DeploymentID, ControllerEpoch: ref.ControllerEpoch, InstallationID: ref.InstallationID, OperationID: ref.OperationID, ResourceID: c.ResourceID, NamespaceID: ref.Payload.DataNamespaceID, ReleaseSHA256: ref.Payload.ReleaseSHA256, Generation: ref.TargetGeneration, ControlFence: ref.InstallationFence, WriterFence: ref.Payload.WriterFence},
		Descriptor: bytes.Clone(descriptor), Platform: c.Platform, Data: driver.CandidateDataEvidence{Root: markerRoot(source.Claim), ProjectID: uint32(project), Device: device, NamespaceInode: inode, QuotaBytes: quota, QuotaInodes: inodes},
	}, nil
}

func dataWriterEvidence(s driver.DataWriterSnapshot) dataWriterNativeEvidence {
	d := s.Data
	return dataWriterNativeEvidence{"portal.runner.data-writer-native/1-dev.1", s.ImageID, s.Platform, s.Profile, s.SeccompSHA256, d.Root.FilesystemUUID, strconv.FormatUint(d.Root.Inode, 10), strconv.FormatUint(uint64(d.ProjectID), 10), strconv.FormatUint(d.Device, 10), strconv.FormatUint(d.NamespaceInode, 10), strconv.FormatInt(d.QuotaBytes, 10), strconv.FormatInt(d.QuotaInodes, 10), strconv.FormatInt(s.UsedBytes, 10), strconv.FormatInt(s.UsedInodes, 10), strconv.FormatInt(s.ObservedMS, 10)}
}

// ExecuteDataWriter connects only the process-local claim to the concrete
// driver. No callback result, decoded snapshot or browser boolean can settle it.
func (s *Store) ExecuteDataWriter(ctx context.Context, claim DataWriterClaim, descriptor []byte, native *driver.CandidateRuntime) (DataWriterReceipt, error) {
	var zero DataWriterReceipt
	if native == nil || len(descriptor) == 0 || len(descriptor) > appplatform.MaxDescriptorBytes {
		return zero, ErrDataWriterClaimUnavailable
	}
	descriptor = bytes.Clone(descriptor)
	var observation driver.DataWriterObservation
	attempt, err := s.RunDataWriterOnce(ctx, claim, func(ctx context.Context, p DataWriterPlan) error {
		var marker dataWriterClaimMarker
		if !canonicalDataJSON(claim.state.marker, &marker) {
			return ErrFenceReconciliation
		}
		request, err := dataWriterNativeRequest(p.Command, p.Source, marker, descriptor)
		if err != nil {
			return err
		}
		observation, err = native.EnableDataWriter(ctx, request)
		return err
	})
	if err != nil {
		if attempt.Invoked {
			return zero, errors.Join(driver.ErrDataWriterUncertain, err)
		}
		return zero, err
	}
	receipt, err := s.settleDataWriter(ctx, claim, descriptor, observation)
	if err != nil {
		return zero, errors.Join(driver.ErrDataWriterUncertain, err)
	}
	return receipt, nil
}

func (s *Store) settleDataWriter(ctx context.Context, claim DataWriterClaim, descriptor []byte, observation driver.DataWriterObservation) (DataWriterReceipt, error) {
	var zero DataWriterReceipt
	live := claim.state
	if live == nil || live.owner != s || !live.used.Load() || s.closing.Load() {
		return zero, ErrDataWriterClaimUnavailable
	}
	var deadlineMarker dataWriterClaimMarker
	if !canonicalDataJSON(live.marker, &deadlineMarker) {
		return zero, ErrFenceReconciliation
	}
	expires, err := strconv.ParseInt(deadlineMarker.ExpiresMS, 10, 64)
	if err != nil {
		return zero, ErrFenceReconciliation
	}
	bounded, cancel := context.WithDeadline(ctx, time.UnixMilli(expires))
	defer cancel()
	ctx = bounded
	// Run's physical callback has finished. Acquire a new owner gate for the
	// atomic settlement so Close cannot release the journal lock mid-commit.
	if err := s.acquireDataExecution(ctx); err != nil {
		return zero, err
	}
	defer s.releaseDataExecution()
	if s.closing.Load() {
		return zero, ErrDataWriterClaimUnavailable
	}
	tx, f, err := s.beginDataWriter(ctx, live.raw, live.signature, "unknown", &live.marker)
	if err != nil {
		return zero, err
	}
	defer tx.Rollback()
	var marker dataWriterClaimMarker
	if !canonicalDataJSON(live.marker, &marker) || !dataWriterMarkerMatches(marker, f) {
		return zero, ErrFenceReconciliation
	}
	request, err := dataWriterNativeRequest(f.verified.Command(), f.source, marker, descriptor)
	if err != nil {
		return zero, err
	}
	actual, err := observation.Snapshot()
	if err != nil || actual.Binding != request.Binding || actual.Identity != request.Identity || actual.Data != request.Data || actual.Platform != request.Platform || actual.Profile != control.DataWriterSandboxProfile || actual.ObservedMS > f.now {
		return zero, ErrFenceReconciliation
	}
	r := DataWriterReceipt{Format: "portal.runner.data-writer-receipt/1-dev.1", Claim: marker, ControlRevision: f.verified.Data().ControlRevision, Native: dataWriterEvidence(actual), SettledMS: strconv.FormatInt(f.now, 10)}
	r.BindingSHA256 = dataWriterReceiptBinding(r)
	if !validDataWriterReceipt(r) {
		return zero, ErrFenceReconciliation
	}
	return s.commitDataWriterReceipt(ctx, tx, f, live.marker, r)
}

// Caller holds the Store execution gate and a freshly verified unknown transaction.
func (s *Store) commitDataWriterReceipt(ctx context.Context, tx *sql.Tx, f dataWriterFacts, marker string, r DataWriterReceipt) (DataWriterReceipt, error) {
	var zero DataWriterReceipt
	if !validDataWriterReceipt(r) {
		return zero, ErrFenceReconciliation
	}
	observed, _ := strconv.ParseInt(r.Native.ObservedMS, 10, 64)
	ref := f.verified.Data()
	changed, err := tx.ExecContext(ctx, `UPDATE runner_resources SET revision=3,observed_ms=?,updated_ms=? WHERE id=? AND kind='volume' AND revision=2 AND desired_state='present' AND observed_state='present' AND control_fence=? AND writer_fence=?`, observed, f.now, ref.Payload.DataNamespaceID, f.fence, ref.Payload.WriterFence)
	if err != nil {
		return zero, err
	}
	if n, err := changed.RowsAffected(); err != nil || n != 1 {
		return zero, ErrFenceReconciliation
	}
	encoded, _ := json.Marshal(r)
	changed, err = tx.ExecContext(ctx, `UPDATE runner_commands SET state='applied',receipt_json=?,updated_ms=? WHERE effect_id=? AND state='unknown' AND receipt_json=? AND ack_attempted_ms IS NULL`, string(encoded), f.now, r.Claim.EffectID, marker)
	if err != nil {
		return zero, err
	}
	if n, err := changed.RowsAffected(); err != nil || n != 1 {
		return zero, ErrFenceReconciliation
	}
	if err := finalDataWriterCheck(ctx, f); err != nil {
		return zero, err
	}
	if s.closing.Load() {
		return zero, ErrDataWriterClaimUnavailable
	}
	if err := tx.Commit(); err != nil {
		return zero, err
	}
	if err := finalDataWriterCheck(ctx, f); err != nil {
		return zero, err
	}
	return r, nil
}

// ReadDataWriterReceipt reads historical initial pre-start evidence only. It
// neither repairs permissions nor proves current native exclusion/readiness.
func (s *Store) ReadDataWriterReceipt(ctx context.Context, raw, signature []byte) (DataWriterReceipt, error) {
	var zero DataWriterReceipt
	tx, f, err := s.beginDataWriter(ctx, raw, signature, "applied", nil)
	if err != nil {
		return zero, err
	}
	defer tx.Rollback()
	r, err := ParseDataWriterReceipt([]byte(f.receipt))
	if err != nil || !dataWriterReceiptMatches(r, f) {
		return zero, ErrFenceReconciliation
	}
	if err := finalDataWriterCheck(ctx, f); err != nil {
		return zero, err
	}
	if err := tx.Commit(); err != nil {
		return zero, err
	}
	if err := finalDataWriterCheck(ctx, f); err != nil {
		return zero, err
	}
	if s.closing.Load() {
		return zero, ErrDataWriterClaimUnavailable
	}
	return r, nil
}

func dataWriterReceiptMatches(r DataWriterReceipt, f dataWriterFacts) bool {
	c, ref := f.verified.Command(), f.verified.Data()
	n, source := r.Native, f.source
	evidenceRevision, _ := strconv.ParseInt(r.ControlRevision, 10, 64)
	sourceRevision, _ := strconv.ParseInt(source.ControlRevision, 10, 64)
	currentRevision, _ := strconv.ParseInt(ref.ControlRevision, 10, 64)
	settled, _ := strconv.ParseInt(r.SettledMS, 10, 64)
	observed, _ := strconv.ParseInt(n.ObservedMS, 10, 64)
	claimTime, _ := strconv.ParseInt(r.Claim.ClaimedMS, 10, 64)
	sourceTime, _ := strconv.ParseInt(source.ObservedMS, 10, 64)
	root := markerRoot(source.Claim)
	return validDataWriterReceipt(r) && r.Claim.EffectID == c.EffectID && r.Claim.PayloadSHA256 == c.PayloadSHA256 && r.Claim.DataReceiptSHA256 == c.DataReceiptSHA256 && r.Claim.ResourceID == c.ResourceID && r.Claim.AdmittedFence == ref.InstallationFence && evidenceRevision >= sourceRevision && evidenceRevision <= currentRevision && n.Platform == c.Platform && n.SeccompSHA256 == driver.DataWriterPolicySHA256() && n.RootFilesystemUUID == root.FilesystemUUID && n.RootInode == strconv.FormatUint(root.Inode, 10) && n.ProjectID == source.ProjectID && n.Device == source.Device && n.NamespaceInode == source.NamespaceInode && n.QuotaBytes == source.QuotaBytes && n.QuotaInodes == source.QuotaInodes && claimTime >= sourceTime && observed <= f.now && settled <= f.now
}
