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

type platformManagedUpdateQuiesceIntent struct {
	command          control.ManagedRemovalCommand
	raw, signature   []byte
	authority        control.Authority
	runtime          platformRuntimeRegistration
	source           platformCheckpointSource
	nativeID         string
	platform         string
	descriptor       []byte
	effectStage      string
	created, updated int64
	applied          bool
	receipt          journal.ManagedRemovalReceipt
}

func (s *Server) advancePlatformManagedUpdateStage(ctx context.Context, expected platformOperationLease) (platformOperationLease, error) {
	var zero platformOperationLease
	nextByStage := map[appplatform.Stage]appplatform.Stage{
		appplatform.StageQueued:   appplatform.StageValidate,
		appplatform.StageValidate: appplatform.StageDownload,
		appplatform.StageDownload: appplatform.StageStage,
		appplatform.StageStage:    appplatform.StageQuiesce,
	}
	next, ok := nextByStage[expected.snapshot.Stage]
	if !ok || expected.snapshot.Action != appplatform.ActionUpdate ||
		!expected.snapshot.Managed || expected.rowRevision == math.MaxInt64 {
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
	trust, err := loadPlatformPublisherTrust(e)
	if err != nil {
		return zero, err
	}
	if _, err := s.loadPlatformAdmittedManagedUpdate(
		e, expected, trust, facts.now); err != nil {
		return zero, err
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
	result.snapshot, result.rowRevision =
		advanced.Snapshot(), expected.rowRevision+1
	changed, err := e.exec(`UPDATE app_platform_operations
		SET stage=?,state_revision=?,row_revision=row_revision+1,updated_ms=?
		WHERE id=? AND installation_id=? AND controller_epoch=?
		AND stage=? AND state_revision=? AND row_revision=? AND fence=?
		AND lease_owner=? AND lease_until_ms=? AND lease_until_ms>?
		AND completed_ms IS NULL`,
		string(next), int64(result.snapshot.Revision), now,
		expected.id, expected.installationID, expected.controllerEpoch,
		string(expected.snapshot.Stage), int64(expected.snapshot.Revision),
		expected.rowRevision, expected.fence, expected.owner,
		expected.leaseUntilMS, now)
	if err := platformLeaseCAS(changed, err); err != nil {
		return zero, err
	}
	detail, _ := json.Marshal(struct {
		Stage appplatform.Stage `json:"stage"`
	}{next})
	if _, err := e.exec(`INSERT INTO audit_log(
		at,actor,actor_ou,action,target_type,target_id,detail,ip)
		VALUES(?,'system',0,'app_platform.operation_stage',
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

func (s *Server) preparePlatformManagedUpdateQuiesce(ctx context.Context, expected platformOperationLease) (platformManagedUpdateQuiesceIntent, error) {
	return s.preparePlatformManagedUpdateSourceRemoval(
		ctx, expected, appplatform.StageQuiesce)
}

func (s *Server) preparePlatformManagedUpdateMigrate(ctx context.Context, expected platformOperationLease) (platformManagedUpdateQuiesceIntent, error) {
	return s.preparePlatformManagedUpdateSourceRemoval(
		ctx, expected, appplatform.StageMigrate)
}

func (s *Server) preparePlatformManagedUpdateSourceRemoval(ctx context.Context, expected platformOperationLease, stage appplatform.Stage) (platformManagedUpdateQuiesceIntent, error) {
	var zero platformManagedUpdateQuiesceIntent
	if expected.snapshot.Action != appplatform.ActionUpdate ||
		!expected.snapshot.Managed ||
		expected.snapshot.Stage != stage ||
		(stage != appplatform.StageQuiesce &&
			stage != appplatform.StageMigrate) ||
		expected.snapshot.Generation < 2 ||
		expected.rowRevision == math.MaxInt64 {
		return zero, errAppPlatformStageEvidence
	}
	commandStage, commandKind := "stop", "stop"
	if stage == appplatform.StageMigrate {
		commandStage, commandKind = "remove_code", "remove-code"
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
	trust, err := loadPlatformPublisherTrust(e)
	if err != nil {
		return zero, err
	}
	admitted, err := s.loadPlatformAdmittedManagedUpdate(
		e, expected, trust, facts.now)
	if err != nil {
		return zero, err
	}
	source, err := loadPlatformCheckpointSource(
		e, expected, runtime, facts.now)
	if err != nil {
		return zero, err
	}
	if source.appID != admitted.plan.AppID ||
		source.publisherID != admitted.plan.PublisherID ||
		source.releaseSHA256 != admitted.plan.SourceReleaseSHA256 {
		return zero, errAppPlatformStageEvidence
	}
	var effectID, kind, digest, state, receipt string
	var admittedFence, observedFence, created, updated int64
	effectErr := e.queryRow(`SELECT effect_id,kind,payload_sha256,
		admitted_fence,observed_fence,state,receipt_json,created_ms,updated_ms
		FROM app_platform_operation_effects
		WHERE operation_id=? AND stage=? AND effect_number=1`,
		expected.id, string(stage)).
		Scan(&effectID, &kind, &digest, &admittedFence, &observedFence,
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
	} else if kind != commandKind || admittedFence != expected.fence ||
		(state != "prepared" && state != "applied") ||
		updated < created || updated > facts.now {
		return zero, errAppPlatformStageEvidence
	}
	issue := time.Now().UnixMilli()
	if issue < facts.now-1000 || issue > facts.now+1000 ||
		issue > math.MaxInt64-25000 {
		return zero, errAppPlatformLeaseConflict
	}
	command := control.ManagedRemovalCommand{
		Format:          control.ManagedRemovalFormat,
		DeploymentID:    runtime.deploymentID,
		ControllerEpoch: expected.controllerEpoch,
		ControlRevision: strconv.FormatInt(runtime.controlRevision, 10),
		InstallationID:  expected.installationID, OperationID: expected.id,
		TargetGeneration: strconv.FormatUint(
			expected.snapshot.Generation, 10),
		Stage: commandStage, EffectNumber: "1", EffectID: effectID,
		Kind: commandKind, InstallationFence: strconv.FormatInt(expected.fence, 10),
		IssuedMS:  strconv.FormatInt(issue, 10),
		ExpiresMS: strconv.FormatInt(issue+25000, 10),
		Payload: control.ManagedRemovalPayload{
			ResourceID:          source.resourceID,
			DataNamespaceID:     source.namespaceID,
			ReleaseSHA256:       source.releaseSHA256,
			Platform:            source.platform,
			SourceOperationID:   source.operationID,
			SourceEffectID:      source.startEffectID,
			SourceReceiptSHA256: source.sourceReceiptSHA256,
		},
	}
	command.PayloadSHA256, err = control.ManagedRemovalLogicalDigest(command)
	if err != nil {
		return zero, err
	}
	authority := control.Authority{
		DeploymentID:    runtime.deploymentID,
		ControllerEpoch: runtime.controllerEpoch,
		ControlRevision: runtime.controlRevision,
		PublicKey:       bytes.Clone(runtime.publicKey),
		Fingerprint:     runtime.fingerprint,
	}
	key := ed25519.NewKeyFromSeed(seed)
	raw, signature, err := control.SignManagedRemoval(
		command, key, authority, issue)
	clear(key)
	if err != nil {
		return zero, err
	}
	result := platformManagedUpdateQuiesceIntent{
		command: command, raw: raw, signature: signature,
		authority: authority, runtime: runtime, source: source,
		nativeID: source.nativeID, platform: source.platform,
		descriptor:  bytes.Clone(source.descriptor),
		effectStage: string(stage),
		created:     created, updated: updated,
	}
	if fresh {
		if _, err := e.exec(`INSERT INTO app_platform_operation_effects(
			operation_id,stage,effect_number,effect_id,kind,payload_sha256,
			runtime_id,controller_epoch,admitted_fence,observed_fence,state,
			receipt_json,created_ms,updated_ms)
			VALUES(?,?,1,?,?,?,?,?,?,0,'prepared','{}',?,?)`,
			expected.id, string(stage), effectID, command.Kind,
			command.PayloadSHA256, runtime.id, expected.controllerEpoch, expected.fence, facts.now,
			facts.now); err != nil {
			return zero, err
		}
	} else {
		if digest != command.PayloadSHA256 {
			return zero, errAppPlatformStageEvidence
		}
		switch state {
		case "prepared":
			if observedFence != 0 || receipt != "{}" ||
				updated != created {
				return zero, errAppPlatformStageEvidence
			}
		case "applied":
			parsed, err := journal.ParseManagedRemovalReceipt(
				[]byte(receipt))
			if err != nil ||
				!platformManagedUpdateQuiesceReceiptMatches(
					parsed, result) ||
				observedFence != expected.fence {
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

func platformManagedUpdateQuiesceReceiptMatches(receipt journal.ManagedRemovalReceipt, intent platformManagedUpdateQuiesceIntent) bool {
	command := intent.command
	state := "stopped"
	if command.Stage == "remove_code" {
		state = "absent"
	}
	return receipt.DeploymentID == command.DeploymentID &&
		receipt.ControllerEpoch == command.ControllerEpoch &&
		receipt.InstallationID == command.InstallationID &&
		receipt.OperationID == command.OperationID &&
		receipt.SourceOperationID == command.Payload.SourceOperationID &&
		receipt.SourceEffectID == command.Payload.SourceEffectID &&
		receipt.TargetGeneration == command.TargetGeneration &&
		receipt.Stage == command.Stage && receipt.State == state &&
		receipt.EffectID == command.EffectID &&
		receipt.PayloadSHA256 == command.PayloadSHA256 &&
		receipt.SourceReceiptSHA256 ==
			command.Payload.SourceReceiptSHA256 &&
		receipt.ResourceID == command.Payload.ResourceID &&
		receipt.DataNamespaceID == command.Payload.DataNamespaceID &&
		receipt.ReleaseSHA256 == command.Payload.ReleaseSHA256 &&
		receipt.NativeID == intent.source.nativeID
}

type platformManagedUpdateQuiesceObservation interface {
	ManagedRemovalReceipt() (journal.ManagedRemovalReceipt, error)
	Controller() control.Authority
	Endpoint() transport.CandidateReceiptEndpoint
	ReceivedAt() time.Time
	CommandSHA256() string
}

func validPlatformManagedUpdateQuiesceObservation(observation platformManagedUpdateQuiesceObservation, intent platformManagedUpdateQuiesceIntent, endpoint platformRuntimeEndpoint) (journal.ManagedRemovalReceipt, bool) {
	receipt, err := observation.ManagedRemovalReceipt()
	if err != nil || !platformManagedUpdateQuiesceReceiptMatches(
		receipt, intent) ||
		observation.CommandSHA256() != platformDigest(intent.raw) ||
		observation.ReceivedAt().IsZero() {
		return journal.ManagedRemovalReceipt{}, false
	}
	actual := observation.Controller()
	if actual.DeploymentID != intent.authority.DeploymentID ||
		actual.ControllerEpoch != intent.authority.ControllerEpoch ||
		actual.ControlRevision != intent.authority.ControlRevision ||
		actual.Fingerprint != intent.authority.Fingerprint ||
		!bytes.Equal(actual.PublicKey, intent.authority.PublicKey) {
		return journal.ManagedRemovalReceipt{}, false
	}
	peer := observation.Endpoint()
	age := time.Since(observation.ReceivedAt())
	observed, observedErr := strconv.ParseInt(receipt.ObservedMS, 10, 64)
	settled, settledErr := strconv.ParseInt(receipt.SettledMS, 10, 64)
	issued, issuedErr := strconv.ParseInt(intent.command.IssuedMS, 10, 64)
	if !peer.RootController || peer.RunnerUID != 0 ||
		peer.Path != endpoint.path ||
		peer.ControllerUID != endpoint.controllerUID ||
		peer.ControllerGID != endpoint.controllerGID ||
		age < 0 || age > 3*time.Second ||
		observedErr != nil || settledErr != nil || issuedErr != nil ||
		observed < issued || observed > settled ||
		settled > observation.ReceivedAt().UnixMilli() {
		return journal.ManagedRemovalReceipt{}, false
	}
	return receipt, true
}

func (s *Server) executePlatformManagedUpdateQuiesce(ctx context.Context, expected platformOperationLease, endpoint platformRuntimeEndpoint) (platformOperationLease, error) {
	return s.executePlatformManagedUpdateSourceRemoval(
		ctx, expected, endpoint, appplatform.StageQuiesce,
		appplatform.StageStop)
}

func (s *Server) executePlatformManagedUpdateMigrate(ctx context.Context, expected platformOperationLease, endpoint platformRuntimeEndpoint) (platformOperationLease, error) {
	return s.executePlatformManagedUpdateSourceRemoval(
		ctx, expected, endpoint, appplatform.StageMigrate,
		appplatform.StageRemoveCode)
}

func (s *Server) executePlatformManagedUpdateSourceRemoval(ctx context.Context, expected platformOperationLease, endpoint platformRuntimeEndpoint, stage, endpointStage appplatform.Stage) (platformOperationLease, error) {
	intent, err := s.preparePlatformManagedUpdateSourceRemoval(
		ctx, expected, stage)
	if err != nil {
		return platformOperationLease{}, fmt.Errorf(
			"managed update %s prepare: %w", stage, err)
	}
	if err := s.checkPlatformManagedOperationRoot(
		ctx, expected.id, intent.runtime, intent.authority,
		endpoint); err != nil {
		return platformOperationLease{}, fmt.Errorf(
			"managed update %s root: %w", stage, err)
	}
	observation, err := platformRunManagedRemoval(
		ctx, endpoint, endpointStage, intent.raw,
		intent.signature, intent.authority, intent.descriptor)
	if err != nil {
		return platformOperationLease{}, fmt.Errorf(
			"managed update %s runner: %w", stage, err)
	}
	receipt, ok := validPlatformManagedUpdateQuiesceObservation(
		observation, intent, endpoint)
	if !ok {
		return platformOperationLease{}, fmt.Errorf(
			"managed update %s observation: %w", stage,
			errAppPlatformStageEvidence)
	}
	result, err := s.commitPlatformManagedUpdateQuiesce(
		ctx, expected, intent, receipt)
	if err != nil {
		return platformOperationLease{}, fmt.Errorf(
			"managed update %s commit: %w", stage, err)
	}
	return result, nil
}

func (s *Server) commitPlatformManagedUpdateQuiesce(ctx context.Context, expected platformOperationLease, intent platformManagedUpdateQuiesceIntent, receipt journal.ManagedRemovalReceipt) (platformOperationLease, error) {
	var zero platformOperationLease
	if expected.snapshot.Action != appplatform.ActionUpdate ||
		!expected.snapshot.Managed ||
		(expected.snapshot.Stage != appplatform.StageQuiesce &&
			expected.snapshot.Stage != appplatform.StageMigrate) ||
		expected.rowRevision == math.MaxInt64 ||
		!platformManagedUpdateQuiesceReceiptMatches(receipt, intent) {
		return zero, errAppPlatformStageEvidence
	}
	encoded, err := json.Marshal(receipt)
	if err != nil || len(encoded) > 16384 {
		return zero, errAppPlatformStageEvidence
	}
	current, err := s.preparePlatformManagedUpdateSourceRemoval(
		ctx, expected, expected.snapshot.Stage)
	if err != nil || !samePlatformManagedUpdateQuiesceIntent(
		intent, current) {
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
	trust, err := loadPlatformPublisherTrust(e)
	if err != nil {
		return zero, err
	}
	admitted, err := s.loadPlatformAdmittedManagedUpdate(
		e, expected, trust, facts.now)
	if err != nil {
		return zero, err
	}
	now, err := finalPlatformOperationLeaseCheck(e, facts)
	if err != nil {
		return zero, err
	}
	changed, err := e.exec(`UPDATE app_platform_operation_effects
		SET state='applied',observed_fence=?,receipt_json=?,updated_ms=?
		WHERE operation_id=? AND stage=? AND effect_number=1
		AND effect_id=? AND kind=? AND payload_sha256=?
		AND state='prepared' AND observed_fence=0 AND receipt_json='{}'
		AND admitted_fence=?`,
		expected.fence, string(encoded), now, expected.id,
		string(expected.snapshot.Stage), intent.command.EffectID,
		intent.command.Kind, intent.command.PayloadSHA256,
		expected.fence)
	if err := platformLeaseCAS(changed, err); err != nil {
		return zero, err
	}
	if expected.snapshot.Stage == appplatform.StageMigrate && admitted.plan.Migration != nil {
		// The source is gone; the migration is still to run, so the operation stays at this stage.
		detail, _ := json.Marshal(struct {
			ResourceID string `json:"resource_id"`
			NativeID   string `json:"native_id"`
			Release    string `json:"release_sha256"`
		}{intent.command.Payload.ResourceID, receipt.NativeID, intent.command.Payload.ReleaseSHA256})
		if _, err := e.exec(`INSERT INTO audit_log(
			at,actor,actor_ou,action,target_type,target_id,detail,ip)
			VALUES(?,'system',0,'app_platform.managed_update_source_removed','app_platform_operation',?,?,'')`,
			time.UnixMilli(now).UTC().Format(time.RFC3339), expected.id, string(detail)); err != nil {
			return zero, err
		}
		if _, err := finalPlatformOperationLeaseCheck(e, facts); err != nil {
			return zero, err
		}
		if err := tx.Commit(); err != nil {
			return zero, err
		}
		return expected, nil
	}
	model, err := appplatform.OperationFromSnapshot(expected.snapshot)
	if err != nil {
		return zero, errAppPlatformStageEvidence
	}
	nextStage := appplatform.StageSnapshot
	if expected.snapshot.Stage == appplatform.StageMigrate {
		nextStage = appplatform.StageStart
	}
	next, err := model.Advance(
		expected.snapshot.Revision, nextStage)
	if err != nil {
		return zero, errAppPlatformStageEvidence
	}
	result := expected
	result.snapshot, result.rowRevision =
		next.Snapshot(), expected.rowRevision+1
	changed, err = e.exec(`UPDATE app_platform_operations
		SET stage=?,state_revision=?,row_revision=row_revision+1,
		updated_ms=? WHERE id=? AND installation_id=?
		AND stage=? AND state_revision=? AND row_revision=?
		AND controller_epoch=? AND fence=? AND lease_owner=?
		AND lease_until_ms=? AND lease_until_ms>?
		AND completed_ms IS NULL`,
		string(nextStage), int64(result.snapshot.Revision), now, expected.id,
		expected.installationID, string(expected.snapshot.Stage),
		int64(expected.snapshot.Revision),
		expected.rowRevision, expected.controllerEpoch, expected.fence,
		expected.owner, expected.leaseUntilMS, now)
	if err := platformLeaseCAS(changed, err); err != nil {
		return zero, err
	}
	detail, _ := json.Marshal(struct {
		ResourceID string `json:"resource_id"`
		NativeID   string `json:"native_id"`
		Release    string `json:"release_sha256"`
	}{intent.command.Payload.ResourceID, receipt.NativeID,
		intent.command.Payload.ReleaseSHA256})
	auditAction := "app_platform.managed_update_quiesced"
	if expected.snapshot.Stage == appplatform.StageMigrate {
		auditAction = "app_platform.managed_update_source_removed"
	}
	if _, err := e.exec(`INSERT INTO audit_log(
		at,actor,actor_ou,action,target_type,target_id,detail,ip)
		VALUES(?,'system',0,?,'app_platform_operation',?,?,'')`,
		time.UnixMilli(now).UTC().Format(time.RFC3339),
		auditAction,
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

func samePlatformManagedUpdateQuiesceIntent(a, b platformManagedUpdateQuiesceIntent) bool {
	first, second := a.command, b.command
	first.IssuedMS, first.ExpiresMS = "", ""
	second.IssuedMS, second.ExpiresMS = "", ""
	return first == second &&
		a.source.operationID == b.source.operationID &&
		a.source.startEffectID == b.source.startEffectID &&
		a.source.sourceReceiptSHA256 == b.source.sourceReceiptSHA256 &&
		a.source.resourceID == b.source.resourceID &&
		a.source.namespaceID == b.source.namespaceID &&
		a.source.releaseSHA256 == b.source.releaseSHA256 &&
		a.source.nativeID == b.source.nativeID &&
		a.authority.Fingerprint == b.authority.Fingerprint &&
		bytes.Equal(a.authority.PublicKey, b.authority.PublicKey)
}

func (s *Server) runPlatformManagedUpdate(ctx context.Context, expected platformOperationLease, endpoint platformRuntimeEndpoint) (platformOperationLease, error) {
	var zero platformOperationLease
	if expected.snapshot.Action != appplatform.ActionUpdate ||
		!expected.snapshot.Managed || endpoint.path == "" {
		return zero, errAppPlatformStageEvidence
	}
	switch expected.snapshot.Stage {
	case appplatform.StageQueued, appplatform.StageValidate,
		appplatform.StageDownload, appplatform.StageStage:
		return s.advancePlatformManagedUpdateStage(ctx, expected)
	case appplatform.StageQuiesce:
		if lease, handled, err := s.runPlatformRetryEvidenceStage(ctx, expected); handled || err != nil {
			return lease, err
		}
		return s.executePlatformManagedUpdateQuiesce(
			ctx, expected, endpoint)
	case appplatform.StageSnapshot:
		return s.dispatchPlatformCheckpoint(ctx, expected, endpoint)
	case appplatform.StageMigrate:
		if lease, handled, err := s.runPlatformRetryEvidenceStage(ctx, expected); handled || err != nil {
			return lease, err
		}
		return s.runPlatformMigrateStage(ctx, expected, endpoint)
	case appplatform.StageStart:
		return s.executePlatformUpdateStart(ctx, expected, endpoint)
	case appplatform.StageProbe:
		return s.probePlatformUpdateStart(ctx, expected, endpoint)
	case appplatform.StageActivate:
		registered, err := s.dispatchPlatformUpdateStartRegistration(
			ctx, expected, endpoint)
		if err != nil {
			return expected, err
		}
		confirmed, err := s.completePlatformUpdateStartRegistration(
			ctx, registered, endpoint)
		if err != nil {
			return registered, err
		}
		if _, err := s.activatePlatformUpdateStart(
			ctx, confirmed, endpoint); err != nil {
			return confirmed, err
		}
		return zero, nil
	default:
		return zero, errAppPlatformStageEvidence
	}
}
