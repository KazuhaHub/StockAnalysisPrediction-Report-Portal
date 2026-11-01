package app

import (
	"context"
	"crypto/ed25519"
	"database/sql"
	"encoding/json"
	"math"
	"strconv"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/journal"
)

func platformReadDeleteDataPlan(
	e migExec, expected platformOperationLease,
) (platformDeleteDataReviewProjection, string, error) {
	var p platformDeleteDataReviewProjection
	var planID, requestSHA, action, scope, operationEpoch string
	var managed, sourceGeneration, targetGeneration int64
	if err := e.queryRow(`SELECT review_plan_id,request_sha256,action,managed,
		source_generation,target_generation,controller_epoch,actor_scope
		FROM app_platform_operations WHERE id=?`, expected.id).Scan(
		&planID, &requestSHA, &action, &managed, &sourceGeneration,
		&targetGeneration, &operationEpoch, &scope); err != nil {
		return p, "", errAppPlatformStageEvidence
	}
	if !platformValidUUID(planID) || !platformValidSHA(requestSHA) ||
		action != string(appplatform.ActionDeleteData) || managed != 1 ||
		operationEpoch != expected.controllerEpoch ||
		sourceGeneration != int64(expected.snapshot.Generation)-1 ||
		targetGeneration != int64(expected.snapshot.Generation) {
		return p, "", errAppPlatformStageEvidence
	}
	var raw, digest, planScope, installation, appID, publisher, planAction,
		release, epoch, authority string
	var targetRelease sql.NullString
	var installRevision, policyRevision, trustRevision int64
	var consumed sql.NullInt64
	if err := e.queryRow(`SELECT plan_json,plan_sha256,actor_scope,
		installation_id,app_id,publisher_id,action,controller_epoch,
		installation_revision,policy_revision,trust_revision,
		source_release_sha256,target_release_sha256,authority_fingerprint,
		consumed_ms FROM app_platform_review_plans WHERE id=?`+
		lockForDriver(e), planID).Scan(
		&raw, &digest, &planScope, &installation, &appID, &publisher,
		&planAction, &epoch, &installRevision, &policyRevision,
		&trustRevision, &release, &targetRelease, &authority,
		&consumed); err != nil {
		return p, "", errAppPlatformStageEvidence
	}
	request, _ := json.Marshal([]string{
		"portal-operation-commit/v1", planID, digest,
	})
	if len(raw) > 65536 || !platformValidSHA(digest) ||
		platformDigest([]byte(raw)) != digest ||
		!consumed.Valid || planScope != scope ||
		platformDigest(request) != requestSHA ||
		installation != expected.installationID ||
		planAction != action || epoch != expected.controllerEpoch ||
		targetRelease.Valid || !platformValidSHA(authority) {
		return p, "", errAppPlatformStageEvidence
	}
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		return p, "", errAppPlatformStageEvidence
	}
	canonical, err := json.Marshal(p)
	if err != nil || string(canonical) != raw ||
		p.Format != platformDeleteDataReviewFormat ||
		p.InstallationID != installation || p.AppID != appID ||
		p.PublisherID != publisher || p.Action != appplatform.ActionDeleteData ||
		!p.Managed || p.ControllerEpoch != epoch ||
		p.InstallationRevision != installRevision ||
		p.PolicyRevision != policyRevision ||
		p.TrustRevision != trustRevision ||
		p.ReleaseSHA256 != release ||
		p.ActorAuthority != authority ||
		p.Generation != sourceGeneration ||
		p.TargetGeneration != targetGeneration ||
		p.Generation < 1 || p.Generation == math.MaxInt64 ||
		p.InstallationRevision < 1 ||
		p.InstallationRevision == math.MaxInt64 ||
		p.PolicyRevision < 1 || p.ControlFence < 1 ||
		!platformValidSHA(p.RecoverySetsSHA256) ||
		p.DataEffect != "delete" || !platformValidUUID(p.Data.ID) ||
		p.Data.OriginInstallationID != installation ||
		!platformValidUUID(p.RuntimeID) ||
		!platformValidUUID(p.ActivationID) ||
		!platformValidSHA(p.Data.SourcePayloadSHA256) ||
		!platformValidSHA(p.Data.SourceReceiptSHA256) ||
		!platformValidSHA(p.Data.OwnershipSHA256) ||
		!platformValidUUID(p.Data.SourceOperationID) ||
		!platformValidUUID(p.Data.SourceEffectID) ||
		p.Data.SourceGeneration < 1 || p.Data.SourceFence < 1 {
		return p, "", errAppPlatformStageEvidence
	}
	return p, planID, nil
}

