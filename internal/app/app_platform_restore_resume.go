package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/journal"
)

// Per-installation resume (doc 168). After a restore_import has placed an installation's data on the
// replacement runtime, a managed restore operation brings the installation back to ready. Its source
// is the applied import rather than a snapshot taken by a failed update: the revoke, quiesce,
// snapshot and restore-data stages advance on evidence alone (the import already performed the data
// effect and nothing runs on the new host yet), and the unchanged restore-start, probe and activate
// stages follow. The operation is admitted while the Portal is paused, and only then.

const platformRestoreResumeFormat = "portal.restore-resume-review/1-dev.1"

type platformRestoreResumePlan struct {
	Format                string                        `json:"format"`
	InstallationID        string                        `json:"installation_id"`
	RecoverySetID         string                        `json:"recovery_set_id"`
	ImportOperationID     string                        `json:"import_operation_id"`
	ImportEffectID        string                        `json:"import_effect_id"`
	ImportReceiptSHA256   string                        `json:"import_receipt_sha256"`
	GrantID               string                        `json:"grant_id"`
	CheckpointOperationID string                        `json:"checkpoint_operation_id"`
	CheckpointEffectID    string                        `json:"checkpoint_effect_id"`
	CheckpointID          string                        `json:"checkpoint_id"`
	NamespaceID           string                        `json:"namespace_id"`
	ReleaseSHA256         string                        `json:"release_sha256"`
	ManifestSHA256        string                        `json:"manifest_sha256"`
	ArchiveBytes          int64                         `json:"archive_bytes"`
	DataFormat            int64                         `json:"data_format"`
	CheckpointWriterFence int64                         `json:"checkpoint_writer_fence"`
	NamespaceWriterFence  int64                         `json:"namespace_writer_fence"`
	RuntimeID             string                        `json:"runtime_id"`
	DeploymentID          string                        `json:"deployment_id"`
	SandboxProfile        string                        `json:"sandbox_profile"`
	SourceGeneration      int64                         `json:"source_generation"`
	TargetGeneration      int64                         `json:"target_generation"`
	PolicyRevision        int64                         `json:"policy_revision"`
	ControllerEpoch       string                        `json:"controller_epoch"`
	Target                *platformManagedInstallTarget `json:"target_runtime"`
}

func platformResumePlanPrefix() string {
	return `{"format":"` + platformRestoreResumeFormat + `",%`
}

// platformOperationIsResume reports whether the operation is a per-installation resume, which is a
// restore whose reviewed plan has the resume format. It is the one switch between the ordinary
// restore evidence (a snapshot of a failed update) and the imported evidence.
func platformOperationIsResume(e migExec, operation string) (bool, error) {
	var n int
	if err := e.queryRow(`SELECT COUNT(*) FROM app_platform_operations o JOIN app_platform_review_plans p ON p.id=o.review_plan_id
		WHERE o.id=? AND o.action='restore' AND p.plan_json LIKE ?`, operation, platformResumePlanPrefix()).Scan(&n); err != nil {
		return false, err
	}
	return n == 1, nil
}

// platformResumeImport is the immutable evidence that an installation's checkpoint was imported: the
// completed restore_import operation, its applied effect and receipt, the consumed grant, and the old
// host's recorded provenance for the recovery set. Nothing here depends on the installation's phase,
// so it is checked identically at admission and at every stage of the operation.
type platformResumeImport struct {
	operationID string
	plan        platformRestoreImportPlan
	effectID    string
	raw         string
	receipt     journal.ImportCheckpointReceipt
	grantID     string
	source      platformImportSource
}

func loadPlatformResumeImport(e migExec, installation string) (platformResumeImport, error) {
	var r platformResumeImport
	fail := func(reason string) (platformResumeImport, error) {
		return r, fmt.Errorf("%w: resume import %s", errAppPlatformStageEvidence, reason)
	}
	var planRaw, planSHA, operationEpoch string
	var sourceGeneration, targetGeneration int64
	if err := e.queryRow(`SELECT o.id,p.plan_json,p.plan_sha256,o.controller_epoch,o.source_generation,o.target_generation
		FROM app_platform_operations o JOIN app_platform_review_plans p ON p.id=o.review_plan_id
		WHERE o.installation_id=? AND o.action='restore_import' AND o.managed=1 AND o.stage='complete' AND p.action='restore_import'
		AND o.completed_ms IS NOT NULL AND o.error_code IS NULL AND o.lease_owner IS NULL
		ORDER BY o.completed_ms DESC,o.id DESC LIMIT 1`, installation).
		Scan(&r.operationID, &planRaw, &planSHA, &operationEpoch, &sourceGeneration, &targetGeneration); err != nil {
		return fail("operation")
	}
	plan := &r.plan
	if json.Unmarshal([]byte(planRaw), plan) != nil {
		return fail("plan")
	}
	canonical, _ := json.Marshal(plan)
	if string(canonical) != planRaw || platformDigest([]byte(planRaw)) != planSHA || plan.Format != platformRestoreImportFormat ||
		plan.InstallationID != installation || plan.ControllerEpoch != operationEpoch || plan.SourceGeneration != sourceGeneration ||
		plan.TargetGeneration != targetGeneration || !platformValidUUID(plan.RecoverySetID) || !platformValidUUID(plan.RuntimeID) {
		return fail("plan binding")
	}
	lock := ""
	if e.s.driver == "postgres" {
		lock = " FOR UPDATE"
	}
	var payload, kind, state, effectRuntime, effectEpoch string
	var effectUpdated int64
	if err := e.queryRow(`SELECT effect_id,payload_sha256,receipt_json,kind,state,runtime_id,controller_epoch,updated_ms
		FROM app_platform_operation_effects WHERE operation_id=? AND stage='restore_data' AND effect_number=1`+lock, r.operationID).
		Scan(&r.effectID, &payload, &r.raw, &kind, &state, &effectRuntime, &effectEpoch, &effectUpdated); err != nil ||
		kind != "import-checkpoint" || state != "applied" || effectRuntime != plan.RuntimeID || effectEpoch != plan.ControllerEpoch {
		return fail("effect")
	}
	receipt, err := journal.ParseImportCheckpointReceipt([]byte(r.raw))
	if err != nil || receipt.Format != platformImportReceiptFormat || receipt.State != "imported" || receipt.EffectID != r.effectID ||
		receipt.PayloadSHA256 != payload || receipt.OperationID != r.operationID || receipt.InstallationID != installation ||
		receipt.ControllerEpoch != plan.ControllerEpoch || receipt.DeploymentID != plan.DeploymentID || receipt.RecoverySetID != plan.RecoverySetID ||
		receipt.CheckpointID != plan.CheckpointID || receipt.NamespaceID != plan.NamespaceID || receipt.ReleaseSHA256 != plan.ReleaseSHA256 ||
		receipt.ManifestSHA256 != plan.ManifestSHA256 || receipt.TargetGeneration != strconv.FormatInt(targetGeneration, 10) {
		return fail("receipt")
	}
	r.receipt = receipt
	// The grant that paid for this import: consumed together with the receipt, by this runner, and the
	// only grant of the recovery set that is not revoked.
	var consumed sql.NullInt64
	var grantState, grantRuntime, grantEpoch string
	if err := e.queryRow(`SELECT id,state,consumed_ms,runtime_id,controller_epoch FROM app_platform_import_grants
		WHERE recovery_set_id=? AND state<>'revoked'`+lock, plan.RecoverySetID).Scan(&r.grantID, &grantState, &consumed, &grantRuntime, &grantEpoch); err != nil ||
		grantState != "consumed" || !consumed.Valid || consumed.Int64 != effectUpdated || grantRuntime != plan.RuntimeID ||
		grantEpoch != plan.ControllerEpoch || r.grantID != receipt.GrantID {
		return fail("grant")
	}
	source, err := loadPlatformImportSource(e, nil, installation, plan.RecoverySetID)
	if err != nil {
		return r, err
	}
	if source.checkpointID != plan.CheckpointID || source.manifest != plan.ManifestSHA256 || source.release != plan.ReleaseSHA256 ||
		source.namespaceID != plan.NamespaceID || source.archiveBytes != plan.ArchiveBytes || source.dataFormat != plan.DataFormat ||
		source.writerFence != plan.WriterFence || source.operationID != plan.CheckpointOperationID || source.checkpoint.EffectID != plan.CheckpointEffectID {
		return fail("recovery set")
	}
	r.source = source
	return r, nil
}

