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

const platformManagedUpdateFormat = "portal.managed-update-review/1-dev.1"

type platformManagedUpdatePlan struct {
	Format                   string `json:"format"`
	InstallationID           string `json:"installation_id"`
	AppID                    string `json:"app_id"`
	PublisherID              string `json:"publisher_id"`
	SourceReleaseSHA256      string `json:"source_release_sha256"`
	TargetReleaseSHA256      string `json:"target_release_sha256"`
	SourceGeneration         int64  `json:"source_generation"`
	TargetGeneration         int64  `json:"target_generation"`
	SourcePolicyRevision     int64  `json:"source_policy_revision"`
	TargetPolicyRevision     int64  `json:"target_policy_revision"`
	ControllerEpoch          string `json:"controller_epoch"`
	RecoverySetID            string `json:"recovery_set_id"`
	CheckpointOperationID    string `json:"checkpoint_operation_id"`
	CheckpointEffectID       string `json:"checkpoint_effect_id"`
	CheckpointID             string `json:"checkpoint_id"`
	CheckpointManifestSHA256 string `json:"checkpoint_manifest_sha256"`
	CheckpointArchiveBytes   int64  `json:"checkpoint_archive_bytes"`
	DataNamespaceID          string `json:"data_namespace_id"`
	DataFormat               int64  `json:"data_format"`
	SourceWriterFence        int64  `json:"source_writer_fence"`
}

type platformUpdateStartIntent struct {
	resourceID, effectID string
	runtime              platformRuntimeRegistration
	authority            control.Authority
	command              control.UpdateStartCommand
	checkpoint           journal.CheckpointReceipt
	checkpointRaw        []byte
	source               platformCheckpointSource
	plan                 platformManagedUpdatePlan
	targetDescriptor     []byte
	reservation          *journal.UpdateStartReservation
	running              *journal.UpdateStartRunningReceipt
	raw, signature       []byte
}

func loadPlatformManagedUpdatePlan(e migExec, expected platformOperationLease, source platformCheckpointSource) (platformManagedUpdatePlan, []byte, error) {
	var zero platformManagedUpdatePlan
	var planRaw, planSHA, operationScope, planScope string
	var sourceGeneration, targetGeneration int64
	if err := e.queryRow(`SELECT p.plan_json,p.plan_sha256,o.actor_scope,p.actor_scope,
		o.source_generation,o.target_generation
		FROM app_platform_operations o JOIN app_platform_review_plans p ON p.id=o.review_plan_id
		WHERE o.id=? AND o.installation_id=? AND o.action='update' AND o.managed=1
		AND o.stage='start'`, expected.id, expected.installationID).
		Scan(&planRaw, &planSHA, &operationScope, &planScope,
			&sourceGeneration, &targetGeneration); err != nil {
		return zero, nil, errAppPlatformStageEvidence
	}
	var plan platformManagedUpdatePlan
	unmarshalErr := json.Unmarshal([]byte(planRaw), &plan)
	canonical, marshalErr := json.Marshal(plan)
	if unmarshalErr != nil || marshalErr != nil || string(canonical) != planRaw ||
		platformDigest([]byte(planRaw)) != planSHA || operationScope != planScope ||
		plan.Format != platformManagedUpdateFormat ||
		plan.InstallationID != expected.installationID ||
		plan.ControllerEpoch != expected.controllerEpoch ||
		plan.SourceGeneration != sourceGeneration || plan.TargetGeneration != targetGeneration ||
		sourceGeneration != int64(expected.snapshot.Generation)-1 ||
		targetGeneration != int64(expected.snapshot.Generation) ||
		plan.SourceReleaseSHA256 != source.releaseSHA256 ||
		plan.AppID != source.appID || plan.PublisherID != source.publisherID ||
		plan.SourceReleaseSHA256 == plan.TargetReleaseSHA256 ||
		plan.SourceGeneration < 1 || plan.TargetGeneration != plan.SourceGeneration+1 ||
		plan.SourcePolicyRevision < 1 || plan.TargetPolicyRevision <= plan.SourcePolicyRevision ||
		plan.DataNamespaceID != source.namespaceID ||
		plan.DataFormat != mustPlatformCheckpointInt(source.dataFormat) ||
		plan.SourceWriterFence != mustPlatformCheckpointInt(source.writerFence) ||
		!platformValidSHA(plan.SourceReleaseSHA256) || !platformValidSHA(plan.TargetReleaseSHA256) ||
		!platformValidUUID(plan.RecoverySetID) || !platformValidUUID(plan.CheckpointOperationID) ||
		!platformValidUUID(plan.CheckpointEffectID) || !platformValidUUID(plan.CheckpointID) ||
		!platformValidSHA(plan.CheckpointManifestSHA256) || plan.CheckpointArchiveBytes <= 0 {
		return zero, nil, errAppPlatformStageEvidence
	}
	var appID, publisher string
	var descriptor []byte
	if err := e.queryRow(`SELECT app_id,publisher_id,descriptor FROM app_platform_releases WHERE sha256=?`,
		plan.TargetReleaseSHA256).Scan(&appID, &publisher, &descriptor); err != nil {
		return zero, nil, errAppPlatformStageEvidence
	}
	parsed, err := appplatform.ParseDescriptor(descriptor)
	if err != nil || parsed.SHA256 != plan.TargetReleaseSHA256 ||
		appID != plan.AppID || publisher != plan.PublisherID ||
		parsed.Manifest.ID != plan.AppID || parsed.Manifest.Publisher != plan.PublisherID ||
		parsed.Manifest.Backend == nil || int64(parsed.Manifest.Data.Schema) != plan.DataFormat {
		return zero, nil, errAppPlatformStageEvidence
	}
	platformFound := false
	for _, platform := range parsed.Manifest.Backend.Platforms {
		if platform == source.platform {
			platformFound = true
		}
	}
	if !platformFound {
		return zero, nil, errAppPlatformStageEvidence
	}
	return plan, descriptor, nil
}

