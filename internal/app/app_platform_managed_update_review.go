package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"math"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/journal"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/transport"
)

type platformManagedUpdateSource struct {
	operationID string
	format      string
	release     string
	generation  int64
	fence       int64
	runtime     *platformManagedInstallTarget
}

func samePlatformManagedUpdateRuntime(a, b *platformManagedInstallTarget) bool {
	return a != nil && b != nil &&
		a.RuntimeID == b.RuntimeID &&
		a.DeploymentID == b.DeploymentID &&
		a.ControllerEpoch == b.ControllerEpoch &&
		a.Fingerprint == b.Fingerprint &&
		a.Profile.Platform == b.Profile.Platform &&
		a.Profile.Protocol == b.Profile.Protocol &&
		a.Profile.BackendProtocol == b.Profile.BackendProtocol &&
		a.Profile.SandboxProfile == b.Profile.SandboxProfile &&
		a.Profile.WritableSandboxProfile == b.Profile.WritableSandboxProfile
}

func loadPlatformCheckpointUpdateSource(e migExec, expected platformOperationLease, runtime platformRuntimeRegistration, action string, now int64) (platformCheckpointSource, error) {
	if action != "update" && action != "restore" {
		return platformCheckpointSource{}, errAppPlatformStageEvidence
	}
	var sourceOperation, planRaw, planSHA, release string
	var generation, sourceFence, completed, sourceTrustRevision int64
	if err := e.queryRow(`SELECT o.id,p.plan_json,p.plan_sha256,
		p.target_release_sha256,o.target_generation,o.fence,o.completed_ms,
		p.trust_revision
		FROM app_platform_operations o
		JOIN app_platform_review_plans p ON p.id=o.review_plan_id
		WHERE o.installation_id=? AND o.action=? AND o.managed=1
		AND o.stage='complete' AND o.completed_ms IS NOT NULL
		AND o.lease_owner IS NULL AND o.lease_until_ms IS NULL
		AND o.error_code IS NULL
		ORDER BY o.target_generation DESC,o.completed_ms DESC,o.id DESC
		LIMIT 1`, expected.installationID, action).
		Scan(&sourceOperation, &planRaw, &planSHA, &release,
			&generation, &sourceFence, &completed,
			&sourceTrustRevision); err != nil ||
		sourceFence < 1 || completed < 0 ||
		platformDigest([]byte(planRaw)) != planSHA {
		return platformCheckpointSource{}, errAppPlatformStageEvidence
	}
	var startEffect, startKind, startPayload, startState, startRaw string
	var startFence int64
	if err := e.queryRow(`SELECT effect_id,kind,payload_sha256,state,
		admitted_fence,receipt_json FROM app_platform_operation_effects
		WHERE operation_id=? AND stage='start' AND effect_number=1`,
		sourceOperation).
		Scan(&startEffect, &startKind, &startPayload, &startState,
			&startFence, &startRaw); err != nil ||
		startKind != action+"-start" || startState != "applied" ||
		startFence != sourceFence {
		return platformCheckpointSource{}, errAppPlatformStageEvidence
	}
	running, err := journal.ParseUpdateStartRunningReceipt(
		[]byte(startRaw))
	if err != nil || running.OperationID != sourceOperation ||
		running.InstallationID != expected.installationID ||
		running.TargetReleaseSHA256 != release ||
		mustPlatformCheckpointInt(running.TargetGeneration) != generation ||
		running.Claim.EffectID != startEffect ||
		running.Claim.PayloadSHA256 != startPayload ||
		running.Claim.Preflight == nil {
		return platformCheckpointSource{}, errAppPlatformStageEvidence
	}
	profile := transport.ManagedRuntimeProfile{
		Protocol:           transport.ManagedRunnerProtocol,
		Platform:           running.Claim.Preflight.Platform,
		BackendProtocol:    transport.ManagedBackendProtocol,
		SandboxProfile:     control.CandidateSandboxProfile,
		RootFilesystemUUID: running.Claim.Preflight.RootFilesystemUUID,
		RootInode:          running.Claim.Preflight.RootInode,
	}
	if running.Claim.SandboxProfile ==
		control.WritableCandidateSandboxProfile {
		profile.WritableSandboxProfile =
			control.WritableCandidateSandboxProfile
	} else if running.Claim.SandboxProfile !=
		control.CandidateSandboxProfile {
		return platformCheckpointSource{}, errAppPlatformStageEvidence
	}
	target := &platformManagedInstallTarget{
		RuntimeID: runtime.id, DeploymentID: runtime.deploymentID,
		ControllerEpoch: runtime.controllerEpoch,
		BindingRevision: runtime.revision,
		ControlRevision: runtime.controlRevision,
		Fingerprint:     runtime.fingerprint, Profile: profile,
	}
	if err := validateManagedInstallTarget(
		e, target, expected.controllerEpoch); err != nil {
		return platformCheckpointSource{}, err
	}
	sourcePlan, err := loadPlatformManagedUpdateSource(
		e, expected.installationID, release, generation, target)
	if err != nil {
		return platformCheckpointSource{}, err
	}
	var checkpoint journal.CheckpointReceipt
	switch action {
	case "update":
		var checkpointRaw string
		if err := e.queryRow(`SELECT receipt_json
			FROM app_platform_operation_effects
			WHERE operation_id=? AND stage='snapshot' AND effect_number=1
			AND state='applied'`, sourceOperation).
			Scan(&checkpointRaw); err != nil {
			return platformCheckpointSource{}, errAppPlatformStageEvidence
		}
		checkpoint, err = journal.ParseCheckpointReceipt(
			[]byte(checkpointRaw))
	case "restore":
		_, _, checkpoint, _, err = loadPlatformRestoreCheckpoint(
			e, platformOperationLease{
				id: sourceOperation, installationID: expected.installationID,
			})
	}
	if err != nil ||
		checkpoint.InstallationID != expected.installationID ||
		checkpoint.NamespaceID == "" ||
		checkpoint.ReleaseSHA256 != running.SourceReleaseSHA256 ||
		checkpoint.TargetGeneration != running.TargetGeneration ||
		checkpoint.SourceDataEffectID == "" ||
		checkpoint.SourceDataReceiptSHA256 == "" {
		return platformCheckpointSource{}, errAppPlatformStageEvidence
	}
	var dataKind, dataState, dataRaw string
	if err := e.queryRow(`SELECT kind,state,receipt_json
		FROM app_platform_operation_effects WHERE effect_id=?`,
		checkpoint.SourceDataEffectID).
		Scan(&dataKind, &dataState, &dataRaw); err != nil ||
		dataKind != "prepare-data" || dataState != "applied" ||
		platformDigest([]byte(dataRaw)) !=
			checkpoint.SourceDataReceiptSHA256 {
		return platformCheckpointSource{}, errAppPlatformStageEvidence
	}
	data, err := journal.ParseDataPreparationReceipt([]byte(dataRaw))
	if err != nil || data.Claim.ResourceID != running.NamespaceID ||
		data.ProjectID != running.Claim.Preflight.ProjectID ||
		data.Device != running.Claim.Preflight.Device ||
		data.NamespaceInode != running.Claim.Preflight.NamespaceInode ||
		data.QuotaBytes != running.Claim.Preflight.QuotaBytes ||
		data.QuotaInodes != running.Claim.Preflight.QuotaInodes {
		return platformCheckpointSource{}, errAppPlatformStageEvidence
	}
	var desired, phase, epoch, runtimeID string
	var installationGeneration, installationPolicy int64
	if err := e.queryRow(`SELECT desired_state,phase,generation,
		policy_revision,controller_epoch,runtime_id
		FROM app_platform_installations
		WHERE id=?`, expected.installationID).
		Scan(&desired, &phase, &installationGeneration,
			&installationPolicy, &epoch, &runtimeID); err != nil ||
		desired != "enabled" ||
		(phase != "ready" &&
			(phase != "recovery_required" ||
				expected.snapshot.Action != appplatform.ActionRestore)) ||
		installationGeneration != generation ||
		epoch != expected.controllerEpoch || runtimeID != runtime.id {
		return platformCheckpointSource{}, errAppPlatformStageEvidence
	}
	var activeActivation, activeRelease string
	var activeGeneration, activePolicy, activeFormat int64
	if err := e.queryRow(`SELECT id,release_sha256,generation,
		policy_revision,data_format FROM app_platform_activations
		WHERE installation_id=? AND state='active'`,
		expected.installationID).
		Scan(&activeActivation, &activeRelease, &activeGeneration,
			&activePolicy, &activeFormat); err != nil ||
		activeActivation == "" || activeRelease != release ||
		activeGeneration != generation || activePolicy < 1 ||
		activePolicy != installationPolicy ||
		activeFormat != mustPlatformCheckpointInt(running.DataFormat) {
		return platformCheckpointSource{}, errAppPlatformStageEvidence
	}
	if action == "update" {
		var plan platformManagedUpdatePlan
		if json.Unmarshal([]byte(planRaw), &plan) != nil ||
			plan.Format != platformManagedUpdateFormat ||
			plan.TargetReleaseSHA256 != release ||
			plan.TargetGeneration != generation ||
			plan.TargetPolicyRevision != activePolicy {
			return platformCheckpointSource{}, errAppPlatformStageEvidence
		}
	}
	var namespace, namespaceState string
	var namespaceFormat, writerFence int64
	if err := e.queryRow(`SELECT id,state,data_format,writer_fence
		FROM app_platform_data_namespaces
		WHERE id=? AND origin_installation_id=?
		AND owner_installation_id=?`,
		running.NamespaceID, expected.installationID,
		expected.installationID).
		Scan(&namespace, &namespaceState, &namespaceFormat,
			&writerFence); err != nil ||
		namespace != running.NamespaceID ||
		namespaceState != "active" ||
		namespaceFormat != mustPlatformCheckpointInt(running.DataFormat) ||
		writerFence != mustPlatformCheckpointInt(
			running.TargetWriterFence) {
		return platformCheckpointSource{}, errAppPlatformStageEvidence
	}
	trust, err := loadPlatformPublisherTrust(e)
	if err != nil {
		return platformCheckpointSource{}, err
	}
	verified, err := platformVerifyStoredRelease(
		e, release, sourceTrustRevision, trust, now)
	if err != nil || verified.sha256 != release {
		return platformCheckpointSource{}, errAppPlatformStageEvidence
	}
	var currentDescriptor []byte
	if err := e.queryRow(`SELECT descriptor FROM app_platform_releases
		WHERE sha256=?`, release).Scan(&currentDescriptor); err != nil {
		return platformCheckpointSource{}, err
	}
	source := platformCheckpointSource{
		operationID: sourceOperation, startEffectID: startEffect,
		dataEffectID:            checkpoint.SourceDataEffectID,
		sourceReceiptSHA256:     platformDigest([]byte(startRaw)),
		sourceDataReceiptSHA256: platformDigest([]byte(dataRaw)),
		resourceID:              running.Claim.ResourceID,
		nativeID:                running.NativeID,
		namespaceID:             running.NamespaceID, releaseSHA256: release,
		projectID: data.ProjectID, namespaceInode: data.NamespaceInode,
		device: data.Device, dataFormat: running.DataFormat,
		quotaBytes: data.QuotaBytes, quotaInodes: data.QuotaInodes,
		rootFilesystemUUID: data.Claim.RootFilesystemUUID,
		rootInode:          data.Claim.RootInode,
		writerFence:        running.TargetWriterFence,
		platform:           running.Claim.Preflight.Platform,
		sandboxProfile:     running.Claim.SandboxProfile,
		appID:              "",
		descriptor:         currentDescriptor,
		runtimeTarget:      target,
	}
	parsed, err := appplatform.ParseDescriptor(currentDescriptor)
	if err != nil || parsed.SHA256 != release ||
		sourcePlan.runtime.RuntimeID != runtime.id {
		return platformCheckpointSource{}, errAppPlatformStageEvidence
	}
	source.appID = parsed.Manifest.ID
	source.publisherID = parsed.Manifest.Publisher
	var pending int
	if err := e.queryRow(`SELECT COUNT(*) FROM app_platform_operations
		WHERE installation_id=? AND completed_ms IS NULL`,
		expected.installationID).Scan(&pending); err != nil ||
		pending != 1 {
		return platformCheckpointSource{}, errAppPlatformStageEvidence
	}
	return source, nil
}