func (r platformResumeImport) quotaBytes() string { return r.receipt.QuotaBytes }

// platformResumeSandboxProfile reads, from the old host's applied start receipt, which sandbox profile
// the app ran under. Candidate receipts do not name one: they are the default profile.
func platformResumeSandboxProfile(e migExec, startEffect string) (string, error) {
	var raw string
	if err := e.queryRow(`SELECT receipt_json FROM app_platform_operation_effects WHERE effect_id=? AND state='applied'`, startEffect).Scan(&raw); err != nil {
		return "", errAppPlatformStageEvidence
	}
	var probe struct {
		Claim struct {
			SandboxProfile string `json:"sandbox_profile"`
		} `json:"claim"`
	}
	if json.Unmarshal([]byte(raw), &probe) != nil {
		return "", errAppPlatformStageEvidence
	}
	switch probe.Claim.SandboxProfile {
	case "", control.CandidateSandboxProfile:
		return control.CandidateSandboxProfile, nil
	case control.WritableCandidateSandboxProfile:
		return control.WritableCandidateSandboxProfile, nil
	}
	return "", errAppPlatformStageEvidence
}

// platformRecoveryTrust is the publisher trust a resume verifies a release against. A restore leaves
// every publisher disabled and recovery admits no trust change, so the signature and key validity are
// verified as they would be for an enabled publisher, but only for a publisher the restore recorded as
// enabled before it ran. A publisher that was disabled before the backup, or any publisher when the
// record is missing, stays untrusted. The publisher itself stays disabled and is re-enabled by the
// ordinary trust review once recovery has ended.
func platformRecoveryTrust(e migExec, trust appplatform.PublisherTrust) (appplatform.PublisherTrust, error) {
	var raw string
	if err := e.queryRow(`SELECT v FROM meta WHERE k=?`, platformRestoreTrustedPublishersKey).Scan(&raw); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			raw = "[]"
		} else {
			return trust, err
		}
	}
	var ids []string
	if json.Unmarshal([]byte(raw), &ids) != nil {
		return trust, errAppPlatformStageEvidence
	}
	trusted := make(map[string]bool, len(ids))
	for _, id := range ids {
		trusted[id] = true
	}
	result := trust
	result.Publishers = append([]appplatform.TrustedPublisher(nil), trust.Publishers...)
	for i := range result.Publishers {
		result.Publishers[i].Enabled = trusted[result.Publishers[i].ID]
	}
	return result, nil
}

// platformResumeTargetMatches reports whether a managed-runtime target describes the replacement
// runtime on the data root the import landed on.
func platformResumeTargetMatches(e migExec, target *platformManagedInstallTarget, plan platformRestoreResumePlan, receipt journal.ImportCheckpointReceipt) bool {
	return target != nil && target.RuntimeID == plan.RuntimeID && target.DeploymentID == plan.DeploymentID &&
		target.ControllerEpoch == plan.ControllerEpoch && validateManagedInstallTarget(e, target, plan.ControllerEpoch) == nil &&
		target.NetworkPolicy == "" && target.NetworkStreamPolicy == "" &&
		target.Profile.RootFilesystemUUID == receipt.Claim.RootFilesystemUUID && target.Profile.RootInode == receipt.Claim.RootInode &&
		(plan.SandboxProfile != control.WritableCandidateSandboxProfile ||
			target.Profile.WritableSandboxProfile == control.WritableCandidateSandboxProfile)
}

func platformResumePlanFor(installation string, imp platformResumeImport, runtime platformRuntimeRegistration, target *platformManagedInstallTarget,
	sandbox string, generation, policy, namespaceWriter int64) platformRestoreResumePlan {
	sum := platformDigest([]byte(imp.raw))
	return platformRestoreResumePlan{
		Format: platformRestoreResumeFormat, InstallationID: installation, RecoverySetID: imp.plan.RecoverySetID,
		ImportOperationID: imp.operationID, ImportEffectID: imp.effectID, ImportReceiptSHA256: sum, GrantID: imp.grantID,
		CheckpointOperationID: imp.source.operationID, CheckpointEffectID: imp.source.checkpoint.EffectID, CheckpointID: imp.source.checkpointID,
		NamespaceID: imp.source.namespaceID, ReleaseSHA256: imp.source.release, ManifestSHA256: imp.source.manifest, ArchiveBytes: imp.source.archiveBytes,
		DataFormat: imp.source.dataFormat, CheckpointWriterFence: imp.source.writerFence, NamespaceWriterFence: namespaceWriter,
		RuntimeID: runtime.id, DeploymentID: runtime.deploymentID, SandboxProfile: sandbox,
		SourceGeneration: generation, TargetGeneration: generation + 1, PolicyRevision: policy, ControllerEpoch: runtime.controllerEpoch, Target: target,
	}
}

type platformResumeCandidate struct {
	plan                                                platformRestoreResumePlan
	appID, publisherID                                  string
	installationRevision, policyRevision, trustRevision int64
}

