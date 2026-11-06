package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/journal"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/transport"
)

func (s *Server) reservePlatformRestoreStart(ctx context.Context, expected platformOperationLease, endpoint platformRuntimeEndpoint) (platformUpdateStartAdmission, error) {
	intent, err := s.preparePlatformRestoreStart(ctx, expected)
	if err != nil {
		return platformUpdateStartAdmission{}, err
	}
	if err := s.checkPlatformManagedOperationRoot(
		ctx, expected.id, intent.runtime, intent.authority, endpoint); err != nil {
		return platformUpdateStartAdmission{}, err
	}
	proof, err := platformReserveUpdateStart(ctx, endpoint, intent)
	if err != nil {
		return platformUpdateStartAdmission{}, err
	}
	reservation, ok := validPlatformUpdateStartObservation(
		proof, intent, endpoint)
	if !ok {
		return platformUpdateStartAdmission{}, errAppPlatformStageEvidence
	}
	return s.commitPlatformRestoreStartReservation(
		ctx, expected, intent, endpoint, reservation)
}

func (s *Server) executePlatformRestoreStart(ctx context.Context, expected platformOperationLease, endpoint platformRuntimeEndpoint) (platformOperationLease, error) {
	admission, err := s.reservePlatformRestoreStart(
		ctx, expected, endpoint)
	if err != nil {
		return platformOperationLease{}, err
	}
	if err := s.checkPlatformManagedOperationRoot(
		ctx, expected.id, admission.intent.runtime,
		admission.intent.authority, endpoint); err != nil {
		return platformOperationLease{}, err
	}
	proof, err := platformExecuteUpdateStart(
		ctx, endpoint, admission.intent)
	if err != nil {
		return platformOperationLease{}, err
	}
	return s.commitPlatformRestoreStartRunning(
		ctx, admission.lease, admission.intent, endpoint, proof)
}

func (s *Server) probePlatformRestoreStart(ctx context.Context, expected platformOperationLease, endpoint platformRuntimeEndpoint) (platformOperationLease, error) {
	intent, err := s.preparePlatformRestoreProbe(ctx, expected)
	if err != nil {
		return platformOperationLease{}, err
	}
	if intent.running == nil || intent.reservation == nil {
		return platformOperationLease{}, errAppPlatformStageEvidence
	}
	if err := s.checkPlatformManagedOperationRoot(
		ctx, expected.id, intent.runtime, intent.authority, endpoint); err != nil {
		return platformOperationLease{}, err
	}
	proof, err := platformProbeUpdateStart(ctx, endpoint, intent)
	if err != nil {
		return platformOperationLease{}, err
	}
	return s.commitPlatformRestoreStartReadiness(
		ctx, expected, intent, endpoint, proof)
}

func (s *Server) dispatchPlatformRestoreStartRegistration(ctx context.Context, lease platformOperationLease, endpoint platformRuntimeEndpoint) (platformOperationLease, error) {
	return s.observePlatformRestoreStartRegistration(
		ctx, lease, endpoint, transport.UpdateStartRegistrationDeliver)
}

func (s *Server) completePlatformRestoreStartRegistration(ctx context.Context, lease platformOperationLease, endpoint platformRuntimeEndpoint) (platformOperationLease, error) {
	return s.observePlatformRestoreStartRegistration(
		ctx, lease, endpoint, transport.UpdateStartRegistrationRead)
}

func (s *Server) observePlatformRestoreStartRegistration(ctx context.Context, lease platformOperationLease, endpoint platformRuntimeEndpoint, action transport.WritableLifecycleAction) (platformOperationLease, error) {
	intent, err := s.preparePlatformRestoreStartRegistration(ctx, lease)
	if err != nil {
		return platformOperationLease{}, err
	}
	defer intent.registration.Close()
	if err := s.checkPlatformManagedRuntimeRoot(
		ctx, lease, intent.start.runtime, intent.start.authority, endpoint); err != nil {
		return platformOperationLease{}, err
	}
	proof, err := platformRunUpdateStartRegistration(
		ctx, endpoint, intent, action)
	if err != nil {
		return platformOperationLease{}, err
	}
	return s.commitPlatformRestoreStartRegistration(
		ctx, lease, intent, endpoint, proof)
}

