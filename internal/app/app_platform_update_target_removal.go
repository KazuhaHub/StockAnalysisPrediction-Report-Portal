package app

import (
	"bytes"
	"context"
	"crypto/ed25519"
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
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/transport"
)

type platformUpdateTargetRemovalIntent struct {
	command          control.UpdateStartRemovalCommand
	raw, signature   []byte
	authority        control.Authority
	start            platformUpdateStartIntent
	runtime          platformRuntimeRegistration
	sourceOperation  string
	nativeID         string
	effectStage      string
	effectNumber     int
	created, updated int64
	applied          bool
	receipt          journal.ManagedRemovalReceipt
}

func loadPlatformUpdateTargetRemovalPlan(e migExec, lease platformOperationLease, runtime platformRuntimeRegistration, checkpoint journal.CheckpointReceipt, now int64) (platformUpdateStartIntent, platformManagedUpdatePlan, platformCheckpointSource, []byte, error) {
	var zero platformUpdateStartIntent
	var zeroPlan platformManagedUpdatePlan
	var zeroSource platformCheckpointSource
	if lease.snapshot.Action != appplatform.ActionRestore ||
		!lease.snapshot.Managed ||
		!((lease.snapshot.Stage == appplatform.StageQuiesce &&
			checkpoint.OperationID != "") ||
			lease.snapshot.Stage == appplatform.StageStart) ||
		lease.snapshot.Generation < 2 {
		return zero, zeroPlan, zeroSource, nil, errAppPlatformStageEvidence
	}
	var installation, stage, epoch string
	var sourceGeneration, targetGeneration, sourceFence int64
	if err := e.queryRow(`SELECT installation_id,source_generation,
		target_generation,stage,fence,controller_epoch
		FROM app_platform_operations
		WHERE id=? AND action='update' AND managed=1
		AND stage='recovery_required' AND completed_ms IS NOT NULL`,
		checkpoint.OperationID).
		Scan(&installation, &sourceGeneration, &targetGeneration,
			&stage, &sourceFence, &epoch); err != nil ||
		installation != lease.installationID ||
		sourceGeneration != int64(lease.snapshot.Generation)-1 ||
		targetGeneration != int64(lease.snapshot.Generation) ||
		stage != "recovery_required" ||
		sourceFence < 1 || epoch != lease.controllerEpoch {
		return zero, zeroPlan, zeroSource, nil, fmt.Errorf(
			"restore target failed update operation: %w",
			errAppPlatformStageEvidence)
	}
	sourceLease := platformOperationLease{
		id: checkpoint.OperationID, installationID: installation,
		controllerEpoch: epoch, fence: sourceFence,
		snapshot: appplatform.OperationSnapshot{
			Action: appplatform.ActionUpdate, Managed: true,
			Generation: uint64(targetGeneration),
			Revision:   1, Stage: appplatform.StageRecoveryRequired,
		},
	}
	source, err := loadPlatformCheckpointSource(
		e, lease, runtime, now)
	if err != nil {
		return zero, zeroPlan, zeroSource, nil, fmt.Errorf(
			"restore target checkpoint source: %w", err)
	}
	plan, descriptor, err := loadPlatformManagedUpdatePlan(
		e, sourceLease, source)
	if err != nil {
		return zero, zeroPlan, zeroSource, nil, fmt.Errorf(
			"restore target failed update plan: %w", err)
	}
	if _, _, err := loadPlatformUpdateStartCheckpoint(
		e, sourceLease, plan); err != nil {
		return zero, zeroPlan, zeroSource, nil, fmt.Errorf(
			"restore target failed update checkpoint: %w", err)
	}
	var effectID, kind, digest, state, receipt string
	var observed int64
	if err := e.queryRow(`SELECT effect_id,kind,payload_sha256,state,
		observed_fence,receipt_json FROM app_platform_operation_effects
		WHERE operation_id=? AND stage='start' AND effect_number=1`,
		sourceLease.id).
		Scan(&effectID, &kind, &digest, &state, &observed, &receipt); err != nil ||
		kind != "update-start" || state != "applied" ||
		observed != sourceFence {
		return zero, zeroPlan, zeroSource, nil, fmt.Errorf(
			"restore target failed update start effect: %w",
			errAppPlatformStageEvidence)
	}
	activation, err := platformResourceForUpdateStartEffect(
		e, sourceLease, effectID)
	if err != nil {
		return zero, zeroPlan, zeroSource, nil, fmt.Errorf(
			"restore target failed update activation: %w", err)
	}
	checkpointReceipt, _, err := loadPlatformUpdateStartCheckpoint(
		e, sourceLease, plan)
	if err != nil {
		return zero, zeroPlan, zeroSource, nil, fmt.Errorf(
			"restore target failed update checkpoint reload: %w", err)
	}
	command, err := platformUpdateStartCommand(
		sourceLease, runtime, source, checkpointReceipt,
		plan, activation, effectID)
	if err != nil || command.PayloadSHA256 != digest {
		return zero, zeroPlan, zeroSource, nil, fmt.Errorf(
			"restore target failed update start command: %w",
			errAppPlatformStageEvidence)
	}
	running, err := journal.ParseUpdateStartRunningReceipt([]byte(receipt))
	if err != nil || !platformUpdateStartRunningMatches(
		running, command, activation, effectID, nil) {
		return zero, zeroPlan, zeroSource, nil, fmt.Errorf(
			"restore target failed update running receipt: %w",
			errAppPlatformStageEvidence)
	}
	return platformUpdateStartIntent{
		resourceID: activation, effectID: effectID, runtime: runtime,
		authority: control.Authority{
			DeploymentID:    runtime.deploymentID,
			ControllerEpoch: runtime.controllerEpoch,
			ControlRevision: runtime.controlRevision,
			PublicKey:       bytes.Clone(runtime.publicKey),
			Fingerprint:     runtime.fingerprint,
		},
		command: command, checkpoint: checkpointReceipt,
		checkpointRaw: []byte(mustMarshalPlatformCheckpointReceipt(checkpointReceipt)),
		source:        source, plan: plan, running: &running,
		targetDescriptor: bytes.Clone(descriptor),
	}, plan, source, descriptor, nil
}

