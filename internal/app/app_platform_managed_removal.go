package app

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/journal"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/transport"
)

type platformManagedRemovalIntent struct {
	command           control.ManagedRemovalCommand
	raw, signature    []byte
	authority         control.Authority
	descriptor        []byte
	runtime           platformRuntimeRegistration
	sourceOperationID string
	nativeID          string
}

type platformManagedRemovalSource struct {
	operationID, releaseSHA256, descriptor string
	startEffectID, startReceiptSHA256      string
	generation, fence                      int64
	resourceID, namespaceID, nativeID      string
	platform                               string
}

func (s *Server) advancePlatformManagedRemovalStage(ctx context.Context, expected platformOperationLease, next appplatform.Stage) (platformOperationLease, error) {
	var zero platformOperationLease
	if (expected.snapshot.Action != appplatform.ActionDisable &&
		expected.snapshot.Action != appplatform.ActionUninstall) ||
		!expected.snapshot.Managed ||
		expected.snapshot.Stage != appplatform.StageRevoke ||
		next != appplatform.StageStop {
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
		return zero, fmt.Errorf(
			"managed removal admitted state: %w", err)
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
		string(next), int64(result.snapshot.Revision), now, expected.id, expected.installationID, expected.controllerEpoch, string(expected.snapshot.Stage), int64(expected.snapshot.Revision), expected.rowRevision, expected.fence, expected.owner, expected.leaseUntilMS, now)
	if err := platformLeaseCAS(changed, err); err != nil {
		return zero, err
	}
	detail, _ := json.Marshal(struct {
		Stage appplatform.Stage `json:"stage"`
	}{next})
	if _, err := e.exec(`INSERT INTO audit_log(at,actor,actor_ou,action,target_type,target_id,detail,ip) VALUES(?,'system',0,'app_platform.operation_stage','app_platform_operation',?,?,'')`, time.UnixMilli(now).UTC().Format(time.RFC3339), expected.id, string(detail)); err != nil {
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

func (s *Server) preparePlatformManagedRemovalIntent(ctx context.Context, expected platformOperationLease) (platformManagedRemovalIntent, error) {
	var zero platformManagedRemovalIntent
	if (expected.snapshot.Action != appplatform.ActionDisable &&
		expected.snapshot.Action != appplatform.ActionUninstall) ||
		!expected.snapshot.Managed ||
		expected.snapshot.Stage != appplatform.StageStop &&
			expected.snapshot.Stage != appplatform.StageRemoveCode ||
		expected.snapshot.Stage == appplatform.StageRemoveCode &&
			expected.snapshot.Action == appplatform.ActionDisable {
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
	runtime, seed, err := s.platformDataRuntime(e, expected.controllerEpoch, dek)
	if err != nil {
		return zero, err
	}
	defer clear(seed)
	facts, err := loadPlatformBoundOperationLease(e, expected)
	if err != nil {
		return zero, err
	}
	if err := validatePlatformAdmittedRemoval(e, expected, facts); err != nil {
		return zero, err
	}
	var appID, desired, phase string
	var generation, revision, controlFence int64
	if err := e.queryRow(`SELECT app_id,desired_state,phase,generation,revision,control_fence FROM app_platform_installations WHERE id=?`, expected.installationID).
		Scan(&appID, &desired, &phase, &generation, &revision, &controlFence); err != nil {
		return zero, err
	}
	expectedDesired, expectedPhase := "removed", "pending"
	if expected.snapshot.Action == appplatform.ActionDisable {
		expectedDesired, expectedPhase = "disabled", "disabled"
	}
	if desired != expectedDesired || phase != expectedPhase || generation != int64(expected.snapshot.Generation) || revision < 1 || controlFence != expected.fence {
		return zero, fmt.Errorf(
			"managed removal installation tuple: %w",
			errAppPlatformStageEvidence)
	}
	var effectCount int
	if err := e.queryRow(`SELECT COUNT(*) FROM app_platform_operation_effects WHERE operation_id=? AND stage=?`, expected.id, string(expected.snapshot.Stage)).Scan(&effectCount); err != nil || effectCount != 0 {
		return zero, fmt.Errorf(
			"managed removal prepared effect: %w",
			errAppPlatformStageEvidence)
	}
	sourceGeneration := int64(expected.snapshot.Generation) - 1
	if sourceGeneration < 1 {
		return zero, errAppPlatformStageEvidence
	}
	var sourceOperation, sourceRelease, descriptorRaw string
	var sourceEffectID, sourceReceiptSHA256 string
	var sourceFence int64
	var resourceID, namespaceID, nativeID, platform string
	if sourceGeneration > 1 {
		source, err := loadPlatformManagedRemovalSource(
			e, expected, sourceGeneration, runtime, facts.now)
		if err != nil {
			return zero, err
		}
		sourceOperation = source.operationID
		sourceRelease = source.releaseSHA256
		descriptorRaw = source.descriptor
		sourceEffectID = source.startEffectID
		sourceReceiptSHA256 = source.startReceiptSHA256
		sourceFence = source.fence
		resourceID = source.resourceID
		namespaceID = source.namespaceID
		nativeID = source.nativeID
		platform = source.platform
	} else {
		var sourcePlanRaw, sourcePlanSHA string
		var sourceCompleted int64
		if err := e.queryRow(`SELECT o.id,p.plan_json,p.plan_sha256,p.target_release_sha256,r.descriptor,o.target_generation,o.fence,o.completed_ms
			FROM app_platform_operations o JOIN app_platform_review_plans p ON p.id=o.review_plan_id JOIN app_platform_releases r ON r.sha256=p.target_release_sha256
			WHERE o.installation_id=? AND o.action='install' AND o.managed=1 AND o.stage='complete' AND o.completed_ms IS NOT NULL
			  AND o.lease_owner IS NULL AND o.lease_until_ms IS NULL AND o.error_code IS NULL
			ORDER BY o.target_generation DESC,o.completed_ms DESC LIMIT 1`, expected.installationID).
			Scan(&sourceOperation, &sourcePlanRaw, &sourcePlanSHA, &sourceRelease, &descriptorRaw, &sourceGeneration, &sourceFence, &sourceCompleted); err != nil {
			return zero, errAppPlatformStageEvidence
		}
		descriptor := []byte(descriptorRaw)
		parsed, err := appplatform.ParseDescriptor(descriptor)
		var plan platformInstallProjection
		if err != nil || json.Unmarshal([]byte(sourcePlanRaw), &plan) != nil || platformDigest([]byte(sourcePlanRaw)) != sourcePlanSHA ||
			!platformManagedFormat(plan.Format) || plan.Runtime == nil || plan.ReleaseSHA256 != sourceRelease || parsed.SHA256 != sourceRelease ||
			plan.Runtime.RuntimeID != runtime.id || validateManagedInstallTarget(e, plan.Runtime, expected.controllerEpoch) != nil ||
			sourceGeneration < 1 || sourceFence < 1 || sourceGeneration+1 != int64(expected.snapshot.Generation) || sourceCompleted > facts.now {
			return zero, errAppPlatformStageEvidence
		}
		start, err := loadPlatformManagedHistoryEffect(e, platformManagedSource{operationID: sourceOperation, installationID: expected.installationID, epoch: expected.controllerEpoch, generation: uint64(sourceGeneration), fence: sourceFence}, runtime.id, "start", "start-candidate", facts.now)
		if err != nil {
			return zero, err
		}
		sourceEffectID = start.id
		sourceReceiptSHA256 = platformDigest([]byte(start.receipt))
		switch plan.Format {
		case platformManagedInstallFormat:
			running, err := journal.ParseCandidateRunningReceipt([]byte(start.receipt))
			if err != nil || running.Claim.Preflight == nil || running.Claim.Preflight.Platform != plan.Runtime.Profile.Platform ||
				running.InstallationID != expected.installationID || running.OperationID != sourceOperation || running.ReleaseSHA256 != sourceRelease ||
				running.Generation != strconv.FormatInt(sourceGeneration, 10) {
				return zero, errAppPlatformStageEvidence
			}
			sourceCommand := control.CandidateCommand{
				Format: control.CandidateFormat, DeploymentID: runtime.deploymentID, ControllerEpoch: expected.controllerEpoch,
				ControlRevision: strconv.FormatInt(runtime.controlRevision, 10), InstallationID: expected.installationID,
				OperationID: sourceOperation, TargetGeneration: running.Generation, Stage: "start", EffectNumber: "1",
				EffectID: start.id, Kind: "start-candidate", InstallationFence: running.Claim.AdmittedFence,
				Payload: control.CandidatePayload{ResourceID: running.Claim.ResourceID, DataNamespaceID: running.DataNamespaceID,
					ReleaseSHA256: running.ReleaseSHA256, Platform: running.Claim.Preflight.Platform, DataFormat: running.DataFormat,
					WriterFence: running.WriterFence, SandboxProfile: control.CandidateSandboxProfile},
			}
			sourceCommand.PayloadSHA256, err = control.CandidateLogicalDigest(sourceCommand)
			if err != nil || sourceCommand.PayloadSHA256 != start.digest || !running.MatchesCommand(sourceCommand) {
				return zero, errAppPlatformStageEvidence
			}
			resourceID, namespaceID, nativeID, platform = running.Claim.ResourceID, running.DataNamespaceID, running.NativeID, running.Claim.Preflight.Platform
		case platformWritableInstallFormat:
			source := platformManagedSource{operationID: sourceOperation, installationID: expected.installationID, epoch: expected.controllerEpoch, generation: uint64(sourceGeneration), fence: sourceFence}
			dataEffect, err := loadPlatformManagedHistoryEffect(e, source, runtime.id, "prepare_data", "prepare-data", facts.now)
			if err != nil {
				return zero, err
			}
			data, err := journal.ParseDataPreparationReceipt([]byte(dataEffect.receipt))
			dataCommand, commandErr := platformDataCommandForSource(e, source, runtime, data.Claim.ResourceID, dataEffect.id)
			original := dataCommand
			original.ControlRevision = data.ControlRevision
			if err != nil || commandErr != nil || dataCommand.PayloadSHA256 != dataEffect.digest || !data.MatchesCommand(original) ||
				data.InstallationID != expected.installationID || data.DeploymentID != runtime.deploymentID ||
				data.ControllerEpoch != expected.controllerEpoch || data.ReleaseSHA256 != sourceRelease ||
				data.Generation != strconv.FormatInt(sourceGeneration, 10) || data.WriterFence != "1" ||
				data.DataFormat != strconv.Itoa(parsed.Manifest.Data.Schema) {
				return zero, errAppPlatformStageEvidence
			}
			running, err := journal.ParseWritableCandidateRunningReceipt([]byte(start.receipt))
			revision, _ := strconv.ParseInt(running.Claim.ControlRevision, 10, 64)
			if err != nil || running.Claim.Preflight == nil || running.Claim.Preflight.Native.Platform != plan.Runtime.Profile.Platform ||
				running.Claim.SandboxProfile != control.WritableCandidateSandboxProfile || running.Claim.EffectID != start.id ||
				running.Claim.PayloadSHA256 != start.digest || running.Claim.AdmittedFence != strconv.FormatInt(sourceFence, 10) ||
				running.Claim.DataReceiptSHA256 != platformDigest([]byte(dataEffect.receipt)) ||
				running.Claim.Preflight.Native.ProjectID != data.ProjectID || running.Claim.Preflight.Native.NamespaceInode != data.NamespaceInode ||
				revision < 1 || revision > runtime.controlRevision {
				return zero, errAppPlatformStageEvidence
			}
			resourceID, namespaceID, nativeID, platform = running.Claim.ResourceID, data.Claim.ResourceID, running.NativeID, running.Claim.Preflight.Native.Platform
		default:
			return zero, errAppPlatformStageEvidence
		}
	}
	descriptor := []byte(descriptorRaw)
	effectID, err := platformNewUUID()
	if err != nil {
		return zero, err
	}
	command := control.ManagedRemovalCommand{
		Format: control.ManagedRemovalFormat, DeploymentID: runtime.deploymentID, ControllerEpoch: expected.controllerEpoch,
		ControlRevision: strconv.FormatInt(runtime.controlRevision, 10), InstallationID: expected.installationID,
		OperationID: expected.id, TargetGeneration: strconv.FormatUint(expected.snapshot.Generation, 10),
		Stage: string(expected.snapshot.Stage), EffectNumber: "1", EffectID: effectID,
		Kind: "stop", InstallationFence: strconv.FormatInt(expected.fence, 10),
		Payload: control.ManagedRemovalPayload{ResourceID: resourceID, DataNamespaceID: namespaceID,
			ReleaseSHA256: sourceRelease, Platform: platform,
			SourceOperationID: sourceOperation, SourceEffectID: sourceEffectID,
			SourceReceiptSHA256: sourceReceiptSHA256},
	}
	if expected.snapshot.Stage == appplatform.StageRemoveCode {
		command.Kind = "remove-code"
	}
	command.PayloadSHA256, err = control.ManagedRemovalLogicalDigest(command)
	if err != nil {
		return zero, err
	}
	issued := facts.now
	command.IssuedMS, command.ExpiresMS = strconv.FormatInt(issued, 10), strconv.FormatInt(issued+25000, 10)
	authority := control.Authority{DeploymentID: runtime.deploymentID, ControllerEpoch: expected.controllerEpoch, ControlRevision: runtime.controlRevision, PublicKey: bytes.Clone(runtime.publicKey), Fingerprint: runtime.fingerprint}
	key := ed25519.NewKeyFromSeed(seed)
	defer clear(key)
	raw, signature, err := control.SignManagedRemoval(command, key, authority, issued)
	if err != nil {
		return zero, err
	}
	if _, err := e.exec(`INSERT INTO app_platform_operation_effects(operation_id,stage,effect_number,effect_id,kind,payload_sha256,runtime_id,controller_epoch,admitted_fence,observed_fence,state,receipt_json,created_ms,updated_ms)
        VALUES(?,?,1,?,?,?,?,?,?,0,'prepared','{}',?,?)`, expected.id, command.Stage, effectID, command.Kind, command.PayloadSHA256, runtime.id, expected.controllerEpoch, expected.fence, facts.now, facts.now); err != nil {
		return zero, err
	}
	if _, err := finalPlatformOperationLeaseCheck(e, facts); err != nil {
		return zero, fmt.Errorf(
			"managed removal final lease check: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return zero, err
	}
	return platformManagedRemovalIntent{command: command, raw: raw, signature: signature, authority: authority, descriptor: descriptor, runtime: runtime, sourceOperationID: sourceOperation, nativeID: nativeID}, nil
}

func loadPlatformManagedRemovalSource(
	e migExec,
	expected platformOperationLease,
	sourceGeneration int64,
	runtime platformRuntimeRegistration,
	now int64,
) (platformManagedRemovalSource, error) {
	var result platformManagedRemovalSource
	var sourceRelease, sourceNamespace, sourceState string
	var sourceActivationGeneration, sourceFormat int64
	if err := e.queryRow(`SELECT release_sha256,generation,data_namespace_id,data_format,state
		FROM app_platform_activations
		WHERE installation_id=? AND generation=? AND state='retired'`,
		expected.installationID, sourceGeneration).Scan(&sourceRelease,
		&sourceActivationGeneration, &sourceNamespace, &sourceFormat,
		&sourceState); err != nil ||
		sourceState != "retired" ||
		sourceActivationGeneration != sourceGeneration ||
		sourceRelease == "" || sourceNamespace == "" || sourceFormat < 1 {
		return result, fmt.Errorf(
			"managed removal source activation: %w",
			errAppPlatformStageEvidence)
	}
	var action, planRaw, planSHA, targetRelease string
	var descriptor []byte
	var completed int64
	if err := e.queryRow(`SELECT o.id,o.action,o.fence,o.completed_ms,
		p.plan_json,p.plan_sha256,p.target_release_sha256,r.descriptor
		FROM app_platform_operations o
		JOIN app_platform_review_plans p ON p.id=o.review_plan_id
		JOIN app_platform_releases r ON r.sha256=p.target_release_sha256
		WHERE o.installation_id=? AND o.target_generation=?
		AND o.action IN ('update','enable','repair','restore') AND o.managed=1
		AND o.stage='complete' AND o.completed_ms IS NOT NULL
		AND o.lease_owner IS NULL AND o.lease_until_ms IS NULL
		AND o.error_code IS NULL AND o.controller_epoch=?
		AND p.action=o.action AND p.controller_epoch=o.controller_epoch
		AND p.target_release_sha256=?
		ORDER BY o.completed_ms DESC,o.id DESC LIMIT 1`,
		expected.installationID, sourceGeneration, expected.controllerEpoch,
		sourceRelease).Scan(&result.operationID, &action, &result.fence,
		&completed, &planRaw, &planSHA, &targetRelease,
		&descriptor); err != nil ||
		!platformValidUUID(result.operationID) || result.fence < 1 ||
		completed < 0 || completed > now ||
		targetRelease != sourceRelease ||
		platformDigest([]byte(planRaw)) != planSHA {
		return platformManagedRemovalSource{}, fmt.Errorf(
			"managed removal source operation: %w",
			errAppPlatformStageEvidence)
	}
	var canonical []byte
	switch action {
	case "update":
		var plan platformManagedUpdatePlan
		if json.Unmarshal([]byte(planRaw), &plan) != nil ||
			plan.Format != platformManagedUpdateFormat ||
			plan.InstallationID != expected.installationID ||
			plan.TargetGeneration != sourceGeneration ||
			plan.TargetReleaseSHA256 != sourceRelease ||
			plan.DataNamespaceID != sourceNamespace ||
			plan.DataFormat != sourceFormat ||
			plan.TargetRuntime == nil ||
			plan.TargetRuntime.RuntimeID != runtime.id {
			return platformManagedRemovalSource{}, fmt.Errorf(
				"managed removal update plan: %w",
				errAppPlatformStageEvidence)
		}
		canonical, _ = json.Marshal(plan)
	case "enable":
		var plan platformManagedUpdatePlan
		if json.Unmarshal([]byte(planRaw), &plan) != nil ||
			plan.Format != platformManagedEnableFormat ||
			plan.InstallationID != expected.installationID ||
			plan.TargetGeneration != sourceGeneration ||
			plan.TargetReleaseSHA256 != sourceRelease ||
			plan.DataNamespaceID != sourceNamespace ||
			plan.DataFormat != sourceFormat ||
			plan.TargetRuntime == nil ||
			plan.TargetRuntime.RuntimeID != runtime.id {
			return platformManagedRemovalSource{}, fmt.Errorf(
				"managed removal enable plan: %w",
				errAppPlatformStageEvidence)
		}
		canonical, _ = json.Marshal(plan)
	case "repair":
		var plan platformManagedUpdatePlan
		if json.Unmarshal([]byte(planRaw), &plan) != nil ||
			plan.Format != platformManagedRepairFormat ||
			plan.InstallationID != expected.installationID ||
			plan.TargetGeneration != sourceGeneration ||
			plan.TargetReleaseSHA256 != sourceRelease ||
			plan.DataNamespaceID != sourceNamespace ||
			plan.DataFormat != sourceFormat ||
			plan.TargetRuntime == nil ||
			plan.TargetRuntime.RuntimeID != runtime.id {
			return platformManagedRemovalSource{}, fmt.Errorf(
				"managed removal repair plan: %w",
				errAppPlatformStageEvidence)
		}
		canonical, _ = json.Marshal(plan)
	case "restore":
		var plan platformManagedRestorePlan
		if json.Unmarshal([]byte(planRaw), &plan) != nil ||
			plan.Format != platformManagedRestoreFormat ||
			plan.InstallationID != expected.installationID ||
			plan.TargetGeneration != sourceGeneration ||
			plan.ReleaseSHA256 != sourceRelease {
			return platformManagedRemovalSource{}, fmt.Errorf(
				"managed removal restore plan: %w",
				errAppPlatformStageEvidence)
		}
		canonical, _ = json.Marshal(plan)
	default:
		return platformManagedRemovalSource{}, errAppPlatformStageEvidence
	}
	if string(canonical) != planRaw {
		return platformManagedRemovalSource{}, fmt.Errorf(
			"managed removal plan canonicalization: %w",
			errAppPlatformStageEvidence)
	}
	target, err := platformManagedTargetForOperation(e, result.operationID)
	if err != nil || target == nil || target.RuntimeID != runtime.id ||
		target.ControllerEpoch != expected.controllerEpoch {
		return platformManagedRemovalSource{}, fmt.Errorf(
			"managed removal runtime target: %w",
			errAppPlatformStageEvidence)
	}
	parsed, err := appplatform.ParseDescriptor(descriptor)
	if err != nil || parsed.SHA256 != sourceRelease ||
		int64(parsed.Manifest.Data.Schema) != sourceFormat {
		return platformManagedRemovalSource{}, fmt.Errorf(
			"managed removal descriptor: %w",
			errAppPlatformStageEvidence)
	}
	expectedKind := map[string]string{
		"update": "update-start", "enable": "enable-start",
		"repair":  "enable-start",
		"restore": "restore-start",
	}[action]
	var effectID, effectKind, effectDigest, receiptRaw string
	var effectCreated, effectUpdated int64
	if err := e.queryRow(`SELECT effect_id,kind,payload_sha256,receipt_json,
		created_ms,updated_ms FROM app_platform_operation_effects
		WHERE operation_id=? AND stage='start' AND effect_number=1
		AND state='applied' AND runtime_id=? AND controller_epoch=?
		AND admitted_fence=? AND observed_fence=?`,
		result.operationID, runtime.id, expected.controllerEpoch,
		result.fence, result.fence).Scan(&effectID, &effectKind,
		&effectDigest, &receiptRaw, &effectCreated,
		&effectUpdated); err != nil ||
		!platformValidUUID(effectID) ||
		effectKind != expectedKind ||
		!platformValidSHA(effectDigest) ||
		effectCreated < 0 || effectUpdated < effectCreated ||
		effectUpdated > completed {
		return platformManagedRemovalSource{}, fmt.Errorf(
			"managed removal start effect: %w",
			errAppPlatformStageEvidence)
	}
	running, err := journal.ParseUpdateStartRunningReceipt(
		[]byte(receiptRaw))
	if err != nil || running.Claim.Preflight == nil ||
		running.InstallationID != expected.installationID ||
		running.OperationID != result.operationID ||
		running.NamespaceID != sourceNamespace ||
		running.TargetReleaseSHA256 != sourceRelease ||
		running.TargetGeneration != strconv.FormatInt(sourceGeneration, 10) ||
		running.DataFormat != strconv.FormatInt(sourceFormat, 10) ||
		running.NativeID == "" ||
		!platformValidUUID(running.Claim.ResourceID) ||
		running.Claim.EffectID != effectID ||
		running.Claim.PayloadSHA256 != effectDigest ||
		running.Claim.AdmittedFence != strconv.FormatInt(result.fence, 10) {
		return platformManagedRemovalSource{}, fmt.Errorf(
			"managed removal running receipt: %w",
			errAppPlatformStageEvidence)
	}
	result.resourceID = running.Claim.ResourceID
	result.releaseSHA256 = sourceRelease
	result.descriptor = string(descriptor)
	result.startEffectID = effectID
	result.startReceiptSHA256 = platformDigest([]byte(receiptRaw))
	result.generation = sourceGeneration
	result.namespaceID = sourceNamespace
	result.nativeID = running.NativeID
	result.platform = running.Claim.Preflight.Platform
	return result, nil
}

func validPlatformManagedRemovalObservation(observation interface {
	ManagedRemovalReceipt() (journal.ManagedRemovalReceipt, error)
	Controller() control.Authority
	Endpoint() transport.CandidateReceiptEndpoint
	ReceivedAt() time.Time
	CommandSHA256() string
}, intent platformManagedRemovalIntent, endpoint platformRuntimeEndpoint) (journal.ManagedRemovalReceipt, bool) {
	receipt, err := observation.ManagedRemovalReceipt()
	if err != nil || !platformManagedRemovalReceiptMatchesIntent(receipt, intent) ||
		observation.CommandSHA256() != platformDigest(intent.raw) || observation.ReceivedAt().IsZero() {
		return journal.ManagedRemovalReceipt{}, false
	}
	a := observation.Controller()
	if a.DeploymentID != intent.authority.DeploymentID || a.ControllerEpoch != intent.authority.ControllerEpoch || a.ControlRevision != intent.authority.ControlRevision ||
		a.Fingerprint != intent.authority.Fingerprint || !bytes.Equal(a.PublicKey, intent.authority.PublicKey) {
		return journal.ManagedRemovalReceipt{}, false
	}
	e := observation.Endpoint()
	age := time.Since(observation.ReceivedAt())
	observed, observedErr := strconv.ParseInt(receipt.ObservedMS, 10, 64)
	settled, settledErr := strconv.ParseInt(receipt.SettledMS, 10, 64)
	issued, issuedErr := strconv.ParseInt(intent.command.IssuedMS, 10, 64)
	if !e.RootController || e.Path != endpoint.path || e.RunnerUID != 0 || e.ControllerUID != endpoint.controllerUID || e.ControllerGID != endpoint.controllerGID ||
		age < 0 || age > 3*time.Second || observedErr != nil || settledErr != nil || issuedErr != nil ||
		observed < issued || observed > settled || settled > observation.ReceivedAt().UnixMilli() {
		return journal.ManagedRemovalReceipt{}, false
	}
	return receipt, true
}

func platformManagedRemovalReceiptMatchesIntent(receipt journal.ManagedRemovalReceipt, intent platformManagedRemovalIntent) bool {
	c := intent.command
	return receipt.DeploymentID == c.DeploymentID &&
		receipt.ControllerEpoch == c.ControllerEpoch &&
		receipt.InstallationID == c.InstallationID &&
		receipt.OperationID == c.OperationID &&
		receipt.SourceOperationID == c.Payload.SourceOperationID &&
		receipt.SourceEffectID == c.Payload.SourceEffectID &&
		receipt.TargetGeneration == c.TargetGeneration &&
		receipt.Stage == c.Stage &&
		receipt.EffectID == c.EffectID &&
		receipt.PayloadSHA256 == c.PayloadSHA256 &&
		receipt.SourceReceiptSHA256 == c.Payload.SourceReceiptSHA256 &&
		receipt.ResourceID == c.Payload.ResourceID &&
		receipt.DataNamespaceID == c.Payload.DataNamespaceID &&
		receipt.ReleaseSHA256 == c.Payload.ReleaseSHA256 &&
		receipt.NativeID == intent.nativeID &&
		((receipt.Stage == "stop" && receipt.State == "stopped") ||
			(receipt.Stage == "remove_code" && receipt.State == "absent"))
}

func (s *Server) settlePlatformManagedRemoval(ctx context.Context, expected platformOperationLease, intent platformManagedRemovalIntent, receipt journal.ManagedRemovalReceipt) (platformOperationLease, error) {
	var zero platformOperationLease
	if !platformManagedRemovalReceiptMatchesIntent(receipt, intent) {
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
		return zero, err
	}
	var state, digest, receiptRaw string
	var observed int64
	if err := e.queryRow(`SELECT state,payload_sha256,receipt_json,observed_fence FROM app_platform_operation_effects WHERE operation_id=? AND stage=? AND effect_number=1 AND effect_id=?`, expected.id, intent.command.Stage, intent.command.EffectID).
		Scan(&state, &digest, &receiptRaw, &observed); err != nil || state != "prepared" || receiptRaw != "{}" || digest != intent.command.PayloadSHA256 || observed != 0 {
		return zero, errAppPlatformStageEvidence
	}
	now, err := finalPlatformOperationLeaseCheck(e, facts)
	if err != nil {
		return zero, err
	}
	encoded, err := json.Marshal(receipt)
	if err != nil {
		return zero, err
	}
	changed, err := e.exec(`UPDATE app_platform_operation_effects SET state='applied',observed_fence=?,receipt_json=?,updated_ms=? WHERE operation_id=? AND stage=? AND effect_number=1 AND effect_id=? AND state='prepared' AND receipt_json='{}' AND observed_fence=0`, expected.fence, string(encoded), now, expected.id, intent.command.Stage, intent.command.EffectID)
	if err := platformLeaseCAS(changed, err); err != nil {
		return zero, err
	}
	next := appplatform.StageComplete
	if expected.snapshot.Action == appplatform.ActionUninstall {
		next = appplatform.StageRemoveCode
		if expected.snapshot.Stage == appplatform.StageRemoveCode {
			next = appplatform.StageRemoveData
		}
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
	if next == appplatform.StageComplete {
		changed, err = e.exec(`UPDATE app_platform_operations SET stage='complete',state_revision=?,row_revision=row_revision+1,completed_ms=?,updated_ms=?,lease_owner=NULL,lease_until_ms=NULL WHERE id=? AND installation_id=? AND controller_epoch=? AND stage=? AND state_revision=? AND row_revision=? AND fence=? AND lease_owner=? AND lease_until_ms=? AND lease_until_ms>? AND completed_ms IS NULL`, int64(result.snapshot.Revision), now, now, expected.id, expected.installationID, expected.controllerEpoch, string(expected.snapshot.Stage), int64(expected.snapshot.Revision), expected.rowRevision, expected.fence, expected.owner, expected.leaseUntilMS, now)
	} else {
		changed, err = e.exec(`UPDATE app_platform_operations SET stage=?,state_revision=?,row_revision=row_revision+1,updated_ms=? WHERE id=? AND installation_id=? AND controller_epoch=? AND stage=? AND state_revision=? AND row_revision=? AND fence=? AND lease_owner=? AND lease_until_ms=? AND lease_until_ms>? AND completed_ms IS NULL`, string(next), int64(result.snapshot.Revision), now, expected.id, expected.installationID, expected.controllerEpoch, string(expected.snapshot.Stage), int64(expected.snapshot.Revision), expected.rowRevision, expected.fence, expected.owner, expected.leaseUntilMS, now)
	}
	if err := platformLeaseCAS(changed, err); err != nil {
		return zero, err
	}
	detail, _ := json.Marshal(struct {
		Stage    appplatform.Stage `json:"stage"`
		NativeID string            `json:"native_id,omitempty"`
	}{next, receipt.NativeID})
	if _, err := e.exec(`INSERT INTO audit_log(at,actor,actor_ou,action,target_type,target_id,detail,ip) VALUES(?,'system',0,'app_platform.operation_stage','app_platform_operation',?,?,'')`, time.UnixMilli(now).UTC().Format(time.RFC3339), expected.id, string(detail)); err != nil {
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

func (s *Server) claimNextPlatformManagedUninstall(ctx context.Context, worker string) (platformInstallSelection, error) {
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
	var epoch, paused string
	if err := e.queryRow(`SELECT v FROM meta WHERE k='app_platform_controller_epoch'`).Scan(&epoch); err != nil {
		return zero, errAppPlatformControlPaused
	}
	if err := e.queryRow(`SELECT v FROM meta WHERE k='app_platform_restore_paused'`).Scan(&paused); err != nil {
		return zero, errAppPlatformControlPaused
	}
	if paused != "0" || !platformValidUUID(epoch) {
		return zero, errAppPlatformControlPaused
	}
	now, err := platformDatabaseNow(e)
	if err != nil {
		return zero, err
	}
	rows, err := e.query(`SELECT o.id FROM app_platform_operations o JOIN app_platform_installations i ON i.id=o.installation_id
        WHERE o.action IN ('disable','uninstall') AND o.managed=1 AND o.completed_ms IS NULL
          AND ((o.action='disable' AND o.stage IN ('queued','validate','revoke','stop')) OR (o.action='uninstall' AND o.stage IN ('queued','validate','revoke','stop','remove_code','remove_data')))
          AND (o.lease_until_ms IS NULL OR o.lease_until_ms<=?) AND i.runtime_id IS NOT NULL
          AND ((o.action='disable' AND i.desired_state='disabled' AND i.phase='disabled') OR (o.action='uninstall' AND i.desired_state='removed' AND i.phase='pending'))
          AND i.generation=o.target_generation AND i.controller_epoch=? AND o.controller_epoch=?
          AND NOT EXISTS(SELECT 1 FROM app_platform_operation_effects f WHERE f.operation_id=o.id AND f.stage<>o.stage AND f.state<>'applied')
        ORDER BY o.created_ms,o.id LIMIT 32`, now, epoch, epoch)
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
		if facts.lease.leaseUntilMS > facts.now ||
			(facts.lease.snapshot.Action != appplatform.ActionDisable &&
				facts.lease.snapshot.Action != appplatform.ActionUninstall) ||
			!facts.lease.snapshot.Managed {
			continue
		}
		if err := e.queryRow(`SELECT runtime_id FROM app_platform_installations WHERE id=?`, facts.lease.installationID).Scan(&facts.runtimeID); err != nil || !facts.runtimeID.Valid {
			if err != nil {
				return zero, err
			}
			return zero, errAppPlatformStageEvidence
		}
		if err := validatePlatformAdmittedRemoval(e, facts.lease, facts); err != nil {
			return zero, err
		}
		lease, err := writePlatformOperationLease(e, facts, worker, nil)
		if err != nil {
			return zero, err
		}
		check := facts
		check.lease = lease
		detail, _ := json.Marshal(struct {
			Worker string `json:"worker_id"`
		}{worker})
		audit := "app_platform.managed_uninstall_claimed"
		if facts.lease.snapshot.Action == appplatform.ActionDisable {
			audit = "app_platform.managed_disable_claimed"
		}
		if _, err := e.exec(`INSERT INTO audit_log(at,actor,actor_ou,action,target_type,target_id,detail,ip) VALUES(?,'system',0,?,'app_platform_operation',?,?,'')`, time.UnixMilli(facts.now).UTC().Format(time.RFC3339), audit, id, string(detail)); err != nil {
			return zero, err
		}
		if _, err := finalPlatformOperationLeaseCheck(e, check); err != nil {
			return zero, err
		}
		if err := tx.Commit(); err != nil {
			return zero, err
		}
		return platformInstallSelection{lease: lease, operationID: id}, nil
	}
	return zero, nil
}

func (s *Server) runPlatformManagedUninstall(ctx context.Context, expected platformOperationLease, endpoint platformRuntimeEndpoint) (platformInstallRun, error) {
	var zero platformInstallRun
	if (expected.snapshot.Action != appplatform.ActionDisable &&
		expected.snapshot.Action != appplatform.ActionUninstall) ||
		!expected.snapshot.Managed {
		return zero, errAppPlatformStageEvidence
	}
	result := expected
	for result.snapshot.Stage != appplatform.StageComplete {
		switch result.snapshot.Stage {
		case appplatform.StageQueued, appplatform.StageValidate:
			next, err := s.st.advancePlatformRemovalControl(result)
			if err != nil {
				return platformInstallRun{lease: result}, err
			}
			result = next
		case appplatform.StageRevoke:
			next, err := s.advancePlatformManagedRemovalStage(ctx, result, appplatform.StageStop)
			if err != nil {
				return platformInstallRun{lease: result}, err
			}
			result = next
		case appplatform.StageStop, appplatform.StageRemoveCode:
			intent, err := s.preparePlatformManagedRemovalIntent(ctx, result)
			if err != nil {
				return platformInstallRun{lease: result}, err
			}
			if err := s.checkPlatformManagedOperationRoot(ctx, intent.sourceOperationID, intent.runtime, intent.authority, endpoint); err != nil {
				return platformInstallRun{lease: result}, err
			}
			observation, err := platformRunManagedRemoval(ctx, endpoint, result.snapshot.Stage, intent.raw, intent.signature, intent.authority, intent.descriptor)
			if err != nil {
				return platformInstallRun{lease: result}, err
			}
			receipt, ok := validPlatformManagedRemovalObservation(observation, intent, endpoint)
			if !ok {
				return platformInstallRun{lease: result}, errAppPlatformStageEvidence
			}
			next, err := s.settlePlatformManagedRemoval(ctx, result, intent, receipt)
			if err != nil {
				return platformInstallRun{lease: result}, err
			}
			result = next
		case appplatform.StageRemoveData:
			intent, err := s.preparePlatformDeleteDataIntent(ctx, result)
			if err != nil {
				return platformInstallRun{lease: result}, err
			}
			if err := s.checkPlatformManagedOperationRoot(
				ctx, intent.sourceOperationID, intent.runtime,
				intent.authority, endpoint); err != nil {
				return platformInstallRun{lease: result}, err
			}
			observation, err := platformDeleteApplicationData(
				ctx, endpoint, intent)
			if err != nil {
				return platformInstallRun{lease: result}, err
			}
			receipt, ok := validPlatformDeleteDataObservation(
				observation, intent, endpoint)
			if !ok {
				return platformInstallRun{lease: result},
					errAppPlatformStageEvidence
			}
			next, err := s.settlePlatformDeleteData(
				ctx, result, intent, receipt)
			if err != nil {
				return platformInstallRun{lease: result}, err
			}
			result = next
		default:
			return zero, errAppPlatformStageEvidence
		}
	}
	return platformInstallRun{lease: result}, nil
}

func (s *Server) finishPlatformManagedUninstallForReview(ctx context.Context, expected platformOperationLease) error {
	if (expected.snapshot.Action != appplatform.ActionDisable &&
		expected.snapshot.Action != appplatform.ActionUninstall) ||
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
	if err := validatePlatformAdmittedRemoval(e, expected, facts); err != nil {
		return err
	}
	now, err := finalPlatformOperationLeaseCheck(e, facts)
	if err != nil {
		return err
	}
	model, err := appplatform.OperationFromSnapshot(expected.snapshot)
	if err != nil {
		return errAppPlatformStageEvidence
	}
	// A disable that never persisted a stop intent cannot have sent a runner
	// command. Once an intent exists, a missing confirmation is uncertain rather
	// than evidence that the process exited.
	var effects int
	if err := e.queryRow(`SELECT COUNT(*) FROM app_platform_operation_effects WHERE operation_id=? AND stage='stop'`, expected.id).Scan(&effects); err != nil {
		return err
	}
	outcome := appplatform.EffectsUnknown
	code := "managed_uninstall_retry_exhausted"
	if expected.snapshot.Action == appplatform.ActionDisable {
		code = "managed_disable_stop_pending"
		if effects == 0 {
			outcome, code = appplatform.EffectsNone, "managed_disable_unavailable"
		}
	}
	failed, err := model.Fail(expected.snapshot.Revision, outcome)
	if err != nil {
		return errAppPlatformStageEvidence
	}
	if failed.Snapshot().Stage == appplatform.StageRecoveryRequired {
		desired, phase := "removed", "pending"
		if expected.snapshot.Action == appplatform.ActionDisable {
			desired, phase = "disabled", "disabled"
		}
		changed, err := e.exec(`UPDATE app_platform_installations SET phase='recovery_required',revision=revision+1,updated_ms=? WHERE id=? AND desired_state=? AND phase=? AND generation=? AND revision=? AND control_fence=? AND controller_epoch=?`, now, expected.installationID, desired, phase, expected.snapshot.Generation, facts.installationRevision, expected.fence, expected.controllerEpoch)
		if err := platformLeaseCAS(changed, err); err != nil {
			return err
		}
	}
	changed, err := e.exec(`UPDATE app_platform_operations SET stage=?,state_revision=?,row_revision=row_revision+1,error_code=?,completed_ms=?,updated_ms=?,lease_owner=NULL,lease_until_ms=NULL WHERE id=? AND installation_id=? AND controller_epoch=? AND stage=? AND state_revision=? AND row_revision=? AND fence=? AND lease_owner=? AND lease_until_ms=? AND lease_until_ms>? AND completed_ms IS NULL`, string(failed.Snapshot().Stage), int64(failed.Snapshot().Revision), code, now, now, expected.id, expected.installationID, expected.controllerEpoch, string(expected.snapshot.Stage), int64(expected.snapshot.Revision), expected.rowRevision, expected.fence, expected.owner, expected.leaseUntilMS, now)
	if err := platformLeaseCAS(changed, err); err != nil {
		return err
	}
	audit := "app_platform.managed_uninstall_failed"
	if expected.snapshot.Action == appplatform.ActionDisable {
		audit = "app_platform.managed_disable_pending"
	}
	if _, err := e.exec(`INSERT INTO audit_log(at,actor,actor_ou,action,target_type,target_id,detail,ip) VALUES(?,'system',0,?,'app_platform_operation',?,'{}','')`, time.UnixMilli(now).UTC().Format(time.RFC3339), audit, expected.id); err != nil {
		return err
	}
	if _, err := finalPlatformOperationLeaseCheck(e, facts); err != nil {
		return err
	}
	return tx.Commit()
}