func loadPlatformManagedUpdateSource(e migExec, installation, activeRelease string, generation int64, target *platformManagedInstallTarget) (platformManagedUpdateSource, error) {
	var result platformManagedUpdateSource
	var planRaw, planSHA, release, action string
	var completed int64
	if err := e.queryRow(`SELECT o.id,p.plan_json,p.plan_sha256,
		p.target_release_sha256,o.target_generation,o.fence,o.completed_ms,o.action
		FROM app_platform_operations o
		JOIN app_platform_review_plans p ON p.id=o.review_plan_id
		WHERE o.installation_id=? AND o.action IN ('install','update','restore')
		AND o.managed=1 AND o.stage='complete' AND o.completed_ms IS NOT NULL
		AND o.lease_owner IS NULL AND o.lease_until_ms IS NULL
		AND o.error_code IS NULL
		ORDER BY o.target_generation DESC,o.completed_ms DESC,o.id DESC LIMIT 1`,
		installation).
		Scan(&result.operationID, &planRaw, &planSHA, &release,
			&result.generation, &result.fence, &completed, &action); err != nil {
		return result, errAppPlatformStageEvidence
	}
	if target == nil || platformDigest([]byte(planRaw)) != planSHA ||
		release != activeRelease || result.generation != generation ||
		result.fence < 1 || completed < 0 {
		return platformManagedUpdateSource{}, errAppPlatformStageEvidence
	}
	switch action {
	case "install":
		var plan platformInstallProjection
		unmarshalErr := json.Unmarshal([]byte(planRaw), &plan)
		canonical, marshalErr := json.Marshal(plan)
		if unmarshalErr != nil || marshalErr != nil ||
			string(canonical) != planRaw ||
			!platformManagedFormat(plan.Format) || plan.Runtime == nil ||
			plan.ReleaseSHA256 != release ||
			!samePlatformManagedUpdateRuntime(plan.Runtime, target) {
			return platformManagedUpdateSource{}, errAppPlatformStageEvidence
		}
		result.format = plan.Format
		result.runtime = plan.Runtime
	case "update", "restore":
		switch action {
		case "update":
			var plan platformManagedUpdatePlan
			unmarshalErr := json.Unmarshal([]byte(planRaw), &plan)
			canonical, marshalErr := json.Marshal(plan)
			if unmarshalErr != nil || marshalErr != nil ||
				string(canonical) != planRaw ||
				plan.Format != platformManagedUpdateFormat ||
				plan.TargetReleaseSHA256 != release ||
				plan.TargetGeneration != generation {
				return platformManagedUpdateSource{}, errAppPlatformStageEvidence
			}
		case "restore":
			var plan platformManagedRestorePlan
			unmarshalErr := json.Unmarshal([]byte(planRaw), &plan)
			canonical, marshalErr := json.Marshal(plan)
			if unmarshalErr != nil || marshalErr != nil ||
				string(canonical) != planRaw ||
				plan.Format != platformManagedRestoreFormat ||
				plan.ReleaseSHA256 != release ||
				plan.TargetGeneration != generation {
				return platformManagedUpdateSource{}, errAppPlatformStageEvidence
			}
		}
		var effectID, kind, digest, state, receipt string
		var observed int64
		if err := e.queryRow(`SELECT effect_id,kind,payload_sha256,state,
			observed_fence,receipt_json FROM app_platform_operation_effects
			WHERE operation_id=? AND stage='start' AND effect_number=1`,
			result.operationID).
			Scan(&effectID, &kind, &digest, &state, &observed,
				&receipt); err != nil ||
			kind != action+"-start" || state != "applied" ||
			observed != result.fence || !platformValidSHA(digest) {
			return platformManagedUpdateSource{}, errAppPlatformStageEvidence
		}
		running, err := journal.ParseUpdateStartRunningReceipt([]byte(receipt))
		if err != nil || running.OperationID != result.operationID ||
			running.InstallationID != installation ||
			running.TargetReleaseSHA256 != release ||
			mustPlatformCheckpointInt(running.TargetGeneration) != generation ||
			mustPlatformCheckpointInt(running.TargetWriterFence) < 1 ||
			running.Claim.EffectID != effectID ||
			running.Claim.PayloadSHA256 != digest ||
			running.Claim.Preflight == nil {
			return platformManagedUpdateSource{}, errAppPlatformStageEvidence
		}
		result.format = platformManagedInstallFormat
		if running.Claim.SandboxProfile ==
			control.WritableCandidateSandboxProfile {
			result.format = platformWritableInstallFormat
		} else if running.Claim.SandboxProfile !=
			control.CandidateSandboxProfile {
			return platformManagedUpdateSource{}, errAppPlatformStageEvidence
		}
		result.runtime = target
	default:
		return platformManagedUpdateSource{}, errAppPlatformStageEvidence
	}
	result.release = release
	return result, nil
}

