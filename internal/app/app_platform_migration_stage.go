package app

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
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

// The migrate stage of an update whose plan declares a data migration. Effect 1 of the stage is the
// source removal that stage always carried; effect 2 is the migrate-data command, sent only after the
// removal is applied; effect 3, owned by settlement, is the discard that settles an unknown migration.
//
// An unknown migration is never sent again. Its outcome is read back under its own identity; a read that
// shows the runner no longer working on it ends the operation for review, where only a discard settles it.

// errPlatformMigrationBusy reports a migration the runner still holds, or one whose absence cannot be taken
// as final yet. Neither is a failure: the same effect is read again.
var errPlatformMigrationBusy = errors.New("the runner still holds the migration")

// errPlatformMigrationEnded reports an update this attempt ended for review: nothing is left for the
// scheduler to retry or finish, and the migration waits for settlement.
var errPlatformMigrationEnded = errors.New("migration ended the update for review")

// endPlatformMigration ends the leased update for review and reports it so the scheduler lets go.
func (s *Server) endPlatformMigration(ctx context.Context, expected platformOperationLease, effects func(e migExec, now int64) error) error {
	if err := s.finishPlatformManagedUpdateWith(ctx, expected, effects); err != nil {
		return err
	}
	return errPlatformMigrationEnded
}

// platformMigrationPollInterval is how often a pending migration is read back within one attempt.
var platformMigrationPollInterval = 2 * time.Second

// platformDerivedUUID names an object of an effect deterministically, so a command signed again describes
// the same job resource, and so a settlement removal has an operation identity apart from the start it
// removes.
func platformDerivedUUID(label, id string) string {
	sum := sha256.Sum256([]byte("portal-migration/" + label + "/" + id))
	b := sum[:16]
	b[6] = (b[6] & 15) | 64
	b[8] = (b[8] & 63) | 128
	h := hex.EncodeToString(b)
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}

type platformMigrateEvidence struct {
	source           platformCheckpointSource
	plan             platformManagedUpdatePlan
	descriptor       []byte
	checkpoint       journal.CheckpointReceipt
	checkpointRaw    []byte
	removeEffectID   string
	removeReceiptSHA string
	// chain is the recovery chain a retry signs, root first; empty for an update that recovers nothing.
	chain string
}

// loadPlatformMigrateEvidence gathers what a migrate-data command binds: the verified checkpoint, the
// applied source removal and the reviewed migration, all of the one operation.
func loadPlatformMigrateEvidence(e migExec, lease platformOperationLease, runtime platformRuntimeRegistration, now int64, pending int) (platformMigrateEvidence, error) {
	var zero platformMigrateEvidence
	source, err := loadPlatformCheckpointSourceMode(e, lease, runtime, now, false, pending)
	if err != nil {
		return zero, err
	}
	plan, descriptor, err := loadPlatformManagedUpdatePlan(e, lease, source)
	if err != nil || plan.Migration == nil || plan.Action != "" {
		return zero, errors.Join(errAppPlatformStageEvidence, err)
	}
	checkpoint, raw, err := loadPlatformUpdateStartCheckpoint(e, lease, plan)
	if err != nil {
		return zero, err
	}
	var effectID, kind, state, receipt string
	if err := e.queryRow(`SELECT effect_id,kind,state,receipt_json FROM app_platform_operation_effects
		WHERE operation_id=? AND stage='migrate' AND effect_number=1`, lease.id).
		Scan(&effectID, &kind, &state, &receipt); err != nil || kind != "remove-code" || state != "applied" {
		return zero, fmt.Errorf("%w: migration source removal", errAppPlatformStageEvidence)
	}
	// A retry's removal row is the portal's copy of the failed update's, under an identity of its own. The runner
	// knows the removal only under the identity its receipt names, so that is the one the command carries.
	removed, err := journal.ParseManagedRemovalReceipt([]byte(receipt))
	if err != nil || !platformValidUUID(removed.EffectID) || (plan.RecoversOperationID == "" && removed.EffectID != effectID) {
		return zero, fmt.Errorf("%w: migration source removal receipt", errAppPlatformStageEvidence)
	}
	effectID = removed.EffectID
	chain, err := platformRecoveryChain(e, plan.RecoversOperationID)
	if err != nil {
		return zero, err
	}
	return platformMigrateEvidence{
		source: source, plan: plan, descriptor: descriptor, checkpoint: checkpoint, checkpointRaw: raw,
		removeEffectID: effectID, removeReceiptSHA: platformDigest([]byte(receipt)), chain: chain,
	}, nil
}