func platformValidateDeleteDataSource(
	e migExec,
	p platformDeleteDataReviewProjection,
	runtimeID string,
	now int64,
) error {
	var state, digest, receiptRaw, effectRuntime, epoch string
	var admitted, observed, created, updated int64
	if err := e.queryRow(`SELECT state,payload_sha256,receipt_json,
		runtime_id,controller_epoch,admitted_fence,observed_fence,
		created_ms,updated_ms
		FROM app_platform_operation_effects
		WHERE operation_id=? AND stage='prepare_data' AND effect_number=1
		AND effect_id=? AND kind='prepare-data'`,
		p.Data.SourceOperationID, p.Data.SourceEffectID).Scan(
		&state, &digest, &receiptRaw, &effectRuntime, &epoch,
		&admitted, &observed, &created, &updated); err != nil ||
		state != "applied" ||
		digest != p.Data.SourcePayloadSHA256 ||
		effectRuntime != runtimeID || epoch != p.ControllerEpoch ||
		admitted != p.Data.SourceFence ||
		observed != p.Data.SourceFence ||
		created < 0 || updated < created || updated > now ||
		platformDigest([]byte(receiptRaw)) != p.Data.SourceReceiptSHA256 {
		return errAppPlatformStageEvidence
	}
	receipt, err := journal.ParseDataPreparationReceipt(
		[]byte(receiptRaw))
	if err != nil || receipt.Claim.EffectID != p.Data.SourceEffectID ||
		receipt.Claim.ResourceID != p.Data.ID ||
		receipt.Claim.OwnershipSHA256 != p.Data.OwnershipSHA256 ||
		receipt.InstallationID != p.InstallationID ||
		receipt.ControllerEpoch != p.ControllerEpoch ||
		receipt.PayloadSHA256 != p.Data.SourcePayloadSHA256 ||
		receipt.ProjectID != p.Data.ProjectID ||
		receipt.Device != p.Data.Device ||
		receipt.NamespaceInode != p.Data.NamespaceInode ||
		receipt.QuotaBytes != platformDeleteDataDecimal(p.Data.QuotaBytes) ||
		receipt.QuotaInodes != p.Data.QuotaInodes {
		return errAppPlatformStageEvidence
	}
	observedMS, observedErr := platformDeleteDataInt(
		receipt.ObservedMS)
	format, formatErr := platformDeleteDataInt(receipt.DataFormat)
	if observedErr != nil || observedMS < 0 || observedMS > now ||
		formatErr != nil || format < 1 || format > p.Data.Format {
		return errAppPlatformStageEvidence
	}
	return nil
}

