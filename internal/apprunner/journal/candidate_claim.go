package journal

import (
	"context"
	"encoding/json"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
)

// CandidateClaim cannot be serialized or reconstructed after owner restart.
// Copies share a one-shot flag; it is not a native root/quota/readiness proof.
type CandidateClaim struct{ state *candidateClaimState }

func (CandidateClaim) MarshalJSON() ([]byte, error) { return nil, ErrCandidateClaimUnavailable }
func (c *CandidateClaim) UnmarshalJSON([]byte) error {
	c.state = nil
	return ErrCandidateClaimUnavailable
}

type candidateClaimState struct {
	owner          *Store
	marker         string
	raw, signature []byte
	deadline       time.Time
	used           atomic.Bool
}
type candidateClaimMarker struct {
	Format            string `json:"format"`
	ClaimID           string `json:"claim_id"`
	EffectID          string `json:"effect_id"`
	ResourceID        string `json:"resource_id"`
	OwnershipSHA256   string `json:"ownership_sha256"`
	DataReceiptSHA256 string `json:"data_receipt_sha256"`
	AdmittedFence     string `json:"admitted_fence"`
	ClaimedMS         string `json:"claimed_ms"`
	ExpiresMS         string `json:"expires_ms"`
}
type CandidateAttempt struct{ Invoked bool }

// ClaimCandidate durably marks uncertainty before issuing a live handle. It
// requires the unchanged reservation and applied data evidence; no OS IO occurs.
func (s *Store) ClaimCandidate(ctx context.Context, raw, signature []byte) (CandidateClaim, error) {
	var zero CandidateClaim
	tx, f, err := s.beginCandidateCommand(ctx, raw, signature, "prepared", "{}")
	if err != nil {
		return zero, err
	}
	defer tx.Rollback()
	existing, err := existingCandidateResource(ctx, tx, f.command)
	if err != nil {
		return zero, err
	}
	reservation, err := reservedCandidate(f, existing)
	if err != nil {
		return zero, err
	}
	id, err := dataClaimID()
	if err != nil {
		return zero, err
	}
	marker, err := json.Marshal(candidateClaimMarker{Format: "portal.runner.candidate-claim/1-dev.1", ClaimID: id, EffectID: f.command.EffectID, ResourceID: reservation.ResourceID, OwnershipSHA256: reservation.OwnershipSHA256, DataReceiptSHA256: f.dataSHA, AdmittedFence: strconv.FormatInt(f.stored.fence, 10), ClaimedMS: strconv.FormatInt(f.now, 10), ExpiresMS: f.command.ExpiresMS})
	if err != nil || len(marker) > 16384 {
		return zero, ErrCandidateClaimUnavailable
	}
	changed, err := tx.ExecContext(ctx, `UPDATE runner_commands SET state='unknown',receipt_json=?,updated_ms=? WHERE effect_id=? AND kind='start-candidate' AND stage='start' AND state='prepared' AND receipt_json='{}' AND ack_attempted_ms IS NULL AND admitted_fence=? AND payload_sha256=?`, string(marker), f.now, f.command.EffectID, f.stored.fence, f.command.PayloadSHA256)
	if err != nil {
		return zero, err
	}
	count, err := changed.RowsAffected()
	if err != nil {
		return zero, err
	}
	if count != 1 {
		return zero, ErrCandidateClaimUnavailable
	}
	if err := finalCandidateCheck(ctx, f); err != nil {
		return zero, err
	}
	if s.closing.Load() {
		return zero, ErrCandidateClaimUnavailable
	}
	if err := tx.Commit(); err != nil {
		return zero, err
	}
	if s.closing.Load() {
		return zero, ErrCandidateClaimUnavailable
	}
	expires, _ := strconv.ParseInt(f.command.ExpiresMS, 10, 64)
	return CandidateClaim{state: &candidateClaimState{owner: s, marker: string(marker), raw: f.raw, signature: f.signature, deadline: f.started.Add(time.UnixMilli(expires).Sub(f.started))}}, nil
}

// RunCandidateOnce admits one trusted synchronous infrastructure callback. Apps
// and JSON cannot supply callbacks. Native integration must still pin/check real
// roots, socket identities, quotas and sandbox policy; no receipt is invented.
// Callbacks must honor cancellation, return only after native work settles, and
// never close this Store or recursively enter the shared execution gate.
func (s *Store) RunCandidateOnce(ctx context.Context, claim CandidateClaim, run func(context.Context, CandidateReservation, control.CandidatePayload, DataPreparationReceipt) error) (CandidateAttempt, error) {
	var zero CandidateAttempt
	live := claim.state
	if live == nil || live.owner != s || run == nil || s.closing.Load() {
		return zero, ErrCandidateClaimUnavailable
	}
	if err := s.acquireDataExecution(ctx); err != nil {
		return zero, err
	}
	defer s.releaseDataExecution()
	if err := ctx.Err(); err != nil {
		return zero, err
	}
	if s.closing.Load() || live.used.Load() || !time.Now().Before(live.deadline) {
		return zero, ErrCandidateClaimUnavailable
	}
	tx, f, err := s.beginCandidateCommand(ctx, live.raw, live.signature, "unknown", live.marker)
	if err != nil {
		return zero, err
	}
	defer tx.Rollback()
	existing, err := existingCandidateResource(ctx, tx, f.command)
	if err != nil {
		return zero, err
	}
	reservation, err := reservedCandidate(f, existing)
	if err != nil {
		return zero, err
	}
	var marker candidateClaimMarker
	if !canonicalDataJSON(live.marker, &marker) || marker.Format != "portal.runner.candidate-claim/1-dev.1" || !enrollmentUUID.MatchString(marker.ClaimID) || marker.EffectID != f.command.EffectID || marker.ResourceID != reservation.ResourceID || marker.OwnershipSHA256 != reservation.OwnershipSHA256 || marker.DataReceiptSHA256 != f.dataSHA || marker.AdmittedFence != strconv.FormatInt(f.stored.fence, 10) || marker.ExpiresMS != f.command.ExpiresMS || !dataNumber(marker.ClaimedMS, false) {
		return zero, ErrCandidateClaimUnavailable
	}
	claimed, _ := strconv.ParseInt(marker.ClaimedMS, 10, 64)
	if claimed > f.now {
		return zero, ErrCandidateClaimUnavailable
	}
	if err := finalCandidateCheck(ctx, f); err != nil {
		return zero, err
	}
	if s.closing.Load() {
		return zero, ErrCandidateClaimUnavailable
	}
	if err := tx.Commit(); err != nil {
		return zero, err
	}
	if err := finalCandidateCheck(ctx, f); err != nil {
		return zero, err
	}
	if s.closing.Load() || !time.Now().Before(live.deadline) || !live.used.CompareAndSwap(false, true) {
		return zero, ErrCandidateClaimUnavailable
	}
	bounded, cancel := context.WithDeadline(ctx, live.deadline)
	defer cancel()
	err = run(bounded, reservation, f.command.Payload, f.data)
	if err == nil {
		err = bounded.Err()
	}
	return CandidateAttempt{Invoked: true}, err
}