func loadPlatformUpdateStartCheckpoint(e migExec, expected platformOperationLease, plan platformManagedUpdatePlan) (journal.CheckpointReceipt, []byte, error) {
	lock := ""
	if e.s.driver == "postgres" {
		lock = " FOR UPDATE"
	}
	var state, checkpointID, manifest, operation string
	var archiveBytes int64
	if err := e.queryRow(`SELECT state,checkpoint_id,manifest_sha256,bytes,operation_id
		FROM app_platform_recovery_sets WHERE id=? AND installation_id=?`+lock,
		plan.RecoverySetID, expected.installationID).
		Scan(&state, &checkpointID, &manifest, &archiveBytes, &operation); err != nil {
		return journal.CheckpointReceipt{}, nil, errAppPlatformStageEvidence
	}
	if state != "verified" || checkpointID != plan.CheckpointID ||
		manifest != plan.CheckpointManifestSHA256 || archiveBytes != plan.CheckpointArchiveBytes ||
		operation != expected.id || plan.CheckpointOperationID != expected.id {
		return journal.CheckpointReceipt{}, nil, errAppPlatformStageEvidence
	}
	var effectID, payload, receiptRaw, effectState string
	if err := e.queryRow(`SELECT effect_id,payload_sha256,receipt_json,state
		FROM app_platform_operation_effects WHERE operation_id=? AND stage='snapshot' AND effect_number=1`,
		expected.id).Scan(&effectID, &payload, &receiptRaw, &effectState); err != nil ||
		effectID != plan.CheckpointEffectID || effectState != "applied" ||
		!platformValidSHA(payload) {
		return journal.CheckpointReceipt{}, nil, errAppPlatformStageEvidence
	}
	receipt, err := journal.ParseCheckpointReceipt([]byte(receiptRaw))
	if err != nil || receipt.EffectID != effectID || receipt.PayloadSHA256 != payload ||
		receipt.OperationID != expected.id || receipt.CheckpointID != checkpointID ||
		receipt.ManifestSHA256 != manifest ||
		mustPlatformCheckpointInt(receipt.ArchiveBytes) != archiveBytes ||
		receipt.ReleaseSHA256 != plan.SourceReleaseSHA256 ||
		receipt.NamespaceID != plan.DataNamespaceID ||
		mustPlatformCheckpointInt(receipt.DataFormat) != plan.DataFormat ||
		mustPlatformCheckpointInt(receipt.WriterFence) != plan.SourceWriterFence {
		return journal.CheckpointReceipt{}, nil, errAppPlatformStageEvidence
	}
	return receipt, []byte(receiptRaw), nil
}