func platformMigrateCommand(lease platformOperationLease, runtime platformRuntimeRegistration, ev platformMigrateEvidence, effectID string, admittedFence int64) (control.MigrateDataCommand, error) {
	m, p := ev.plan.Migration, ev.plan
	command := control.MigrateDataCommand{
		CommandHeader: control.CommandHeader{
			Format: control.MigrateDataFormat, DeploymentID: runtime.deploymentID, ControllerEpoch: lease.controllerEpoch,
			ControlRevision: strconv.FormatInt(runtime.controlRevision, 10), InstallationID: lease.installationID, OperationID: lease.id,
			TargetGeneration: strconv.FormatUint(lease.snapshot.Generation, 10), Stage: "migrate",
			EffectNumber: control.MigrateDataEffectNumber, EffectID: effectID, Kind: "migrate-data",
			InstallationFence: strconv.FormatInt(admittedFence, 10),
		},
		Payload: control.MigrateDataPayload{
			NamespaceID: ev.source.namespaceID, ResourceID: platformDerivedUUID("migration-job", effectID),
			CheckpointID: ev.checkpoint.CheckpointID, CheckpointEffectID: ev.checkpoint.EffectID,
			CheckpointReceiptSHA256: platformDigest(ev.checkpointRaw),
			RemoveCodeEffectID:      ev.removeEffectID, RemoveCodeReceiptSHA256: ev.removeReceiptSHA,
			SourceReleaseSHA256: p.SourceReleaseSHA256, TargetReleaseSHA256: p.TargetReleaseSHA256,
			SourceSchema: strconv.FormatInt(m.From, 10), TargetSchema: strconv.FormatInt(m.To, 10),
			CheckpointWriterFence: strconv.FormatInt(p.SourceWriterFence, 10),
			WriterFence:           strconv.FormatInt(p.SourceWriterFence+1, 10),
			StartWriterFence:      strconv.FormatInt(p.SourceWriterFence+2, 10),
			ProjectID:             ev.source.projectID, NamespaceInode: ev.source.namespaceInode, Device: ev.source.device,
			QuotaBytes: ev.source.quotaBytes, QuotaInodes: ev.source.quotaInodes,
			WallClockSeconds: strconv.FormatInt(m.WallClockSeconds, 10), EntrypointSHA256: m.EntrypointSHA256,
			RecoversChain: ev.chain,
		},
	}
	digest, err := control.MigrateDataLogicalDigest(command)
	if err != nil {
		return command, errAppPlatformStageEvidence
	}
	command.PayloadSHA256 = digest
	return command, nil
}

type platformMigrateIntent struct {
	effectID      string
	admittedFence int64
	fresh         bool
	runtime       platformRuntimeRegistration
	authority     control.Authority
	command       control.MigrateDataCommand
	// descriptor is the target release descriptor the runner verifies the command against.
	descriptor                    []byte
	raw, signature, readSignature []byte
	// pendingReceipt is the effect row's receipt as read, and priorSignedUntil the latest expiry of any
	// command signed for the effect before this one.
	pendingReceipt   string
	priorSignedUntil int64
	effectState      string
	// recorded is set when the command's own signed window was recorded on the effect, which is when it can be
	// executed. A command signed only to read the effect back is never sent, so its window counts for nothing.
	recorded bool
}

