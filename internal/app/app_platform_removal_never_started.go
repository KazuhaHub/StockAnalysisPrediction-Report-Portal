package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/journal"
)

// The removal of a managed installation that never started.
//
// A managed install prepares its data namespace before it starts anything, so an install the runner refuses
// at the start (capacity, or a start the runner proved never ran) already owns an active namespace, and the
// runner holds a disk reservation for it. The install ends without a running backend, and none of the
// ordinary removal's evidence exists for it: no completed install operation, no active activation, no start
// receipt, no code resource to stop or remove. This variant lets an administrator uninstall such an
// installation. It skips stop and remove_code (there is nothing to stop or remove), deletes the namespace
// with the ordinary signed delete-data command bound to the failed install's applied prepare_data effect,
// and finishes the installation removed.
//
// The variant is admitted only on positive proof that nothing ever ran, and fails closed on anything else
// with the same typed error the ordinary review gives, so an install whose start is unsettled stays where it
// is until the orphan settlement has proved the start never ran.

// platformRemovalNeverStarted is the evidence the variant rests on. It is part of the reviewed projection, so
// the consuming transaction and every later stage compare against exactly what the administrator saw.
type platformRemovalNeverStarted struct {
	OperationID   string `json:"operation_id"`
	ReleaseSHA256 string `json:"release_sha256"`
	EffectID      string `json:"prepare_data_effect_id"`
	PayloadSHA256 string `json:"prepare_data_payload_sha256"`
	ReceiptSHA256 string `json:"prepare_data_receipt_sha256"`
	AdmittedFence int64  `json:"prepare_data_admitted_fence"`
}

// platformStartBearingKinds are the effect kinds that start a backend: a candidate or writable candidate
// (both are start-candidate), an update start, an enable start and a restore start.
var platformStartBearingKinds = map[string]bool{
	"start-candidate": true, "update-start": true, "enable-start": true, "restore-start": true,
}