func platformUpdateStartCommand(expected platformOperationLease, runtime platformRuntimeRegistration, source platformCheckpointSource, checkpoint journal.CheckpointReceipt, plan platformManagedUpdatePlan, resourceID, effectID string) (control.UpdateStartCommand, error) {
	command := control.UpdateStartCommand{
		Format: control.UpdateStartFormat, DeploymentID: runtime.deploymentID,
		ControllerEpoch: expected.controllerEpoch, ControlRevision: strconv.FormatInt(runtime.controlRevision, 10),
		InstallationID: expected.installationID, OperationID: expected.id,
		TargetGeneration: strconv.FormatUint(expected.snapshot.Generation, 10),
		Stage:            "start", EffectNumber: "1", EffectID: effectID,
		Kind: "update-start", InstallationFence: strconv.FormatInt(expected.fence, 10),
		Payload: control.UpdateStartPayload{
			ResourceID: resourceID, NamespaceID: source.namespaceID,
			SourceOperationID:             source.operationID,
			SourceStartEffectID:           source.startEffectID,
			SourceStartReceiptSHA256:      source.sourceReceiptSHA256,
			SourceDataEffectID:            source.dataEffectID,
			SourceDataReceiptSHA256:       source.sourceDataReceiptSHA256,
			SourceCheckpointOperationID:   expected.id,
			SourceCheckpointEffectID:      checkpoint.EffectID,
			SourceCheckpointReceiptSHA256: platformDigest([]byte(mustMarshalPlatformCheckpointReceipt(checkpoint))),
			CheckpointID:                  checkpoint.CheckpointID,
			CheckpointManifestSHA256:      checkpoint.ManifestSHA256,
			CheckpointArchiveBytes:        checkpoint.ArchiveBytes,
			SourceReleaseSHA256:           source.releaseSHA256,
			TargetReleaseSHA256:           plan.TargetReleaseSHA256,
			SourceGeneration:              strconv.FormatUint(expected.snapshot.Generation-1, 10),
			TargetPolicyRevision:          strconv.FormatInt(plan.TargetPolicyRevision, 10),
			ProjectID:                     source.projectID, NamespaceInode: source.namespaceInode,
			Device: source.device, QuotaBytes: source.quotaBytes,
			QuotaInodes: source.quotaInodes, RootFilesystemUUID: source.rootFilesystemUUID,
			RootInode: source.rootInode, DataFormat: source.dataFormat,
			SourceWriterFence: source.writerFence,
			TargetWriterFence: strconv.FormatInt(mustPlatformCheckpointInt(source.writerFence)+1, 10),
			Platform:          source.platform, SandboxProfile: source.sandboxProfile,
		},
	}
	digest, err := control.UpdateStartLogicalDigest(command)
	if err != nil {
		return control.UpdateStartCommand{}, errAppPlatformStageEvidence
	}
	command.PayloadSHA256 = digest
	return command, nil
}

