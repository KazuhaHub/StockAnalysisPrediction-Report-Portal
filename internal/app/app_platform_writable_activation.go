package app

import (
	"bytes"
	"context"
	"encoding/json"
	"math"
	"strconv"
	"time"
	"unicode/utf8"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
)

type platformWritableActivationMetadata struct {
	frontendSHA, documentSHA string
	audience                 []platformInstallAudience
}

func validPlatformWritableBootForActivation(o platformWritableBootObservation, lease platformOperationLease, i platformWritableRegistrationIntent) bool {
	age := time.Since(o.receivedAt)
	delivery, _ := json.Marshal(i.receipt)
	return i.applied && !o.receivedAt.IsZero() && age >= 0 && age <= 3*time.Second && o.lease == lease && samePlatformManagedRuntime(o.runtime, i.start.writer.runtime) && len(o.bootID) > 0 && len(o.bootID) <= 128 && utf8.ValidString(o.bootID) && o.nativeID == i.start.running.NativeID && o.deliverySHA256 == platformDigest(delivery) && o.documentSHA256 == i.command.DocumentSHA256 && o.startSHA256 == i.command.RunningReceiptSHA256
}
func loadPlatformWritableActivationMetadata(e migExec, lease platformOperationLease, i platformWritableRegistrationIntent, now int64) (platformWritableActivationMetadata, error) {
	var zero platformWritableActivationMetadata
	if !validPlatformWritableActivateStage(lease) || !i.applied {
		return zero, errAppPlatformStageEvidence
	}
	var archive []byte
	var raw, digest string
	if err := e.queryRow(`SELECT p.plan_json,p.plan_sha256,f.content FROM app_platform_operations o JOIN app_platform_review_plans p ON p.id=o.review_plan_id JOIN app_platform_releases r ON r.sha256=p.target_release_sha256 JOIN app_platform_frontend_artifacts f ON f.sha256=r.frontend_sha256 WHERE o.id=?`, lease.id).Scan(&raw, &digest, &archive); err != nil {
		return zero, err
	}
	var plan platformInstallProjection
	if json.Unmarshal([]byte(raw), &plan) != nil || plan.Format != platformWritableInstallFormat || plan.ControllerEpoch != lease.controllerEpoch || plan.ReleaseSHA256 != i.start.writer.data.ReleaseSHA256 || plan.Runtime == nil || plan.Runtime.RuntimeID != i.start.writer.runtime.id || platformDigest([]byte(raw)) != digest {
		return zero, errAppPlatformStageEvidence
	}
	canonical, _ := json.Marshal(plan)
	parsed, err := appplatform.ParseDescriptor(i.start.writer.descriptor)
	if err != nil || !bytes.Equal(canonical, []byte(raw)) || parsed.SHA256 != plan.ReleaseSHA256 || parsed.Manifest.ID != plan.AppID || parsed.Manifest.Publisher != plan.PublisherID || installRequirementsForTarget(parsed.Manifest, plan.Format, plan.Runtime) != nil || validateManagedInstallTarget(e, plan.Runtime, lease.controllerEpoch) != nil {
		return zero, errAppPlatformStageEvidence
	}
	document, err := appplatform.BuildFrontendDocument(parsed.Manifest, archive)
	if err != nil {
		return zero, errAppPlatformStageEvidence
	}
	if err := validatePlatformInstallAudience(e, plan.Audience, now); err != nil {
		return zero, err
	}
	return platformWritableActivationMetadata{frontendSHA: document.SourceSHA256, documentSHA: document.DocumentSHA256, audience: plan.Audience}, nil
}

