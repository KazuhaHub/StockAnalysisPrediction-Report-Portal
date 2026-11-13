package journal

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/driver"
)

// Settling a candidate or writable-candidate start that was claimed and has no executor.
//
// A start claimed (unknown) whose native attempt failed stays unknown: the journal cannot tell a start that never
// created anything from one that left a container. Its capacity reservation is held for as long as the effect is
// unknown, so without a settlement a failed start would hold its share of the envelope until the journal is
// replaced. The settlement is modelled on settleOrphan of an update start (update_start_settle.go) and changes
// nothing natively; it only looks:
//
//   - the start's command window has ended, so nothing can still claim or run it, and the data execution gate is
//     taken without waiting, so no start is executing;
//   - the container is proven absent by name and by label (TargetInspector.InspectTarget);
//   - then, in ONE transaction, the command is settled reverted with the typed orphan receipt, the resource row is
//     marked absent, and the reservation is released, each with a compare-and-swap on the exact prior contents and
//     a rows-affected check.
//
// A container in any state keeps the effect unknown and the reservation held; a failed inspection changes nothing.
//
// Which Portal call path reaches it: the Portal's scheduler resends the signed start (or the writable start
// reconcile) through the runner's lifecycle actions. The plain candidate start arrives at DispatchCandidate (candidate
// execute action; completePlatformInstallCandidate on the Portal side) and the writable candidate start at
// DispatchWritableCandidate and ReconcileWritableCandidate (writable start and reconcile actions;
// reconcilePlatformWritableStart on the Portal side). Each of them reaches the settlement for an unknown start,
// after the window ended.

const candidateOrphanReceiptFormat = "portal.runner.candidate-orphan-receipt/1-dev.1"

// CandidateOrphanOutcome is the typed outcome of a start proven to have never run.
const CandidateOrphanOutcome = "candidate_never_ran"

var (
	// ErrCandidateOrphanSettled is the definite answer for a start the runner proved never ran and settled. It is
	// distinct from ErrFenceReconciliation: errors.Is(err, ErrFenceReconciliation) is false for it.
	ErrCandidateOrphanSettled = errors.New("runner candidate start never ran and was settled")
	// ErrCandidateOrphanPresent keeps the effect unknown: a container exists for the start, in some state.
	ErrCandidateOrphanPresent = fmt.Errorf("%w: a container exists for the unknown candidate start", ErrFenceReconciliation)
	// ErrCandidateOrphanBusy keeps the effect unknown: the data execution gate is held, so a start may be executing.
	ErrCandidateOrphanBusy = fmt.Errorf("%w: the data execution gate is held", ErrFenceReconciliation)
	// ErrCandidateOrphanWindow keeps the effect unknown: the start's command window has not ended.
	ErrCandidateOrphanWindow = fmt.Errorf("%w: the candidate start window has not ended", ErrFenceReconciliation)
)

// CandidateTargetInspector is the narrow native seam of the settlement: what exists for a resource's container, by
// name and by label. *driver.CandidateRuntime and TargetInspector implement it.
type CandidateTargetInspector interface {
	InspectTarget(ctx context.Context, resourceID string) (driver.TargetInspection, error)
}

// CandidateOrphanReceipt records that a claimed start was proven never to have run. It is the receipt of a
// reverted command row, so a replay returns the same bytes.
type CandidateOrphanReceipt struct {
	Format           string `json:"format"`
	DeploymentID     string `json:"deployment_id"`
	ControllerEpoch  string `json:"controller_epoch"`
	InstallationID   string `json:"installation_id"`
	OperationID      string `json:"operation_id"`
	TargetGeneration string `json:"target_generation"`
	Stage            string `json:"stage"`
	EffectNumber     string `json:"effect_number"`
	EffectID         string `json:"effect_id"`
	Kind             string `json:"kind"`
	PayloadSHA256    string `json:"payload_sha256"`
	ResourceID       string `json:"resource_id"`
	Outcome          string `json:"outcome"`
	SettledMS        string `json:"settled_ms"`
}

// CandidateOrphanSettledError answers a start the runner settled as never run. The settlement is already durable
// when it is returned, and a replay returns an equal receipt.
type CandidateOrphanSettledError struct{ Receipt CandidateOrphanReceipt }

func (e *CandidateOrphanSettledError) Error() string {
	return "runner candidate start never ran: " + e.Receipt.Outcome
}

// Is makes errors.Is(err, ErrCandidateOrphanSettled) true.
func (e *CandidateOrphanSettledError) Is(target error) bool {
	return target == ErrCandidateOrphanSettled
}

