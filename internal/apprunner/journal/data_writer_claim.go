package journal

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
)

var ErrDataWriterClaimUnavailable = errors.New("runner data writer claim is unavailable")

type DataWriterClaim struct{ state *dataWriterClaimState }

func (DataWriterClaim) MarshalJSON() ([]byte, error) { return nil, ErrDataWriterClaimUnavailable }
func (c *DataWriterClaim) UnmarshalJSON([]byte) error {
	c.state = nil
	return ErrDataWriterClaimUnavailable
}

type dataWriterClaimState struct {
	owner          *Store
	raw, signature []byte
	marker         string
	used           atomic.Bool
}
type dataWriterClaimMarker struct {
	Format            string `json:"format"`
	ClaimID           string `json:"claim_id"`
	EffectID          string `json:"effect_id"`
	PayloadSHA256     string `json:"payload_sha256"`
	DataReceiptSHA256 string `json:"data_receipt_sha256"`
	ResourceID        string `json:"resource_id"`
	AdmittedFence     string `json:"admitted_fence"`
	ClaimedMS         string `json:"claimed_ms"`
	ExpiresMS         string `json:"expires_ms"`
}
type dataWriterFacts struct {
	verified       control.VerifiedDataWriterCommand
	authority      control.Authority
	raw, signature []byte
	source         DataPreparationReceipt
	receipt        string
	fence, now     int64
}

func finalDataWriterCheck(ctx context.Context, f dataWriterFacts) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	now := time.Now().UnixMilli()
	if now < f.now {
		return control.ErrInvalidCommand
	}
	_, err := control.VerifyDataWriter(f.raw, f.signature, f.authority, now)
	return err
}

