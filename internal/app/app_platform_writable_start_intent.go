package app

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/driver"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/journal"
)

type platformWritableStartIntent struct {
	writer                       platformWriterIntent
	command                      control.WritableCandidateCommand
	raw, signature               []byte
	effectCreated, effectUpdated int64
	applied                      bool
	running                      journal.WritableCandidateRunningReceipt
}

func validPlatformWritableProbeStage(lease platformOperationLease) bool {
	return lease.snapshot.Action == appplatform.ActionInstall && lease.snapshot.Managed && lease.snapshot.Generation == 1 && lease.snapshot.Stage == appplatform.StageProbe
}

// The only extension to the pre-start writer source is this exact start effect.
// Registration/readiness history stays forbidden until separately implemented.
func loadPlatformWritableStartIntent(e migExec, lease platformOperationLease, runtime platformRuntimeRegistration, seed []byte, now int64, create bool) (platformWritableStartIntent, bool, error) {
	var zero, result platformWritableStartIntent
	if !validPlatformWriterStage(lease) && !validPlatformWritableProbeStage(lease) {
		return zero, false, errAppPlatformStageEvidence
	}
	writer, _, err := loadPlatformWriterIntentMode(e, lease, runtime, seed, now, false, true)
	if err != nil || !writer.applied {
		return zero, false, errAppPlatformStageEvidence
	}
	result.writer = writer
	var effect, kind, digest, runtimeID, epoch, state, receipt string
	var admitted, observed, created, updated int64
	err = e.queryRow(`SELECT effect_id,kind,payload_sha256,runtime_id,controller_epoch,admitted_fence,observed_fence,state,receipt_json,created_ms,updated_ms FROM app_platform_operation_effects WHERE operation_id=? AND stage='start' AND effect_number=1`, lease.id).Scan(&effect, &kind, &digest, &runtimeID, &epoch, &admitted, &observed, &state, &receipt, &created, &updated)
	fresh := errors.Is(err, sql.ErrNoRows)
	var count int
	if err := e.queryRow(`SELECT COUNT(*) FROM app_platform_operation_effects WHERE operation_id=?`, lease.id).Scan(&count); err != nil {
		return zero, false, err
	}
	if fresh {
		if !create || !validPlatformWriterStage(lease) || count != 2 {
			return zero, false, errAppPlatformStageEvidence
		}
		effect, err = platformNewUUID()
		if err != nil {
			return zero, false, err
		}
		created, updated = now, now
	} else if err != nil {
		return zero, false, err
	} else {
		if count != 3 || !platformValidUUID(effect) || kind != "start-candidate" || runtimeID != runtime.id || epoch != lease.controllerEpoch || admitted != lease.fence || created < writer.effectUpdated || updated < created || updated > now {
			return zero, false, errAppPlatformStageEvidence
		}
		switch state {
		case "prepared":
			if !validPlatformWriterStage(lease) || observed != 0 || receipt != "{}" || updated != created {
				return zero, false, errAppPlatformStageEvidence
			}
		case "applied":
			if !validPlatformWritableProbeStage(lease) || observed != lease.fence {
				return zero, false, errAppPlatformStageEvidence
			}
			result.running, err = journal.ParseWritableCandidateRunningReceipt([]byte(receipt))
			if err != nil {
				return zero, false, err
			}
			settled, _ := strconv.ParseInt(result.running.SettledMS, 10, 64)
			if settled > updated+1000 {
				return zero, false, errAppPlatformStageEvidence
			}
			result.applied = true
		default:
			return zero, false, errAppPlatformStageEvidence
		}
	}
	result.effectCreated, result.effectUpdated = created, updated
	writerReceipt, _ := json.Marshal(writer.receipt)
	c := control.WritableCandidateCommand{Format: control.WritableCandidateFormat, EffectID: effect, WriterRaw: bytes.Clone(writer.raw), WriterSignature: bytes.Clone(writer.signature), WriterReceiptSHA256: platformDigest(writerReceipt), SandboxProfile: control.WritableCandidateSandboxProfile}
	c.PayloadSHA256, err = control.WritableCandidateLogicalDigest(c)
	if err != nil || (!fresh && c.PayloadSHA256 != digest) {
		return zero, false, errAppPlatformStageEvidence
	}
	result.command = c
	key := ed25519.NewKeyFromSeed(seed)
	defer clear(key)
	result.raw, result.signature, err = control.SignWritableCandidate(c, key, writer.authority, time.Now().UnixMilli())
	if err != nil {
		return zero, false, err
	}
	if result.applied && !platformWritableRunningMatches(result.running, result, lease, now) {
		return zero, false, errAppPlatformStageEvidence
	}
	if fresh {
		if _, err := e.exec(`INSERT INTO app_platform_operation_effects(operation_id,stage,effect_number,effect_id,kind,payload_sha256,runtime_id,controller_epoch,admitted_fence,observed_fence,state,receipt_json,created_ms,updated_ms) VALUES(?,'start',1,?,'start-candidate',?,?,?,?,0,'prepared','{}',?,?)`, lease.id, effect, c.PayloadSHA256, runtime.id, lease.controllerEpoch, lease.fence, now, now); err != nil {
			return zero, false, err
		}
	}
	return result, fresh, nil
}