// Development continuation only. It verifies a real current boot before the
// atomic Portal transition; no native side effect occurs inside the transaction.
func (s *Server) activatePlatformWritableInstall(ctx context.Context, lease platformOperationLease, endpoint platformRuntimeEndpoint) (platformActivationResult, error) {
	i, err := s.preparePlatformWritableRegistration(ctx, lease)
	if err != nil {
		return platformActivationResult{}, err
	}
	defer i.registration.Close()
	if !i.applied {
		return platformActivationResult{}, errAppPlatformStageEvidence
	}
	boot, err := s.probePlatformWritableBackendBoot(ctx, lease, endpoint)
	if err != nil {
		return platformActivationResult{}, err
	}
	return s.commitPlatformWritableActivation(ctx, lease, i, boot)
}
func (s *Server) commitPlatformWritableActivation(ctx context.Context, lease platformOperationLease, intent platformWritableRegistrationIntent, boot platformWritableBootObservation) (platformActivationResult, error) {
	var zero platformActivationResult
	intent = clonePlatformWritableRegistrationIntent(intent)
	if !validPlatformWritableActivateStage(lease) || lease.rowRevision == math.MaxInt64 || !validPlatformWritableBootForActivation(boot, lease, intent) {
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
	if err := lockPlatformPublisherTrust(e); err != nil {
		return zero, err
	}
	runtime, seed, err := s.platformDataRuntime(e, lease.controllerEpoch, dek)
	if err != nil {
		return zero, err
	}
	defer clear(seed)
	facts, err := loadPlatformBoundOperationLease(e, lease)
	if err != nil {
		return zero, err
	}
	if facts.installationRevision == math.MaxInt64 {
		return zero, errAppPlatformStageEvidence
	}
	trust, err := loadPlatformPublisherTrust(e)
	if err != nil {
		return zero, err
	}
	if err := validatePlatformInstallAuthority(e, lease, trust, facts.now); err != nil {
		return zero, err
	}
	current, _, err := loadPlatformWritableRegistration(e, lease, runtime, seed, dek, facts.now, false)
	if err != nil {
		return zero, err
	}
	defer current.registration.Close()
	if !samePlatformWritableRegistrationIntent(intent, current) || !validPlatformWritableBootForActivation(boot, lease, current) {
		return zero, errAppPlatformStageEvidence
	}
	metadata, err := loadPlatformWritableActivationMetadata(e, lease, current, facts.now)
	if err != nil {
		return zero, err
	}
	now, err := finalPlatformOperationLeaseCheck(e, facts)
	if err != nil {
		return zero, err
	}
	committedAt := now
	w := current.start.writer
	changed, err := e.exec(`UPDATE app_platform_activations SET state='active',updated_ms=? WHERE id=? AND installation_id=? AND release_sha256=? AND data_namespace_id=? AND generation=1 AND policy_revision=1 AND state='candidate' AND readiness_evidence=? AND observed_ms=? AND created_ms=? AND updated_ms=?`, now, w.resourceID, lease.installationID, w.data.ReleaseSHA256, w.namespaceID, w.activationReadiness, w.activationObserved.Int64, w.activationCreated, w.activationUpdated)
	if err := platformLeaseCAS(changed, err); err != nil {
		return zero, err
	}
	if err := insertPlatformInitialAudience(e, lease.installationID, metadata.audience, now); err != nil {
		return zero, err
	}
	changed, err = e.exec(`UPDATE app_platform_installations SET phase='ready',revision=revision+1,updated_ms=? WHERE id=? AND desired_state='enabled' AND phase='pending' AND generation=1 AND policy_revision=1 AND revision=? AND control_fence=? AND controller_epoch=? AND runtime_id=?`, now, lease.installationID, facts.installationRevision, lease.fence, lease.controllerEpoch, runtime.id)
	if err := platformLeaseCAS(changed, err); err != nil {
		return zero, err
	}
	model, err := appplatform.OperationFromSnapshot(lease.snapshot)
	if err != nil {
		return zero, errAppPlatformStageEvidence
	}
	next, err := model.Advance(lease.snapshot.Revision, appplatform.StageComplete)
	if err != nil {
		return zero, errAppPlatformStageEvidence
	}
	changed, err = e.exec(`UPDATE app_platform_operations SET stage='complete',state_revision=?,row_revision=row_revision+1,lease_owner=NULL,lease_until_ms=NULL,completed_ms=?,updated_ms=? WHERE id=? AND installation_id=? AND stage='activate' AND state_revision=? AND row_revision=? AND controller_epoch=? AND fence=? AND lease_owner=? AND lease_until_ms=? AND lease_until_ms>? AND completed_ms IS NULL`, int64(next.Snapshot().Revision), now, now, lease.id, lease.installationID, int64(lease.snapshot.Revision), lease.rowRevision, lease.controllerEpoch, lease.fence, lease.owner, lease.leaseUntilMS, now)
	if err := platformLeaseCAS(changed, err); err != nil {
		return zero, err
	}
	audience, _ := json.Marshal(metadata.audience)
	detail, _ := json.Marshal(struct{ ActivationID, BootSHA256, DeliverySHA256, FrontendSHA256, DocumentSHA256, AudienceSHA256 string }{w.resourceID, platformDigest([]byte(boot.bootID)), boot.deliverySHA256, metadata.frontendSHA, metadata.documentSHA, platformDigest(audience)})
	if _, err := e.exec(`INSERT INTO audit_log(at,actor,actor_ou,action,target_type,target_id,detail,ip) VALUES(?,'system',0,'app_platform.writable_install_activated','app_platform_installation',?,?,'')`, time.UnixMilli(now).UTC().Format(time.RFC3339), lease.installationID, string(detail)); err != nil {
		return zero, err
	}
	now, err = finalPlatformOperationLeaseCheck(e, facts)
	if err != nil {
		return zero, err
	}
	if err := validatePlatformInstallAudience(e, metadata.audience, now); err != nil {
		return zero, err
	}
	if err := platformVerifyInstallAuthorityAtCommit(e, lease, trust, now); err != nil {
		return zero, err
	}
	expectedActive := clonePlatformWritableRegistrationIntent(current)
	expectedActive.start.writer.activationUpdated = committedAt
	format, _ := strconv.ParseInt(w.data.DataFormat, 10, 64)
	active, err := s.loadPlatformWritableBackendHistory(e, platformBackendHistoryBinding{installation: lease.installationID, activation: w.resourceID, release: w.data.ReleaseSHA256, namespace: w.namespaceID, epoch: lease.controllerEpoch, format: format, fence: lease.fence, observed: w.activationObserved.Int64, readiness: w.activationReadiness}, runtime, seed, dek, now)
	if err != nil {
		return zero, err
	}
	defer active.registration.Close()
	if !samePlatformWritableRegistrationIntent(expectedActive, active) {
		return zero, errAppPlatformStageEvidence
	}
	if !validPlatformWritableBootForActivation(boot, lease, current) {
		return zero, errAppPlatformStageEvidence
	}
	if _, err := control.VerifyWritableBackendRegistration(intent.raw, intent.signature, current.start.writer.authority, time.Now().UnixMilli()); err != nil {
		return zero, errAppPlatformStageEvidence
	}
	if err := tx.Commit(); err != nil {
		return zero, err
	}
	return platformActivationResult{activationID: w.resourceID, installationID: lease.installationID, snapshot: next.Snapshot()}, nil
}