// platformLoadNeverStartedEvidence proves that the installation never started anything and names the
// evidence its namespace deletion is bound to. exceptOperation is the removal operation itself once it is
// admitted (empty at review). Every condition is required:
//
//   - the installation has exactly one other operation, its install, which ended failed (or recovery_required,
//     where an unsettled effect kept it) with an error code and no lease;
//   - no start-bearing effect of that operation is anything but reverted: an applied, unknown or merely
//     prepared start may have run;
//   - every other effect of the operation is settled (applied or reverted) and none lies past the start stage;
//   - its prepare_data effect is applied and its receipt names the namespace;
//   - the namespace is the installation's only one, and it is active;
//   - no activation is active.
func platformLoadNeverStartedEvidence(e migExec, installation, epoch, runtimeID, namespaceID string, namespaceFormat int64, exceptOperation string) (platformRemovalNeverStarted, error) {
	var zero, result platformRemovalNeverStarted
	refused := func(reason string) (platformRemovalNeverStarted, error) {
		return zero, fmt.Errorf("never-started removal (%s): %w", reason, errAppPlatformPlanChanged)
	}
	rows, err := e.query(`SELECT id,review_plan_id,action,managed,source_generation,target_generation,stage,controller_epoch,error_code,completed_ms,lease_owner,lease_until_ms
		FROM app_platform_operations WHERE installation_id=? AND id<>?`, installation, exceptOperation)
	if err != nil {
		return zero, err
	}
	type operation struct {
		id, plan, action, stage, epoch string
		managed, source, target        int64
		code                           sql.NullString
		completed, until               sql.NullInt64
		owner                          sql.NullString
	}
	var operations []operation
	for rows.Next() {
		var o operation
		if err := rows.Scan(&o.id, &o.plan, &o.action, &o.managed, &o.source, &o.target, &o.stage, &o.epoch, &o.code, &o.completed, &o.owner, &o.until); err != nil {
			rows.Close()
			return zero, err
		}
		operations = append(operations, o)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return zero, err
	}
	if len(operations) != 1 {
		return refused("the installation has more or fewer operations than its install")
	}
	install := operations[0]
	if install.action != string(appplatform.ActionInstall) || install.managed != 1 || install.source != 0 || install.target != 1 ||
		(install.stage != string(appplatform.StageFailed) && install.stage != string(appplatform.StageRecoveryRequired)) ||
		install.epoch != epoch || !install.code.Valid || install.code.String == "" || !install.completed.Valid || install.owner.Valid || install.until.Valid ||
		!platformValidUUID(install.id) {
		return refused("the install did not end failed")
	}
	var planRaw, planSHA string
	var target sql.NullString
	if err := e.queryRow(`SELECT plan_json,plan_sha256,target_release_sha256 FROM app_platform_review_plans WHERE id=?`, install.plan).Scan(&planRaw, &planSHA, &target); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return refused("the install review is gone")
		}
		return zero, err
	}
	var plan platformInstallProjection
	if platformDigest([]byte(planRaw)) != planSHA || json.Unmarshal([]byte(planRaw), &plan) != nil || !platformManagedFormat(plan.Format) || plan.Runtime == nil ||
		plan.Runtime.RuntimeID != runtimeID || plan.ControllerEpoch != epoch || !target.Valid || plan.ReleaseSHA256 != target.String || !platformValidSHA(target.String) {
		return refused("the install review does not describe a managed install on this runtime")
	}
	var managed int
	if err := e.queryRow(`SELECT managed FROM app_platform_releases WHERE sha256=?`, target.String).Scan(&managed); err != nil || managed != 1 {
		if err == nil || errors.Is(err, sql.ErrNoRows) {
			return refused("the install release is not a stored managed release")
		}
		return zero, err
	}
	effects, err := e.query(`SELECT stage,effect_number,effect_id,kind,payload_sha256,runtime_id,controller_epoch,admitted_fence,state,receipt_json
		FROM app_platform_operation_effects WHERE operation_id=? AND kind NOT IN ('load_image','remove_image')`, install.id)
	if err != nil {
		return zero, err
	}
	type effect struct {
		stage, id, kind, digest, epoch, state, receipt string
		number, fence                                  int64
		runtime                                        sql.NullString
	}
	var found []effect
	for effects.Next() {
		var f effect
		if err := effects.Scan(&f.stage, &f.number, &f.id, &f.kind, &f.digest, &f.runtime, &f.epoch, &f.fence, &f.state, &f.receipt); err != nil {
			effects.Close()
			return zero, err
		}
		found = append(found, f)
	}
	err = effects.Err()
	effects.Close()
	if err != nil {
		return zero, err
	}
	var prepare *effect
	for index := range found {
		f := found[index]
		if f.state != "applied" && f.state != "reverted" {
			return refused("an effect of the install is unsettled")
		}
		if f.stage != string(appplatform.StageStage) && f.stage != string(appplatform.StagePrepareData) && f.stage != string(appplatform.StageStart) {
			return refused("an effect lies past the start stage")
		}
		if platformStartBearingKinds[f.kind] || f.stage == string(appplatform.StageStart) {
			if !platformStartBearingKinds[f.kind] || f.state != "reverted" {
				return refused("the start of the install is not reverted")
			}
			continue
		}
		if f.stage == string(appplatform.StagePrepareData) && f.number == 1 {
			if f.kind != "prepare-data" || f.state != "applied" || prepare != nil || !f.runtime.Valid || f.runtime.String != runtimeID || f.epoch != epoch ||
				f.fence < 1 || !platformValidUUID(f.id) || !platformValidSHA(f.digest) {
				return refused("the prepare_data effect is not applied")
			}
			prepare = &found[index]
		}
	}
	if prepare == nil {
		return refused("the install has no applied prepare_data effect")
	}
	receipt, err := journal.ParseDataPreparationReceipt([]byte(prepare.receipt))
	format, formatErr := strconv.ParseInt(receipt.DataFormat, 10, 64)
	if err != nil || formatErr != nil || receipt.Claim.EffectID != prepare.id || receipt.Claim.ResourceID != namespaceID || receipt.InstallationID != installation ||
		receipt.PayloadSHA256 != prepare.digest || format != namespaceFormat {
		return refused("the prepare_data receipt does not name the installation's namespace")
	}
	var namespaces, owned, active int
	if err := e.queryRow(`SELECT
		(SELECT COUNT(*) FROM app_platform_data_namespaces WHERE origin_installation_id=? OR owner_installation_id=?),
		(SELECT COUNT(*) FROM app_platform_data_namespaces WHERE id=? AND origin_installation_id=? AND owner_installation_id=? AND state='active'),
		(SELECT COUNT(*) FROM app_platform_activations WHERE installation_id=? AND (state='active' OR generation<>1))`,
		installation, installation, namespaceID, installation, installation, installation).Scan(&namespaces, &owned, &active); err != nil {
		return zero, err
	}
	if namespaces != 1 || owned != 1 || active != 0 {
		return refused("the namespace or the activations are not those of a never-started install")
	}
	result = platformRemovalNeverStarted{
		OperationID: install.id, ReleaseSHA256: target.String, EffectID: prepare.id, PayloadSHA256: prepare.digest,
		ReceiptSHA256: platformDigest([]byte(prepare.receipt)), AdmittedFence: prepare.fence,
	}
	return result, nil
}