// Historical receipt validation only. Native/transport provenance is mandatory
// separately; parsed JSON does not prove a process exists or is healthy.
func platformWritableRunningMatches(r journal.WritableCandidateRunningReceipt, i platformWritableStartIntent, lease platformOperationLease, now int64) bool {
	encoded, _ := json.Marshal(r)
	if _, err := journal.ParseWritableCandidateRunningReceipt(encoded); err != nil {
		return false
	}
	w, d, c, m := i.writer, i.writer.data, i.command, r.Claim
	if m.Preflight == nil {
		return false
	}
	p := m.Preflight.Native
	revision, _ := strconv.ParseInt(m.ControlRevision, 10, 64)
	writerRevision, _ := strconv.ParseInt(w.receipt.ControlRevision, 10, 64)
	recoveryRevision := revision
	if r.RecoveryControlRevision != "" {
		recoveryRevision, _ = strconv.ParseInt(r.RecoveryControlRevision, 10, 64)
	}
	claimed, _ := strconv.ParseInt(m.ClaimedMS, 10, 64)
	settled, _ := strconv.ParseInt(r.SettledMS, 10, 64)
	if claimed+1000 < i.effectCreated || settled > now+1000 || settled > time.Now().UnixMilli() || revision < writerRevision || revision > w.runtime.controlRevision || recoveryRevision > w.runtime.controlRevision {
		return false
	}
	owner := struct{ Format, Deployment, Epoch, Effect, Resource, Installation, Operation, Namespace, Release, Generation, Fence, Writer, Payload, DataReceipt, WriterReceipt string }{"portal.runner.writable-candidate-ownership/1-dev.1", w.runtime.deploymentID, lease.controllerEpoch, c.EffectID, w.resourceID, lease.installationID, lease.id, w.namespaceID, d.ReleaseSHA256, "1", strconv.FormatInt(lease.fence, 10), "1", c.PayloadSHA256, w.dataSHA, c.WriterReceiptSHA256}
	ownerRaw, _ := json.Marshal(owner)
	return m.EffectID == c.EffectID && m.PayloadSHA256 == c.PayloadSHA256 && m.ResourceID == w.resourceID && m.OwnershipSHA256 == platformDigest(ownerRaw) && m.DataReceiptSHA256 == w.dataSHA && m.WriterReceiptSHA256 == c.WriterReceiptSHA256 && m.AdmittedFence == strconv.FormatInt(lease.fence, 10) && m.SandboxProfile == control.WritableCandidateSandboxProfile && m.Preflight.SeccompSHA256 == w.receipt.Native.SeccompSHA256 && m.Preflight.SeccompSHA256 == driver.DataWriterPolicySHA256() && p.ImageID == w.receipt.Native.ImageID && p.Platform == w.command.Platform && p.RootFilesystemUUID == d.Claim.RootFilesystemUUID && p.RootInode == d.Claim.RootInode && p.ProjectID == d.ProjectID && p.Device == d.Device && p.NamespaceInode == d.NamespaceInode && p.QuotaBytes == d.QuotaBytes && p.QuotaInodes == d.QuotaInodes
}