// loadPlatformMigrateIntent accepts either no effect (create, once) or the exact original one. It never
// repairs a partial row and never mints a second effect for a lease takeover: the original is signed again
// at the fence it was admitted under.
func loadPlatformMigrateIntent(e migExec, lease platformOperationLease, runtime platformRuntimeRegistration, ev platformMigrateEvidence, now int64, create bool) (platformMigrateIntent, error) {
	var zero platformMigrateIntent
	lock := ""
	if e.s.driver == "postgres" {
		lock = " FOR UPDATE"
	}
	var effectID, kind, digest, epoch, state, receipt string
	var admitted, observed int64
	effectErr := e.queryRow(`SELECT effect_id,kind,payload_sha256,controller_epoch,admitted_fence,observed_fence,state,receipt_json
		FROM app_platform_operation_effects WHERE operation_id=? AND stage='migrate' AND effect_number=2`+lock, lease.id).
		Scan(&effectID, &kind, &digest, &epoch, &admitted, &observed, &state, &receipt)
	if effectErr != nil && !errors.Is(effectErr, sql.ErrNoRows) {
		return zero, effectErr
	}
	fresh := errors.Is(effectErr, sql.ErrNoRows)
	var prior int64
	if fresh {
		if !create {
			return zero, errAppPlatformStageEvidence
		}
		var err error
		if effectID, err = platformNewUUID(); err != nil {
			return zero, err
		}
		admitted, receipt, state = lease.fence, "{}", "prepared"
	} else {
		until, ok := platformImportSignedUntil(receipt)
		if kind != "migrate-data" || epoch != lease.controllerEpoch || admitted <= 0 || admitted > lease.fence || observed != 0 ||
			(state != "prepared" && state != "unknown") || !ok {
			return zero, errAppPlatformStageEvidence
		}
		prior = until
	}
	command, err := platformMigrateCommand(lease, runtime, ev, effectID, admitted)
	if err != nil {
		return zero, err
	}
	if fresh {
		if _, err := e.exec(`INSERT INTO app_platform_operation_effects(operation_id,stage,effect_number,effect_id,kind,payload_sha256,runtime_id,controller_epoch,admitted_fence,observed_fence,state,receipt_json,created_ms,updated_ms)
			VALUES(?,'migrate',2,?,'migrate-data',?,?,?,?,0,'prepared','{}',?,?)`,
			lease.id, effectID, command.PayloadSHA256, runtime.id, lease.controllerEpoch, admitted, now, now); err != nil {
			return zero, err
		}
	} else if command.PayloadSHA256 != digest {
		return zero, errAppPlatformStageEvidence
	}
	return platformMigrateIntent{
		effectID: effectID, admittedFence: admitted, fresh: fresh, runtime: runtime, authority: platformImportAuthority(runtime),
		command: command, descriptor: bytes.Clone(ev.descriptor), pendingReceipt: receipt, priorSignedUntil: prior, effectState: state,
	}, nil
}

// signPlatformMigrateCommand records the new expiry on the effect before the command exists outside the
// transaction, then signs the execute and read signatures.
//
// record is false for a command that will only be read with: a read is not bound by its signed window, so it
// must not push out the moment an absence becomes final.
func signPlatformMigrateCommand(e migExec, leaseID string, seed []byte, intent *platformMigrateIntent, issued, expires int64, record bool) error {
	intent.command.IssuedMS, intent.command.ExpiresMS = strconv.FormatInt(issued, 10), strconv.FormatInt(expires, 10)
	if record {
		changed, err := e.exec(`UPDATE app_platform_operation_effects SET receipt_json=?
			WHERE operation_id=? AND stage='migrate' AND effect_number=2 AND effect_id=? AND state=? AND observed_fence=0 AND receipt_json=?`,
			platformImportPendingReceipt(max(intent.priorSignedUntil, expires)), leaseID, intent.effectID, intent.effectState, intent.pendingReceipt)
		if err := platformLeaseCAS(changed, err); err != nil {
			return err
		}
	}
	key := ed25519.NewKeyFromSeed(seed)
	defer clear(key)
	var signErr error
	if intent.raw, intent.signature, signErr = control.SignMigrateData(intent.command, key, intent.authority, issued); signErr != nil {
		return signErr
	}
	intent.readSignature, signErr = control.SignMigrateDataRead(intent.raw, key, intent.authority, issued)
	return signErr
}

