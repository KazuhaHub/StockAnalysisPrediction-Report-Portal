package journal

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
)

// Removing a target the runner reserved and never created.
//
// A start the update ended with after the runner reserved it holds a pending target resource and no container. The
// ordinary removal needs a running receipt to bind, so this one binds the signed start and the sentinel
// control.ReservedStartReceiptSHA256 in its place. It runs under the data execution gate, taken without waiting: a
// start that is executing holds the gate, and nothing is touched then. With the gate held no native create can race
// it; it proves, by name and by label, that no container exists for the resource, and in one transaction marks the
// resource and settles the start as not run, whether it was still prepared or claimed and orphaned. It never resends
// the start.

// TargetAbsenceProver proves that no container exists for a resource, by name and by label.
type TargetAbsenceProver interface {
	ProveTargetAbsent(ctx context.Context, resourceID string) error
}

const updateStartNotRunFormat = "portal.runner.update-start-not-run/1-dev.1"

type updateStartNotRun struct {
	Format    string `json:"format"`
	Outcome   string `json:"outcome"`
	EffectID  string `json:"effect_id"`
	SettledMS string `json:"settled_ms"`
}

type reservedRemovalFacts struct {
	verified control.VerifiedUpdateStartRemovalCommand
	start    admittedCommand
	row      admittedCommand
	resource *dataResource
	now      int64
}