// loadPlatformResumeCandidate is the admission check: a paused, restored installation that now runs on
// the replacement runtime with its checkpoint imported, nothing else in flight, and a release whose
// signature still verifies.
func loadPlatformResumeCandidate(e migExec, installation string, runtime platformRuntimeRegistration, target *platformManagedInstallTarget, now int64) (platformResumeCandidate, error) {
	var result platformResumeCandidate
	if !platformValidUUID(installation) || target == nil || now <= 0 {
		return result, errAppPlatformStageEvidence
	}
	lock := ""
	if e.s.driver == "postgres" {
		lock = " FOR UPDATE"
	}
	var desired, phase, epoch string
	var generation int64
	var runtimeID sql.NullString
	if err := e.queryRow(`SELECT app_id,publisher_id,desired_state,phase,generation,revision,policy_revision,controller_epoch,runtime_id
		FROM app_platform_installations WHERE id=?`+lock, installation).
		Scan(&result.appID, &result.publisherID, &desired, &phase, &generation, &result.installationRevision, &result.policyRevision, &epoch, &runtimeID); err != nil {
		return result, errAppPlatformStageEvidence
	}
	if desired != "disabled" || phase != "review_required" || epoch != runtime.controllerEpoch || !runtimeID.Valid || runtimeID.String != runtime.id {
		return result, errAppPlatformStageEvidence
	}
	imp, err := loadPlatformResumeImport(e, installation)
	if err != nil {
		return result, err
	}
	if imp.plan.RuntimeID != runtime.id || imp.plan.DeploymentID != runtime.deploymentID || imp.plan.ControllerEpoch != epoch ||
		imp.plan.SourceGeneration != generation || imp.plan.TargetGeneration != generation+1 {
		return result, errAppPlatformStageEvidence
	}
	var recoveryState, recoveryEpoch, namespaceState string
	var namespaceOwner sql.NullString
	var namespaceWriter int64
	if err := e.queryRow(`SELECT r.state,r.controller_epoch,n.state,n.owner_installation_id,n.writer_fence
		FROM app_platform_recovery_sets r JOIN app_platform_data_namespaces n ON n.id=r.data_namespace_id
		WHERE r.id=? AND r.installation_id=? AND n.origin_installation_id=?`+lock, imp.plan.RecoverySetID, installation, installation).
		Scan(&recoveryState, &recoveryEpoch, &namespaceState, &namespaceOwner, &namespaceWriter); err != nil ||
		recoveryState != "review_required" || recoveryEpoch != epoch || namespaceState != "review_required" || namespaceOwner.Valid ||
		namespaceWriter < imp.source.writerFence {
		return result, errAppPlatformStageEvidence
	}
	var pending, activations int
	if err := e.queryRow(`SELECT COUNT(*) FROM app_platform_operations WHERE installation_id=? AND completed_ms IS NULL`, installation).Scan(&pending); err != nil {
		return result, err
	}
	if pending != 0 {
		return result, errAppPlatformOperationConflict
	}
	if err := e.queryRow(`SELECT COUNT(*) FROM app_platform_activations WHERE installation_id=? AND (state IN ('active','candidate') OR generation=?)`,
		installation, generation+1).Scan(&activations); err != nil || activations != 0 {
		return result, errAppPlatformStageEvidence
	}
	sandbox, err := platformResumeSandboxProfile(e, imp.source.checkpoint.SourceStartEffectID)
	if err != nil {
		return result, err
	}
	result.plan = platformResumePlanFor(installation, imp, runtime, target, sandbox, generation, result.policyRevision, namespaceWriter)
	if !platformResumeTargetMatches(e, target, result.plan, imp.receipt) {
		return result, errAppPlatformStageEvidence
	}
	if err := e.queryRow(`SELECT revision FROM app_platform_publishers WHERE id=?`, result.publisherID).Scan(&result.trustRevision); err != nil || result.trustRevision < 1 {
		return result, errAppPlatformStageEvidence
	}
	trust, err := loadPlatformPublisherTrust(e)
	if err != nil {
		return result, err
	}
	recovery, err := platformRecoveryTrust(e, trust)
	if err != nil {
		return result, err
	}
	verified, err := platformVerifyStoredRelease(e, imp.source.release, result.trustRevision, recovery, now)
	if err != nil || verified.appID != result.appID || verified.publisherID != result.publisherID || !verified.managed {
		return result, errAppPlatformStageEvidence
	}
	return result, nil
}

func (s *Server) createPlatformRestoreResumeReview(ctx context.Context, session platformActorSession, installation string, target *platformManagedInstallTarget) (platformRemovalReview, error) {
	if !platformValidUUID(installation) || target == nil {
		return platformRemovalReview{}, errAppPlatformPlanChanged
	}
	dek, err := s.dek()
	if err != nil {
		return platformRemovalReview{}, err
	}
	var result platformRemovalReview
	err = s.withPlatformRecoveryTransactionContext(ctx, session, func(e migExec, actor platformActorFacts, control platformActorControl) error {
		runtime, seed, err := s.platformDataRuntime(e, control.epoch, dek)
		if err != nil {
			return err
		}
		defer clear(seed)
		candidate, err := loadPlatformResumeCandidate(e, installation, runtime, target, control.nowMS)
		if err != nil {
			if errors.Is(err, errAppPlatformOperationConflict) {
				return err
			}
			return errAppPlatformPlanChanged
		}
		control, err = platformRefreshActor(e, session, actor, control)
		if err != nil {
			return err
		}
		raw, err := json.Marshal(candidate.plan)
		if err != nil || len(raw) > 65536 || control.nowMS > math.MaxInt64-900000 {
			return errAppPlatformPlanChanged
		}
		id, err := platformNewUUID()
		if err != nil {
			return err
		}
		digest, expires := platformDigest(raw), control.nowMS+900000
		if _, err := e.exec(`INSERT INTO app_platform_review_plans(id,installation_id,app_id,publisher_id,action,source_release_sha256,target_release_sha256,
			installation_revision,policy_revision,trust_revision,controller_epoch,actor_scope,username,account_created,account_generation,authority_fingerprint,
			plan_sha256,plan_json,expires_ms,consumed_ms,created_ms,updated_ms)
			VALUES(?,?,?,?,'restore',?,?,?,?,?,?,?,?,?,?,?,?,?,?,NULL,?,?)`,
			id, installation, candidate.appID, candidate.publisherID, candidate.plan.ReleaseSHA256, candidate.plan.ReleaseSHA256,
			candidate.installationRevision, candidate.policyRevision, candidate.trustRevision, control.epoch, actor.scope, actor.username,
			actor.created, actor.generation, actor.authorityFingerprint, digest, string(raw), expires, control.nowMS, control.nowMS); err != nil {
			return err
		}
		result = platformRemovalReview{ID: id, SHA256: digest, JSON: string(raw), ExpiresMS: expires}
		_, err = platformRefreshActor(e, session, actor, control)
		return err
	})
	if err != nil {
		return platformRemovalReview{}, err
	}
	return result, nil
}

