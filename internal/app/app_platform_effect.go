package app

import (
	"database/sql"
	"errors"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

var errAppPlatformEffectConflict = errors.New("application operation effect conflict")

// These private records persist intent, not executable commands or proof of an
// effect. No receipt mutation or stage-completion API is exposed by this store.
type platformEffectIntent struct {
	number           int64
	kind, payloadSHA string
}

type platformEffectRecord struct {
	id, runtimeID, state         string
	admittedFence, observedFence int64
	created                      bool
}

func platformEffectKindAllowed(stage appplatform.Stage, kind string) bool {
	switch stage {
	case appplatform.StageStage:
		return kind == "stage" || kind == "verify"
	case appplatform.StagePrepareData:
		return kind == "prepare-data"
	case appplatform.StageStart:
		return kind == "start-candidate"
	case appplatform.StageProbe:
		return kind == "probe"
	case appplatform.StageActivate:
		return kind == "activate"
	case appplatform.StageStop:
		return kind == "stop"
	case appplatform.StageSnapshot:
		return kind == "snapshot"
	case appplatform.StageRestoreData:
		return kind == "restore"
	case appplatform.StageRemoveCode:
		return kind == "remove-code"
	case appplatform.StageRemoveData:
		return kind == "delete-data"
	}
	return false
}

// preparePlatformEffect preserves the original effect identity across worker
// reclaim. created only describes an INSERT; neither value authorizes dispatch.
// The future typed provider must bind payloadSHA to its reviewed payload and
// reconcile any existing effect before deciding whether work may proceed.
func (s *Store) preparePlatformEffect(expected platformOperationLease, intent platformEffectIntent) (platformEffectRecord, error) {
	var zero platformEffectRecord
	if !platformValidUUID(expected.id) || !platformValidUUID(expected.owner) || intent.number < 1 || intent.number > 32 ||
		!platformValidSHA(intent.payloadSHA) || !platformEffectKindAllowed(expected.snapshot.Stage, intent.kind) {
		return zero, errAppPlatformEffectConflict
	}
	tx, err := s.db.Begin()
	if err != nil {
		return zero, err
	}
	defer tx.Rollback()
	e := migExec{s: s, ex: tx}
	facts, err := loadPlatformBoundOperationLease(e, expected)
	if err != nil {
		return zero, err
	}
	runtime := facts.runtimeID
	lock := ""
	if s.driver == "postgres" {
		lock = " FOR UPDATE"
	}
	var result platformEffectRecord
	var kind, digest, epoch string
	var storedRuntime sql.NullString
	err = e.queryRow(`SELECT effect_id,kind,payload_sha256,runtime_id,controller_epoch,admitted_fence,observed_fence,state
        FROM app_platform_operation_effects WHERE operation_id=? AND stage=? AND effect_number=?`+lock,
		expected.id, string(expected.snapshot.Stage), intent.number).Scan(&result.id, &kind, &digest, &storedRuntime, &epoch, &result.admittedFence, &result.observedFence, &result.state)
	// A receipt writer may have held the effect row after the installation locks
	// were obtained. Waiting for it must not extend this worker's lease.
	readErr := err
	if readErr != nil && !errors.Is(readErr, sql.ErrNoRows) {
		return zero, readErr
	}
	now, err := finalPlatformOperationLeaseCheck(e, facts)
	if err != nil {
		return zero, err
	}
	err = readErr
	if err == nil {
		if !platformValidUUID(result.id) || kind != intent.kind || digest != intent.payloadSHA || storedRuntime != runtime || epoch != expected.controllerEpoch ||
			result.admittedFence < 1 || result.admittedFence > expected.fence || result.observedFence > expected.fence {
			return zero, errAppPlatformEffectConflict
		}
		switch result.state {
		case "prepared", "applied", "reverted", "unknown":
		default:
			return zero, errAppPlatformEffectConflict
		}
	} else if errors.Is(err, sql.ErrNoRows) {
		result.id, err = platformNewUUID()
		if err != nil {
			return zero, err
		}
		_, err = e.exec(`INSERT INTO app_platform_operation_effects(operation_id,stage,effect_number,effect_id,kind,payload_sha256,runtime_id,
            controller_epoch,admitted_fence,observed_fence,state,receipt_json,created_ms,updated_ms)
            VALUES(?,?,?,?,?,?,?,?,?,0,'prepared','{}',?,?)`, expected.id, string(expected.snapshot.Stage), intent.number, result.id, intent.kind, intent.payloadSHA, runtime,
			expected.controllerEpoch, expected.fence, now, now)
		if err != nil {
			return zero, err
		}
		result.admittedFence, result.state, result.created = expected.fence, "prepared", true
	} else {
		return zero, err
	}
	result.runtimeID = runtime.String
	if err := tx.Commit(); err != nil {
		return zero, err
	}
	return result, nil
}
