package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"slices"
	"strconv"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

// Installation is authority-creating work. Unlike denial-only cleanup, it refuses
// a deleted, demoted, inactive, expired or recreated admitting account. Session
// expiry does not erase a successfully consumed durable decision.
func platformInstallSponsor(e migExec, username sql.NullString, created string, generation, now int64) error {
	if !username.Valid {
		return errAppPlatformStageEvidence
	}
	var currentCreated, role, expiry string
	var active int
	err := e.queryRow(`SELECT COALESCE(created_at,''),COALESCE(role,'user'),COALESCE(active,1),COALESCE(expires_at,'') FROM users WHERE username=?`, username.String).Scan(&currentCreated, &role, &active, &expiry)
	if errors.Is(err, sql.ErrNoRows) {
		return errAppPlatformStageEvidence
	}
	if err != nil {
		return err
	}
	currentGeneration, err := notificationAccountGeneration(e, username.String)
	if err != nil {
		return err
	}
	if currentCreated != created || currentGeneration != generation || role != "admin" || active != 1 {
		return errAppPlatformStageEvidence
	}
	if expiry != "" {
		var zone sql.NullString
		err := e.queryRow(`SELECT v FROM meta WHERE k='timezone'`).Scan(&zone)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		loc := time.Local
		if zone.Valid && zone.String != "" {
			loc, err = time.LoadLocation(zone.String)
			if err != nil {
				return errAppPlatformStageEvidence
			}
		}
		if _, err := time.Parse("2006-01-02", expiry); err != nil || time.UnixMilli(now).In(loc).Format("2006-01-02") > expiry {
			return errAppPlatformStageEvidence
		}
	}
	return nil
}

