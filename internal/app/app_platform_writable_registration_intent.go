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
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/journal"
)

type platformWritableRegistrationIntent struct {
	start            platformWritableStartIntent
	command          control.WritableBackendRegistrationCommand
	raw, signature   []byte
	registration     appplatform.BackendRegistration
	applied          bool
	receipt          journal.WritableBackendRegistrationReceipt
	created, updated int64
}

func freshPlatformWritableRegistrationSource(i platformWritableStartIntent) bool {
	return freshPlatformWritableRegistrationSourceAt(i, time.Now().UnixMilli())
}

func freshPlatformWritableRegistrationSourceAt(i platformWritableStartIntent, now int64) bool {
	observed := i.writer.activationObserved
	return observed.Valid && observed.Int64 <= now && now-observed.Int64 <= 3000
}

func platformWritableRegistrationReceiptMatches(r journal.WritableBackendRegistrationReceipt, i platformWritableRegistrationIntent, now int64) bool {
	raw, _ := json.Marshal(r)
	if _, err := journal.ParseWritableBackendRegistrationReceipt(raw); err != nil {
		return false
	}
	c, s := i.command, i.start
	sourceRevision, _ := strconv.ParseInt(s.running.Claim.ControlRevision, 10, 64)
	if s.running.RecoveryControlRevision != "" {
		sourceRevision, _ = strconv.ParseInt(s.running.RecoveryControlRevision, 10, 64)
	}
	revision, _ := strconv.ParseInt(r.Claim.ControlRevision, 10, 64)
	recovery := revision
	if r.RecoveryControlRevision != "" {
		recovery, _ = strconv.ParseInt(r.RecoveryControlRevision, 10, 64)
	}
	claimed, _ := strconv.ParseInt(r.Claim.ClaimedMS, 10, 64)
	settled, _ := strconv.ParseInt(r.SettledMS, 10, 64)
	return r.Claim.EffectID == c.EffectID && r.Claim.PayloadSHA256 == c.PayloadSHA256 && r.Claim.SourceSHA256 == c.RunningReceiptSHA256 && r.Claim.DocumentSHA256 == c.DocumentSHA256 && r.Claim.NativeID == s.running.NativeID && revision >= sourceRevision && revision <= s.writer.runtime.controlRevision && recovery <= s.writer.runtime.controlRevision && claimed+1000 >= i.created && settled <= now+1000 && settled <= time.Now().UnixMilli()
}

// The only continuation that accepts a fourth effect validates it here in full.
// No key/document bytes are persisted in control rows or signed envelopes.
func loadPlatformWritableRegistration(e migExec, lease platformOperationLease, runtime platformRuntimeRegistration, seed, dek []byte, now int64, create bool) (platformWritableRegistrationIntent, bool, error) {
	var zero platformWritableRegistrationIntent
	if !validPlatformWritableActivateStage(lease) || len(seed) != ed25519.SeedSize {
		return zero, false, errAppPlatformStageEvidence
	}
	start, _, err := loadPlatformWritableStartSource(e, lease, runtime, seed, now, false, true, true)
	if err != nil {
		return zero, false, err
	}
	var effect, kind, digest, runtimeID, epoch, state, receipt string
	var admitted, observed, created, updated int64
	err = e.queryRow(`SELECT effect_id,kind,payload_sha256,runtime_id,controller_epoch,admitted_fence,observed_fence,state,receipt_json,created_ms,updated_ms FROM app_platform_operation_effects WHERE operation_id=? AND stage='activate' AND effect_number=1`, lease.id).Scan(&effect, &kind, &digest, &runtimeID, &epoch, &admitted, &observed, &state, &receipt, &created, &updated)
	fresh := errors.Is(err, sql.ErrNoRows)
	var count int
	if err := e.queryRow(`SELECT COUNT(*) FROM app_platform_operation_effects WHERE operation_id=?`, lease.id).Scan(&count); err != nil {
		return zero, false, err
	}
	if fresh {
		if !create || count != 3 || !freshPlatformWritableRegistrationSource(start) {
			return zero, false, errAppPlatformStageEvidence
		}
		effect, err = platformNewUUID()
		if err != nil {
			return zero, false, err
		}
		created, updated = now, now
	} else if err != nil {
		return zero, false, err
	} else if count != 4 || !platformValidUUID(effect) || kind != "activate" || runtimeID != runtime.id || epoch != lease.controllerEpoch || admitted != lease.fence || created < start.effectUpdated || updated < created || updated > now {
		return zero, false, errAppPlatformStageEvidence
	}
	source := platformManagedSource{operationID: lease.id, installationID: lease.installationID, epoch: lease.controllerEpoch, generation: lease.snapshot.Generation, fence: lease.fence}
	registration, err := platformBackendRegistrationForRelease(e, source, runtime, start.writer.descriptor, start.writer.data.ReleaseSHA256, dek)
	if err != nil {
		return zero, false, err
	}
	failed := true
	defer func() {
		if failed {
			registration.Close()
		}
	}()
	document, err := registration.Document()
	if err != nil {
		return zero, false, err
	}
	defer clear(document)
	running, _ := json.Marshal(start.running)
	c := control.WritableBackendRegistrationCommand{Format: control.WritableBackendRegistrationFormat, EffectID: effect, CandidateRaw: bytes.Clone(start.raw), CandidateSignature: bytes.Clone(start.signature), RunningReceiptSHA256: platformDigest(running), DocumentSHA256: platformDigest(document)}
	c.PayloadSHA256, err = control.WritableBackendRegistrationLogicalDigest(c)
	if err != nil {
		return zero, false, err
	}
	result := platformWritableRegistrationIntent{start: start, command: c, registration: registration, created: created, updated: updated}
	if fresh {
		if _, err := e.exec(`INSERT INTO app_platform_operation_effects(operation_id,stage,effect_number,effect_id,kind,payload_sha256,runtime_id,controller_epoch,admitted_fence,observed_fence,state,receipt_json,created_ms,updated_ms) VALUES(?,'activate',1,?,'activate',?,?,?,?,0,'prepared','{}',?,?)`, lease.id, effect, c.PayloadSHA256, runtime.id, lease.controllerEpoch, lease.fence, now, now); err != nil {
			return zero, false, err
		}
	} else {
		if digest != c.PayloadSHA256 {
			return zero, false, errAppPlatformStageEvidence
		}
		switch state {
		case "prepared":
			if observed != 0 || receipt != "{}" || updated != created {
				return zero, false, errAppPlatformStageEvidence
			}
		case "applied":
			result.receipt, err = journal.ParseWritableBackendRegistrationReceipt([]byte(receipt))
			settled, _ := strconv.ParseInt(result.receipt.SettledMS, 10, 64)
			if err != nil || observed != lease.fence || settled > updated+1000 || !platformWritableRegistrationReceiptMatches(result.receipt, result, now) {
				return zero, false, errAppPlatformStageEvidence
			}
			result.applied = true
		default:
			return zero, false, errAppPlatformStageEvidence
		}
	}
	key := ed25519.NewKeyFromSeed(seed)
	defer clear(key)
	result.raw, result.signature, err = control.SignWritableBackendRegistration(c, key, start.writer.authority, time.Now().UnixMilli())
	if err != nil {
		return zero, false, err
	}
	failed = false
	return result, fresh, nil
}