// consumePlatformRestoreResumeReview is the one transaction that stages the installation for the
// restore stages and queues the operation. The installation moves from awaiting review to
// recovery-required, the state every restore stage expects, and nothing else changes yet.
func (s *Server) consumePlatformRestoreResumeReview(ctx context.Context, session platformActorSession, planID, planSHA, key string) (platformRemovalCommit, error) {
	var result platformRemovalCommit
	if !platformValidUUID(planID) || !platformValidSHA(planSHA) || !platformValidUUID(key) {
		return result, errAppPlatformPlanChanged
	}
	dek, err := s.dek()
	if err != nil {
		return result, err
	}
	request, _ := json.Marshal([]string{"portal-operation-commit/v1", planID, planSHA})
	requestDigest, keyDigest := platformDigest(request), platformIdempotencySHA(key)
	err = s.withPlatformRecoveryTransactionContext(ctx, session, func(e migExec, actor platformActorFacts, control platformActorControl) error {
		previous, exists, err := platformFindRemovalCommit(e, actor.scope, keyDigest, requestDigest)
		if err != nil {
			return err
		}
		if exists {
			resume, err := platformOperationIsResume(e, previous.OperationID)
			if err != nil || !resume {
				return errAppPlatformOperationConflict
			}
			result = previous
			return nil
		}
		lock := ""
		if e.s.driver == "postgres" {
			lock = " FOR UPDATE"
		}
		var raw, digest, scope, username, created, authority, action, epoch, appID, publisher string
		var installation sql.NullString
		var expires, revision, policyRevision, trustRevision, generation int64
		var consumed sql.NullInt64
		if err := e.queryRow(`SELECT plan_json,plan_sha256,actor_scope,username,account_created,account_generation,authority_fingerprint,expires_ms,consumed_ms,
			installation_revision,policy_revision,trust_revision,controller_epoch,action,installation_id,app_id,publisher_id
			FROM app_platform_review_plans WHERE id=?`+lock, planID).Scan(&raw, &digest, &scope, &username, &created, &generation, &authority, &expires, &consumed,
			&revision, &policyRevision, &trustRevision, &epoch, &action, &installation, &appID, &publisher); err != nil {
			return errAppPlatformPlanChanged
		}
		if consumed.Valid {
			return errAppPlatformOperationConflict
		}
		control, err = platformRefreshActor(e, session, actor, control)
		if err != nil {
			return err
		}
		if expires <= control.nowMS || action != "restore" || !installation.Valid || scope != actor.scope || username != actor.username ||
			created != actor.created || generation != actor.generation || authority != actor.authorityFingerprint || epoch != control.epoch ||
			digest != planSHA || platformDigest([]byte(raw)) != planSHA {
			return errAppPlatformPlanChanged
		}
		runtime, seed, err := s.platformDataRuntime(e, epoch, dek)
		if err != nil {
			return err
		}
		defer clear(seed)
		var plan platformRestoreResumePlan
		if json.Unmarshal([]byte(raw), &plan) != nil || plan.Format != platformRestoreResumeFormat || plan.InstallationID != installation.String {
			return errAppPlatformPlanChanged
		}
		if canonical, marshalErr := json.Marshal(plan); marshalErr != nil || string(canonical) != raw {
			return errAppPlatformPlanChanged
		}
		candidate, err := loadPlatformResumeCandidate(e, installation.String, runtime, plan.Target, control.nowMS)
		if err != nil {
			return errAppPlatformPlanChanged
		}
		expected, err := json.Marshal(candidate.plan)
		if err != nil || string(expected) != raw || candidate.appID != appID || candidate.publisherID != publisher ||
			candidate.installationRevision != revision || candidate.policyRevision != policyRevision || candidate.trustRevision != trustRevision {
			return errAppPlatformPlanChanged
		}
		now := control.nowMS
		changed, err := e.exec(`UPDATE app_platform_installations SET desired_state='enabled',phase='recovery_required',revision=revision+1,updated_ms=?
			WHERE id=? AND runtime_id=? AND revision=? AND desired_state='disabled' AND phase='review_required' AND controller_epoch=?`,
			now, installation.String, runtime.id, revision, epoch)
		if err := platformLeaseCAS(changed, err); err != nil {
			return err
		}
		operationID, err := platformNewUUID()
		if err != nil {
			return err
		}
		if _, err := e.exec(`INSERT INTO app_platform_operations(id,installation_id,review_plan_id,actor_scope,idempotency_sha256,request_sha256,action,managed,
			source_generation,target_generation,stage,state_revision,row_revision,lease_owner,lease_until_ms,fence,controller_epoch,error_code,completed_ms,created_ms,updated_ms)
			VALUES(?,?,?,?,?,?,'restore',1,?,?,'revoke',3,1,NULL,NULL,0,?,NULL,NULL,?,?)`,
			operationID, installation.String, planID, actor.scope, keyDigest, requestDigest, plan.SourceGeneration, plan.TargetGeneration, epoch, now, now); err != nil {
			return err
		}
		changed, err = e.exec(`UPDATE app_platform_review_plans SET consumed_ms=?,updated_ms=? WHERE id=? AND consumed_ms IS NULL AND expires_ms>? AND plan_sha256=?`,
			now, now, planID, now, planSHA)
		if err := platformLeaseCAS(changed, err); err != nil {
			return err
		}
		detail, _ := json.Marshal(struct {
			Plan, Action string
			RecoverySet  string `json:"recovery_set_id"`
			ImportID     string `json:"import_operation_id"`
			Generation   int64
			Managed      bool
		}{planID, "restore", plan.RecoverySetID, plan.ImportOperationID, plan.TargetGeneration, true})
		if _, err := e.exec(`INSERT INTO audit_log(at,actor,actor_ou,action,target_type,target_id,detail,ip) VALUES(?,?,?,'app_platform.operation_admitted','app_platform_operation',?,?,'')`,
			time.UnixMilli(now).UTC().Format(time.RFC3339), actor.username, actor.ouID, operationID, string(detail)); err != nil {
			return err
		}
		result = platformRemovalCommit{OperationID: operationID, InstallationID: installation.String, Generation: plan.TargetGeneration, Revision: 3, Stage: appplatform.StageRevoke}
		_, err = platformRefreshActor(e, session, actor, control)
		return err
	})
	if err != nil {
		return platformRemovalCommit{}, err
	}
	return result, nil
}

// platformResumeEvidence is everything an in-flight resume stage must re-prove: the reviewed plan, the
// import it rests on, and that the installation and its runtime are still what the plan names.
type platformResumeEvidence struct {
	plan       platformRestoreResumePlan
	imp        platformResumeImport
	descriptor []byte
	appID      string
	publisher  string
	policy     int64
}

