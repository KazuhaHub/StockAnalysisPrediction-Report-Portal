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

type platformBackendDeliveryIntent struct {
	candidate      platformCandidateIntent
	command        control.BackendRegistrationCommand
	raw, signature []byte
	registration   appplatform.BackendRegistration
	applied        bool
	receipt        journal.BackendRegistrationReceipt
}

func validPlatformBackendDeliveryStage(lease platformOperationLease) bool {
	return lease.snapshot.Action == appplatform.ActionInstall && lease.snapshot.Managed && lease.snapshot.Generation == 1 && lease.snapshot.Stage == appplatform.StageActivate
}

// This loader may decrypt/sign only inside a current lease/OU/publisher/runtime
// transaction. Its private handle must be closed on both success and failure.
func loadPlatformBackendDelivery(e migExec, lease platformOperationLease, runtime platformRuntimeRegistration, seed, dek []byte, now int64, create bool) (platformBackendDeliveryIntent, bool, error) {
	var zero platformBackendDeliveryIntent
	if !validPlatformBackendDeliveryStage(lease) || len(seed) != ed25519.SeedSize {
		return zero, false, errAppPlatformStageEvidence
	}
	candidate, _, err := loadPlatformRegistrationCandidate(e, lease, runtime, now)
	if err != nil {
		return zero, false, err
	}
	registration, err := platformBackendRegistrationMaterial(e, lease, runtime, candidate, dek)
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
	var effect, kind, digest, runtimeID, epoch, state, receipt string
	var admitted, observed, created, updated int64
	err = e.queryRow(`SELECT effect_id,kind,payload_sha256,runtime_id,controller_epoch,admitted_fence,observed_fence,state,receipt_json,created_ms,updated_ms FROM app_platform_operation_effects WHERE operation_id=? AND stage='activate' AND effect_number=1`, lease.id).Scan(&effect, &kind, &digest, &runtimeID, &epoch, &admitted, &observed, &state, &receipt, &created, &updated)
	fresh := errors.Is(err, sql.ErrNoRows)
	if fresh {
		if !create {
			return zero, false, errAppPlatformStageEvidence
		}
		effect, err = platformNewUUID()
		if err != nil {
			return zero, false, err
		}
	} else if err != nil {
		return zero, false, err
	} else if !platformValidUUID(effect) || kind != "activate" || runtimeID != runtime.id || epoch != lease.controllerEpoch || admitted != lease.fence || created < 0 || updated < created || updated > now {
		return zero, false, errAppPlatformStageEvidence
	}
	issued := time.Now().UnixMilli()
	if issued < now-1000 || issued > now+1000 {
		return zero, false, errAppPlatformLeaseConflict
	}
	expires := min(issued+30000, lease.leaseUntilMS-1000)
	if expires <= issued {
		return zero, false, errAppPlatformLeaseConflict
	}
	candidate.command.IssuedMS, candidate.command.ExpiresMS = strconv.FormatInt(issued, 10), strconv.FormatInt(expires, 10)
	key := ed25519.NewKeyFromSeed(seed)
	defer clear(key)
	candidate.raw, candidate.signature, err = control.SignCandidate(candidate.command, key, candidate.authority, issued)
	if err != nil {
		return zero, false, err
	}
	command := control.BackendRegistrationCommand{Format: control.BackendRegistrationFormat, EffectID: effect, CandidateRaw: bytes.Clone(candidate.raw), CandidateSignature: bytes.Clone(candidate.signature), DocumentSHA256: platformDigest(document)}
	command.PayloadSHA256, err = control.BackendRegistrationLogicalDigest(command)
	if err != nil {
		return zero, false, err
	}
	result := platformBackendDeliveryIntent{candidate: candidate, command: command, registration: registration}
	if fresh {
		if _, err := e.exec(`INSERT INTO app_platform_operation_effects(operation_id,stage,effect_number,effect_id,kind,payload_sha256,runtime_id,controller_epoch,admitted_fence,observed_fence,state,receipt_json,created_ms,updated_ms) VALUES(?,'activate',1,?,'activate',?,?,?,?,0,'prepared','{}',?,?)`, lease.id, effect, command.PayloadSHA256, runtime.id, lease.controllerEpoch, lease.fence, now, now); err != nil {
			return zero, false, err
		}
	} else {
		if digest != command.PayloadSHA256 {
			return zero, false, errAppPlatformStageEvidence
		}
		switch state {
		case "prepared":
			if observed != 0 || receipt != "{}" || updated != created {
				return zero, false, errAppPlatformStageEvidence
			}
		case "applied":
			r, err := journal.ParseBackendRegistrationReceipt([]byte(receipt))
			source, _ := json.Marshal(candidate.running)
			settled, _ := strconv.ParseInt(r.SettledMS, 10, 64)
			if err != nil || settled > time.Now().UnixMilli() || observed != lease.fence || !r.MatchesCommand(command) || r.Claim.SourceSHA256 != platformDigest(source) || r.Claim.NativeID != candidate.running.NativeID {
				return zero, false, errAppPlatformStageEvidence
			}
			result.applied, result.receipt = true, r
		default:
			return zero, false, errAppPlatformStageEvidence
		}
	}
	result.raw, result.signature, err = control.SignBackendRegistration(command, key, candidate.authority, issued)
	if err != nil {
		return zero, false, err
	}
	failed = false
	return result, fresh, nil
}

// Prepare once under current authority, then renew only the short-lived signed
// envelope. Existing logical identity and derived key/digest must remain exact.
func (s *Server) preparePlatformBackendDelivery(ctx context.Context, expected platformOperationLease) (platformBackendDeliveryIntent, error) {
	var zero platformBackendDeliveryIntent
	if !validPlatformBackendDeliveryStage(expected) {
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
	result, fresh, err := loadPlatformBackendDelivery(e, expected, runtime, seed, dek, facts.now, true)
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
		detail, _ := json.Marshal(struct {
			EffectID   string `json:"effect_id"`
			ResourceID string `json:"resource_id"`
		}{result.command.EffectID, result.candidate.resourceID})
		if _, err := e.exec(`INSERT INTO audit_log(at,actor,actor_ou,action,target_type,target_id,detail,ip) VALUES(?,'system',0,'app_platform.backend_delivery_intent','app_platform_operation',?,?,'')`, time.UnixMilli(facts.now).UTC().Format(time.RFC3339), expected.id, string(detail)); err != nil {
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
	if fresh {
		observed, _ := strconv.ParseInt(result.candidate.readiness.ObservedMS, 10, 64)
		if observed > now || now-observed > 3000 {
			return zero, errAppPlatformStageEvidence
		}
	}
	current, _, err := loadPlatformBackendDelivery(e, expected, runtime, seed, dek, now, false)
	if err != nil {
		return zero, err
	}
	defer current.registration.Close()
	if current.command.EffectID != result.command.EffectID || current.command.PayloadSHA256 != result.command.PayloadSHA256 || current.applied != result.applied {
		return zero, errAppPlatformStageEvidence
	}
	if err := platformVerifyInstallAuthorityAtCommit(e, expected, trust, now); err != nil {
		return zero, err
	}
	if _, err := control.VerifyBackendRegistration(result.raw, result.signature, result.candidate.authority, time.Now().UnixMilli()); err != nil {
		return zero, err
	}
	if err := tx.Commit(); err != nil {
		return zero, err
	}
	failed = false
	return result, nil
}
