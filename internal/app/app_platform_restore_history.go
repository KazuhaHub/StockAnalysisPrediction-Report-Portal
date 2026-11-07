package app

import (
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/journal"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/transport"
)

// platformCompletedRestoreAt reports whether the installation's operation for the generation is a completed
// roll back: a restore from a snapshot checkpoint, as opposed to a recovery resume. It is a routing decision
// only; the loader re-proves everything it relies on.
func platformCompletedRestoreAt(e migExec, installation string, generation int64) (bool, error) {
	var n int
	if err := e.queryRow(`SELECT COUNT(*) FROM app_platform_operations o JOIN app_platform_review_plans p ON p.id=o.review_plan_id
		WHERE o.installation_id=? AND o.action='restore' AND o.managed=1 AND o.target_generation=? AND o.stage='complete'
		AND o.completed_ms IS NOT NULL AND p.plan_json NOT LIKE ?`, installation, generation, platformResumePlanPrefix()).Scan(&n); err != nil {
		return false, err
	}
	return n > 0, nil
}

// loadPlatformCompletedRestoreCheckpoint reads the snapshot receipt a completed roll back restored from. It
// repeats the plan and receipt bindings of the live restore, but nothing about the recovery set's or the failed
// update's present state: retention may have deleted the archive since, and the receipt is immutable.
func loadPlatformCompletedRestoreCheckpoint(e migExec, lease platformOperationLease) (string, journal.CheckpointReceipt, error) {
	var zero journal.CheckpointReceipt
	fail := func(reason string) (string, journal.CheckpointReceipt, error) {
		return "", zero, fmt.Errorf("%w: completed restore %s", errAppPlatformStageEvidence, reason)
	}
	var raw, sha, planInstallation, planEpoch string
	var source, target int64
	if err := e.queryRow(`SELECT p.plan_json,p.plan_sha256,p.installation_id,p.controller_epoch,o.source_generation,o.target_generation
		FROM app_platform_operations o JOIN app_platform_review_plans p ON p.id=o.review_plan_id
		WHERE o.id=? AND o.installation_id=? AND o.action='restore' AND o.managed=1 AND p.action='restore'`, lease.id, lease.installationID).
		Scan(&raw, &sha, &planInstallation, &planEpoch, &source, &target); err != nil {
		return fail("plan row")
	}
	var plan platformManagedRestorePlan
	if json.Unmarshal([]byte(raw), &plan) != nil {
		return fail("plan")
	}
	canonical, marshalErr := json.Marshal(plan)
	if marshalErr != nil || string(canonical) != raw || platformDigest([]byte(raw)) != sha || plan.Format != platformManagedRestoreFormat ||
		plan.InstallationID != lease.installationID || planInstallation != lease.installationID ||
		planEpoch != lease.controllerEpoch || plan.ControllerEpoch != lease.controllerEpoch ||
		plan.SourceGeneration != source || plan.TargetGeneration != target || target != int64(lease.snapshot.Generation) ||
		!platformValidUUID(plan.RecoverySetID) || !platformValidUUID(plan.CheckpointOperationID) || !platformValidUUID(plan.CheckpointEffectID) ||
		!platformValidUUID(plan.CheckpointID) || !platformValidSHA(plan.ReleaseSHA256) || !platformValidSHA(plan.ManifestSHA256) || plan.ArchiveBytes <= 0 {
		return fail("plan binding")
	}
	var n int
	if err := e.queryRow(`SELECT COUNT(*) FROM app_platform_recovery_sets WHERE id=? AND installation_id=? AND operation_id=? AND checkpoint_id=?
		AND manifest_sha256=? AND bytes=?`, plan.RecoverySetID, lease.installationID, plan.CheckpointOperationID, plan.CheckpointID,
		plan.ManifestSHA256, plan.ArchiveBytes).Scan(&n); err != nil || n != 1 {
		return fail("recovery set")
	}
	var effectID, payload, receiptRaw string
	if err := e.queryRow(`SELECT effect_id,payload_sha256,receipt_json FROM app_platform_operation_effects
		WHERE operation_id=? AND stage='snapshot' AND effect_number=1 AND state='applied'`, plan.CheckpointOperationID).
		Scan(&effectID, &payload, &receiptRaw); err != nil || effectID != plan.CheckpointEffectID || !platformValidSHA(payload) {
		return fail("checkpoint effect")
	}
	receipt, err := journal.ParseCheckpointReceipt([]byte(receiptRaw))
	if err != nil || receipt.EffectID != effectID || receipt.PayloadSHA256 != payload || receipt.OperationID != plan.CheckpointOperationID ||
		receipt.CheckpointID != plan.CheckpointID || receipt.ManifestSHA256 != plan.ManifestSHA256 || receipt.ReleaseSHA256 != plan.ReleaseSHA256 ||
		mustPlatformCheckpointInt(receipt.ArchiveBytes) != plan.ArchiveBytes ||
		receipt.TargetGeneration != strconv.FormatUint(lease.snapshot.Generation, 10) ||
		!platformCheckpointReceiptMatchesSourceOperation(receipt, lease.installationID) {
		return fail("checkpoint receipt")
	}
	return plan.RecoverySetID, receipt, nil
}

