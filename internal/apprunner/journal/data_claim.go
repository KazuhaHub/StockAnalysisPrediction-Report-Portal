package journal

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"sync/atomic"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
)

var ErrDataClaimUnavailable = errors.New("runner data execution claim is unavailable")

// DataClaim is an opaque process-local handle for one durable uncertain attempt.
// Copying it shares the same consumption state; JSON/zero values grant nothing.
type DataClaim struct{ state *dataClaimState }

func (DataClaim) MarshalJSON() ([]byte, error)  { return nil, ErrDataClaimUnavailable }
func (c *DataClaim) UnmarshalJSON([]byte) error { c.state = nil; return ErrDataClaimUnavailable }

type dataClaimState struct {
	owner          *Store
	marker         string
	raw, signature []byte
	used           atomic.Bool
}

// DataAttempt records invocation only, never physical readiness or completion.
type DataAttempt struct{ Invoked bool }

type dataClaimMarker struct {
	Format          string `json:"format"`
	ClaimID         string `json:"claim_id"`
	EffectID        string `json:"effect_id"`
	ResourceID      string `json:"resource_id"`
	OwnershipSHA256 string `json:"ownership_sha256"`
	AdmittedFence   string `json:"admitted_fence"`
	ClaimedMS       string `json:"claimed_ms"`
	ExpiresMS       string `json:"expires_ms"`
}

func dataClaimID() (string, error) {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return "", err
	}
	id[6] = (id[6] & 15) | 64
	id[8] = (id[8] & 63) | 128
	return fmt.Sprintf("%x-%x-%x-%x-%x", id[:4], id[4:6], id[6:8], id[8:10], id[10:]), nil
}
func (s *Store) acquireDataExecution(ctx context.Context) error {
	s.executionOnce.Do(func() { s.executionGate = make(chan struct{}, 1) })
	select {
	case s.executionGate <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (s *Store) releaseDataExecution() { <-s.executionGate }

// ClaimPrepareData changes prepared to unknown before issuing a live handle.
// It requires an existing exact reservation and never calls a native provider.
// A restart cannot reconstruct the handle or claim unknown work a second time.
func (s *Store) ClaimPrepareData(ctx context.Context, raw, signature []byte) (DataClaim, error) {
	var zero DataClaim
	if s.closing.Load() {
		return zero, ErrDataClaimUnavailable
	}
	tx, facts, err := s.beginDataCommand(ctx, raw, signature, "prepared", "{}")
	if err != nil {
		return zero, err
	}
	defer tx.Rollback()
	existing, err := existingDataResource(ctx, tx, facts.command)
	if err != nil {
		return zero, err
	}
	reservation, err := reservedDataResource(facts, existing)
	if err != nil {
		return zero, err
	}
	id, err := dataClaimID()
	if err != nil {
		return zero, err
	}
	marker, err := json.Marshal(dataClaimMarker{"portal.runner.prepare-data-claim/1-dev.1", id, reservation.EffectID, reservation.ResourceID, reservation.OwnershipSHA256, strconv.FormatInt(reservation.ControlFence, 10), strconv.FormatInt(facts.now, 10), facts.command.ExpiresMS})
	if err != nil || len(marker) > 16384 {
		return zero, ErrDataClaimUnavailable
	}
	if s.closing.Load() {
		return zero, ErrDataClaimUnavailable
	}
	changed, err := tx.ExecContext(ctx, `UPDATE runner_commands SET state='unknown',receipt_json=?,updated_ms=? WHERE effect_id=? AND state='prepared' AND receipt_json='{}' AND ack_attempted_ms IS NULL AND admitted_fence=? AND payload_sha256=?`, string(marker), facts.now, facts.command.EffectID, facts.stored.fence, facts.command.PayloadSHA256)
	if err != nil {
		return zero, err
	}
	count, err := changed.RowsAffected()
	if err != nil {
		return zero, err
	}
	if count != 1 {
		return zero, ErrDataClaimUnavailable
	}
	if err := finalDataCommandCheck(ctx, facts); err != nil {
		return zero, err
	}
	if s.closing.Load() {
		return zero, ErrDataClaimUnavailable
	}
	if err := tx.Commit(); err != nil {
		return zero, err
	}
	if s.closing.Load() {
		return zero, ErrDataClaimUnavailable
	}
	return DataClaim{&dataClaimState{owner: s, marker: string(marker), raw: facts.raw, signature: facts.signature}}, nil
}

// RunPrepareDataOnce serializes trusted synchronous infrastructure callbacks.
// It does not accept callbacks from apps/JSON or commit any physical receipt.
// The callback must not close this Store, recursively enter this gate, or return
// while its native work is still running. A native adapter/root pin is not wired.
func (s *Store) RunPrepareDataOnce(ctx context.Context, claim DataClaim, run func(context.Context, DataReservation, control.PrepareDataPayload) error) (DataAttempt, error) {
	var zero DataAttempt
	live := claim.state
	if live == nil || live.owner != s || run == nil || s.closing.Load() {
		return zero, ErrDataClaimUnavailable
	}
	if err := s.acquireDataExecution(ctx); err != nil {
		return zero, err
	}
	defer s.releaseDataExecution()
	if err := ctx.Err(); err != nil {
		return zero, err
	}
	if s.closing.Load() || live.used.Load() {
		return zero, ErrDataClaimUnavailable
	}
	tx, facts, err := s.beginDataCommand(ctx, live.raw, live.signature, "unknown", live.marker)
	if err != nil {
		return zero, err
	}
	defer tx.Rollback()
	existing, err := existingDataResource(ctx, tx, facts.command)
	if err != nil {
		return zero, err
	}
	reservation, err := reservedDataResource(facts, existing)
	if err != nil {
		return zero, err
	}
	if err := finalDataCommandCheck(ctx, facts); err != nil {
		return zero, err
	}
	if s.closing.Load() {
		return zero, ErrDataClaimUnavailable
	}
	if err := tx.Commit(); err != nil {
		return zero, err
	}
	// No SQL lock spans external work. The owner gate remains held until the
	// synchronous invocation settles, including when Close is waiting for it.
	if err := finalDataCommandCheck(ctx, facts); err != nil {
		return zero, err
	}
	if s.closing.Load() {
		return zero, ErrDataClaimUnavailable
	}
	if !live.used.CompareAndSwap(false, true) {
		return zero, ErrDataClaimUnavailable
	}
	return DataAttempt{Invoked: true}, run(ctx, reservation, facts.command.Payload)
}