func samePlatformWritableRegistrationIntent(a, b platformWritableRegistrationIntent) bool {
	ar, _ := json.Marshal(a.receipt)
	br, _ := json.Marshal(b.receipt)
	return samePlatformWritableStartIntent(a.start, b.start) && a.command.Format == b.command.Format && a.command.EffectID == b.command.EffectID && a.command.PayloadSHA256 == b.command.PayloadSHA256 && a.command.DocumentSHA256 == b.command.DocumentSHA256 && a.command.RunningReceiptSHA256 == b.command.RunningReceiptSHA256 && a.applied == b.applied && a.created == b.created && a.updated == b.updated && bytes.Equal(ar, br)
}
func clonePlatformWritableRegistrationIntent(i platformWritableRegistrationIntent) platformWritableRegistrationIntent {
	i.start = clonePlatformWritableStartIntent(i.start)
	i.raw, i.signature = bytes.Clone(i.raw), bytes.Clone(i.signature)
	i.command.CandidateRaw, i.command.CandidateSignature = bytes.Clone(i.command.CandidateRaw), bytes.Clone(i.command.CandidateSignature)
	return i
}

func (s *Server) preparePlatformWritableRegistration(ctx context.Context, lease platformOperationLease) (platformWritableRegistrationIntent, error) {
	var zero platformWritableRegistrationIntent
	if !validPlatformWritableActivateStage(lease) {
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
	result, fresh, err := loadPlatformWritableRegistration(e, lease, runtime, seed, dek, facts.now, true)
	if err != nil {
		return zero, err
	}
	failed := true
	defer func() {
		if failed {
			result.registration.Close()
		}
	}()
	if fresh {
		detail, _ := json.Marshal(struct{ EffectID, ResourceID, DocumentSHA256 string }{result.command.EffectID, result.start.writer.resourceID, result.command.DocumentSHA256})
		if _, err := e.exec(`INSERT INTO audit_log(at,actor,actor_ou,action,target_type,target_id,detail,ip) VALUES(?,'system',0,'app_platform.writable_registration_intent','app_platform_operation',?,?,'')`, time.UnixMilli(facts.now).UTC().Format(time.RFC3339), lease.id, string(detail)); err != nil {
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
	if fresh && !freshPlatformWritableRegistrationSource(result.start) {
		return zero, errAppPlatformStageEvidence
	}
	current, _, err := loadPlatformWritableRegistration(e, lease, runtime, seed, dek, now, false)
	if err != nil {
		return zero, err
	}
	defer current.registration.Close()
	if !samePlatformWritableRegistrationIntent(result, current) {
		return zero, errAppPlatformStageEvidence
	}
	if err := platformVerifyInstallAuthorityAtCommit(e, lease, trust, now); err != nil {
		return zero, err
	}
	if _, err := control.VerifyWritableBackendRegistration(result.raw, result.signature, result.start.writer.authority, time.Now().UnixMilli()); err != nil {
		return zero, err
	}
	if err := tx.Commit(); err != nil {
		return zero, err
	}
	failed = false
	return result, nil
}