func platformValidateAdmittedDeleteData(
	e migExec, expected platformOperationLease, facts platformLeaseFacts,
) (platformDeleteDataReviewProjection, error) {
	p, _, err := platformReadDeleteDataPlan(e, expected)
	if err != nil {
		return p, err
	}
	var appID, publisher, desired, phase, epoch string
	var runtimeID sql.NullString
	var generation, revision, policy, fence int64
	if err := e.queryRow(`SELECT app_id,publisher_id,desired_state,phase,
		controller_epoch,runtime_id,generation,revision,policy_revision,
		control_fence FROM app_platform_installations WHERE id=?`,
		expected.installationID).Scan(
		&appID, &publisher, &desired, &phase, &epoch, &runtimeID,
		&generation, &revision, &policy, &fence); err != nil {
		return p, err
	}
	if appID != p.AppID || publisher != p.PublisherID ||
		epoch != p.ControllerEpoch || desired != p.DesiredState ||
		phase != p.Phase || !runtimeID.Valid ||
		runtimeID.String != p.RuntimeID ||
		!facts.runtimeID.Valid || facts.runtimeID.String != p.RuntimeID ||
		generation != p.TargetGeneration ||
		revision < p.InstallationRevision+1 ||
		policy != p.PolicyRevision ||
		fence < p.ControlFence+1 ||
		fence != expected.fence {
		return p, errAppPlatformStageEvidence
	}
	var pending int
	if err := e.queryRow(`SELECT COUNT(*) FROM app_platform_operations
		WHERE installation_id=? AND id<>? AND completed_ms IS NULL`,
		expected.installationID, expected.id).Scan(&pending); err != nil ||
		pending != 0 {
		if err != nil {
			return p, err
		}
		return p, errAppPlatformStageEvidence
	}
	var active int
	if err := e.queryRow(`SELECT COUNT(*) FROM app_platform_activations
		WHERE installation_id=? AND state='active'`,
		expected.installationID).Scan(&active); err != nil || active != 0 {
		if err != nil {
			return p, err
		}
		return p, errAppPlatformStageEvidence
	}
	var activationInstallation, release, namespace, state string
	var activationGeneration, activationFormat int64
	if err := e.queryRow(`SELECT installation_id,release_sha256,
		generation,data_namespace_id,data_format,state
		FROM app_platform_activations WHERE id=?`,
		p.ActivationID).Scan(
		&activationInstallation, &release, &activationGeneration,
		&namespace, &activationFormat, &state); err != nil ||
		activationInstallation != p.InstallationID ||
		release != p.ReleaseSHA256 ||
		activationGeneration != p.Generation ||
		namespace != p.Data.ID ||
		activationFormat != p.Data.Format ||
		state != "retired" {
		return p, errAppPlatformStageEvidence
	}
	var origin string
	var owner sql.NullString
	var dataState string
	var dataFormat, dataRevision, writer, quota, observed int64
	if err := e.queryRow(`SELECT origin_installation_id,
		owner_installation_id,state,data_format,revision,writer_fence,
		quota_bytes,observed_bytes
		FROM app_platform_data_namespaces WHERE id=?`,
		p.Data.ID).Scan(
		&origin, &owner, &dataState, &dataFormat, &dataRevision,
		&writer, &quota, &observed); err != nil ||
		origin != p.InstallationID || owner.Valid ||
		dataState != "deleting" ||
		dataFormat != p.Data.Format ||
		dataRevision != p.Data.Revision+1 ||
		writer != p.Data.WriterFence+1 ||
		quota != p.Data.QuotaBytes ||
		observed < 0 {
		return p, errAppPlatformStageEvidence
	}
	recovery, err := platformDeleteDataRecoverySetsDigest(
		e, p.Data.ID)
	if err != nil || recovery != p.RecoverySetsSHA256 {
		return p, errAppPlatformStageEvidence
	}
	if err := platformValidateDeleteDataSource(
		e, p, p.RuntimeID, facts.now); err != nil {
		return p, err
	}
	return p, nil
}

