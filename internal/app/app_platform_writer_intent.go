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
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/driver"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/journal"
)

type platformWriterIntent struct {
	namespaceID, resourceID, effectID                                                                 string
	dataUpdated, namespaceUpdated, namespaceRevision, effectCreated, effectUpdated, activationCreated int64
	runtime                                                                                           platformRuntimeRegistration
	authority                                                                                         control.Authority
	command                                                                                           control.DataWriterCommand
	raw, signature, descriptor                                                                        []byte
	data                                                                                              journal.DataPreparationReceipt
	dataSHA                                                                                           string
	applied                                                                                           bool
	receipt                                                                                           journal.DataWriterReceipt
}

func validPlatformWriterStage(lease platformOperationLease) bool {
	return lease.snapshot.Action == appplatform.ActionInstall && lease.snapshot.Managed && lease.snapshot.Generation == 1 && lease.snapshot.Stage == appplatform.StageStart
}

// This source path deliberately accepts only the separately reviewed writable
// contract. The old sealed candidate path retains its exact two/three-effect rules.
// This initial pre-start writer source permits exactly preparation + this writer.
func loadPlatformWriterIntent(e migExec, lease platformOperationLease, runtime platformRuntimeRegistration, seed []byte, now int64, create bool) (platformWriterIntent, bool, error) {
	var zero, result platformWriterIntent
	profile, err := platformInstallProfileInTransaction(e, lease.id)
	if err != nil || profile != platformWritableInstallFormat || !validPlatformWriterStage(lease) {
		return zero, false, errAppPlatformStageEvidence
	}
	target, err := platformManagedTargetForOperation(e, lease.id)
	if err != nil || target.RuntimeID != runtime.id || target.Profile.WritableSandboxProfile != control.WritableCandidateSandboxProfile {
		return zero, false, errAppPlatformRuntimeConflict
	}
	var release string
	if err := e.queryRow(`SELECT p.target_release_sha256,r.descriptor FROM app_platform_operations o JOIN app_platform_review_plans p ON p.id=o.review_plan_id JOIN app_platform_releases r ON r.sha256=p.target_release_sha256 WHERE o.id=?`, lease.id).Scan(&release, &result.descriptor); err != nil {
		return zero, false, err
	}
	parsed, err := appplatform.ParseDescriptor(result.descriptor)
	if err != nil || parsed.SHA256 != release || parsed.Manifest.Backend == nil || len(parsed.Manifest.Capabilities.Required) != 0 {
		return zero, false, errAppPlatformStageEvidence
	}
	var origin, owner, state string
	var schema, quota, used, writer, revision, created, updated int64
	var retained, deleted sql.NullInt64
	var namespaces, effects, candidates, grants int
	if err := e.queryRow(`SELECT (SELECT COUNT(*) FROM app_platform_data_namespaces WHERE origin_installation_id=? OR owner_installation_id=?),(SELECT COUNT(*) FROM app_platform_operation_effects WHERE operation_id=?),(SELECT COUNT(*) FROM app_platform_activations WHERE installation_id=?),(SELECT COUNT(*) FROM app_platform_access_grants WHERE installation_id=?)`, lease.installationID, lease.installationID, lease.id, lease.installationID, lease.installationID).Scan(&namespaces, &effects, &candidates, &grants); err != nil {
		return zero, false, err
	}
	if namespaces != 1 || grants != 0 {
		return zero, false, errAppPlatformStageEvidence
	}
	if err := e.queryRow(`SELECT id,origin_installation_id,owner_installation_id,state,data_format,quota_bytes,observed_bytes,writer_fence,revision,retained_ms,deleted_ms,created_ms,updated_ms FROM app_platform_data_namespaces WHERE owner_installation_id=?`, lease.installationID).Scan(&result.namespaceID, &origin, &owner, &state, &schema, &quota, &used, &writer, &revision, &retained, &deleted, &created, &updated); err != nil {
		return zero, false, err
	}
	if !platformValidUUID(result.namespaceID) || origin != lease.installationID || owner != lease.installationID || state != "active" || schema != int64(parsed.Manifest.Data.Schema) || quota != 256<<20 || used < 0 || used > quota || writer != 1 || (revision != 2 && revision != 3) || retained.Valid || deleted.Valid || created < 0 || updated < created || updated > now {
		return zero, false, errAppPlatformStageEvidence
	}
	namespaceUpdated := updated
	result.namespaceUpdated, result.namespaceRevision = updated, revision
	var dataEffect, kind, digest, runtimeID, epoch, receipt string
	var admitted, observed int64
	if err := e.queryRow(`SELECT effect_id,kind,payload_sha256,runtime_id,controller_epoch,admitted_fence,observed_fence,state,receipt_json,created_ms,updated_ms FROM app_platform_operation_effects WHERE operation_id=? AND stage='prepare_data' AND effect_number=1`, lease.id).Scan(&dataEffect, &kind, &digest, &runtimeID, &epoch, &admitted, &observed, &state, &receipt, &created, &updated); err != nil {
		return zero, false, err
	}
	if !platformValidUUID(dataEffect) || kind != "prepare-data" || runtimeID != runtime.id || epoch != lease.controllerEpoch || admitted != lease.fence || observed != lease.fence || state != "applied" || created < 0 || updated < created || updated > now {
		return zero, false, errAppPlatformStageEvidence
	}
	dataUpdated := updated
	result.dataUpdated = updated
	if revision == 2 && namespaceUpdated != dataUpdated {
		return zero, false, errAppPlatformStageEvidence
	}
	dataCommand, err := platformDataCommand(e, lease, runtime, result.namespaceID, dataEffect)
	if err != nil || dataCommand.PayloadSHA256 != digest {
		return zero, false, errAppPlatformStageEvidence
	}
	result.data, err = journal.ParseDataPreparationReceipt([]byte(receipt))
	if err != nil {
		return zero, false, err
	}
	canonical, _ := json.Marshal(result.data)
	original := dataCommand
	original.ControlRevision = result.data.ControlRevision
	originalRevision, _ := strconv.ParseInt(original.ControlRevision, 10, 64)
	if string(canonical) != receipt || originalRevision > runtime.controlRevision || !result.data.MatchesCommand(original) || result.data.Claim.RootFilesystemUUID != target.Profile.RootFilesystemUUID || result.data.Claim.RootInode != target.Profile.RootInode {
		return zero, false, errAppPlatformStageEvidence
	}
	result.dataSHA = platformDigest(canonical)
	fresh := effects == 1 && candidates == 0
	if fresh {
		if !create || revision != 2 || strconv.FormatInt(used, 10) != result.data.UsedBytes {
			return zero, false, errAppPlatformStageEvidence
		}
		result.resourceID, err = platformNewUUID()
		if err != nil {
			return zero, false, err
		}
		result.effectCreated, result.effectUpdated, result.activationCreated = now, now, now
		result.effectID, err = platformNewUUID()
		if err != nil {
			return zero, false, err
		}
	} else {
		if effects != 2 || candidates != 1 {
			return zero, false, errAppPlatformStageEvidence
		}
		var storedRelease, namespace, readiness string
		var generation, policy, format int64
		var observedAt sql.NullInt64
		if err := e.queryRow(`SELECT id,release_sha256,generation,policy_revision,data_namespace_id,data_format,state,readiness_evidence,observed_ms,created_ms,updated_ms FROM app_platform_activations WHERE installation_id=?`, lease.installationID).Scan(&result.resourceID, &storedRelease, &generation, &policy, &namespace, &format, &state, &readiness, &observedAt, &created, &updated); err != nil {
			return zero, false, err
		}
		if !platformValidUUID(result.resourceID) || storedRelease != release || namespace != result.namespaceID || generation != 1 || policy != 1 || format != schema || state != "candidate" || readiness != "{}" || observedAt.Valid || created < 0 || updated != created || created > now {
			return zero, false, errAppPlatformStageEvidence
		}
		result.activationCreated = created
		if err := e.queryRow(`SELECT effect_id,kind,payload_sha256,runtime_id,controller_epoch,admitted_fence,observed_fence,state,receipt_json,created_ms,updated_ms FROM app_platform_operation_effects WHERE operation_id=? AND stage='prepare_data' AND effect_number=2`, lease.id).Scan(&result.effectID, &kind, &digest, &runtimeID, &epoch, &admitted, &observed, &state, &receipt, &created, &updated); err != nil {
			return zero, false, err
		}
		if !platformValidUUID(result.effectID) || kind != "activate" || runtimeID != runtime.id || epoch != lease.controllerEpoch || admitted != lease.fence || created < dataUpdated || updated < created || updated > now {
			return zero, false, errAppPlatformStageEvidence
		}
		result.effectCreated, result.effectUpdated = created, updated
		if created != result.activationCreated {
			return zero, false, errAppPlatformStageEvidence
		}
		switch state {
		case "prepared":
			if observed != 0 || receipt != "{}" || updated != created || revision != 2 || strconv.FormatInt(used, 10) != result.data.UsedBytes {
				return zero, false, errAppPlatformStageEvidence
			}
		case "applied":
			result.receipt, err = journal.ParseDataWriterReceipt([]byte(receipt))
			if err != nil || observed != lease.fence || revision != 3 || namespaceUpdated != updated || strconv.FormatInt(used, 10) != result.receipt.Native.UsedBytes {
				return zero, false, errAppPlatformStageEvidence
			}
			settled, _ := strconv.ParseInt(result.receipt.SettledMS, 10, 64)
			claimed, _ := strconv.ParseInt(result.receipt.Claim.ClaimedMS, 10, 64)
			if settled > updated || claimed < created {
				return zero, false, errAppPlatformStageEvidence
			}
			result.applied = true
		default:
			return zero, false, errAppPlatformStageEvidence
		}
	}
	issued := time.Now().UnixMilli()
	if issued < now-1000 || issued > now+1000 {
		return zero, false, errAppPlatformLeaseConflict
	}
	expires := min(issued+30000, lease.leaseUntilMS-1000)
	if expires <= issued {
		return zero, false, errAppPlatformLeaseConflict
	}
	dataCommand.IssuedMS, dataCommand.ExpiresMS = strconv.FormatInt(issued, 10), strconv.FormatInt(expires, 10)
	key := ed25519.NewKeyFromSeed(seed)
	defer clear(key)
	result.runtime = runtime
	result.authority = platformRuntimeAuthority(runtime)
	dataRaw, dataSignature, err := control.Sign(dataCommand, key, result.authority, issued)
	if err != nil {
		return zero, false, err
	}
	if result.resourceID == dataEffect || result.resourceID == result.namespaceID || result.effectID == result.namespaceID {
		return zero, false, errAppPlatformStageEvidence
	}
	result.command = control.DataWriterCommand{Format: control.DataWriterFormat, EffectID: result.effectID, DataRaw: dataRaw, DataSignature: dataSignature, DataReceiptSHA256: result.dataSHA, ResourceID: result.resourceID, Platform: target.Profile.Platform, SandboxProfile: control.DataWriterSandboxProfile}
	result.command.PayloadSHA256, err = control.DataWriterLogicalDigest(result.command)
	if err != nil || (!fresh && result.command.PayloadSHA256 != digest) {
		return zero, false, errAppPlatformStageEvidence
	}
	if result.applied && !platformWriterReceiptMatches(result.receipt, result, lease, now) {
		return zero, false, errAppPlatformStageEvidence
	}
	if fresh {
		if _, err := e.exec(`INSERT INTO app_platform_activations(id,installation_id,release_sha256,generation,policy_revision,data_namespace_id,data_format,state,readiness_evidence,observed_ms,created_ms,updated_ms) VALUES(?,?,?,1,1,?,?,'candidate','{}',NULL,?,?)`, result.resourceID, lease.installationID, release, result.namespaceID, schema, now, now); err != nil {
			return zero, false, err
		}
		if _, err := e.exec(`INSERT INTO app_platform_operation_effects(operation_id,stage,effect_number,effect_id,kind,payload_sha256,runtime_id,controller_epoch,admitted_fence,observed_fence,state,receipt_json,created_ms,updated_ms) VALUES(?,'prepare_data',2,?,'activate',?,?,?,?,0,'prepared','{}',?,?)`, lease.id, result.effectID, result.command.PayloadSHA256, runtime.id, lease.controllerEpoch, lease.fence, now, now); err != nil {
			return zero, false, err
		}
	}
	result.raw, result.signature, err = control.SignDataWriter(result.command, key, result.authority, issued)
	if err != nil {
		return zero, false, err
	}
	return result, fresh, nil
}

