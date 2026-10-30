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

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/driver"
)

var ErrWritableRegistrationUncertain = errors.New("writable backend registration requires reconciliation")

type writableRegistrationFacts struct {
	verified       control.VerifiedWritableBackendRegistrationCommand
	source         writableCandidateFacts
	running        WritableCandidateRunningReceipt
	row            writableRegistrationRow
	raw, signature []byte
}
type writableRegistrationAttempt struct {
	owner    *Store
	marker   string
	deadline time.Time
	used     atomic.Bool
}

func (s *Store) beginWritableRegistration(ctx context.Context, raw, signature []byte, state string) (*sql.Tx, writableRegistrationFacts, error) {
	var zero writableRegistrationFacts
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
	d, err := readControllerDeployment(ctx, tx)
	if err != nil {
		return nil, zero, err
	}
	a := control.Authority{DeploymentID: d.DeploymentID, ControllerEpoch: d.ControllerEpoch, ControlRevision: d.ControlRevision, PublicKey: d.PublicKey, Fingerprint: d.Fingerprint}
	started := time.Now()
	now := started.UnixMilli()
	v, err := control.VerifyWritableBackendRegistration(raw, signature, a, now)
	if err != nil {
		return nil, zero, err
	}
	source, running, err := loadWritableRegistrationSource(ctx, tx, v, a, now, started)
	if err != nil {
		return nil, zero, err
	}
	row, present, err := readWritableRegistrationRow(ctx, tx, v, now)
	if err != nil {
		return nil, zero, err
	}
	floor, err := commandResourceFloor(ctx, tx, writableRegistrationIndex(v))
	if err != nil || !present || row.stored.state != state || floor != row.stored.fence {
		return nil, zero, ErrFenceReconciliation
	}
	f := writableRegistrationFacts{verified: v, source: source, running: running, row: row, raw: raw, signature: signature}
	if err := checkWritableRegistrationCurrent(ctx, f); err != nil {
		return nil, zero, err
	}
	ok = true
	return tx, f, nil
}
func checkWritableRegistrationCurrent(ctx context.Context, f writableRegistrationFacts) error {
	if err := finalWritableCandidateCheck(ctx, f.source); err != nil {
		return err
	}
	_, err := control.VerifyWritableBackendRegistration(f.raw, f.signature, f.source.authority, time.Now().UnixMilli())
	return err
}
func sameWritableRegistrationSource(a, b writableRegistrationFacts) bool {
	return a.source.source == b.source.source && a.source.receipt == b.source.receipt && a.source.created == b.source.created && a.source.updated == b.source.updated && a.source.resource != nil && b.source.resource != nil && *a.source.resource == *b.source.resource && a.row.receipt == b.row.receipt && a.row.created == b.row.created && a.row.updated == b.row.updated && a.row.stored == b.row.stored && b.source.now >= a.source.now
}
func writableRegistrationRecoveryFresh(f writableRegistrationFacts) bool {
	issued, _ := strconv.ParseInt(f.verified.Candidate().Index().IssuedMS, 10, 64)
	expires, _ := strconv.ParseInt(f.row.marker.ExpiresMS, 10, 64)
	return issued > expires && issued <= f.source.now
}

