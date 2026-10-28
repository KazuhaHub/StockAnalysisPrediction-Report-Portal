package journal

import (
	"context"
	"encoding/json"
	"strconv"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/driver"
)

// Historical process evidence only. No readiness, registration, active writer
// lease, acknowledgement or native mutation follows from parsing/reading it.
type WritableCandidateRunningReceipt struct {
	Format           string                       `json:"format"`
	Claim            writableCandidateClaimMarker `json:"claim"`
	NativeID         string                       `json:"native_id"`
	NativeObservedMS string                       `json:"native_observed_ms"`
	SettledMS        string                       `json:"settled_ms"`
}

func validWritableRunningReceipt(r WritableCandidateRunningReceipt) bool {
	m := r.Claim
	if r.Format != writableRunningFormat || m.Format != "portal.runner.writable-candidate-claim/1-dev.1" || m.SandboxProfile != control.WritableCandidateSandboxProfile || m.Preflight == nil || !validWritablePreflight(*m.Preflight) || !dataSHA(r.NativeID) {
		return false
	}
	for _, id := range []string{m.ClaimID, m.EffectID, m.ResourceID} {
		if !enrollmentUUID.MatchString(id) || id == "00000000-0000-0000-0000-000000000000" {
			return false
		}
	}
	for _, sha := range []string{m.PayloadSHA256, m.OwnershipSHA256, m.DataReceiptSHA256, m.WriterReceiptSHA256} {
		if !dataSHA(sha) {
			return false
		}
	}
	for _, n := range []string{m.ControlRevision, m.AdmittedFence} {
		if !dataNumber(n, true) {
			return false
		}
	}
	for _, n := range []string{m.ClaimedMS, m.ExpiresMS, m.PreflightBoundMS, r.NativeObservedMS, r.SettledMS} {
		if !dataNumber(n, false) {
			return false
		}
	}
	claimed, _ := strconv.ParseInt(m.ClaimedMS, 10, 64)
	expires, _ := strconv.ParseInt(m.ExpiresMS, 10, 64)
	bound, _ := strconv.ParseInt(m.PreflightBoundMS, 10, 64)
	observed, _ := strconv.ParseInt(r.NativeObservedMS, 10, 64)
	settled, _ := strconv.ParseInt(r.SettledMS, 10, 64)
	return expires > claimed && expires-claimed <= 30000 && bound >= claimed && observed >= bound && settled >= observed && settled < expires
}
func ParseWritableCandidateRunningReceipt(raw []byte) (WritableCandidateRunningReceipt, error) {
	var zero WritableCandidateRunningReceipt
	var r WritableCandidateRunningReceipt
	if len(raw) == 0 || len(raw) > 16384 || !canonicalDataJSON(string(raw), &r) || !validWritableRunningReceipt(r) {
		return zero, ErrFenceReconciliation
	}
	return r, nil
}
func runningWritableCandidateResource(f writableCandidateFacts, r *dataResource, receipt WritableCandidateRunningReceipt) error {
	c := f.verified.Index()
	generation, _ := strconv.ParseInt(c.TargetGeneration, 10, 64)
	writer, _ := strconv.ParseInt(c.Payload.WriterFence, 10, 64)
	claimed, _ := strconv.ParseInt(receipt.Claim.ClaimedMS, 10, 64)
	observed, _ := strconv.ParseInt(receipt.NativeObservedMS, 10, 64)
	settled, _ := strconv.ParseInt(receipt.SettledMS, 10, 64)
	if r == nil || !validWritableRunningReceipt(receipt) || !writableCandidateMarkerIdentityMatches(receipt.Claim, f) || r.id != c.Payload.ResourceID || r.installation != c.InstallationID || !r.namespace.Valid || r.namespace.String != c.Payload.DataNamespaceID || r.kind != "container" || r.native != receipt.NativeID || !r.release.Valid || r.release.String != c.Payload.ReleaseSHA256 || r.generation != generation || r.epoch != c.ControllerEpoch || r.fence != f.stored.fence || r.writer != writer || r.desired != "present" || r.observed != "running" || r.ownership != writableCandidateOwnership(f) || r.revision != 2 || !r.observedMS.Valid || r.observedMS.Int64 != observed || r.created < f.created || r.created > claimed || r.updated != settled || settled > f.now {
		return ErrFenceReconciliation
	}
	return nil
}

