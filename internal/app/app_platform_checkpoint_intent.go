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

type platformCheckpointSource struct {
	operationID, startEffectID, dataEffectID      string
	sourceReceiptSHA256, sourceDataReceiptSHA256  string
	resourceID, namespaceID, releaseSHA256        string
	projectID, namespaceInode, device, dataFormat string
	writerFence                                   string
	descriptor                                    []byte
}

type platformCheckpointIntent struct {
	recoverySetID, effectID string
	runtime                 platformRuntimeRegistration
	authority               control.Authority
	command                 control.CheckpointCommand
	source                  platformCheckpointSource
	raw, signature          []byte
}

func platformCheckpointMatchesCommand(receipt journal.CheckpointReceipt, command control.CheckpointCommand) bool {
	return receipt.Stage == "snapshot" &&
		receipt.DeploymentID == command.DeploymentID &&
		receipt.ControllerEpoch == command.ControllerEpoch &&
		receipt.InstallationID == command.InstallationID &&
		receipt.OperationID == command.OperationID &&
		receipt.TargetGeneration == command.TargetGeneration &&
		receipt.EffectID == command.EffectID &&
		receipt.PayloadSHA256 == command.PayloadSHA256 &&
		receipt.CheckpointID == command.Payload.CheckpointID &&
		receipt.NamespaceID == command.Payload.NamespaceID &&
		receipt.ResourceID == command.Payload.ResourceID &&
		receipt.ReleaseSHA256 == command.Payload.ReleaseSHA256 &&
		receipt.SourceOperationID == command.Payload.SourceOperationID &&
		receipt.SourceStartEffectID == command.Payload.SourceStartEffectID &&
		receipt.SourceReceiptSHA256 == command.Payload.SourceReceiptSHA256 &&
		receipt.SourceDataEffectID == command.Payload.SourceDataEffectID &&
		receipt.SourceDataReceiptSHA256 == command.Payload.SourceDataReceiptSHA256 &&
		receipt.ProjectID == command.Payload.ProjectID &&
		receipt.NamespaceInode == command.Payload.NamespaceInode &&
		receipt.Device == command.Payload.Device &&
		receipt.DataFormat == command.Payload.DataFormat &&
		receipt.WriterFence == command.Payload.WriterFence
}

