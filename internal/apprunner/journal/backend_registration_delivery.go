package journal

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"strconv"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/driver"
)

const registrationClaimFormat = "portal.runner.backend-registration-claim/1-dev.1"
const registrationReceiptFormat = "portal.runner.backend-registration-delivery/1-dev.1"

type backendRegistrationFacts struct {
	verified control.VerifiedBackendRegistrationCommand
	source   candidateFacts
	running  CandidateRunningReceipt
	receipt  string
}
type backendRegistrationClaim struct {
	Format         string `json:"format"`
	ClaimID        string `json:"claim_id"`
	EffectID       string `json:"effect_id"`
	PayloadSHA256  string `json:"payload_sha256"`
	SourceSHA256   string `json:"source_sha256"`
	NativeID       string `json:"native_id"`
	DocumentSHA256 string `json:"document_sha256"`
	ClaimedMS      string `json:"claimed_ms"`
}

// BackendRegistrationReceipt is historical protected-file delivery evidence,
// not a completed application handshake, viewer grant or activation receipt.
type BackendRegistrationReceipt struct {
	Format        string                   `json:"format"`
	Claim         backendRegistrationClaim `json:"claim"`
	BindingSHA256 string                   `json:"binding_sha256"`
	Device        string                   `json:"device"`
	Inode         string                   `json:"inode"`
	ObservedMS    string                   `json:"observed_ms"`
	SettledMS     string                   `json:"settled_ms"`
}

