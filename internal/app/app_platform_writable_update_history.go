package app

import (
	"bytes"
	"crypto/ed25519"
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/journal"
)

// Reconstruct a completed managed update from the same signed update-start and
// registration evidence used by the live transaction. The fresh envelopes are
// access-capability proofs only; they cannot mutate the completed operation.
func (s *Server) loadPlatformWritableUpdateBackendHistory(e migExec, b platformBackendHistoryBinding, runtime platformRuntimeRegistration, seed, dek []byte, now int64) (platformWritableRegistrationIntent, error) {
	var zero platformWritableRegistrationIntent
	if b.generation < 2 || len(seed) != ed25519.SeedSize {
		return zero, fmt.Errorf(
			"update history generation: %w", errAppPlatformLaunchUnavailable)
	}
	var operationID, planRaw, planSHA, historyAction string
	var descriptor []byte
	var completed, created, updated, fence int64
	if err := e.queryRow(`SELECT o.id,p.plan_json,p.plan_sha256,
		r.descriptor,o.action,o.completed_ms,o.created_ms,o.updated_ms,o.fence
		FROM app_platform_operations o
		JOIN app_platform_review_plans p ON p.id=o.review_plan_id
		JOIN app_platform_releases r ON r.sha256=p.target_release_sha256
		WHERE o.installation_id=? AND o.action IN ('update','enable') AND o.managed=1
		AND o.target_generation=? AND o.stage='complete'
		AND o.completed_ms IS NOT NULL AND o.lease_owner IS NULL
		AND o.lease_until_ms IS NULL AND o.error_code IS NULL
		AND o.controller_epoch=? AND p.controller_epoch=o.controller_epoch
		AND p.target_release_sha256=? AND p.consumed_ms IS NOT NULL`,
		b.installation, b.generation, b.epoch, b.release).
		Scan(&operationID, &planRaw, &planSHA, &descriptor,
			&historyAction,
			&completed, &created, &updated, &fence); err != nil ||
		created < 0 || completed < created || updated < completed ||
		updated > now || fence < 1 || fence > b.fence {
		return zero, fmt.Errorf(
			"update history operation err=%v created=%d completed=%d updated=%d fence=%d/%d: %w",
			err, created, completed, updated, fence, b.fence,
			errAppPlatformLaunchUnavailable)
	}
	if !platformValidUUID(operationID) ||
		platformDigest([]byte(planRaw)) != planSHA {
		return zero, fmt.Errorf(
			"update history operation tuple: %w", errAppPlatformLaunchUnavailable)
	}
	var plan platformManagedUpdatePlan
	unmarshalErr := json.Unmarshal([]byte(planRaw), &plan)
	canonical, marshalErr := json.Marshal(plan)
	history := appplatform.Action(historyAction)
	validFormat := history == appplatform.ActionUpdate &&
		plan.Format == platformManagedUpdateFormat ||
		history == appplatform.ActionEnable &&
			plan.Format == platformManagedEnableFormat
	if unmarshalErr != nil || marshalErr != nil ||
		string(canonical) != planRaw ||
		!validFormat ||
		plan.InstallationID != b.installation ||
		plan.ControllerEpoch != b.epoch ||
		plan.TargetReleaseSHA256 != b.release ||
		plan.TargetGeneration != b.generation ||
		plan.SourceGeneration+1 != plan.TargetGeneration ||
		plan.TargetRuntime == nil ||
		plan.TargetRuntime.RuntimeID != runtime.id {
		return zero, fmt.Errorf(
			"update history plan tuple: %w", errAppPlatformLaunchUnavailable)
	}
	revision := uint64(11)
	if history == appplatform.ActionEnable {
		revision = 6
	}
	expected := platformOperationLease{
		id: operationID, installationID: b.installation,
		controllerEpoch: b.epoch, fence: fence,
		snapshot: appplatform.OperationSnapshot{
			Action: history, Managed: true,
			Generation: uint64(b.generation),
			Revision:   revision,
			Stage:      appplatform.StageActivate,
		},
	}
	checkpoint, _, err := loadPlatformUpdateStartCheckpoint(
		e, expected, plan)
	if err != nil {
		return zero, fmt.Errorf("update history checkpoint: %w", err)
	}
	var startEffect, effectKind, startDigest, startState, runningRaw string
	expectedStartKind := "update-start"
	if history == appplatform.ActionEnable {
		expectedStartKind = "enable-start"
	}
	if err := e.queryRow(`SELECT effect_id,kind,payload_sha256,state,receipt_json
		FROM app_platform_operation_effects
		WHERE operation_id=? AND stage='start' AND effect_number=1`,
		operationID).Scan(&startEffect, &effectKind, &startDigest,
		&startState, &runningRaw); err != nil ||
		!platformValidUUID(startEffect) || effectKind != expectedStartKind ||
		startState != "applied" || !platformValidSHA(startDigest) {
		return zero, fmt.Errorf(
			"update history start effect: %w", errAppPlatformLaunchUnavailable)
	}
	running, err := journal.ParseUpdateStartRunningReceipt([]byte(runningRaw))
	if err != nil || running.OperationID != operationID ||
		running.InstallationID != b.installation ||
		running.TargetReleaseSHA256 != plan.TargetReleaseSHA256 ||
		mustPlatformCheckpointInt(running.TargetGeneration) != b.generation ||
		running.Claim.Preflight == nil {
		return zero, fmt.Errorf(
			"update history running receipt: %w", errAppPlatformLaunchUnavailable)
	}
	source, err := loadCompletedUpdateHistorySource(
		e, expected, plan, checkpoint, running, runtime, descriptor, now)
	if err != nil {
		return zero, fmt.Errorf("update history source: %w", err)
	}
	verifiedPlan, verifiedDescriptor, err := loadPlatformManagedUpdatePlan(
		e, expected, source)
	if err != nil {
		return zero, fmt.Errorf("update history verified plan: %w", err)
	}
	if verifiedPlan.TargetReleaseSHA256 != plan.TargetReleaseSHA256 ||
		!bytes.Equal(verifiedDescriptor, descriptor) {
		return zero, fmt.Errorf(
			"update history plan drift: %w", errAppPlatformLaunchUnavailable)
	}
	plan, descriptor = verifiedPlan, verifiedDescriptor
	start, err := loadPlatformCompletedUpdateStartActivation(
		e, expected, runtime, source, checkpoint, plan)
	if err != nil {
		return zero, fmt.Errorf("update history activation: %w", err)
	}
	if start.running == nil || start.readiness == nil ||
		start.resourceID != b.activation || start.running.NativeID == "" {
		return zero, fmt.Errorf(
			"update history activation tuple: %w", errAppPlatformLaunchUnavailable)
	}
	start.targetDescriptor = bytes.Clone(descriptor)
	readiness, _ := json.Marshal(start.readiness)
	if start.plan.TargetReleaseSHA256 != b.release ||
		start.plan.DataNamespaceID != b.namespace ||
		start.plan.DataFormat != b.format ||
		start.activationObserved != b.observed ||
		string(readiness) != b.readiness {
		return zero, fmt.Errorf(
			"update history activation evidence: %w", errAppPlatformLaunchUnavailable)
	}
	issued := time.Now().UnixMilli()
	if issued < now-1000 || issued > now+1000 ||
		issued > math.MaxInt64-25000 {
		return zero, fmt.Errorf(
			"update history start clock: %w", errAppPlatformLaunchUnavailable)
	}
	start.command.IssuedMS = strconv.FormatInt(issued, 10)
	start.command.ExpiresMS = strconv.FormatInt(issued+25000, 10)
	key := ed25519.NewKeyFromSeed(seed)
	start.raw, start.signature, err = control.SignUpdateStart(
		start.command, key, start.authority, issued)
	clear(key)
	if err != nil {
		return zero, fmt.Errorf("update history start sign: %w", err)
	}
	registration, _, err := loadPlatformUpdateStartRegistration(
		e, expected, start, runtime, seed, dek, now, false)
	if err != nil {
		return zero, fmt.Errorf("update history registration: %w", err)
	}
	if registration.start.running == nil || !registration.applied {
		registration.registration.Close()
		return zero, fmt.Errorf(
			"update history registration tuple: %w", errAppPlatformLaunchUnavailable)
	}
	writer := platformWriterIntent{
		namespaceID: b.namespace, resourceID: b.activation,
		effectID: start.effectID, runtime: runtime,
		authority: start.authority, descriptor: bytes.Clone(descriptor),
		raw: bytes.Clone(start.raw), signature: bytes.Clone(start.signature),
		activationReadiness: b.readiness,
		activationObserved:  sql.NullInt64{Int64: b.observed, Valid: true},
		applied:             true,
	}
	runningView := journal.WritableCandidateRunningReceipt{
		NativeID: start.running.NativeID,
	}
	return platformWritableRegistrationIntent{
		generation: b.generation,
		start: platformWritableStartIntent{
			writer: writer, command: control.WritableCandidateCommand{},
			raw: bytes.Clone(start.raw), signature: bytes.Clone(start.signature),
			running: runningView, applied: true,
		},
		updateCommand: registration.command,
		raw:           bytes.Clone(registration.raw),
		signature:     bytes.Clone(registration.signature),
		registration:  registration.registration,
		update:        &registration,
		applied:       registration.applied,
		updateReceipt: registration.receipt,
		created:       registration.created,
		updated:       registration.updated,
	}, nil
}

