package app

import (
	"database/sql"
	"errors"
	"math"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

var (
	errAppPlatformLeaseConflict = errors.New("application operation lease conflict")
	errAppPlatformControlPaused = errors.New("application control requires initialization or recovery review")
)

const appPlatformLeaseMS int64 = 30_000

// This is an internal fencing token, not an external API or execution grant.
// Claiming work permits inspection/reconciliation; it does not prove any stage
// effect completed or admit a package, resource or new application authority.
type platformOperationLease struct {
	id, installationID, controllerEpoch, owner string
	fence, rowRevision, leaseUntilMS           int64
	snapshot                                   appplatform.OperationSnapshot
}

func (s *Store) claimPlatformOperation(id, worker string) (platformOperationLease, error) {
	return s.updatePlatformOperationLease(id, worker, nil)
}

func (s *Store) heartbeatPlatformOperation(expected platformOperationLease) (platformOperationLease, error) {
	return s.updatePlatformOperationLease(expected.id, expected.owner, &expected)
}

func platformValidUUID(id string) bool {
	if len(id) != 36 {
		return false
	}
	for index, c := range []byte(id) {
		if index == 8 || index == 13 || index == 18 || index == 23 {
			if c != '-' {
				return false
			}
		} else if !(c >= '0' && c <= '9') && !(c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

func (s *Store) updatePlatformOperationLease(id, worker string, expected *platformOperationLease) (platformOperationLease, error) {
	var zero platformOperationLease
	if !platformValidUUID(id) || !platformValidUUID(worker) {
		return zero, errAppPlatformLeaseConflict
	}
	tx, err := s.db.Begin()
	if err != nil {
		return zero, err
	}
	defer tx.Rollback()
	e := migExec{s: s, ex: tx}
	facts, err := loadPlatformOperationLease(e, id)
	if err != nil {
		return zero, err
	}
	lease, installationRevision, controlFence, now := facts.lease, facts.installationRevision, facts.controlFence, facts.now
	installation, epoch, stateRevision := lease.installationID, lease.controllerEpoch, int64(lease.snapshot.Revision)
	if lease.rowRevision == math.MaxInt64 {
		return zero, errAppPlatformLeaseConflict
	}
	old := lease
	if expected != nil {
		if old != *expected || old.owner == "" || old.leaseUntilMS <= now || controlFence != lease.fence {
			return zero, errAppPlatformLeaseConflict
		}
	} else {
		if (old.leaseUntilMS > now) || controlFence == math.MaxInt64 || installationRevision == math.MaxInt64 {
			return zero, errAppPlatformLeaseConflict
		}
		lease.fence = controlFence + 1
		result, err := e.exec(`UPDATE app_platform_installations SET control_fence=?,revision=revision+1,updated_ms=?
            WHERE id=? AND revision=? AND control_fence=? AND controller_epoch=?`, lease.fence, now, installation, installationRevision, controlFence, epoch)
		if err := platformLeaseCAS(result, err); err != nil {
			return zero, err
		}
	}
	lease.owner, lease.leaseUntilMS, lease.rowRevision = worker, now+appPlatformLeaseMS, old.rowRevision+1
	eligibility := `(lease_until_ms IS NULL OR lease_until_ms<=?)`
	if expected != nil {
		eligibility = `lease_until_ms>? AND lease_owner=?`
	}
	args := []any{worker, lease.leaseUntilMS, lease.fence, now, id, installation, epoch, old.rowRevision, stateRevision, old.fence, now}
	if expected != nil {
		args = append(args, expected.owner)
	}
	result, err := e.exec(`UPDATE app_platform_operations SET lease_owner=?,lease_until_ms=?,fence=?,row_revision=row_revision+1,updated_ms=?
        WHERE id=? AND installation_id=? AND controller_epoch=? AND row_revision=? AND state_revision=? AND fence=?
        AND completed_ms IS NULL AND `+eligibility, args...)
	if err := platformLeaseCAS(result, err); err != nil {
		return zero, err
	}
	if err := tx.Commit(); err != nil {
		return zero, err
	}
	return lease, nil
}

type platformLeaseFacts struct {
	runtimeID                               sql.NullString
	lease                                   platformOperationLease
	installationRevision, controlFence, now int64
}

func loadPlatformOperationLease(e migExec, id string) (platformLeaseFacts, error) {
	var zero platformLeaseFacts
	// SQLite must obtain write intent before reading. A deferred read followed
	// by a write upgrade could otherwise race another connection/process.
	if e.s.driver != "postgres" {
		if _, err := e.exec(`UPDATE app_platform_installations SET revision=revision
            WHERE id=(SELECT installation_id FROM app_platform_operations WHERE id=?)`, id); err != nil {
			return zero, err
		}
	}
	var installation string
	if err := e.queryRow(`SELECT installation_id FROM app_platform_operations WHERE id=?`, id).Scan(&installation); err != nil {
		return zero, platformLeaseReadError(err)
	}
	lock := ""
	if e.s.driver == "postgres" {
		lock = " FOR UPDATE"
	}
	var epoch, phase, desired string
	var generation, installationRevision, controlFence int64
	if err := e.queryRow(`SELECT controller_epoch,generation,revision,control_fence,phase,desired_state
        FROM app_platform_installations WHERE id=?`+lock, installation).Scan(&epoch, &generation, &installationRevision, &controlFence, &phase, &desired); err != nil {
		return zero, platformLeaseReadError(err)
	}
	var lease platformOperationLease
	var action, stage string
	var managed int
	var source, target, stateRevision int64
	var owner sql.NullString
	var until, completed sql.NullInt64
	lease.id = id
	if err := e.queryRow(`SELECT installation_id,controller_epoch,action,managed,source_generation,target_generation,
        stage,state_revision,row_revision,fence,lease_owner,lease_until_ms,completed_ms
        FROM app_platform_operations WHERE id=?`+lock, id).Scan(&lease.installationID, &lease.controllerEpoch,
		&action, &managed, &source, &target, &stage, &stateRevision, &lease.rowRevision, &lease.fence, &owner, &until, &completed); err != nil {
		return zero, platformLeaseReadError(err)
	}
	lease.owner, lease.leaseUntilMS = owner.String, until.Int64
	lease.snapshot = appplatform.OperationSnapshot{Action: appplatform.Action(action), Managed: managed == 1, Generation: uint64(target), Revision: uint64(stateRevision), Stage: appplatform.Stage(stage)}
	if _, err := appplatform.OperationFromSnapshot(lease.snapshot); err != nil {
		return zero, errAppPlatformLeaseConflict
	}
	if source < 0 || target < 1 || stateRevision < 1 || (managed != 0 && managed != 1) || installation != lease.installationID || epoch != lease.controllerEpoch || completed.Valid {
		return zero, errAppPlatformLeaseConflict
	}
	if phase == "review_required" || phase == "recovery_required" || phase == "quarantined" || (desired == "removed" && action != string(appplatform.ActionUninstall)) {
		return zero, errAppPlatformLeaseConflict
	}
	if (action == string(appplatform.ActionInstall)) != (source == 0) || source == math.MaxInt64 || target != source+1 || (generation != source && generation != target) || controlFence < lease.fence {
		return zero, errAppPlatformLeaseConflict
	}
	for _, setting := range []struct{ key, value string }{
		{"app_platform_restore_paused", "0"},
		{"app_platform_controller_epoch", epoch},
	} {
		var value string
		if err := e.queryRow(`SELECT v FROM meta WHERE k=?`, setting.key).Scan(&value); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return zero, errAppPlatformControlPaused
			}
			return zero, err
		}
		if value != setting.value {
			return zero, errAppPlatformControlPaused
		}
	}
	// Sample the database clock after obtaining locks. Independent worker clocks
	// cannot extend or shorten another worker's lease.
	now, err := platformDatabaseNow(e)
	if err != nil {
		return zero, err
	}
	if now < 0 || now > math.MaxInt64-appPlatformLeaseMS {
		return zero, errAppPlatformLeaseConflict
	}
	return platformLeaseFacts{lease: lease, installationRevision: installationRevision, controlFence: controlFence, now: now}, nil
}

func platformLeaseReadError(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return errAppPlatformLeaseConflict
	}
	return err
}

func platformLeaseCAS(result sql.Result, err error) error {
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		return errAppPlatformLeaseConflict
	}
	return nil
}