func (s *Server) commitPlatformRestoreStartRegistration(ctx context.Context, lease platformOperationLease, intent platformUpdateStartRegistrationIntent, endpoint platformRuntimeEndpoint, proof platformUpdateStartRegistrationObservation) (platformOperationLease, error) {
	var zero platformOperationLease
	if lease.snapshot.Action != appplatform.ActionRestore ||
		!lease.snapshot.Managed ||
		lease.snapshot.Stage != appplatform.StageActivate ||
		lease.rowRevision == math.MaxInt64 ||
		!validPlatformUpdateStartRegistrationObservation(
			proof, intent, endpoint) {
		return zero, errAppPlatformStageEvidence
	}
	current, err := s.preparePlatformRestoreStartRegistration(ctx, lease)
	if err != nil || !samePlatformUpdateStartRegistrationIntent(
		intent, current) {
		return zero, errAppPlatformStageEvidence
	}
	defer current.registration.Close()
	receipt, err := proof.UpdateStartRegistrationReceipt()
	if err != nil {
		return zero, errAppPlatformStageEvidence
	}
	encoded, err := json.Marshal(receipt)
	if err != nil || len(encoded) > 8192 {
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
	runtime, seed, err := s.platformDataRuntime(
		e, lease.controllerEpoch, dek)
	if err != nil {
		return zero, err
	}
	defer clear(seed)
	facts, err := loadPlatformBoundOperationLease(e, lease)
	if err != nil {
		return zero, err
	}
	result := lease
	expected := current
	if current.applied {
		if current.receipt != receipt {
			return zero, errAppPlatformStageEvidence
		}
	} else {
		now, err := finalPlatformOperationLeaseCheck(e, facts)
		if err != nil {
			return zero, err
		}
		changed, err := e.exec(`UPDATE app_platform_operation_effects
			SET state='applied',observed_fence=?,receipt_json=?,updated_ms=?
			WHERE operation_id=? AND stage='activate' AND effect_number=1
			AND effect_id=? AND kind='update-start-registration'
			AND payload_sha256=? AND runtime_id=? AND controller_epoch=?
			AND state='prepared' AND receipt_json='{}'
			AND admitted_fence=? AND observed_fence=0
			AND created_ms=? AND updated_ms=?`,
			lease.fence, string(encoded), now, lease.id,
			current.command.EffectID, current.command.PayloadSHA256,
			runtime.id, lease.controllerEpoch, lease.fence,
			current.created, current.updated)
		if err := platformLeaseCAS(changed, err); err != nil {
			return zero, err
		}
		result.rowRevision++
		changed, err = e.exec(`UPDATE app_platform_operations
			SET row_revision=row_revision+1,updated_ms=?
			WHERE id=? AND installation_id=? AND stage='activate'
			AND state_revision=? AND row_revision=? AND controller_epoch=?
			AND fence=? AND lease_owner=? AND lease_until_ms=?
			AND lease_until_ms>? AND completed_ms IS NULL`,
			now, lease.id, lease.installationID,
			int64(lease.snapshot.Revision), lease.rowRevision,
			lease.controllerEpoch, lease.fence, lease.owner,
			lease.leaseUntilMS, now)
		if err := platformLeaseCAS(changed, err); err != nil {
			return zero, err
		}
		detail, _ := json.Marshal(struct {
			EffectID      string `json:"effect_id"`
			ResourceID    string `json:"resource_id"`
			ReceiptSHA256 string `json:"receipt_sha256"`
		}{current.command.EffectID, current.start.resourceID,
			platformDigest(encoded)})
		if _, err := e.exec(`INSERT INTO audit_log(
			at,actor,actor_ou,action,target_type,target_id,detail,ip)
			VALUES(?,'system',0,'app_platform.restore_start_registration_observed',
			'app_platform_operation',?,?,'')`,
			time.UnixMilli(now).UTC().Format(time.RFC3339),
			lease.id, string(detail)); err != nil {
			return zero, err
		}
		expected.applied, expected.receipt, expected.updated =
			true, receipt, now
	}
	if _, err := finalPlatformOperationLeaseCheck(e, facts); err != nil {
		return zero, err
	}
	if err := tx.Commit(); err != nil {
		return zero, err
	}
	after, err := s.preparePlatformRestoreStartRegistration(
		ctx, result)
	if err != nil || !samePlatformUpdateStartRegistrationIntent(
		expected, after) {
		return zero, errAppPlatformStageEvidence
	}
	defer after.registration.Close()
	return result, nil
}

func (s *Server) probePlatformRestoreStartBoot(ctx context.Context, lease platformOperationLease, endpoint platformRuntimeEndpoint) (platformUpdateStartBootObservation, error) {
	var zero platformUpdateStartBootObservation
	intent, err := s.preparePlatformRestoreStartRegistration(ctx, lease)
	if err != nil {
		return zero, err
	}
	defer intent.registration.Close()
	if !intent.applied {
		return zero, errAppPlatformStageEvidence
	}
	if err := s.checkPlatformManagedRuntimeRoot(
		ctx, lease, intent.start.runtime, intent.start.authority, endpoint); err != nil {
		return zero, err
	}
	signer, err := intent.registration.Signer("")
	if err != nil {
		return zero, err
	}
	defer clear(signer.Key)
	challenge, err := signer.NewChallenge(time.Now())
	if err != nil {
		return zero, err
	}
	proof, err := platformHandshakeUpdateStartBackend(
		ctx, endpoint, intent, challenge)
	if err != nil {
		return zero, err
	}
	return s.finishPlatformRestoreStartBoot(
		ctx, lease, intent, endpoint, challenge, proof)
}

func (s *Server) finishPlatformRestoreStartBoot(ctx context.Context, lease platformOperationLease, intent platformUpdateStartRegistrationIntent, endpoint platformRuntimeEndpoint, challenge string, proof platformUpdateStartHandshakeObservation) (platformUpdateStartBootObservation, error) {
	var zero platformUpdateStartBootObservation
	intent = clonePlatformUpdateStartRegistrationIntent(intent)
	if lease.snapshot.Action != appplatform.ActionRestore ||
		!lease.snapshot.Managed ||
		lease.snapshot.Stage != appplatform.StageActivate ||
		lease.snapshot.Generation < 2 ||
		!validPlatformUpdateStartHandshakeObservation(
			proof, intent, endpoint, challenge) {
		return zero, errAppPlatformStageEvidence
	}
	signer, err := intent.registration.Signer("")
	if err != nil {
		return zero, err
	}
	defer clear(signer.Key)
	boot, err := signer.VerifyAnswer(
		challenge, proof.Evidence().Answer, time.Now())
	if err != nil {
		return zero, appplatform.ErrBackendHandshake
	}
	current, err := s.preparePlatformRestoreStartRegistration(
		ctx, lease)
	if err != nil {
		return zero, err
	}
	defer current.registration.Close()
	if !samePlatformUpdateStartRegistrationIntent(intent, current) ||
		!validPlatformUpdateStartHandshakeObservation(
			proof, intent, endpoint, challenge) {
		return zero, errAppPlatformStageEvidence
	}
	currentSigner, err := current.registration.Signer("")
	if err != nil {
		return zero, err
	}
	defer clear(currentSigner.Key)
	currentBoot, err := currentSigner.VerifyAnswer(
		challenge, proof.Evidence().Answer, time.Now())
	if err != nil || currentBoot != boot {
		return zero, appplatform.ErrBackendHandshake
	}
	delivery, _ := json.Marshal(current.receipt)
	return platformUpdateStartBootObservation{
		lease: lease, runtime: current.start.runtime,
		bootID: boot, nativeID: proof.Evidence().NativeID,
		deliverySHA256: platformDigest(delivery),
		documentSHA256: current.command.DocumentSHA256,
		startSHA256:    current.command.RunningReceiptSHA256,
		receivedAt:     proof.ReceivedAt(),
	}, nil
}

func (s *Server) activatePlatformRestoreStart(ctx context.Context, lease platformOperationLease, endpoint platformRuntimeEndpoint) (platformActivationResult, error) {
	intent, err := s.preparePlatformRestoreStartRegistration(ctx, lease)
	if err != nil {
		return platformActivationResult{}, err
	}
	defer intent.registration.Close()
	if !intent.applied {
		return platformActivationResult{}, errAppPlatformStageEvidence
	}
	boot, err := s.probePlatformRestoreStartBoot(
		ctx, lease, endpoint)
	if err != nil {
		return platformActivationResult{}, err
	}
	return s.commitPlatformRestoreStartActivation(
		ctx, lease, intent, boot)
}

func (s *Server) commitPlatformRestoreStartActivation(ctx context.Context, lease platformOperationLease, intent platformUpdateStartRegistrationIntent, boot platformUpdateStartBootObservation) (platformActivationResult, error) {
	var zero platformActivationResult
	if lease.snapshot.Action != appplatform.ActionRestore ||
		!lease.snapshot.Managed ||
		lease.snapshot.Stage != appplatform.StageActivate ||
		lease.rowRevision == math.MaxInt64 ||
		!validPlatformUpdateStartBootForActivation(
			boot, lease, intent) {
		return zero, errAppPlatformStageEvidence
	}
	current, err := s.preparePlatformRestoreStartRegistration(
		ctx, lease)
	if err != nil || !current.applied ||
		!samePlatformUpdateStartRegistrationIntent(intent, current) {
		return zero, errAppPlatformStageEvidence
	}
	defer current.registration.Close()
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
	runtime, seed, err := s.platformDataRuntime(
		e, lease.controllerEpoch, dek)
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
	source, err := loadPlatformCheckpointSource(
		e, lease, runtime, facts.now)
	if err != nil {
		return zero, err
	}
	resume, err := platformOperationIsResume(e, lease.id)
	if err != nil {
		return zero, err
	}
	var sourceActivation, sourceRelease, sourceState string
	var sourceGeneration, sourcePolicy, sourceFormat, sourceWriter int64
	if resume {
		// A restored Portal retired every activation, and the resume brought none up before this
		// one: there is nothing to retire, and nothing may be active beside the candidate.
		var active int
		if err := e.queryRow(`SELECT COUNT(*) FROM app_platform_activations
			WHERE installation_id=? AND state='active'`, lease.installationID).
			Scan(&active); err != nil || active != 0 {
			return zero, errAppPlatformStageEvidence
		}
	} else if err := e.queryRow(`SELECT id,release_sha256,generation,
		policy_revision,data_format,state FROM app_platform_activations
		WHERE installation_id=? AND state='active'`,
		lease.installationID).
		Scan(&sourceActivation, &sourceRelease, &sourceGeneration,
			&sourcePolicy, &sourceFormat, &sourceState); err != nil ||
		sourceState != "active" ||
		sourceActivation != source.resourceID ||
		sourceRelease != current.start.command.Payload.SourceReleaseSHA256 ||
		sourceGeneration != int64(lease.snapshot.Generation)-1 ||
		sourcePolicy != current.start.plan.SourcePolicyRevision ||
		sourceFormat != current.start.plan.DataFormat {
		return zero, errAppPlatformStageEvidence
	}
	if err := e.queryRow(`SELECT writer_fence
		FROM app_platform_data_namespaces
		WHERE id=? AND owner_installation_id=? AND state='active'`,
		current.start.plan.DataNamespaceID, lease.installationID).
		Scan(&sourceWriter); err != nil ||
		sourceWriter != current.start.plan.SourceWriterFence {
		return zero, errAppPlatformStageEvidence
	}
	now, err := finalPlatformOperationLeaseCheck(e, facts)
	if err != nil {
		return zero, err
	}
	if !resume {
		changed, err := e.exec(`UPDATE app_platform_activations
			SET state='retired',updated_ms=?
			WHERE id=? AND installation_id=? AND release_sha256=?
			AND generation=? AND policy_revision=? AND data_namespace_id=?
			AND data_format=? AND state='active'`,
			now, sourceActivation, lease.installationID, sourceRelease,
			sourceGeneration, sourcePolicy, current.start.plan.DataNamespaceID,
			sourceFormat)
		if err := platformLeaseCAS(changed, err); err != nil {
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
		current.start.command.Payload.TargetReleaseSHA256,
		lease.snapshot.Generation, current.start.plan.TargetPolicyRevision,
		current.start.plan.DataNamespaceID, current.start.plan.DataFormat,
		mustMarshalString(current.start.readiness),
		current.start.activationObserved)
	if err := platformLeaseCAS(changed, err); err != nil {
		return zero, err
	}
	changed, err = e.exec(`UPDATE app_platform_data_namespaces
		SET writer_fence=writer_fence+1,revision=revision+1,updated_ms=?
		WHERE id=? AND owner_installation_id=? AND state='active'
		AND writer_fence=?`,
		now, current.start.plan.DataNamespaceID, lease.installationID,
		current.start.plan.SourceWriterFence)
	if err := platformLeaseCAS(changed, err); err != nil {
		return zero, err
	}
	if resume {
		// The Portal's restore raised the installation's policy revision past the capability rows the
		// resume was reviewed against. The administrator approved resuming with exactly those rows, so
		// they are carried to the resumed revision (never lowered); launch accepts only rows at the
		// installation's own revision.
		if _, err := e.exec(`UPDATE app_platform_capability_grants
			SET policy_revision=?,updated_ms=?
			WHERE installation_id=? AND policy_revision<?`,
			current.start.plan.TargetPolicyRevision, now,
			lease.installationID, current.start.plan.TargetPolicyRevision); err != nil {
			return zero, err
		}
	}
	activationPhase, err := platformReadyPhaseForInstallation(
		e, lease.installationID, current.start.plan.TargetPolicyRevision)
	if err != nil {
		return zero, err
	}
	changed, err = e.exec(`UPDATE app_platform_installations
		SET generation=?,policy_revision=?,revision=revision+1,
		phase=?,updated_ms=?
		WHERE id=? AND desired_state='enabled' AND phase='recovery_required'
		AND generation=? AND policy_revision=? AND revision=?
		AND control_fence=? AND controller_epoch=? AND runtime_id=?`,
		lease.snapshot.Generation, current.start.plan.TargetPolicyRevision,
		activationPhase, now, lease.installationID, current.start.plan.SourceGeneration,
		current.start.plan.SourcePolicyRevision, facts.installationRevision,
		lease.fence, lease.controllerEpoch, runtime.id)
	if err := platformLeaseCAS(changed, err); err != nil {
		return zero, err
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
	detail, _ := json.Marshal(struct {
		SourceActivation string `json:"source_activation_id"`
		TargetActivation string `json:"target_activation_id"`
		Generation       int64  `json:"generation"`
		PolicyRevision   int64  `json:"policy_revision"`
		BootSHA256       string `json:"boot_sha256"`
		DocumentSHA256   string `json:"document_sha256"`
	}{sourceActivation, current.start.resourceID,
		int64(lease.snapshot.Generation),
		current.start.plan.TargetPolicyRevision,
		platformDigest([]byte(boot.bootID)),
		current.command.DocumentSHA256})
	if _, err := e.exec(`INSERT INTO audit_log(
		at,actor,actor_ou,action,target_type,target_id,detail,ip)
		VALUES(?,'system',0,'app_platform.restore_activated',
		'app_platform_installation',?,?,'')`,
		time.UnixMilli(now).UTC().Format(time.RFC3339),
		lease.installationID, string(detail)); err != nil {
		return zero, err
	}
	var active int
	var activeRelease string
	var activeGeneration, activePolicy int64
	if err := e.queryRow(`SELECT COUNT(*),COALESCE(MAX(release_sha256),''),
		COALESCE(MAX(generation),0),COALESCE(MAX(policy_revision),0)
		FROM app_platform_activations
		WHERE installation_id=? AND state='active'`,
		lease.installationID).
		Scan(&active, &activeRelease, &activeGeneration, &activePolicy); err != nil ||
		active != 1 ||
		activeRelease != current.start.command.Payload.TargetReleaseSHA256 ||
		activeGeneration != int64(lease.snapshot.Generation) ||
		activePolicy != current.start.plan.TargetPolicyRevision {
		return zero, errAppPlatformStageEvidence
	}
	var writerFence int64
	if err := e.queryRow(`SELECT writer_fence
		FROM app_platform_data_namespaces
		WHERE id=? AND owner_installation_id=? AND state='active'`,
		current.start.plan.DataNamespaceID, lease.installationID).
		Scan(&writerFence); err != nil ||
		writerFence != mustPlatformCheckpointInt(
			current.start.command.Payload.TargetWriterFence) {
		return zero, errAppPlatformStageEvidence
	}
	var phase string
	var generation, policyRevision, installationRevision int64
	if err := e.queryRow(`SELECT phase,generation,policy_revision,revision
		FROM app_platform_installations WHERE id=?`,
		lease.installationID).
		Scan(&phase, &generation, &policyRevision,
			&installationRevision); err != nil ||
		phase != "ready" ||
		generation != int64(lease.snapshot.Generation) ||
		policyRevision != current.start.plan.TargetPolicyRevision ||
		installationRevision != facts.installationRevision+1 {
		return zero, errAppPlatformStageEvidence
	}
	if _, err := finalPlatformOperationLeaseCheck(e, facts); err != nil {
		return zero, err
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

func samePlatformRestoreStartIntent(a, b platformUpdateStartIntent) bool {
	if a.running == nil || b.running == nil {
		return a.running == nil && b.running == nil
	}
	return platformUpdateStartCommandsEqual(a.command, b.command) &&
		a.resourceID == b.resourceID && a.effectID == b.effectID &&
		platformDigest(mustMarshalPlatformUpdateStartReceipt(*a.running)) ==
			platformDigest(mustMarshalPlatformUpdateStartReceipt(*b.running)) &&
		a.authority.Fingerprint == b.authority.Fingerprint &&
		bytes.Equal(a.authority.PublicKey, b.authority.PublicKey)
}

func (s *Server) commitPlatformRestoreStartReservation(ctx context.Context, expected platformOperationLease, intent platformUpdateStartIntent, endpoint platformRuntimeEndpoint, reservation journal.UpdateStartReservation) (platformUpdateStartAdmission, error) {
	var zero platformUpdateStartAdmission
	if expected.snapshot.Action != appplatform.ActionRestore ||
		!expected.snapshot.Managed ||
		expected.snapshot.Stage != appplatform.StageStart ||
		!platformUpdateStartReservationMatches(
			reservation, intent.command, intent.resourceID, intent.effectID) {
		return zero, errAppPlatformStageEvidence
	}
	current, err := s.preparePlatformRestoreStart(ctx, expected)
	if err != nil || !platformUpdateStartCommandsEqual(
		current.command, intent.command) ||
		current.resourceID != intent.resourceID ||
		current.effectID != intent.effectID {
		return zero, errAppPlatformStageEvidence
	}
	encoded, err := json.Marshal(reservation)
	if err != nil {
		return zero, err
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
	now, err := finalPlatformOperationLeaseCheck(e, facts)
	if err != nil {
		return zero, err
	}
	changed, err := e.exec(`UPDATE app_platform_operation_effects
		SET observed_fence=?,receipt_json=?,updated_ms=?
		WHERE operation_id=? AND stage='start' AND effect_number=1
		AND effect_id=? AND kind='restore-start' AND payload_sha256=?
		AND runtime_id=? AND controller_epoch=? AND admitted_fence=?
		AND state='prepared' AND observed_fence=0 AND receipt_json='{}'`,
		expected.fence, string(encoded), now, expected.id,
		intent.effectID, intent.command.PayloadSHA256, runtime.id,
		expected.controllerEpoch, expected.fence)
	if err := platformLeaseCAS(changed, err); err != nil {
		return zero, err
	}
	result := expected
	result.rowRevision++
	changed, err = e.exec(`UPDATE app_platform_operations
		SET row_revision=row_revision+1,updated_ms=?
		WHERE id=? AND installation_id=? AND stage='start'
		AND state_revision=? AND row_revision=? AND controller_epoch=?
		AND fence=? AND lease_owner=? AND lease_until_ms=?
		AND lease_until_ms>? AND completed_ms IS NULL`,
		now, expected.id, expected.installationID,
		int64(expected.snapshot.Revision), expected.rowRevision,
		expected.controllerEpoch, expected.fence, expected.owner,
		expected.leaseUntilMS, now)
	if err := platformLeaseCAS(changed, err); err != nil {
		return zero, err
	}
	detail, _ := json.Marshal(struct {
		ResourceID string `json:"resource_id"`
		ReservedAt int64  `json:"reserved_ms"`
	}{reservation.ResourceID, now})
	if _, err := e.exec(`INSERT INTO audit_log(
		at,actor,actor_ou,action,target_type,target_id,detail,ip)
		VALUES(?,'system',0,'app_platform.restore_start_reserved',
		'app_platform_operation',?,?,'')`,
		time.UnixMilli(now).UTC().Format(time.RFC3339),
		expected.id, string(detail)); err != nil {
		return zero, err
	}
	if _, err := finalPlatformOperationLeaseCheck(e, facts); err != nil {
		return zero, err
	}
	if err := tx.Commit(); err != nil {
		return zero, err
	}
	intent.reservation = &reservation
	return platformUpdateStartAdmission{
		intent: intent, reservation: reservation, lease: result,
	}, nil
}

func (s *Server) commitPlatformRestoreStartRunning(ctx context.Context, expected platformOperationLease, intent platformUpdateStartIntent, endpoint platformRuntimeEndpoint, observation platformUpdateStartRunningObservation) (platformOperationLease, error) {
	var zero platformOperationLease
	if expected.snapshot.Action != appplatform.ActionRestore ||
		!expected.snapshot.Managed ||
		expected.snapshot.Stage != appplatform.StageStart ||
		expected.rowRevision == math.MaxInt64 ||
		intent.reservation == nil {
		return zero, fmt.Errorf("restore-start running precondition: %w", errAppPlatformStageEvidence)
	}
	running, ok := validPlatformUpdateStartRunningObservation(
		observation, intent, endpoint)
	if !ok || !platformUpdateStartRunningMatches(
		running, intent.command, intent.resourceID,
		intent.effectID, intent.reservation) {
		return zero, fmt.Errorf("restore-start running observation: %w", errAppPlatformStageEvidence)
	}
	encoded, err := json.Marshal(running)
	if err != nil || len(encoded) > 16384 {
		return zero, errAppPlatformStageEvidence
	}
	reservationRaw, _ := json.Marshal(*intent.reservation)
	current, err := s.preparePlatformRestoreStart(ctx, expected)
	if err != nil || current.reservation == nil ||
		!platformUpdateStartCommandsEqual(current.command, intent.command) ||
		!platformUpdateStartReservationsEqual(
			*current.reservation, *intent.reservation) {
		return zero, fmt.Errorf("restore-start running current: %w", errAppPlatformStageEvidence)
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
	now, err := finalPlatformOperationLeaseCheck(e, facts)
	if err != nil {
		return zero, err
	}
	changed, err := e.exec(`UPDATE app_platform_operation_effects
		SET state='applied',observed_fence=?,receipt_json=?,updated_ms=?
		WHERE operation_id=? AND stage='start' AND effect_number=1
		AND effect_id=? AND kind='restore-start' AND payload_sha256=?
		AND runtime_id=? AND controller_epoch=? AND admitted_fence=?
		AND state='prepared' AND observed_fence=? AND receipt_json=?`,
		expected.fence, string(encoded), now, expected.id,
		intent.effectID, intent.command.PayloadSHA256, runtime.id,
		expected.controllerEpoch, expected.fence, expected.fence,
		string(reservationRaw))
	if err := platformLeaseCAS(changed, err); err != nil {
		return zero, err
	}
	model, err := appplatform.OperationFromSnapshot(expected.snapshot)
	if err != nil {
		return zero, errAppPlatformStageEvidence
	}
	next, err := model.Advance(
		expected.snapshot.Revision, appplatform.StageProbe)
	if err != nil {
		return zero, errAppPlatformStageEvidence
	}
	result := expected
	result.snapshot, result.rowRevision = next.Snapshot(), expected.rowRevision+1
	changed, err = e.exec(`UPDATE app_platform_operations
		SET stage='probe',state_revision=?,row_revision=row_revision+1,updated_ms=?
		WHERE id=? AND installation_id=? AND controller_epoch=?
		AND stage='start' AND state_revision=? AND row_revision=?
		AND fence=? AND lease_owner=? AND lease_until_ms=?
		AND lease_until_ms>? AND completed_ms IS NULL`,
		int64(result.snapshot.Revision), now, expected.id,
		expected.installationID, expected.controllerEpoch,
		int64(expected.snapshot.Revision), expected.rowRevision,
		expected.fence, expected.owner, expected.leaseUntilMS, now)
	if err := platformLeaseCAS(changed, err); err != nil {
		return zero, err
	}
	detail, _ := json.Marshal(struct {
		ResourceID string `json:"resource_id"`
		NativeID   string `json:"native_id"`
		Release    string `json:"release_sha256"`
	}{intent.resourceID, running.NativeID, running.TargetReleaseSHA256})
	if _, err := e.exec(`INSERT INTO audit_log(
		at,actor,actor_ou,action,target_type,target_id,detail,ip)
		VALUES(?,'system',0,'app_platform.restore_start_running',
		'app_platform_operation',?,?,'')`,
		time.UnixMilli(now).UTC().Format(time.RFC3339),
		expected.id, string(detail)); err != nil {
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

func (s *Server) commitPlatformRestoreStartReadiness(ctx context.Context, expected platformOperationLease, intent platformUpdateStartIntent, endpoint platformRuntimeEndpoint, observation platformUpdateStartReadinessObservation) (platformOperationLease, error) {
	var zero platformOperationLease
	if expected.snapshot.Action != appplatform.ActionRestore ||
		!expected.snapshot.Managed ||
		expected.snapshot.Stage != appplatform.StageProbe ||
		expected.rowRevision == math.MaxInt64 ||
		intent.running == nil || intent.reservation == nil {
		return zero, errAppPlatformStageEvidence
	}
	readiness, ok := validPlatformUpdateStartReadinessObservation(
		observation, intent, endpoint)
	if !ok {
		return zero, errAppPlatformStageEvidence
	}
	encoded, err := json.Marshal(readiness)
	if err != nil || len(encoded) > 4096 {
		return zero, errAppPlatformStageEvidence
	}
	observed, err := strconv.ParseInt(readiness.ObservedMS, 10, 64)
	if err != nil || observed <= 0 {
		return zero, errAppPlatformStageEvidence
	}
	current, err := s.preparePlatformRestoreProbe(ctx, expected)
	if err != nil || current.running == nil ||
		!samePlatformRestoreStartIntent(intent, current) {
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
	_, seed, err := s.platformDataRuntime(
		e, expected.controllerEpoch, dek)
	if err != nil {
		return zero, err
	}
	defer clear(seed)
	facts, err := loadPlatformBoundOperationLease(e, expected)
	if err != nil {
		return zero, err
	}
	now, err := finalPlatformOperationLeaseCheck(e, facts)
	if err != nil {
		return zero, err
	}
	changed, err := e.exec(`UPDATE app_platform_activations
		SET readiness_evidence=?,observed_ms=?,updated_ms=?
		WHERE id=? AND installation_id=? AND release_sha256=?
		AND generation=? AND policy_revision=? AND data_namespace_id=?
		AND data_format=? AND state='candidate'
		AND readiness_evidence='{}' AND observed_ms IS NULL`,
		string(encoded), observed, now, intent.resourceID,
		expected.installationID, intent.command.Payload.TargetReleaseSHA256,
		expected.snapshot.Generation,
		intent.plan.TargetPolicyRevision, intent.plan.DataNamespaceID,
		intent.plan.DataFormat)
	if err := platformLeaseCAS(changed, err); err != nil {
		return zero, err
	}
	model, err := appplatform.OperationFromSnapshot(expected.snapshot)
	if err != nil {
		return zero, errAppPlatformStageEvidence
	}
	next, err := model.Advance(
		expected.snapshot.Revision, appplatform.StageActivate)
	if err != nil {
		return zero, errAppPlatformStageEvidence
	}
	result := expected
	result.snapshot, result.rowRevision = next.Snapshot(), expected.rowRevision+1
	changed, err = e.exec(`UPDATE app_platform_operations
		SET stage='activate',state_revision=?,row_revision=row_revision+1,updated_ms=?
		WHERE id=? AND installation_id=? AND controller_epoch=?
		AND stage='probe' AND state_revision=? AND row_revision=?
		AND fence=? AND lease_owner=? AND lease_until_ms=?
		AND lease_until_ms>? AND completed_ms IS NULL`,
		int64(result.snapshot.Revision), now, expected.id,
		expected.installationID, expected.controllerEpoch,
		int64(expected.snapshot.Revision), expected.rowRevision,
		expected.fence, expected.owner, expected.leaseUntilMS, now)
	if err := platformLeaseCAS(changed, err); err != nil {
		return zero, err
	}
	detail, _ := json.Marshal(struct {
		NativeID string `json:"native_id"`
		Protocol string `json:"protocol"`
		Observed string `json:"observed_ms"`
	}{readiness.NativeID, readiness.Protocol, readiness.ObservedMS})
	if _, err := e.exec(`INSERT INTO audit_log(
		at,actor,actor_ou,action,target_type,target_id,detail,ip)
		VALUES(?,'system',0,'app_platform.restore_start_ready',
		'app_platform_operation',?,?,'')`,
		time.UnixMilli(now).UTC().Format(time.RFC3339),
		expected.id, string(detail)); err != nil {
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
