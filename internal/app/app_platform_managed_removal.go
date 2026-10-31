package app

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
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

func (s *Server) advancePlatformManagedRemovalStage(ctx context.Context, expected platformOperationLease, next appplatform.Stage) (platformOperationLease, error) {
	var zero platformOperationLease
	if expected.snapshot.Action != appplatform.ActionUninstall || !expected.snapshot.Managed || expected.snapshot.Stage != appplatform.StageRevoke || next != appplatform.StageStop {
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
	if expected.snapshot.Action != appplatform.ActionUninstall || !expected.snapshot.Managed ||
		(expected.snapshot.Stage != appplatform.StageStop && expected.snapshot.Stage != appplatform.StageRemoveCode) {
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
	if desired != "removed" || phase != "pending" || generation != int64(expected.snapshot.Generation) || revision < 1 || controlFence != expected.fence {
		return zero, errAppPlatformStageEvidence
	}
	var effectCount int
	if err := e.queryRow(`SELECT COUNT(*) FROM app_platform_operation_effects WHERE operation_id=? AND stage=?`, expected.id, string(expected.snapshot.Stage)).Scan(&effectCount); err != nil || effectCount != 0 {
		return zero, errAppPlatformStageEvidence
	}
	var sourceOperation, sourcePlanRaw, sourcePlanSHA, sourceRelease, descriptorRaw string
	var sourceGeneration, sourceFence, sourceCompleted int64
	if err := e.queryRow(`SELECT o.id,p.plan_json,p.plan_sha256,p.target_release_sha256,r.descriptor,o.target_generation,o.fence,o.completed_ms
        FROM app_platform_operations o JOIN app_platform_review_plans p ON p.id=o.review_plan_id JOIN app_platform_releases r ON r.sha256=p.target_release_sha256
        WHERE o.installation_id=? AND o.action='install' AND o.managed=1 AND o.stage='complete' AND o.completed_ms IS NOT NULL
          AND o.lease_owner IS NULL AND o.lease_until_ms IS NULL AND o.error_code IS NULL
        ORDER BY o.target_generation DESC,o.completed_ms DESC LIMIT 1`, expected.installationID).Scan(&sourceOperation, &sourcePlanRaw, &sourcePlanSHA, &sourceRelease, &descriptorRaw, &sourceGeneration, &sourceFence, &sourceCompleted); err != nil {
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
	var resourceID, namespaceID, nativeID, platform string
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
			SourceOperationID: sourceOperation, SourceEffectID: start.id, SourceReceiptSHA256: platformDigest([]byte(start.receipt))},
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
		return zero, err
	}
	if err := tx.Commit(); err != nil {
		return zero, err
	}
	return platformManagedRemovalIntent{command: command, raw: raw, signature: signature, authority: authority, descriptor: descriptor, runtime: runtime, sourceOperationID: sourceOperation, nativeID: nativeID}, nil
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
	next := appplatform.StageRemoveCode
	if expected.snapshot.Stage == appplatform.StageRemoveCode {
		next = appplatform.StageComplete
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
		changed, err = e.exec(`UPDATE app_platform_installations SET phase='removed',revision=revision+1,updated_ms=? WHERE id=? AND desired_state='removed' AND phase='pending' AND generation=? AND revision=? AND control_fence=? AND controller_epoch=?`, now, expected.installationID, expected.snapshot.Generation, facts.installationRevision, expected.fence, expected.controllerEpoch)
		if err := platformLeaseCAS(changed, err); err != nil {
			return zero, err
		}
		if _, err := e.exec(`DELETE FROM app_platform_capability_grants WHERE installation_id=?`, expected.installationID); err != nil {
			return zero, err
		}
		if _, err := e.exec(`DELETE FROM app_platform_access_grants WHERE installation_id=?`, expected.installationID); err != nil {
			return zero, err
		}
		changed, err = e.exec(`UPDATE app_platform_data_namespaces SET state='retained',retained_ms=?,revision=revision+1,updated_ms=? WHERE owner_installation_id=? AND state='active'`, now, now, expected.installationID)
		if err := platformLeaseCAS(changed, err); err != nil {
			return zero, err
		}
		changed, err = e.exec(`UPDATE app_platform_operations SET stage='complete',state_revision=?,row_revision=row_revision+1,completed_ms=?,lease_owner=NULL,lease_until_ms=NULL,updated_ms=? WHERE id=? AND installation_id=? AND controller_epoch=? AND stage=? AND state_revision=? AND row_revision=? AND fence=? AND lease_owner=? AND lease_until_ms=? AND lease_until_ms>? AND completed_ms IS NULL`, int64(result.snapshot.Revision), now, now, expected.id, expected.installationID, expected.controllerEpoch, string(expected.snapshot.Stage), int64(expected.snapshot.Revision), expected.rowRevision, expected.fence, expected.owner, expected.leaseUntilMS, now)
		if err := platformLeaseCAS(changed, err); err != nil {
			return zero, err
		}
	} else {
		changed, err = e.exec(`UPDATE app_platform_operations SET stage=?,state_revision=?,row_revision=row_revision+1,updated_ms=? WHERE id=? AND installation_id=? AND controller_epoch=? AND stage=? AND state_revision=? AND row_revision=? AND fence=? AND lease_owner=? AND lease_until_ms=? AND lease_until_ms>? AND completed_ms IS NULL`, string(next), int64(result.snapshot.Revision), now, expected.id, expected.installationID, expected.controllerEpoch, string(expected.snapshot.Stage), int64(expected.snapshot.Revision), expected.rowRevision, expected.fence, expected.owner, expected.leaseUntilMS, now)
		if err := platformLeaseCAS(changed, err); err != nil {
			return zero, err
		}
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
        WHERE o.action='uninstall' AND o.managed=1 AND o.completed_ms IS NULL AND o.stage IN ('queued','validate','revoke','stop','remove_code')
          AND (o.lease_until_ms IS NULL OR o.lease_until_ms<=?) AND i.desired_state='removed' AND i.phase='pending' AND i.runtime_id IS NOT NULL
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
		if facts.lease.leaseUntilMS > facts.now || facts.lease.snapshot.Action != appplatform.ActionUninstall || !facts.lease.snapshot.Managed {
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
		if _, err := e.exec(`INSERT INTO audit_log(at,actor,actor_ou,action,target_type,target_id,detail,ip) VALUES(?,'system',0,'app_platform.managed_uninstall_claimed','app_platform_operation',?,?,'')`, time.UnixMilli(facts.now).UTC().Format(time.RFC3339), id, string(detail)); err != nil {
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
		default:
			return zero, errAppPlatformStageEvidence
		}
	}
	return platformInstallRun{lease: result}, nil
}

func (s *Server) finishPlatformManagedUninstallForReview(ctx context.Context, expected platformOperationLease) error {
	if expected.snapshot.Action != appplatform.ActionUninstall || !expected.snapshot.Managed {
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
	// Managed uninstall crosses native stop/removal boundaries, so even a
	// pre-dispatch failure is reconciled as an uncertain effect rather than
	// claiming that no process or container state changed.
	failed, err := model.Fail(expected.snapshot.Revision, appplatform.EffectsUnknown)
	if err != nil {
		return errAppPlatformStageEvidence
	}
	changed, err := e.exec(`UPDATE app_platform_installations SET phase='recovery_required',revision=revision+1,updated_ms=? WHERE id=? AND desired_state='removed' AND phase='pending' AND generation=? AND revision=? AND control_fence=? AND controller_epoch=?`, now, expected.installationID, expected.snapshot.Generation, facts.installationRevision, expected.fence, expected.controllerEpoch)
	if err := platformLeaseCAS(changed, err); err != nil {
		return err
	}
	changed, err = e.exec(`UPDATE app_platform_operations SET stage=?,state_revision=?,row_revision=row_revision+1,error_code='managed_uninstall_retry_exhausted',completed_ms=?,updated_ms=?,lease_owner=NULL,lease_until_ms=NULL WHERE id=? AND installation_id=? AND controller_epoch=? AND stage=? AND state_revision=? AND row_revision=? AND fence=? AND lease_owner=? AND lease_until_ms=? AND lease_until_ms>? AND completed_ms IS NULL`, string(failed.Snapshot().Stage), int64(failed.Snapshot().Revision), now, now, expected.id, expected.installationID, expected.controllerEpoch, string(expected.snapshot.Stage), int64(expected.snapshot.Revision), expected.rowRevision, expected.fence, expected.owner, expected.leaseUntilMS, now)
	if err := platformLeaseCAS(changed, err); err != nil {
		return err
	}
	if _, err := e.exec(`INSERT INTO audit_log(at,actor,actor_ou,action,target_type,target_id,detail,ip) VALUES(?,'system',0,'app_platform.managed_uninstall_failed','app_platform_operation',?,'{}','')`, time.UnixMilli(now).UTC().Format(time.RFC3339), expected.id); err != nil {
		return err
	}
	if _, err := finalPlatformOperationLeaseCheck(e, facts); err != nil {
		return err
	}
	return tx.Commit()
}
