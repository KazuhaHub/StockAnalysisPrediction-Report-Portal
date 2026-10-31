package journal

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/driver"
)

type updateStartRemovalFacts struct {
	verified       control.VerifiedUpdateStartRemovalCommand
	start          updateStartFacts
	running        UpdateStartRunningReceipt
	request        driver.UpdateStartLaunchRequest
	expected       driver.UpdateStartLaunchSnapshot
	resource       *dataResource
	row            admittedCommand
	raw, signature []byte
	now            int64
}

func updateStartRemovalIndex(v control.VerifiedUpdateStartRemovalCommand) control.Command {
	command, start := v.Command(), v.Start().Command()
	return control.Command{
		Format:       control.UpdateStartRemovalFormat,
		DeploymentID: start.DeploymentID, ControllerEpoch: start.ControllerEpoch,
		ControlRevision: start.ControlRevision,
		InstallationID:  start.InstallationID, OperationID: command.OperationID,
		TargetGeneration: start.TargetGeneration,
		Stage:            command.Stage,
		EffectNumber:     "1", EffectID: command.EffectID,
		Kind:              map[string]string{"stop": "stop", "remove_code": "remove-code"}[command.Stage],
		InstallationFence: command.InstallationFence,
		PayloadSHA256:     command.PayloadSHA256,
		IssuedMS:          start.IssuedMS, ExpiresMS: start.ExpiresMS,
		Payload: control.PrepareDataPayload{
			DataNamespaceID: start.Payload.NamespaceID,
			ReleaseSHA256:   start.Payload.TargetReleaseSHA256,
			DataFormat:      start.Payload.DataFormat,
			WriterFence:     start.Payload.TargetWriterFence,
		},
	}
}

func loadUpdateStartRemovalSource(ctx context.Context, tx *sql.Tx, verified control.VerifiedUpdateStartRemovalCommand, state string, now int64) (updateStartRemovalFacts, error) {
	var result updateStartRemovalFacts
	command, startCommand := verified.Command(), verified.Start().Command()
	index := updateStartIndex(startCommand)
	fence, _ := strconv.ParseInt(command.InstallationFence, 10, 64)
	stored, err := loadAdmittedCommand(ctx, tx, startCommand.EffectID)
	if err != nil || stored.state != "applied" ||
		!sameLogicalCommand(stored, index) {
		return result, ErrFenceReconciliation
	}
	floor, err := commandResourceFloor(ctx, tx, index)
	if err != nil || fence < floor || fence < stored.fence {
		return result, ErrFenceReconciliation
	}
	source, err := loadUpdateStartSourceExcludingRegistration(
		ctx, tx, startCommand, now, command.EffectID)
	if err != nil {
		return result, err
	}
	start := updateStartFacts{
		command: startCommand, stored: stored, source: source, now: now,
	}
	var raw string
	var ack sql.NullInt64
	var created, updated int64
	if err := tx.QueryRowContext(ctx,
		`SELECT receipt_json,ack_attempted_ms,created_ms,updated_ms FROM runner_commands WHERE effect_id=?`,
		startCommand.EffectID).
		Scan(&raw, &ack, &created, &updated); err != nil ||
		ack.Valid || created < 0 || updated < created || updated > now {
		return result, ErrFenceReconciliation
	}
	start.receipt, start.created, start.updated = raw, created, updated
	running, err := ParseUpdateStartRunningReceipt([]byte(raw))
	if err != nil || candidateHash([]byte(raw)) != command.RunningReceiptSHA256 {
		return result, ErrFenceReconciliation
	}
	resource, err := existingCandidateResourcePrefix(
		ctx, tx, index, updateStartPendingPrefix, startCommand.Payload.ResourceID)
	if err != nil || !updateStartRemovalResourceMatches(
		start, running, resource, command.Stage, state) {
		return result, ErrFenceReconciliation
	}
	request, expected, err := updateStartExpectedSnapshot(start, running.Claim, nil)
	if err != nil {
		return result, ErrFenceReconciliation
	}
	result = updateStartRemovalFacts{
		verified: verified, start: start, running: running,
		request: request, expected: expected, resource: resource, now: now,
	}
	return result, nil
}

