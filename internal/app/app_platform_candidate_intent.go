package app

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"database/sql"
	"encoding/json"
	"strconv"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/journal"
)

type platformCandidateIntent struct {
	resourceID, effectID       string
	runtime                    platformRuntimeRegistration
	authority                  control.Authority
	command                    control.CandidateCommand
	raw, signature, descriptor []byte
	data                       journal.DataPreparationReceipt
	dataSHA                    string
}

// The only candidate resource is the existing candidate activation row. Its
// presence records intent, never readiness or live application access.
func loadPlatformCandidateIntent(e migExec, lease platformOperationLease, runtime platformRuntimeRegistration, now int64, create bool) (platformCandidateIntent, bool, error) {
	var zero, result platformCandidateIntent
	target, err := platformManagedTargetForOperation(e, lease.id)
	if err != nil || target.RuntimeID != runtime.id {
		return zero, false, errAppPlatformRuntimeConflict
	}
	var release string
	if err := e.queryRow(`SELECT p.target_release_sha256,r.descriptor FROM app_platform_operations o JOIN app_platform_review_plans p ON p.id=o.review_plan_id JOIN app_platform_releases r ON r.sha256=p.target_release_sha256 WHERE o.id=?`, lease.id).Scan(&release, &result.descriptor); err != nil {
		return zero, false, err
	}
	var namespace, origin, owner, state, dataEffect, dataKind, dataDigest, dataRuntime, epoch, receipt string
	var format, quota, used, writer, revision, admitted, observed, created, updated int64
	var retained, deleted sql.NullInt64
	var namespaceCount int
	if err := e.queryRow(`SELECT COUNT(*) FROM app_platform_data_namespaces WHERE owner_installation_id=? OR origin_installation_id=?`, lease.installationID, lease.installationID).Scan(&namespaceCount); err != nil {
		return zero, false, err
	}
	if namespaceCount != 1 {
		return zero, false, errAppPlatformStageEvidence
	}
	err = e.queryRow(`SELECT id,origin_installation_id,owner_installation_id,state,data_format,quota_bytes,observed_bytes,writer_fence,revision,retained_ms,deleted_ms FROM app_platform_data_namespaces WHERE owner_installation_id=?`, lease.installationID).Scan(&namespace, &origin, &owner, &state, &format, &quota, &used, &writer, &revision, &retained, &deleted)
	if err != nil {
		return zero, false, err
	}
	if origin != lease.installationID || owner != lease.installationID || state != "active" || format < 1 || quota != 256<<20 || used < 0 || used > quota || writer != 1 || revision != 2 || retained.Valid || deleted.Valid {
		return zero, false, errAppPlatformStageEvidence
	}
	err = e.queryRow(`SELECT effect_id,kind,payload_sha256,runtime_id,controller_epoch,admitted_fence,observed_fence,state,receipt_json,created_ms,updated_ms FROM app_platform_operation_effects WHERE operation_id=? AND stage='prepare_data' AND effect_number=1`, lease.id).Scan(&dataEffect, &dataKind, &dataDigest, &dataRuntime, &epoch, &admitted, &observed, &state, &receipt, &created, &updated)
	if err != nil {
		return zero, false, err
	}
	if dataKind != "prepare-data" || dataRuntime != runtime.id || epoch != lease.controllerEpoch || admitted != lease.fence || observed != lease.fence || state != "applied" || created < 0 || updated < created || updated > now {
		return zero, false, errAppPlatformStageEvidence
	}
	dataCommand, err := platformDataCommand(e, lease, runtime, namespace, dataEffect)
	if err != nil || dataDigest != dataCommand.PayloadSHA256 {
		return zero, false, errAppPlatformStageEvidence
	}
	result.data, err = journal.ParseDataPreparationReceipt([]byte(receipt))
	canonical, marshalErr := json.Marshal(result.data)
	if err != nil || marshalErr != nil || string(canonical) != receipt || !result.data.MatchesCommand(dataCommand) || result.data.Claim.RootFilesystemUUID != target.Profile.RootFilesystemUUID || result.data.Claim.RootInode != target.Profile.RootInode || result.data.DataFormat != strconv.FormatInt(format, 10) || result.data.UsedBytes != strconv.FormatInt(used, 10) {
		return zero, false, errAppPlatformStageEvidence
	}
	result.dataSHA = platformDigest(canonical)
	var forbidden, effects, candidates int
	if err := e.queryRow(`SELECT (SELECT COUNT(*) FROM app_platform_access_grants WHERE installation_id=?)+(SELECT COUNT(*) FROM app_platform_activations WHERE installation_id=? AND state<>'candidate'),(SELECT COUNT(*) FROM app_platform_operation_effects WHERE operation_id=?),(SELECT COUNT(*) FROM app_platform_activations WHERE installation_id=?)`, lease.installationID, lease.installationID, lease.id, lease.installationID).Scan(&forbidden, &effects, &candidates); err != nil {
		return zero, false, err
	}
	if forbidden != 0 {
		return zero, false, errAppPlatformStageEvidence
	}
	fresh := candidates == 0 && effects == 1
	if fresh {
		if !create {
			return zero, false, errAppPlatformStageEvidence
		}
		result.resourceID, err = platformNewUUID()
		if err != nil {
			return zero, false, err
		}
		result.effectID, err = platformNewUUID()
		if err != nil {
			return zero, false, err
		}
	} else {
		if candidates != 1 || effects != 2 {
			return zero, false, errAppPlatformStageEvidence
		}
		var storedRelease, storedNamespace, readiness string
		var generation, policy, storedFormat int64
		var observedAt sql.NullInt64
		err = e.queryRow(`SELECT id,release_sha256,generation,policy_revision,data_namespace_id,data_format,state,readiness_evidence,observed_ms,created_ms,updated_ms FROM app_platform_activations WHERE installation_id=?`, lease.installationID).Scan(&result.resourceID, &storedRelease, &generation, &policy, &storedNamespace, &storedFormat, &state, &readiness, &observedAt, &created, &updated)
		if err != nil {
			return zero, false, err
		}
		if !platformValidUUID(result.resourceID) || storedRelease != release || generation != 1 || policy != 1 || storedNamespace != namespace || storedFormat != format || state != "candidate" || readiness != "{}" || observedAt.Valid || created < 0 || updated != created || updated > now {
			return zero, false, errAppPlatformStageEvidence
		}
		var kind, digest, runtimeID string
		err = e.queryRow(`SELECT effect_id,kind,payload_sha256,runtime_id,controller_epoch,admitted_fence,observed_fence,state,receipt_json,created_ms,updated_ms FROM app_platform_operation_effects WHERE operation_id=? AND stage='start' AND effect_number=1`, lease.id).Scan(&result.effectID, &kind, &digest, &runtimeID, &epoch, &admitted, &observed, &state, &receipt, &created, &updated)
		if err != nil {
			return zero, false, err
		}
		if !platformValidUUID(result.effectID) || kind != "start-candidate" || runtimeID != runtime.id || epoch != lease.controllerEpoch || admitted != lease.fence || observed != 0 || state != "prepared" || receipt != "{}" || created < 0 || updated != created || updated > now {
			return zero, false, errAppPlatformStageEvidence
		}
		result.command.PayloadSHA256 = digest
	}
	c := control.CandidateCommand{Format: control.CandidateFormat, DeploymentID: runtime.deploymentID, ControllerEpoch: lease.controllerEpoch, ControlRevision: strconv.FormatInt(runtime.controlRevision, 10), InstallationID: lease.installationID, OperationID: lease.id, TargetGeneration: "1", Stage: "start", EffectNumber: "1", EffectID: result.effectID, Kind: "start-candidate", InstallationFence: strconv.FormatInt(lease.fence, 10), Payload: control.CandidatePayload{ResourceID: result.resourceID, DataNamespaceID: namespace, ReleaseSHA256: release, Platform: target.Profile.Platform, DataFormat: strconv.FormatInt(format, 10), WriterFence: "1", SandboxProfile: target.Profile.SandboxProfile}}
	c.PayloadSHA256, err = control.CandidateLogicalDigest(c)
	if err != nil || (!fresh && c.PayloadSHA256 != result.command.PayloadSHA256) {
		return zero, false, errAppPlatformStageEvidence
	}
	result.command = c
	if fresh {
		if _, err := e.exec(`INSERT INTO app_platform_activations(id,installation_id,release_sha256,generation,policy_revision,data_namespace_id,data_format,state,readiness_evidence,observed_ms,created_ms,updated_ms) VALUES(?,?,?,1,1,?,?,'candidate','{}',NULL,?,?)`, result.resourceID, lease.installationID, release, namespace, format, now, now); err != nil {
			return zero, false, err
		}
		if _, err := e.exec(`INSERT INTO app_platform_operation_effects(operation_id,stage,effect_number,effect_id,kind,payload_sha256,runtime_id,controller_epoch,admitted_fence,observed_fence,state,receipt_json,created_ms,updated_ms) VALUES(?,'start',1,?,'start-candidate',?,?,?,?,0,'prepared','{}',?,?)`, lease.id, result.effectID, c.PayloadSHA256, runtime.id, lease.controllerEpoch, lease.fence, now, now); err != nil {
			return zero, false, err
		}
	}
	result.runtime = runtime
	result.authority = control.Authority{DeploymentID: runtime.deploymentID, ControllerEpoch: runtime.controllerEpoch, ControlRevision: runtime.controlRevision, PublicKey: bytes.Clone(runtime.publicKey), Fingerprint: runtime.fingerprint}
	return result, fresh, nil
}