// platformOperationNeverStarted returns the never-started evidence a removal operation was admitted with, or nil
// for every ordinary removal. It reads the consumed review the operation is bound to.
func platformOperationNeverStarted(e migExec, operation string) (*platformRemovalNeverStarted, error) {
	var raw string
	if err := e.queryRow(`SELECT p.plan_json FROM app_platform_operations o JOIN app_platform_review_plans p ON p.id=o.review_plan_id WHERE o.id=?`, operation).Scan(&raw); err != nil {
		return nil, err
	}
	var p platformRemovalProjection
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		return nil, errAppPlatformStageEvidence
	}
	return p.NeverStarted, nil
}

// advancePlatformNeverStartedRemoval passes the stop and remove_code stages of a never-started uninstall. No
// runner command is prepared or sent: there is no code resource. The evidence is verified again in this
// transaction (validatePlatformAdmittedRemoval), and the stages hold no effect, so nothing can have been
// skipped that the ordinary path would have recorded.
func (s *Server) advancePlatformNeverStartedRemoval(ctx context.Context, expected platformOperationLease) (platformOperationLease, error) {
	var zero platformOperationLease
	var next appplatform.Stage
	switch expected.snapshot.Stage {
	case appplatform.StageStop:
		next = appplatform.StageRemoveCode
	case appplatform.StageRemoveCode:
		next = appplatform.StageRemoveData
	}
	if expected.snapshot.Action != appplatform.ActionUninstall || !expected.snapshot.Managed || next == "" {
		return zero, errAppPlatformStageEvidence
	}
	tx, err := s.st.db.BeginTx(ctx, nil)
	if err != nil {
		return zero, err
	}
	defer tx.Rollback()
	e := migExec{s: s.st, ex: platformContextTx{Tx: tx, ctx: ctx}}
	if err := lockOUMutation(e); err != nil {
		return zero, err
	}
	facts, err := loadPlatformBoundOperationLease(e, expected)
	if err != nil {
		return zero, err
	}
	if err := validatePlatformAdmittedRemoval(e, expected, facts); err != nil {
		return zero, fmt.Errorf("never-started removal admitted state: %w", err)
	}
	never, err := platformOperationNeverStarted(e, expected.id)
	if err != nil {
		return zero, err
	}
	if never == nil {
		return zero, errAppPlatformStageEvidence
	}
	var effects int
	if err := e.queryRow(`SELECT COUNT(*) FROM app_platform_operation_effects WHERE operation_id=?`, expected.id).Scan(&effects); err != nil || effects != 0 {
		return zero, fmt.Errorf("never-started removal effect: %w", errAppPlatformStageEvidence)
	}
	now, err := finalPlatformOperationLeaseCheck(e, facts)
	if err != nil {
		return zero, err
	}
	model, err := appplatform.OperationFromSnapshot(expected.snapshot)
	if err != nil {
		return zero, errAppPlatformStageEvidence
	}
	advanced, err := model.Advance(expected.snapshot.Revision, next)
	if err != nil {
		return zero, errAppPlatformStageEvidence
	}
	result := expected
	result.snapshot = advanced.Snapshot()
	result.rowRevision++
	changed, err := e.exec(`UPDATE app_platform_operations SET stage=?,state_revision=?,row_revision=row_revision+1,updated_ms=?
        WHERE id=? AND installation_id=? AND controller_epoch=? AND stage=? AND state_revision=? AND row_revision=? AND fence=? AND lease_owner=? AND lease_until_ms=? AND lease_until_ms>? AND completed_ms IS NULL`,
		string(next), int64(result.snapshot.Revision), now, expected.id, expected.installationID, expected.controllerEpoch, string(expected.snapshot.Stage),
		int64(expected.snapshot.Revision), expected.rowRevision, expected.fence, expected.owner, expected.leaseUntilMS, now)
	if err := platformLeaseCAS(changed, err); err != nil {
		return zero, err
	}
	detail, _ := json.Marshal(struct {
		Stage        appplatform.Stage `json:"stage"`
		NeverStarted bool              `json:"never_started"`
	}{next, true})
	if _, err := e.exec(`INSERT INTO audit_log(at,actor,actor_ou,action,target_type,target_id,detail,ip) VALUES(?,'system',0,'app_platform.operation_stage','app_platform_operation',?,?,'')`,
		time.UnixMilli(now).UTC().Format(time.RFC3339), expected.id, string(detail)); err != nil {
		return zero, err
	}
	if _, err := finalPlatformOperationLeaseCheck(e, facts); err != nil {
		return zero, err
	}
	if err := tx.Commit(); err != nil {
		return zero, err
	}
	return result, nil
}