func platformResourceForUpdateStartEffect(e migExec, lease platformOperationLease, effectID string) (string, error) {
	var activation string
	if err := e.queryRow(`SELECT id FROM app_platform_activations
		WHERE installation_id=? AND generation=? AND state='candidate'`,
		lease.installationID, lease.snapshot.Generation).
		Scan(&activation); err != nil {
		return "", errAppPlatformStageEvidence
	}
	return activation, nil
}

func platformUpdateTargetRemovalCommand(lease platformOperationLease, start platformUpdateStartIntent, effectID, stage string) (control.UpdateStartRemovalCommand, error) {
	runningRaw, _ := json.Marshal(*start.running)
	command := control.UpdateStartRemovalCommand{
		Format: control.UpdateStartRemovalFormat, EffectID: effectID,
		OperationID: lease.id, Stage: stage,
		InstallationFence:    strconv.FormatInt(lease.fence, 10),
		StartRaw:             bytes.Clone(start.raw),
		StartSignature:       bytes.Clone(start.signature),
		RunningReceiptSHA256: platformDigest(runningRaw),
	}
	digest, err := control.UpdateStartRemovalLogicalDigest(command)
	if err != nil {
		return control.UpdateStartRemovalCommand{}, errAppPlatformStageEvidence
	}
	command.PayloadSHA256 = digest
	return command, nil
}