func validPlatformCandidateStage(lease platformOperationLease) bool {
	return lease.snapshot.Action == appplatform.ActionInstall && lease.snapshot.Managed && lease.snapshot.Generation == 1 && lease.snapshot.Stage == appplatform.StageStart
}

// Preparing intent does no native IO. Repeated calls retain the same resource,
// effect and logical digest. Takeover/partial/unknown records require recovery.
func (s *Server) preparePlatformInstallCandidate(ctx context.Context, expected platformOperationLease) (platformCandidateIntent, error) {
	var zero platformCandidateIntent
	if !validPlatformCandidateStage(expected) {
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
	if err := validatePlatformInstallAuthority(e, expected, trust, facts.now); err != nil {
		return zero, err
	}
	result, fresh, err := loadPlatformCandidateIntent(e, expected, runtime, facts.now, true)
	if err != nil {
		return zero, err
	}
	if fresh {
		detail, _ := json.Marshal(struct {
			ResourceID string `json:"resource_id"`
			EffectID   string `json:"effect_id"`
		}{result.resourceID, result.effectID})
		if _, err := e.exec(`INSERT INTO audit_log(at,actor,actor_ou,action,target_type,target_id,detail,ip) VALUES(?,'system',0,'app_platform.candidate_intent','app_platform_operation',?,?,'')`, time.UnixMilli(facts.now).UTC().Format(time.RFC3339), expected.id, string(detail)); err != nil {
			return zero, err
		}
	}
	now, err := finalPlatformOperationLeaseCheck(e, facts)
	if err != nil {
		return zero, err
	}
	if err := validatePlatformInstallAuthority(e, expected, trust, now); err != nil {
		return zero, err
	}
	if _, _, err := loadPlatformCandidateIntent(e, expected, runtime, now, false); err != nil {
		return zero, err
	}
	issued := time.Now().UnixMilli()
	if issued < now-1000 || issued > now+1000 {
		return zero, errAppPlatformLeaseConflict
	}
	expires := min(issued+30000, expected.leaseUntilMS-1000)
	if expires <= issued {
		return zero, errAppPlatformLeaseConflict
	}
	result.command.IssuedMS, result.command.ExpiresMS = strconv.FormatInt(issued, 10), strconv.FormatInt(expires, 10)
	key := ed25519.NewKeyFromSeed(seed)
	defer clear(key)
	result.raw, result.signature, err = control.SignCandidate(result.command, key, result.authority, issued)
	if err != nil {
		return zero, err
	}
	now, err = finalPlatformOperationLeaseCheck(e, facts)
	if err != nil {
		return zero, err
	}
	if err := platformVerifyInstallAuthorityAtCommit(e, expected, trust, now); err != nil {
		return zero, err
	}
	if err := tx.Commit(); err != nil {
		return zero, err
	}
	if _, err := control.VerifyCandidate(result.raw, result.signature, result.authority, time.Now().UnixMilli()); err != nil {
		return zero, err
	}
	return result, nil
}
