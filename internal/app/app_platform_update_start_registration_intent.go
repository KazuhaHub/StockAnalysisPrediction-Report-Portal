package app

import (
	"bytes"
	"crypto/ed25519"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/journal"
)

type platformUpdateStartRegistrationIntent struct {
	start            platformUpdateStartIntent
	command          control.UpdateStartRegistrationCommand
	raw, signature   []byte
	registration     appplatform.BackendRegistration
	applied          bool
	receipt          journal.UpdateStartRegistrationReceipt
	created, updated int64
}

func freshPlatformUpdateRegistrationSourceAt(i platformUpdateStartIntent, now int64) bool {
	return i.activationObserved > 0 && i.activationObserved <= now &&
		now-i.activationObserved <= 3000
}

func platformUpdateStartRegistrationReceiptMatches(r journal.UpdateStartRegistrationReceipt, i platformUpdateStartRegistrationIntent, now int64) bool {
	raw, _ := json.Marshal(r)
	if _, err := journal.ParseUpdateStartRegistrationReceipt(raw); err != nil ||
		i.start.running == nil {
		return false
	}
	c := i.command
	claimed, _ := strconv.ParseInt(r.Claim.ClaimedMS, 10, 64)
	settled, _ := strconv.ParseInt(r.SettledMS, 10, 64)
	observed, _ := strconv.ParseInt(r.ObservedMS, 10, 64)
	revision, _ := strconv.ParseInt(r.Claim.ControlRevision, 10, 64)
	sourceRevision, _ := strconv.ParseInt(
		i.start.running.Claim.ControlRevision, 10, 64)
	recovery := revision
	if r.RecoveryControlRevision != "" {
		recovery, _ = strconv.ParseInt(r.RecoveryControlRevision, 10, 64)
	}
	return r.Claim.EffectID == c.EffectID &&
		r.Claim.PayloadSHA256 == c.PayloadSHA256 &&
		r.Claim.SourceSHA256 == c.RunningReceiptSHA256 &&
		r.Claim.DocumentSHA256 == c.DocumentSHA256 &&
		r.Claim.NativeID == i.start.running.NativeID &&
		revision >= sourceRevision && revision <= i.start.runtime.controlRevision &&
		recovery <= i.start.runtime.controlRevision &&
		observed >= i.start.activationObserved &&
		claimed+1000 >= i.created && observed <= now+1000 &&
		settled >= observed && settled <= now+1000 &&
		settled <= time.Now().UnixMilli()
}

