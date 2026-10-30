package app

import (
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

var errAppPlatformStageEvidence = errors.New("application operation stage evidence mismatch")

// validatePlatformAdmittedRemoval verifies the consumed review and durable denial
// intent. It does not re-admit work as the original user or prove external
// revocation/drain. Account deletion and current publisher denial must not undo
// an already admitted cleanup operation.
func validatePlatformAdmittedRemoval(e migExec, lease platformOperationLease, facts platformLeaseFacts) error {
	var planID, scope, requestSHA string
	if err := e.queryRow(`SELECT review_plan_id,actor_scope,request_sha256 FROM app_platform_operations WHERE id=?`, lease.id).Scan(&planID, &scope, &requestSHA); err != nil {
		return err
	}
	lock := ""
	if e.s.driver == "postgres" {
		lock = " FOR UPDATE"
	}
	var raw, digest, planScope, appID, publisher, action, epoch, authority string
	var installation, sourceRelease, targetRelease sql.NullString
	var installRevision, policyRevision, trustRevision int64
	var consumed sql.NullInt64
	if err := e.queryRow(`SELECT plan_json,plan_sha256,actor_scope,installation_id,app_id,publisher_id,action,controller_epoch,
        installation_revision,policy_revision,trust_revision,source_release_sha256,target_release_sha256,authority_fingerprint,consumed_ms
        FROM app_platform_review_plans WHERE id=?`+lock, planID).Scan(&raw, &digest, &planScope, &installation, &appID, &publisher, &action, &epoch,
		&installRevision, &policyRevision, &trustRevision, &sourceRelease, &targetRelease, &authority, &consumed); err != nil {
		return err
	}
	if len(raw) > 65536 || !platformValidSHA(digest) || platformDigest([]byte(raw)) != digest || !consumed.Valid || !platformValidSHA(scope) || scope != planScope {
		return errAppPlatformStageEvidence
	}
	request, _ := json.Marshal([]string{"portal-operation-commit/v1", planID, digest})
	if platformDigest(request) != requestSHA {
		return errAppPlatformStageEvidence
	}
	var p platformRemovalProjection
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		return errAppPlatformStageEvidence
	}
	canonical, err := json.Marshal(p)
	if err != nil || string(canonical) != raw {
		return errAppPlatformStageEvidence
	}
	if p.Format != "portal.removal-review/1" || !installation.Valid || installation.String != lease.installationID || p.InstallationID != installation.String ||
		p.AppID != appID || p.PublisherID != publisher || string(p.Action) != action || p.Action != lease.snapshot.Action || p.Managed != lease.snapshot.Managed ||
		p.ControllerEpoch != epoch || epoch != lease.controllerEpoch || p.Generation < 1 || p.Generation == math.MaxInt64 || p.TargetGeneration != p.Generation+1 || uint64(p.TargetGeneration) != lease.snapshot.Generation ||
		p.InstallationRevision != installRevision || p.PolicyRevision != policyRevision || p.TrustRevision != trustRevision || installRevision < 1 || installRevision == math.MaxInt64 || policyRevision < 1 || trustRevision < 1 ||
		p.SourceReleaseSHA256 != sourceRelease.String || targetRelease.Valid || p.ActorAuthority != authority || !platformValidSHA(authority) || !platformValidSHA(p.GrantsSHA256) ||
		p.RuntimeID != facts.runtimeID.String || (p.Managed && p.RuntimeRevision < 1) || (!p.Managed && p.RuntimeRevision != 0) {
		return errAppPlatformStageEvidence
	}
	if sourceRelease.Valid && !platformValidSHA(sourceRelease.String) {
		return errAppPlatformStageEvidence
	}
	desired, phase, dataEffect := "disabled", "disabled", "preserve"
	if p.Action == appplatform.ActionUninstall {
		desired, phase, dataEffect = "removed", "pending", "retain"
	}
	if p.DataEffect != dataEffect || (p.DesiredState != "enabled" && !(p.Action == appplatform.ActionUninstall && p.DesiredState == "disabled")) {
		return errAppPlatformStageEvidence
	}
	var currentApp, currentPublisher, currentDesired, currentPhase string
	var generation, currentRevision, currentPolicy int64
	if err := e.queryRow(`SELECT app_id,publisher_id,desired_state,phase,generation,revision,policy_revision FROM app_platform_installations WHERE id=?`, lease.installationID).
		Scan(&currentApp, &currentPublisher, &currentDesired, &currentPhase, &generation, &currentRevision, &currentPolicy); err != nil {
		return err
	}
	if currentApp != p.AppID || currentPublisher != p.PublisherID || currentDesired != desired || currentPhase != phase || generation != p.TargetGeneration || currentRevision < installRevision+1 || currentPolicy < policyRevision {
		return errAppPlatformStageEvidence
	}
	var active, effects int
	if err := e.queryRow(`SELECT (SELECT COUNT(*) FROM app_platform_activations WHERE installation_id=? AND state='active'),
        (SELECT COUNT(*) FROM app_platform_operation_effects WHERE operation_id=?)`, lease.installationID, lease.id).Scan(&active, &effects); err != nil {
		return err
	}
	if active != 0 || effects != 0 {
		return errAppPlatformStageEvidence
	}
	var data platformRemovalData
	err = e.queryRow(`SELECT id,state,data_format,revision,writer_fence FROM app_platform_data_namespaces WHERE owner_installation_id=?`, lease.installationID).
		Scan(&data.ID, &data.State, &data.Format, &data.Revision, &data.WriterFence)
	if errors.Is(err, sql.ErrNoRows) {
		if p.Data != nil {
			return errAppPlatformStageEvidence
		}
		return nil
	}
	if err != nil {
		return err
	}
	if p.Data == nil || data.ID != p.Data.ID || data.State != "active" || p.Data.State != "active" || data.Format != p.Data.Format || p.Data.Revision < 1 || p.Data.Revision == math.MaxInt64 ||
		p.Data.WriterFence < 0 || p.Data.WriterFence == math.MaxInt64 || data.Revision < p.Data.Revision+1 || data.WriterFence < p.Data.WriterFence+1 {
		return errAppPlatformStageEvidence
	}
	return nil
}