func (s *Store) beginDataWriter(ctx context.Context, raw, signature []byte, state string, receipt *string) (*sql.Tx, dataWriterFacts, error) {
	var zero dataWriterFacts
	if s.closing.Load() || len(raw) == 0 || len(raw) > control.MaxCommandBytes || len(signature) != 64 {
		return nil, zero, ErrDataWriterClaimUnavailable
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
	d, err := readControllerDeployment(ctx, tx)
	if err != nil {
		return nil, zero, err
	}
	a := control.Authority{DeploymentID: d.DeploymentID, ControllerEpoch: d.ControllerEpoch, ControlRevision: d.ControlRevision, PublicKey: d.PublicKey, Fingerprint: d.Fingerprint}
	now := time.Now().UnixMilli()
	v, err := control.VerifyDataWriter(raw, signature, a, now)
	if err != nil {
		return nil, zero, err
	}
	index := dataWriterIndex(v)
	stored, err := loadAdmittedCommand(ctx, tx, index.EffectID)
	if err != nil {
		return nil, zero, err
	}
	fence, _ := strconv.ParseInt(index.InstallationFence, 10, 64)
	floor, err := commandResourceFloor(ctx, tx, index)
	if err != nil || !sameLogicalCommand(stored, index) || stored.state != state || stored.fence != fence || floor != fence {
		return nil, zero, ErrFenceReconciliation
	}
	var current string
	var ack sql.NullInt64
	if err := tx.QueryRowContext(ctx, `SELECT receipt_json,ack_attempted_ms FROM runner_commands WHERE effect_id=?`, index.EffectID).Scan(&current, &ack); err != nil {
		return nil, zero, err
	}
	if ack.Valid || (receipt != nil && current != *receipt) {
		return nil, zero, ErrFenceReconciliation
	}
	source, err := validateDataWriterSource(ctx, tx, v, now)
	if err != nil {
		return nil, zero, err
	}
	f := dataWriterFacts{verified: v, authority: a, raw: raw, signature: signature, source: source, receipt: current, fence: fence, now: now}
	if err := finalDataWriterCheck(ctx, f); err != nil {
		return nil, zero, err
	}
	ok = true
	return tx, f, nil
}

func dataWriterMarkerMatches(m dataWriterClaimMarker, f dataWriterFacts) bool {
	c, ref := f.verified.Command(), f.verified.Data()
	claimed, err := strconv.ParseInt(m.ClaimedMS, 10, 64)
	expires, expiresErr := strconv.ParseInt(m.ExpiresMS, 10, 64)
	return m.Format == "portal.runner.data-writer-claim/1-dev.1" && enrollmentUUID.MatchString(m.ClaimID) && m.ClaimID != "00000000-0000-0000-0000-000000000000" && m.EffectID == c.EffectID && m.PayloadSHA256 == c.PayloadSHA256 && m.DataReceiptSHA256 == c.DataReceiptSHA256 && m.ResourceID == c.ResourceID && m.AdmittedFence == strconv.FormatInt(f.fence, 10) && dataNumber(m.ClaimedMS, false) && err == nil && expiresErr == nil && claimed <= f.now && expires > claimed && expires-claimed <= 30000 && m.ExpiresMS == ref.ExpiresMS
}

// ClaimDataWriter records uncertainty before issuing a process-local one-shot
// handle. It performs no native IO and cannot reconstruct an unknown attempt.
func (s *Store) ClaimDataWriter(ctx context.Context, raw, signature []byte) (DataWriterClaim, error) {
	var zero DataWriterClaim
	empty := "{}"
	tx, f, err := s.beginDataWriter(ctx, raw, signature, "prepared", &empty)
	if err != nil {
		return zero, err
	}
	defer tx.Rollback()
	id, err := dataClaimID()
	if err != nil {
		return zero, err
	}
	c := f.verified.Command()
	m := dataWriterClaimMarker{"portal.runner.data-writer-claim/1-dev.1", id, c.EffectID, c.PayloadSHA256, c.DataReceiptSHA256, c.ResourceID, strconv.FormatInt(f.fence, 10), strconv.FormatInt(f.now, 10), f.verified.Data().ExpiresMS}
	if !dataWriterMarkerMatches(m, f) {
		return zero, ErrFenceReconciliation
	}
	encoded, _ := json.Marshal(m)
	changed, err := tx.ExecContext(ctx, `UPDATE runner_commands SET state='unknown',receipt_json=?,updated_ms=? WHERE effect_id=? AND state='prepared' AND receipt_json='{}' AND ack_attempted_ms IS NULL AND admitted_fence=? AND payload_sha256=?`, string(encoded), f.now, c.EffectID, f.fence, c.PayloadSHA256)
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
	if s.closing.Load() {
		return zero, ErrDataWriterClaimUnavailable
	}
	return DataWriterClaim{state: &dataWriterClaimState{owner: s, raw: f.raw, signature: f.signature, marker: string(encoded)}}, nil
}

// DataWriterAttempt indicates invocation only, never physical success/readiness.
type DataWriterAttempt struct{ Invoked bool }
type DataWriterPlan struct {
	Command control.DataWriterCommand
	Source  DataPreparationReceipt
}

// RunDataWriterOnce serializes a trusted synchronous native preflight/effect
// callback. No app or wire message can supply it. The callback must verify actual
// writer/process exclusion, must not close/reenter the Store, settle its row or
// return while work is still live. Callback success never creates an applied
// receipt. A later native adapter must settle independent opaque OS evidence.
func (s *Store) RunDataWriterOnce(ctx context.Context, claim DataWriterClaim, run func(context.Context, DataWriterPlan) error) (DataWriterAttempt, error) {
	var zero DataWriterAttempt
	live := claim.state
	if live == nil || live.owner != s || run == nil || s.closing.Load() {
		return zero, ErrDataWriterClaimUnavailable
	}
	if err := s.acquireDataExecution(ctx); err != nil {
		return zero, err
	}
	defer s.releaseDataExecution()
	if live.used.Load() || s.closing.Load() {
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
	if err := finalDataWriterCheck(ctx, f); err != nil {
		return zero, err
	}
	if err := tx.Commit(); err != nil {
		return zero, err
	}
	expires, _ := strconv.ParseInt(marker.ExpiresMS, 10, 64)
	bounded, cancel := context.WithDeadline(ctx, time.UnixMilli(expires))
	defer cancel()
	if err := finalDataWriterCheck(bounded, f); err != nil {
		return zero, err
	}
	if s.closing.Load() || !live.used.CompareAndSwap(false, true) {
		return zero, ErrDataWriterClaimUnavailable
	}
	attempt := DataWriterAttempt{Invoked: true}
	runErr := run(bounded, DataWriterPlan{Command: f.verified.Command(), Source: f.source})
	// Once invoked, every subsequent error is compatible with a physical effect.
	// Re-read durable authority/source after IO instead of trusting old facts.
	post, after, err := s.beginDataWriter(bounded, live.raw, live.signature, "unknown", &live.marker)
	if err != nil {
		return attempt, errors.Join(runErr, err)
	}
	defer post.Rollback()
	if after.now < f.now || after.source != f.source {
		return attempt, errors.Join(runErr, ErrFenceReconciliation)
	}
	if err := finalDataWriterCheck(bounded, after); err != nil {
		return attempt, errors.Join(runErr, err)
	}
	if err := post.Commit(); err != nil {
		return attempt, errors.Join(runErr, err)
	}
	if err := finalDataWriterCheck(bounded, after); err != nil {
		return attempt, errors.Join(runErr, err)
	}
	if s.closing.Load() {
		return attempt, errors.Join(runErr, ErrDataWriterClaimUnavailable)
	}
	return attempt, runErr
}
