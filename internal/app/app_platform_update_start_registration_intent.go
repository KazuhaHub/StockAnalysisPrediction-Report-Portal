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

// platformUpdateStartRegistrationRows is everything the registration effect reads from the
// database: its own row (absent for a fresh one), the effect counts that judge it, and the sealed
// identity seed its registration derives from.
type platformUpdateStartRegistrationRows struct {
	effect, kind, digest, epoch, state, receipt string
	admitted, observed, created, updated        int64
	fresh                                       bool
	count, baseEffects                          int
	sealed                                      string
}

func checkPlatformUpdateStartRegistrationSource(lease platformOperationLease, start platformUpdateStartIntent) error {
	if (lease.snapshot.Action != appplatform.ActionUpdate &&
		lease.snapshot.Action != appplatform.ActionRestore &&
		!platformSameReleaseLifecycle(lease.snapshot.Action)) ||
		!lease.snapshot.Managed ||
		lease.snapshot.Stage != appplatform.StageActivate ||
		lease.snapshot.Generation < 2 || start.running == nil ||
		start.readiness == nil || start.plan.TargetPolicyRevision < 1 {
		return errAppPlatformStageEvidence
	}
	return nil
}

// readPlatformUpdateStartRegistrationRows performs every database read the registration needs, in
// the caller's transaction. buildPlatformUpdateStartRegistration then works from the rows alone.
func readPlatformUpdateStartRegistrationRows(e migExec, lease platformOperationLease) (platformUpdateStartRegistrationRows, error) {
	var r platformUpdateStartRegistrationRows
	lock := ""
	if e.s.driver == "postgres" {
		lock = " FOR UPDATE"
	}
	err := e.queryRow(`SELECT effect_id,kind,payload_sha256,controller_epoch,
		admitted_fence,observed_fence,state,receipt_json,created_ms,updated_ms
		FROM app_platform_operation_effects
		WHERE operation_id=? AND stage='activate' AND effect_number=1`+lock,
		lease.id).
		Scan(&r.effect, &r.kind, &r.digest, &r.epoch, &r.admitted, &r.observed,
			&r.state, &r.receipt, &r.created, &r.updated)
	r.fresh = errors.Is(err, sql.ErrNoRows)
	if err != nil && !r.fresh {
		return r, err
	}
	if err := e.queryRow(
		`SELECT COUNT(*) FROM app_platform_operation_effects WHERE operation_id=? AND kind NOT IN ('load_image','remove_image')`,
		lease.id).Scan(&r.count); err != nil {
		return r, err
	}
	r.baseEffects = 2
	if lease.snapshot.Action == appplatform.ActionUpdate {
		r.baseEffects = 4
		migrations, err := platformMigrationEffectCount(e, lease.id)
		if err != nil {
			return r, err
		}
		r.baseEffects += migrations
	}
	if lease.snapshot.Action == appplatform.ActionRestore {
		r.baseEffects = 4
		if settled, err := platformRestoreSettledByMigration(e, lease); err != nil {
			return r, err
		} else if settled {
			// The failed update's settlement did the stop, removal and restore; this operation only starts.
			r.baseEffects = 1
		}
		// A resume has only the start effect before this one: the import already ran the data
		// stages, and nothing was stopped or removed on the new host.
		if resume, err := platformOperationIsResume(e, lease.id); err != nil {
			return r, err
		} else if resume {
			r.baseEffects = 1
			// A resume that followed an applied start also stopped and removed that earlier container.
			var quiesce int
			if err := e.queryRow(`SELECT COUNT(*) FROM app_platform_operation_effects WHERE operation_id=? AND stage='quiesce'`, lease.id).Scan(&quiesce); err != nil {
				return r, err
			}
			r.baseEffects += quiesce
		}
	}
	r.sealed, err = platformReadSealedIdentitySeed(e, lease.installationID)
	if err != nil {
		return r, err
	}
	return r, nil
}

func loadPlatformUpdateStartRegistration(e migExec, lease platformOperationLease, start platformUpdateStartIntent, runtime platformRuntimeRegistration, seed, dek []byte, now int64, create bool) (platformUpdateStartRegistrationIntent, bool, error) {
	var zero platformUpdateStartRegistrationIntent
	if err := checkPlatformUpdateStartRegistrationSource(lease, start); err != nil {
		return zero, false, err
	}
	rows, err := readPlatformUpdateStartRegistrationRows(e, lease)
	if err != nil {
		return zero, false, err
	}
	result, err := buildPlatformUpdateStartRegistration(rows, lease, start, runtime, dek, now, create)
	if err != nil {
		return zero, false, err
	}
	failed := true
	defer func() {
		if failed {
			result.registration.Close()
		}
	}()
	if rows.fresh {
		if _, err := e.exec(`INSERT INTO app_platform_operation_effects(
			operation_id,stage,effect_number,effect_id,kind,payload_sha256,
			runtime_id,controller_epoch,admitted_fence,observed_fence,state,
			receipt_json,created_ms,updated_ms)
			VALUES(?,'activate',1,?,'update-start-registration',?,?,?,?,0,'prepared','{}',?,?)`,
			lease.id, result.command.EffectID, result.command.PayloadSHA256, runtime.id,
			lease.controllerEpoch, lease.fence, now, now); err != nil {
			return zero, false, err
		}
	}
	result, err = completePlatformUpdateStartRegistration(rows, result, lease, start, seed, now)
	if err != nil {
		return zero, false, err
	}
	failed = false
	return result, rows.fresh, nil
}