func platformWriterReceiptMatches(r journal.DataWriterReceipt, i platformWriterIntent, lease platformOperationLease, now int64) bool {
	n, d, c := r.Native, i.data, i.command
	raw, _ := json.Marshal(r)
	if _, err := journal.ParseDataWriterReceipt(raw); err != nil {
		return false
	}
	revision, _ := strconv.ParseInt(r.ControlRevision, 10, 64)
	sourceRevision, _ := strconv.ParseInt(d.ControlRevision, 10, 64)
	settled, _ := strconv.ParseInt(r.SettledMS, 10, 64)
	observed, _ := strconv.ParseInt(n.ObservedMS, 10, 64)
	return r.Claim.EffectID == i.effectID && r.Claim.PayloadSHA256 == c.PayloadSHA256 && r.Claim.DataReceiptSHA256 == i.dataSHA && r.Claim.ResourceID == i.resourceID && r.Claim.AdmittedFence == strconv.FormatInt(lease.fence, 10) && revision >= sourceRevision && revision <= i.runtime.controlRevision && settled <= now && observed <= now && n.Profile == control.DataWriterSandboxProfile && n.SeccompSHA256 == driver.DataWriterPolicySHA256() && n.Platform == c.Platform && n.RootFilesystemUUID == d.Claim.RootFilesystemUUID && n.RootInode == d.Claim.RootInode && n.ProjectID == d.ProjectID && n.Device == d.Device && n.NamespaceInode == d.NamespaceInode && n.QuotaBytes == d.QuotaBytes && n.QuotaInodes == d.QuotaInodes
}

