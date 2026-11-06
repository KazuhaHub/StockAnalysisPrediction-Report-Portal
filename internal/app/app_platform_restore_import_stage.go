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
)

// Execution of a restore_import operation: queued -> validate -> restore_data -> complete. The
// operation is admitted by consumePlatformRestoreImportReview; nothing here re-admits it. Every stage
// re-reads the grant, the recovery set and the checkpoint receipt, because a lease outlives any
// earlier look at them.

type platformRestoreImportEvidence struct {
	plan    platformRestoreImportPlan
	source  platformImportSource
	grantID string
}

func platformImportRestoreGuard(lease platformOperationLease) bool {
	return lease.snapshot.Action == appplatform.ActionRestoreImport && lease.snapshot.Managed
}

func loadPlatformRestoreImportEvidence(e migExec, s *Server, lease platformOperationLease) (platformRestoreImportEvidence, error) {
	var ev platformRestoreImportEvidence
	var raw, sha, planInstallation, planEpoch string
	var planSource, planTarget int64
	if err := e.queryRow(`SELECT p.plan_json,p.plan_sha256,p.installation_id,p.controller_epoch,o.source_generation,o.target_generation
		FROM app_platform_operations o JOIN app_platform_review_plans p ON p.id=o.review_plan_id
		WHERE o.id=? AND o.installation_id=? AND o.action='restore_import' AND o.managed=1 AND p.action='restore_import'`,
		lease.id, lease.installationID).Scan(&raw, &sha, &planInstallation, &planEpoch, &planSource, &planTarget); err != nil {
		return ev, fmt.Errorf("%w: restore import plan row", errAppPlatformStageEvidence)
	}
	plan := &ev.plan
	if json.Unmarshal([]byte(raw), plan) != nil {
		return ev, fmt.Errorf("%w: restore import plan", errAppPlatformStageEvidence)
	}
	canonical, _ := json.Marshal(plan)
	if string(canonical) != raw || platformDigest([]byte(raw)) != sha || plan.Format != platformRestoreImportFormat ||
		plan.InstallationID != lease.installationID || planInstallation != lease.installationID ||
		planEpoch != lease.controllerEpoch || plan.ControllerEpoch != lease.controllerEpoch ||
		planSource != int64(lease.snapshot.Generation)-1 || planTarget != int64(lease.snapshot.Generation) ||
		plan.SourceGeneration != planSource || plan.TargetGeneration != planTarget ||
		!platformValidUUID(plan.RecoverySetID) || !platformValidUUID(plan.RuntimeID) || !platformValidUUID(plan.SourceRuntimeID) {
		return ev, fmt.Errorf("%w: restore import plan binding", errAppPlatformStageEvidence)
	}
	var runtimeID sql.NullString
	if err := e.queryRow(`SELECT runtime_id FROM app_platform_installations WHERE id=?`, lease.installationID).Scan(&runtimeID); err != nil ||
		!runtimeID.Valid || runtimeID.String != plan.RuntimeID {
		return ev, fmt.Errorf("%w: installation is not on the import runtime", errAppPlatformStageEvidence)
	}
	active, found, _, err := platformActiveRuntime(e, false)
	if err != nil || !found || active.id != plan.RuntimeID || active.enabled != 1 || active.deploymentID != plan.DeploymentID ||
		active.controllerEpoch != lease.controllerEpoch {
		return ev, fmt.Errorf("%w: import runtime", errAppPlatformStageEvidence)
	}
	lock := ""
	if e.s.driver == "postgres" {
		lock = " FOR UPDATE"
	}
	var state, grantEpoch string
	if err := e.queryRow(`SELECT id,state,controller_epoch FROM app_platform_import_grants WHERE recovery_set_id=? AND runtime_id=? AND state<>'revoked'`+lock,
		plan.RecoverySetID, plan.RuntimeID).Scan(&ev.grantID, &state, &grantEpoch); err != nil ||
		state != "issued" || grantEpoch != lease.controllerEpoch {
		return ev, fmt.Errorf("%w: import grant is not issued to this runtime", errAppPlatformStageEvidence)
	}
	source, err := loadPlatformImportSource(e, s, lease.installationID, plan.RecoverySetID)
	if err != nil {
		return ev, err
	}
	if source.state != "review_required" || source.epoch != lease.controllerEpoch || source.checkpointID != plan.CheckpointID ||
		source.manifest != plan.ManifestSHA256 || source.release != plan.ReleaseSHA256 || source.namespaceID != plan.NamespaceID ||
		source.archiveBytes != plan.ArchiveBytes || source.dataFormat != plan.DataFormat || source.writerFence != plan.WriterFence ||
		source.operationID != plan.CheckpointOperationID || source.checkpoint.EffectID != plan.CheckpointEffectID {
		return ev, fmt.Errorf("%w: recovery set no longer matches the reviewed plan", errAppPlatformStageEvidence)
	}
	ev.source = source
	return ev, nil
}