func loadCompletedUpdateHistorySource(e migExec, expected platformOperationLease, plan platformManagedUpdatePlan, checkpoint journal.CheckpointReceipt, running journal.UpdateStartRunningReceipt, runtime platformRuntimeRegistration, descriptor []byte, now int64) (platformCheckpointSource, error) {
	var zero platformCheckpointSource
	var desired, phase, epoch, runtimeID string
	var generation, policy int64
	if err := e.queryRow(`SELECT desired_state,phase,generation,policy_revision,
		controller_epoch,runtime_id FROM app_platform_installations WHERE id=?`,
		expected.installationID).
		Scan(&desired, &phase, &generation, &policy, &epoch, &runtimeID); err != nil ||
		desired != "enabled" || phase != "ready" ||
		generation != plan.TargetGeneration ||
		policy != plan.TargetPolicyRevision ||
		epoch != expected.controllerEpoch || runtimeID != runtime.id {
		return zero, errAppPlatformLaunchUnavailable
	}
	var activeRelease, activeNamespace string
	var activeGeneration, activePolicy, activeFormat int64
	if err := e.queryRow(`SELECT release_sha256,generation,policy_revision,
		data_namespace_id,data_format FROM app_platform_activations
		WHERE installation_id=? AND state='active'`,
		expected.installationID).
		Scan(&activeRelease, &activeGeneration, &activePolicy,
			&activeNamespace, &activeFormat); err != nil ||
		activeRelease != plan.TargetReleaseSHA256 ||
		activeGeneration != plan.TargetGeneration ||
		activePolicy != plan.TargetPolicyRevision ||
		activeNamespace != plan.DataNamespaceID ||
		activeFormat != plan.DataFormat {
		return zero, fmt.Errorf(
			"update history active activation release=%s/%s generation=%d/%d policy=%d/%d namespace=%s/%s format=%d/%d: %w",
			activeRelease, plan.TargetReleaseSHA256, activeGeneration,
			plan.TargetGeneration, activePolicy, plan.TargetPolicyRevision,
			activeNamespace, plan.DataNamespaceID, activeFormat,
			plan.DataFormat, errAppPlatformLaunchUnavailable)
	}
	var namespaceState string
	var namespaceFormat, writerFence int64
	if err := e.queryRow(`SELECT state,data_format,writer_fence
		FROM app_platform_data_namespaces
		WHERE id=? AND origin_installation_id=? AND owner_installation_id=?`,
		plan.DataNamespaceID, expected.installationID, expected.installationID).
		Scan(&namespaceState, &namespaceFormat, &writerFence); err != nil ||
		namespaceState != "active" || namespaceFormat != plan.DataFormat ||
		writerFence != plan.SourceWriterFence+1 ||
		mustPlatformCheckpointInt(running.TargetWriterFence) != writerFence {
		return zero, fmt.Errorf(
			"update history namespace state=%s format=%d/%d writer=%d/%d target=%s: %w",
			namespaceState, namespaceFormat, plan.DataFormat,
			writerFence, plan.SourceWriterFence+1,
			running.TargetWriterFence, errAppPlatformLaunchUnavailable)
	}
	checkpointWriterFence := plan.SourceWriterFence
	if plan.Action == appplatform.ActionEnable {
		checkpointWriterFence = plan.CheckpointWriterFence
	}
	if checkpoint.ReleaseSHA256 != plan.SourceReleaseSHA256 ||
		checkpoint.NamespaceID != plan.DataNamespaceID ||
		mustPlatformCheckpointInt(checkpoint.DataFormat) != plan.DataFormat ||
		mustPlatformCheckpointInt(checkpoint.WriterFence) !=
			checkpointWriterFence {
		return zero, fmt.Errorf(
			"update history checkpoint release=%s/%s namespace=%s/%s format=%d/%d writer=%d/%d: %w",
			checkpoint.ReleaseSHA256, plan.SourceReleaseSHA256,
			checkpoint.NamespaceID, plan.DataNamespaceID,
			mustPlatformCheckpointInt(checkpoint.DataFormat), plan.DataFormat,
			mustPlatformCheckpointInt(checkpoint.WriterFence),
			checkpointWriterFence, errAppPlatformLaunchUnavailable)
	}
	var sourceOperation, sourceAction, sourceRelease string
	var sourceGeneration, sourceCompleted int64
	if err := e.queryRow(`SELECT o.action,o.target_generation,
		p.target_release_sha256,o.completed_ms
		FROM app_platform_operations o
		JOIN app_platform_review_plans p ON p.id=o.review_plan_id
		WHERE o.id=? AND o.installation_id=? AND o.managed=1
		AND o.action IN ('install','update','restore')
		AND o.stage='complete' AND o.completed_ms IS NOT NULL
		AND o.lease_owner IS NULL AND o.lease_until_ms IS NULL
		AND o.error_code IS NULL
		AND p.action=o.action AND p.consumed_ms IS NOT NULL`,
		checkpoint.SourceOperationID, expected.installationID).
		Scan(&sourceAction, &sourceGeneration, &sourceRelease,
			&sourceCompleted); err != nil ||
		sourceGeneration != plan.SourceGeneration ||
		sourceRelease != plan.SourceReleaseSHA256 ||
		sourceCompleted < 0 || sourceCompleted > now {
		return zero, fmt.Errorf(
			"update history source operation action=%s generation=%d/%d release=%s/%s completed=%d: %w",
			sourceAction, sourceGeneration, plan.SourceGeneration,
			sourceRelease, plan.SourceReleaseSHA256, sourceCompleted,
			errAppPlatformLaunchUnavailable)
	}
	sourceOperation = checkpoint.SourceOperationID
	var dataReceipt string
	var dataAction string
	var dataGeneration, dataCompleted int64
	if err := e.queryRow(`SELECT f.receipt_json,o.action,o.target_generation,
		o.completed_ms
		FROM app_platform_operation_effects f
		JOIN app_platform_operations o ON o.id=f.operation_id
		WHERE f.effect_id=? AND o.installation_id=? AND f.stage='prepare_data'
		AND f.kind='prepare-data' AND f.state='applied'
		AND o.managed=1 AND o.stage='complete' AND o.completed_ms IS NOT NULL
		AND o.lease_owner IS NULL AND o.lease_until_ms IS NULL
		AND o.error_code IS NULL
		AND o.action IN ('install','update','restore')`,
		checkpoint.SourceDataEffectID, expected.installationID).
		Scan(&dataReceipt, &dataAction, &dataGeneration,
			&dataCompleted); err != nil ||
		platformDigest([]byte(dataReceipt)) !=
			checkpoint.SourceDataReceiptSHA256 ||
		dataGeneration < 1 || dataGeneration > plan.SourceGeneration ||
		dataCompleted < 0 || dataCompleted > now {
		return zero, fmt.Errorf(
			"update history data effect action=%s generation=%d/%d completed=%d: %w",
			dataAction, dataGeneration, plan.SourceGeneration,
			dataCompleted, errAppPlatformLaunchUnavailable)
	}
	data, err := journal.ParseDataPreparationReceipt([]byte(dataReceipt))
	if err != nil || data.Claim.ResourceID != running.NamespaceID ||
		data.ProjectID != running.Claim.Preflight.ProjectID ||
		data.Device != running.Claim.Preflight.Device ||
		data.NamespaceInode != running.Claim.Preflight.NamespaceInode ||
		data.QuotaBytes != running.Claim.Preflight.QuotaBytes ||
		data.QuotaInodes != running.Claim.Preflight.QuotaInodes {
		return zero, fmt.Errorf(
			"update history data receipt resource=%s/%s project=%s/%s device=%s/%s inode=%s/%s: %w",
			data.Claim.ResourceID, running.NamespaceID, data.ProjectID,
			running.Claim.Preflight.ProjectID, data.Device,
			running.Claim.Preflight.Device, data.NamespaceInode,
			running.Claim.Preflight.NamespaceInode,
			errAppPlatformLaunchUnavailable)
	}
	var sourceStart, sourceStartKind, sourceStartState, sourceStartRaw string
	if err := e.queryRow(`SELECT effect_id,kind,state,receipt_json
		FROM app_platform_operation_effects
		WHERE operation_id=? AND effect_id=? AND stage='start'
		AND effect_number=1 AND state='applied'`,
		sourceOperation, checkpoint.SourceStartEffectID).
		Scan(&sourceStart, &sourceStartKind,
			&sourceStartState, &sourceStartRaw); err != nil ||
		!platformValidUUID(sourceStart) ||
		(sourceStartKind != "start-candidate" &&
			sourceStartKind != "update-start" &&
			sourceStartKind != "restore-start") ||
		sourceStartState != "applied" ||
		platformDigest([]byte(sourceStartRaw)) !=
			checkpoint.SourceReceiptSHA256 {
		return zero, fmt.Errorf(
			"update history source start kind=%s state=%s: %w",
			sourceStartKind, sourceStartState,
			errAppPlatformLaunchUnavailable)
	}
	if len(descriptor) == 0 || plan.AppID == "" || plan.PublisherID == "" ||
		plan.SourceGeneration < 1 || plan.SourceWriterFence < 1 ||
		now < sourceCompleted {
		return zero, fmt.Errorf(
			"update history source tuple descriptor=%d app=%s publisher=%s generation=%d writer=%d completed=%d/%d: %w",
			len(descriptor), plan.AppID, plan.PublisherID,
			plan.SourceGeneration, plan.SourceWriterFence,
			sourceCompleted, now, errAppPlatformLaunchUnavailable)
	}
	historySource := platformCheckpointSource{
		operationID: sourceOperation, startEffectID: sourceStart,
		dataEffectID:            checkpoint.SourceDataEffectID,
		sourceReceiptSHA256:     platformDigest([]byte(sourceStartRaw)),
		sourceDataReceiptSHA256: platformDigest([]byte(dataReceipt)),
		resourceID:              data.Claim.ResourceID,
		nativeID:                running.NativeID,
		namespaceID:             running.NamespaceID,
		releaseSHA256:           plan.SourceReleaseSHA256,
		projectID:               data.ProjectID,
		namespaceInode:          data.NamespaceInode,
		device:                  data.Device,
		dataFormat:              running.DataFormat,
		quotaBytes:              data.QuotaBytes,
		quotaInodes:             data.QuotaInodes,
		rootFilesystemUUID:      data.Claim.RootFilesystemUUID,
		rootInode:               data.Claim.RootInode,
		writerFence:             strconv.FormatInt(checkpointWriterFence, 10),
		platform:                running.Claim.Preflight.Platform,
		sandboxProfile:          running.Claim.SandboxProfile,
		appID:                   plan.AppID,
		publisherID:             plan.PublisherID,
		descriptor:              bytes.Clone(descriptor),
		runtimeTarget:           plan.TargetRuntime,
	}
	if plan.Action == appplatform.ActionEnable {
		historySource.currentWriterFence =
			strconv.FormatInt(plan.SourceWriterFence, 10)
	}
	return historySource, nil
}
