package app

import (
	"database/sql"
	"errors"
	"fmt"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/journal"
)

// platformRestoreStartRow is the start effect row of a managed restore, as loadPlatformSettlementStart read it.
type platformRestoreStartRow struct {
	effectID, kind, digest, epoch, state, receipt string
	admitted, observed                            int64
}

// loadPlatformRestoreSettlementStart rebuilds the restore-start command of a finished roll back (a managed restore
// that is not a recovery resume) from the evidence it was built from, at the fence its effect was admitted under, and
// proves it is the very command the effect row records. It is the restore counterpart of the update branch of
// loadPlatformSettlementStart, and it signs and writes nothing. A recovery resume is read through the recovery
// reconciliation (loadPlatformRecoveryStartFacts) instead and is refused here, because the imported checkpoint it
// starts from is not this operation's snapshot.
func loadPlatformRestoreSettlementStart(e migExec, lease platformOperationLease, runtime platformRuntimeRegistration, now, completed int64, row platformRestoreStartRow) (platformSettlementStartRead, error) {
	var zero platformSettlementStartRead
	fail := func(reason string) (platformSettlementStartRead, error) {
		return zero, errors.Join(errAppPlatformStageEvidence, errors.New("settlement restore start "+reason))
	}
	if resume, err := platformOperationIsResume(e, lease.id); err != nil {
		return zero, err
	} else if resume {
		return fail("is a recovery resume")
	}
	if row.kind != "restore-start" || row.epoch != lease.controllerEpoch || row.admitted < 1 || row.admitted > lease.fence ||
		(row.state != "prepared" && row.state != "unknown" && row.state != "applied") {
		return fail("effect row")
	}
	var reservation *journal.UpdateStartReservation
	if row.state == "applied" {
		if row.observed != row.admitted {
			return fail("effect row")
		}
	} else if row.receipt != "{}" {
		parsed, err := journal.ParseUpdateStartReservation([]byte(row.receipt))
		if err != nil || row.observed != row.admitted {
			return fail("reservation")
		}
		reservation = &parsed
	} else if row.observed != 0 {
		return fail("effect row")
	}
	readLease := lease
	readLease.fence = row.admitted
	// The roll back has ended, so none of the installation's operations is in flight any more.
	source, err := loadPlatformCheckpointSourceMode(e, readLease, runtime, now, false, 0)
	if err != nil {
		return zero, fmt.Errorf("settlement restore start source: %w", err)
	}
	recoverySetID, _, checkpoint, _, err := loadPlatformRestoreCheckpoint(e, readLease)
	if err != nil || recoverySetID == "" {
		return fail("checkpoint")
	}
	checkpointRaw, err := loadPlatformRestoreCheckpointRaw(e, readLease, checkpoint)
	if err != nil {
		return zero, fmt.Errorf("settlement restore start checkpoint receipt: %w", err)
	}
	var activation, namespace, activationState string
	var format int64
	if err := e.queryRow(`SELECT id,data_namespace_id,data_format,state FROM app_platform_activations WHERE installation_id=? AND generation=?`,
		lease.installationID, lease.snapshot.Generation).Scan(&activation, &namespace, &format, &activationState); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fail("activation")
		}
		return zero, err
	}
	if activationState != "candidate" && !(row.state == "applied" && activationState == "active") || namespace != checkpoint.NamespaceID ||
		format != mustPlatformCheckpointInt(checkpoint.DataFormat) {
		return fail("activation")
	}
	var policy int64
	if err := e.queryRow(`SELECT policy_revision FROM app_platform_installations WHERE id=? AND desired_state='enabled' AND phase='recovery_required'`,
		lease.installationID).Scan(&policy); err != nil || policy < 1 {
		return fail("installation")
	}
	command, err := platformRestoreStartCommand(readLease, runtime, source, checkpoint, checkpointRaw, activation, row.effectID, policy)
	if err != nil || command.PayloadSHA256 != row.digest || (reservation != nil && !platformUpdateStartReservationMatches(*reservation, command, activation, row.effectID)) {
		return fail("command")
	}
	intent := platformUpdateStartIntent{
		resourceID: activation, effectID: row.effectID, runtime: runtime, authority: platformImportAuthority(runtime),
		command: command, checkpoint: checkpoint, checkpointRaw: checkpointRaw, source: source,
		plan:             platformRestoreStartPlan(readLease, checkpoint, recoverySetID, policy, source),
		targetDescriptor: append([]byte(nil), source.descriptor...), reservation: reservation,
	}
	return platformSettlementStartRead{intent: intent, reserved: reservation != nil, admitted: row.admitted,
		definiteAfter: completed + platformSigningSlackMS + platformStartCommandWindowMS + platformImportAbsenceMarginMS + platformCreateGraceMS + control.CommandClockSkewMS}, nil
}
