package app

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"time"
	"unicode/utf8"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
)

func validPlatformUpdateStartBootForActivation(boot platformUpdateStartBootObservation, lease platformOperationLease, intent platformUpdateStartRegistrationIntent) bool {
	if intent.start.running == nil {
		return false
	}
	age := time.Since(boot.receivedAt)
	delivery, _ := json.Marshal(intent.receipt)
	return intent.applied && !boot.receivedAt.IsZero() &&
		age >= 0 && age <= 3*time.Second && boot.lease == lease &&
		samePlatformManagedRuntime(boot.runtime, intent.start.runtime) &&
		len(boot.bootID) > 0 && len(boot.bootID) <= 128 &&
		utf8.ValidString(boot.bootID) &&
		boot.nativeID == intent.start.running.NativeID &&
		boot.deliverySHA256 == platformDigest(delivery) &&
		boot.documentSHA256 == intent.command.DocumentSHA256 &&
		boot.startSHA256 == intent.command.RunningReceiptSHA256
}

func (s *Server) activatePlatformUpdateStart(ctx context.Context, lease platformOperationLease, endpoint platformRuntimeEndpoint) (platformActivationResult, error) {
	intent, err := s.preparePlatformUpdateStartRegistration(ctx, lease)
	if err != nil {
		return platformActivationResult{}, err
	}
	defer intent.registration.Close()
	if !intent.applied {
		return platformActivationResult{}, errAppPlatformStageEvidence
	}
	boot, err := s.probePlatformUpdateStartBoot(ctx, lease, endpoint)
	if err != nil {
		return platformActivationResult{}, err
	}
	return s.commitPlatformUpdateStartActivation(ctx, lease, intent, boot)
}

