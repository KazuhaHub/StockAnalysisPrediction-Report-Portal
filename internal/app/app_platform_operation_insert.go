package app

import (
	"fmt"
	"strings"
)

// platformOperationRow is every value an application operation is admitted with. The columns an admission never
// varies (a fresh row has no lease and fence 0, no error code, and one row revision) are not here.
type platformOperationRow struct {
	ID, InstallationID, ReviewPlanID, ActorScope string
	IdempotencySHA, RequestSHA, Action           string
	Managed                                      bool
	SourceGeneration, TargetGeneration           int64
	Stage                                        string
	StateRevision                                int64
	ControllerEpoch                              string
	// Completed admits an operation that is already complete (a recovery marker or a Portal-side uninstall).
	Completed bool
	NowMS     int64
}

// insertPlatformOperation is the only writer of app_platform_operations. Its installation is explicit, and it
// refuses, in the same transaction as the insert, an installation that holds a load-image effect with no verdict:
// the load may still run on the runner or run later under its id, and any new operation would move the
// installation's fence past the one the settlement is bound to. The guard test
// TestNoCodeWritesTheOperationsTableOutsideTheInsertHelper keeps it the only writer.
func insertPlatformOperation(e migExec, row platformOperationRow) error {
	if !platformValidUUID(row.InstallationID) {
		return errAppPlatformStageEvidence
	}
	if platformStartsBackend(row.Action) {
		if err := platformRefuseWhileLoadUnsettled(e, row.InstallationID); err != nil {
			return err
		}
	}
	var completed any
	if row.Completed {
		completed = row.NowMS
	}
	_, err := e.exec(`INSERT INTO app_platform_operations(
		id,installation_id,review_plan_id,actor_scope,idempotency_sha256,request_sha256,action,managed,
		source_generation,target_generation,stage,state_revision,row_revision,lease_owner,lease_until_ms,fence,
		controller_epoch,error_code,completed_ms,created_ms,updated_ms)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,1,NULL,NULL,0,?,NULL,?,?,?)`,
		row.ID, row.InstallationID, row.ReviewPlanID, row.ActorScope, row.IdempotencySHA, row.RequestSHA, row.Action,
		boolInt(row.Managed), row.SourceGeneration, row.TargetGeneration, row.Stage, row.StateRevision,
		row.ControllerEpoch, completed, row.NowMS, row.NowMS)
	return err
}

// platformStartBearingActions are the operation kinds that start a backend, and so need the release image on the
// host: install, update (which also carries retry and roll back), enable, repair and restore. Only these are
// refused while a load has no verdict. Every other kind is always admitted: disable, uninstall, delete_data,
// restore_import, and the trust quarantine with its stop. That is safe because the runner's readers ignore load
// rows and a load touches only the daemon's image storage, never data, fences or containers; and it is necessary
// because a trust quarantine must never be deferred. platformNonStartActions is the rest; a test requires every
// operation kind to be in exactly one of the two lists.
var platformStartBearingActions = []string{"install", "update", "enable", "repair", "restore"}

var platformNonStartActions = []string{"disable", "uninstall", "delete_data", "restore_import"}

func platformStartsBackend(action string) bool {
	for _, a := range platformStartBearingActions {
		if a == action {
			return true
		}
	}
	return false
}

// platformStartBearingSQL is the action list as an SQL set, for queries that must agree with the list above.
func platformStartBearingSQL() string {
	quoted := make([]string, len(platformStartBearingActions))
	for i, a := range platformStartBearingActions {
		quoted[i] = "'" + a + "'"
	}
	return "(" + strings.Join(quoted, ",") + ")"
}

// platformRefuseWhileLoadUnsettled refuses a new operation for an installation that holds a load with no verdict.
// The settlement driver lifts it by settling the load.
func platformRefuseWhileLoadUnsettled(e migExec, installation string) error {
	var open int
	if err := e.queryRow(`SELECT COUNT(*) FROM app_platform_operation_effects f JOIN app_platform_operations o ON o.id=f.operation_id
		WHERE f.kind='load_image' AND f.state IN ('prepared','unknown') AND o.installation_id=?`, installation).Scan(&open); err != nil {
		return err
	}
	if open != 0 {
		return fmt.Errorf("%w: a load of the application image has no verdict yet", errAppPlatformOperationConflict)
	}
	return nil
}