func loadPlatformResumeEvidence(e migExec, lease platformOperationLease) (platformResumeEvidence, error) {
	var ev platformResumeEvidence
	fail := func(reason string) (platformResumeEvidence, error) {
		return ev, fmt.Errorf("%w: resume %s", errAppPlatformStageEvidence, reason)
	}
	var raw, sha, planInstallation, planEpoch string
	var source, target int64
	if err := e.queryRow(`SELECT p.plan_json,p.plan_sha256,p.installation_id,p.controller_epoch,o.source_generation,o.target_generation
		FROM app_platform_operations o JOIN app_platform_review_plans p ON p.id=o.review_plan_id
		WHERE o.id=? AND o.installation_id=? AND o.action='restore' AND o.managed=1 AND p.action='restore'`, lease.id, lease.installationID).
		Scan(&raw, &sha, &planInstallation, &planEpoch, &source, &target); err != nil {
		return fail("plan row")
	}
	plan := &ev.plan
	if json.Unmarshal([]byte(raw), plan) != nil {
		return fail("plan")
	}
	canonical, _ := json.Marshal(plan)
	if string(canonical) != raw || platformDigest([]byte(raw)) != sha || plan.Format != platformRestoreResumeFormat ||
		plan.InstallationID != lease.installationID || planInstallation != lease.installationID || planEpoch != lease.controllerEpoch ||
		plan.ControllerEpoch != lease.controllerEpoch || plan.SourceGeneration != source || plan.TargetGeneration != target ||
		source != int64(lease.snapshot.Generation)-1 || target != int64(lease.snapshot.Generation) {
		return fail("plan binding")
	}
	imp, err := loadPlatformResumeImport(e, lease.installationID)
	if err != nil {
		return ev, err
	}
	fresh := platformResumePlanFor(lease.installationID, imp, platformRuntimeRegistration{id: plan.RuntimeID, deploymentID: plan.DeploymentID, controllerEpoch: plan.ControllerEpoch},
		plan.Target, plan.SandboxProfile, plan.SourceGeneration, plan.PolicyRevision, plan.NamespaceWriterFence)
	freshRaw, err := json.Marshal(fresh)
	if err != nil || string(freshRaw) != raw {
		return fail("import evidence changed")
	}
	if !platformResumeTargetMatches(e, plan.Target, *plan, imp.receipt) {
		return fail("runtime target")
	}
	var runtimeID sql.NullString
	var release string
	if err := e.queryRow(`SELECT i.runtime_id,i.policy_revision,i.app_id,i.publisher_id FROM app_platform_installations i WHERE i.id=?`, lease.installationID).
		Scan(&runtimeID, &ev.policy, &ev.appID, &ev.publisher); err != nil || !runtimeID.Valid || runtimeID.String != plan.RuntimeID || ev.policy != plan.PolicyRevision {
		return fail("installation")
	}
	release = plan.ReleaseSHA256
	if err := e.queryRow(`SELECT descriptor FROM app_platform_releases WHERE sha256=?`, release).Scan(&ev.descriptor); err != nil {
		return fail("release")
	}
	parsed, err := appplatform.ParseDescriptor(ev.descriptor)
	if err != nil || parsed.SHA256 != release || parsed.Manifest.ID != ev.appID || parsed.Manifest.Publisher != ev.publisher || parsed.Manifest.Backend == nil {
		return fail("descriptor")
	}
	ev.imp = imp
	return ev, nil
}

// loadPlatformResumeCheckpoint expresses the applied import in the shape the restore start consumes. It
// is not a snapshot receipt: the operation and effect it names are the import's, so the start command
// carries the import as its checkpoint source, and the data identity is the receiving host's.
func loadPlatformResumeCheckpoint(e migExec, lease platformOperationLease) (string, string, journal.CheckpointReceipt, int64, error) {
	ev, err := loadPlatformResumeEvidence(e, lease)
	if err != nil {
		return "", "", journal.CheckpointReceipt{}, 0, err
	}
	r, cp := ev.imp.receipt, ev.imp.source.checkpoint
	checkpoint := journal.CheckpointReceipt{
		Format: cp.Format, DeploymentID: r.DeploymentID, ControllerEpoch: r.ControllerEpoch, InstallationID: r.InstallationID,
		OperationID: r.OperationID, TargetGeneration: r.TargetGeneration, Stage: "restore_data", EffectID: r.EffectID, PayloadSHA256: r.PayloadSHA256,
		CheckpointID: r.CheckpointID, NamespaceID: r.NamespaceID, ResourceID: cp.ResourceID, ReleaseSHA256: r.ReleaseSHA256,
		SourceOperationID: cp.SourceOperationID, SourceStartEffectID: cp.SourceStartEffectID, SourceReceiptSHA256: cp.SourceReceiptSHA256,
		SourceDataEffectID: cp.SourceDataEffectID, SourceDataReceiptSHA256: cp.SourceDataReceiptSHA256,
		ProjectID: r.ProjectID, NamespaceInode: r.NamespaceInode, Device: r.Device, DataFormat: r.DataFormat, WriterFence: r.WriterFence,
		ArchiveBytes: r.ArchiveBytes, Entries: r.Entries, ManifestSHA256: r.ManifestSHA256, ObservedMS: r.ObservedMS, SettledMS: r.SettledMS,
	}
	return ev.plan.RecoverySetID, ev.imp.effectID, checkpoint, ev.imp.source.archiveBytes, nil
}

// loadPlatformResumeCheckpointSource is the start source of a resume: the old host's lineage, which
// this Portal recorded, together with the receiving host's data identity from the import receipt.
func loadPlatformResumeCheckpointSource(e migExec, lease platformOperationLease, runtime platformRuntimeRegistration) (platformCheckpointSource, error) {
	var zero platformCheckpointSource
	ev, err := loadPlatformResumeEvidence(e, lease)
	if err != nil {
		return zero, err
	}
	var pending int
	if err := e.queryRow(`SELECT COUNT(*) FROM app_platform_operations WHERE installation_id=? AND completed_ms IS NULL`, lease.installationID).Scan(&pending); err != nil || pending != 1 {
		return zero, errAppPlatformStageEvidence
	}
	return platformResumeCheckpointSourceFor(ev, runtime)
}

// platformResumeCheckpointSourceFor builds the start source from proven resume evidence. It does not
// look at what else is in flight, so a completed resume is read back through it as well.
func platformResumeCheckpointSourceFor(ev platformResumeEvidence, runtime platformRuntimeRegistration) (platformCheckpointSource, error) {
	plan, r, cp := ev.plan, ev.imp.receipt, ev.imp.source.checkpoint
	if runtime.id != plan.RuntimeID || runtime.deploymentID != plan.DeploymentID || runtime.controllerEpoch != plan.ControllerEpoch {
		return platformCheckpointSource{}, errAppPlatformStageEvidence
	}
	return platformCheckpointSource{
		operationID: cp.SourceOperationID, startEffectID: cp.SourceStartEffectID, dataEffectID: cp.SourceDataEffectID,
		sourceReceiptSHA256: cp.SourceReceiptSHA256, sourceDataReceiptSHA256: cp.SourceDataReceiptSHA256,
		resourceID: cp.ResourceID, namespaceID: plan.NamespaceID, releaseSHA256: plan.ReleaseSHA256, sourceGeneration: plan.SourceGeneration,
		projectID: r.ProjectID, namespaceInode: r.NamespaceInode, device: r.Device, dataFormat: r.DataFormat,
		quotaBytes: r.QuotaBytes, quotaInodes: r.QuotaInodes, rootFilesystemUUID: r.Claim.RootFilesystemUUID, rootInode: r.Claim.RootInode,
		writerFence: r.WriterFence, currentWriterFence: strconv.FormatInt(plan.NamespaceWriterFence, 10),
		platform: plan.Target.Profile.Platform, sandboxProfile: plan.SandboxProfile, appID: ev.appID, publisherID: ev.publisher,
		descriptor: ev.descriptor, runtimeTarget: plan.Target,
	}, nil
}