func (s *Server) preparePlatformUpdateTargetRemoval(ctx context.Context, lease platformOperationLease, stage string, effectNumber int) (platformUpdateTargetRemovalIntent, error) {
	var zero platformUpdateTargetRemovalIntent
	if lease.snapshot.Action != appplatform.ActionRestore ||
		!lease.snapshot.Managed || lease.snapshot.Stage != appplatform.StageQuiesce ||
		(stage != "stop" && stage != "remove_code") ||
		effectNumber < 1 || effectNumber > 2 {
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
	recoverySetID, _, checkpoint, _, err := loadPlatformRestoreCheckpoint(e, lease)
	if err != nil || recoverySetID == "" {
		return zero, fmt.Errorf("target removal restore checkpoint: %w", err)
	}
	start, _, source, _, err :=
		loadPlatformUpdateTargetRemovalPlan(
			e, lease, runtime, checkpoint, facts.now)
	if err != nil || start.running == nil ||
		start.plan.TargetPolicyRevision < 1 {
		return zero, fmt.Errorf("target removal source: %w", err)
	}
	// Removal targets the failed update release, not the checkpoint source
	// release. The recovery plan must therefore point at the failed operation.
	if checkpoint.OperationID != start.command.OperationID ||
		checkpoint.ReleaseSHA256 != source.releaseSHA256 {
		return zero, fmt.Errorf("target removal checkpoint binding: %w", errAppPlatformStageEvidence)
	}
	issue := time.Now().UnixMilli()
	if issue < facts.now-1000 || issue > facts.now+1000 ||
		issue > math.MaxInt64-25000 {
		return zero, errAppPlatformLeaseConflict
	}
	start.command.IssuedMS = strconv.FormatInt(issue, 10)
	start.command.ExpiresMS = strconv.FormatInt(issue+25000, 10)
	key := ed25519.NewKeyFromSeed(seed)
	start.raw, start.signature, err = control.SignUpdateStart(
		start.command, key, start.authority, issue)
	clear(key)
	if err != nil {
		return zero, err
	}
	var effectID, kind, digest, state, receipt string
	var admitted, observed, created, updated int64
	effectErr := e.queryRow(`SELECT effect_id,kind,payload_sha256,
		admitted_fence,observed_fence,state,receipt_json,created_ms,updated_ms
		FROM app_platform_operation_effects
		WHERE operation_id=? AND stage='quiesce' AND effect_number=?`,
		lease.id, effectNumber).
		Scan(&effectID, &kind, &digest, &admitted, &observed,
			&state, &receipt, &created, &updated)
	fresh := errors.Is(effectErr, sql.ErrNoRows)
	if effectErr != nil && !fresh {
		return zero, effectErr
	}
	if fresh {
		effectID, err = platformNewUUID()
		if err != nil {
			return zero, err
		}
		created, updated = facts.now, facts.now
	} else if kind != stage || admitted != lease.fence ||
		(state != "prepared" && state != "applied") ||
		updated < created || updated > facts.now {
		return zero, errAppPlatformStageEvidence
	}
	command, err := platformUpdateTargetRemovalCommand(
		lease, start, effectID, stage)
	if err != nil {
		return zero, err
	}
	authority := start.authority
	key = ed25519.NewKeyFromSeed(seed)
	raw, signature, err := control.SignUpdateStartRemoval(
		command, key, authority, issue)
	clear(key)
	if err != nil {
		return zero, err
	}
	result := platformUpdateTargetRemovalIntent{
		command: command, raw: raw, signature: signature,
		authority: authority, start: start, runtime: runtime,
		sourceOperation: checkpoint.OperationID,
		nativeID:        start.running.NativeID,
		effectStage:     "quiesce",
		effectNumber:    effectNumber,
		created:         created, updated: updated,
	}
	if fresh {
		if _, err := e.exec(`INSERT INTO app_platform_operation_effects(
			operation_id,stage,effect_number,effect_id,kind,payload_sha256,
			runtime_id,controller_epoch,admitted_fence,observed_fence,state,
			receipt_json,created_ms,updated_ms)
			VALUES(?,'quiesce',?,?,?,?,?,?,?,0,'prepared','{}',?,?)`,
			lease.id, effectNumber, effectID, stage,
			command.PayloadSHA256, runtime.id, lease.controllerEpoch,
			lease.fence, facts.now, facts.now); err != nil {
			return zero, err
		}
	} else {
		if digest != command.PayloadSHA256 {
			return zero, errAppPlatformStageEvidence
		}
		switch state {
		case "prepared":
			if observed != 0 || receipt != "{}" || updated != created {
				return zero, errAppPlatformStageEvidence
			}
		case "applied":
			parsed, err := journal.ParseManagedRemovalReceipt([]byte(receipt))
			if err != nil ||
				!updateStartRemovalReceiptMatchesIntent(parsed, result) ||
				observed != lease.fence {
				return zero, errAppPlatformStageEvidence
			}
			result.applied, result.receipt = true, parsed
		}
	}
	if _, err := finalPlatformOperationLeaseCheck(e, facts); err != nil {
		return zero, err
	}
	if err := tx.Commit(); err != nil {
		return zero, err
	}
	return result, nil
}

func updateStartRemovalReceiptMatchesIntent(receipt journal.ManagedRemovalReceipt, intent platformUpdateTargetRemovalIntent) bool {
	return receipt.OperationID == intent.command.OperationID &&
		receipt.SourceOperationID == intent.start.command.OperationID &&
		receipt.SourceEffectID == intent.start.effectID &&
		receipt.SourceReceiptSHA256 == intent.command.RunningReceiptSHA256 &&
		receipt.Stage == intent.command.Stage &&
		receipt.EffectID == intent.command.EffectID &&
		receipt.PayloadSHA256 == intent.command.PayloadSHA256 &&
		receipt.ResourceID == intent.start.resourceID &&
		receipt.DataNamespaceID == intent.start.command.Payload.NamespaceID &&
		receipt.ReleaseSHA256 == intent.start.command.Payload.TargetReleaseSHA256 &&
		receipt.NativeID == intent.nativeID
}

type platformUpdateTargetRemovalObservation interface {
	ManagedRemovalReceipt() (journal.ManagedRemovalReceipt, error)
	Controller() control.Authority
	Endpoint() transport.CandidateReceiptEndpoint
	ReceivedAt() time.Time
	CommandSHA256() string
}

func validatePlatformUpdateTargetRemovalObservation(observation platformUpdateTargetRemovalObservation, intent platformUpdateTargetRemovalIntent, endpoint platformRuntimeEndpoint) error {
	receipt, err := observation.ManagedRemovalReceipt()
	if err != nil || !updateStartRemovalReceiptMatchesIntent(receipt, intent) {
		return fmt.Errorf("receipt binding: %w", errAppPlatformStageEvidence)
	}
	if observation.CommandSHA256() != platformDigest(intent.raw) {
		return fmt.Errorf("command digest: %w", errAppPlatformStageEvidence)
	}
	if observation.ReceivedAt().IsZero() {
		return fmt.Errorf("receive time: %w", errAppPlatformStageEvidence)
	}
	controller := observation.Controller()
	want := intent.authority
	peer := observation.Endpoint()
	observed, observedErr := strconv.ParseInt(receipt.ObservedMS, 10, 64)
	settled, settledErr := strconv.ParseInt(receipt.SettledMS, 10, 64)
	issued, issuedErr := strconv.ParseInt(
		intent.start.command.IssuedMS, 10, 64)
	age := time.Since(observation.ReceivedAt())
	if controller.DeploymentID != want.DeploymentID ||
		controller.ControllerEpoch != want.ControllerEpoch ||
		controller.ControlRevision != want.ControlRevision ||
		controller.Fingerprint != want.Fingerprint ||
		!bytes.Equal(controller.PublicKey, want.PublicKey) {
		return fmt.Errorf("controller identity: %w", errAppPlatformStageEvidence)
	}
	if !peer.RootController || peer.RunnerUID != 0 ||
		peer.Path != endpoint.path ||
		peer.ControllerUID != endpoint.controllerUID ||
		peer.ControllerGID != endpoint.controllerGID {
		return fmt.Errorf("endpoint identity: %w", errAppPlatformStageEvidence)
	}
	if age < 0 || age > 3*time.Second {
		return fmt.Errorf("response age=%s: %w", age, errAppPlatformStageEvidence)
	}
	if observedErr != nil || settledErr != nil || issuedErr != nil ||
		observed < issued || observed > settled ||
		settled > observation.ReceivedAt().UnixMilli() {
		return fmt.Errorf(
			"observation times issued=%d observed=%d settled=%d received=%d: %w",
			issued, observed, settled, observation.ReceivedAt().UnixMilli(),
			errAppPlatformStageEvidence)
	}
	return nil
}