func samePlatformWritableStartIntent(a, b platformWritableStartIntent) bool {
	ar, _ := json.Marshal(a.running)
	br, _ := json.Marshal(b.running)
	return samePlatformWriterIntent(a.writer, b.writer) && a.command.Format == b.command.Format && a.command.EffectID == b.command.EffectID && a.command.PayloadSHA256 == b.command.PayloadSHA256 && a.command.WriterReceiptSHA256 == b.command.WriterReceiptSHA256 && a.command.SandboxProfile == b.command.SandboxProfile && a.effectCreated == b.effectCreated && a.effectUpdated == b.effectUpdated && a.applied == b.applied && bytes.Equal(ar, br)
}
func clonePlatformWritableStartIntent(i platformWritableStartIntent) platformWritableStartIntent {
	i.writer = clonePlatformWriterIntent(i.writer)
	i.raw, i.signature = bytes.Clone(i.raw), bytes.Clone(i.signature)
	i.command.WriterRaw, i.command.WriterSignature = bytes.Clone(i.command.WriterRaw), bytes.Clone(i.command.WriterSignature)
	raw, _ := json.Marshal(i.running)
	var running journal.WritableCandidateRunningReceipt
	json.Unmarshal(raw, &running)
	i.running = running
	return i
}
func (s *Server) preparePlatformWritableStart(ctx context.Context, lease platformOperationLease) (platformWritableStartIntent, error) {
	var zero platformWritableStartIntent
	if !validPlatformWriterStage(lease) && !validPlatformWritableProbeStage(lease) {
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
	trust, err := loadPlatformPublisherTrust(e)
	if err != nil {
		return zero, err
	}
	if err := validatePlatformInstallAuthority(e, lease, trust, facts.now); err != nil {
		return zero, err
	}
	result, fresh, err := loadPlatformWritableStartIntent(e, lease, runtime, seed, facts.now, true)
	if err != nil {
		return zero, err
	}
	if fresh {
		detail, _ := json.Marshal(struct{ ResourceID, EffectID string }{result.writer.resourceID, result.command.EffectID})
		if _, err := e.exec(`INSERT INTO audit_log(at,actor,actor_ou,action,target_type,target_id,detail,ip) VALUES(?,'system',0,'app_platform.writable_start_intent','app_platform_operation',?,?,'')`, time.UnixMilli(facts.now).UTC().Format(time.RFC3339), lease.id, string(detail)); err != nil {
			return zero, err
		}
	}
	now, err := finalPlatformOperationLeaseCheck(e, facts)
	if err != nil {
		return zero, err
	}
	if err := validatePlatformInstallAuthority(e, lease, trust, now); err != nil {
		return zero, err
	}
	again, _, err := loadPlatformWritableStartIntent(e, lease, runtime, seed, now, false)
	if err != nil || !samePlatformWritableStartIntent(result, again) {
		return zero, errAppPlatformStageEvidence
	}
	if err := platformVerifyInstallAuthorityAtCommit(e, lease, trust, now); err != nil {
		return zero, err
	}
	if err := tx.Commit(); err != nil {
		return zero, err
	}
	if _, err := control.VerifyWritableCandidate(result.raw, result.signature, result.writer.authority, time.Now().UnixMilli()); err != nil {
		return zero, errAppPlatformStageEvidence
	}
	return result, nil
}