func validCandidateOrphanReceipt(r CandidateOrphanReceipt) bool {
	if r.Format != candidateOrphanReceiptFormat || r.Outcome != CandidateOrphanOutcome || r.Stage != "start" || r.Kind != "start-candidate" ||
		!dataSHA(r.PayloadSHA256) || !dataNumber(r.TargetGeneration, true) || !dataNumber(r.EffectNumber, true) || !dataNumber(r.SettledMS, true) {
		return false
	}
	for _, id := range []string{r.DeploymentID, r.ControllerEpoch, r.InstallationID, r.OperationID, r.EffectID, r.ResourceID} {
		if !importID(id) {
			return false
		}
	}
	return true
}

// ParseCandidateOrphanReceipt accepts only the canonical encoding of a valid orphan receipt.
func ParseCandidateOrphanReceipt(raw []byte) (CandidateOrphanReceipt, error) {
	var r CandidateOrphanReceipt
	if len(raw) == 0 || len(raw) > 4096 || !canonicalDataJSON(string(raw), &r) || !validCandidateOrphanReceipt(r) {
		return CandidateOrphanReceipt{}, ErrFenceReconciliation
	}
	return r, nil
}

// Matches reports whether the receipt answers exactly this signed command.
func (r CandidateOrphanReceipt) Matches(id CapacityIdentity) bool {
	return r.DeploymentID == id.DeploymentID && r.ControllerEpoch == id.ControllerEpoch && r.InstallationID == id.InstallationID &&
		r.OperationID == id.OperationID && r.TargetGeneration == id.TargetGeneration && r.Stage == id.Stage && r.EffectNumber == id.EffectNumber &&
		r.EffectID == id.EffectID && r.Kind == id.Kind && r.PayloadSHA256 == id.PayloadSHA256
}

func storedCandidateOrphan(raw string, id CapacityIdentity) (CandidateOrphanReceipt, bool) {
	r, err := ParseCandidateOrphanReceipt([]byte(raw))
	if err != nil || !r.Matches(id) {
		return CandidateOrphanReceipt{}, false
	}
	return r, true
}

// absenceSettleDelay is how long an absence settlement waits between its two looks. The Docker daemon can finish a
// container create after the client context that asked for it was cancelled, so a container missing at one look can
// exist a moment later. A package variable so tests do not wait it out.
var absenceSettleDelay = 2 * time.Second