func validatePlatformInstallAuthority(e migExec, lease platformOperationLease, trust appplatform.PublisherTrust, now int64) error {
	var planID, scope, requestSHA string
	var sourceGeneration int64
	if err := e.queryRow(`SELECT review_plan_id,actor_scope,request_sha256,source_generation FROM app_platform_operations WHERE id=?`, lease.id).Scan(&planID, &scope, &requestSHA, &sourceGeneration); err != nil {
		return err
	}
	lock := ""
	if e.s.driver == "postgres" {
		lock = " FOR UPDATE"
	}
	var raw, digest, planScope, appID, publisher, action, epoch, authority, created string
	var installation, source, target, username sql.NullString
	var installRevision, policyRevision, trustRevision, accountGeneration int64
	var consumed sql.NullInt64
	err := e.queryRow(`SELECT plan_json,plan_sha256,actor_scope,installation_id,app_id,publisher_id,action,controller_epoch,installation_revision,policy_revision,trust_revision,source_release_sha256,target_release_sha256,authority_fingerprint,consumed_ms,username,account_created,account_generation FROM app_platform_review_plans WHERE id=?`+lock, planID).Scan(&raw, &digest, &planScope, &installation, &appID, &publisher, &action, &epoch, &installRevision, &policyRevision, &trustRevision, &source, &target, &authority, &consumed, &username, &created, &accountGeneration)
	if err != nil {
		return err
	}
	if len(raw) > 65536 || !platformValidSHA(digest) || platformDigest([]byte(raw)) != digest || !consumed.Valid || consumed.Int64 > now || !platformValidSHA(scope) || scope != planScope || sourceGeneration != 0 || installation.Valid || source.Valid || !target.Valid || action != "install" || epoch != lease.controllerEpoch || installRevision != 0 || policyRevision != 0 {
		return errAppPlatformStageEvidence
	}
	request, _ := json.Marshal([]string{"portal-operation-commit/v1", planID, digest})
	if platformDigest(request) != requestSHA {
		return errAppPlatformStageEvidence
	}
	var p platformInstallProjection
	if json.Unmarshal([]byte(raw), &p) != nil {
		return errAppPlatformStageEvidence
	}
	canonical, err := json.Marshal(p)
	if err != nil || string(canonical) != raw {
		return errAppPlatformStageEvidence
	}
	if (p.Format != platformNativeInstallFormat && p.Format != platformFrontendInstallFormat && p.Format != platformManagedInstallFormat) || p.AppID != appID || p.PublisherID != publisher || p.ControllerEpoch != epoch || p.ReleaseSHA256 != target.String || p.TrustRevision != trustRevision || p.ActorAuthority != authority || !platformValidSHA(authority) || p.PlatformAPI != platformDevelopmentAPI {
		return errAppPlatformStageEvidence
	}
	verified, err := platformVerifyStoredRelease(e, target.String, trustRevision, trust, now)
	if err != nil {
		return err
	}
	if verified.appID != appID || verified.publisherID != publisher || verified.keyRevision != p.KeyRevision || verified.verificationKeyID != p.KeyID {
		return errAppPlatformStageEvidence
	}
	if err := platformInstallSponsor(e, username, created, accountGeneration, now); err != nil {
		return err
	}
	var currentApp, currentPublisher, desired, phase string
	var runtimeID sql.NullString
	var generation, policy int64
	if err := e.queryRow(`SELECT app_id,publisher_id,desired_state,phase,generation,policy_revision,runtime_id FROM app_platform_installations WHERE id=?`, lease.installationID).Scan(&currentApp, &currentPublisher, &desired, &phase, &generation, &policy, &runtimeID); err != nil {
		return err
	}
	if currentApp != appID || currentPublisher != publisher || desired != "enabled" || phase != "pending" || generation != 1 || policy != 1 {
		return errAppPlatformStageEvidence
	}
	if lease.snapshot.Managed != (p.Format == platformManagedInstallFormat) || runtimeID != managedInstallRuntimeID(p) {
		return errAppPlatformStageEvidence
	}
	if p.Format == platformManagedInstallFormat {
		if err := validateManagedInstallTarget(e, p.Runtime, lease.controllerEpoch); err != nil {
			return err
		}
	}
	var descriptor []byte
	if err := e.queryRow(`SELECT descriptor FROM app_platform_releases WHERE sha256=?`, target.String).Scan(&descriptor); err != nil {
		return err
	}
	parsed, err := appplatform.ParseDescriptor(descriptor)
	if err != nil {
		return errAppPlatformStageEvidence
	}
	if err := installRequirementsForTarget(parsed.Manifest, p.Format, p.Runtime); err != nil {
		return err
	}
	expected := platformInstallProjection{Format: p.Format, Runtime: p.Runtime, AppID: appID, PublisherID: publisher, ReleaseSHA256: target.String, TrustRevision: verified.trustRevision, KeyRevision: verified.keyRevision, KeyID: verified.verificationKeyID, ControllerEpoch: epoch, ActorAuthority: authority, PlatformAPI: platformDevelopmentAPI, DeclinedCapabilities: append([]string{}, parsed.Manifest.Capabilities.Optional...), DataQuotaBytes: 256 << 20}
	quota, err := platformInstallProfileQuota(p.Format, parsed.Manifest)
	if err != nil || p.DataQuotaBytes != quota {
		return errAppPlatformStageEvidence
	}
	expected.DataQuotaBytes = quota
	if err := validatePlatformInstallAudience(e, p.Audience, now); err != nil {
		return err
	}
	expected.Audience = p.Audience
	slices.Sort(expected.DeclinedCapabilities)
	expectedRaw, _ := json.Marshal(expected)
	if string(expectedRaw) != raw {
		return errAppPlatformStageEvidence
	}
	rows, err := e.query(`SELECT capability,granted,policy_json,policy_revision,review_plan_id FROM app_platform_capability_grants WHERE installation_id=? ORDER BY capability`, lease.installationID)
	if err != nil {
		return err
	}
	index := 0
	for rows.Next() {
		var capability, policyJSON, review string
		var granted int
		var revision int64
		if err := rows.Scan(&capability, &granted, &policyJSON, &revision, &review); err != nil {
			rows.Close()
			return err
		}
		if index >= len(p.DeclinedCapabilities) || capability != p.DeclinedCapabilities[index] || granted != 0 || policyJSON != "{}" || revision != 1 || review != planID {
			rows.Close()
			return errAppPlatformStageEvidence
		}
		index++
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if index != len(p.DeclinedCapabilities) {
		return errAppPlatformStageEvidence
	}
	return nil
}

func validatePlatformAdmittedInstall(e migExec, lease platformOperationLease, trust appplatform.PublisherTrust, now int64) error {
	if err := validatePlatformInstallAuthority(e, lease, trust, now); err != nil {
		return err
	}
	var resources int
	if err := e.queryRow(`SELECT (SELECT COUNT(*) FROM app_platform_access_grants WHERE installation_id=?)+(SELECT COUNT(*) FROM app_platform_activations WHERE installation_id=?)+(SELECT COUNT(*) FROM app_platform_data_namespaces WHERE owner_installation_id=?)+(SELECT COUNT(*) FROM app_platform_operation_effects WHERE operation_id=?)`, lease.installationID, lease.installationID, lease.installationID, lease.id).Scan(&resources); err != nil {
		return err
	}
	if resources != 0 {
		return errAppPlatformStageEvidence
	}
	return nil
}

// Advances only database-resident verified artifact staging. Entry into
// prepare_data is a request for the separate provider, never data-ready proof.
func (s *Store) advancePlatformInstallStaging(expected platformOperationLease) (platformOperationLease, error) {
	return s.advancePlatformInstallStagingContext(context.Background(), expected)
}
func (s *Store) advancePlatformInstallStagingContext(ctx context.Context, expected platformOperationLease) (platformOperationLease, error) {
	return s.advancePlatformInstallLocalStage(ctx, expected, false)
}
func (s *Store) advancePlatformInstallLocalStage(ctx context.Context, expected platformOperationLease, frontendData bool) (platformOperationLease, error) {
	var zero platformOperationLease
	if expected.snapshot.Action != appplatform.ActionInstall || expected.snapshot.Generation != 1 {
		return zero, errAppPlatformStageEvidence
	}
	nextByStage := map[appplatform.Stage]appplatform.Stage{appplatform.StageQueued: appplatform.StageValidate, appplatform.StageValidate: appplatform.StageDownload, appplatform.StageDownload: appplatform.StageStage, appplatform.StageStage: appplatform.StagePrepareData}
	next, ok := nextByStage[expected.snapshot.Stage]
	if frontendData && expected.snapshot.Managed {
		return zero, errAppPlatformStageEvidence
	}
	if frontendData {
		next, ok = appplatform.StageActivate, expected.snapshot.Stage == appplatform.StagePrepareData
	}
	if !ok {
		return zero, errAppPlatformStageEvidence
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return zero, err
	}
	defer tx.Rollback()
	e := migExec{s: s, ex: platformContextTx{Tx: tx, ctx: ctx}}
	if err := lockOUMutation(e); err != nil {
		return zero, err
	}
	if err := lockPlatformPublisherTrust(e); err != nil {
		return zero, err
	}
	facts, err := loadPlatformBoundOperationLease(e, expected)
	if err != nil {
		return zero, err
	}
	if expected.rowRevision == math.MaxInt64 {
		return zero, errAppPlatformLeaseConflict
	}
	trust, err := loadPlatformPublisherTrust(e)
	if err != nil {
		return zero, err
	}
	if err := validatePlatformAdmittedInstall(e, expected, trust, facts.now); err != nil {
		return zero, err
	}
	if frontendData {
		if _, err := loadPlatformFrontendCandidate(e, expected, facts.now); err != nil {
			return zero, err
		}
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
	updated, err := e.exec(`UPDATE app_platform_operations SET stage=?,state_revision=?,row_revision=row_revision+1,updated_ms=? WHERE id=? AND installation_id=? AND controller_epoch=? AND stage=? AND state_revision=? AND row_revision=? AND fence=? AND lease_owner=? AND lease_until_ms=? AND lease_until_ms>? AND completed_ms IS NULL`, string(next), int64(result.snapshot.Revision), now, expected.id, expected.installationID, expected.controllerEpoch, string(expected.snapshot.Stage), int64(expected.snapshot.Revision), expected.rowRevision, expected.fence, expected.owner, expected.leaseUntilMS, now)
	if err := platformLeaseCAS(updated, err); err != nil {
		return zero, err
	}
	detail, _ := json.Marshal(struct {
		Stage appplatform.Stage `json:"stage"`
	}{next})
	if _, err := e.exec(`INSERT INTO audit_log(at,actor,actor_ou,action,target_type,target_id,detail,ip) VALUES(?,'system',0,'app_platform.operation_stage','app_platform_operation',?,?,'')`, time.UnixMilli(now).UTC().Format(time.RFC3339), expected.id, string(detail)); err != nil {
		return zero, err
	}
	// A blocked audit write may consume the lease or key lifetime. Every artifact
	// row and policy lock is already held; reread and refuse before commit.
	now, err = finalPlatformOperationLeaseCheck(e, facts)
	if err != nil {
		return zero, err
	}
	if err := validatePlatformAdmittedInstall(e, expected, trust, now); err != nil {
		return zero, err
	}
	now, err = finalPlatformOperationLeaseCheck(e, facts)
	if err != nil {
		return zero, err
	}
	if err := platformVerifyInstallAuthorityAtCommit(e, expected, trust, now); err != nil {
		return zero, err
	}
	if err := tx.Commit(); err != nil {
		return zero, err
	}
	return result, nil
}

func platformVerifyInstallAuthorityAtCommit(e migExec, lease platformOperationLease, trust appplatform.PublisherTrust, now int64) error {
	var raw, envelope []byte
	var username sql.NullString
	var created string
	var generation int64
	if err := e.queryRow(`SELECT r.descriptor,r.signature,p.username,p.account_created,p.account_generation FROM app_platform_operations o JOIN app_platform_review_plans p ON p.id=o.review_plan_id JOIN app_platform_releases r ON r.sha256=p.target_release_sha256 WHERE o.id=?`, lease.id).Scan(&raw, &envelope, &username, &created, &generation); err != nil {
		return err
	}
	if err := platformInstallSponsor(e, username, created, generation, now); err != nil {
		return err
	}
	if _, err := appplatform.VerifyPublisherDescriptor(raw, envelope, trust, strconv.FormatInt(now, 10)); err != nil {
		return errAppPlatformStageEvidence
	}
	return nil
}