func beginUpdateStartRemoval(ctx context.Context, store *Store, raw, signature []byte) (*sql.Tx, updateStartRemovalFacts, error) {
	var zero updateStartRemovalFacts
	if store.closing.Load() || len(raw) == 0 ||
		len(raw) > control.MaxCommandBytes || len(signature) != 64 {
		return nil, zero, ErrFenceReconciliation
	}
	raw, signature = bytes.Clone(raw), bytes.Clone(signature)
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, zero, err
	}
	ok := false
	defer func() {
		if !ok {
			tx.Rollback()
		}
	}()
	if _, err := tx.ExecContext(ctx,
		`UPDATE runner_deployment SET revision=revision WHERE singleton=1`); err != nil {
		return nil, zero, err
	}
	deployment, err := readControllerDeployment(ctx, tx)
	if err != nil {
		return nil, zero, err
	}
	authority := control.Authority{
		DeploymentID:    deployment.DeploymentID,
		ControllerEpoch: deployment.ControllerEpoch,
		ControlRevision: deployment.ControlRevision,
		PublicKey:       deployment.PublicKey, Fingerprint: deployment.Fingerprint,
	}
	now := time.Now().UTC().UnixMilli()
	verified, err := control.VerifyUpdateStartRemoval(
		raw, signature, authority, now)
	if err != nil {
		return nil, zero, err
	}
	command := verified.Command()
	index := updateStartRemovalIndex(verified)
	row, err := loadAdmittedCommand(ctx, tx, index.EffectID)
	if err == nil {
		if !sameLogicalCommand(row, index) ||
			strconv.FormatInt(row.fence, 10) != command.InstallationFence ||
			(row.state != "prepared" && row.state != "unknown" &&
				row.state != "applied") {
			return nil, zero, ErrFenceReconciliation
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return nil, zero, err
	}
	state := row.state
	if state == "" {
		state = "prepared"
	}
	facts, err := loadUpdateStartRemovalSource(
		ctx, tx, verified, state, now)
	if err != nil {
		return nil, zero, err
	}
	facts.row, facts.raw, facts.signature = row, raw, signature
	if _, err := control.VerifyUpdateStartRemoval(
		raw, signature, authority, time.Now().UnixMilli()); err != nil {
		return nil, zero, err
	}
	ok = true
	return tx, facts, nil
}