func loadPlatformUpdateStartIntent(e migExec, expected platformOperationLease, runtime platformRuntimeRegistration, source platformCheckpointSource, checkpoint journal.CheckpointReceipt, plan platformManagedUpdatePlan, now int64, create bool) (platformUpdateStartIntent, bool, error) {
	var zero platformUpdateStartIntent
	lock := ""
	if e.s.driver == "postgres" {
		lock = " FOR UPDATE"
	}
	var activation, release, state, namespace string
	var generation, policy, format int64
	var readiness string
	var observed sql.NullInt64
	err := e.queryRow(`SELECT id,release_sha256,generation,policy_revision,data_namespace_id,
		data_format,state,readiness_evidence,observed_ms
		FROM app_platform_activations WHERE installation_id=? AND generation=?`+lock,
		expected.installationID, expected.snapshot.Generation).
		Scan(&activation, &release, &generation, &policy, &namespace, &format, &state, &readiness, &observed)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return zero, false, err
	}
	var effectID, kind, digest, effectEpoch, effectState, receipt string
	var admitted, observedFence int64
	effectErr := e.queryRow(`SELECT effect_id,kind,payload_sha256,controller_epoch,
		admitted_fence,observed_fence,state,receipt_json
		FROM app_platform_operation_effects WHERE operation_id=? AND stage='start' AND effect_number=1`+lock,
		expected.id).Scan(&effectID, &kind, &digest, &effectEpoch, &admitted,
		&observedFence, &effectState, &receipt)
	if effectErr != nil && !errors.Is(effectErr, sql.ErrNoRows) {
		return zero, false, effectErr
	}
	fresh := errors.Is(err, sql.ErrNoRows)
	if fresh != errors.Is(effectErr, sql.ErrNoRows) {
		return zero, false, errAppPlatformStageEvidence
	}
	if fresh {
		if !create {
			return zero, false, errAppPlatformStageEvidence
		}
		activation, err = platformNewUUID()
		if err != nil {
			return zero, false, err
		}
		effectID, err = platformNewUUID()
		if err != nil {
			return zero, false, err
		}
		if _, err := e.exec(`INSERT INTO app_platform_activations(id,installation_id,release_sha256,generation,policy_revision,data_namespace_id,data_format,state,readiness_evidence,observed_ms,created_ms,updated_ms)
			VALUES(?,?,?,?,?,?,?,'candidate','{}',NULL,?,?)`,
			activation, expected.installationID, plan.TargetReleaseSHA256,
			expected.snapshot.Generation, plan.TargetPolicyRevision, plan.DataNamespaceID,
			plan.DataFormat, now, now); err != nil {
			return zero, false, err
		}
		command, err := platformUpdateStartCommand(expected, runtime, source, checkpoint, plan, activation, effectID)
		if err != nil {
			return zero, false, err
		}
		if _, err := e.exec(`INSERT INTO app_platform_operation_effects(operation_id,stage,effect_number,effect_id,kind,payload_sha256,runtime_id,controller_epoch,admitted_fence,observed_fence,state,receipt_json,created_ms,updated_ms)
			VALUES(?,'start',1,?,'update-start',?,?,?, ?,0,'prepared','{}',?,?)`,
			expected.id, effectID, command.PayloadSHA256, runtime.id,
			expected.controllerEpoch, expected.fence, now, now); err != nil {
			return zero, false, err
		}
		return platformUpdateStartIntent{
			resourceID: activation, effectID: effectID, runtime: runtime,
			authority: control.Authority{DeploymentID: runtime.deploymentID,
				ControllerEpoch: runtime.controllerEpoch, ControlRevision: runtime.controlRevision,
				PublicKey: bytes.Clone(runtime.publicKey), Fingerprint: runtime.fingerprint},
			command: command, checkpoint: checkpoint, checkpointRaw: []byte(mustMarshalPlatformCheckpointReceipt(checkpoint)),
			source: source, plan: plan, targetDescriptor: bytes.Clone(source.descriptor),
		}, true, nil
	}
	if release != plan.TargetReleaseSHA256 || generation != int64(expected.snapshot.Generation) ||
		policy != plan.TargetPolicyRevision || namespace != plan.DataNamespaceID ||
		format != plan.DataFormat || state != "candidate" || readiness != "{}" ||
		observed.Valid || kind != "update-start" || effectEpoch != expected.controllerEpoch ||
		admitted != expected.fence || (effectState != "prepared" && effectState != "applied") {
		return zero, false, errAppPlatformStageEvidence
	}
	command, err := platformUpdateStartCommand(expected, runtime, source, checkpoint, plan, activation, effectID)
	if err != nil || command.PayloadSHA256 != digest {
		return zero, false, errAppPlatformStageEvidence
	}
	result := platformUpdateStartIntent{
		resourceID: activation, effectID: effectID, runtime: runtime,
		authority: control.Authority{DeploymentID: runtime.deploymentID,
			ControllerEpoch: runtime.controllerEpoch, ControlRevision: runtime.controlRevision,
			PublicKey: bytes.Clone(runtime.publicKey), Fingerprint: runtime.fingerprint},
		command: command, checkpoint: checkpoint, checkpointRaw: []byte(mustMarshalPlatformCheckpointReceipt(checkpoint)),
		source: source, plan: plan, targetDescriptor: bytes.Clone(source.descriptor),
	}
	switch effectState {
	case "prepared":
		if receipt == "{}" {
			if observedFence != 0 {
				return zero, false, errAppPlatformStageEvidence
			}
			return result, false, nil
		}
		if observedFence != expected.fence {
			return zero, false, errAppPlatformStageEvidence
		}
		reservation, err := journal.ParseUpdateStartReservation([]byte(receipt))
		if err != nil || !platformUpdateStartReservationMatches(reservation, command, activation, effectID) {
			return zero, false, errAppPlatformStageEvidence
		}
		result.reservation = &reservation
	case "applied":
		if observedFence != expected.fence {
			return zero, false, errAppPlatformStageEvidence
		}
		running, err := journal.ParseUpdateStartRunningReceipt([]byte(receipt))
		if err != nil || !platformUpdateStartRunningMatches(running, command, activation, effectID, nil) {
			return zero, false, errAppPlatformStageEvidence
		}
		reservation := journal.UpdateStartReservation{
			ResourceID: running.Claim.ResourceID, InstallationID: running.InstallationID,
			NamespaceID: running.NamespaceID, EffectID: running.Claim.EffectID,
			OwnershipSHA256:     running.Claim.OwnershipSHA256,
			SourceGeneration:    mustPlatformCheckpointInt(running.SourceGeneration),
			TargetGeneration:    mustPlatformCheckpointInt(running.TargetGeneration),
			SourceReleaseSHA256: running.SourceReleaseSHA256,
			TargetReleaseSHA256: running.TargetReleaseSHA256,
			ControlFence:        mustPlatformCheckpointInt(running.ControlFence),
			SourceWriterFence:   mustPlatformCheckpointInt(running.SourceWriterFence),
			TargetWriterFence:   mustPlatformCheckpointInt(running.TargetWriterFence),
		}
		result.reservation = &reservation
		result.running = &running
	default:
		return zero, false, errAppPlatformStageEvidence
	}
	return result, false, nil
}