// loadPlatformBoundOperationLease locks the runtime binding before the shared
// installation/operation lease checks. Callers must already own a transaction.
func loadPlatformBoundOperationLease(e migExec, expected platformOperationLease) (platformLeaseFacts, error) {
	var zero platformLeaseFacts
	if !platformValidUUID(expected.id) || !platformValidUUID(expected.owner) {
		return zero, errAppPlatformLeaseConflict
	}
	// Lock runtime before installation, matching review admission. SQLite's write
	// intent serializes this transaction before any preliminary reads.
	if e.s.driver != "postgres" {
		if _, err := e.exec(`UPDATE app_platform_operations SET row_revision=row_revision WHERE id=?`, expected.id); err != nil {
			return zero, err
		}
	}
	var runtime sql.NullString
	if err := e.queryRow(`SELECT i.runtime_id FROM app_platform_operations o JOIN app_platform_installations i ON i.id=o.installation_id WHERE o.id=?`, expected.id).Scan(&runtime); err != nil {
		return zero, platformLeaseReadError(err)
	}
	lock := ""
	if e.s.driver == "postgres" {
		lock = " FOR UPDATE"
	}
	if runtime.Valid {
		var epoch string
		if err := e.queryRow(`SELECT controller_epoch FROM app_platform_runtime_bindings WHERE id=?`+lock, runtime.String).Scan(&epoch); err != nil {
			return zero, err
		}
		if epoch != expected.controllerEpoch {
			return zero, errAppPlatformEffectConflict
		}
	}
	facts, err := loadPlatformOperationLease(e, expected.id)
	if err != nil {
		return zero, err
	}
	if facts.lease != expected || expected.leaseUntilMS <= facts.now || expected.fence != facts.controlFence {
		return zero, errAppPlatformLeaseConflict
	}
	var currentRuntime sql.NullString
	if err := e.queryRow(`SELECT runtime_id FROM app_platform_installations WHERE id=?`, expected.installationID).Scan(&currentRuntime); err != nil {
		return zero, err
	}
	if runtime != currentRuntime || runtime.Valid != expected.snapshot.Managed {
		return zero, errAppPlatformEffectConflict
	}
	facts.runtimeID = runtime
	return facts, nil
}

// Recheck after all potentially blocking resource/review/effect locks. On
// PostgreSQL the short final commit section holds the marker rows against a
// concurrent pause/epoch change. Marker writers must not acquire resource locks
// after updating these rows; coordinated restore remains an offline operation.
func finalPlatformOperationLeaseCheck(e migExec, facts platformLeaseFacts) (int64, error) {
	lock := ""
	if e.s.driver == "postgres" {
		lock = " FOR SHARE"
	}
	for _, setting := range []struct{ key, value string }{
		{"app_platform_restore_paused", "0"},
		{"app_platform_controller_epoch", facts.lease.controllerEpoch},
	} {
		var value string
		if err := e.queryRow(`SELECT v FROM meta WHERE k=?`+lock, setting.key).Scan(&value); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return 0, errAppPlatformControlPaused
			}
			return 0, err
		}
		if value != setting.value {
			return 0, errAppPlatformControlPaused
		}
	}
	now, err := platformDatabaseNow(e)
	if err != nil {
		return 0, err
	}
	if now < facts.now || now >= facts.lease.leaseUntilMS {
		return 0, errAppPlatformLeaseConflict
	}
	return now, nil
}
