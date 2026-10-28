package journal

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
)

// Copies share one consumed flag. Neither restart nor JSON can reconstruct it.
type WritableCandidateClaim struct{ state *writableCandidateClaimState }
type writableCandidateClaimState struct {
	owner          *Store
	raw, signature []byte
	marker         string
	deadline       time.Time
	used           atomic.Bool
	bound          *writableCandidatePreflightEvidence
}

func (WritableCandidateClaim) MarshalJSON() ([]byte, error) {
	return nil, ErrWritableCandidateClaimUnavailable
}
func (c *WritableCandidateClaim) UnmarshalJSON([]byte) error {
	c.state = nil
	return ErrWritableCandidateClaimUnavailable
}

type writableCandidateClaimMarker struct {
	Format              string                              `json:"format"`
	ClaimID             string                              `json:"claim_id"`
	EffectID            string                              `json:"effect_id"`
	ResourceID          string                              `json:"resource_id"`
	PayloadSHA256       string                              `json:"payload_sha256"`
	OwnershipSHA256     string                              `json:"ownership_sha256"`
	DataReceiptSHA256   string                              `json:"data_receipt_sha256"`
	WriterReceiptSHA256 string                              `json:"writer_receipt_sha256"`
	ControlRevision     string                              `json:"control_revision"`
	AdmittedFence       string                              `json:"admitted_fence"`
	SandboxProfile      string                              `json:"sandbox_profile"`
	ClaimedMS           string                              `json:"claimed_ms"`
	ExpiresMS           string                              `json:"expires_ms"`
	Preflight           *writableCandidatePreflightEvidence `json:"preflight,omitempty"`
	PreflightBoundMS    string                              `json:"preflight_bound_ms,omitempty"`
}

func writableCandidateMarkerMatches(m writableCandidateClaimMarker, f writableCandidateFacts) bool {
	c := f.verified.Index()
	return writableCandidateMarkerIdentityMatches(m, f) && m.ControlRevision == c.ControlRevision && m.ExpiresMS == c.ExpiresMS
}
func writableCandidateMarkerIdentityMatches(m writableCandidateClaimMarker, f writableCandidateFacts) bool {
	c := f.verified.Index()
	claimed, _ := strconv.ParseInt(m.ClaimedMS, 10, 64)
	expires, _ := strconv.ParseInt(m.ExpiresMS, 10, 64)
	revision, _ := strconv.ParseInt(m.ControlRevision, 10, 64)
	sourceRevision, _ := strconv.ParseInt(f.source.writer.ControlRevision, 10, 64)
	currentRevision, _ := strconv.ParseInt(c.ControlRevision, 10, 64)
	if m.Format != "portal.runner.writable-candidate-claim/1-dev.1" || !enrollmentUUID.MatchString(m.ClaimID) || m.ClaimID == "00000000-0000-0000-0000-000000000000" || m.EffectID != c.EffectID || m.ResourceID != c.Payload.ResourceID || m.PayloadSHA256 != c.PayloadSHA256 || m.OwnershipSHA256 != writableCandidateOwnership(f) || m.DataReceiptSHA256 != f.verified.Writer().Command().DataReceiptSHA256 || m.WriterReceiptSHA256 != f.verified.Command().WriterReceiptSHA256 || !dataNumber(m.ControlRevision, true) || revision < sourceRevision || revision > currentRevision || m.AdmittedFence != strconv.FormatInt(f.stored.fence, 10) || m.SandboxProfile != control.WritableCandidateSandboxProfile || !dataNumber(m.ClaimedMS, false) || !dataNumber(m.ExpiresMS, true) || claimed < f.created || claimed > f.now || expires <= claimed || expires-claimed > 30000 {
		return false
	}
	if m.Preflight == nil {
		return m.PreflightBoundMS == ""
	}
	if !dataNumber(m.PreflightBoundMS, false) || !writablePreflightMatches(*m.Preflight, f, m) {
		return false
	}
	bound, _ := strconv.ParseInt(m.PreflightBoundMS, 10, 64)
	return bound >= claimed && bound < expires && bound <= f.now
}

// ClaimWritableCandidate commits unknown intent before returning a live handle.
// It neither contacts Docker nor proves a process started or became ready.
func (s *Store) ClaimWritableCandidate(ctx context.Context, raw, signature []byte) (WritableCandidateClaim, error) {
	var zero WritableCandidateClaim
	empty := "{}"
	tx, f, err := s.beginWritableCandidate(ctx, raw, signature, "prepared", &empty)
	if err != nil {
		return zero, err
	}
	defer tx.Rollback()
	reservation, err := reservedWritableCandidate(f, f.resource)
	if err != nil {
		return zero, err
	}
	id, err := dataClaimID()
	if err != nil {
		return zero, err
	}
	c := f.verified.Index()
	m := writableCandidateClaimMarker{Format: "portal.runner.writable-candidate-claim/1-dev.1", ClaimID: id, EffectID: c.EffectID, ResourceID: reservation.ResourceID, PayloadSHA256: c.PayloadSHA256, OwnershipSHA256: reservation.OwnershipSHA256, DataReceiptSHA256: reservation.DataReceiptSHA256, WriterReceiptSHA256: reservation.WriterReceiptSHA256, ControlRevision: c.ControlRevision, AdmittedFence: strconv.FormatInt(f.stored.fence, 10), SandboxProfile: control.WritableCandidateSandboxProfile, ClaimedMS: strconv.FormatInt(f.now, 10), ExpiresMS: c.ExpiresMS}
	if !writableCandidateMarkerMatches(m, f) {
		return zero, ErrFenceReconciliation
	}
	encoded, _ := json.Marshal(m)
	changed, err := tx.ExecContext(ctx, `UPDATE runner_commands SET state='unknown',receipt_json=?,updated_ms=? WHERE effect_id=? AND kind='start-candidate' AND stage='start' AND state='prepared' AND receipt_json='{}' AND ack_attempted_ms IS NULL AND admitted_fence=? AND payload_sha256=?`, string(encoded), f.now, c.EffectID, f.stored.fence, c.PayloadSHA256)
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
	if s.closing.Load() {
		return zero, ErrWritableCandidateClaimUnavailable
	}
	expires, _ := strconv.ParseInt(c.ExpiresMS, 10, 64)
	return WritableCandidateClaim{state: &writableCandidateClaimState{owner: s, raw: f.raw, signature: f.signature, marker: string(encoded), deadline: f.started.Add(time.UnixMilli(expires).Sub(f.started))}}, nil
}