func (s *Store) advancePlatformDeleteDataControl(
	expected platformOperationLease,
) (platformOperationLease, error) {
	var zero platformOperationLease
	if expected.snapshot.Action != appplatform.ActionDeleteData ||
		!expected.snapshot.Managed {
		return zero, errAppPlatformStageEvidence
	}
	next := appplatform.StageValidate
	switch expected.snapshot.Stage {
	case appplatform.StageQueued:
		next = appplatform.StageValidate
	case appplatform.StageValidate:
		next = appplatform.StageRevoke
	case appplatform.StageRevoke:
		next = appplatform.StageRemoveData
	default:
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
	if _, err := platformValidateAdmittedDeleteData(
		e, expected, facts); err != nil {
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
	changed, err := e.exec(`UPDATE app_platform_operations
		SET stage=?,state_revision=?,row_revision=row_revision+1,updated_ms=?
		WHERE id=? AND installation_id=? AND controller_epoch=?
		AND stage=? AND state_revision=? AND row_revision=? AND fence=?
		AND lease_owner=? AND lease_until_ms=? AND lease_until_ms>?
		AND completed_ms IS NULL`, string(next),
		int64(result.snapshot.Revision), now, expected.id,
		expected.installationID, expected.controllerEpoch,
		string(expected.snapshot.Stage), int64(expected.snapshot.Revision),
		expected.rowRevision, expected.fence, expected.owner,
		expected.leaseUntilMS, now)
	if err := platformLeaseCAS(changed, err); err != nil {
		return zero, err
	}
	detail, _ := json.Marshal(struct {
		Stage appplatform.Stage `json:"stage"`
	}{next})
	if _, err := e.exec(`INSERT INTO audit_log(
		at,actor,actor_ou,action,target_type,target_id,detail,ip)
		VALUES(?,'system',0,'app_platform.operation_stage',
		'app_platform_operation',?,?,'')`,
		timeUnixMilliUTC(now), expected.id, string(detail)); err != nil {
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

func (s *Server) preparePlatformStandaloneDeleteDataIntent(
	ctx context.Context, expected platformOperationLease,
) (platformDeleteDataIntent, error) {
	var zero platformDeleteDataIntent
	if expected.snapshot.Action != appplatform.ActionDeleteData ||
		!expected.snapshot.Managed ||
		expected.snapshot.Stage != appplatform.StageRemoveData {
		return zero, errAppPlatformStageEvidence
	}
	dek, err := s.dek()
	if err != nil {
		return zero, err
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
	runtime, seed, err := s.platformDataRuntime(
		e, expected.controllerEpoch, dek)
	if err != nil {
		return zero, err
	}
	defer clear(seed)
	facts, err := loadPlatformBoundOperationLease(e, expected)
	if err != nil {
		return zero, err
	}
	p, err := platformValidateAdmittedDeleteData(
		e, expected, facts)
	if err != nil {
		return zero, err
	}
	var descriptor []byte
	if err := e.queryRow(`SELECT descriptor
		FROM app_platform_releases WHERE sha256=?`,
		p.ReleaseSHA256).Scan(&descriptor); err != nil {
		return zero, errAppPlatformStageEvidence
	}
	var effectCount int
	if err := e.queryRow(`SELECT COUNT(*)
		FROM app_platform_operation_effects
		WHERE operation_id=? AND stage='remove_data' AND effect_number=1`,
		expected.id).Scan(&effectCount); err != nil || effectCount != 0 {
		return zero, errAppPlatformStageEvidence
	}
	effectID, err := platformNewUUID()
	if err != nil {
		return zero, err
	}
	quotaInodes, err := platformDeleteDataInt(p.Data.QuotaInodes)
	if err != nil {
		return zero, errAppPlatformStageEvidence
	}
	device, err := platformDeleteDataUint(p.Data.Device)
	if err != nil {
		return zero, errAppPlatformStageEvidence
	}
	inode, err := platformDeleteDataUint(p.Data.NamespaceInode)
	if err != nil {
		return zero, errAppPlatformStageEvidence
	}
	project, err := platformDeleteDataUint(p.Data.ProjectID)
	if err != nil || project > math.MaxUint32 {
		return zero, errAppPlatformStageEvidence
	}
	command, err := platformDeleteDataCommand(
		expected, runtime, p.Data.ID, p.ReleaseSHA256, p.Data.Format,
		p.Data.WriterFence+1, int64(project),
		p.Data.QuotaBytes, quotaInodes, device, inode, effectID)
	if err != nil {
		return zero, err
	}
	now, err := finalPlatformOperationLeaseCheck(e, facts)
	if err != nil || now > math.MaxInt64-25000 {
		return zero, errAppPlatformLeaseConflict
	}
	issued := time.Now().UnixMilli()
	if issued < now-1000 || issued > now+1000 {
		return zero, errAppPlatformLeaseConflict
	}
	expires := min(issued+25000, expected.leaseUntilMS-1000)
	if expires <= issued {
		return zero, errAppPlatformLeaseConflict
	}
	command.IssuedMS = platformDeleteDataDecimal(issued)
	command.ExpiresMS = platformDeleteDataDecimal(expires)
	authority := platformRuntimeAuthority(runtime)
	key := ed25519.NewKeyFromSeed(seed)
	raw, signature, err := control.SignDeleteData(
		command, key, authority, issued)
	clear(key)
	if err != nil {
		return zero, err
	}
	if _, err := e.exec(`INSERT INTO app_platform_operation_effects(
		operation_id,stage,effect_number,effect_id,kind,
		payload_sha256,runtime_id,controller_epoch,admitted_fence,
		observed_fence,state,receipt_json,created_ms,updated_ms)
		VALUES(?,'remove_data',1,?,'delete-data',?,?,?,?,0,'prepared',
		'{}',?,?)`, expected.id, effectID, command.PayloadSHA256,
		runtime.id, expected.controllerEpoch, expected.fence,
		facts.now, facts.now); err != nil {
		return zero, err
	}
	if _, err := e.exec(`INSERT INTO audit_log(
		at,actor,actor_ou,action,target_type,target_id,detail,ip)
		VALUES(?,'system',0,'app_platform.data_delete_intent',
		'app_platform_operation',?,?,'')`, timeUnixMilliUTC(facts.now),
		expected.id, string(mustPlatformJSON(struct {
			NamespaceID string `json:"namespace_id"`
			EffectID    string `json:"effect_id"`
		}{p.Data.ID, effectID}))); err != nil {
		return zero, err
	}
	if _, err := finalPlatformOperationLeaseCheck(e, facts); err != nil {
		return zero, err
	}
	if err := tx.Commit(); err != nil {
		return zero, err
	}
	return platformDeleteDataIntent{
		command: command, raw: raw, signature: signature,
		authority: authority, runtime: runtime,
		descriptor: descriptor, sourceOperationID: p.Data.SourceOperationID,
		dataReceiptSHA256: p.Data.SourceReceiptSHA256,
	}, nil
}

func platformDeleteDataUint(value string) (uint64, error) {
	result, err := strconv.ParseUint(value, 10, 64)
	if err != nil || result == 0 ||
		strconv.FormatUint(result, 10) != value {
		return 0, errAppPlatformStageEvidence
	}
	return result, nil
}

func platformDeleteDataInt(value string) (int64, error) {
	result, err := strconv.ParseInt(value, 10, 64)
	if err != nil || strconv.FormatInt(result, 10) != value {
		return 0, errAppPlatformStageEvidence
	}
	return result, nil
}

func platformDeleteDataDecimal(value int64) string {
	return strconv.FormatInt(value, 10)
}

func (s *Server) settlePlatformStandaloneDeleteData(
	ctx context.Context,
	expected platformOperationLease,
	intent platformDeleteDataIntent,
	receipt journal.DeleteDataReceipt,
) (platformOperationLease, error) {
	var zero platformOperationLease
	if expected.snapshot.Action != appplatform.ActionDeleteData ||
		!expected.snapshot.Managed ||
		expected.snapshot.Stage != appplatform.StageRemoveData ||
		!platformDeleteDataReceiptMatchesIntent(receipt, intent) {
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
	p, err := platformValidateAdmittedDeleteData(e, expected, facts)
	if err != nil {
		return zero, err
	}
	var state, digest, receiptRaw, runtime string
	var observed int64
	if err := e.queryRow(`SELECT state,payload_sha256,receipt_json,
		runtime_id,observed_fence FROM app_platform_operation_effects
		WHERE operation_id=? AND stage='remove_data' AND effect_number=1
		AND effect_id=?`, expected.id, intent.command.EffectID).Scan(
		&state, &digest, &receiptRaw, &runtime, &observed); err != nil ||
		state != "prepared" || receiptRaw != "{}" || observed != 0 ||
		digest != intent.command.PayloadSHA256 ||
		runtime != intent.runtime.id {
		return zero, errAppPlatformStageEvidence
	}
	now, err := finalPlatformOperationLeaseCheck(e, facts)
	if err != nil {
		return zero, err
	}
	usedBytes, err := platformDeleteDataInt(receipt.UsedBytes)
	if err != nil || usedBytes < 0 {
		return zero, errAppPlatformStageEvidence
	}
	changed, err := e.exec(`UPDATE app_platform_data_namespaces
		SET state='deleted',observed_bytes=?,deleted_ms=?,revision=revision+1,
		updated_ms=? WHERE id=? AND origin_installation_id=?
		AND owner_installation_id IS NULL AND state='deleting'
		AND revision=? AND writer_fence=?`,
		usedBytes, now, now, p.Data.ID, p.InstallationID,
		p.Data.Revision+1, p.Data.WriterFence+1)
	if err := platformLeaseCAS(changed, err); err != nil {
		return zero, errAppPlatformStageEvidence
	}
	if _, err := e.exec(`UPDATE app_platform_recovery_sets
		SET state='deleted',deleted_ms=?,updated_ms=?
		WHERE data_namespace_id=? AND state<>'deleted'`,
		now, now, p.Data.ID); err != nil {
		return zero, err
	}
	encoded, err := json.Marshal(receipt)
	if err != nil || len(encoded) > 32768 {
		return zero, errAppPlatformStageEvidence
	}
	changed, err = e.exec(`UPDATE app_platform_operation_effects
		SET state='applied',observed_fence=?,receipt_json=?,updated_ms=?
		WHERE operation_id=? AND stage='remove_data' AND effect_number=1
		AND effect_id=? AND state='prepared' AND receipt_json='{}'
		AND observed_fence=0`, expected.fence, string(encoded), now,
		expected.id, intent.command.EffectID)
	if err := platformLeaseCAS(changed, err); err != nil {
		return zero, err
	}
	model, err := appplatform.OperationFromSnapshot(expected.snapshot)
	if err != nil {
		return zero, errAppPlatformStageEvidence
	}
	complete, err := model.Advance(
		expected.snapshot.Revision, appplatform.StageComplete)
	if err != nil {
		return zero, errAppPlatformStageEvidence
	}
	result := expected
	result.snapshot = complete.Snapshot()
	result.rowRevision++
	changed, err = e.exec(`UPDATE app_platform_operations
		SET stage='complete',state_revision=?,row_revision=row_revision+1,
		completed_ms=?,lease_owner=NULL,lease_until_ms=NULL,updated_ms=?
		WHERE id=? AND installation_id=? AND controller_epoch=?
		AND stage=? AND state_revision=? AND row_revision=? AND fence=?
		AND lease_owner=? AND lease_until_ms=? AND lease_until_ms>?
		AND completed_ms IS NULL`,
		int64(result.snapshot.Revision), now, now, expected.id,
		expected.installationID, expected.controllerEpoch,
		string(expected.snapshot.Stage), int64(expected.snapshot.Revision),
		expected.rowRevision, expected.fence, expected.owner,
		expected.leaseUntilMS, now)
	if err := platformLeaseCAS(changed, err); err != nil {
		return zero, err
	}
	if _, err := e.exec(`INSERT INTO audit_log(
		at,actor,actor_ou,action,target_type,target_id,detail,ip)
		VALUES(?,'system',0,'app_platform.data_deleted',
		'app_platform_operation',?,?,'')`, timeUnixMilliUTC(now),
		expected.id, string(mustPlatformJSON(struct {
			NamespaceID   string `json:"namespace_id"`
			Entries       string `json:"entries"`
			AlreadyAbsent bool   `json:"already_absent"`
		}{receipt.NamespaceID, receipt.Entries,
			receipt.AlreadyAbsent}))); err != nil {
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

func (s *Server) finishPlatformDeleteDataForReview(
	ctx context.Context, expected platformOperationLease,
) error {
	if expected.snapshot.Action != appplatform.ActionDeleteData ||
		!expected.snapshot.Managed {
		return errAppPlatformStageEvidence
	}
	tx, err := s.st.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	e := migExec{s: s.st, ex: platformContextTx{Tx: tx, ctx: ctx}}
	if err := lockOUMutation(e); err != nil {
		return err
	}
	facts, err := loadPlatformBoundOperationLease(e, expected)
	if err != nil {
		return err
	}
	if _, err := platformValidateAdmittedDeleteData(
		e, expected, facts); err != nil {
		return err
	}
	now, err := finalPlatformOperationLeaseCheck(e, facts)
	if err != nil {
		return err
	}
	var effects int
	if err := e.queryRow(`SELECT COUNT(*)
		FROM app_platform_operation_effects WHERE operation_id=?`,
		expected.id).Scan(&effects); err != nil {
		return err
	}
	outcome := appplatform.EffectsUnknown
	code := "data_delete_recovery_required"
	if effects == 0 {
		outcome, code = appplatform.EffectsNone, "data_delete_unavailable"
	}
	model, err := appplatform.OperationFromSnapshot(expected.snapshot)
	if err != nil {
		return errAppPlatformStageEvidence
	}
	failed, err := model.Fail(expected.snapshot.Revision, outcome)
	if err != nil {
		return errAppPlatformStageEvidence
	}
	changed, err := e.exec(`UPDATE app_platform_operations
		SET stage=?,state_revision=?,row_revision=row_revision+1,
		error_code=?,completed_ms=?,updated_ms=?,lease_owner=NULL,
		lease_until_ms=NULL WHERE id=? AND installation_id=?
		AND controller_epoch=? AND stage=? AND state_revision=?
		AND row_revision=? AND fence=? AND lease_owner=?
		AND lease_until_ms=? AND lease_until_ms>? AND completed_ms IS NULL`,
		string(failed.Snapshot().Stage), int64(failed.Snapshot().Revision),
		code, now, now, expected.id, expected.installationID,
		expected.controllerEpoch, string(expected.snapshot.Stage),
		int64(expected.snapshot.Revision), expected.rowRevision,
		expected.fence, expected.owner, expected.leaseUntilMS, now)
	if err := platformLeaseCAS(changed, err); err != nil {
		return err
	}
	if _, err := e.exec(`INSERT INTO audit_log(
		at,actor,actor_ou,action,target_type,target_id,detail,ip)
		VALUES(?,'system',0,'app_platform.data_delete_failed',
		'app_platform_operation',?,'{}','')`, timeUnixMilliUTC(now),
		expected.id); err != nil {
		return err
	}
	if _, err := finalPlatformOperationLeaseCheck(e, facts); err != nil {
		return err
	}
	return tx.Commit()
}