// signPlatformMigrate signs the migrate-data effect of the leased operation, creating it only when create is
// set and none exists. With send set the command is signed to be executed and its window is recorded; without it
// an existing effect is signed for a read only.
func (s *Server) signPlatformMigrate(ctx context.Context, expected platformOperationLease, create, send bool) (platformMigrateIntent, error) {
	var zero platformMigrateIntent
	if expected.snapshot.Action != appplatform.ActionUpdate || !expected.snapshot.Managed ||
		expected.snapshot.Stage != appplatform.StageMigrate || ctx == nil || ctx.Err() != nil {
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
		return zero, fmt.Errorf("migrate runtime: %w", err)
	}
	defer clear(seed)
	facts, err := loadPlatformBoundOperationLease(e, expected)
	if err != nil {
		return zero, err
	}
	ev, err := loadPlatformMigrateEvidence(e, expected, runtime, facts.now, 1)
	if err != nil {
		return zero, err
	}
	intent, err := loadPlatformMigrateIntent(e, expected, runtime, ev, facts.now, create)
	if err != nil {
		return zero, err
	}
	if intent.fresh {
		detail, _ := json.Marshal(struct {
			EffectID string `json:"effect_id"`
			From     int64  `json:"source_schema"`
			To       int64  `json:"target_schema"`
		}{intent.effectID, ev.plan.Migration.From, ev.plan.Migration.To})
		if _, err := e.exec(`INSERT INTO audit_log(at,actor,actor_ou,action,target_type,target_id,detail,ip) VALUES(?,'system',0,'app_platform.migrate_data_intent','app_platform_operation',?,?,'')`,
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
	// A new effect is about to be sent, so its window is recorded. An existing one is only read back first: its
	// window is recorded when, and only when, a final absence lets it be sent again.
	record := intent.fresh || send
	if err := signPlatformMigrateCommand(e, expected.id, seed, &intent, issued, expires, record); err != nil {
		return zero, err
	}
	intent.recorded = record
	if _, err := finalPlatformOperationLeaseCheck(e, facts); err != nil {
		return zero, err
	}
	if err := tx.Commit(); err != nil {
		return zero, err
	}
	return intent, nil
}

// platformMigrateReceiptForCommand is the Portal's own check that a receipt settles exactly the command it
// issued, whatever the transport claims.
func platformMigrateReceiptForCommand(r journal.MigrateDataReceipt, c control.MigrateDataCommand) bool {
	if !journal.MigrateDataReceiptMatchesCommand(r, c) || r.Claim.ResourceID != c.Payload.ResourceID {
		return false
	}
	observed, observedErr := strconv.ParseInt(r.ObservedMS, 10, 64)
	settled, settledErr := strconv.ParseInt(r.SettledMS, 10, 64)
	return observedErr == nil && settledErr == nil && settled >= observed
}

// validPlatformLifecycleProof checks the provenance of a runner reply against the command it answers.
//
// A status describes the moment of the read, so it must have been observed after the command was signed. A
// terminal receipt (applied, reverted, discarded) is immutable and bound to its effect, so read back on a later
// tick it still carries the time it was first observed: fresh is false for it.
func validPlatformLifecycleProof(proof platformLifecycleProof, raw []byte, authority control.Authority, endpoint platformRuntimeEndpoint, observedMS, settledMS, issuedMS string, fresh bool) bool {
	if proof.CommandSHA256() != platformDigest(raw) || proof.ReceivedAt().IsZero() {
		return false
	}
	a := proof.Controller()
	if a.DeploymentID != authority.DeploymentID || a.ControllerEpoch != authority.ControllerEpoch || a.ControlRevision != authority.ControlRevision ||
		a.Fingerprint != authority.Fingerprint || !bytes.Equal(a.PublicKey, authority.PublicKey) {
		return false
	}
	peer := proof.Endpoint()
	age := time.Since(proof.ReceivedAt())
	observed, observedErr := strconv.ParseInt(observedMS, 10, 64)
	issued, issuedErr := strconv.ParseInt(issuedMS, 10, 64)
	settled, settledErr := observed, error(nil)
	if settledMS != "" {
		settled, settledErr = strconv.ParseInt(settledMS, 10, 64)
	}
	return peer.RootController && peer.Path == endpoint.path && peer.RunnerUID == 0 && peer.ControllerUID == endpoint.controllerUID &&
		peer.ControllerGID == endpoint.controllerGID && age >= 0 && age <= 3*time.Second && observedErr == nil && settledErr == nil &&
		(!fresh || (issuedErr == nil && observed >= issued)) && observed <= settled && settled <= proof.ReceivedAt().UnixMilli()
}

func validPlatformMigrateObservation(o platformMigrateObservation, intent platformMigrateIntent, endpoint platformRuntimeEndpoint) (journal.MigrateDataReceipt, bool) {
	receipt, err := o.MigrateDataReceipt()
	if err != nil || !platformMigrateReceiptForCommand(receipt, intent.command) ||
		!validPlatformLifecycleProof(o, intent.raw, intent.authority, endpoint, receipt.ObservedMS, receipt.SettledMS, intent.command.IssuedMS, false) {
		return journal.MigrateDataReceipt{}, false
	}
	return receipt, true
}

// validPlatformMigrateStatus checks a typed pending, orphaned or absent reply against the command it answers.
func validPlatformMigrateStatus(o platformLifecycleProof, status journal.MigrateDataStatus, header control.CommandHeader, raw []byte, authority control.Authority, endpoint platformRuntimeEndpoint) bool {
	return status.Kind == header.Kind && status.EffectID == header.EffectID && status.PayloadSHA256 == header.PayloadSHA256 &&
		status.OperationID == header.OperationID && status.InstallationID == header.InstallationID &&
		validPlatformLifecycleProof(o, raw, authority, endpoint, status.ObservedMS, "", header.IssuedMS, true)
}

// dispatchPlatformMigrateData sends the migrate-data effect and settles it from the runner's receipt.
func (s *Server) dispatchPlatformMigrateData(ctx context.Context, expected platformOperationLease, endpoint platformRuntimeEndpoint) (platformOperationLease, error) {
	if err := s.ensurePlatformReleaseImage(ctx, expected, endpoint); err != nil {
		return platformOperationLease{}, fmt.Errorf("image load: %w", err)
	}
	intent, err := s.signPlatformMigrate(ctx, expected, true, false)
	if err != nil {
		return platformOperationLease{}, fmt.Errorf("migrate-data prepare: %w", err)
	}
	runner := s.platformMigrationRunner()
	if err := runner.CheckRoot(ctx, expected.id, intent.runtime, intent.authority, endpoint); err != nil {
		return platformOperationLease{}, fmt.Errorf("migrate-data root: %w", err)
	}
	if !intent.fresh {
		// An earlier attempt may have reached the runner. Ask before sending anything: a receipt settles the
		// effect, a runner still working is waited for, and only a final absence lets the effect be sent.
		lease, sent, err := s.readBackPlatformMigrate(ctx, expected, endpoint, intent)
		if err != nil || !sent {
			return lease, err
		}
		// A final absence: every command signed for the effect has expired, so it is sent again under the same
		// identity, with a window of its own recorded first.
		if intent, err = s.signPlatformMigrate(ctx, expected, false, true); err != nil {
			return platformOperationLease{}, fmt.Errorf("migrate-data resend: %w", err)
		}
	}
	observation, err := runner.ExecuteMigrate(ctx, endpoint, intent)
	if err != nil {
		// A lost acknowledgement says nothing about the job. The only way on is to read the effect back.
		lease, sendAgain, readErr := s.readBackPlatformMigrate(ctx, expected, endpoint, intent)
		if readErr != nil || sendAgain {
			if readErr == nil {
				readErr = err
			}
			return platformOperationLease{}, readErr
		}
		return lease, nil
	}
	return s.commitPlatformMigrate(ctx, expected, intent, endpoint, observation)
}

// readBackPlatformMigrate reads the effect until the runner answers with a receipt (committed), reports it
// orphaned (the operation ends for review) or the context ends. sendAgain is set only for a final absence.
func (s *Server) readBackPlatformMigrate(ctx context.Context, expected platformOperationLease, endpoint platformRuntimeEndpoint, intent platformMigrateIntent) (lease platformOperationLease, sendAgain bool, err error) {
	runner := s.platformMigrationRunner()
	for {
		observation, readErr := runner.ReadMigrate(ctx, endpoint, intent)
		switch {
		case errors.Is(readErr, errPlatformMigrationAbsent):
			// The command just signed for this attempt counts too: it may still be on its way.
			until := intent.priorSignedUntil
			if intent.recorded {
				until = max(until, platformSignedUntilOf(intent.command))
			}
			if time.Now().UnixMilli() <= until+platformImportAbsenceMarginMS {
				return platformOperationLease{}, false, errPlatformMigrationBusy
			}
			return platformOperationLease{}, true, nil
		case readErr != nil:
			return platformOperationLease{}, false, readErr
		}
		if receipt, ok := validPlatformMigrateObservation(observation, intent, endpoint); ok {
			_ = receipt
			lease, err = s.commitPlatformMigrate(ctx, expected, intent, endpoint, observation)
			return lease, false, err
		}
		status, statusErr := observation.MigrationStatus()
		if statusErr != nil || !validPlatformMigrateStatus(observation, status, intent.command.CommandHeader, intent.raw, intent.authority, endpoint) {
			return platformOperationLease{}, false, errAppPlatformStageEvidence
		}
		switch status.State {
		case "orphaned":
			// Nothing is running it and nothing will finish it. Only a discard settles it, and that is
			// done once the operation is in review.
			return platformOperationLease{}, false, s.endPlatformMigration(ctx, expected, nil)
		case "pending":
			select {
			case <-ctx.Done():
				return platformOperationLease{}, false, ctx.Err()
			case <-time.After(platformMigrationPollInterval):
			}
		default:
			return platformOperationLease{}, false, errPlatformMigrationBusy
		}
	}
}

// commitPlatformMigrate settles the effect from a verified receipt. A migrated receipt applies the effect
// and moves the operation to the start; a failed one reverts it and ends the operation for review.
func (s *Server) commitPlatformMigrate(ctx context.Context, expected platformOperationLease, intent platformMigrateIntent, endpoint platformRuntimeEndpoint, observation platformMigrateObservation) (platformOperationLease, error) {
	var zero platformOperationLease
	if expected.snapshot.Action != appplatform.ActionUpdate || !expected.snapshot.Managed ||
		expected.snapshot.Stage != appplatform.StageMigrate || expected.rowRevision == math.MaxInt64 {
		return zero, errAppPlatformStageEvidence
	}
	intent.raw = bytes.Clone(intent.raw)
	intent.authority.PublicKey = bytes.Clone(intent.authority.PublicKey)
	receipt, ok := validPlatformMigrateObservation(observation, intent, endpoint)
	if !ok || (receipt.State != "migrated" && receipt.State != "failed") {
		return zero, errAppPlatformStageEvidence
	}
	encoded, err := json.Marshal(receipt)
	if err != nil || len(encoded) > 16384 {
		return zero, errAppPlatformStageEvidence
	}
	if receipt.State == "failed" {
		// A known failure: the effect is reverted and the operation waits for settlement.
		return zero, s.endPlatformMigration(ctx, expected, func(e migExec, now int64) error {
			changed, err := e.exec(`UPDATE app_platform_operation_effects SET state='reverted',observed_fence=?,receipt_json=?,updated_ms=?
				WHERE operation_id=? AND stage='migrate' AND effect_number=2 AND effect_id=? AND kind='migrate-data' AND payload_sha256=?
				AND admitted_fence=? AND state='prepared' AND observed_fence=0 AND receipt_json=?`,
				intent.admittedFence, string(encoded), now, expected.id, intent.effectID, intent.command.PayloadSHA256, intent.admittedFence, intent.pendingReceiptNow(e, expected.id))
			return platformLeaseCAS(changed, err)
		})
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
	ev, err := loadPlatformMigrateEvidence(e, expected, runtime, facts.now, 1)
	if err != nil {
		return zero, err
	}
	current, err := loadPlatformMigrateIntent(e, expected, runtime, ev, facts.now, false)
	if err != nil || current.effectID != intent.effectID || current.command.PayloadSHA256 != intent.command.PayloadSHA256 {
		return zero, errors.Join(errAppPlatformStageEvidence, err)
	}
	now, err := finalPlatformOperationLeaseCheck(e, facts)
	if err != nil {
		return zero, err
	}
	changed, err := e.exec(`UPDATE app_platform_operation_effects SET state='applied',observed_fence=?,receipt_json=?,updated_ms=?
		WHERE operation_id=? AND stage='migrate' AND effect_number=2 AND effect_id=? AND kind='migrate-data' AND payload_sha256=?
		AND runtime_id=? AND controller_epoch=? AND admitted_fence=? AND state=? AND observed_fence=0 AND receipt_json=?`,
		current.admittedFence, string(encoded), now, expected.id, current.effectID, current.command.PayloadSHA256, runtime.id,
		expected.controllerEpoch, current.admittedFence, current.effectState, current.pendingReceipt)
	if err := platformLeaseCAS(changed, err); err != nil {
		return zero, err
	}
	model, err := appplatform.OperationFromSnapshot(expected.snapshot)
	if err != nil {
		return zero, errAppPlatformStageEvidence
	}
	next, err := model.Advance(expected.snapshot.Revision, appplatform.StageStart)
	if err != nil {
		return zero, errAppPlatformStageEvidence
	}
	result := expected
	result.snapshot, result.rowRevision = next.Snapshot(), expected.rowRevision+1
	changed, err = e.exec(`UPDATE app_platform_operations SET stage='start',state_revision=?,row_revision=row_revision+1,updated_ms=?
		WHERE id=? AND installation_id=? AND controller_epoch=? AND stage='migrate' AND state_revision=? AND row_revision=? AND fence=?
		AND lease_owner=? AND lease_until_ms=? AND lease_until_ms>? AND completed_ms IS NULL`,
		int64(result.snapshot.Revision), now, expected.id, expected.installationID, expected.controllerEpoch,
		int64(expected.snapshot.Revision), expected.rowRevision, expected.fence, expected.owner, expected.leaseUntilMS, now)
	if err := platformLeaseCAS(changed, err); err != nil {
		return zero, err
	}
	detail, _ := json.Marshal(struct {
		EffectID string `json:"effect_id"`
		Receipt  string `json:"receipt_sha256"`
		UsedByte string `json:"used_bytes"`
	}{current.effectID, platformDigest(encoded), receipt.UsedBytes})
	if _, err := e.exec(`INSERT INTO audit_log(at,actor,actor_ou,action,target_type,target_id,detail,ip) VALUES(?,'system',0,'app_platform.migrate_data_applied','app_platform_operation',?,?,'')`,
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

// pendingReceiptNow reads the effect's receipt column as it is now, so a settlement of the failed branch
// matches whatever signed-until record the last signing left.
func (i platformMigrateIntent) pendingReceiptNow(e migExec, operationID string) string {
	var receipt string
	if err := e.queryRow(`SELECT receipt_json FROM app_platform_operation_effects WHERE operation_id=? AND stage='migrate' AND effect_number=2`, operationID).Scan(&receipt); err != nil {
		return i.pendingReceipt
	}
	return receipt
}

// runPlatformMigrateStage runs the migrate stage of a managed update: the source removal, then, when the
// reviewed plan declares a migration, the migrate-data effect.
func (s *Server) runPlatformMigrateStage(ctx context.Context, expected platformOperationLease, endpoint platformRuntimeEndpoint) (platformOperationLease, error) {
	var removal, migrate string
	_ = s.st.queryRow(`SELECT COALESCE(MAX(CASE WHEN effect_number=1 THEN state END),''),
		COALESCE(MAX(CASE WHEN effect_number=2 THEN state END),'')
		FROM app_platform_operation_effects WHERE operation_id=? AND stage='migrate'`, expected.id).Scan(&removal, &migrate)
	if removal == "applied" {
		return s.dispatchPlatformMigrateData(ctx, expected, endpoint)
	}
	return s.executePlatformManagedUpdateMigrate(ctx, expected, endpoint)
}

type platformDiscardIntent struct {
	effectID      string
	admittedFence int64
	runtime       platformRuntimeRegistration
	authority     control.Authority
	command       control.DiscardMigrationCommand
	descriptor    []byte
	// migrate is the migrate-data effect this discard settles.
	migrateEffectID               string
	raw, signature, readSignature []byte
	pendingReceipt                string
	priorSignedUntil              int64
	effectState                   string
}