type WritableCandidateAttempt struct{ Invoked bool }
type WritableCandidatePlan struct {
	Command     control.WritableCandidateCommand
	Candidate   control.CandidateCommand // verified index projection, not wire authority
	Reservation WritableCandidateReservation
	Data        DataPreparationReceipt
	Writer      DataWriterReceipt
}

// RunWritableCandidateOnce admits one trusted synchronous infrastructure
// callback, never application-supplied code. Callbacks must honor cancellation,
// return only when IO stops, and not close/reenter the Store, alter its durable
// claim/resource or settle the command. Success alone leaves unknown state;
// ExecuteWritableCandidate alone uses the private typed preflight binder during
// its callback and settles opaque process evidence after this guard succeeds.
func (s *Store) RunWritableCandidateOnce(ctx context.Context, claim WritableCandidateClaim, run func(context.Context, WritableCandidatePlan) error) (WritableCandidateAttempt, error) {
	var zero WritableCandidateAttempt
	live := claim.state
	if live == nil || live.owner != s || run == nil || s.closing.Load() {
		return zero, ErrWritableCandidateClaimUnavailable
	}
	if err := s.acquireDataExecution(ctx); err != nil {
		return zero, err
	}
	defer s.releaseDataExecution()
	if s.closing.Load() || live.used.Load() || !time.Now().Before(live.deadline) {
		return zero, ErrWritableCandidateClaimUnavailable
	}
	tx, f, err := s.beginWritableCandidate(ctx, live.raw, live.signature, "unknown", &live.marker)
	if err != nil {
		return zero, err
	}
	defer tx.Rollback()
	reservation, err := reservedWritableCandidate(f, f.resource)
	if err != nil {
		return zero, err
	}
	if err := finalWritableCandidateCheck(ctx, f); err != nil {
		return zero, err
	}
	var original writableCandidateClaimMarker
	if !canonicalDataJSON(live.marker, &original) || original.Preflight != nil || live.bound != nil {
		return zero, ErrFenceReconciliation
	}
	if err := tx.Commit(); err != nil {
		return zero, err
	}
	bounded, cancel := context.WithDeadline(ctx, live.deadline)
	defer cancel()
	if err := finalWritableCandidateCheck(bounded, f); err != nil {
		return zero, err
	}
	if s.closing.Load() || !time.Now().Before(live.deadline) || !live.used.CompareAndSwap(false, true) {
		return zero, ErrWritableCandidateClaimUnavailable
	}
	attempt := WritableCandidateAttempt{Invoked: true}
	runErr := run(bounded, WritableCandidatePlan{Command: f.verified.Command(), Candidate: f.verified.Index(), Reservation: reservation, Data: f.source.data, Writer: f.source.writer})
	post, after, err := s.beginWritableCandidate(bounded, live.raw, live.signature, "unknown", &live.marker)
	if err != nil {
		return attempt, errors.Join(ErrWritableCandidateUncertain, runErr, err)
	}
	defer post.Rollback()
	if after.now < f.now || after.source != f.source || after.resource == nil || *after.resource != *f.resource || after.created != f.created {
		return attempt, errors.Join(ErrWritableCandidateUncertain, runErr, ErrFenceReconciliation)
	}
	var current writableCandidateClaimMarker
	if !canonicalDataJSON(after.receipt, &current) || writableMarkerCore(current) != writableMarkerCore(original) || (current.Preflight == nil) != (live.bound == nil) || (current.Preflight != nil && *current.Preflight != *live.bound) {
		return attempt, errors.Join(ErrWritableCandidateUncertain, runErr, ErrFenceReconciliation)
	}
	if err := finalWritableCandidateCheck(bounded, after); err != nil {
		return attempt, errors.Join(ErrWritableCandidateUncertain, runErr, err)
	}
	if s.closing.Load() {
		return attempt, errors.Join(ErrWritableCandidateUncertain, runErr, ErrWritableCandidateClaimUnavailable)
	}
	if err := post.Commit(); err != nil {
		return attempt, errors.Join(ErrWritableCandidateUncertain, runErr, err)
	}
	if err := finalWritableCandidateCheck(bounded, after); err != nil {
		return attempt, errors.Join(ErrWritableCandidateUncertain, runErr, err)
	}
	if runErr != nil {
		return attempt, errors.Join(ErrWritableCandidateUncertain, runErr)
	}
	if s.closing.Load() {
		return attempt, errors.Join(ErrWritableCandidateUncertain, ErrWritableCandidateClaimUnavailable)
	}
	return attempt, nil
}