// advancePlatformResumeStage moves a resume across one of its stages that has no native effect. Each
// move re-proves the import, so a revoked grant or an altered receipt stops the operation where it is.
func (s *Server) advancePlatformResumeStage(ctx context.Context, expected platformOperationLease, next appplatform.Stage) (platformOperationLease, error) {
	var zero platformOperationLease
	allowed := map[appplatform.Stage]appplatform.Stage{
		appplatform.StageRevoke: appplatform.StageQuiesce, appplatform.StageQuiesce: appplatform.StageSnapshot, appplatform.StageSnapshot: appplatform.StageRestoreData,
	}
	if expected.snapshot.Action != appplatform.ActionRestore || !expected.snapshot.Managed || expected.rowRevision == math.MaxInt64 ||
		allowed[expected.snapshot.Stage] != next {
		return zero, fmt.Errorf("resume stage transition %s -> %s: %w", expected.snapshot.Stage, next, errAppPlatformStageEvidence)
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
	if err := lockPlatformPublisherTrust(e); err != nil {
		return zero, err
	}
	facts, err := loadPlatformBoundOperationLease(e, expected)
	if err != nil {
		return zero, err
	}
	if err := requirePlatformResumeStaged(e, expected); err != nil {
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
	now, err := finalPlatformOperationLeaseCheck(e, facts)
	if err != nil {
		return zero, err
	}
	result := expected
	result.snapshot, result.rowRevision = advanced.Snapshot(), expected.rowRevision+1
	if err := updatePlatformResumeStage(e, expected, result, now); err != nil {
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

// requirePlatformResumeStaged proves, for the stages before restore-data, that the installation is
// staged and that nothing has started: no activation exists, the namespace still awaits review and no
// native effect was ever issued for the operation. Restore-data is the first stage that changes them.
func requirePlatformResumeStaged(e migExec, lease platformOperationLease) error {
	ev, err := loadPlatformResumeEvidence(e, lease)
	if err != nil {
		return err
	}
	return requirePlatformResumeStagedForData(e, lease, ev)
}

func updatePlatformResumeStage(e migExec, expected, result platformOperationLease, now int64) error {
	changed, err := e.exec(`UPDATE app_platform_operations SET stage=?,state_revision=?,row_revision=row_revision+1,updated_ms=?
		WHERE id=? AND installation_id=? AND controller_epoch=? AND stage=? AND state_revision=? AND row_revision=? AND fence=?
		AND lease_owner=? AND lease_until_ms=? AND lease_until_ms>? AND completed_ms IS NULL`,
		string(result.snapshot.Stage), int64(result.snapshot.Revision), now, expected.id, expected.installationID, expected.controllerEpoch,
		string(expected.snapshot.Stage), int64(expected.snapshot.Revision), expected.rowRevision, expected.fence, expected.owner, expected.leaseUntilMS, now)
	if err := platformLeaseCAS(changed, err); err != nil {
		return err
	}
	detail, _ := json.Marshal(struct {
		Stage appplatform.Stage `json:"stage"`
	}{result.snapshot.Stage})
	_, err = e.exec(`INSERT INTO audit_log(at,actor,actor_ou,action,target_type,target_id,detail,ip) VALUES(?,'system',0,'app_platform.operation_stage','app_platform_operation',?,?,'')`,
		time.UnixMilli(now).UTC().Format(time.RFC3339), expected.id, string(detail))
	return err
}

// settlePlatformResumeRestoreData is the resume's restore-data stage. The import already placed the
// data, so there is no effect to issue: the stage gives the namespace back to the installation, makes the
// recovery set usable again, prepares the candidate activation the start will bring up, and advances.
func (s *Server) settlePlatformResumeRestoreData(ctx context.Context, expected platformOperationLease) (platformOperationLease, error) {
	var zero platformOperationLease
	if expected.snapshot.Action != appplatform.ActionRestore || !expected.snapshot.Managed || expected.snapshot.Stage != appplatform.StageRestoreData ||
		expected.rowRevision == math.MaxInt64 {
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
	if err := lockPlatformPublisherTrust(e); err != nil {
		return zero, err
	}
	facts, err := loadPlatformBoundOperationLease(e, expected)
	if err != nil {
		return zero, err
	}
	ev, err := loadPlatformResumeEvidence(e, expected)
	if err != nil {
		return zero, err
	}
	if err := requirePlatformResumeStagedForData(e, expected, ev); err != nil {
		return zero, err
	}
	model, err := appplatform.OperationFromSnapshot(expected.snapshot)
	if err != nil {
		return zero, errAppPlatformStageEvidence
	}
	advanced, err := model.Advance(expected.snapshot.Revision, appplatform.StageStart)
	if err != nil {
		return zero, errAppPlatformStageEvidence
	}
	now, err := finalPlatformOperationLeaseCheck(e, facts)
	if err != nil {
		return zero, err
	}
	plan, receipt := ev.plan, ev.imp.receipt
	used, _ := platformDecimal(receipt.UsedBytes, false)
	changed, err := e.exec(`UPDATE app_platform_recovery_sets SET state='verified',updated_ms=?
		WHERE id=? AND installation_id=? AND data_namespace_id=? AND checkpoint_id=? AND manifest_sha256=? AND state='review_required' AND controller_epoch=?`,
		now, plan.RecoverySetID, expected.installationID, plan.NamespaceID, plan.CheckpointID, plan.ManifestSHA256, expected.controllerEpoch)
	if err := platformLeaseCAS(changed, err); err != nil {
		return zero, err
	}
	changed, err = e.exec(`UPDATE app_platform_data_namespaces SET state='active',owner_installation_id=?,observed_bytes=?,revision=revision+1,updated_ms=?
		WHERE id=? AND origin_installation_id=? AND state='review_required' AND owner_installation_id IS NULL AND writer_fence=?`,
		expected.installationID, used, now, plan.NamespaceID, expected.installationID, plan.NamespaceWriterFence)
	if err := platformLeaseCAS(changed, err); err != nil {
		return zero, err
	}
	activation, err := platformNewUUID()
	if err != nil {
		return zero, err
	}
	if _, err := e.exec(`INSERT INTO app_platform_activations(id,installation_id,release_sha256,generation,policy_revision,data_namespace_id,data_format,state,readiness_evidence,observed_ms,created_ms,updated_ms)
		VALUES(?,?,?,?,?,?,?,'candidate','{}',NULL,?,?)`,
		activation, expected.installationID, plan.ReleaseSHA256, plan.TargetGeneration, plan.PolicyRevision, plan.NamespaceID, plan.DataFormat, now, now); err != nil {
		return zero, err
	}
	result := expected
	result.snapshot, result.rowRevision = advanced.Snapshot(), expected.rowRevision+1
	if err := updatePlatformResumeStage(e, expected, result, now); err != nil {
		return zero, err
	}
	detail, _ := json.Marshal(struct {
		RecoverySetID string `json:"recovery_set_id"`
		CheckpointID  string `json:"checkpoint_id"`
		ManifestSHA   string `json:"manifest_sha256"`
		Entries       int64  `json:"entries"`
	}{plan.RecoverySetID, plan.CheckpointID, plan.ManifestSHA256, func() int64 { n, _ := platformDecimal(receipt.Entries, false); return n }()})
	if _, err := e.exec(`INSERT INTO audit_log(at,actor,actor_ou,action,target_type,target_id,detail,ip) VALUES(?,'system',0,'app_platform.restore_verified','app_platform_operation',?,?,'')`,
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

func requirePlatformResumeStagedForData(e migExec, lease platformOperationLease, ev platformResumeEvidence) error {
	var desired, phase string
	var generation int64
	var activations, effects int
	var namespaceState string
	var owner sql.NullString
	if err := e.queryRow(`SELECT desired_state,phase,generation FROM app_platform_installations WHERE id=?`, lease.installationID).Scan(&desired, &phase, &generation); err != nil ||
		desired != "enabled" || phase != "recovery_required" || generation != ev.plan.SourceGeneration {
		return fmt.Errorf("%w: resume installation is not staged", errAppPlatformStageEvidence)
	}
	if err := e.queryRow(`SELECT COUNT(*) FROM app_platform_activations WHERE installation_id=? AND state IN ('active','candidate')`,
		lease.installationID).Scan(&activations); err != nil || activations != 0 {
		return fmt.Errorf("%w: an activation exists during resume", errAppPlatformStageEvidence)
	}
	if err := e.queryRow(`SELECT COUNT(*) FROM app_platform_operation_effects WHERE operation_id=?`, lease.id).Scan(&effects); err != nil || effects != 0 {
		return fmt.Errorf("%w: resume stage carries native work", errAppPlatformStageEvidence)
	}
	if err := e.queryRow(`SELECT state,owner_installation_id FROM app_platform_data_namespaces WHERE id=?`, ev.plan.NamespaceID).Scan(&namespaceState, &owner); err != nil ||
		namespaceState != "review_required" || owner.Valid {
		return fmt.Errorf("%w: resume namespace is not awaiting review", errAppPlatformStageEvidence)
	}
	return nil
}

// runPlatformRestoreResumeStage runs the stages of a resume that carry no native effect. The start,
// probe and activate stages are the ordinary restore's and are not handled here.
func (s *Server) runPlatformRestoreResumeStage(ctx context.Context, expected platformOperationLease) (platformOperationLease, bool, error) {
	switch expected.snapshot.Stage {
	case appplatform.StageRevoke:
		lease, err := s.advancePlatformResumeStage(ctx, expected, appplatform.StageQuiesce)
		return lease, true, err
	case appplatform.StageQuiesce:
		lease, err := s.advancePlatformResumeStage(ctx, expected, appplatform.StageSnapshot)
		return lease, true, err
	case appplatform.StageSnapshot:
		lease, err := s.advancePlatformResumeStage(ctx, expected, appplatform.StageRestoreData)
		return lease, true, err
	case appplatform.StageRestoreData:
		lease, err := s.settlePlatformResumeRestoreData(ctx, expected)
		return lease, true, err
	}
	return platformOperationLease{}, false, nil
}

// claimNextPlatformRestoreResume selects the next resume, and only on a paused Portal.
func (s *Server) claimNextPlatformRestoreResume(ctx context.Context, worker string) (platformInstallSelection, error) {
	var zero platformInstallSelection
	if !platformValidUUID(worker) {
		return zero, errAppPlatformLeaseConflict
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
	if err := lockPlatformPublisherTrust(e); err != nil {
		return zero, err
	}
	var epoch, paused string
	if err := e.queryRow(`SELECT COALESCE((SELECT v FROM meta WHERE k='app_platform_controller_epoch'),''),COALESCE((SELECT v FROM meta WHERE k='app_platform_restore_paused'),'')`).Scan(&epoch, &paused); err != nil || epoch == "" {
		return zero, errAppPlatformControlPaused
	}
	if paused != "1" {
		return zero, nil
	}
	now, err := platformDatabaseNow(e)
	if err != nil {
		return zero, err
	}
	rows, err := e.query(`SELECT o.id FROM app_platform_operations o
		JOIN app_platform_installations i ON i.id=o.installation_id JOIN app_platform_review_plans p ON p.id=o.review_plan_id
		WHERE o.action='restore' AND o.managed=1 AND o.completed_ms IS NULL
		AND o.stage IN ('revoke','quiesce','snapshot','restore_data','start','probe','activate')
		AND (o.lease_until_ms IS NULL OR o.lease_until_ms<=?)
		AND i.desired_state='enabled' AND i.phase='recovery_required' AND i.controller_epoch=? AND o.controller_epoch=?
		AND p.plan_json LIKE ? ORDER BY o.created_ms,o.id LIMIT 32`, now, epoch, epoch, platformResumePlanPrefix())
	if err != nil {
		return zero, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return zero, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return zero, err
	}
	for _, id := range ids {
		facts, err := loadPlatformOperationLease(e, id)
		if err != nil {
			if errors.Is(err, errAppPlatformLeaseConflict) {
				continue
			}
			return zero, err
		}
		old := facts.lease
		if old.leaseUntilMS > facts.now || old.snapshot.Action != appplatform.ActionRestore || !old.snapshot.Managed {
			continue
		}
		evidenceErr := error(nil)
		if _, _, _, _, err := loadPlatformResumeCheckpoint(e, old); err != nil {
			if !errors.Is(err, errAppPlatformStageEvidence) {
				return zero, err
			}
			evidenceErr = err
		}
		lease, err := writePlatformOperationLease(e, facts, worker, nil)
		if err != nil {
			return zero, err
		}
		detail, _ := json.Marshal(struct {
			Worker string `json:"worker_id"`
		}{worker})
		if _, err := e.exec(`INSERT INTO audit_log(at,actor,actor_ou,action,target_type,target_id,detail,ip) VALUES(?,'system',0,'app_platform.restore_claimed','app_platform_operation',?,?,'')`,
			time.UnixMilli(facts.now).UTC().Format(time.RFC3339), id, string(detail)); err != nil {
			return zero, err
		}
		check := facts
		check.lease = lease
		if _, err := finalPlatformOperationLeaseCheck(e, check); err != nil {
			return zero, err
		}
		if err := tx.Commit(); err != nil {
			return zero, err
		}
		if evidenceErr != nil {
			// The evidence of this resume no longer loads, so it can never run. It is leased properly
			// above and ended for review here, which frees the claim for the resumes behind it.
			if err := s.finishPlatformRestoreResumeForReview(ctx, lease); err != nil {
				return zero, err
			}
			return zero, nil
		}
		return platformInstallSelection{lease: lease, operationID: id}, nil
	}
	return zero, nil
}

// finishPlatformRestoreResumeForReview ends a resume that cannot go on and puts the installation back
// where a resume review finds it: disabled and awaiting review, with the recovery set and the
// namespace awaiting review again and the candidate activation gone. It deliberately does not
// re-prove the import. A resume whose grant or receipt was lost must still be endable, and it ends by
// returning everything it staged, which needs only the reviewed plan. An effect that was issued but
// never settled is marked unknown, so the operation is retained as needing recovery rather than as a
// clean failure. A start that was reserved and not settled is the exception: its staged rows are kept
// and the installation stays recovery_required until the runner settles the effect.
func (s *Server) finishPlatformRestoreResumeForReview(ctx context.Context, expected platformOperationLease) error {
	if expected.snapshot.Action != appplatform.ActionRestore || !expected.snapshot.Managed || expected.id == "" ||
		expected.snapshot.Stage == appplatform.StageComplete || expected.snapshot.Stage == appplatform.StageFailed ||
		expected.snapshot.Stage == appplatform.StageRecoveryRequired {
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
	if err := lockPlatformPublisherTrust(e); err != nil {
		return err
	}
	facts, err := loadPlatformBoundOperationLease(e, expected)
	if err != nil {
		return err
	}
	var raw string
	if err := e.queryRow(`SELECT p.plan_json FROM app_platform_operations o JOIN app_platform_review_plans p ON p.id=o.review_plan_id
		WHERE o.id=? AND o.installation_id=? AND o.action='restore' AND p.action='restore'`, expected.id, expected.installationID).Scan(&raw); err != nil {
		return errAppPlatformStageEvidence
	}
	var plan platformRestoreResumePlan
	if json.Unmarshal([]byte(raw), &plan) != nil || plan.Format != platformRestoreResumeFormat || plan.InstallationID != expected.installationID ||
		plan.ControllerEpoch != expected.controllerEpoch || plan.TargetGeneration != int64(expected.snapshot.Generation) {
		return errAppPlatformStageEvidence
	}
	var effects int
	if err := e.queryRow(`SELECT COUNT(*) FROM app_platform_operation_effects WHERE operation_id=?`, expected.id).Scan(&effects); err != nil {
		return err
	}
	var unsettled int
	if err := e.queryRow(`SELECT COUNT(*) FROM app_platform_operation_effects WHERE operation_id=? AND state IN ('prepared','unknown') AND receipt_json<>'{}'`, expected.id).Scan(&unsettled); err != nil {
		return err
	}
	model, err := appplatform.OperationFromSnapshot(expected.snapshot)
	if err != nil {
		return errAppPlatformStageEvidence
	}
	outcome, code, target := appplatform.EffectsNone, "restore_resume_failed", "failed"
	if effects != 0 {
		outcome, code, target = appplatform.EffectsUnknown, "restore_resume_recovery_required", "recovery_required"
	}
	ended, err := model.Fail(expected.snapshot.Revision, outcome)
	if err != nil {
		return errAppPlatformStageEvidence
	}
	now, err := finalPlatformOperationLeaseCheck(e, facts)
	if err != nil {
		return err
	}
	changed, err := e.exec(`UPDATE app_platform_operations SET stage=?,state_revision=?,row_revision=row_revision+1,error_code=?,completed_ms=?,updated_ms=?,lease_owner=NULL,lease_until_ms=NULL
		WHERE id=? AND installation_id=? AND controller_epoch=? AND stage=? AND state_revision=? AND row_revision=? AND fence=?
		AND lease_owner=? AND lease_until_ms=? AND lease_until_ms>? AND completed_ms IS NULL`,
		target, int64(ended.Snapshot().Revision), code, now, now, expected.id, expected.installationID, expected.controllerEpoch,
		string(expected.snapshot.Stage), int64(expected.snapshot.Revision), expected.rowRevision, expected.fence, expected.owner,
		expected.leaseUntilMS, now)
	if err := platformLeaseCAS(changed, err); err != nil {
		return err
	}
	if effects != 0 {
		if _, err := e.exec(`UPDATE app_platform_operation_effects SET state='unknown',updated_ms=? WHERE operation_id=? AND state='prepared'`, now, expected.id); err != nil {
			return err
		}
	}
	// A start that was reserved may have been executed, and the Portal holds no record that it was not.
	// Until the runner settles that effect the activation, the namespace and the installation stay
	// exactly as the start left them: returning them to review would admit a second start over a
	// candidate that may already be running.
	if unsettled > 0 {
		detail, _ := json.Marshal(struct {
			Stage appplatform.Stage `json:"stage"`
			Code  string            `json:"code"`
		}{expected.snapshot.Stage, code})
		if _, err := e.exec(`INSERT INTO audit_log(at,actor,actor_ou,action,target_type,target_id,detail,ip) VALUES(?,'system',0,'app_platform.restore_resume_ended','app_platform_operation',?,?,'')`,
			time.UnixMilli(now).UTC().Format(time.RFC3339), expected.id, string(detail)); err != nil {
			return err
		}
		if _, err := finalPlatformOperationLeaseCheck(e, facts); err != nil {
			return err
		}
		return tx.Commit()
	}
	if err := returnPlatformResumeToReview(e, expected.installationID, expected.controllerEpoch, plan, now); err != nil {
		return err
	}
	detail, _ := json.Marshal(struct {
		Stage appplatform.Stage `json:"stage"`
		Code  string            `json:"code"`
	}{expected.snapshot.Stage, code})
	if _, err := e.exec(`INSERT INTO audit_log(at,actor,actor_ou,action,target_type,target_id,detail,ip) VALUES(?,'system',0,'app_platform.restore_resume_ended','app_platform_operation',?,?,'')`,
		time.UnixMilli(now).UTC().Format(time.RFC3339), expected.id, string(detail)); err != nil {
		return err
	}
	if _, err := finalPlatformOperationLeaseCheck(e, facts); err != nil {
		return err
	}
	return tx.Commit()
}

// returnPlatformResumeToReview returns everything a resume attempt staged: the candidate activation is
// dropped, the namespace and the recovery set await review again, and the installation is where a resume
// review finds it, disabled and awaiting review. The data stage and the start each change one thing, and
// only while their own conditions still hold, so a stage the attempt never reached changes nothing.
func returnPlatformResumeToReview(e migExec, installation, epoch string, plan platformRestoreResumePlan, now int64) error {
	if _, err := e.exec(`DELETE FROM app_platform_activations WHERE installation_id=? AND generation=? AND state='candidate'`,
		installation, plan.TargetGeneration); err != nil {
		return err
	}
	if _, err := e.exec(`UPDATE app_platform_data_namespaces SET state='review_required',owner_installation_id=NULL,revision=revision+1,updated_ms=?
		WHERE id=? AND origin_installation_id=? AND owner_installation_id=? AND state='active' AND writer_fence=?`,
		now, plan.NamespaceID, installation, installation, plan.NamespaceWriterFence); err != nil {
		return err
	}
	if _, err := e.exec(`UPDATE app_platform_recovery_sets SET state='review_required',updated_ms=?
		WHERE id=? AND installation_id=? AND state='verified' AND controller_epoch=?`,
		now, plan.RecoverySetID, installation, epoch); err != nil {
		return err
	}
	changed, err := e.exec(`UPDATE app_platform_installations SET desired_state='disabled',phase='review_required',revision=revision+1,updated_ms=?
		WHERE id=? AND desired_state='enabled' AND phase='recovery_required' AND generation=? AND controller_epoch=? AND runtime_id=?`,
		now, installation, plan.SourceGeneration, epoch, plan.RuntimeID)
	return platformLeaseCAS(changed, err)
}