// buildPlatformUpdateStartRegistration judges the registration effect's row and derives the
// registration and its command from the rows alone: no database access.
func buildPlatformUpdateStartRegistration(r platformUpdateStartRegistrationRows, lease platformOperationLease, start platformUpdateStartIntent, runtime platformRuntimeRegistration, dek []byte, now int64, create bool) (platformUpdateStartRegistrationIntent, error) {
	var zero platformUpdateStartRegistrationIntent
	effect, created, updated := r.effect, r.created, r.updated
	var err error
	if r.fresh {
		if !create || r.count != r.baseEffects || !freshPlatformUpdateRegistrationSourceAt(start, now) {
			return zero, fmt.Errorf(
				"update-start registration source window: %w",
				errAppPlatformStageEvidence)
		}
		effect, err = platformNewUUID()
		if err != nil {
			return zero, err
		}
		created, updated = now, now
	} else {
		switch {
		case r.count != r.baseEffects+1 || !platformValidUUID(effect):
			return zero, fmt.Errorf(
				"update-start registration effect count: %w",
				errAppPlatformStageEvidence)
		case r.kind != "update-start-registration":
			return zero, fmt.Errorf(
				"update-start registration effect kind: %w",
				errAppPlatformStageEvidence)
		case r.epoch != lease.controllerEpoch:
			return zero, fmt.Errorf(
				"update-start registration effect epoch: %w",
				errAppPlatformStageEvidence)
		case r.admitted != lease.fence:
			return zero, fmt.Errorf(
				"update-start registration effect fence: %w",
				errAppPlatformStageEvidence)
		case created < start.activationObserved:
			return zero, fmt.Errorf(
				"update-start registration effect start time: %w",
				errAppPlatformStageEvidence)
		case updated < created || updated > now:
			return zero, fmt.Errorf(
				"update-start registration effect clock: %w",
				errAppPlatformStageEvidence)
		case r.state != "prepared" && r.state != "applied":
			return zero, fmt.Errorf(
				"update-start registration effect state: %w",
				errAppPlatformStageEvidence)
		}
	}
	registration, err := platformBackendRegistrationFromSealedSeed(
		platformManagedSource{
			operationID: lease.id, installationID: lease.installationID,
			epoch: lease.controllerEpoch, generation: lease.snapshot.Generation,
			fence: lease.fence,
		},
		runtime, bytes.Clone(start.targetDescriptor),
		start.plan.TargetReleaseSHA256, start.plan.TargetPolicyRevision, dek, r.sealed)
	if err != nil {
		return zero, err
	}
	failed := true
	defer func() {
		if failed {
			registration.Close()
		}
	}()
	document, err := registration.Document()
	if err != nil {
		return zero, err
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
		return zero, err
	}
	failed = false
	return platformUpdateStartRegistrationIntent{
		start: start, command: command, registration: registration,
		created: created, updated: updated,
	}, nil
}

// completePlatformUpdateStartRegistration checks an existing effect's digest and receipt against the
// built command and signs the registration. It reads no database.
func completePlatformUpdateStartRegistration(r platformUpdateStartRegistrationRows, result platformUpdateStartRegistrationIntent, lease platformOperationLease, start platformUpdateStartIntent, seed []byte, now int64) (platformUpdateStartRegistrationIntent, error) {
	var zero platformUpdateStartRegistrationIntent
	var err error
	if !r.fresh {
		if r.digest != result.command.PayloadSHA256 {
			return zero, errAppPlatformStageEvidence
		}
		switch r.state {
		case "prepared":
			if r.observed != 0 || r.receipt != "{}" || r.updated != r.created {
				return zero, errAppPlatformStageEvidence
			}
		case "applied":
			result.receipt, err = journal.ParseUpdateStartRegistrationReceipt([]byte(r.receipt))
			settled, _ := strconv.ParseInt(result.receipt.SettledMS, 10, 64)
			if err != nil || r.observed != lease.fence ||
				settled > r.updated+1000 ||
				!platformUpdateStartRegistrationReceiptMatches(result.receipt, result, now) {
				return zero, errAppPlatformStageEvidence
			}
			result.applied = true
		}
	}
	key := ed25519.NewKeyFromSeed(seed)
	result.raw, result.signature, err = control.SignUpdateStartRegistration(
		result.command, key, start.authority, time.Now().UnixMilli())
	clear(key)
	if err != nil {
		return zero, err
	}
	return result, nil
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