func registrationIndex(v control.VerifiedBackendRegistrationCommand) control.Command {
	c, ref := v.Command(), v.Candidate()
	index := candidateIndex(ref)
	index.Format, index.Stage, index.Kind, index.EffectID, index.EffectNumber, index.PayloadSHA256 = control.BackendRegistrationFormat, "activate", "activate", c.EffectID, "1", c.PayloadSHA256
	return index
}
func (s *Store) beginBackendRegistration(ctx context.Context, raw, signature []byte, state string) (*sql.Tx, backendRegistrationFacts, error) {
	var zero backendRegistrationFacts
	if s.closing.Load() || len(raw) == 0 || len(raw) > control.MaxCommandBytes || len(signature) != 64 {
		return nil, zero, ErrFenceReconciliation
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
	enrollment, err := readControllerDeployment(ctx, tx)
	if err != nil {
		return nil, zero, err
	}
	a := control.Authority{DeploymentID: enrollment.DeploymentID, ControllerEpoch: enrollment.ControllerEpoch, ControlRevision: enrollment.ControlRevision, PublicKey: enrollment.PublicKey, Fingerprint: enrollment.Fingerprint}
	started := time.Now()
	now := started.UnixMilli()
	v, err := control.VerifyBackendRegistration(raw, signature, a, now)
	if err != nil {
		return nil, zero, err
	}
	index := registrationIndex(v)
	ref := v.Candidate()
	stored, err := loadAdmittedCommand(ctx, tx, index.EffectID)
	if err != nil {
		return nil, zero, err
	}
	fence, _ := strconv.ParseInt(ref.InstallationFence, 10, 64)
	floor, err := commandResourceFloor(ctx, tx, index)
	if err != nil || !sameLogicalCommand(stored, index) || stored.state != state || stored.fence != fence || floor != fence {
		return nil, zero, ErrFenceReconciliation
	}
	var receipt string
	var ack sql.NullInt64
	if err := tx.QueryRowContext(ctx, `SELECT receipt_json,ack_attempted_ms FROM runner_commands WHERE effect_id=?`, index.EffectID).Scan(&receipt, &ack); err != nil {
		return nil, zero, err
	}
	if ack.Valid {
		return nil, zero, ErrFenceReconciliation
	}
	var pending int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM runner_commands WHERE installation_id=? AND effect_id<>? AND state IN ('prepared','unknown')`, ref.InstallationID, index.EffectID).Scan(&pending); err != nil {
		return nil, zero, err
	}
	if pending != 0 {
		return nil, zero, ErrFenceReconciliation
	}
	if err := validateBackendRegistrationSource(ctx, tx, ref); err != nil {
		return nil, zero, err
	}
	sourceStored, err := loadAdmittedCommand(ctx, tx, ref.EffectID)
	if err != nil {
		return nil, zero, err
	}
	var sourceReceipt string
	if err := tx.QueryRowContext(ctx, `SELECT receipt_json FROM runner_commands WHERE effect_id=?`, ref.EffectID).Scan(&sourceReceipt); err != nil {
		return nil, zero, err
	}
	proof, proofSHA, err := candidateDataEvidence(ctx, tx, ref, now)
	if err != nil {
		return nil, zero, err
	}
	source := candidateFacts{command: ref, stored: sourceStored, authority: a, raw: v.Command().CandidateRaw, signature: v.Command().CandidateSignature, now: now, started: started, data: proof, dataSHA: proofSHA, receipt: sourceReceipt}
	running, err := readCandidateRunningTransaction(ctx, tx, source)
	if err != nil {
		return nil, zero, err
	}
	// Keep the nested source envelope in source for the existing candidate final
	// checks; the separately verified outer envelope is retained in verified.
	f := backendRegistrationFacts{verified: v, source: source, running: running, receipt: receipt}
	if err := finalCandidateCheck(ctx, source); err != nil {
		return nil, zero, err
	}
	ok = true
	return tx, f, nil
}
func checkBackendRegistration(ctx context.Context, f backendRegistrationFacts, raw, signature []byte) error {
	if err := finalCandidateCheck(ctx, f.source); err != nil {
		return err
	}
	_, err := control.VerifyBackendRegistration(raw, signature, f.source.authority, time.Now().UnixMilli())
	return err
}
func validRegistrationClaim(m backendRegistrationClaim) bool {
	return m.Format == registrationClaimFormat && enrollmentUUID.MatchString(m.ClaimID) && m.ClaimID != "00000000-0000-0000-0000-000000000000" && enrollmentUUID.MatchString(m.EffectID) && dataSHA(m.PayloadSHA256) && dataSHA(m.SourceSHA256) && dataSHA(m.NativeID) && dataSHA(m.DocumentSHA256) && dataNumber(m.ClaimedMS, false)
}
func registrationClaimMatches(m backendRegistrationClaim, f backendRegistrationFacts) bool {
	c := f.verified.Command()
	source, _ := json.Marshal(f.running)
	claimed, _ := strconv.ParseInt(m.ClaimedMS, 10, 64)
	return validRegistrationClaim(m) && m.EffectID == c.EffectID && m.PayloadSHA256 == c.PayloadSHA256 && m.SourceSHA256 == candidateHash(source) && m.NativeID == f.running.NativeID && m.DocumentSHA256 == c.DocumentSHA256 && claimed <= f.source.now
}
func registrationBinding(m backendRegistrationClaim) string {
	raw, _ := json.Marshal(m)
	return candidateHash(raw)
}
func (s *Store) claimBackendRegistration(ctx context.Context, raw, signature []byte) (backendRegistrationClaim, error) {
	var zero backendRegistrationClaim
	tx, f, err := s.beginBackendRegistration(ctx, raw, signature, "prepared")
	if err != nil {
		return zero, err
	}
	defer tx.Rollback()
	if f.receipt != "{}" {
		return zero, ErrFenceReconciliation
	}
	id, err := dataClaimID()
	if err != nil {
		return zero, err
	}
	source, _ := json.Marshal(f.running)
	c := f.verified.Command()
	m := backendRegistrationClaim{Format: registrationClaimFormat, ClaimID: id, EffectID: c.EffectID, PayloadSHA256: c.PayloadSHA256, SourceSHA256: candidateHash(source), NativeID: f.running.NativeID, DocumentSHA256: c.DocumentSHA256, ClaimedMS: strconv.FormatInt(f.source.now, 10)}
	if !registrationClaimMatches(m, f) {
		return zero, ErrFenceReconciliation
	}
	encoded, _ := json.Marshal(m)
	result, err := tx.ExecContext(ctx, `UPDATE runner_commands SET state='unknown',receipt_json=?,updated_ms=? WHERE effect_id=? AND state='prepared' AND receipt_json='{}' AND ack_attempted_ms IS NULL`, string(encoded), f.source.now, c.EffectID)
	if err != nil {
		return zero, err
	}
	if n, err := result.RowsAffected(); err != nil || n != 1 {
		return zero, ErrFenceReconciliation
	}
	if err := checkBackendRegistration(ctx, f, raw, signature); err != nil {
		return zero, err
	}
	if s.closing.Load() {
		return zero, ErrFenceReconciliation
	}
	if err := tx.Commit(); err != nil {
		return zero, err
	}
	return m, nil
}

// DispatchBackendRegistration serializes native work under the shared execution
// gate. Only a fresh prepared claim can write. Unknown retries observe an exact
// already-published claim-bound file; they never create, overwrite or repair one.
// This private infrastructure path is not exposed to an application/browser API.
func (s *Store) DispatchBackendRegistration(ctx context.Context, raw, signature, descriptor, document []byte, native *driver.CandidateRuntime) (BackendRegistrationReceipt, error) {
	var zero BackendRegistrationReceipt
	if native == nil || len(raw) == 0 || len(raw) > control.MaxCommandBytes || len(signature) != 64 || len(descriptor) == 0 || len(descriptor) > appplatform.MaxDescriptorBytes || len(document) == 0 || len(document) > 4096 {
		return zero, ErrFenceReconciliation
	}
	raw, signature, descriptor, document = bytes.Clone(raw), bytes.Clone(signature), bytes.Clone(descriptor), bytes.Clone(document)
	defer clear(document)
	if err := s.acquireDataExecution(ctx); err != nil {
		return zero, err
	}
	defer s.releaseDataExecution()
	// Validate secrets/descriptor before admitting any new intent. No private bytes
	// enter journal rows or an error message.
	enrollment, err := s.ControllerDeployment(ctx)
	if err != nil {
		return zero, err
	}
	a := control.Authority{DeploymentID: enrollment.DeploymentID, ControllerEpoch: enrollment.ControllerEpoch, ControlRevision: enrollment.ControlRevision, PublicKey: enrollment.PublicKey, Fingerprint: enrollment.Fingerprint}
	v, err := control.VerifyBackendRegistration(raw, signature, a, time.Now().UnixMilli())
	if err != nil {
		return zero, err
	}
	config, err := control.ValidateBackendRegistrationDocument(v, descriptor, document)
	if err != nil {
		return zero, err
	}
	config.Close()
	admitted, err := s.AdmitBackendRegistrationCommand(ctx, raw, signature)
	if err != nil {
		return zero, err
	}
	if admitted.AdmittedFence != admitted.CurrentFence {
		return zero, ErrFenceReconciliation
	}
	observeOnly := true
	var marker backendRegistrationClaim
	switch admitted.State {
	case "prepared":
		marker, err = s.claimBackendRegistration(ctx, raw, signature)
		if err != nil {
			return zero, err
		}
		observeOnly = false
	case "unknown":
	case "applied":
		return s.ReadBackendRegistrationReceipt(ctx, raw, signature)
	default:
		return zero, ErrFenceReconciliation
	}
	tx, f, err := s.beginBackendRegistration(ctx, raw, signature, "unknown")
	if err != nil {
		return zero, err
	}
	if observeOnly {
		if !canonicalDataJSON(f.receipt, &marker) {
			tx.Rollback()
			return zero, ErrFenceReconciliation
		}
	}
	if !registrationClaimMatches(marker, f) || registrationBinding(marker) != candidateHash([]byte(f.receipt)) {
		tx.Rollback()
		return zero, ErrFenceReconciliation
	}
	if err := checkBackendRegistration(ctx, f, raw, signature); err != nil {
		tx.Rollback()
		return zero, err
	}
	if err := tx.Commit(); err != nil {
		return zero, err
	}
	ref := f.verified.Candidate()
	request, err := candidateNativeRequest(ref, f.source.data, descriptor)
	if err != nil {
		return zero, err
	}
	launch, err := native.PrepareCandidate(ctx, request)
	if err != nil {
		return zero, err
	}
	defer launch.Close()
	snapshot, err := launch.Snapshot()
	if err != nil || candidatePreflight(snapshot) != *f.running.Claim.Preflight {
		return zero, ErrFenceReconciliation
	}
	expires, _ := strconv.ParseInt(ref.ExpiresMS, 10, 64)
	bounded, cancel := context.WithDeadline(ctx, f.source.started.Add(time.UnixMilli(expires).Sub(f.source.started)))
	defer cancel()
	// Revalidate journal state after native preflight, immediately before file IO.
	current, check, err := s.beginBackendRegistration(bounded, raw, signature, "unknown")
	if err != nil {
		return zero, err
	}
	if check.receipt != f.receipt || !registrationClaimMatches(marker, check) {
		current.Rollback()
		return zero, ErrFenceReconciliation
	}
	if err := checkBackendRegistration(bounded, check, raw, signature); err != nil {
		current.Rollback()
		return zero, err
	}
	if err := current.Commit(); err != nil {
		return zero, err
	}
	var observed driver.BackendRegistrationObservation
	if observeOnly {
		observed, err = native.ObserveCandidateRegistration(bounded, launch, f.running.NativeID, registrationBinding(marker), document)
	} else {
		observed, err = native.DeliverCandidateRegistration(bounded, launch, f.running.NativeID, registrationBinding(marker), document)
	}
	if err != nil {
		return zero, err
	}
	return s.settleBackendRegistration(bounded, raw, signature, marker, observed)
}
func (s *Store) settleBackendRegistration(ctx context.Context, raw, signature []byte, marker backendRegistrationClaim, observation driver.BackendRegistrationObservation) (BackendRegistrationReceipt, error) {
	var zero BackendRegistrationReceipt
	snapshot, err := observation.Snapshot()
	if err != nil {
		return zero, err
	}
	tx, f, err := s.beginBackendRegistration(ctx, raw, signature, "unknown")
	if err != nil {
		return zero, err
	}
	defer tx.Rollback()
	encodedMarker, _ := json.Marshal(marker)
	request, err := candidateNativeRequest(f.verified.Candidate(), f.source.data, nil)
	claimed, _ := strconv.ParseInt(marker.ClaimedMS, 10, 64)
	if err != nil || f.receipt != string(encodedMarker) || !registrationClaimMatches(marker, f) || snapshot.Launch.Identity != request.Identity || snapshot.Launch.Data != request.Data || candidatePreflight(snapshot.Launch) != *f.running.Claim.Preflight || snapshot.NativeID != marker.NativeID || snapshot.DocumentSHA256 != marker.DocumentSHA256 || snapshot.BindingSHA256 != registrationBinding(marker) || snapshot.ObservedMS < claimed || snapshot.ObservedMS > f.source.now {
		return zero, ErrFenceReconciliation
	}
	receipt := BackendRegistrationReceipt{Format: registrationReceiptFormat, Claim: marker, BindingSHA256: snapshot.BindingSHA256, Device: strconv.FormatUint(snapshot.Device, 10), Inode: strconv.FormatUint(snapshot.Inode, 10), ObservedMS: strconv.FormatInt(snapshot.ObservedMS, 10), SettledMS: strconv.FormatInt(f.source.now, 10)}
	encoded, _ := json.Marshal(receipt)
	if _, err := ParseBackendRegistrationReceipt(encoded); err != nil {
		return zero, err
	}
	result, err := tx.ExecContext(ctx, `UPDATE runner_commands SET state='applied',receipt_json=?,updated_ms=? WHERE effect_id=? AND state='unknown' AND receipt_json=? AND ack_attempted_ms IS NULL`, string(encoded), f.source.now, marker.EffectID, f.receipt)
	if err != nil {
		return zero, err
	}
	if n, err := result.RowsAffected(); err != nil || n != 1 {
		return zero, ErrFenceReconciliation
	}
	if err := checkBackendRegistration(ctx, f, raw, signature); err != nil {
		return zero, err
	}
	if s.closing.Load() {
		return zero, ErrFenceReconciliation
	}
	if err := tx.Commit(); err != nil {
		return zero, err
	}
	return receipt, nil
}
func ParseBackendRegistrationReceipt(raw []byte) (BackendRegistrationReceipt, error) {
	var r BackendRegistrationReceipt
	if len(raw) == 0 || len(raw) > 4096 || !canonicalDataJSON(string(raw), &r) || r.Format != registrationReceiptFormat || !validRegistrationClaim(r.Claim) || r.BindingSHA256 != registrationBinding(r.Claim) || !nativeNumber(r.Device, false) || !nativeNumber(r.Inode, true) || !dataNumber(r.ObservedMS, false) || !dataNumber(r.SettledMS, false) {
		return BackendRegistrationReceipt{}, ErrFenceReconciliation
	}
	claimed, _ := strconv.ParseInt(r.Claim.ClaimedMS, 10, 64)
	observed, _ := strconv.ParseInt(r.ObservedMS, 10, 64)
	settled, _ := strconv.ParseInt(r.SettledMS, 10, 64)
	if observed < claimed || settled < observed {
		return BackendRegistrationReceipt{}, ErrFenceReconciliation
	}
	return r, nil
}
func (s *Store) ReadBackendRegistrationReceipt(ctx context.Context, raw, signature []byte) (BackendRegistrationReceipt, error) {
	var zero BackendRegistrationReceipt
	tx, f, err := s.beginBackendRegistration(ctx, raw, signature, "applied")
	if err != nil {
		return zero, err
	}
	defer tx.Rollback()
	receipt, err := ParseBackendRegistrationReceipt([]byte(f.receipt))
	settled, _ := strconv.ParseInt(receipt.SettledMS, 10, 64)
	if err != nil || !registrationClaimMatches(receipt.Claim, f) || settled > f.source.now {
		return zero, ErrFenceReconciliation
	}
	if err := checkBackendRegistration(ctx, f, raw, signature); err != nil {
		return zero, err
	}
	if s.closing.Load() {
		return zero, ErrFenceReconciliation
	}
	if err := tx.Commit(); err != nil {
		return zero, err
	}
	return receipt, nil
}