// waitAbsenceSettle sleeps the settle delay, or returns the context's error if it ends first.
func waitAbsenceSettle(ctx context.Context) error {
	if absenceSettleDelay <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(absenceSettleDelay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// candidateOrphanFacts is what the settlement needs of an unknown start, whichever kind it is.
type candidateOrphanFacts struct {
	id         CapacityIdentity
	resource   dataResource
	receipt    string
	fence      int64
	now        int64
	finalCheck func(context.Context) error
}

// loadCandidateOrphan opens a transaction on the unknown start and checks that its window ended. The caller owns
// the transaction.
type loadCandidateOrphan func(ctx context.Context) (*sql.Tx, candidateOrphanFacts, error)

// settleCandidateOrphanLocked runs the settlement with the data execution gate held by the caller.
func (s *Store) settleCandidateOrphanLocked(ctx context.Context, load loadCandidateOrphan, inspector CandidateTargetInspector) (CandidateOrphanReceipt, error) {
	var zero CandidateOrphanReceipt
	if inspector == nil || s.closing.Load() {
		return zero, ErrFenceReconciliation
	}
	tx, before, err := load(ctx)
	if err != nil {
		return zero, err
	}
	tx.Rollback()
	// Native IO happens with no transaction open.
	observed, err := inspector.InspectTarget(ctx, before.resource.id)
	if err != nil {
		return zero, fmt.Errorf("%w: inspect the candidate target: %v", ErrFenceReconciliation, err)
	}
	if observed.Found {
		return zero, ErrCandidateOrphanPresent
	}
	// One absence proves nothing: the daemon can finish a create after the client gave up. A second look, a settle
	// delay later, must agree before anything is released; otherwise the start stays unknown.
	if err := waitAbsenceSettle(ctx); err != nil {
		return zero, fmt.Errorf("%w: wait before the second look: %v", ErrFenceReconciliation, err)
	}
	observed, err = inspector.InspectTarget(ctx, before.resource.id)
	if err != nil {
		return zero, fmt.Errorf("%w: inspect the candidate target again: %v", ErrFenceReconciliation, err)
	}
	if observed.Found {
		return zero, ErrCandidateOrphanPresent
	}
	tx, f, err := load(ctx)
	if err != nil {
		return zero, err
	}
	defer tx.Rollback()
	// With the gate held and nothing recorded since, the start is exactly what the inspection was asked about.
	if f.receipt != before.receipt || f.resource != before.resource || f.id != before.id {
		return zero, fmt.Errorf("%w: the candidate start changed during the inspection", ErrFenceReconciliation)
	}
	now := time.Now().UTC().UnixMilli()
	if now < f.now {
		return zero, ErrFenceReconciliation
	}
	receipt := CandidateOrphanReceipt{
		Format: candidateOrphanReceiptFormat, DeploymentID: f.id.DeploymentID, ControllerEpoch: f.id.ControllerEpoch, InstallationID: f.id.InstallationID,
		OperationID: f.id.OperationID, TargetGeneration: f.id.TargetGeneration, Stage: f.id.Stage, EffectNumber: f.id.EffectNumber, EffectID: f.id.EffectID,
		Kind: f.id.Kind, PayloadSHA256: f.id.PayloadSHA256, ResourceID: f.resource.id, Outcome: CandidateOrphanOutcome, SettledMS: strconv.FormatInt(now, 10),
	}
	encoded, err := json.Marshal(receipt)
	if err != nil || len(encoded) > 4096 || !validCandidateOrphanReceipt(receipt) {
		return zero, ErrFenceReconciliation
	}
	changed, err := tx.ExecContext(ctx, `UPDATE runner_commands SET state='reverted',receipt_json=?,updated_ms=?
		WHERE effect_id=? AND kind='start-candidate' AND stage='start' AND state='unknown' AND receipt_json=? AND ack_attempted_ms IS NULL AND admitted_fence=? AND payload_sha256=?`,
		string(encoded), now, f.id.EffectID, f.receipt, f.fence, f.id.PayloadSHA256)
	if err != nil {
		return zero, err
	}
	if n, err := changed.RowsAffected(); err != nil || n != 1 {
		return zero, fmt.Errorf("%w: settle candidate orphan command rows=%d err=%v", ErrFenceReconciliation, n, err)
	}
	r := f.resource
	changed, err = tx.ExecContext(ctx, `UPDATE runner_resources SET desired_state='absent',observed_state='absent',revision=4,observed_ms=?,updated_ms=?
		WHERE id=? AND kind='container' AND native_id=? AND installation_id=? AND controller_epoch=? AND generation=? AND control_fence=? AND ownership_sha256=?
		AND revision=1 AND desired_state='present' AND observed_state='unknown' AND observed_ms IS NULL`,
		now, now, r.id, r.native, r.installation, r.epoch, r.generation, r.fence, r.ownership)
	if err != nil {
		return zero, err
	}
	if n, err := changed.RowsAffected(); err != nil || n != 1 {
		return zero, fmt.Errorf("%w: settle candidate orphan resource rows=%d err=%v", ErrFenceReconciliation, n, err)
	}
	// The absence is proven in this transaction, so the reservation is released in it.
	if err := releaseHeldReservationExactly(ctx, tx, r.id, now); err != nil {
		return zero, err
	}
	if err := f.finalCheck(ctx); err != nil {
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

// releaseHeldReservationExactly releases the resource's held reservation and requires that exactly that one row
// changed. A resource with no held reservation (budgets were not configured when it was reserved, or it was
// released already) has nothing to release.
func releaseHeldReservationExactly(ctx context.Context, tx *sql.Tx, resourceID string, now int64) error {
	var held int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM runner_reservations WHERE resource_id=? AND state='held'`, resourceID).Scan(&held); err != nil {
		return err
	}
	if held == 0 {
		return nil
	}
	changed, err := tx.ExecContext(ctx, `UPDATE runner_reservations SET state='released',updated_ms=? WHERE resource_id=? AND state='held'`, now, resourceID)
	if err != nil {
		return err
	}
	if n, err := changed.RowsAffected(); err != nil || n != 1 {
		return fmt.Errorf("%w: release the candidate reservation rows=%d err=%v", ErrFenceReconciliation, n, err)
	}
	return nil
}

// SettleCandidateOrphan settles an unknown plain candidate start the runner can prove never ran. It takes the data
// execution gate without waiting: a start that is executing holds it, and nothing is touched then.
func (s *Store) SettleCandidateOrphan(ctx context.Context, raw, signature []byte, inspector CandidateTargetInspector) (CandidateOrphanReceipt, error) {
	if !s.tryAcquireDataExecution() {
		return CandidateOrphanReceipt{}, ErrCandidateOrphanBusy
	}
	defer s.releaseDataExecution()
	return s.settleCandidateOrphanLocked(ctx, s.candidateOrphanLoader(raw, signature), inspector)
}

// SettleWritableCandidateOrphan is SettleCandidateOrphan for a writable candidate start.
func (s *Store) SettleWritableCandidateOrphan(ctx context.Context, raw, signature []byte, inspector CandidateTargetInspector) (CandidateOrphanReceipt, error) {
	if !s.tryAcquireDataExecution() {
		return CandidateOrphanReceipt{}, ErrCandidateOrphanBusy
	}
	defer s.releaseDataExecution()
	return s.settleCandidateOrphanLocked(ctx, s.writableCandidateOrphanLoader(raw, signature), inspector)
}

func (s *Store) candidateOrphanLoader(raw, signature []byte) loadCandidateOrphan {
	return func(ctx context.Context) (*sql.Tx, candidateOrphanFacts, error) {
		var zero candidateOrphanFacts
		tx, f, err := s.beginCandidateCommandReceipt(ctx, raw, signature, "unknown", nil)
		if err != nil {
			return nil, zero, err
		}
		ok := false
		defer func() {
			if !ok {
				tx.Rollback()
			}
		}()
		var marker candidateClaimMarker
		if !canonicalDataJSON(f.receipt, &marker) || !validCandidateMarker(marker) {
			return nil, zero, ErrFenceReconciliation
		}
		resource, err := existingCandidateResource(ctx, tx, f.command)
		if err != nil {
			return nil, zero, err
		}
		reservation, err := reservedCandidate(f, resource)
		if err != nil {
			return nil, zero, err
		}
		if marker.EffectID != f.command.EffectID || marker.ResourceID != reservation.ResourceID || marker.OwnershipSHA256 != reservation.OwnershipSHA256 ||
			marker.DataReceiptSHA256 != f.dataSHA || marker.AdmittedFence != strconv.FormatInt(f.stored.fence, 10) {
			return nil, zero, ErrFenceReconciliation
		}
		// The window of the claim ended, and the envelope that asks was issued after it: the controller itself saw the
		// start expire. Nothing can still claim it, and its in-memory execution deadline has passed.
		expires, _ := strconv.ParseInt(marker.ExpiresMS, 10, 64)
		issued, _ := strconv.ParseInt(f.command.IssuedMS, 10, 64)
		if f.now <= expires || issued <= expires {
			return nil, zero, ErrCandidateOrphanWindow
		}
		ok = true
		return tx, candidateOrphanFacts{
			id: CapacityIdentityOfCommand(candidateIndex(f.command)), resource: *resource, receipt: f.receipt, fence: f.stored.fence, now: f.now,
			finalCheck: func(ctx context.Context) error { return finalCandidateCheck(ctx, f) },
		}, nil
	}
}

func (s *Store) writableCandidateOrphanLoader(raw, signature []byte) loadCandidateOrphan {
	return func(ctx context.Context) (*sql.Tx, candidateOrphanFacts, error) {
		var zero candidateOrphanFacts
		tx, f, err := s.beginWritableCandidateView(ctx, raw, signature, "unknown", nil, writableViewOrphan)
		if err != nil {
			return nil, zero, err
		}
		ok := false
		defer func() {
			if !ok {
				tx.Rollback()
			}
		}()
		var marker writableCandidateClaimMarker
		if !canonicalDataJSON(f.receipt, &marker) || !writableCandidateMarkerIdentityMatches(marker, f) || f.resource == nil {
			return nil, zero, ErrFenceReconciliation
		}
		expires, _ := strconv.ParseInt(marker.ExpiresMS, 10, 64)
		issued, _ := strconv.ParseInt(f.verified.Index().IssuedMS, 10, 64)
		if f.now <= expires || issued <= expires || issued > f.now {
			return nil, zero, ErrCandidateOrphanWindow
		}
		ok = true
		return tx, candidateOrphanFacts{
			id: CapacityIdentityOfCommand(candidateIndex(f.verified.Index())), resource: *f.resource, receipt: f.receipt, fence: f.stored.fence, now: f.now,
			finalCheck: func(ctx context.Context) error { return finalWritableCandidateCheck(ctx, f) },
		}, nil
	}
}

// candidateInspectorOf adapts the concrete native runtime to the settlement's seam. A nil runtime is a nil seam, never
// a non-nil interface holding a nil pointer.
func candidateInspectorOf(native *driver.CandidateRuntime) CandidateTargetInspector {
	if native == nil {
		return nil
	}
	return native
}