// platformRestoreHistorySource rebuilds the source a roll back's start was issued against. The failed update's
// source is no longer the installation's latest history, so it is read from what the snapshot and the start's
// own running receipt recorded; the start command digest the caller compares against is what proves it.
func platformRestoreHistorySource(e migExec, running journal.UpdateStartRunningReceipt, checkpoint journal.CheckpointReceipt, runtime platformRuntimeRegistration, generation int64) (platformCheckpointSource, error) {
	var zero platformCheckpointSource
	pre := running.Claim.Preflight
	if pre == nil {
		return zero, fmt.Errorf("%w: restore history preflight", errAppPlatformStageEvidence)
	}
	var descriptor []byte
	if err := e.queryRow(`SELECT descriptor FROM app_platform_releases WHERE sha256=?`, checkpoint.ReleaseSHA256).Scan(&descriptor); err != nil {
		return zero, fmt.Errorf("%w: restore history release", errAppPlatformStageEvidence)
	}
	parsed, err := appplatform.ParseDescriptor(descriptor)
	if err != nil || parsed.SHA256 != checkpoint.ReleaseSHA256 {
		return zero, fmt.Errorf("%w: restore history descriptor", errAppPlatformStageEvidence)
	}
	profile := transport.ManagedRuntimeProfile{
		Protocol: transport.ManagedRunnerProtocol, Platform: pre.Platform, BackendProtocol: transport.ManagedBackendProtocol,
		SandboxProfile: control.CandidateSandboxProfile, RootFilesystemUUID: pre.RootFilesystemUUID, RootInode: pre.RootInode,
	}
	if running.Claim.SandboxProfile == control.WritableCandidateSandboxProfile {
		profile.WritableSandboxProfile = control.WritableCandidateSandboxProfile
	}
	target := &platformManagedInstallTarget{
		RuntimeID: runtime.id, DeploymentID: runtime.deploymentID, ControllerEpoch: runtime.controllerEpoch, BindingRevision: runtime.revision,
		ControlRevision: runtime.controlRevision, Fingerprint: runtime.fingerprint, Profile: profile,
	}
	source := platformCheckpointSource{
		operationID: checkpoint.SourceOperationID, startEffectID: checkpoint.SourceStartEffectID, dataEffectID: checkpoint.SourceDataEffectID,
		sourceReceiptSHA256: checkpoint.SourceReceiptSHA256, sourceDataReceiptSHA256: checkpoint.SourceDataReceiptSHA256,
		resourceID: checkpoint.ResourceID, namespaceID: checkpoint.NamespaceID, releaseSHA256: checkpoint.ReleaseSHA256, sourceGeneration: generation - 1,
		projectID: pre.ProjectID, namespaceInode: pre.NamespaceInode, device: pre.Device, dataFormat: checkpoint.DataFormat,
		quotaBytes: pre.QuotaBytes, quotaInodes: pre.QuotaInodes, rootFilesystemUUID: pre.RootFilesystemUUID, rootInode: pre.RootInode,
		writerFence: checkpoint.WriterFence, platform: pre.Platform, sandboxProfile: running.Claim.SandboxProfile,
		appID: parsed.Manifest.ID, publisherID: parsed.Manifest.Publisher, descriptor: descriptor, runtimeTarget: target,
	}
	// A start issued after the namespace's writer fence moved past the checkpoint's names both.
	if running.SourceWriterFence != checkpoint.WriterFence {
		source.currentWriterFence = running.SourceWriterFence
	}
	return source, nil
}