func (s *Server) preparePlatformInstallWriter(ctx context.Context, expected platformOperationLease) (platformWriterIntent, error) {
	var zero platformWriterIntent
	if !validPlatformWriterStage(expected) {
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
	result, fresh, err := loadPlatformWriterIntent(e, expected, runtime, seed, facts.now, true)
	if err != nil {
		return zero, err
	}
	if fresh {
		detail, _ := json.Marshal(struct{ ResourceID, EffectID string }{result.resourceID, result.effectID})
		if _, err := e.exec(`INSERT INTO audit_log(at,actor,actor_ou,action,target_type,target_id,detail,ip) VALUES(?,'system',0,'app_platform.writer_intent','app_platform_operation',?,?,'')`, time.UnixMilli(facts.now).UTC().Format(time.RFC3339), expected.id, string(detail)); err != nil {
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
	again, _, err := loadPlatformWriterIntent(e, expected, runtime, seed, now, false)
	if err != nil || !samePlatformWriterIntent(result, again) {
		return zero, errAppPlatformStageEvidence
	}
	if err := platformVerifyInstallAuthorityAtCommit(e, expected, trust, now); err != nil {
		return zero, err
	}
	if err := tx.Commit(); err != nil {
		return zero, err
	}
	if _, err := control.VerifyDataWriter(result.raw, result.signature, result.authority, time.Now().UnixMilli()); err != nil {
		return zero, errAppPlatformStageEvidence
	}
	return result, nil
}
func samePlatformWriterIntent(a, b platformWriterIntent) bool {
	return a.namespaceID == b.namespaceID && a.resourceID == b.resourceID && a.effectID == b.effectID && samePlatformWriterCommand(a.command, b.command) && a.data == b.data && a.dataSHA == b.dataSHA && a.dataUpdated == b.dataUpdated && a.namespaceUpdated == b.namespaceUpdated && a.namespaceRevision == b.namespaceRevision && a.effectCreated == b.effectCreated && a.effectUpdated == b.effectUpdated && a.activationCreated == b.activationCreated && a.applied == b.applied && a.receipt == b.receipt && samePlatformManagedRuntime(a.runtime, b.runtime) && bytes.Equal(a.descriptor, b.descriptor)
}

func samePlatformWriterCommand(a, b control.DataWriterCommand) bool {
	return a.Format == b.Format && a.EffectID == b.EffectID && a.PayloadSHA256 == b.PayloadSHA256 && a.DataReceiptSHA256 == b.DataReceiptSHA256 && a.ResourceID == b.ResourceID && a.Platform == b.Platform && a.SandboxProfile == b.SandboxProfile
}
func clonePlatformWriterIntent(i platformWriterIntent) platformWriterIntent {
	i.raw, i.signature, i.descriptor = bytes.Clone(i.raw), bytes.Clone(i.signature), bytes.Clone(i.descriptor)
	i.command.DataRaw, i.command.DataSignature = bytes.Clone(i.command.DataRaw), bytes.Clone(i.command.DataSignature)
	i.authority.PublicKey, i.runtime.publicKey = bytes.Clone(i.authority.PublicKey), bytes.Clone(i.runtime.publicKey)
	return i
}