func loadPlatformUpdateStartRegistration(e migExec, lease platformOperationLease, start platformUpdateStartIntent, runtime platformRuntimeRegistration, seed, dek []byte, now int64, create bool) (platformUpdateStartRegistrationIntent, bool, error) {
	var zero platformUpdateStartRegistrationIntent
	if (lease.snapshot.Action != appplatform.ActionUpdate &&
		lease.snapshot.Action != appplatform.ActionRestore &&
		!platformSameReleaseLifecycle(lease.snapshot.Action)) ||
		!lease.snapshot.Managed ||
		lease.snapshot.Stage != appplatform.StageActivate ||
		lease.snapshot.Generation < 2 || start.running == nil ||
		start.readiness == nil || start.plan.TargetPolicyRevision < 1 {
		return zero, false, errAppPlatformStageEvidence
	}
	lock := ""
	if e.s.driver == "postgres" {
		lock = " FOR UPDATE"
	}
	var effect, kind, digest, epoch, state, receipt string
	var admitted, observed, created, updated int64
	err := e.queryRow(`SELECT effect_id,kind,payload_sha256,controller_epoch,
		admitted_fence,observed_fence,state,receipt_json,created_ms,updated_ms
		FROM app_platform_operation_effects
		WHERE operation_id=? AND stage='activate' AND effect_number=1`+lock,
		lease.id).
		Scan(&effect, &kind, &digest, &epoch, &admitted, &observed,
			&state, &receipt, &created, &updated)
	fresh := errors.Is(err, sql.ErrNoRows)
	if err != nil && !fresh {
		return zero, false, err
	}
	var count int
	if err := e.queryRow(
		`SELECT COUNT(*) FROM app_platform_operation_effects WHERE operation_id=?`,
		lease.id).Scan(&count); err != nil {
		return zero, false, err
	}
	baseEffects := 2
	if lease.snapshot.Action == appplatform.ActionUpdate {
		baseEffects = 4
	}
	if lease.snapshot.Action == appplatform.ActionRestore {
		baseEffects = 4
	}
	if fresh {
		if !create || count != baseEffects || !freshPlatformUpdateRegistrationSourceAt(start, now) {
			return zero, false, fmt.Errorf(
				"update-start registration source window: %w",
				errAppPlatformStageEvidence)
		}
		effect, err = platformNewUUID()
		if err != nil {
			return zero, false, err
		}
		created, updated = now, now
	} else {
		switch {
		case count != baseEffects+1 || !platformValidUUID(effect):
			return zero, false, fmt.Errorf(
				"update-start registration effect count: %w",
				errAppPlatformStageEvidence)
		case kind != "update-start-registration":
			return zero, false, fmt.Errorf(
				"update-start registration effect kind: %w",
				errAppPlatformStageEvidence)
		case epoch != lease.controllerEpoch:
			return zero, false, fmt.Errorf(
				"update-start registration effect epoch: %w",
				errAppPlatformStageEvidence)
		case admitted != lease.fence:
			return zero, false, fmt.Errorf(
				"update-start registration effect fence: %w",
				errAppPlatformStageEvidence)
		case created < start.activationObserved:
			return zero, false, fmt.Errorf(
				"update-start registration effect start time: %w",
				errAppPlatformStageEvidence)
		case updated < created || updated > now:
			return zero, false, fmt.Errorf(
				"update-start registration effect clock: %w",
				errAppPlatformStageEvidence)
		case state != "prepared" && state != "applied":
			return zero, false, fmt.Errorf(
				"update-start registration effect state: %w",
				errAppPlatformStageEvidence)
		}
	}
	registration, err := platformBackendRegistrationForRelease(
		e, platformManagedSource{
			operationID: lease.id, installationID: lease.installationID,
			epoch: lease.controllerEpoch, generation: lease.snapshot.Generation,
			fence: lease.fence,
		},
		runtime, bytes.Clone(start.targetDescriptor),
		start.plan.TargetReleaseSHA256, start.plan.TargetPolicyRevision, dek)
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
	runningRaw, _ := json.Marshal(*start.running)
	command := control.UpdateStartRegistrationCommand{
		Format: control.UpdateStartRegistrationFormat, EffectID: effect,
		StartRaw: bytes.Clone(start.raw), StartSignature: bytes.Clone(start.signature),
		RunningReceiptSHA256: platformDigest(runningRaw),
		DocumentSHA256:       platformDigest(document),
	}
	command.PayloadSHA256, err = control.UpdateStartRegistrationLogicalDigest(command)
	if err != nil {
		return zero, false, err
	}
	result := platformUpdateStartRegistrationIntent{
		start: start, command: command, registration: registration,
		created: created, updated: updated,
	}
	if fresh {
		if _, err := e.exec(`INSERT INTO app_platform_operation_effects(
			operation_id,stage,effect_number,effect_id,kind,payload_sha256,
			runtime_id,controller_epoch,admitted_fence,observed_fence,state,
			receipt_json,created_ms,updated_ms)
			VALUES(?,'activate',1,?,'update-start-registration',?,?,?,?,0,'prepared','{}',?,?)`,
			lease.id, effect, command.PayloadSHA256, runtime.id,
			lease.controllerEpoch, lease.fence, now, now); err != nil {
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
			result.receipt, err = journal.ParseUpdateStartRegistrationReceipt([]byte(receipt))
			settled, _ := strconv.ParseInt(result.receipt.SettledMS, 10, 64)
			if err != nil || observed != lease.fence ||
				settled > updated+1000 ||
				!platformUpdateStartRegistrationReceiptMatches(result.receipt, result, now) {
				return zero, false, errAppPlatformStageEvidence
			}
			result.applied = true
		}
	}
	key := ed25519.NewKeyFromSeed(seed)
	result.raw, result.signature, err = control.SignUpdateStartRegistration(
		command, key, start.authority, time.Now().UnixMilli())
	clear(key)
	if err != nil {
		return zero, false, err
	}
	failed = false
	return result, fresh, nil
}

func samePlatformUpdateStartRegistrationIntent(a, b platformUpdateStartRegistrationIntent) bool {
	ar, _ := json.Marshal(a.receipt)
	br, _ := json.Marshal(b.receipt)
	if a.start.running == nil || b.start.running == nil {
		return false
	}
	aa, _ := json.Marshal(*a.start.running)
	bb, _ := json.Marshal(*b.start.running)
	return platformUpdateStartCommandsEqual(a.start.command, b.start.command) &&
		bytes.Equal(aa, bb) &&
		a.command.Format == b.command.Format &&
		a.command.EffectID == b.command.EffectID &&
		a.command.PayloadSHA256 == b.command.PayloadSHA256 &&
		a.command.RunningReceiptSHA256 == b.command.RunningReceiptSHA256 &&
		a.command.DocumentSHA256 == b.command.DocumentSHA256 &&
		a.applied == b.applied && a.created == b.created &&
		a.updated == b.updated && bytes.Equal(ar, br)
}

func clonePlatformUpdateStartRegistrationIntent(i platformUpdateStartRegistrationIntent) platformUpdateStartRegistrationIntent {
	i.raw, i.signature = bytes.Clone(i.raw), bytes.Clone(i.signature)
	i.command.StartRaw, i.command.StartSignature =
		bytes.Clone(i.command.StartRaw), bytes.Clone(i.command.StartSignature)
	i.start.raw, i.start.signature = bytes.Clone(i.start.raw), bytes.Clone(i.start.signature)
	i.start.authority.PublicKey = bytes.Clone(i.start.authority.PublicKey)
	return i
}