func (s *Server) preparePlatformUpdateStart(ctx context.Context, expected platformOperationLease) (platformUpdateStartIntent, error) {
	var zero platformUpdateStartIntent
	if expected.snapshot.Action != appplatform.ActionUpdate || !expected.snapshot.Managed ||
		expected.snapshot.Stage != appplatform.StageStart || expected.snapshot.Generation < 2 {
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
		return zero, fmt.Errorf("update-start runtime: %w", err)
	}
	defer clear(seed)
	facts, err := loadPlatformBoundOperationLease(e, expected)
	if err != nil {
		return zero, err
	}
	source, err := loadPlatformCheckpointSource(e, expected, runtime, facts.now)
	if err != nil {
		return zero, fmt.Errorf("update-start source: %w", err)
	}
	plan, targetDescriptor, err := loadPlatformManagedUpdatePlan(e, expected, source)
	if err != nil {
		return zero, err
	}
	checkpoint, checkpointRaw, err := loadPlatformUpdateStartCheckpoint(e, expected, plan)
	if err != nil {
		return zero, err
	}
	result, fresh, err := loadPlatformUpdateStartIntent(e, expected, runtime, source, checkpoint, plan, facts.now, true)
	if err != nil {
		return zero, fmt.Errorf("update-start intent: %w", err)
	}
	result.checkpointRaw, result.targetDescriptor = bytes.Clone(checkpointRaw), bytes.Clone(targetDescriptor)
	if fresh {
		detail, _ := json.Marshal(struct {
			ResourceID string `json:"resource_id"`
			EffectID   string `json:"effect_id"`
		}{result.resourceID, result.effectID})
		if _, err := e.exec(`INSERT INTO audit_log(at,actor,actor_ou,action,target_type,target_id,detail,ip)
			VALUES(?,'system',0,'app_platform.update_start_intent','app_platform_operation',?,?,'')`,
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
	result.raw, result.signature, err = control.SignUpdateStart(result.command, key, result.authority, issued)
	if err != nil {
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

type platformUpdateStartObservation interface {
	UpdateStartReservation() (journal.UpdateStartReservation, error)
	Controller() control.Authority
	Endpoint() transport.CandidateReceiptEndpoint
	ReceivedAt() time.Time
	CommandSHA256() string
}

type platformUpdateStartRunningObservation interface {
	UpdateStartRunningReceipt() (journal.UpdateStartRunningReceipt, error)
	Controller() control.Authority
	Endpoint() transport.CandidateReceiptEndpoint
	ReceivedAt() time.Time
	CommandSHA256() string
}

func validPlatformUpdateStartObservation(observation platformUpdateStartObservation, intent platformUpdateStartIntent, endpoint platformRuntimeEndpoint) (journal.UpdateStartReservation, bool) {
	reservation, err := observation.UpdateStartReservation()
	command := intent.command
	if err != nil || !platformUpdateStartReservationMatches(reservation, command, intent.resourceID, intent.effectID) ||
		observation.CommandSHA256() != platformDigest(intent.raw) || observation.ReceivedAt().IsZero() {
		return journal.UpdateStartReservation{}, false
	}
	a, b := observation.Controller(), intent.authority
	peer := observation.Endpoint()
	age := time.Since(observation.ReceivedAt())
	if a.DeploymentID != b.DeploymentID || a.ControllerEpoch != b.ControllerEpoch ||
		a.ControlRevision != b.ControlRevision || a.Fingerprint != b.Fingerprint ||
		!bytes.Equal(a.PublicKey, b.PublicKey) || !peer.RootController ||
		peer.Path != endpoint.path || peer.RunnerUID != 0 ||
		peer.ControllerUID != endpoint.controllerUID || peer.ControllerGID != endpoint.controllerGID ||
		age < 0 || age > 3*time.Second {
		return journal.UpdateStartReservation{}, false
	}
	return reservation, true
}

func validPlatformUpdateStartRunningObservation(observation platformUpdateStartRunningObservation, intent platformUpdateStartIntent, endpoint platformRuntimeEndpoint) (journal.UpdateStartRunningReceipt, bool) {
	receipt, err := observation.UpdateStartRunningReceipt()
	if err != nil || !platformUpdateStartRunningMatches(receipt, intent.command, intent.resourceID, intent.effectID, intent.reservation) ||
		observation.CommandSHA256() != platformDigest(intent.raw) || observation.ReceivedAt().IsZero() {
		return journal.UpdateStartRunningReceipt{}, false
	}
	received := observation.ReceivedAt()
	observed, observedErr := strconv.ParseInt(receipt.NativeObservedMS, 10, 64)
	settled, settledErr := strconv.ParseInt(receipt.SettledMS, 10, 64)
	bound, boundErr := strconv.ParseInt(receipt.Claim.PreflightBoundMS, 10, 64)
	if observedErr != nil || settledErr != nil || boundErr != nil ||
		observed < bound || observed > received.UnixMilli() ||
		settled < observed || settled > received.UnixMilli() {
		return journal.UpdateStartRunningReceipt{}, false
	}
	a, b := observation.Controller(), intent.authority
	peer := observation.Endpoint()
	age := time.Since(observation.ReceivedAt())
	if a.DeploymentID != b.DeploymentID || a.ControllerEpoch != b.ControllerEpoch ||
		a.ControlRevision != b.ControlRevision || a.Fingerprint != b.Fingerprint ||
		!bytes.Equal(a.PublicKey, b.PublicKey) || !peer.RootController ||
		peer.Path != endpoint.path || peer.RunnerUID != 0 ||
		peer.ControllerUID != endpoint.controllerUID || peer.ControllerGID != endpoint.controllerGID ||
		age < 0 || age > 3*time.Second {
		return journal.UpdateStartRunningReceipt{}, false
	}
	return receipt, true
}

func platformUpdateStartRunningMatches(receipt journal.UpdateStartRunningReceipt, command control.UpdateStartCommand, resourceID, effectID string, reservation *journal.UpdateStartReservation) bool {
	claim := receipt.Claim
	if claim.Preflight == nil || claim.EffectID != effectID || claim.ResourceID != resourceID ||
		claim.PayloadSHA256 != command.PayloadSHA256 ||
		claim.CheckpointReceiptSHA256 != command.Payload.SourceCheckpointReceiptSHA256 ||
		claim.ControlRevision != command.ControlRevision ||
		claim.AdmittedFence != command.InstallationFence ||
		claim.SandboxProfile != command.Payload.SandboxProfile ||
		receipt.DeploymentID != command.DeploymentID ||
		receipt.ControllerEpoch != command.ControllerEpoch ||
		receipt.InstallationID != command.InstallationID ||
		receipt.OperationID != command.OperationID ||
		receipt.NamespaceID != command.Payload.NamespaceID ||
		receipt.SourceReleaseSHA256 != command.Payload.SourceReleaseSHA256 ||
		receipt.TargetReleaseSHA256 != command.Payload.TargetReleaseSHA256 ||
		receipt.SourceGeneration != command.Payload.SourceGeneration ||
		receipt.TargetGeneration != command.TargetGeneration ||
		receipt.ControlFence != command.InstallationFence ||
		receipt.SourceWriterFence != command.Payload.SourceWriterFence ||
		receipt.TargetWriterFence != command.Payload.TargetWriterFence ||
		receipt.DataFormat != command.Payload.DataFormat {
		return false
	}
	preflight := claim.Preflight
	if preflight.Platform != command.Payload.Platform ||
		preflight.RootFilesystemUUID != command.Payload.RootFilesystemUUID ||
		preflight.RootInode != command.Payload.RootInode ||
		preflight.ProjectID != command.Payload.ProjectID ||
		preflight.Device != command.Payload.Device ||
		preflight.NamespaceInode != command.Payload.NamespaceInode ||
		preflight.QuotaBytes != command.Payload.QuotaBytes ||
		preflight.QuotaInodes != command.Payload.QuotaInodes {
		return false
	}
	if reservation != nil {
		value := *reservation
		if claim.OwnershipSHA256 != value.OwnershipSHA256 ||
			claim.ResourceID != value.ResourceID || claim.EffectID != value.EffectID ||
			receipt.NamespaceID != value.NamespaceID ||
			receipt.SourceReleaseSHA256 != value.SourceReleaseSHA256 ||
			receipt.TargetReleaseSHA256 != value.TargetReleaseSHA256 ||
			mustPlatformCheckpointInt(receipt.SourceGeneration) != value.SourceGeneration ||
			mustPlatformCheckpointInt(receipt.TargetGeneration) != value.TargetGeneration ||
			mustPlatformCheckpointInt(receipt.ControlFence) != value.ControlFence ||
			mustPlatformCheckpointInt(receipt.SourceWriterFence) != value.SourceWriterFence ||
			mustPlatformCheckpointInt(receipt.TargetWriterFence) != value.TargetWriterFence {
			return false
		}
	}
	return true
}

func platformUpdateStartReservationMatches(reservation journal.UpdateStartReservation, command control.UpdateStartCommand, resourceID, effectID string) bool {
	return reservation.ResourceID == resourceID &&
		reservation.InstallationID == command.InstallationID &&
		reservation.NamespaceID == command.Payload.NamespaceID &&
		reservation.EffectID == effectID &&
		reservation.SourceGeneration == mustPlatformCheckpointInt(command.Payload.SourceGeneration) &&
		reservation.TargetGeneration == mustPlatformCheckpointInt(command.TargetGeneration) &&
		reservation.SourceReleaseSHA256 == command.Payload.SourceReleaseSHA256 &&
		reservation.TargetReleaseSHA256 == command.Payload.TargetReleaseSHA256 &&
		reservation.ControlFence == mustPlatformCheckpointInt(command.InstallationFence) &&
		reservation.SourceWriterFence == mustPlatformCheckpointInt(command.Payload.SourceWriterFence) &&
		reservation.TargetWriterFence == mustPlatformCheckpointInt(command.Payload.TargetWriterFence)
}

func platformUpdateStartReservationsEqual(a, b journal.UpdateStartReservation) bool {
	a.Replayed, b.Replayed = false, false
	return a == b
}