func platformBuildManagedUpdateProjection(e migExec, installation string, source platformManagedUpdateSource, sourceRevision, sourcePolicy, currentFormat int64, actor platformActorFacts, control platformActorControl, target *platformManagedInstallTarget, release platformStagedRelease) (platformUpdateProjection, error) {
	if target == nil || !platformManagedFormat(source.format) ||
		!samePlatformManagedUpdateRuntime(source.runtime, target) ||
		source.generation < 1 || source.generation == math.MaxInt64 ||
		sourceRevision < 1 || sourceRevision == math.MaxInt64 ||
		sourcePolicy < 1 || sourcePolicy == math.MaxInt64 ||
		currentFormat < 1 ||
		release.appID == "" || release.publisherID == "" ||
		!platformValidSHA(release.sha256) || release.sha256 == source.release {
		return platformUpdateProjection{}, errAppPlatformPlanChanged
	}
	var descriptor []byte
	if err := e.queryRow(`SELECT descriptor FROM app_platform_releases
		WHERE sha256=?`, release.sha256).Scan(&descriptor); err != nil {
		return platformUpdateProjection{}, errAppPlatformPlanChanged
	}
	parsed, err := appplatform.ParseDescriptor(descriptor)
	if err != nil || parsed.Manifest.ID != release.appID ||
		parsed.Manifest.Publisher != release.publisherID ||
		parsed.Manifest.Backend == nil ||
		int64(parsed.Manifest.Data.Schema) != currentFormat {
		return platformUpdateProjection{}, errAppPlatformInstallUnavailable
	}
	if err := installRequirementsForTarget(
		parsed.Manifest, source.format, target); err != nil {
		return platformUpdateProjection{}, err
	}
	if err := appplatform.RequireDataUpgrade(
		int(currentFormat), parsed.Manifest.Data); err != nil {
		return platformUpdateProjection{}, errAppPlatformInstallUnavailable
	}
	selection, err := platformInstallCapabilitySelection(
		parsed.Manifest, target)
	if err != nil {
		return platformUpdateProjection{}, err
	}
	previous, err := platformCurrentCapabilities(e, installation)
	if err != nil {
		return platformUpdateProjection{}, err
	}
	targetCapabilities := make([]string, 0, len(selection))
	for _, capability := range selection {
		if capability.granted {
			targetCapabilities = append(
				targetCapabilities, capability.capability)
		}
	}
	grants, err := platformRemovalGrantsDigest(e, installation)
	if err != nil {
		return platformUpdateProjection{}, err
	}
	quota := int64(0)
	for _, capability := range parsed.Manifest.Capabilities.Required {
		if capability == "storage.files" {
			quota = 256 << 20
		}
	}
	targetProjection := platformInstallProjection{
		Format: source.format, AppID: release.appID,
		PublisherID: release.publisherID, ReleaseSHA256: release.sha256,
		TrustRevision: release.trustRevision, KeyRevision: release.keyRevision,
		KeyID: release.verificationKeyID, ControllerEpoch: control.epoch,
		ActorAuthority:       actor.authorityFingerprint,
		PlatformAPI:          platformDevelopmentAPI,
		DeclinedCapabilities: platformDeclinedCapabilities(selection),
		DataQuotaBytes:       quota, Runtime: target,
	}
	return platformUpdateProjection{
		Format: platformUpdateReviewFormat, InstallationID: installation,
		AppID: release.appID, PublisherID: release.publisherID,
		SourceReleaseSHA256: source.release, SourceGeneration: source.generation,
		TargetGeneration:     source.generation + 1,
		InstallationRevision: sourceRevision, PolicyRevision: sourcePolicy,
		TargetPolicyRevision: sourcePolicy + 1,
		TrustRevision:        release.trustRevision, ControllerEpoch: control.epoch,
		ActorAuthority:       actor.authorityFingerprint,
		CurrentDataFormat:    currentFormat,
		PreviousCapabilities: previous, TargetCapabilities: targetCapabilities,
		GrantsSHA256: grants, Target: targetProjection,
	}, nil
}