// loadPlatformCheckpointSource derives the checkpoint identity only from a
// completed managed install and its applied start/data effects. The source
// process must already have been stopped by the update quiesce step; the runner
// checks that state independently before writing checkpoint bytes.
func loadPlatformCheckpointSource(e migExec, expected platformOperationLease, runtime platformRuntimeRegistration, now int64) (platformCheckpointSource, error) {
	var zero platformCheckpointSource
	var sourceOperation, sourcePlanRaw, sourcePlanSHA, sourceRelease, descriptorRaw string
	var sourceGeneration, sourceFence, sourceCompleted int64
	if err := e.queryRow(`SELECT o.id,p.plan_json,p.plan_sha256,p.target_release_sha256,r.descriptor,o.target_generation,o.fence,o.completed_ms
		FROM app_platform_operations o
		JOIN app_platform_review_plans p ON p.id=o.review_plan_id
		JOIN app_platform_releases r ON r.sha256=p.target_release_sha256
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
		!platformManagedFormat(plan.Format) || plan.Runtime == nil || plan.ReleaseSHA256 != sourceRelease ||
		parsed.SHA256 != sourceRelease || plan.Runtime.RuntimeID != runtime.id ||
		validateManagedInstallTarget(e, plan.Runtime, expected.controllerEpoch) != nil ||
		sourceGeneration < 1 || sourceFence < 1 || sourceGeneration+1 != int64(expected.snapshot.Generation) ||
		sourceCompleted < 0 || sourceCompleted > now {
		return zero, fmt.Errorf("%w: checkpoint source plan", errAppPlatformStageEvidence)
	}
	trust, err := loadPlatformPublisherTrust(e)
	if err != nil {
		return zero, err
	}
	verified, err := platformVerifyStoredRelease(e, sourceRelease, plan.TrustRevision, trust, now)
	if err != nil || verified.sha256 != sourceRelease || verified.appID != plan.AppID || verified.publisherID != plan.PublisherID {
		return zero, fmt.Errorf("%w: checkpoint source trust", errAppPlatformStageEvidence)
	}
	var appID, publisher, desired, phase, installedEpoch string
	var runtimeID, activeNamespace sql.NullString
	var activation string
	var installationGeneration, policyRevision int64
	if err := e.queryRow(`SELECT app_id,publisher_id,desired_state,phase,generation,policy_revision,controller_epoch,runtime_id
		FROM app_platform_installations WHERE id=?`, expected.installationID).
		Scan(&appID, &publisher, &desired, &phase, &installationGeneration, &policyRevision, &installedEpoch, &runtimeID); err != nil {
		return zero, fmt.Errorf("%w: checkpoint current installation", errAppPlatformStageEvidence)
	}
	if appID != plan.AppID || publisher != plan.PublisherID || desired != "enabled" || phase != "ready" ||
		installedEpoch != expected.controllerEpoch || !runtimeID.Valid || runtimeID.String != runtime.id ||
		installationGeneration != sourceGeneration || policyRevision < 1 {
		return zero, fmt.Errorf("%w: checkpoint activation", errAppPlatformStageEvidence)
	}
	var activeRelease string
	var activeGeneration, activePolicy, activeFormat int64
	if err := e.queryRow(`SELECT id,release_sha256,generation,policy_revision,data_namespace_id,data_format
		FROM app_platform_activations WHERE installation_id=? AND state='active'`, expected.installationID).
		Scan(&activation, &activeRelease, &activeGeneration, &activePolicy, &activeNamespace, &activeFormat); err != nil {
		return zero, fmt.Errorf("%w: checkpoint active activation: %v", errAppPlatformStageEvidence, err)
	}
	if activation == "" || activeRelease != sourceRelease || activeGeneration != sourceGeneration ||
		activePolicy != policyRevision || !activeNamespace.Valid || activeFormat < 1 {
		return zero, errAppPlatformStageEvidence
	}
	source := platformCheckpointSource{
		operationID: sourceOperation, resourceID: activation, namespaceID: activeNamespace.String,
		releaseSHA256: sourceRelease, dataFormat: strconv.FormatInt(activeFormat, 10),
		writerFence: "1", descriptor: descriptor,
	}
	var origin, owner, namespaceState string
	var namespaceFormat, quota, observedBytes, writerFence int64
	var retained, deleted sql.NullInt64
	if err := e.queryRow(`SELECT origin_installation_id,owner_installation_id,state,data_format,quota_bytes,observed_bytes,writer_fence,retained_ms,deleted_ms
		FROM app_platform_data_namespaces WHERE id=?`, source.namespaceID).
		Scan(&origin, &owner, &namespaceState, &namespaceFormat, &quota, &observedBytes, &writerFence, &retained, &deleted); err != nil {
		return zero, errAppPlatformStageEvidence
	}
	if origin != expected.installationID || owner != expected.installationID || namespaceState != "active" ||
		namespaceFormat != activeFormat || quota != 256<<20 || observedBytes < 0 || observedBytes > quota ||
		writerFence != 1 || retained.Valid || deleted.Valid {
		return zero, fmt.Errorf("%w: checkpoint namespace origin=%s owner=%s state=%s format=%d active=%d quota=%d bytes=%d writer=%d retained=%v deleted=%v",
			errAppPlatformStageEvidence, origin, owner, namespaceState, namespaceFormat, activeFormat,
			quota, observedBytes, writerFence, retained.Valid, deleted.Valid)
	}
	history := platformManagedSource{
		operationID: source.operationID, installationID: expected.installationID,
		epoch: expected.controllerEpoch, generation: uint64(sourceGeneration), fence: sourceFence,
	}
	dataEffect, err := loadPlatformManagedHistoryEffect(e, history, runtime.id, "prepare_data", "prepare-data", now)
	if err != nil {
		return zero, fmt.Errorf("%w: checkpoint data effect", errAppPlatformStageEvidence)
	}
	dataCommand, err := platformDataCommandForSource(e, history, runtime, source.namespaceID, dataEffect.id)
	if err != nil {
		return zero, fmt.Errorf("%w: checkpoint data command", errAppPlatformStageEvidence)
	}
	data, dataErr := journal.ParseDataPreparationReceipt([]byte(dataEffect.receipt))
	originalDataCommand := dataCommand
	originalDataCommand.ControlRevision = data.ControlRevision
	if dataErr != nil || !data.MatchesCommand(originalDataCommand) || dataEffect.digest != data.PayloadSHA256 ||
		data.DataFormat != source.dataFormat || data.WriterFence != source.writerFence ||
		data.ProjectID == "" || data.NamespaceInode == "" || data.Device == "" {
		return zero, fmt.Errorf("%w: checkpoint data receipt", errAppPlatformStageEvidence)
	}
	startEffect, err := loadPlatformManagedHistoryEffect(e, history, runtime.id, "start", "start-candidate", now)
	if err != nil {
		return zero, fmt.Errorf("%w: checkpoint start effect", errAppPlatformStageEvidence)
	}
	var nativeID, runningRelease, runningGeneration, runningNamespace, runningOperation string
	var runningFormat, runningWriter string
	switch plan.Format {
	case platformManagedInstallFormat:
		running, parseErr := journal.ParseCandidateRunningReceipt([]byte(startEffect.receipt))
		if parseErr != nil || running.Claim.Preflight == nil ||
			running.Claim.Preflight.Platform != plan.Runtime.Profile.Platform ||
			running.Claim.Preflight.ProjectID != data.ProjectID ||
			running.Claim.Preflight.NamespaceInode != data.NamespaceInode ||
			running.Claim.Preflight.Device != data.Device ||
			running.Claim.DataReceiptSHA256 != platformDigest([]byte(dataEffect.receipt)) ||
			running.NativeID == "" {
			return zero, fmt.Errorf("%w: checkpoint managed running receipt", errAppPlatformStageEvidence)
		}
		candidate := control.CandidateCommand{
			Format: control.CandidateFormat, DeploymentID: runtime.deploymentID, ControllerEpoch: expected.controllerEpoch,
			ControlRevision: running.ControlRevision, InstallationID: expected.installationID, OperationID: source.operationID,
			TargetGeneration: strconv.FormatInt(sourceGeneration, 10), Stage: "start", EffectNumber: "1",
			EffectID: startEffect.id, Kind: "start-candidate", InstallationFence: strconv.FormatInt(sourceFence, 10),
			Payload: control.CandidatePayload{
				ResourceID: running.Claim.ResourceID, DataNamespaceID: running.DataNamespaceID,
				ReleaseSHA256: sourceRelease, Platform: running.Claim.Preflight.Platform,
				DataFormat: running.DataFormat, WriterFence: running.WriterFence,
				SandboxProfile: control.CandidateSandboxProfile,
			},
		}
		candidate.PayloadSHA256, err = control.CandidateLogicalDigest(candidate)
		if err != nil || candidate.PayloadSHA256 != startEffect.digest || !running.MatchesCommand(candidate) {
			return zero, fmt.Errorf("%w: checkpoint managed start command", errAppPlatformStageEvidence)
		}
		source.resourceID = running.Claim.ResourceID
		nativeID, runningRelease, runningGeneration = running.NativeID, running.ReleaseSHA256, running.Generation
		runningNamespace, runningOperation = running.DataNamespaceID, running.OperationID
		runningFormat, runningWriter = running.DataFormat, running.WriterFence
	case platformWritableInstallFormat:
		running, parseErr := journal.ParseWritableCandidateRunningReceipt([]byte(startEffect.receipt))
		if parseErr != nil || running.Claim.Preflight == nil ||
			running.Claim.SandboxProfile != control.WritableCandidateSandboxProfile ||
			running.Claim.EffectID != startEffect.id || running.Claim.PayloadSHA256 != startEffect.digest ||
			running.Claim.DataReceiptSHA256 != platformDigest([]byte(dataEffect.receipt)) ||
			running.Claim.Preflight.Native.ProjectID != data.ProjectID ||
			running.Claim.Preflight.Native.NamespaceInode != data.NamespaceInode ||
			running.Claim.Preflight.Native.Device != data.Device ||
			running.NativeID == "" {
			return zero, fmt.Errorf("%w: checkpoint writable running receipt", errAppPlatformStageEvidence)
		}
		source.resourceID = running.Claim.ResourceID
		nativeID, runningRelease, runningGeneration = running.NativeID, sourceRelease, strconv.FormatInt(sourceGeneration, 10)
		runningNamespace, runningOperation = data.Claim.ResourceID, source.operationID
		runningFormat, runningWriter = data.DataFormat, data.WriterFence
	default:
		return zero, fmt.Errorf("%w: checkpoint running identity", errAppPlatformStageEvidence)
	}
	if runningOperation != source.operationID || runningRelease != sourceRelease ||
		runningGeneration != strconv.FormatInt(sourceGeneration, 10) ||
		runningNamespace != source.namespaceID || runningFormat != source.dataFormat ||
		runningWriter != source.writerFence || nativeID == "" {
		return zero, fmt.Errorf("%w: checkpoint operation count", errAppPlatformStageEvidence)
	}
	source.startEffectID, source.dataEffectID = startEffect.id, dataEffect.id
	source.sourceReceiptSHA256 = platformDigest([]byte(startEffect.receipt))
	source.sourceDataReceiptSHA256 = platformDigest([]byte(dataEffect.receipt))
	source.projectID, source.namespaceInode, source.device = data.ProjectID, data.NamespaceInode, data.Device
	var pending int
	if err := e.queryRow(`SELECT COUNT(*) FROM app_platform_operations WHERE installation_id=? AND completed_ms IS NULL`, expected.installationID).Scan(&pending); err != nil || pending != 1 {
		return zero, errAppPlatformStageEvidence
	}
	return source, nil
}

func platformCheckpointCommand(expected platformOperationLease, runtime platformRuntimeRegistration, source platformCheckpointSource, checkpointID, effectID string) (control.CheckpointCommand, error) {
	command := control.CheckpointCommand{
		Format: control.CheckpointFormat, DeploymentID: runtime.deploymentID,
		ControllerEpoch: expected.controllerEpoch, ControlRevision: strconv.FormatInt(runtime.controlRevision, 10),
		InstallationID: expected.installationID, OperationID: expected.id,
		TargetGeneration: strconv.FormatUint(expected.snapshot.Generation, 10), Stage: "snapshot",
		EffectNumber: "1", EffectID: effectID, Kind: "snapshot",
		InstallationFence: strconv.FormatInt(expected.fence, 10),
		Payload: control.CheckpointPayload{
			CheckpointID: checkpointID, NamespaceID: source.namespaceID, ResourceID: source.resourceID,
			ReleaseSHA256: source.releaseSHA256, SourceOperationID: source.operationID,
			SourceStartEffectID: source.startEffectID, SourceReceiptSHA256: source.sourceReceiptSHA256,
			SourceDataEffectID: source.dataEffectID, SourceDataReceiptSHA256: source.sourceDataReceiptSHA256,
			ProjectID: source.projectID, NamespaceInode: source.namespaceInode, Device: source.device,
			DataFormat: source.dataFormat, WriterFence: source.writerFence,
		},
	}
	digest, err := control.CheckpointLogicalDigest(command)
	if err != nil {
		return control.CheckpointCommand{}, errAppPlatformStageEvidence
	}
	command.PayloadSHA256 = digest
	return command, nil
}

func loadPlatformCheckpointIntent(e migExec, expected platformOperationLease, runtime platformRuntimeRegistration, source platformCheckpointSource, now int64, create, allowVerified bool) (platformCheckpointIntent, bool, error) {
	var zero platformCheckpointIntent
	lock := ""
	if e.s.driver == "postgres" {
		lock = " FOR UPDATE"
	}
	var recoveryCount, effectCount int
	if err := e.queryRow(`SELECT COUNT(*) FROM app_platform_recovery_sets WHERE operation_id=?`, expected.id).Scan(&recoveryCount); err != nil || recoveryCount > 1 {
		return zero, false, errAppPlatformStageEvidence
	}
	if err := e.queryRow(`SELECT COUNT(*) FROM app_platform_operation_effects WHERE operation_id=? AND stage='snapshot' AND effect_number=1`, expected.id).Scan(&effectCount); err != nil || effectCount > 1 {
		return zero, false, errAppPlatformStageEvidence
	}
	var recoverySetID, checkpointID, state, release, namespace, epoch, recoveryInstallation string
	var format, writerFence int64
	var manifest sql.NullString
	var checkpointBytes, verifiedMS, deletedMS sql.NullInt64
	err := e.queryRow(`SELECT id,checkpoint_id,release_sha256,data_format,data_namespace_id,state,controller_epoch,writer_fence
		,installation_id,manifest_sha256,bytes,verified_ms,deleted_ms
		FROM app_platform_recovery_sets WHERE operation_id=?`+lock, expected.id).
		Scan(&recoverySetID, &checkpointID, &release, &format, &namespace, &state, &epoch, &writerFence,
			&recoveryInstallation, &manifest, &checkpointBytes, &verifiedMS, &deletedMS)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return zero, false, err
	}
	fresh := recoveryCount == 0
	if fresh != errors.Is(err, sql.ErrNoRows) || fresh != (effectCount == 0) {
		return zero, false, errAppPlatformStageEvidence
	}
	var effectID, kind, digest, runtimeID, effectEpoch, effectState, receipt string
	var admitted, observed int64
	effectErr := e.queryRow(`SELECT effect_id,kind,payload_sha256,runtime_id,controller_epoch,admitted_fence,observed_fence,state,receipt_json
		FROM app_platform_operation_effects WHERE operation_id=? AND stage='snapshot' AND effect_number=1`+lock, expected.id).
		Scan(&effectID, &kind, &digest, &runtimeID, &effectEpoch, &admitted, &observed, &effectState, &receipt)
	if effectErr != nil && !errors.Is(effectErr, sql.ErrNoRows) {
		return zero, false, effectErr
	}
	if fresh {
		if !create || effectErr == nil {
			return zero, false, errAppPlatformStageEvidence
		}
		recoverySetID, err = platformNewUUID()
		if err != nil {
			return zero, false, err
		}
		checkpointID, err = platformNewUUID()
		if err != nil {
			return zero, false, err
		}
		effectID, err = platformNewUUID()
		if err != nil {
			return zero, false, err
		}
		command, err := platformCheckpointCommand(expected, runtime, source, checkpointID, effectID)
		if err != nil {
			return zero, false, err
		}
		if _, err := e.exec(`INSERT INTO app_platform_recovery_sets(id,installation_id,data_namespace_id,operation_id,release_sha256,data_format,checkpoint_id,manifest_sha256,bytes,state,controller_epoch,writer_fence,verified_ms,deleted_ms,created_ms,updated_ms)
			VALUES(?,?,?,?,?,?,?,NULL,0,'preparing',?,?,NULL,NULL,?,?)`,
			recoverySetID, expected.installationID, source.namespaceID, expected.id, source.releaseSHA256,
			mustPlatformCheckpointInt(source.dataFormat), checkpointID, expected.controllerEpoch,
			mustPlatformCheckpointInt(source.writerFence), now, now); err != nil {
			return zero, false, err
		}
		if _, err := e.exec(`INSERT INTO app_platform_operation_effects(operation_id,stage,effect_number,effect_id,kind,payload_sha256,runtime_id,controller_epoch,admitted_fence,observed_fence,state,receipt_json,created_ms,updated_ms)
			VALUES(?,'snapshot',1,?,'snapshot',?,?,?, ?,0,'prepared','{}',?,?)`,
			expected.id, effectID, command.PayloadSHA256, runtime.id, expected.controllerEpoch, expected.fence, now, now); err != nil {
			return zero, false, err
		}
		zero = platformCheckpointIntent{
			recoverySetID: recoverySetID, effectID: effectID, runtime: runtime,
			authority: control.Authority{
				DeploymentID: runtime.deploymentID, ControllerEpoch: runtime.controllerEpoch,
				ControlRevision: runtime.controlRevision, PublicKey: bytes.Clone(runtime.publicKey),
				Fingerprint: runtime.fingerprint,
			},
			command: command, source: source,
		}
		return zero, true, nil
	}
	preparingFields := state == "preparing" && !manifest.Valid && checkpointBytes.Valid && checkpointBytes.Int64 == 0 && !verifiedMS.Valid && !deletedMS.Valid
	verifiedFields := allowVerified && state == "verified" && manifest.Valid && platformValidSHA(manifest.String) &&
		checkpointBytes.Valid && checkpointBytes.Int64 > 0 && verifiedMS.Valid && !deletedMS.Valid
	preparedEffect := effectState == "prepared" && observed == 0 && receipt == "{}"
	appliedEffect := allowVerified && effectState == "applied" && observed == expected.fence && receipt != "{}"
	if effectErr != nil || effectCount != 1 || !platformValidUUID(recoverySetID) || !platformValidUUID(checkpointID) || !platformValidUUID(effectID) ||
		release != source.releaseSHA256 || format != mustPlatformCheckpointInt(source.dataFormat) ||
		namespace != source.namespaceID || recoveryInstallation != expected.installationID || epoch != expected.controllerEpoch ||
		writerFence != mustPlatformCheckpointInt(source.writerFence) ||
		(!preparingFields && !verifiedFields) ||
		kind != "snapshot" || runtimeID != runtime.id || effectEpoch != expected.controllerEpoch ||
		admitted != expected.fence || (!preparedEffect && !appliedEffect) ||
		(preparingFields && !preparedEffect) || (verifiedFields && !appliedEffect) {
		return zero, false, errAppPlatformStageEvidence
	}
	command, err := platformCheckpointCommand(expected, runtime, source, checkpointID, effectID)
	if err != nil || command.PayloadSHA256 != digest {
		return zero, false, errAppPlatformStageEvidence
	}
	return platformCheckpointIntent{
		recoverySetID: recoverySetID, effectID: effectID, runtime: runtime,
		authority: control.Authority{
			DeploymentID: runtime.deploymentID, ControllerEpoch: runtime.controllerEpoch,
			ControlRevision: runtime.controlRevision, PublicKey: bytes.Clone(runtime.publicKey),
			Fingerprint: runtime.fingerprint,
		},
		command: command, source: source,
	}, false, nil
}

func mustPlatformCheckpointInt(value string) int64 {
	n, _ := strconv.ParseInt(value, 10, 64)
	return n
}

// preparePlatformCheckpoint persists one exact signed checkpoint identity. It
// never dispatches native work and never advances the operation stage.
func (s *Server) preparePlatformCheckpoint(ctx context.Context, expected platformOperationLease) (platformCheckpointIntent, error) {
	var zero platformCheckpointIntent
	if expected.snapshot.Action != appplatform.ActionUpdate || !expected.snapshot.Managed ||
		expected.snapshot.Stage != appplatform.StageSnapshot || expected.snapshot.Generation < 2 {
		return zero, errAppPlatformStageEvidence
	}
	if ctx == nil || ctx.Err() != nil {
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
	runtime, seed, err := s.platformDataRuntime(e, expected.controllerEpoch, dek)
	if err != nil {
		return zero, fmt.Errorf("checkpoint runtime: %w", err)
	}
	defer clear(seed)
	facts, err := loadPlatformBoundOperationLease(e, expected)
	if err != nil {
		return zero, err
	}
	source, err := loadPlatformCheckpointSource(e, expected, runtime, facts.now)
	if err != nil {
		return zero, err
	}
	result, fresh, err := loadPlatformCheckpointIntent(e, expected, runtime, source, facts.now, true, false)
	if err != nil {
		return zero, err
	}
	if fresh {
		detail, _ := json.Marshal(struct {
			RecoverySetID string `json:"recovery_set_id"`
			CheckpointID  string `json:"checkpoint_id"`
			EffectID      string `json:"effect_id"`
		}{result.recoverySetID, result.command.Payload.CheckpointID, result.effectID})
		if _, err := e.exec(`INSERT INTO audit_log(at,actor,actor_ou,action,target_type,target_id,detail,ip) VALUES(?,'system',0,'app_platform.checkpoint_intent','app_platform_operation',?,?,'')`,
			time.UnixMilli(facts.now).UTC().Format(time.RFC3339), expected.id, string(detail)); err != nil {
			return zero, err
		}
	}
	now, err := finalPlatformOperationLeaseCheck(e, facts)
	if err != nil || now > math.MaxInt64-25000 {
		return zero, errAppPlatformLeaseConflict
	}
	issued := time.Now().UnixMilli()
	if issued < now-1000 || issued > now+1000 {
		return zero, errAppPlatformLeaseConflict
	}
	expires := min(issued+25000, expected.leaseUntilMS-1000)
	if expires <= issued {
		return zero, errAppPlatformLeaseConflict
	}
	result.command.IssuedMS, result.command.ExpiresMS = strconv.FormatInt(issued, 10), strconv.FormatInt(expires, 10)
	key := ed25519.NewKeyFromSeed(seed)
	defer clear(key)
	result.raw, result.signature, err = control.SignCheckpoint(result.command, key, result.authority, issued)
	if err != nil {
		return zero, err
	}
	if _, err := finalPlatformOperationLeaseCheck(e, facts); err != nil {
		return zero, err
	}
	if err := tx.Commit(); err != nil {
		return zero, err
	}
	if _, err := control.VerifyCheckpoint(result.raw, result.signature, result.authority, time.Now().UnixMilli()); err != nil {
		return zero, err
	}
	return result, nil
}

type platformCheckpointObservation interface {
	CheckpointReceipt() (journal.CheckpointReceipt, error)
	Controller() control.Authority
	Endpoint() transport.CandidateReceiptEndpoint
	ReceivedAt() time.Time
	CommandSHA256() string
}

func validPlatformCheckpointObservation(observation platformCheckpointObservation, intent platformCheckpointIntent, endpoint platformRuntimeEndpoint) (journal.CheckpointReceipt, bool) {
	receipt, err := observation.CheckpointReceipt()
	if err != nil || !platformCheckpointMatchesCommand(receipt, intent.command) ||
		observation.CommandSHA256() != platformDigest(intent.raw) || observation.ReceivedAt().IsZero() {
		return journal.CheckpointReceipt{}, false
	}
	a, b := observation.Controller(), intent.authority
	if a.DeploymentID != b.DeploymentID || a.ControllerEpoch != b.ControllerEpoch ||
		a.ControlRevision != b.ControlRevision || a.Fingerprint != b.Fingerprint ||
		!bytes.Equal(a.PublicKey, b.PublicKey) {
		return journal.CheckpointReceipt{}, false
	}
	peer := observation.Endpoint()
	age := time.Since(observation.ReceivedAt())
	observed, observedErr := strconv.ParseInt(receipt.ObservedMS, 10, 64)
	settled, settledErr := strconv.ParseInt(receipt.SettledMS, 10, 64)
	issued, issuedErr := strconv.ParseInt(intent.command.IssuedMS, 10, 64)
	if !peer.RootController || peer.Path != endpoint.path || peer.RunnerUID != 0 ||
		peer.ControllerUID != endpoint.controllerUID || peer.ControllerGID != endpoint.controllerGID ||
		age < 0 || age > 3*time.Second || observedErr != nil || settledErr != nil || issuedErr != nil ||
		observed < issued || observed > settled || settled > observation.ReceivedAt().UnixMilli() {
		return journal.CheckpointReceipt{}, false
	}
	return receipt, true
}