// advancePlatformRemovalControl enters validation/revocation only. It cannot
// finish revocation, stop a process, release data or complete the operation.
func (s *Store) advancePlatformRemovalControl(expected platformOperationLease) (platformOperationLease, error) {
	var zero platformOperationLease
	if expected.snapshot.Action != appplatform.ActionDisable && expected.snapshot.Action != appplatform.ActionUninstall {
		return zero, errAppPlatformStageEvidence
	}
	next := appplatform.StageValidate
	if expected.snapshot.Stage == appplatform.StageValidate {
		next = appplatform.StageRevoke
	} else if expected.snapshot.Stage != appplatform.StageQueued {
		return zero, errAppPlatformStageEvidence
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
	if expected.rowRevision == math.MaxInt64 {
		return zero, errAppPlatformLeaseConflict
	}
	if err := validatePlatformAdmittedRemoval(e, expected, facts); err != nil {
		return zero, err
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
        WHERE id=? AND installation_id=? AND controller_epoch=? AND stage=? AND state_revision=? AND row_revision=? AND fence=?
        AND lease_owner=? AND lease_until_ms=? AND lease_until_ms>? AND completed_ms IS NULL`, string(next), int64(result.snapshot.Revision), now,
		expected.id, expected.installationID, expected.controllerEpoch, string(expected.snapshot.Stage), int64(expected.snapshot.Revision), expected.rowRevision, expected.fence, expected.owner, expected.leaseUntilMS, now)
	if err := platformLeaseCAS(changed, err); err != nil {
		return zero, err
	}
	detail, _ := json.Marshal(struct {
		Stage appplatform.Stage `json:"stage"`
	}{next})
	if _, err := e.exec(`INSERT INTO audit_log(at,actor,actor_ou,action,target_type,target_id,detail,ip)
        VALUES(?,'system',0,'app_platform.operation_stage','app_platform_operation',?,?,'')`, time.UnixMilli(now).UTC().Format(time.RFC3339), expected.id, string(detail)); err != nil {
		return zero, err
	}
	if err := tx.Commit(); err != nil {
		return zero, err
	}
	return result, nil
}