func (s *Server) createPlatformManagedUpdateReview(ctx context.Context, session platformActorSession, installation, release string, trustRevision int64, endpoint platformRuntimeEndpoint) (platformRemovalReview, error) {
	facts, err := s.observePlatformManagedRuntime(ctx, session, endpoint)
	if err != nil {
		return platformRemovalReview{}, err
	}
	return s.createPlatformManagedUpdateReviewWithRuntime(
		ctx, session, installation, release, trustRevision, &facts, endpoint)
}

func (s *Server) createPlatformManagedUpdateReviewWithRuntime(ctx context.Context, session platformActorSession, installation, release string, trustRevision int64, facts *platformManagedRuntimeFacts, endpoint platformRuntimeEndpoint) (platformRemovalReview, error) {
	if !platformValidUUID(installation) || !platformValidSHA(release) ||
		trustRevision < 1 || facts == nil ||
		!validPlatformInstallRuntimeFacts(*facts, endpoint) {
		return platformRemovalReview{}, errAppPlatformPlanChanged
	}
	target, err := managedInstallTarget(*facts, endpoint)
	if err != nil {
		return platformRemovalReview{}, err
	}
	var result platformRemovalReview
	err = s.withPlatformStagedReleaseContext(
		ctx, session, release, trustRevision,
		func(e migExec, actor platformActorFacts, control platformActorControl, verified platformStagedRelease) error {
			lock := ""
			if e.s.driver == "postgres" {
				lock = " FOR UPDATE"
			}
			var appID, publisher, desired, phase, epoch, runtimeID string
			var generation, revision, policy int64
			if err := e.queryRow(`SELECT app_id,publisher_id,desired_state,
				phase,generation,revision,policy_revision,controller_epoch,runtime_id
				FROM app_platform_installations WHERE id=?`+lock,
				installation).
				Scan(&appID, &publisher, &desired, &phase, &generation,
					&revision, &policy, &epoch, &runtimeID); err != nil ||
				desired != "enabled" || phase != "ready" ||
				epoch != control.epoch || runtimeID != target.RuntimeID ||
				generation < 1 || generation == math.MaxInt64 ||
				revision < 1 || revision == math.MaxInt64 ||
				policy < 1 || policy == math.MaxInt64 {
				return errAppPlatformPlanChanged
			}
			var activeRelease string
			var activeFormat int64
			if err := e.queryRow(`SELECT release_sha256,data_format
				FROM app_platform_activations
				WHERE installation_id=? AND generation=? AND policy_revision=?
				AND state='active'`, installation, generation, policy).
				Scan(&activeRelease, &activeFormat); err != nil ||
				!platformValidSHA(activeRelease) ||
				activeRelease == verified.sha256 || activeFormat < 1 {
				return errAppPlatformPlanChanged
			}
			if verified.appID != appID || verified.publisherID != publisher {
				return errAppPlatformPlanChanged
			}
			source, err := loadPlatformManagedUpdateSource(
				e, installation, activeRelease, generation, target)
			if err != nil {
				return err
			}
			if err := validateManagedInstallTarget(
				e, target, control.epoch); err != nil {
				return err
			}
			projection, err := platformBuildManagedUpdateProjection(
				e, installation, source, revision, policy, activeFormat,
				actor, control, target, verified)
			if err != nil {
				return err
			}
			control, err = platformRefreshActor(e, session, actor, control)
			if err != nil {
				return err
			}
			raw, err := json.Marshal(projection)
			if err != nil || len(raw) > 65536 ||
				control.nowMS > math.MaxInt64-900000 {
				return errAppPlatformPlanChanged
			}
			if !validPlatformInstallRuntimeFacts(*facts, endpoint) {
				return errAppPlatformRuntimeConflict
			}
			id, err := platformNewUUID()
			if err != nil {
				return err
			}
			digest := platformDigest(raw)
			expires := control.nowMS + 900000
			if _, err := e.exec(`INSERT INTO app_platform_review_plans(
				id,installation_id,app_id,publisher_id,action,
				source_release_sha256,target_release_sha256,
				installation_revision,policy_revision,trust_revision,
				controller_epoch,actor_scope,username,account_created,
				account_generation,authority_fingerprint,plan_sha256,
				plan_json,expires_ms,consumed_ms,created_ms,updated_ms)
				VALUES(?,?,?,?,'update',?,?,?,?,?,?,?,?,?,?,?,?,?,?,NULL,?,?)`,
				id, installation, appID, publisher, activeRelease,
				verified.sha256, revision, policy, trustRevision,
				control.epoch, actor.scope, actor.username, actor.created,
				actor.generation, actor.authorityFingerprint, digest,
				string(raw), expires, control.nowMS, control.nowMS); err != nil {
				return err
			}
			result = platformRemovalReview{
				ID: id, SHA256: digest, JSON: string(raw), ExpiresMS: expires,
			}
			_, err = platformRefreshActor(e, session, actor, control)
			return err
		})
	if err != nil {
		return platformRemovalReview{}, err
	}
	return result, nil
}