func (s *Server) advancePlatformRestoreImportStage(ctx context.Context, expected platformOperationLease, next appplatform.Stage) (platformOperationLease, error) {
	var zero platformOperationLease
	if !platformImportRestoreGuard(expected) || expected.rowRevision == math.MaxInt64 ||
		!((expected.snapshot.Stage == appplatform.StageQueued && next == appplatform.StageValidate) ||
			(expected.snapshot.Stage == appplatform.StageValidate && next == appplatform.StageRestoreData)) {
		return zero, fmt.Errorf("restore import stage transition %s -> %s: %w", expected.snapshot.Stage, next, errAppPlatformStageEvidence)
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
	if _, err := loadPlatformRestoreImportEvidence(e, s, expected); err != nil {
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
	now, err := finalPlatformOperationLeaseCheck(e, facts)
	if err != nil {
		return zero, err
	}
	result := expected
	result.snapshot, result.rowRevision = advanced.Snapshot(), expected.rowRevision+1
	changed, err := e.exec(`UPDATE app_platform_operations SET stage=?,state_revision=?,row_revision=row_revision+1,updated_ms=?
		WHERE id=? AND installation_id=? AND controller_epoch=? AND stage=? AND state_revision=? AND row_revision=? AND fence=?
		AND lease_owner=? AND lease_until_ms=? AND lease_until_ms>? AND completed_ms IS NULL`,
		string(next), int64(result.snapshot.Revision), now, expected.id, expected.installationID, expected.controllerEpoch,
		string(expected.snapshot.Stage), int64(expected.snapshot.Revision), expected.rowRevision, expected.fence, expected.owner,
		expected.leaseUntilMS, now)
	if err := platformLeaseCAS(changed, err); err != nil {
		return zero, err
	}
	detail, _ := json.Marshal(struct {
		Stage appplatform.Stage `json:"stage"`
	}{next})
	if _, err := e.exec(`INSERT INTO audit_log(at,actor,actor_ou,action,target_type,target_id,detail,ip) VALUES(?,'system',0,'app_platform.operation_stage','app_platform_operation',?,?,'')`,
		time.UnixMilli(now).UTC().Format(time.RFC3339), expected.id, string(detail)); err != nil {
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

func platformImportCommand(expected platformOperationLease, runtime platformRuntimeRegistration, ev platformRestoreImportEvidence, effectID string, admittedFence int64) (control.ImportCheckpointCommand, error) {
	cp := ev.source.checkpoint
	command := control.ImportCheckpointCommand{
		Format: control.ImportCheckpointFormat, DeploymentID: runtime.deploymentID, ControllerEpoch: expected.controllerEpoch,
		ControlRevision: strconv.FormatInt(runtime.controlRevision, 10), InstallationID: expected.installationID, OperationID: expected.id,
		TargetGeneration: strconv.FormatUint(expected.snapshot.Generation, 10), Stage: "restore_data", EffectNumber: "1", EffectID: effectID,
		Kind: "import-checkpoint", InstallationFence: strconv.FormatInt(admittedFence, 10),
		Payload: control.ImportCheckpointPayload{
			GrantID: ev.grantID, RecoverySetID: ev.plan.RecoverySetID, CheckpointID: ev.source.checkpointID, NamespaceID: ev.source.namespaceID,
			ReleaseSHA256: ev.source.release, ManifestSHA256: ev.source.manifest, ArchiveBytes: strconv.FormatInt(ev.source.archiveBytes, 10),
			DataFormat: strconv.FormatInt(ev.source.dataFormat, 10), WriterFence: strconv.FormatInt(ev.source.writerFence, 10),
			QuotaBytes: strconv.FormatInt(ev.source.quotaBytes, 10), QuotaInodes: platformImportQuotaInodes,
			SourceProjectID: cp.ProjectID, SourceNamespaceInode: cp.NamespaceInode, SourceDevice: cp.Device,
			SourceFilesystemUUID: ev.source.sourceFilesystemUUID, SourceRootInode: ev.source.sourceRootInode,
		},
	}
	digest, err := control.ImportCheckpointLogicalDigest(command)
	if err != nil {
		return command, errAppPlatformStageEvidence
	}
	command.PayloadSHA256 = digest
	return command, nil
}

func platformImportAuthority(runtime platformRuntimeRegistration) control.Authority {
	return control.Authority{
		DeploymentID: runtime.deploymentID, ControllerEpoch: runtime.controllerEpoch, ControlRevision: runtime.controlRevision,
		PublicKey: bytes.Clone(runtime.publicKey), Fingerprint: runtime.fingerprint,
	}
}

// While an import effect is prepared, its receipt column records the latest expiry of any command the
// Portal has signed for it ("signed_until_ms"), or is "{}" before the first. The runner accepts a
// command until it expires, so until then an absent answer proves nothing about a command that may
// still be in flight. The column is the effect row's own: no schema is involved.
const platformImportAbsenceMarginMS = 5000

func platformImportPendingReceipt(until int64) string {
	raw, _ := json.Marshal(struct {
		SignedUntil int64 `json:"signed_until_ms"`
	}{until})
	return string(raw)
}

// platformImportSignedUntil reads the pending receipt of a prepared import effect.
func platformImportSignedUntil(receipt string) (int64, bool) {
	if receipt == "{}" {
		return 0, true
	}
	var probe struct {
		SignedUntil int64 `json:"signed_until_ms"`
	}
	if json.Unmarshal([]byte(receipt), &probe) != nil || probe.SignedUntil <= 0 || platformImportPendingReceipt(probe.SignedUntil) != receipt {
		return 0, false
	}
	return probe.SignedUntil, true
}

// loadPlatformImportIntent accepts either no effect (create, once) or the exact original unobserved
// one. It never repairs a partial row and never mints a second effect for a lease takeover: a
// takeover raises the installation fence, and the original effect is signed again at the fence it was
// admitted under, which is the fence the runner's claim binds.
func loadPlatformImportIntent(e migExec, s *Server, expected platformOperationLease, runtime platformRuntimeRegistration, ev platformRestoreImportEvidence, now int64, create bool) (platformImportIntent, bool, error) {
	var zero platformImportIntent
	lock := ""
	if e.s.driver == "postgres" {
		lock = " FOR UPDATE"
	}
	var effectID, kind, digest, effectEpoch, state, receipt string
	var admitted, observed int64
	effectErr := e.queryRow(`SELECT effect_id,kind,payload_sha256,controller_epoch,admitted_fence,observed_fence,state,receipt_json
		FROM app_platform_operation_effects WHERE operation_id=? AND stage='restore_data' AND effect_number=1`+lock, expected.id).
		Scan(&effectID, &kind, &digest, &effectEpoch, &admitted, &observed, &state, &receipt)
	if effectErr != nil && !errors.Is(effectErr, sql.ErrNoRows) {
		return zero, false, effectErr
	}
	fresh := errors.Is(effectErr, sql.ErrNoRows)
	var priorSignedUntil int64
	if fresh {
		if !create {
			return zero, false, errAppPlatformStageEvidence
		}
		var err error
		if effectID, err = platformNewUUID(); err != nil {
			return zero, false, err
		}
		admitted, receipt, state = expected.fence, "{}", "prepared"
	} else {
		until, ok := platformImportSignedUntil(receipt)
		// A finished operation (recovery_required) has had its prepared effect marked unknown.
		wantState := "prepared"
		if expected.snapshot.Stage == appplatform.StageRecoveryRequired {
			wantState = "unknown"
		}
		if kind != "import-checkpoint" || effectEpoch != expected.controllerEpoch || admitted <= 0 || admitted > expected.fence || observed != 0 ||
			state != wantState || !ok {
			return zero, false, errAppPlatformStageEvidence
		}
		priorSignedUntil = until
	}
	command, err := platformImportCommand(expected, runtime, ev, effectID, admitted)
	if err != nil {
		return zero, false, err
	}
	if fresh {
		if _, err := e.exec(`INSERT INTO app_platform_operation_effects(operation_id,stage,effect_number,effect_id,kind,payload_sha256,runtime_id,controller_epoch,admitted_fence,observed_fence,state,receipt_json,created_ms,updated_ms)
			VALUES(?,'restore_data',1,?,'import-checkpoint',?,?,?,?,0,'prepared','{}',?,?)`,
			expected.id, effectID, command.PayloadSHA256, runtime.id, expected.controllerEpoch, admitted, now, now); err != nil {
			return zero, false, err
		}
	} else if command.PayloadSHA256 != digest {
		return zero, false, errAppPlatformStageEvidence
	}
	descriptor, err := platformReleaseDescriptor(e, ev.source.release)
	if err != nil || len(descriptor) == 0 {
		return zero, false, fmt.Errorf("%w: release descriptor", errAppPlatformStageEvidence)
	}
	return platformImportIntent{effectID: effectID, admittedFence: admitted, runtime: runtime, authority: platformImportAuthority(runtime), command: command, descriptor: descriptor,
		pendingReceipt: receipt, priorSignedUntil: priorSignedUntil, effectState: state}, fresh, nil
}

func (s *Server) preparePlatformRestoreImport(ctx context.Context, expected platformOperationLease) (platformImportIntent, error) {
	return s.signPlatformRestoreImport(ctx, expected, true)
}

// signPlatformRestoreImport signs the import effect of the operation, creating it only when create
// is set and none exists. The result's fresh flag says whether this call created it.
func (s *Server) signPlatformRestoreImport(ctx context.Context, expected platformOperationLease, create bool) (platformImportIntent, error) {
	var zero platformImportIntent
	if !platformImportRestoreGuard(expected) || expected.snapshot.Stage != appplatform.StageRestoreData || ctx == nil || ctx.Err() != nil {
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
		return zero, fmt.Errorf("restore import runtime: %w", err)
	}
	defer clear(seed)
	facts, err := loadPlatformBoundOperationLease(e, expected)
	if err != nil {
		return zero, err
	}
	ev, err := loadPlatformRestoreImportEvidence(e, s, expected)
	if err != nil || ev.plan.RuntimeID != runtime.id {
		return zero, errors.Join(errAppPlatformStageEvidence, err)
	}
	result, fresh, err := loadPlatformImportIntent(e, s, expected, runtime, ev, facts.now, create)
	if err != nil {
		return zero, err
	}
	result.fresh = fresh
	if fresh {
		detail, _ := json.Marshal(struct {
			RecoverySetID string `json:"recovery_set_id"`
			GrantID       string `json:"grant_id"`
			EffectID      string `json:"effect_id"`
		}{ev.plan.RecoverySetID, ev.grantID, result.effectID})
		if _, err := e.exec(`INSERT INTO audit_log(at,actor,actor_ou,action,target_type,target_id,detail,ip) VALUES(?,'system',0,'app_platform.restore_import_intent','app_platform_operation',?,?,'')`,
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
	// Record the expiry before the command exists outside this transaction.
	changed, err := e.exec(`UPDATE app_platform_operation_effects SET receipt_json=?
		WHERE operation_id=? AND stage='restore_data' AND effect_number=1 AND effect_id=? AND state='prepared' AND observed_fence=0 AND receipt_json=?`,
		platformImportPendingReceipt(max(result.priorSignedUntil, expires)), expected.id, result.effectID, result.pendingReceipt)
	if err := platformLeaseCAS(changed, err); err != nil {
		return zero, err
	}
	key := ed25519.NewKeyFromSeed(seed)
	defer clear(key)
	if result.raw, result.signature, err = control.SignImportCheckpoint(result.command, key, result.authority, issued); err != nil {
		return zero, err
	}
	if result.readSignature, err = control.SignImportCheckpointRead(result.raw, key, result.authority, issued); err != nil {
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

func validPlatformImportObservation(observation platformImportObservation, intent platformImportIntent, endpoint platformRuntimeEndpoint) (journal.ImportCheckpointReceipt, bool) {
	receipt, err := observation.ImportCheckpointReceipt()
	if err != nil || !platformImportReceiptMatchesCommand(receipt, intent.command) ||
		observation.CommandSHA256() != platformDigest(intent.raw) || observation.ReceivedAt().IsZero() {
		return journal.ImportCheckpointReceipt{}, false
	}
	a, b := observation.Controller(), intent.authority
	if a.DeploymentID != b.DeploymentID || a.ControllerEpoch != b.ControllerEpoch || a.ControlRevision != b.ControlRevision ||
		a.Fingerprint != b.Fingerprint || !bytes.Equal(a.PublicKey, b.PublicKey) {
		return journal.ImportCheckpointReceipt{}, false
	}
	peer := observation.Endpoint()
	age := time.Since(observation.ReceivedAt())
	observed, observedErr := strconv.ParseInt(receipt.ObservedMS, 10, 64)
	settled, settledErr := strconv.ParseInt(receipt.SettledMS, 10, 64)
	issued, issuedErr := strconv.ParseInt(intent.command.IssuedMS, 10, 64)
	if !peer.RootController || peer.Path != endpoint.path || peer.RunnerUID != 0 || peer.ControllerUID != endpoint.controllerUID ||
		peer.ControllerGID != endpoint.controllerGID || age < 0 || age > 3*time.Second || observedErr != nil || settledErr != nil ||
		issuedErr != nil || observed < issued || observed > settled || settled > observation.ReceivedAt().UnixMilli() {
		return journal.ImportCheckpointReceipt{}, false
	}
	return receipt, true
}

// commitPlatformRestoreImport settles the effect, consumes the grant and completes the operation in
// one transaction. The grant is consumed by a conditional update that must change exactly one row, so
// a grant that was already consumed, revoked or issued to another runtime cannot be spent again.
func (s *Server) commitPlatformRestoreImport(ctx context.Context, expected platformOperationLease, intent platformImportIntent, endpoint platformRuntimeEndpoint, observation platformImportObservation) (platformOperationLease, error) {
	var zero platformOperationLease
	if !platformImportRestoreGuard(expected) || expected.snapshot.Stage != appplatform.StageRestoreData || expected.rowRevision == math.MaxInt64 {
		return zero, errAppPlatformStageEvidence
	}
	intent.raw, intent.signature = bytes.Clone(intent.raw), bytes.Clone(intent.signature)
	intent.authority.PublicKey = bytes.Clone(intent.authority.PublicKey)
	receipt, ok := validPlatformImportObservation(observation, intent, endpoint)
	if !ok {
		return zero, errAppPlatformStageEvidence
	}
	if err := ctx.Err(); err != nil {
		return zero, err
	}
	encoded, err := json.Marshal(receipt)
	if err != nil || len(encoded) > 16384 {
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
	if err := refusePlatformAbandonedRoot(e, receipt); err != nil {
		return zero, err
	}
	ev, err := loadPlatformRestoreImportEvidence(e, s, expected)
	if err != nil || ev.plan.RuntimeID != runtime.id {
		return zero, errors.Join(errAppPlatformStageEvidence, err)
	}
	current, _, err := loadPlatformImportIntent(e, s, expected, runtime, ev, facts.now, false)
	if err != nil {
		return zero, err
	}
	currentCommand, intentCommand := current.command, intent.command
	currentCommand.IssuedMS, currentCommand.ExpiresMS, intentCommand.IssuedMS, intentCommand.ExpiresMS = "", "", "", ""
	if current.effectID != intent.effectID || currentCommand != intentCommand || current.authority.Fingerprint != intent.authority.Fingerprint ||
		!bytes.Equal(current.authority.PublicKey, intent.authority.PublicKey) {
		return zero, errAppPlatformStageEvidence
	}
	now, err := finalPlatformOperationLeaseCheck(e, facts)
	if err != nil {
		return zero, errAppPlatformStageEvidence
	}
	changed, err := e.exec(`UPDATE app_platform_operation_effects SET state='applied',observed_fence=?,receipt_json=?,updated_ms=?
		WHERE operation_id=? AND stage='restore_data' AND effect_number=1 AND effect_id=? AND kind='import-checkpoint'
		AND payload_sha256=? AND runtime_id=? AND controller_epoch=? AND admitted_fence=? AND state='prepared'
		AND observed_fence=0 AND receipt_json=?`,
		expected.fence, string(encoded), now, expected.id, current.effectID, current.command.PayloadSHA256, runtime.id,
		expected.controllerEpoch, current.admittedFence, current.pendingReceipt)
	if err := platformLeaseCAS(changed, err); err != nil {
		return zero, err
	}
	changed, err = e.exec(`UPDATE app_platform_import_grants SET state='consumed',consumed_ms=?,updated_ms=?
		WHERE id=? AND recovery_set_id=? AND runtime_id=? AND controller_epoch=? AND state='issued'`,
		now, now, ev.grantID, ev.plan.RecoverySetID, runtime.id, expected.controllerEpoch)
	if err := platformLeaseCAS(changed, err); err != nil {
		return zero, err
	}
	model, err := appplatform.OperationFromSnapshot(expected.snapshot)
	if err != nil {
		return zero, errAppPlatformStageEvidence
	}
	next, err := model.Advance(expected.snapshot.Revision, appplatform.StageComplete)
	if err != nil {
		return zero, errAppPlatformStageEvidence
	}
	result := expected
	result.snapshot, result.rowRevision = next.Snapshot(), expected.rowRevision+1
	result.owner, result.leaseUntilMS = "", 0
	changed, err = e.exec(`UPDATE app_platform_operations SET stage='complete',state_revision=?,row_revision=row_revision+1,completed_ms=?,updated_ms=?,lease_owner=NULL,lease_until_ms=NULL
		WHERE id=? AND installation_id=? AND controller_epoch=? AND stage='restore_data' AND state_revision=? AND row_revision=? AND fence=?
		AND lease_owner=? AND lease_until_ms=? AND lease_until_ms>? AND completed_ms IS NULL`,
		int64(result.snapshot.Revision), now, now, expected.id, expected.installationID, expected.controllerEpoch,
		int64(expected.snapshot.Revision), expected.rowRevision, expected.fence, expected.owner, expected.leaseUntilMS, now)
	if err := platformLeaseCAS(changed, err); err != nil {
		return zero, err
	}
	detail, _ := json.Marshal(struct {
		RecoverySetID string `json:"recovery_set_id"`
		GrantID       string `json:"grant_id"`
		CheckpointID  string `json:"checkpoint_id"`
		ManifestSHA   string `json:"manifest_sha256"`
		Entries       string `json:"entries"`
	}{ev.plan.RecoverySetID, ev.grantID, ev.source.checkpointID, ev.source.manifest, receipt.Entries})
	if _, err := e.exec(`INSERT INTO audit_log(at,actor,actor_ou,action,target_type,target_id,detail,ip) VALUES(?,'system',0,'app_platform.restore_import_applied','app_platform_operation',?,?,'')`,
		time.UnixMilli(now).UTC().Format(time.RFC3339), expected.id, string(detail)); err != nil {
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

// replayPlatformRestoreImport accepts a receipt for an import that has already settled, and only when
// it is byte-identical to the one recorded. It changes nothing.
func (s *Server) replayPlatformRestoreImport(ctx context.Context, operationID string, intent platformImportIntent, endpoint platformRuntimeEndpoint, observation platformImportObservation) error {
	receipt, ok := validPlatformImportObservation(observation, intent, endpoint)
	if !ok {
		return errAppPlatformStageEvidence
	}
	encoded, err := json.Marshal(receipt)
	if err != nil {
		return errAppPlatformStageEvidence
	}
	var stage, effectState, stored, digest, grantState string
	var effectUpdated, consumed sql.NullInt64
	if err := s.st.db.QueryRowContext(ctx, s.st.bind(`SELECT o.stage,f.state,f.receipt_json,f.payload_sha256,f.updated_ms
		FROM app_platform_operations o JOIN app_platform_operation_effects f ON f.operation_id=o.id AND f.stage='restore_data' AND f.effect_number=1
		WHERE o.id=? AND o.action='restore_import' AND f.effect_id=?`), operationID, intent.effectID).
		Scan(&stage, &effectState, &stored, &digest, &effectUpdated); err != nil {
		return errAppPlatformStageEvidence
	}
	if err := s.st.db.QueryRowContext(ctx, s.st.bind(`SELECT state,consumed_ms FROM app_platform_import_grants WHERE id=?`), intent.command.Payload.GrantID).
		Scan(&grantState, &consumed); err != nil {
		return errAppPlatformStageEvidence
	}
	if stage != "complete" || effectState != "applied" || stored != string(encoded) || digest != intent.command.PayloadSHA256 ||
		grantState != "consumed" || !consumed.Valid || consumed.Int64 != effectUpdated.Int64 {
		return errAppPlatformStageEvidence
	}
	return nil
}

func (s *Server) dispatchPlatformRestoreImport(ctx context.Context, expected platformOperationLease, endpoint platformRuntimeEndpoint) (platformOperationLease, error) {
	intent, err := s.preparePlatformRestoreImport(ctx, expected)
	if err != nil {
		return platformOperationLease{}, err
	}
	if !intent.fresh {
		// An earlier attempt may have reached the runner. Ask before sending anything: an applied
		// answer settles the effect, a definite no-row answer ends the operation, and anything else
		// falls through to executing the same effect, which the runner reconciles rather than repeats.
		lease, outcome, err := s.settlePlatformRestoreImportReceipt(ctx, expected, endpoint, intent)
		if err != nil || outcome != platformImportUnknown {
			return lease, err
		}
	}
	observation, err := s.platformImportExecutor().Execute(ctx, endpoint, intent)
	if err != nil {
		return platformOperationLease{}, err
	}
	return s.commitPlatformRestoreImport(ctx, expected, intent, endpoint, observation)
}

const (
	platformImportApplied = "applied"
	platformImportAbsent  = "absent"
	platformImportUnknown = "unknown"
)

// errPlatformImportAbsent is what an executor returns when the runner states definitively that it
// holds no row for the effect: the command was refused before it was claimed.
var errPlatformImportAbsent = errors.New("runner holds no import effect")

// errPlatformImportEnded reports an import operation that this attempt ended as failed with its grant
// revoked; the scheduler has nothing left to retry or finish.
var errPlatformImportEnded = errors.New("restore import ended without an effect")

// reconcilePlatformRestoreImport asks the runner for the receipt of the operation's existing import
// effect, never creating one. Applied settles the effect and consumes the grant; absent reverts the
// effect and revokes the grant so a new review can be admitted; unknown changes nothing.
func (s *Server) reconcilePlatformRestoreImport(ctx context.Context, expected platformOperationLease, endpoint platformRuntimeEndpoint) (platformOperationLease, string, error) {
	intent, err := s.signPlatformRestoreImport(ctx, expected, false)
	if err != nil {
		return platformOperationLease{}, platformImportUnknown, err
	}
	return s.settlePlatformRestoreImportReceipt(ctx, expected, endpoint, intent)
}

func (s *Server) settlePlatformRestoreImportReceipt(ctx context.Context, expected platformOperationLease, endpoint platformRuntimeEndpoint, intent platformImportIntent) (platformOperationLease, string, error) {
	observation, err := s.platformImportExecutor().Read(ctx, endpoint, intent)
	if errors.Is(err, errPlatformImportAbsent) {
		if err := s.endPlatformRestoreImport(ctx, expected, intent.effectID, intent.priorSignedUntil); err != nil {
			if errors.Is(err, errPlatformImportAbsenceNotDefinite) {
				return platformOperationLease{}, platformImportUnknown, nil
			}
			return platformOperationLease{}, platformImportUnknown, err
		}
		return platformOperationLease{}, platformImportAbsent, errPlatformImportEnded
	}
	if err != nil {
		return platformOperationLease{}, platformImportUnknown, nil
	}
	lease, err := s.commitPlatformRestoreImport(ctx, expected, intent, endpoint, observation)
	if err != nil {
		return platformOperationLease{}, platformImportUnknown, err
	}
	return lease, platformImportApplied, nil
}

func (s *Server) runPlatformRestoreImport(ctx context.Context, expected platformOperationLease, endpoint platformRuntimeEndpoint) (platformOperationLease, error) {
	if !platformImportRestoreGuard(expected) || endpoint.path == "" {
		return platformOperationLease{}, errAppPlatformStageEvidence
	}
	switch expected.snapshot.Stage {
	case appplatform.StageQueued:
		return s.advancePlatformRestoreImportStage(ctx, expected, appplatform.StageValidate)
	case appplatform.StageValidate:
		return s.advancePlatformRestoreImportStage(ctx, expected, appplatform.StageRestoreData)
	case appplatform.StageRestoreData:
		return s.dispatchPlatformRestoreImport(ctx, expected, endpoint)
	default:
		return platformOperationLease{}, errAppPlatformStageEvidence
	}
}

// finishPlatformRestoreImportForReview ends an operation the scheduler has given up on. An import
// that never produced an effect cannot have touched the runner: it fails and gives its grant back.
// One that did may have applied: it is left for review with the grant intact and the effect unknown,
// never retried under a new identity.
func (s *Server) finishPlatformRestoreImportForReview(ctx context.Context, expected platformOperationLease) error {
	return s.endPlatformRestoreImport(ctx, expected, "", 0)
}

// errPlatformImportAbsenceNotDefinite reports an absent answer that cannot yet be taken as final,
// because a command signed for the effect has not expired.
var errPlatformImportAbsenceNotDefinite = errors.New("a command signed for the import effect has not expired")

// endPlatformRestoreImport ends the operation. revertedEffect names an effect the runner has
// definitively never claimed: it is reverted and the operation fails with the grant given back, as if
// the effect had never been created. That is final only once every command signed for it before this
// one has expired (definiteAfterMS plus a margin): until then the runner could still claim one.
func (s *Server) endPlatformRestoreImport(ctx context.Context, expected platformOperationLease, revertedEffect string, definiteAfterMS int64) error {
	if !platformImportRestoreGuard(expected) || expected.snapshot.Stage == appplatform.StageComplete ||
		expected.snapshot.Stage == appplatform.StageFailed || expected.snapshot.Stage == appplatform.StageRecoveryRequired {
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
	var effects int
	if err := e.queryRow(`SELECT COUNT(*) FROM app_platform_operation_effects WHERE operation_id=?`, expected.id).Scan(&effects); err != nil {
		return err
	}
	model, err := appplatform.OperationFromSnapshot(expected.snapshot)
	if err != nil {
		return errAppPlatformStageEvidence
	}
	outcome, code, target := appplatform.EffectsNone, "restore_import_failed", "failed"
	if effects != 0 && revertedEffect == "" {
		outcome, code, target = appplatform.EffectsUnknown, "restore_import_recovery_required", "recovery_required"
	}
	ended, err := model.Fail(expected.snapshot.Revision, outcome)
	if err != nil {
		return errAppPlatformStageEvidence
	}
	now, err := finalPlatformOperationLeaseCheck(e, facts)
	if err != nil {
		return err
	}
	if revertedEffect != "" && now <= definiteAfterMS+platformImportAbsenceMarginMS {
		return errPlatformImportAbsenceNotDefinite
	}
	changed, err := e.exec(`UPDATE app_platform_operations SET stage=?,state_revision=?,row_revision=row_revision+1,error_code=?,completed_ms=?,updated_ms=?,lease_owner=NULL,lease_until_ms=NULL
		WHERE id=? AND installation_id=? AND controller_epoch=? AND stage=? AND state_revision=? AND row_revision=? AND fence=?
		AND lease_owner=? AND lease_until_ms=? AND lease_until_ms>? AND completed_ms IS NULL`,
		target, int64(ended.Snapshot().Revision), code, now, now, expected.id, expected.installationID, expected.controllerEpoch,
		string(expected.snapshot.Stage), int64(expected.snapshot.Revision), expected.rowRevision, expected.fence, expected.owner,
		expected.leaseUntilMS, now)
	if err := platformLeaseCAS(changed, err); err != nil {
		return err
	}
	if revertedEffect != "" {
		changed, err := e.exec(`UPDATE app_platform_operation_effects SET state='reverted',updated_ms=? WHERE operation_id=? AND effect_id=? AND state='prepared' AND observed_fence=0`,
			now, expected.id, revertedEffect)
		if err := platformLeaseCAS(changed, err); err != nil {
			return err
		}
	}
	if effects == 0 || revertedEffect != "" {
		var planRaw string
		if err := e.queryRow(`SELECT p.plan_json FROM app_platform_operations o JOIN app_platform_review_plans p ON p.id=o.review_plan_id WHERE o.id=?`, expected.id).Scan(&planRaw); err != nil {
			return err
		}
		var plan platformRestoreImportPlan
		if json.Unmarshal([]byte(planRaw), &plan) != nil {
			return errAppPlatformStageEvidence
		}
		if _, err := e.exec(`UPDATE app_platform_import_grants SET state='revoked',updated_ms=? WHERE state='issued' AND recovery_set_id=? AND runtime_id=?`,
			now, plan.RecoverySetID, plan.RuntimeID); err != nil {
			return err
		}
	} else if _, err := e.exec(`UPDATE app_platform_operation_effects SET state='unknown',updated_ms=? WHERE operation_id=? AND state='prepared'`, now, expected.id); err != nil {
		return err
	}
	detail, _ := json.Marshal(struct {
		Stage appplatform.Stage `json:"stage"`
		Code  string            `json:"code"`
	}{expected.snapshot.Stage, code})
	if _, err := e.exec(`INSERT INTO audit_log(at,actor,actor_ou,action,target_type,target_id,detail,ip) VALUES(?,'system',0,'app_platform.restore_import_ended','app_platform_operation',?,?,'')`,
		time.UnixMilli(now).UTC().Format(time.RFC3339), expected.id, string(detail)); err != nil {
		return err
	}
	if _, err := finalPlatformOperationLeaseCheck(e, facts); err != nil {
		return err
	}
	return tx.Commit()
}

// claimNextPlatformRestoreImport selects the next restore import, and only on a paused Portal.
func (s *Server) claimNextPlatformRestoreImport(ctx context.Context, worker string) (platformInstallSelection, error) {
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
	if err := e.queryRow(`SELECT COALESCE((SELECT v FROM meta WHERE k='app_platform_controller_epoch'),''),COALESCE((SELECT v FROM meta WHERE k='app_platform_restore_paused'),'')`).Scan(&epoch, &paused); err != nil || epoch == "" {
		return zero, errAppPlatformControlPaused
	}
	if paused != "1" {
		return zero, nil
	}
	now, err := platformDatabaseNow(e)
	if err != nil {
		return zero, err
	}
	rows, err := e.query(`SELECT o.id FROM app_platform_operations o JOIN app_platform_installations i ON i.id=o.installation_id
		WHERE o.action='restore_import' AND o.managed=1 AND o.completed_ms IS NULL AND o.stage IN ('queued','validate','restore_data')
		AND (o.lease_until_ms IS NULL OR o.lease_until_ms<=?) AND i.phase='review_required' AND i.controller_epoch=? AND o.controller_epoch=?
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
		if facts.lease.leaseUntilMS > facts.now || !platformImportRestoreGuard(facts.lease) {
			continue
		}
		lease, err := writePlatformOperationLease(e, facts, worker, nil)
		if err != nil {
			return zero, err
		}
		detail, _ := json.Marshal(struct {
			Worker string `json:"worker_id"`
		}{worker})
		if _, err := e.exec(`INSERT INTO audit_log(at,actor,actor_ou,action,target_type,target_id,detail,ip) VALUES(?,'system',0,'app_platform.restore_import_claimed','app_platform_operation',?,?,'')`,
			time.UnixMilli(facts.now).UTC().Format(time.RFC3339), id, string(detail)); err != nil {
			return zero, err
		}
		check := facts
		check.lease = lease
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
