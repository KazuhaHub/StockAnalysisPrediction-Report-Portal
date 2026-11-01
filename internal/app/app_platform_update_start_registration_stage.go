package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/journal"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/transport"
)

func (s *Server) preparePlatformUpdateStartRegistration(ctx context.Context, lease platformOperationLease) (platformUpdateStartRegistrationIntent, error) {
	var zero platformUpdateStartRegistrationIntent
	if lease.snapshot.Action != appplatform.ActionUpdate || !lease.snapshot.Managed ||
		lease.snapshot.Stage != appplatform.StageActivate ||
		lease.snapshot.Generation < 2 {
		return zero, fmt.Errorf("update-start registration precondition: %w", errAppPlatformStageEvidence)
	}
	start, err := s.preparePlatformUpdateRegistrationSource(ctx, lease)
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
	runtime, seed, err := s.platformDataRuntime(e, lease.controllerEpoch, dek)
	if err != nil {
		return zero, err
	}
	defer clear(seed)
	facts, err := loadPlatformBoundOperationLease(e, lease)
	if err != nil {
		return zero, err
	}
	source, err := loadPlatformCheckpointSource(e, lease, runtime, facts.now)
	if err != nil {
		return zero, err
	}
	plan, _, err := loadPlatformManagedUpdatePlan(e, lease, source)
	if err != nil {
		return zero, err
	}
	checkpoint, _, err := loadPlatformUpdateStartCheckpoint(e, lease, plan)
	if err != nil {
		return zero, err
	}
	current, err := loadPlatformUpdateStartActivation(
		e, lease, runtime, source, checkpoint, plan)
	if err != nil || current.running == nil ||
		current.resourceID != start.resourceID || current.effectID != start.effectID ||
		!platformUpdateStartCommandsEqual(current.command, start.command) ||
		platformDigest(mustMarshalPlatformUpdateStartReceipt(*current.running)) !=
			platformDigest(mustMarshalPlatformUpdateStartReceipt(*start.running)) ||
		current.authority.Fingerprint != start.authority.Fingerprint ||
		!bytes.Equal(current.authority.PublicKey, start.authority.PublicKey) {
		return zero, fmt.Errorf("update-start registration source: %w", errAppPlatformStageEvidence)
	}
	if _, err := control.VerifyUpdateStart(
		start.raw, start.signature, start.authority, time.Now().UnixMilli()); err != nil {
		return zero, fmt.Errorf(
			"update-start registration start signature: %w",
			errAppPlatformStageEvidence)
	}
	intent, fresh, err := loadPlatformUpdateStartRegistration(
		e, lease, start, runtime, seed, dek, facts.now, true)
	if err != nil {
		return zero, err
	}
	failed := true
	defer func() {
		if failed {
			intent.registration.Close()
		}
	}()
	if fresh {
		detail, _ := json.Marshal(struct {
			EffectID    string `json:"effect_id"`
			ResourceID  string `json:"resource_id"`
			DocumentSHA string `json:"document_sha256"`
			RunningSHA  string `json:"running_receipt_sha256"`
		}{intent.command.EffectID, intent.start.resourceID,
			intent.command.DocumentSHA256, intent.command.RunningReceiptSHA256})
		if _, err := e.exec(`INSERT INTO audit_log(
			at,actor,actor_ou,action,target_type,target_id,detail,ip)
			VALUES(?,'system',0,'app_platform.update_start_registration_intent',
			'app_platform_operation',?,?,'')`,
			time.UnixMilli(facts.now).UTC().Format(time.RFC3339),
			lease.id, string(detail)); err != nil {
			return zero, err
		}
	}
	now, err := finalPlatformOperationLeaseCheck(e, facts)
	if err != nil {
		return zero, err
	}
	current, err = loadPlatformUpdateStartActivation(
		e, lease, runtime, source, checkpoint, plan)
	if err != nil || current.running == nil || start.running == nil ||
		current.resourceID != start.resourceID ||
		current.effectID != start.effectID ||
		!platformUpdateStartCommandsEqual(current.command, start.command) ||
		platformDigest(mustMarshalPlatformUpdateStartReceipt(*current.running)) !=
			platformDigest(mustMarshalPlatformUpdateStartReceipt(*start.running)) ||
		current.authority.Fingerprint != start.authority.Fingerprint ||
		!bytes.Equal(current.authority.PublicKey, start.authority.PublicKey) {
		return zero, fmt.Errorf(
			"update-start registration activation current: %w",
			errAppPlatformStageEvidence)
	}
	if fresh && !freshPlatformUpdateRegistrationSourceAt(current, now) {
		return zero, fmt.Errorf(
			"update-start registration source freshness: %w",
			errAppPlatformStageEvidence)
	}
	after, _, err := loadPlatformUpdateStartRegistration(
		e, lease, start, runtime, seed, dek, now, false)
	if err != nil {
		return zero, err
	}
	defer after.registration.Close()
	if !samePlatformUpdateStartRegistrationIntent(intent, after) {
		return zero, fmt.Errorf(
			"update-start registration after intent: %w",
			errAppPlatformStageEvidence)
	}
	if _, err := control.VerifyUpdateStartRegistration(
		intent.raw, intent.signature, start.authority,
		time.Now().UnixMilli()); err != nil {
		return zero, fmt.Errorf(
			"update-start registration signature: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return zero, err
	}
	failed = false
	return intent, nil
}

type platformUpdateStartRegistrationObservation interface {
	UpdateStartRegistrationReceipt() (journal.UpdateStartRegistrationReceipt, error)
	Controller() control.Authority
	Endpoint() transport.CandidateReceiptEndpoint
	ReceivedAt() time.Time
	CommandSHA256() string
	Action() transport.WritableLifecycleAction
}

func validPlatformUpdateStartRegistrationObservation(observation platformUpdateStartRegistrationObservation, intent platformUpdateStartRegistrationIntent, endpoint platformRuntimeEndpoint) bool {
	if intent.start.running == nil {
		return false
	}
	if !bytes.Equal(intent.command.StartRaw, intent.start.raw) ||
		!bytes.Equal(intent.command.StartSignature, intent.start.signature) {
		return false
	}
	wire, err := json.Marshal(intent.command)
	if err != nil || !bytes.Equal(wire, intent.raw) {
		return false
	}
	age := time.Since(observation.ReceivedAt())
	a, b := observation.Controller(), intent.start.authority
	peer := observation.Endpoint()
	receipt, err := observation.UpdateStartRegistrationReceipt()
	return (observation.Action() == transport.UpdateStartRegistrationDeliver ||
		observation.Action() == transport.UpdateStartRegistrationRead) &&
		err == nil && !observation.ReceivedAt().IsZero() &&
		age >= 0 && age <= 3*time.Second &&
		peer.RootController && peer.RunnerUID == 0 &&
		endpoint.controllerUID > 0 && endpoint.controllerGID > 0 &&
		peer.Path == endpoint.path &&
		peer.ControllerUID == endpoint.controllerUID &&
		peer.ControllerGID == endpoint.controllerGID &&
		a.DeploymentID == b.DeploymentID &&
		a.ControllerEpoch == b.ControllerEpoch &&
		a.ControlRevision == b.ControlRevision &&
		a.Fingerprint == b.Fingerprint &&
		bytes.Equal(a.PublicKey, b.PublicKey) &&
		observation.CommandSHA256() == platformDigest(intent.raw) &&
		platformUpdateStartRegistrationReceiptMatches(
			receipt, intent, time.Now().UnixMilli())
}

func (s *Server) dispatchPlatformUpdateStartRegistration(ctx context.Context, lease platformOperationLease, endpoint platformRuntimeEndpoint) (platformOperationLease, error) {
	return s.observePlatformUpdateStartRegistration(
		ctx, lease, endpoint, transport.UpdateStartRegistrationDeliver)
}

func (s *Server) completePlatformUpdateStartRegistration(ctx context.Context, lease platformOperationLease, endpoint platformRuntimeEndpoint) (platformOperationLease, error) {
	return s.observePlatformUpdateStartRegistration(
		ctx, lease, endpoint, transport.UpdateStartRegistrationRead)
}

func (s *Server) observePlatformUpdateStartRegistration(ctx context.Context, lease platformOperationLease, endpoint platformRuntimeEndpoint, action transport.WritableLifecycleAction) (platformOperationLease, error) {
	intent, err := s.preparePlatformUpdateStartRegistration(ctx, lease)
	if err != nil {
		return platformOperationLease{}, err
	}
	defer intent.registration.Close()
	if err := s.checkPlatformManagedRuntimeRoot(
		ctx, lease, intent.start.runtime, intent.start.authority, endpoint); err != nil {
		return platformOperationLease{}, err
	}
	proof, err := platformRunUpdateStartRegistration(ctx, endpoint, intent, action)
	if err != nil {
		return platformOperationLease{}, err
	}
	return s.commitPlatformUpdateStartRegistration(ctx, lease, intent, endpoint, proof)
}

func (s *Server) commitPlatformUpdateStartRegistration(ctx context.Context, lease platformOperationLease, intent platformUpdateStartRegistrationIntent, endpoint platformRuntimeEndpoint, proof platformUpdateStartRegistrationObservation) (platformOperationLease, error) {
	var zero platformOperationLease
	intent = clonePlatformUpdateStartRegistrationIntent(intent)
	if lease.snapshot.Action != appplatform.ActionUpdate || !lease.snapshot.Managed ||
		lease.snapshot.Stage != appplatform.StageActivate ||
		lease.snapshot.Generation < 2 || lease.rowRevision == math.MaxInt64 ||
		!validPlatformUpdateStartRegistrationObservation(proof, intent, endpoint) {
		return zero, fmt.Errorf(
			"update-start registration commit precondition: %w",
			errAppPlatformStageEvidence)
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
	source, err := loadPlatformCheckpointSource(e, lease, runtime, facts.now)
	if err != nil {
		return zero, err
	}
	plan, _, err := loadPlatformManagedUpdatePlan(e, lease, source)
	if err != nil {
		return zero, err
	}
	checkpoint, _, err := loadPlatformUpdateStartCheckpoint(e, lease, plan)
	if err != nil {
		return zero, err
	}
	currentStart, err := loadPlatformUpdateStartActivation(
		e, lease, runtime, source, checkpoint, plan)
	if err != nil {
		return zero, fmt.Errorf("update-start registration activation: %w", err)
	}
	if currentStart.running == nil || intent.start.running == nil ||
		currentStart.resourceID != intent.start.resourceID ||
		currentStart.effectID != intent.start.effectID ||
		!platformUpdateStartCommandsEqual(
			currentStart.command, intent.start.command) ||
		platformDigest(mustMarshalPlatformUpdateStartReceipt(*currentStart.running)) !=
			platformDigest(mustMarshalPlatformUpdateStartReceipt(*intent.start.running)) {
		return zero, fmt.Errorf("update-start registration activation drift: %w", errAppPlatformStageEvidence)
	}
	current, _, err := loadPlatformUpdateStartRegistration(
		e, lease, intent.start, runtime, seed, dek, facts.now, false)
	if err != nil {
		return zero, fmt.Errorf("update-start registration load: %w", err)
	}
	defer current.registration.Close()
	if !samePlatformUpdateStartRegistrationIntent(intent, current) {
		return zero, fmt.Errorf("update-start registration current intent: %w", errAppPlatformStageEvidence)
	}
	if _, err := control.VerifyUpdateStartRegistration(
		intent.raw, intent.signature, intent.start.authority,
		time.Now().UnixMilli()); err != nil {
		return zero, fmt.Errorf("update-start registration signature: %w", errAppPlatformStageEvidence)
	}
	receipt, err := proof.UpdateStartRegistrationReceipt()
	if err != nil {
		return zero, fmt.Errorf("update-start registration receipt parse: %w", err)
	}
	encoded, err := json.Marshal(receipt)
	if err != nil || len(encoded) > 8192 {
		return zero, fmt.Errorf("update-start registration receipt encode: %w", errAppPlatformStageEvidence)
	}
	result := lease
	expected := current
	if current.applied {
		if current.receipt != receipt {
			return zero, fmt.Errorf("update-start registration applied drift: %w", errAppPlatformStageEvidence)
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
			return zero, fmt.Errorf("update-start registration effect CAS: %w", err)
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
			return zero, fmt.Errorf("update-start registration operation CAS: %w", err)
		}
		detail, _ := json.Marshal(struct {
			EffectID      string `json:"effect_id"`
			ResourceID    string `json:"resource_id"`
			ReceiptSHA256 string `json:"receipt_sha256"`
		}{current.command.EffectID, current.start.resourceID,
			platformDigest(encoded)})
		if _, err := e.exec(`INSERT INTO audit_log(
			at,actor,actor_ou,action,target_type,target_id,detail,ip)
			VALUES(?,'system',0,'app_platform.update_start_registration_observed',
			'app_platform_operation',?,?,'')`,
			time.UnixMilli(now).UTC().Format(time.RFC3339),
			lease.id, string(detail)); err != nil {
			return zero, err
		}
		expected.applied, expected.receipt, expected.updated = true, receipt, now
	}
	now, err := finalPlatformOperationLeaseCheck(e, facts)
	if err != nil {
		return zero, err
	}
	after, _, err := loadPlatformUpdateStartRegistration(
		e, lease, intent.start, runtime, seed, dek, now, false)
	if err != nil {
		return zero, err
	}
	defer after.registration.Close()
	if !samePlatformUpdateStartRegistrationIntent(expected, after) {
		return zero, fmt.Errorf("update-start registration after: %w", errAppPlatformStageEvidence)
	}
	if !validPlatformUpdateStartRegistrationObservation(proof, intent, endpoint) {
		return zero, fmt.Errorf("update-start registration proof: %w", errAppPlatformStageEvidence)
	}
	if err := tx.Commit(); err != nil {
		return zero, err
	}
	return result, nil
}