func (s *Server) consumePlatformManagedUpdateReview(ctx context.Context, session platformActorSession, planID, planSHA, key string, endpoint platformRuntimeEndpoint) (platformRemovalCommit, error) {
	facts, err := s.observePlatformManagedRuntime(ctx, session, endpoint)
	if err != nil {
		return platformRemovalCommit{}, err
	}
	return s.consumePlatformManagedUpdateReviewWithRuntime(
		ctx, session, planID, planSHA, key, &facts, endpoint)
}

func (s *Server) consumePlatformManagedUpdateReviewWithRuntime(ctx context.Context, session platformActorSession, planID, planSHA, key string, facts *platformManagedRuntimeFacts, endpoint platformRuntimeEndpoint) (platformRemovalCommit, error) {
	var result platformRemovalCommit
	if !platformValidUUID(planID) || !platformValidSHA(planSHA) ||
		!platformValidUUID(key) || facts == nil ||
		!validPlatformInstallRuntimeFacts(*facts, endpoint) {
		return result, errAppPlatformPlanChanged
	}
	target, err := managedInstallTarget(*facts, endpoint)
	if err != nil {
		return result, err
	}
	request, _ := json.Marshal(
		[]string{"portal-operation-commit/v1", planID, planSHA})
	requestDigest, keyDigest := platformDigest(request),
		platformIdempotencySHA(key)
	err = s.withPlatformTrustActorContext(
		ctx, session,
		func(e migExec, actor platformActorFacts, control platformActorControl, trust appplatform.PublisherTrust) error {
			previous, exists, err := platformFindRemovalCommit(
				e, actor.scope, keyDigest, requestDigest)
			if err != nil {
				return err
			}
			if exists {
				var action string
				if err := e.queryRow(`SELECT action FROM
					app_platform_operations WHERE id=?`,
					previous.OperationID).Scan(&action); err != nil ||
					action != "update" {
					return errAppPlatformOperationConflict
				}
				result = previous
				return nil
			}
			lock := ""
			if e.s.driver == "postgres" {
				lock = " FOR UPDATE"
			}
			var raw, digest, scope, username, created, authority,
				epoch, action, appID, publisher string
			var installation, source, release sql.NullString
			var expires, revision, policy, trustRevision,
				accountGeneration int64
			var consumed sql.NullInt64
			if err := e.queryRow(`SELECT plan_json,plan_sha256,
				actor_scope,username,account_created,account_generation,
				authority_fingerprint,expires_ms,consumed_ms,
				installation_revision,policy_revision,trust_revision,
				controller_epoch,action,installation_id,
				source_release_sha256,target_release_sha256,app_id,publisher_id
				FROM app_platform_review_plans WHERE id=?`+lock, planID).
				Scan(&raw, &digest, &scope, &username, &created,
					&accountGeneration, &authority, &expires, &consumed,
					&revision, &policy, &trustRevision, &epoch, &action,
					&installation, &source, &release, &appID, &publisher); err != nil {
				return errAppPlatformPlanChanged
			}
			control, err = platformRefreshActor(e, session, actor, control)
			if err != nil {
				return err
			}
			if consumed.Valid || expires <= control.nowMS ||
				action != "update" || !installation.Valid ||
				!source.Valid || !release.Valid || scope != actor.scope ||
				username != actor.username || created != actor.created ||
				accountGeneration != actor.generation ||
				authority != actor.authorityFingerprint ||
				epoch != control.epoch || digest != planSHA ||
				platformDigest([]byte(raw)) != planSHA ||
				!platformValidSHA(source.String) ||
				!platformValidSHA(release.String) {
				return errAppPlatformPlanChanged
			}
			var projection platformUpdateProjection
			if json.Unmarshal([]byte(raw), &projection) != nil ||
				projection.Format != platformUpdateReviewFormat ||
				projection.InstallationID != installation.String ||
				projection.AppID != appID ||
				projection.PublisherID != publisher ||
				projection.SourceReleaseSHA256 != source.String ||
				projection.Target.ReleaseSHA256 != release.String ||
				projection.InstallationRevision != revision ||
				projection.PolicyRevision != policy ||
				projection.TrustRevision != trustRevision ||
				projection.ControllerEpoch != epoch ||
				projection.ActorAuthority != authority ||
				projection.Target.Runtime == nil ||
				*projection.Target.Runtime != *target ||
				projection.TargetGeneration != projection.SourceGeneration+1 {
				return errAppPlatformPlanChanged
			}
			canonical, err := json.Marshal(projection)
			if err != nil || string(canonical) != raw ||
				!platformManagedFormat(projection.Target.Format) {
				return errAppPlatformPlanChanged
			}
			var currentApp, currentPublisher, desired, phase,
				currentEpoch, runtimeID string
			var generation, currentRevision, currentPolicy int64
			if err := e.queryRow(`SELECT app_id,publisher_id,desired_state,
				phase,generation,revision,policy_revision,controller_epoch,runtime_id
				FROM app_platform_installations WHERE id=?`+lock,
				installation.String).
				Scan(&currentApp, &currentPublisher, &desired, &phase,
					&generation, &currentRevision, &currentPolicy,
					&currentEpoch, &runtimeID); err != nil ||
				currentApp != appID || currentPublisher != publisher ||
				desired != "enabled" || phase != "ready" ||
				generation != projection.SourceGeneration ||
				currentRevision != revision ||
				currentPolicy != policy ||
				currentEpoch != epoch || runtimeID != target.RuntimeID {
				return errAppPlatformPlanChanged
			}
			var activeRelease string
			var activeFormat int64
			if err := e.queryRow(`SELECT release_sha256,data_format
				FROM app_platform_activations
				WHERE installation_id=? AND generation=? AND policy_revision=?
				AND state='active'`, installation.String,
				generation, policy).
				Scan(&activeRelease, &activeFormat); err != nil ||
				activeRelease != source.String ||
				activeFormat != projection.CurrentDataFormat {
				return errAppPlatformPlanChanged
			}
			var pending int
			if err := e.queryRow(`SELECT COUNT(*) FROM app_platform_operations
				WHERE installation_id=? AND completed_ms IS NULL`,
				installation.String).Scan(&pending); err != nil ||
				pending != 0 {
				return errAppPlatformOperationConflict
			}
			verified, err := platformVerifyStoredRelease(
				e, release.String, trustRevision, trust, control.nowMS)
			if err != nil || verified.appID != appID ||
				verified.publisherID != publisher {
				return errAppPlatformPlanChanged
			}
			sourcePlan, err := loadPlatformManagedUpdateSource(
				e, installation.String, activeRelease, generation, target)
			if err != nil {
				return err
			}
			expected, err := platformBuildManagedUpdateProjection(
				e, installation.String, sourcePlan, revision, policy,
				projection.CurrentDataFormat, actor,
				platformActorControl{epoch: epoch, nowMS: control.nowMS},
				target, verified)
			if err != nil {
				return err
			}
			expectedRaw, marshalErr := json.Marshal(expected)
			if marshalErr != nil || string(expectedRaw) != raw {
				return errAppPlatformPlanChanged
			}
			if !validPlatformInstallRuntimeFacts(*facts, endpoint) {
				return errAppPlatformRuntimeConflict
			}
			operationID, err := platformNewUUID()
			if err != nil {
				return err
			}
			if _, err := e.exec(`INSERT INTO app_platform_operations(
				id,installation_id,review_plan_id,actor_scope,
				idempotency_sha256,request_sha256,action,managed,
				source_generation,target_generation,stage,state_revision,
				row_revision,lease_owner,lease_until_ms,fence,controller_epoch,
				error_code,completed_ms,created_ms,updated_ms)
				VALUES(?,?,?,?,?,?,'update',1,?,?,'queued',1,1,NULL,NULL,0,?,
				NULL,NULL,?,?)`,
				operationID, installation.String, planID, actor.scope,
				keyDigest, requestDigest, generation,
				projection.TargetGeneration, epoch, control.nowMS,
				control.nowMS); err != nil {
				return err
			}
			changed, err := e.exec(`UPDATE app_platform_review_plans
				SET consumed_ms=?,updated_ms=? WHERE id=?
				AND consumed_ms IS NULL AND expires_ms>? AND plan_sha256=?`,
				control.nowMS, control.nowMS, planID,
				control.nowMS, planSHA)
			if err := platformLeaseCAS(changed, err); err != nil {
				return err
			}
			detail, _ := json.Marshal(struct {
				Plan, Action string
				Generation   int64
				Managed      bool
			}{planID, "update", projection.TargetGeneration, true})
			if _, err := e.exec(`INSERT INTO audit_log(
				at,actor,actor_ou,action,target_type,target_id,detail,ip)
				VALUES(?,?,?,'app_platform.operation_admitted',
				'app_platform_operation',?,?,'')`,
				databaseNowRFC3339(control.nowMS),
				actor.username, actor.ouID, operationID,
				string(detail)); err != nil {
				return err
			}
			result = platformRemovalCommit{
				OperationID: operationID, InstallationID: installation.String,
				Generation: projection.TargetGeneration,
				Revision:   1, Stage: appplatform.StageQueued,
			}
			_, err = platformRefreshActor(e, session, actor, control)
			return err
		})
	if err != nil {
		return platformRemovalCommit{}, err
	}
	return result, nil
}

