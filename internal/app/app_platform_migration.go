package app

import (
	"encoding/json"
	"errors"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/journal"
)

// platformDefaultMigrationWallClockSeconds is the descriptor's default bound on a migration job.
const platformDefaultMigrationWallClockSeconds = 600

// platformMigrationStep is the single data migration an update review binds: the schemas it converts
// between, the wall-clock bound the job runs under and the digest of the descriptor's entrypoint. The
// entrypoint itself stays in the signed descriptor; the runner is handed only its digest.
type platformMigrationStep struct {
	From             int64  `json:"from"`
	To               int64  `json:"to"`
	WallClockSeconds int64  `json:"wall_clock_seconds"`
	EntrypointSHA256 string `json:"entrypoint_sha256"`
}

// platformPlanMigration selects what an update from the installed schema needs. It returns no step when
// the target reads the installed schema as it is (and declares that same schema), the declared step when
// one starts at the installed schema, and an error for any other schema change.
func platformPlanMigration(currentFormat int64, data appplatform.DataCompatibility) (*platformMigrationStep, error) {
	step, err := appplatform.PlanDataUpgrade(int(currentFormat), data)
	if err != nil {
		return nil, errAppPlatformInstallUnavailable
	}
	if step == nil {
		if int64(data.Schema) != currentFormat {
			return nil, errAppPlatformInstallUnavailable
		}
		return nil, nil
	}
	seconds := int64(platformDefaultMigrationWallClockSeconds)
	if step.WallClockSeconds != nil {
		seconds = int64(*step.WallClockSeconds)
	}
	return &platformMigrationStep{
		From: int64(step.From), To: int64(step.To), WallClockSeconds: seconds,
		EntrypointSHA256: control.MigrationEntrypointSHA256(step.Entrypoint),
	}, nil
}

func samePlatformMigration(a, b *platformMigrationStep) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// targetDataFormat is the data format the target release declares, and so the format its activation and
// readiness carry: the migration's target when the update migrates, else the installed format.
func (p platformManagedUpdatePlan) targetDataFormat() int64 {
	if p.Migration != nil {
		return p.Migration.To
	}
	return p.DataFormat
}

// activationWriterFence is the namespace writer fence once the target is active.
func (p platformManagedUpdatePlan) activationWriterFence() int64 {
	if p.Migration != nil {
		return p.SourceWriterFence + 2
	}
	return p.SourceWriterFence + 1
}

// platformMigrationEvidence names the applied migrate-data effect an update-start binds.
type platformMigrationEvidence struct{ effectID, receiptSHA256 string }

// loadPlatformAppliedMigration returns the operation's applied migrate-data evidence, or none when the plan
// declares no migration. A plan that declares one has no start without an applied migration.
func loadPlatformAppliedMigration(e migExec, operationID string, plan platformManagedUpdatePlan) (*platformMigrationEvidence, error) {
	if plan.Migration == nil {
		return nil, nil
	}
	var effectID, kind, state, receipt string
	if err := e.queryRow(`SELECT effect_id,kind,state,receipt_json FROM app_platform_operation_effects
		WHERE operation_id=? AND stage='migrate' AND effect_number=2`, operationID).
		Scan(&effectID, &kind, &state, &receipt); err != nil || kind != "migrate-data" || state != "applied" {
		return nil, errAppPlatformStageEvidence
	}
	parsed, err := journal.ParseMigrateDataReceipt([]byte(receipt))
	if err != nil || parsed.State != "migrated" || parsed.EffectID != effectID || parsed.OperationID != operationID {
		return nil, errAppPlatformStageEvidence
	}
	return &platformMigrationEvidence{effectID: effectID, receiptSHA256: platformDigest([]byte(receipt))}, nil
}

// platformMigrationEffectCount counts the applied migrate-data effects of an operation: the one extra
// effect a migrating update carries beside stop, snapshot, source removal and start.
func platformMigrationEffectCount(e migExec, operationID string) (int, error) {
	var n int
	err := e.queryRow(`SELECT COUNT(*) FROM app_platform_operation_effects WHERE operation_id=? AND kind='migrate-data' AND state='applied'`, operationID).Scan(&n)
	return n, err
}

// platformRestorePendingReceipt accepts the receipt column of a restore effect that has not been observed:
// empty for an ordinary restore, empty or the record of the latest signed expiry for the restore that
// settles a migration.
func platformRestorePendingReceipt(expected platformOperationLease, receipt string) bool {
	if receipt == "{}" {
		return true
	}
	if expected.snapshot.Action != appplatform.ActionUpdate {
		return false
	}
	_, ok := platformImportSignedUntil(receipt)
	return ok
}

// errAppPlatformMigrationUnsettled refuses a recovery choice while the failed update's data migration has not
// been settled: the namespace may still hold migrated data.
var errAppPlatformMigrationUnsettled = errors.New("application data migration is not settled")

// platformRequireSettledMigration refuses when the operation carries a migration that is not settled. An
// update with no migration has nothing to settle.
func platformRequireSettledMigration(e migExec, operation string) error {
	if err := platformRequireSettledLoad(e, operation); err != nil {
		return err
	}
	state, err := platformMigrationSettlementState(e, operation)
	if err != nil {
		return err
	}
	if state != "" && state != "settled" {
		return errAppPlatformMigrationUnsettled
	}
	return nil
}

// platformRestoreSettledByMigration reports whether the restore operation's checkpoint belongs to a failed
// update whose migration settlement already restored it. A checkpoint restores once, so the roll back takes
// its restore stage on that evidence alone.
func platformRestoreSettledByMigration(e migExec, lease platformOperationLease) (bool, error) {
	if lease.snapshot.Action != appplatform.ActionRestore {
		return false, nil
	}
	if resume, err := platformOperationIsResume(e, lease.id); err != nil || resume {
		return false, err
	}
	_, _, checkpoint, _, err := loadPlatformRestoreCheckpoint(e, lease)
	if err != nil {
		return false, err
	}
	state, err := platformMigrationSettlementState(e, checkpoint.OperationID)
	return state == "settled", err
}

func jsonUnmarshalString(raw string, v any) error { return json.Unmarshal([]byte(raw), v) }