// loadPlatformWritableRestoreBackendHistory reconstructs a completed roll back from the signed start and
// registration evidence the live operation used, so a rolled back installation is admitted exactly like an
// updated one. The fresh envelopes are access proofs only. Any gap in the evidence refuses access.
func (s *Server) loadPlatformWritableRestoreBackendHistory(e migExec, b platformBackendHistoryBinding, runtime platformRuntimeRegistration, seed, dek []byte, now int64) (platformWritableRegistrationIntent, error) {
	var zero platformWritableRegistrationIntent
	fail := func(stage string, err error) (platformWritableRegistrationIntent, error) {
		return zero, fmt.Errorf("restore history %s (%v): %w", stage, err, errAppPlatformLaunchUnavailable)
	}
	if b.generation < 2 {
		return fail("generation", nil)
	}
	var operationID string
	var completed, created, updated, fence int64
	if err := e.queryRow(`SELECT o.id,o.completed_ms,o.created_ms,o.updated_ms,o.fence
		FROM app_platform_operations o JOIN app_platform_review_plans p ON p.id=o.review_plan_id
		WHERE o.installation_id=? AND o.action='restore' AND o.managed=1 AND o.target_generation=? AND o.stage='complete'
		AND o.completed_ms IS NOT NULL AND o.lease_owner IS NULL AND o.lease_until_ms IS NULL AND o.error_code IS NULL
		AND o.controller_epoch=? AND p.controller_epoch=o.controller_epoch AND p.action='restore' AND p.consumed_ms IS NOT NULL
		AND p.target_release_sha256=? AND p.plan_json NOT LIKE ?`,
		b.installation, b.generation, b.epoch, b.release, platformResumePlanPrefix()).
		Scan(&operationID, &completed, &created, &updated, &fence); err != nil ||
		!platformValidUUID(operationID) || created < 0 || completed < created || updated < completed || updated > now ||
		fence < 1 || fence > b.fence {
		return fail("operation", err)
	}
	expected := platformOperationLease{
		id: operationID, installationID: b.installation, controllerEpoch: b.epoch, fence: fence,
		snapshot: appplatform.OperationSnapshot{
			Action: appplatform.ActionRestore, Managed: true, Generation: uint64(b.generation), Stage: appplatform.StageActivate,
		},
	}
	recoverySetID, checkpoint, err := loadPlatformCompletedRestoreCheckpoint(e, expected)
	if err != nil {
		return fail("checkpoint", err)
	}
	var startRaw string
	if err := e.queryRow(`SELECT receipt_json FROM app_platform_operation_effects
		WHERE operation_id=? AND stage='start' AND effect_number=1 AND kind='restore-start' AND state='applied'`, operationID).Scan(&startRaw); err != nil {
		return fail("start effect", err)
	}
	running, err := journal.ParseUpdateStartRunningReceipt([]byte(startRaw))
	if err != nil {
		return fail("running receipt", err)
	}
	source, err := platformRestoreHistorySource(e, running, checkpoint, runtime, b.generation)
	if err != nil {
		return fail("source", err)
	}
	var policy int64
	if err := e.queryRow(`SELECT policy_revision FROM app_platform_activations WHERE installation_id=? AND generation=?`, b.installation, b.generation).Scan(&policy); err != nil || policy < 1 {
		return fail("policy", err)
	}
	plan := platformRestoreStartPlan(expected, checkpoint, recoverySetID, policy, source)
	start, err := loadPlatformCompletedUpdateStartActivation(e, expected, runtime, source, checkpoint, plan)
	if err != nil {
		return fail("activation", err)
	}
	return platformWritableHistoryFromStart(e, b, expected, runtime, seed, dek, now, start, source.descriptor)
}