// Caller may read under a renewed current key/reference. The original claim,
// control revision, expiry, native binding and physical observation are retained.
func (s *Store) ReadWritableCandidateRunningReceipt(ctx context.Context, raw, signature []byte) (WritableCandidateRunningReceipt, error) {
	var zero WritableCandidateRunningReceipt
	tx, f, err := s.beginWritableCandidate(ctx, raw, signature, "applied", nil)
	if err != nil {
		return zero, err
	}
	defer tx.Rollback()
	r, err := ParseWritableCandidateRunningReceipt([]byte(f.receipt))
	if err != nil {
		return zero, err
	}
	if err := runningWritableCandidateResource(f, f.resource, r); err != nil {
		return zero, err
	}
	if err := finalWritableCandidateCheck(ctx, f); err != nil {
		return zero, err
	}
	if s.closing.Load() {
		return zero, ErrWritableCandidateClaimUnavailable
	}
	if err := tx.Commit(); err != nil {
		return zero, err
	}
	if err := finalWritableCandidateCheck(ctx, f); err != nil {
		return zero, err
	}
	if s.closing.Load() {
		return zero, ErrWritableCandidateClaimUnavailable
	}
	return r, nil
}

// A parsed receipt/snapshot is never accepted here; only the concrete driver's
// opaque process result bound to this live claim can settle the atomic pair.
func (s *Store) settleWritableCandidateRunning(ctx context.Context, claim WritableCandidateClaim, observed driver.WritableCandidateRunningObservation) (WritableCandidateRunningReceipt, error) {
	var zero WritableCandidateRunningReceipt
	live := claim.state
	if live == nil || live.owner != s || !live.used.Load() {
		return zero, ErrWritableCandidateClaimUnavailable
	}
	bounded, cancel := context.WithDeadline(ctx, live.deadline)
	defer cancel()
	ctx = bounded
	if err := s.acquireDataExecution(ctx); err != nil {
		return zero, err
	}
	defer s.releaseDataExecution()
	if s.closing.Load() || live.bound == nil {
		return zero, ErrWritableCandidateClaimUnavailable
	}
	actual, err := observed.Snapshot()
	if err != nil {
		return zero, err
	}
	tx, f, err := s.beginWritableCandidate(ctx, live.raw, live.signature, "unknown", &live.marker)
	if err != nil {
		return zero, err
	}
	defer tx.Rollback()
	var m writableCandidateClaimMarker
	if !canonicalDataJSON(live.marker, &m) || m.Preflight == nil || *m.Preflight != *live.bound || *m.Preflight != writablePreflight(actual.Launch) || !writableSnapshotMatches(actual.Launch, f, m) || actual.ObservedMS > f.now {
		return zero, ErrFenceReconciliation
	}
	r := WritableCandidateRunningReceipt{Format: writableRunningFormat, Claim: m, NativeID: actual.NativeID, NativeObservedMS: strconv.FormatInt(actual.ObservedMS, 10), SettledMS: strconv.FormatInt(f.now, 10)}
	if !validWritableRunningReceipt(r) {
		return zero, ErrFenceReconciliation
	}
	c := f.verified.Index()
	changed, err := tx.ExecContext(ctx, `UPDATE runner_resources SET native_id=?,observed_state='running',revision=2,observed_ms=?,updated_ms=? WHERE id=? AND kind='container' AND native_id=? AND revision=1 AND observed_state='unknown' AND observed_ms IS NULL AND ownership_sha256=?`, r.NativeID, actual.ObservedMS, f.now, c.Payload.ResourceID, writableCandidatePendingPrefix+c.Payload.ResourceID, writableCandidateOwnership(f))
	if err != nil {
		return zero, err
	}
	if n, err := changed.RowsAffected(); err != nil || n != 1 {
		return zero, ErrFenceReconciliation
	}
	encoded, _ := json.Marshal(r)
	changed, err = tx.ExecContext(ctx, `UPDATE runner_commands SET state='applied',receipt_json=?,updated_ms=? WHERE effect_id=? AND state='unknown' AND receipt_json=? AND ack_attempted_ms IS NULL`, string(encoded), f.now, c.EffectID, live.marker)
	if err != nil {
		return zero, err
	}
	if n, err := changed.RowsAffected(); err != nil || n != 1 {
		return zero, ErrFenceReconciliation
	}
	if err := finalWritableCandidateCheck(ctx, f); err != nil {
		return zero, err
	}
	if s.closing.Load() {
		return zero, ErrWritableCandidateClaimUnavailable
	}
	if err := tx.Commit(); err != nil {
		return zero, err
	}
	if err := finalWritableCandidateCheck(ctx, f); err != nil {
		return zero, err
	}
	return r, nil
}