func (s *Server) commitPlatformUpdateStartActivation(ctx context.Context, lease platformOperationLease, intent platformUpdateStartRegistrationIntent, boot platformUpdateStartBootObservation) (platformActivationResult, error) {
	var zero platformActivationResult
	intent = clonePlatformUpdateStartRegistrationIntent(intent)
	if (lease.snapshot.Action != appplatform.ActionUpdate &&
		!platformSameReleaseLifecycle(lease.snapshot.Action)) ||
		!lease.snapshot.Managed || lease.snapshot.Stage != appplatform.StageActivate ||
		lease.snapshot.Generation < 2 || lease.rowRevision == math.MaxInt64 ||
		!validPlatformUpdateStartBootForActivation(boot, lease, intent) {
		return zero, fmt.Errorf("update activation precondition: %w", errAppPlatformStageEvidence)
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
	var source platformCheckpointSource
	if platformSameReleaseLifecycle(lease.snapshot.Action) {
		source, err = loadPlatformCheckpointSourceForEnable(
			e, lease, runtime, facts.now)
	} else {
		source, err = loadPlatformCheckpointSource(
			e, lease, runtime, facts.now)
	}
	if err != nil {
		return zero, err
	}
	plan, targetDescriptor, err := loadPlatformManagedUpdatePlan(e, lease, source)
	if err != nil {
		return zero, err
	}
	checkpoint, _, err := loadPlatformUpdateStartCheckpoint(e, lease, plan)
	if err != nil {
		return zero, err
	}
	start, err := loadPlatformUpdateStartActivation(
		e, lease, runtime, source, checkpoint, plan)
	if err != nil {
		return zero, fmt.Errorf("update activation source: %w", err)
	}
	current, _, err := loadPlatformUpdateStartRegistration(
		e, lease, intent.start, runtime, seed, dek, facts.now, false)
	if err != nil {
		return zero, fmt.Errorf("update activation registration: %w", err)
	}
	defer current.registration.Close()
	if !current.applied ||
		!samePlatformUpdateStartRegistrationIntent(intent, current) ||
		start.resourceID != current.start.resourceID ||
		start.effectID != current.start.effectID ||
		!platformUpdateStartCommandsEqual(start.command, current.start.command) {
		return zero, fmt.Errorf("update activation current intent: %w", errAppPlatformStageEvidence)
	}
	if _, err := control.VerifyUpdateStartRegistration(
		intent.raw, intent.signature, current.start.authority,
		time.Now().UnixMilli()); err != nil {
		return zero, fmt.Errorf("update activation registration signature: %w", errAppPlatformStageEvidence)
	}
	manifest, err := appplatform.ParseDescriptor(targetDescriptor)
	if err != nil || manifest.SHA256 != plan.TargetReleaseSHA256 ||
		manifest.Manifest.Backend == nil ||
		int64(manifest.Manifest.Data.Schema) != plan.DataFormat {
		return zero, errAppPlatformStageEvidence
	}
	if source.runtimeTarget == nil {
		return zero, errAppPlatformStageEvidence
	}
	var capabilities []platformSelectedCapability
	if lease.snapshot.Action == appplatform.ActionUpdate {
		capabilities, err = platformInstallCapabilitySelection(
			manifest.Manifest, source.runtimeTarget)
		if err != nil {
			return zero, err
		}
	}
	var sourceActivation, sourceRelease, sourceState string
	var sourceGeneration, sourcePolicy, sourceFormat int64
	if platformSameReleaseLifecycle(lease.snapshot.Action) {
		if err := e.queryRow(`SELECT id,release_sha256,generation,
			policy_revision,data_format,state FROM app_platform_activations
			WHERE installation_id=? AND id=? AND generation=?`,
			lease.installationID, plan.SourceActivationID,
			plan.SourceActivationGeneration).
			Scan(&sourceActivation, &sourceRelease, &sourceGeneration,
				&sourcePolicy, &sourceFormat, &sourceState); err != nil ||
			sourceState != "retired" ||
			sourceActivation != plan.SourceActivationID ||
			sourceRelease != plan.SourceReleaseSHA256 ||
			sourceGeneration != plan.SourceGeneration ||
			sourcePolicy != plan.SourcePolicyRevision ||
			sourceFormat != plan.DataFormat {
			return zero, errAppPlatformStageEvidence
		}
	} else {
		if err := e.queryRow(`SELECT id,release_sha256,generation,
			policy_revision,data_format,state FROM app_platform_activations
			WHERE installation_id=? AND state='active'`,
			lease.installationID).
			Scan(&sourceActivation, &sourceRelease, &sourceGeneration,
				&sourcePolicy, &sourceFormat, &sourceState); err != nil {
			return zero, errAppPlatformStageEvidence
		}
		if sourceState != "active" ||
			sourceActivation != source.resourceID ||
			sourceRelease != plan.SourceReleaseSHA256 ||
			sourceGeneration != plan.SourceGeneration ||
			sourcePolicy != plan.SourcePolicyRevision ||
			sourceFormat != plan.DataFormat {
			return zero, errAppPlatformStageEvidence
		}
	}
	now, err := finalPlatformOperationLeaseCheck(e, facts)
	if err != nil {
		return zero, err
	}
	if lease.snapshot.Action == appplatform.ActionUpdate {
		if _, err := e.exec(`UPDATE app_platform_activations
			SET state='retired',updated_ms=?
			WHERE id=? AND installation_id=? AND release_sha256=?
			AND generation=? AND policy_revision=? AND data_namespace_id=?
			AND data_format=? AND state='active'`,
			now, sourceActivation, lease.installationID, sourceRelease,
			sourceGeneration, sourcePolicy, plan.DataNamespaceID,
			sourceFormat); err != nil {
			return zero, err
		}
	}
	changed, err := e.exec(`UPDATE app_platform_activations
		SET state='active',updated_ms=?
		WHERE id=? AND installation_id=? AND release_sha256=?
		AND generation=? AND policy_revision=? AND data_namespace_id=?
		AND data_format=? AND state='candidate'
		AND readiness_evidence=? AND observed_ms=?`,
		now, current.start.resourceID, lease.installationID,
		plan.TargetReleaseSHA256, lease.snapshot.Generation,
		plan.TargetPolicyRevision, plan.DataNamespaceID, plan.DataFormat,
		mustMarshalString(current.start.readiness), current.start.activationObserved)
	if err := platformLeaseCAS(changed, err); err != nil {
		return zero, err
	}
	var targetWriterFence int64
	if err := e.queryRow(`SELECT writer_fence FROM app_platform_data_namespaces
		WHERE id=? AND owner_installation_id=? AND state='active'`,
		plan.DataNamespaceID, lease.installationID).Scan(&targetWriterFence); err != nil ||
		targetWriterFence != plan.SourceWriterFence {
		return zero, errAppPlatformStageEvidence
	}
	changed, err = e.exec(`UPDATE app_platform_data_namespaces
		SET writer_fence=writer_fence+1,revision=revision+1,updated_ms=?
		WHERE id=? AND owner_installation_id=? AND state='active'
		AND writer_fence=?`,
		now, plan.DataNamespaceID, lease.installationID,
		plan.SourceWriterFence)
	if err := platformLeaseCAS(changed, err); err != nil {
		return zero, err
	}
	currentPhase := "ready"
	currentGeneration := plan.SourceGeneration
	if platformSameReleaseLifecycle(lease.snapshot.Action) {
		currentPhase = "pending"
		currentGeneration = plan.TargetGeneration
	}
	changed, err = e.exec(`UPDATE app_platform_installations
		SET generation=?,policy_revision=?,revision=revision+1,
		phase='ready',updated_ms=?
		WHERE id=? AND desired_state='enabled' AND phase=?
		AND generation=? AND policy_revision=? AND revision=?
		AND control_fence=? AND controller_epoch=? AND runtime_id=?`,
		lease.snapshot.Generation, plan.TargetPolicyRevision, now,
		lease.installationID, currentPhase, currentGeneration,
		plan.SourcePolicyRevision, facts.installationRevision,
		lease.fence, lease.controllerEpoch, runtime.id)
	if err := platformLeaseCAS(changed, err); err != nil {
		return zero, err
	}
	if lease.snapshot.Action == appplatform.ActionUpdate {
		if _, err := e.exec(
			`DELETE FROM app_platform_capability_grants WHERE installation_id=?`,
			lease.installationID); err != nil {
			return zero, err
		}
		planID := ""
		if err := e.queryRow(
			`SELECT review_plan_id FROM app_platform_operations WHERE id=?`,
			lease.id).Scan(&planID); err != nil {
			return zero, err
		}
		for _, capability := range capabilities {
			if _, err := e.exec(`INSERT INTO app_platform_capability_grants(
				installation_id,capability,granted,policy_json,policy_revision,
				review_plan_id,created_ms,updated_ms)
				VALUES(?,?,?,?,?,?,?,?)`,
				lease.installationID, capability.capability,
				boolInt(capability.granted), capability.policy,
				plan.TargetPolicyRevision, planID, now, now); err != nil {
				return zero, err
			}
		}
		if err := verifyPlatformSelectedCapabilityRows(
			e, lease.installationID, capabilities, true,
			plan.TargetPolicyRevision, planID,
			errAppPlatformStageEvidence); err != nil {
			return zero, err
		}
	} else {
		grants, err := platformRemovalGrantsDigest(
			e, lease.installationID)
		if err != nil || grants != plan.GrantsSHA256 {
			return zero, errAppPlatformStageEvidence
		}
		audience, err := platformInstallationAudienceSHA(
			e, lease.installationID)
		if err != nil || audience != plan.AudienceSHA256 {
			return zero, errAppPlatformStageEvidence
		}
	}
	model, err := appplatform.OperationFromSnapshot(lease.snapshot)
	if err != nil {
		return zero, errAppPlatformStageEvidence
	}
	next, err := model.Advance(
		lease.snapshot.Revision, appplatform.StageComplete)
	if err != nil {
		return zero, errAppPlatformStageEvidence
	}
	changed, err = e.exec(`UPDATE app_platform_operations
		SET stage='complete',state_revision=?,row_revision=row_revision+1,
		lease_owner=NULL,lease_until_ms=NULL,completed_ms=?,updated_ms=?
		WHERE id=? AND installation_id=? AND stage='activate'
		AND state_revision=? AND row_revision=? AND controller_epoch=?
		AND fence=? AND lease_owner=? AND lease_until_ms=?
		AND lease_until_ms>? AND completed_ms IS NULL`,
		int64(next.Snapshot().Revision), now, now, lease.id,
		lease.installationID, int64(lease.snapshot.Revision),
		lease.rowRevision, lease.controllerEpoch, lease.fence,
		lease.owner, lease.leaseUntilMS, now)
	if err := platformLeaseCAS(changed, err); err != nil {
		return zero, err
	}
	bootDigest := platformDigest([]byte(boot.bootID))
	detail, _ := json.Marshal(struct {
		SourceActivation string `json:"source_activation_id"`
		TargetActivation string `json:"target_activation_id"`
		Generation       int64  `json:"generation"`
		PolicyRevision   int64  `json:"policy_revision"`
		BootSHA256       string `json:"boot_sha256"`
		DocumentSHA256   string `json:"document_sha256"`
	}{sourceActivation, current.start.resourceID,
		int64(lease.snapshot.Generation), plan.TargetPolicyRevision,
		bootDigest, current.command.DocumentSHA256})
	audit := "app_platform.managed_update_activated"
	if platformSameReleaseLifecycle(lease.snapshot.Action) {
		audit = "app_platform.managed_enable_activated"
		if lease.snapshot.Action == appplatform.ActionRepair {
			audit = "app_platform.managed_repair_activated"
		}
	}
	if _, err := e.exec(`INSERT INTO audit_log(
		at,actor,actor_ou,action,target_type,target_id,detail,ip)
		VALUES(?,'system',0,?,
		'app_platform_installation',?,?,'')`,
		time.UnixMilli(now).UTC().Format(time.RFC3339), audit,
		lease.installationID, string(detail)); err != nil {
		return zero, err
	}
	if _, err := finalPlatformOperationLeaseCheck(e, facts); err != nil {
		return zero, err
	}
	if !validPlatformUpdateStartBootForActivation(boot, lease, current) {
		return zero, errAppPlatformStageEvidence
	}
	var active int
	var activeRelease string
	var activeGeneration, activePolicy int64
	if err := e.queryRow(`SELECT COUNT(*),COALESCE(MAX(release_sha256),''),
		COALESCE(MAX(generation),0),COALESCE(MAX(policy_revision),0)
		FROM app_platform_activations WHERE installation_id=? AND state='active'`,
		lease.installationID).
		Scan(&active, &activeRelease, &activeGeneration, &activePolicy); err != nil ||
		active != 1 || activeRelease != plan.TargetReleaseSHA256 ||
		activeGeneration != int64(lease.snapshot.Generation) ||
		activePolicy != plan.TargetPolicyRevision {
		return zero, errAppPlatformStageEvidence
	}
	if err := tx.Commit(); err != nil {
		return zero, err
	}
	return platformActivationResult{
		activationID:   current.start.resourceID,
		installationID: lease.installationID,
		snapshot:       next.Snapshot(),
	}, nil
}

func mustMarshalString(value any) string {
	raw, _ := json.Marshal(value)
	return string(raw)
}