func (store *Store) DispatchUpdateStartRemoval(ctx context.Context, raw, signature []byte, native *driver.CandidateRuntime) (ManagedRemovalReceipt, error) {
	var zero ManagedRemovalReceipt
	if native == nil || store.closing.Load() {
		return zero, ErrManagedRemovalUnavailable
	}
	tx, facts, err := beginUpdateStartRemoval(
		ctx, store, raw, signature)
	if err != nil {
		return zero, err
	}
	command := facts.verified.Command()
	if facts.row.effectID != "" {
		switch facts.row.state {
		case "applied":
			tx.Rollback()
			return store.ReadUpdateStartRemovalReceipt(ctx, raw, signature)
		case "unknown":
			tx.Rollback()
			return zero, ErrManagedRemovalUnavailable
		case "prepared":
		default:
			tx.Rollback()
			return zero, ErrFenceReconciliation
		}
		changed, err := tx.ExecContext(ctx,
			`UPDATE runner_commands SET state='unknown',updated_ms=? WHERE effect_id=? AND state='prepared' AND receipt_json='{}' AND ack_attempted_ms IS NULL`,
			facts.now, command.EffectID)
		if err != nil {
			tx.Rollback()
			return zero, err
		}
		if n, err := changed.RowsAffected(); err != nil || n != 1 {
			tx.Rollback()
			return zero, ErrFenceReconciliation
		}
	} else {
		index := updateStartRemovalIndex(facts.verified)
		fence, _ := strconv.ParseInt(command.InstallationFence, 10, 64)
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO runner_commands(effect_id,operation_id,installation_id,stage,effect_number,kind,payload_sha256,target_generation,controller_epoch,admitted_fence,state,receipt_json,ack_attempted_ms,created_ms,updated_ms)
			VALUES(?,?,?,?,?,?,?,?,?,?,'unknown','{}',NULL,?,?)`,
			command.EffectID, command.OperationID,
			facts.start.command.InstallationID, command.Stage,
			"1", index.Kind, command.PayloadSHA256,
			facts.start.command.TargetGeneration,
			facts.start.command.ControllerEpoch, fence,
			facts.now, facts.now); err != nil {
			tx.Rollback()
			return zero, err
		}
	}
	if err := tx.Commit(); err != nil {
		return zero, err
	}
	launch, err := native.PrepareUpdateStartRemoval(ctx, facts.request)
	if err != nil {
		return zero, errors.Join(ErrManagedRemovalUnavailable, err)
	}
	defer launch.Close()
	snapshot, err := launch.Snapshot()
	if err != nil || snapshot != facts.expected {
		return zero, errors.Join(ErrFenceReconciliation, err)
	}
	var observed driver.CandidateRemovalObservation
	if command.Stage == "stop" {
		observed, err = native.StopUpdateStart(
			ctx, launch, facts.running.NativeID)
	} else {
		observed, err = native.RemoveUpdateStart(
			ctx, launch, facts.running.NativeID)
	}
	if err != nil {
		return zero, errors.Join(ErrManagedRemovalUnavailable, err)
	}
	return store.settleUpdateStartRemoval(
		ctx, raw, signature, facts, observed)
}

func (store *Store) settleUpdateStartRemoval(ctx context.Context, raw, signature []byte, before updateStartRemovalFacts, observation driver.CandidateRemovalObservation) (ManagedRemovalReceipt, error) {
	var zero ManagedRemovalReceipt
	snapshot, err := observation.Snapshot()
	if err != nil || snapshot.Launch != before.expected.Launch ||
		snapshot.NativeID != before.running.NativeID {
		return zero, ErrFenceReconciliation
	}
	command := before.verified.Command()
	expectedState := "stopped"
	nextRevision := int64(3)
	nextDesired := "stopped"
	if command.Stage == "remove_code" {
		expectedState = "absent"
		nextRevision = 4
		nextDesired = "absent"
	}
	if snapshot.State != expectedState {
		return zero, ErrFenceReconciliation
	}
	tx, current, err := beginUpdateStartRemoval(
		ctx, store, raw, signature)
	if err != nil {
		return zero, err
	}
	defer tx.Rollback()
	if current.row.state != "unknown" ||
		current.running.NativeID != before.running.NativeID {
		return zero, ErrFenceReconciliation
	}
	now := time.Now().UTC().UnixMilli()
	changed, err := tx.ExecContext(ctx,
		`UPDATE runner_resources SET desired_state=?,observed_state=?,revision=?,observed_ms=?,updated_ms=?
		WHERE id=? AND kind='container' AND native_id=? AND revision=2
		AND desired_state='present' AND observed_state='running'`,
		nextDesired, expectedState, nextRevision,
		snapshot.ObservedMS, now, current.start.command.Payload.ResourceID,
		current.running.NativeID)
	if err != nil {
		return zero, err
	}
	if n, err := changed.RowsAffected(); err != nil || n != 1 {
		return zero, ErrFenceReconciliation
	}
	start := current.verified.Start().Command()
	receipt := ManagedRemovalReceipt{
		Format:       managedRemovalReceiptFormat,
		DeploymentID: start.DeploymentID, ControllerEpoch: start.ControllerEpoch,
		InstallationID:    start.InstallationID,
		OperationID:       command.OperationID,
		SourceOperationID: start.OperationID, SourceEffectID: start.EffectID,
		TargetGeneration: start.TargetGeneration,
		Stage:            command.Stage, EffectID: command.EffectID,
		PayloadSHA256:       command.PayloadSHA256,
		SourceReceiptSHA256: command.RunningReceiptSHA256,
		ResourceID:          start.Payload.ResourceID,
		DataNamespaceID:     start.Payload.NamespaceID,
		ReleaseSHA256:       start.Payload.TargetReleaseSHA256,
		NativeID:            snapshot.NativeID, State: expectedState,
		ObservedMS: strconv.FormatInt(snapshot.ObservedMS, 10),
		SettledMS:  strconv.FormatInt(now, 10),
	}
	encoded, err := json.Marshal(receipt)
	if err != nil || len(encoded) > 16384 {
		return zero, ErrFenceReconciliation
	}
	if _, err := ParseManagedRemovalReceipt(encoded); err != nil {
		return zero, err
	}
	changed, err = tx.ExecContext(ctx,
		`UPDATE runner_commands SET state='applied',receipt_json=?,updated_ms=? WHERE effect_id=? AND state='unknown' AND receipt_json='{}' AND ack_attempted_ms IS NULL`,
		string(encoded), now, command.EffectID)
	if err != nil {
		return zero, err
	}
	if n, err := changed.RowsAffected(); err != nil || n != 1 {
		return zero, ErrFenceReconciliation
	}
	if err := tx.Commit(); err != nil {
		return zero, err
	}
	return receipt, nil
}

func (store *Store) ReadUpdateStartRemovalReceipt(ctx context.Context, raw, signature []byte) (ManagedRemovalReceipt, error) {
	tx, facts, err := beginUpdateStartRemoval(
		ctx, store, raw, signature)
	if err != nil {
		return ManagedRemovalReceipt{}, err
	}
	defer tx.Rollback()
	if facts.row.state != "applied" {
		return ManagedRemovalReceipt{}, ErrFenceReconciliation
	}
	var receiptRaw string
	if err := tx.QueryRowContext(ctx,
		`SELECT receipt_json FROM runner_commands WHERE effect_id=?`,
		facts.row.effectID).Scan(&receiptRaw); err != nil {
		return ManagedRemovalReceipt{}, err
	}
	receipt, err := ParseManagedRemovalReceipt([]byte(receiptRaw))
	if err != nil || !updateStartRemovalReceiptMatchesCommand(
		receipt, facts.verified.Command()) {
		return ManagedRemovalReceipt{}, ErrFenceReconciliation
	}
	return receipt, nil
}

func updateStartRemovalResourceMatches(start updateStartFacts, running UpdateStartRunningReceipt, resource *dataResource, stage, state string) bool {
	if resource == nil {
		return false
	}
	command := start.command
	if resource.id != command.Payload.ResourceID ||
		!resource.namespace.Valid ||
		resource.namespace.String != command.Payload.NamespaceID ||
		resource.kind != "container" ||
		resource.native != running.NativeID ||
		!resource.release.Valid ||
		resource.release.String != command.Payload.TargetReleaseSHA256 ||
		resource.generation != mustDataInt(command.TargetGeneration) ||
		resource.epoch != command.ControllerEpoch ||
		resource.fence != start.stored.fence ||
		resource.writer != mustDataInt(command.Payload.TargetWriterFence) ||
		resource.ownership != running.Claim.OwnershipSHA256 {
		return false
	}
	if stage == "stop" {
		if state == "applied" {
			return resource.desired == "stopped" &&
				resource.observed == "stopped" && resource.revision == 3 &&
				resource.observedMS.Valid
		}
		return resource.desired == "present" && resource.observed == "running" &&
			resource.revision == 2 && resource.observedMS.Valid &&
			resource.observedMS.Int64 == mustDataInt(running.NativeObservedMS) &&
			resource.updated == mustDataInt(running.SettledMS)
	}
	if stage != "remove_code" || !resource.observedMS.Valid {
		return false
	}
	if state == "applied" {
		return resource.desired == "absent" &&
			resource.observed == "absent" && resource.revision == 4
	}
	return resource.desired == "stopped" &&
		resource.observed == "stopped" && resource.revision == 3
}

func updateStartRemovalReceiptMatchesCommand(receipt ManagedRemovalReceipt, command control.UpdateStartRemovalCommand) bool {
	return receipt.OperationID == command.OperationID &&
		receipt.Stage == command.Stage &&
		receipt.EffectID == command.EffectID &&
		receipt.PayloadSHA256 == command.PayloadSHA256 &&
		receipt.SourceReceiptSHA256 == command.RunningReceiptSHA256
}
