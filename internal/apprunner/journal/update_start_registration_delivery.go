package journal

import (
	"bytes"
	"context"
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

var ErrUpdateStartRegistrationUncertain = errors.New("update-start registration requires reconciliation")

type updateStartRegistrationAttempt struct {
	owner    *Store
	marker   string
	deadline time.Time
	used     atomic.Bool
}

func (s *Store) claimUpdateStartRegistration(ctx context.Context, raw, signature []byte) (*updateStartRegistrationAttempt, error) {
	tx, facts, err := beginUpdateStartRegistration(ctx, s, raw, signature, "prepared")
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	id, err := dataClaimID()
	if err != nil {
		return nil, err
	}
	c, start := facts.verified.Command(), facts.verified.Start().Command()
	marker := updateStartRegistrationClaim{
		Format: updateStartRegistrationClaimFormat, ClaimID: id,
		EffectID: c.EffectID, PayloadSHA256: c.PayloadSHA256,
		SourceSHA256: c.RunningReceiptSHA256, NativeID: facts.source.running.NativeID,
		DocumentSHA256: c.DocumentSHA256, ControlRevision: start.ControlRevision,
		ClaimedMS: strconv.FormatInt(facts.source.start.now, 10),
		ExpiresMS: start.ExpiresMS,
	}
	if !updateStartRegistrationMarkerMatches(
		marker, facts.verified, facts.source.running,
		facts.row.created, facts.source.start.now) {
		return nil, ErrFenceReconciliation
	}
	encoded, _ := json.Marshal(marker)
	changed, err := tx.ExecContext(ctx,
		`UPDATE runner_commands SET state='unknown',receipt_json=?,updated_ms=? WHERE effect_id=? AND state='prepared' AND receipt_json='{}' AND ack_attempted_ms IS NULL`,
		string(encoded), facts.source.start.now, c.EffectID)
	if err != nil {
		return nil, err
	}
	if n, err := changed.RowsAffected(); err != nil || n != 1 {
		return nil, ErrFenceReconciliation
	}
	if err := checkUpdateStartRegistrationCurrent(ctx, facts); err != nil {
		return nil, err
	}
	if s.closing.Load() {
		return nil, ErrFenceReconciliation
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	if err := checkUpdateStartRegistrationCurrent(ctx, facts); err != nil {
		return nil, err
	}
	expires, err := updateStartRegistrationExpiry(facts)
	if err != nil {
		return nil, err
	}
	return &updateStartRegistrationAttempt{
		owner: s, marker: string(encoded),
		deadline: time.UnixMilli(expires),
	}, nil
}

// DispatchUpdateStartRegistration validates private bytes before journal
// admission. This path creates no viewer grant and does not make the candidate
// the active generation.
func (s *Store) DispatchUpdateStartRegistration(ctx context.Context, raw, signature, descriptor, document []byte, native *driver.CandidateRuntime) (UpdateStartRegistrationReceipt, error) {
	var zero UpdateStartRegistrationReceipt
	if native == nil || s.closing.Load() || len(raw) == 0 ||
		len(raw) > control.MaxCommandBytes || len(signature) != 64 ||
		len(descriptor) == 0 || len(descriptor) > appplatform.MaxDescriptorBytes ||
		len(document) == 0 || len(document) > 4096 {
		return zero, ErrFenceReconciliation
	}
	raw, signature, descriptor, document = bytes.Clone(raw), bytes.Clone(signature), bytes.Clone(descriptor), bytes.Clone(document)
	defer clear(document)
	if err := s.acquireDataExecution(ctx); err != nil {
		return zero, err
	}
	defer s.releaseDataExecution()
	if s.closing.Load() {
		return zero, ErrFenceReconciliation
	}
	deployment, err := s.ControllerDeployment(ctx)
	if err != nil {
		return zero, err
	}
	authority := control.Authority{
		DeploymentID: deployment.DeploymentID, ControllerEpoch: deployment.ControllerEpoch,
		ControlRevision: deployment.ControlRevision, PublicKey: deployment.PublicKey,
		Fingerprint: deployment.Fingerprint,
	}
	verified, err := control.VerifyUpdateStartRegistration(
		raw, signature, authority, time.Now().UnixMilli())
	if err != nil {
		return zero, fmt.Errorf(
			"update-start registration command: %w", err)
	}
	private, err := control.ValidateUpdateStartRegistrationDocument(
		verified, descriptor, document)
	if err != nil {
		return zero, fmt.Errorf(
			"update-start registration document: %w", err)
	}
	private.Close()
	expires, _ := strconv.ParseInt(verified.Start().Command().ExpiresMS, 10, 64)
	ctx, cancel := context.WithDeadline(ctx, time.UnixMilli(expires))
	defer cancel()
	admitted, err := s.AdmitUpdateStartRegistrationCommand(ctx, raw, signature)
	if err != nil {
		return zero, fmt.Errorf(
			"update-start registration admission: %w", err)
	}
	var attempt *updateStartRegistrationAttempt
	switch admitted.State {
	case "prepared":
		if _, err := s.preflightUpdateStartRegistration(
			ctx, raw, signature, descriptor, native, "prepared"); err != nil {
			return zero, fmt.Errorf(
				"update-start registration preflight: %w", err)
		}
		attempt, err = s.claimUpdateStartRegistration(ctx, raw, signature)
		if err != nil {
			return zero, fmt.Errorf(
				"update-start registration claim: %w", err)
		}
	case "unknown":
	case "applied":
		return s.ReadUpdateStartRegistrationReceipt(ctx, raw, signature)
	default:
		return zero, ErrFenceReconciliation
	}
	return s.executeUpdateStartRegistration(
		ctx, raw, signature, descriptor, document, native, attempt)
}

func (s *Store) executeUpdateStartRegistration(ctx context.Context, raw, signature, descriptor, document []byte, native *driver.CandidateRuntime, attempt *updateStartRegistrationAttempt) (UpdateStartRegistrationReceipt, error) {
	var zero UpdateStartRegistrationReceipt
	if native == nil {
		return zero, ErrFenceReconciliation
	}
	tx, facts, err := beginUpdateStartRegistration(ctx, s, raw, signature, "unknown")
	if err != nil {
		return zero, err
	}
	tx.Rollback()
	marker := facts.row.marker
	if len(document) == 0 || len(document) > 4096 ||
		updateStartRegistrationDocumentDigest(document) != marker.DocumentSHA256 {
		return zero, ErrFenceReconciliation
	}
	recovery := attempt == nil
	if !recovery {
		if attempt.owner != s || attempt.marker != facts.row.receipt ||
			marker.ExpiresMS != facts.verified.Start().Command().ExpiresMS ||
			marker.ControlRevision != facts.verified.Start().Command().ControlRevision ||
			!time.Now().Before(attempt.deadline) ||
			!attempt.used.CompareAndSwap(false, true) {
			return zero, ErrFenceReconciliation
		}
		bounded, cancel := context.WithDeadline(ctx, attempt.deadline)
		defer cancel()
		ctx = bounded
	} else {
		if !updateStartRegistrationRecoveryFresh(facts) {
			return zero, ErrFenceReconciliation
		}
		fresh, err := s.preflightUpdateStartRegistration(
			ctx, raw, signature, descriptor, native, "unknown")
		if err != nil {
			return zero, err
		}
		if !sameUpdateStartRegistrationSource(facts, fresh) {
			return zero, ErrFenceReconciliation
		}
		facts = fresh
	}
	if err := checkUpdateStartRegistrationCurrent(ctx, facts); err != nil {
		return zero, err
	}
	if s.closing.Load() {
		return zero, ErrFenceReconciliation
	}
	var observed driver.UpdateStartRegistrationObservation
	if recovery {
		observed, err = native.ObserveUpdateStartRegistration(
			ctx, facts.source.request, facts.source.expected,
			facts.source.running.NativeID, updateStartRegistrationBinding(marker),
			document)
	} else {
		observed, err = native.DeliverUpdateStartRegistration(
			ctx, facts.source.request, facts.source.expected,
			facts.source.running.NativeID, updateStartRegistrationBinding(marker),
			document)
	}
	if err != nil {
		return zero, errors.Join(ErrUpdateStartRegistrationUncertain, err)
	}
	result, err := s.settleUpdateStartRegistration(
		ctx, facts, observed, recovery)
	if err != nil {
		return zero, errors.Join(ErrUpdateStartRegistrationUncertain, err)
	}
	return result, nil
}

func (s *Store) settleUpdateStartRegistration(ctx context.Context, before updateStartRegistrationFacts, observed driver.UpdateStartRegistrationObservation, recovery bool) (UpdateStartRegistrationReceipt, error) {
	var zero UpdateStartRegistrationReceipt
	tx, facts, err := beginUpdateStartRegistration(
		ctx, s, before.raw, before.signature, "unknown")
	if err != nil {
		return zero, err
	}
	defer tx.Rollback()
	if !sameUpdateStartRegistrationSource(before, facts) {
		return zero, ErrFenceReconciliation
	}
	if recovery && !updateStartRegistrationRecoveryFresh(facts) {
		return zero, ErrFenceReconciliation
	}
	actual, err := observed.Snapshot()
	marker := facts.row.marker
	if err != nil || actual.Launch != facts.source.expected ||
		actual.NativeID != marker.NativeID ||
		actual.BindingSHA256 != updateStartRegistrationBinding(marker) ||
		actual.DocumentSHA256 != marker.DocumentSHA256 ||
		actual.ObservedMS < before.source.start.now ||
		actual.ObservedMS > facts.source.start.now {
		return zero, ErrFenceReconciliation
	}
	receipt := UpdateStartRegistrationReceipt{
		Format: updateStartRegistrationReceiptFormat, Claim: marker,
		BindingSHA256: actual.BindingSHA256,
		Device:        strconv.FormatUint(actual.Device, 10),
		Inode:         strconv.FormatUint(actual.Inode, 10),
		ObservedMS:    strconv.FormatInt(actual.ObservedMS, 10),
		SettledMS:     strconv.FormatInt(facts.source.start.now, 10),
	}
	if recovery {
		start := facts.verified.Start().Command()
		receipt.Format = updateStartRegistrationRecoveredFormat
		receipt.RecoveryEnvelopeSHA256 = candidateHash(facts.raw)
		receipt.RecoveryControlRevision = start.ControlRevision
		receipt.RecoveryIssuedMS = start.IssuedMS
		receipt.RecoveryExpiresMS = start.ExpiresMS
	}
	encoded, _ := json.Marshal(receipt)
	if _, err := ParseUpdateStartRegistrationReceipt(encoded); err != nil {
		return zero, err
	}
	changed, err := tx.ExecContext(ctx,
		`UPDATE runner_commands SET state='applied',receipt_json=?,updated_ms=? WHERE effect_id=? AND state='unknown' AND receipt_json=? AND ack_attempted_ms IS NULL`,
		string(encoded), facts.source.start.now, marker.EffectID, facts.row.receipt)
	if err != nil {
		return zero, err
	}
	if n, err := changed.RowsAffected(); err != nil || n != 1 {
		return zero, ErrFenceReconciliation
	}
	if err := checkUpdateStartRegistrationCurrent(ctx, facts); err != nil {
		return zero, err
	}
	if s.closing.Load() {
		return zero, ErrFenceReconciliation
	}
	if err := tx.Commit(); err != nil {
		return zero, err
	}
	if err := checkUpdateStartRegistrationCurrent(ctx, facts); err != nil {
		return zero, err
	}
	return receipt, nil
}

func (s *Store) ReadUpdateStartRegistrationReceipt(ctx context.Context, raw, signature []byte) (UpdateStartRegistrationReceipt, error) {
	var zero UpdateStartRegistrationReceipt
	tx, facts, err := beginUpdateStartRegistration(ctx, s, raw, signature, "applied")
	if err != nil {
		return zero, err
	}
	defer tx.Rollback()
	receipt, err := ParseUpdateStartRegistrationReceipt([]byte(facts.row.receipt))
	if err != nil {
		return zero, err
	}
	if err := checkUpdateStartRegistrationCurrent(ctx, facts); err != nil {
		return zero, err
	}
	if s.closing.Load() {
		return zero, ErrFenceReconciliation
	}
	if err := tx.Commit(); err != nil {
		return zero, err
	}
	if err := checkUpdateStartRegistrationCurrent(ctx, facts); err != nil {
		return zero, err
	}
	return receipt, nil
}