func (s *Server) createPlatformUpdateReviewContext(ctx context.Context, session platformActorSession, installation, release string, trustRevision int64) (platformRemovalReview, error) {
	var runtimeID sql.NullString
	if err := s.st.queryRow(`SELECT runtime_id
		FROM app_platform_installations WHERE id=?`,
		installation).Scan(&runtimeID); err != nil {
		return platformRemovalReview{}, errAppPlatformPlanChanged
	}
	if !runtimeID.Valid {
		return s.createPlatformFrontendUpdateReviewContext(
			ctx, session, installation, release, trustRevision)
	}
	endpoint, ok := s.platformRuntimeEndpoint()
	if !ok {
		return platformRemovalReview{}, errAppPlatformRuntimeConflict
	}
	return s.createPlatformManagedUpdateReview(
		ctx, session, installation, release, trustRevision, endpoint)
}

func (s *Server) consumePlatformUpdateReviewContext(ctx context.Context, session platformActorSession, planID, planSHA, key string) (platformRemovalCommit, error) {
	var installation sql.NullString
	if err := s.st.queryRow(`SELECT installation_id
		FROM app_platform_review_plans WHERE id=? AND action='update'`,
		planID).Scan(&installation); err != nil || !installation.Valid {
		return platformRemovalCommit{}, errAppPlatformPlanChanged
	}
	var runtimeID sql.NullString
	if err := s.st.queryRow(`SELECT runtime_id
		FROM app_platform_installations WHERE id=?`,
		installation.String).Scan(&runtimeID); err != nil {
		return platformRemovalCommit{}, errAppPlatformPlanChanged
	}
	if !runtimeID.Valid {
		return s.consumePlatformUpdateReview(
			session, planID, planSHA, key)
	}
	endpoint, ok := s.platformRuntimeEndpoint()
	if !ok {
		return platformRemovalCommit{}, errAppPlatformRuntimeConflict
	}
	return s.consumePlatformManagedUpdateReview(
		ctx, session, planID, planSHA, key, endpoint)
}

func databaseNowRFC3339(ms int64) string {
	return time.UnixMilli(ms).UTC().Format(time.RFC3339)
}