// Caller holds owner execution gate. Only this fresh prepared transition yields
// a process-local once-only write attempt; unknown/applied states never do.
func (s *Store) claimWritableRegistration(ctx context.Context, raw, signature []byte) (*writableRegistrationAttempt, error) {
	tx, f, err := s.beginWritableRegistration(ctx, raw, signature, "prepared")
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	id, err := dataClaimID()
	if err != nil {
		return nil, err
	}
	c, ref := f.verified.Command(), f.verified.Candidate().Index()
	m := writableRegistrationClaim{Format: writableRegistrationClaimFormat, ClaimID: id, EffectID: c.EffectID, PayloadSHA256: c.PayloadSHA256, SourceSHA256: c.RunningReceiptSHA256, NativeID: f.running.NativeID, DocumentSHA256: c.DocumentSHA256, ControlRevision: ref.ControlRevision, ClaimedMS: strconv.FormatInt(f.source.now, 10), ExpiresMS: ref.ExpiresMS}
	if !writableRegistrationMarkerMatches(m, f.verified, f.running, f.row.created, f.source.now) {
		return nil, ErrFenceReconciliation
	}
	encoded, _ := json.Marshal(m)
	changed, err := tx.ExecContext(ctx, `UPDATE runner_commands SET state='unknown',receipt_json=?,updated_ms=? WHERE effect_id=? AND state='prepared' AND receipt_json='{}' AND ack_attempted_ms IS NULL`, string(encoded), f.source.now, c.EffectID)
	if err != nil {
		return nil, err
	}
	if n, err := changed.RowsAffected(); err != nil || n != 1 {
		return nil, ErrFenceReconciliation
	}
	if err := checkWritableRegistrationCurrent(ctx, f); err != nil {
		return nil, err
	}
	if s.closing.Load() {
		return nil, ErrFenceReconciliation
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	if err := checkWritableRegistrationCurrent(ctx, f); err != nil {
		return nil, err
	}
	expires, _ := strconv.ParseInt(ref.ExpiresMS, 10, 64)
	return &writableRegistrationAttempt{owner: s, marker: string(encoded), deadline: f.source.started.Add(time.UnixMilli(expires).Sub(f.source.started))}, nil
}
func (s *Store) preflightWritableRegistration(ctx context.Context, raw, signature, descriptor []byte, native *driver.CandidateRuntime, state string) (writableRegistrationFacts, error) {
	var zero writableRegistrationFacts
	tx, before, err := s.beginWritableRegistration(ctx, raw, signature, state)
	if err != nil {
		return zero, err
	}
	request, expected, err := writableCandidateExpectedSnapshot(before.source, before.running.Claim, descriptor)
	tx.Rollback()
	if err != nil {
		return zero, err
	}
	observation, err := native.CheckWritableCandidateReadiness(ctx, request, expected, before.running.NativeID, before.verified.Candidate().Index().Payload.DataFormat)
	if err != nil {
		return zero, err
	}
	actual, err := observation.Snapshot()
	if err != nil || actual.Launch != expected || actual.NativeID != before.running.NativeID {
		return zero, ErrFenceReconciliation
	}
	post, after, err := s.beginWritableRegistration(ctx, raw, signature, state)
	if err != nil {
		return zero, err
	}
	defer post.Rollback()
	if !sameWritableRegistrationSource(before, after) || actual.ObservedMS < before.source.now || actual.ObservedMS > after.source.now {
		return zero, ErrFenceReconciliation
	}
	if err := checkWritableRegistrationCurrent(ctx, after); err != nil {
		return zero, err
	}
	if s.closing.Load() {
		return zero, ErrFenceReconciliation
	}
	if err := post.Commit(); err != nil {
		return zero, err
	}
	return after, nil
}

// Infrastructure-only. Validate private bytes before admission/claiming; keep
// them out of rows/errors. Prepared first writes, expired unknown only observes,
// and applied only returns current independently validated historical evidence.
func (s *Store) DispatchWritableBackendRegistration(ctx context.Context, raw, signature, descriptor, document []byte, native *driver.CandidateRuntime) (WritableBackendRegistrationReceipt, error) {
	var zero WritableBackendRegistrationReceipt
	if native == nil || s.closing.Load() || len(raw) == 0 || len(raw) > control.MaxCommandBytes || len(signature) != 64 || len(descriptor) == 0 || len(descriptor) > appplatform.MaxDescriptorBytes || len(document) == 0 || len(document) > 4096 {
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
	d, err := s.ControllerDeployment(ctx)
	if err != nil {
		return zero, err
	}
	a := control.Authority{DeploymentID: d.DeploymentID, ControllerEpoch: d.ControllerEpoch, ControlRevision: d.ControlRevision, PublicKey: d.PublicKey, Fingerprint: d.Fingerprint}
	v, err := control.VerifyWritableBackendRegistration(raw, signature, a, time.Now().UnixMilli())
	if err != nil {
		return zero, err
	}
	private, err := control.ValidateWritableBackendRegistrationDocument(v, descriptor, document)
	if err != nil {
		return zero, err
	}
	private.Close()
	expires, _ := strconv.ParseInt(v.Candidate().Index().ExpiresMS, 10, 64)
	ctx, cancel := context.WithDeadline(ctx, time.UnixMilli(expires))
	defer cancel()
	admitted, err := s.AdmitWritableBackendRegistrationCommand(ctx, raw, signature)
	if err != nil {
		return zero, err
	}
	var attempt *writableRegistrationAttempt
	switch admitted.State {
	case "prepared":
		if _, err := s.preflightWritableRegistration(ctx, raw, signature, descriptor, native, "prepared"); err != nil {
			return zero, err
		}
		attempt, err = s.claimWritableRegistration(ctx, raw, signature)
		if err != nil {
			return zero, err
		}
	case "unknown":
	case "applied":
		return s.ReadWritableBackendRegistrationReceipt(ctx, raw, signature)
	default:
		return zero, ErrFenceReconciliation
	}
	return s.executeWritableRegistration(ctx, raw, signature, descriptor, document, native, attempt)
}

// Caller retains the execution gate through preflight, possible effect, and
// settlement. No callback, parsed receipt or boolean can supply native success.
func (s *Store) executeWritableRegistration(ctx context.Context, raw, signature, descriptor, document []byte, native *driver.CandidateRuntime, attempt *writableRegistrationAttempt) (WritableBackendRegistrationReceipt, error) {
	var zero WritableBackendRegistrationReceipt
	if native == nil {
		return zero, ErrFenceReconciliation
	}
	tx, f, err := s.beginWritableRegistration(ctx, raw, signature, "unknown")
	if err != nil {
		return zero, err
	}
	tx.Rollback()
	m, ref := f.row.marker, f.verified.Candidate().Index()
	if len(document) == 0 || len(document) > 4096 || candidateHash(document) != m.DocumentSHA256 {
		return zero, ErrFenceReconciliation
	}
	recovery := attempt == nil
	if !recovery {
		if attempt.owner != s || attempt.marker != f.row.receipt || m.ExpiresMS != ref.ExpiresMS || m.ControlRevision != ref.ControlRevision || !time.Now().Before(attempt.deadline) || !attempt.used.CompareAndSwap(false, true) {
			return zero, ErrFenceReconciliation
		}
		bounded, cancel := context.WithDeadline(ctx, attempt.deadline)
		defer cancel()
		ctx = bounded
	} else {
		if !writableRegistrationRecoveryFresh(f) {
			return zero, ErrFenceReconciliation
		}
		fresh, err := s.preflightWritableRegistration(ctx, raw, signature, descriptor, native, "unknown")
		if err != nil {
			return zero, err
		}
		if !sameWritableRegistrationSource(f, fresh) {
			return zero, ErrFenceReconciliation
		}
		f = fresh
	}
	request, expected, err := writableCandidateExpectedSnapshot(f.source, f.running.Claim, descriptor)
	if err != nil {
		return zero, err
	}
	if err := checkWritableRegistrationCurrent(ctx, f); err != nil {
		return zero, err
	}
	if s.closing.Load() {
		return zero, ErrFenceReconciliation
	}
	var observed driver.WritableBackendRegistrationObservation
	if recovery {
		observed, err = native.ObserveWritableBackendRegistration(ctx, request, expected, m.NativeID, writableRegistrationBinding(m), document)
	} else {
		observed, err = native.DeliverWritableBackendRegistration(ctx, request, expected, m.NativeID, writableRegistrationBinding(m), document)
	}
	if err != nil {
		return zero, errors.Join(ErrWritableRegistrationUncertain, err)
	}
	result, err := s.settleWritableRegistration(ctx, f, expected, observed, recovery)
	if err != nil {
		return zero, errors.Join(ErrWritableRegistrationUncertain, err)
	}
	return result, nil
}
func (s *Store) settleWritableRegistration(ctx context.Context, before writableRegistrationFacts, expected driver.WritableCandidateLaunchSnapshot, observed driver.WritableBackendRegistrationObservation, recovery bool) (WritableBackendRegistrationReceipt, error) {
	var zero WritableBackendRegistrationReceipt
	tx, f, err := s.beginWritableRegistration(ctx, before.raw, before.signature, "unknown")
	if err != nil {
		return zero, err
	}
	defer tx.Rollback()
	if !sameWritableRegistrationSource(before, f) {
		return zero, ErrFenceReconciliation
	}
	if recovery && !writableRegistrationRecoveryFresh(f) {
		return zero, ErrFenceReconciliation
	}
	actual, err := observed.Snapshot()
	m := f.row.marker
	if err != nil || actual.Launch != expected || !writableSnapshotMatches(actual.Launch, f.source, f.running.Claim) || actual.NativeID != m.NativeID || actual.BindingSHA256 != writableRegistrationBinding(m) || actual.DocumentSHA256 != m.DocumentSHA256 || actual.ObservedMS < before.source.now || actual.ObservedMS > f.source.now {
		return zero, ErrFenceReconciliation
	}
	r := WritableBackendRegistrationReceipt{Format: writableRegistrationReceiptFormat, Claim: m, BindingSHA256: actual.BindingSHA256, Device: strconv.FormatUint(actual.Device, 10), Inode: strconv.FormatUint(actual.Inode, 10), ObservedMS: strconv.FormatInt(actual.ObservedMS, 10), SettledMS: strconv.FormatInt(f.source.now, 10)}
	if recovery {
		ref := f.verified.Candidate().Index()
		r.Format = writableRegistrationRecoveredFormat
		r.RecoveryEnvelopeSHA256 = candidateHash(f.raw)
		r.RecoveryControlRevision = ref.ControlRevision
		r.RecoveryIssuedMS = ref.IssuedMS
		r.RecoveryExpiresMS = ref.ExpiresMS
	}
	encoded, _ := json.Marshal(r)
	if _, err := ParseWritableBackendRegistrationReceipt(encoded); err != nil {
		return zero, err
	}
	changed, err := tx.ExecContext(ctx, `UPDATE runner_commands SET state='applied',receipt_json=?,updated_ms=? WHERE effect_id=? AND state='unknown' AND receipt_json=? AND ack_attempted_ms IS NULL`, string(encoded), f.source.now, m.EffectID, f.row.receipt)
	if err != nil {
		return zero, err
	}
	if n, err := changed.RowsAffected(); err != nil || n != 1 {
		return zero, ErrFenceReconciliation
	}
	if err := checkWritableRegistrationCurrent(ctx, f); err != nil {
		return zero, err
	}
	if s.closing.Load() {
		return zero, ErrFenceReconciliation
	}
	if err := tx.Commit(); err != nil {
		return zero, err
	}
	if err := checkWritableRegistrationCurrent(ctx, f); err != nil {
		return zero, err
	}
	return r, nil
}
func (s *Store) ReadWritableBackendRegistrationReceipt(ctx context.Context, raw, signature []byte) (WritableBackendRegistrationReceipt, error) {
	var zero WritableBackendRegistrationReceipt
	tx, f, err := s.beginWritableRegistration(ctx, raw, signature, "applied")
	if err != nil {
		return zero, err
	}
	defer tx.Rollback()
	r, err := ParseWritableBackendRegistrationReceipt([]byte(f.row.receipt))
	if err != nil {
		return zero, err
	}
	if err := checkWritableRegistrationCurrent(ctx, f); err != nil {
		return zero, err
	}
	if s.closing.Load() {
		return zero, ErrFenceReconciliation
	}
	if err := tx.Commit(); err != nil {
		return zero, err
	}
	if err := checkWritableRegistrationCurrent(ctx, f); err != nil {
		return zero, err
	}
	if s.closing.Load() {
		return zero, ErrFenceReconciliation
	}
	return r, nil
}