// loadReservedRemoval verifies the command and what it removes. The resource must be the start's own pending target
// (stop) or the stopped one the stop left (remove_code), and the start must be one the runner holds without a
// running receipt.
func loadReservedRemoval(ctx context.Context, tx *sql.Tx, raw, signature []byte) (reservedRemovalFacts, error) {
	var zero reservedRemovalFacts
	fail := func(reason string) (reservedRemovalFacts, error) {
		return zero, fmt.Errorf("%w: reserved start removal %s", ErrFenceReconciliation, reason)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE runner_deployment SET revision=revision WHERE singleton=1`); err != nil {
		return zero, err
	}
	deployment, err := readControllerDeployment(ctx, tx)
	if err != nil {
		return zero, err
	}
	authority := control.Authority{
		DeploymentID: deployment.DeploymentID, ControllerEpoch: deployment.ControllerEpoch,
		ControlRevision: deployment.ControlRevision, PublicKey: deployment.PublicKey, Fingerprint: deployment.Fingerprint,
	}
	now := time.Now().UTC().UnixMilli()
	verified, err := control.VerifyUpdateStartRemoval(raw, signature, authority, now)
	if err != nil {
		return zero, err
	}
	command, startCommand := verified.Command(), verified.Start().Command()
	if command.RunningReceiptSHA256 != control.ReservedStartReceiptSHA256 || startCommand.Kind != "update-start" {
		return fail("is not for a reserved update-start")
	}
	fence, _ := strconv.ParseInt(command.InstallationFence, 10, 64)
	start, err := loadAdmittedCommand(ctx, tx, startCommand.EffectID)
	if err != nil || !sameLogicalCommand(start, updateStartIndex(startCommand)) {
		return fail("start is not held")
	}
	floor, err := commandFenceFloor(ctx, tx, updateStartIndex(startCommand))
	if err != nil || fence < floor || fence < start.fence {
		return fail("fence")
	}
	index := updateStartRemovalIndex(verified)
	row, err := loadAdmittedCommand(ctx, tx, index.EffectID)
	if err == nil {
		if !sameLogicalCommand(row, index) || strconv.FormatInt(row.fence, 10) != command.InstallationFence || (row.state != "unknown" && row.state != "applied") {
			return fail("removal row")
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return zero, err
	}
	resource, err := existingCandidateResourcePrefix(ctx, tx, updateStartIndex(startCommand), updateStartPendingPrefix, startCommand.Payload.ResourceID)
	if err != nil || resource == nil {
		return fail("resource")
	}
	p := startCommand.Payload
	if resource.id != p.ResourceID || resource.installation != startCommand.InstallationID || !resource.namespace.Valid || resource.namespace.String != p.NamespaceID ||
		resource.kind != "container" || resource.native != updateStartPendingPrefix+p.ResourceID || !resource.release.Valid || resource.release.String != p.TargetReleaseSHA256 ||
		resource.generation != mustDataInt(startCommand.TargetGeneration) || resource.epoch != startCommand.ControllerEpoch || resource.fence != start.fence ||
		resource.writer != mustDataInt(p.TargetWriterFence) || resource.ownership != updateStartOwnership(startCommand, start.fence) {
		return fail("resource identity")
	}
	switch command.Stage {
	case "stop":
		// Only a start that was never claimed is reserved. A claimed one may have a container, which only the
		// settlement of an orphan can account for.
		held := start.state == "prepared" || (row.state == "applied" && start.state == "reverted")
		pending := resource.desired == "present" && resource.observed == "unknown" && resource.revision == 1 && !resource.observedMS.Valid
		stopped := row.state == "applied" && resource.desired == "stopped" && resource.observed == "stopped" && resource.revision == 3
		if !held || !(pending || stopped) {
			if row.state == "" && start.state != "prepared" {
				return zero, fmt.Errorf("%w: the start is %s", ErrUpdateStartNotReserved, start.state)
			}
			return fail("start or resource state at stop")
		}
	case "remove_code":
		stopped := resource.desired == "stopped" && resource.observed == "stopped" && resource.revision == 3
		removed := row.state == "applied" && resource.desired == "absent" && resource.observed == "absent" && resource.revision == 4
		if start.state != "reverted" || !(stopped || removed) {
			return fail("start or resource state at remove")
		}
	default:
		return fail("stage")
	}
	return reservedRemovalFacts{verified: verified, start: start, row: row, resource: resource, now: now}, nil
}

// RemoveReservedUpdateStart removes the pending target of a start the runner reserved and never ran, one stage at a
// time, and settles the start as not run. It reports the managed-removal receipt of the stage.
func (s *Store) RemoveReservedUpdateStart(ctx context.Context, raw, signature []byte, prover TargetAbsenceProver) (ManagedRemovalReceipt, error) {
	var zero ManagedRemovalReceipt
	if prover == nil || s.closing.Load() || len(raw) == 0 || len(raw) > control.MaxCommandBytes || len(signature) != 64 {
		return zero, ErrManagedRemovalUnavailable
	}
	// A start that is executing holds the gate. Waiting for it would hand the removal a start that may then run.
	if !s.tryAcquireDataExecution() {
		return zero, fmt.Errorf("%w: the data execution gate is held", ErrManagedRemovalUnavailable)
	}
	defer s.releaseDataExecution()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return zero, err
	}
	defer tx.Rollback()
	facts, err := loadReservedRemoval(ctx, tx, raw, signature)
	if err != nil {
		return zero, err
	}
	if facts.row.state == "applied" {
		return readReservedRemovalReceipt(ctx, tx, facts)
	}
	// The proof changes nothing, so nothing is claimed beforehand: the outcome is written in one transaction after it,
	// and a failed proof leaves no row behind.
	tx.Rollback()
	if err := prover.ProveTargetAbsent(ctx, facts.resource.id); err != nil {
		return zero, fmt.Errorf("%w: prove the reserved target absent: %v", ErrManagedRemovalUnavailable, err)
	}
	return s.settleReservedRemoval(ctx, raw, signature)
}

func (s *Store) settleReservedRemoval(ctx context.Context, raw, signature []byte) (ManagedRemovalReceipt, error) {
	var zero ManagedRemovalReceipt
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return zero, err
	}
	defer tx.Rollback()
	facts, err := loadReservedRemoval(ctx, tx, raw, signature)
	if err != nil {
		return zero, err
	}
	if facts.row.effectID != "" {
		return zero, fmt.Errorf("%w: reserved start removal settlement state %q", ErrFenceReconciliation, facts.row.state)
	}
	command, start := facts.verified.Command(), facts.verified.Start().Command()
	now := time.Now().UTC().UnixMilli()
	state := "stopped"
	var changed sql.Result
	if command.Stage == "stop" {
		// The start never ran: it is closed as not run in the same transaction that stops its resource.
		notRun, _ := json.Marshal(updateStartNotRun{Format: updateStartNotRunFormat, Outcome: "never_ran", EffectID: start.EffectID, SettledMS: strconv.FormatInt(now, 10)})
		changed, err = tx.ExecContext(ctx, `UPDATE runner_commands SET state='reverted',receipt_json=?,updated_ms=? WHERE effect_id=? AND state IN ('prepared','unknown') AND ack_attempted_ms IS NULL`,
			string(notRun), now, start.EffectID)
		if err != nil {
			return zero, err
		}
		if n, err := changed.RowsAffected(); err != nil || n != 1 {
			return zero, fmt.Errorf("%w: reserved start settlement rows=%d err=%v", ErrFenceReconciliation, n, err)
		}
		changed, err = tx.ExecContext(ctx, `UPDATE runner_resources SET desired_state='stopped',observed_state='stopped',revision=3,observed_ms=?,updated_ms=?
			WHERE id=? AND kind='container' AND native_id=? AND revision=1 AND desired_state='present' AND observed_state='unknown' AND observed_ms IS NULL`,
			now, now, facts.resource.id, updateStartPendingPrefix+start.Payload.ResourceID)
	} else {
		state = "absent"
		changed, err = tx.ExecContext(ctx, `UPDATE runner_resources SET desired_state='absent',observed_state='absent',revision=4,observed_ms=?,updated_ms=?
			WHERE id=? AND kind='container' AND native_id=? AND revision=3 AND desired_state='stopped' AND observed_state='stopped'`,
			now, now, facts.resource.id, updateStartPendingPrefix+start.Payload.ResourceID)
		if err == nil {
			// Proven absence is recorded in this transaction, so the reservation is released in it.
			err = releaseReservation(ctx, tx, facts.resource.id, now)
		}
	}
	if err != nil {
		return zero, err
	}
	if n, err := changed.RowsAffected(); err != nil || n != 1 {
		return zero, fmt.Errorf("%w: reserved start resource rows=%d err=%v", ErrFenceReconciliation, n, err)
	}
	receipt := ManagedRemovalReceipt{
		Format: managedRemovalReceiptFormat, DeploymentID: start.DeploymentID, ControllerEpoch: start.ControllerEpoch,
		InstallationID: start.InstallationID, OperationID: command.OperationID, SourceOperationID: start.OperationID, SourceEffectID: start.EffectID,
		TargetGeneration: start.TargetGeneration, Stage: command.Stage, EffectID: command.EffectID, PayloadSHA256: command.PayloadSHA256,
		SourceReceiptSHA256: command.RunningReceiptSHA256, ResourceID: start.Payload.ResourceID, DataNamespaceID: start.Payload.NamespaceID,
		ReleaseSHA256: start.Payload.TargetReleaseSHA256, NativeID: control.ReservedStartNativeID(start.Payload.ResourceID), State: state,
		ObservedMS: strconv.FormatInt(now, 10), SettledMS: strconv.FormatInt(now, 10),
	}
	encoded, err := json.Marshal(receipt)
	if err != nil || len(encoded) > 16384 {
		return zero, fmt.Errorf("%w: reserved start removal receipt", ErrFenceReconciliation)
	}
	if _, err := ParseManagedRemovalReceipt(encoded); err != nil {
		return zero, err
	}
	index := updateStartRemovalIndex(facts.verified)
	fence, _ := strconv.ParseInt(command.InstallationFence, 10, 64)
	if _, err := tx.ExecContext(ctx, `INSERT INTO runner_commands(effect_id,operation_id,installation_id,stage,effect_number,kind,payload_sha256,target_generation,controller_epoch,admitted_fence,state,receipt_json,ack_attempted_ms,created_ms,updated_ms)
		VALUES(?,?,?,?,?,?,?,?,?,?,'applied',?,NULL,?,?)`,
		command.EffectID, command.OperationID, start.InstallationID, command.Stage, "1", index.Kind, command.PayloadSHA256,
		start.TargetGeneration, start.ControllerEpoch, fence, string(encoded), now, now); err != nil {
		return zero, err
	}
	if err := tx.Commit(); err != nil {
		return zero, err
	}
	return receipt, nil
}

func readReservedRemovalReceipt(ctx context.Context, tx *sql.Tx, facts reservedRemovalFacts) (ManagedRemovalReceipt, error) {
	var raw string
	if err := tx.QueryRowContext(ctx, `SELECT receipt_json FROM runner_commands WHERE effect_id=?`, facts.row.effectID).Scan(&raw); err != nil {
		return ManagedRemovalReceipt{}, err
	}
	receipt, err := ParseManagedRemovalReceipt([]byte(raw))
	if err != nil || !updateStartRemovalReceiptMatchesCommand(receipt, facts.verified.Command()) {
		return ManagedRemovalReceipt{}, ErrFenceReconciliation
	}
	return receipt, nil
}

// ReadReservedUpdateStartRemovalReceipt answers a read of a reserved-start removal that was applied.
func (s *Store) ReadReservedUpdateStartRemovalReceipt(ctx context.Context, raw, signature []byte) (ManagedRemovalReceipt, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ManagedRemovalReceipt{}, err
	}
	defer tx.Rollback()
	facts, err := loadReservedRemoval(ctx, tx, raw, signature)
	if err != nil {
		return ManagedRemovalReceipt{}, err
	}
	if facts.row.state != "applied" {
		return ManagedRemovalReceipt{}, ErrFenceReconciliation
	}
	return readReservedRemovalReceipt(ctx, tx, facts)
}
