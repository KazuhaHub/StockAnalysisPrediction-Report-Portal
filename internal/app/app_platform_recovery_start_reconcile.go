package app

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"strconv"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/journal"
)

// Recovery-mode reconciliation of a resume whose start the scheduler gave up on.
//
// A resume that ended recovery_required with a reserved start holds a start effect the runner may have
// executed. Its lease is gone, so nothing would ever ask, and its candidate activation stays in place:
// the installation could be neither resumed again nor served. While the Portal is paused and the
// scheduler has nothing else to run, this reads the ORIGINAL start effect back from the runner, under its
// own identity and at the fence it was admitted under, and settles it:
//
//   - an applied running receipt settles the effect and returns the operation to the probe stage, on a
//     lease at the effect's own fence, so the unchanged probe, registration and activation stages take
//     the installation to ready;
//   - a definite absence for a start with a recorded reservation is a journal-integrity failure, because
//     the reservation is the runner's own receipt: it is audited and nothing is reverted, so the resume
//     stays recovery_required until the runtime is abandoned;
//   - a definite absence for a start with no recorded reservation, once every command signed for the
//     effect has expired, reverts the effect, settles the operation and returns the installation to review;
//   - anything else changes nothing and is read again later.
//
// It never sends the start: only a read-receipt leaves the Portal.

// errPlatformStartAbsent is the runner's definite statement that it holds no row for a start effect.
var errPlatformStartAbsent = errors.New("runner holds no such start effect")

// platformStartCommandWindowMS is how long a start command stays valid after it is signed.
const platformStartCommandWindowMS = 25000

// platformStartReceiptReader is the runner seam for the restore-start read-receipt.
type platformStartReceiptReader func(ctx context.Context, endpoint platformRuntimeEndpoint, intent platformUpdateStartIntent) (platformUpdateStartRunningObservation, error)

func (s *Server) platformStartReceiptRead() platformStartReceiptReader {
	if s.platformReadStartReceipt != nil {
		return s.platformReadStartReceipt
	}
	return platformReadUpdateStartReceipt
}

// platformStartPendingReceipt is the receipt of an unknown start while the recovery read is in progress: the
// reservation as it was committed, and the latest expiry of any command the recovery read has signed for
// the effect. It is the effect row's own column; no schema is involved.
func platformStartPendingReceipt(reservation string, until int64) string {
	var raw []byte
	if reservation == "" {
		raw, _ = json.Marshal(struct {
			SignedUntil int64 `json:"signed_until_ms"`
		}{until})
	} else {
		raw, _ = json.Marshal(struct {
			Reservation json.RawMessage `json:"reservation"`
			SignedUntil int64           `json:"signed_until_ms"`
		}{json.RawMessage(reservation), until})
	}
	return string(raw)
}

// platformStartRecoveryReceipt reads the receipt of an unknown start: the reservation, and the latest
// signed expiry (zero before the first recovery read). It accepts only the two forms it writes.
func platformStartRecoveryReceipt(receipt string) (string, int64, bool) {
	// An effect with no recorded reservation carries "{}", or only the signed expiry once read.
	if receipt == "{}" {
		return "", 0, true
	}
	if _, err := journal.ParseUpdateStartReservation([]byte(receipt)); err == nil {
		return receipt, 0, true
	}
	var probe struct {
		Reservation json.RawMessage `json:"reservation"`
		SignedUntil int64           `json:"signed_until_ms"`
	}
	if json.Unmarshal([]byte(receipt), &probe) != nil || probe.SignedUntil <= 0 {
		return "", 0, false
	}
	if len(probe.Reservation) == 0 {
		return "", probe.SignedUntil, platformStartPendingReceipt("", probe.SignedUntil) == receipt
	}
	if platformStartPendingReceipt(string(probe.Reservation), probe.SignedUntil) != receipt {
		return "", 0, false
	}
	if _, err := journal.ParseUpdateStartReservation(probe.Reservation); err != nil {
		return "", 0, false
	}
	return string(probe.Reservation), probe.SignedUntil, true
}

type platformRecoveryStartCandidate struct {
	lease       platformOperationLease
	completedMS int64
}

// platformRecoveryStartCandidates lists finished resumes whose start effect is still unknown.
func (s *Server) platformRecoveryStartCandidates(ctx context.Context) ([]platformRecoveryStartCandidate, error) {
	rows, err := s.st.queryContext(ctx, `SELECT o.id,o.installation_id,o.controller_epoch,o.fence,o.target_generation,o.state_revision,o.row_revision,o.completed_ms
		FROM app_platform_operations o JOIN app_platform_operation_effects f ON f.operation_id=o.id AND f.stage='start' AND f.effect_number=1
		JOIN app_platform_review_plans p ON p.id=o.review_plan_id
		WHERE o.action='restore' AND o.managed=1 AND o.stage='recovery_required' AND o.completed_ms IS NOT NULL
		AND f.kind='restore-start' AND f.state='unknown' AND p.plan_json LIKE ?
		ORDER BY o.completed_ms,o.id LIMIT 8`, platformResumePlanPrefix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []platformRecoveryStartCandidate
	for rows.Next() {
		var id, installation, epoch string
		var fence, generation, stateRevision, rowRevision, completed int64
		if err := rows.Scan(&id, &installation, &epoch, &fence, &generation, &stateRevision, &rowRevision, &completed); err != nil {
			return nil, err
		}
		if !platformValidUUID(id) || !platformValidUUID(installation) || generation < 1 || stateRevision < 1 || rowRevision < 1 || fence < 1 {
			continue
		}
		result = append(result, platformRecoveryStartCandidate{
			lease: platformOperationLease{
				id: id, installationID: installation, controllerEpoch: epoch, fence: fence, rowRevision: rowRevision,
				snapshot: appplatform.OperationSnapshot{Action: appplatform.ActionRestore, Managed: true, Generation: uint64(generation), Revision: uint64(stateRevision), Stage: appplatform.StageRecoveryRequired},
			},
			completedMS: completed,
		})
	}
	return result, rows.Err()
}

// platformRecoveryStartFacts is what a recovery read of a start effect is built from and checked against.
type platformRecoveryStartFacts struct {
	intent        platformUpdateStartIntent
	effectState   string
	receipt       string
	reservation   string
	signedUntil   int64
	admittedFence int64
	controlFence  int64
	plan          platformRestoreResumePlan
}

// loadPlatformRecoveryStartFacts rebuilds the original restore-start command of a finished resume from
// the evidence it was built from, at the fence the effect was admitted under, and proves it is the very
// command the effect row records. It signs nothing and writes nothing.
func (s *Server) loadPlatformRecoveryStartFacts(e migExec, c platformRecoveryStartCandidate, runtime platformRuntimeRegistration) (platformRecoveryStartFacts, error) {
	var zero platformRecoveryStartFacts
	fail := func(reason string) (platformRecoveryStartFacts, error) {
		return zero, errors.Join(errAppPlatformStageEvidence, errors.New("recovery start "+reason))
	}
	lease := c.lease
	if resume, err := platformOperationIsResume(e, lease.id); err != nil || !resume {
		return fail("operation")
	}
	var effectID, kind, digest, epoch, state, receipt, effectRuntime string
	var admitted, observed int64
	if err := e.queryRow(`SELECT effect_id,kind,payload_sha256,controller_epoch,admitted_fence,observed_fence,state,receipt_json,runtime_id
		FROM app_platform_operation_effects WHERE operation_id=? AND stage='start' AND effect_number=1`, lease.id).
		Scan(&effectID, &kind, &digest, &epoch, &admitted, &observed, &state, &receipt, &effectRuntime); err != nil {
		return fail("effect")
	}
	reservationRaw, signedUntil, ok := platformStartRecoveryReceipt(receipt)
	if kind != "restore-start" || state != "unknown" || epoch != lease.controllerEpoch || effectRuntime != runtime.id ||
		admitted < 1 || admitted > lease.fence || !ok || (reservationRaw != "" && observed != admitted) || (reservationRaw == "" && observed != 0) {
		return fail("effect row")
	}
	var reservation *journal.UpdateStartReservation
	if reservationRaw != "" {
		parsed, err := journal.ParseUpdateStartReservation([]byte(reservationRaw))
		if err != nil {
			return fail("reservation")
		}
		reservation = &parsed
	}
	readLease := lease
	readLease.fence = admitted
	// A finished resume is not in flight, so its source is built from the proven import evidence alone,
	// as it is when a completed resume is read back.
	evidence, err := loadPlatformResumeEvidence(e, readLease)
	if err != nil {
		return zero, fmt.Errorf("recovery start evidence: %w", err)
	}
	source, err := platformResumeCheckpointSourceFor(evidence, runtime)
	if err != nil {
		return zero, fmt.Errorf("recovery start source: %w", err)
	}
	recoverySetID, _, checkpoint, _, err := loadPlatformRestoreCheckpoint(e, readLease)
	if err != nil || recoverySetID == "" {
		return fail("checkpoint")
	}
	checkpointRaw, err := loadPlatformRestoreCheckpointRaw(e, readLease, checkpoint)
	if err != nil {
		return zero, fmt.Errorf("recovery start checkpoint receipt: %w", err)
	}
	var activation, namespace, activationState string
	var format int64
	if err := e.queryRow(`SELECT id,data_namespace_id,data_format,state FROM app_platform_activations WHERE installation_id=? AND generation=?`,
		lease.installationID, lease.snapshot.Generation).Scan(&activation, &namespace, &format, &activationState); err != nil ||
		activationState != "candidate" || namespace != checkpoint.NamespaceID || format != mustPlatformCheckpointInt(checkpoint.DataFormat) {
		return fail("activation")
	}
	var policy, controlFence int64
	if err := e.queryRow(`SELECT policy_revision,control_fence FROM app_platform_installations
		WHERE id=? AND desired_state='enabled' AND phase='recovery_required' AND controller_epoch=? AND runtime_id=?`,
		lease.installationID, lease.controllerEpoch, runtime.id).Scan(&policy, &controlFence); err != nil || policy < 1 {
		return fail("installation")
	}
	command, err := platformRestoreStartCommand(readLease, runtime, source, checkpoint, checkpointRaw, activation, effectID, policy)
	if err != nil || command.PayloadSHA256 != digest || (reservation != nil && !platformUpdateStartReservationMatches(*reservation, command, activation, effectID)) {
		return fail("command")
	}
	var planRaw string
	if err := e.queryRow(`SELECT p.plan_json FROM app_platform_operations o JOIN app_platform_review_plans p ON p.id=o.review_plan_id
		WHERE o.id=? AND o.installation_id=? AND o.action='restore' AND p.action='restore'`, lease.id, lease.installationID).Scan(&planRaw); err != nil {
		return fail("plan")
	}
	var plan platformRestoreResumePlan
	if json.Unmarshal([]byte(planRaw), &plan) != nil || plan.Format != platformRestoreResumeFormat || plan.InstallationID != lease.installationID ||
		plan.ControllerEpoch != lease.controllerEpoch || plan.TargetGeneration != int64(lease.snapshot.Generation) || plan.RuntimeID != runtime.id {
		return fail("plan binding")
	}
	return platformRecoveryStartFacts{
		intent: platformUpdateStartIntent{
			resourceID: activation, effectID: effectID, runtime: runtime,
			authority: control.Authority{DeploymentID: runtime.deploymentID, ControllerEpoch: runtime.controllerEpoch,
				ControlRevision: runtime.controlRevision, PublicKey: bytes.Clone(runtime.publicKey), Fingerprint: runtime.fingerprint},
			command: command, checkpoint: checkpoint, checkpointRaw: checkpointRaw, source: source,
			plan:             platformRestoreStartPlan(readLease, checkpoint, recoverySetID, policy, source),
			targetDescriptor: source.descriptor, reservation: reservation,
		},
		effectState: state, receipt: receipt, reservation: reservationRaw, signedUntil: signedUntil,
		admittedFence: admitted, controlFence: controlFence, plan: plan,
	}, nil
}

// signPlatformRecoveryStartRead signs the original start effect for a read. The command is signed afresh,
// but it names the effect's own identity and the fence it was admitted under, and its expiry is recorded
// on the effect like every other command signed for it.
func (s *Server) signPlatformRecoveryStartRead(ctx context.Context, c platformRecoveryStartCandidate) (platformRecoveryStartFacts, error) {
	var zero platformRecoveryStartFacts
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
	if paused, err := platformRecoveryPaused(e); err != nil || !paused {
		return zero, errAppPlatformControlPaused
	}
	runtime, seed, err := s.platformDataRuntime(e, c.lease.controllerEpoch, dek)
	if err != nil {
		return zero, err
	}
	defer clear(seed)
	now, err := platformDatabaseNow(e)
	if err != nil {
		return zero, err
	}
	facts, err := s.loadPlatformRecoveryStartFacts(e, c, runtime)
	if err != nil {
		return zero, err
	}
	issued := time.Now().UnixMilli()
	if issued < now-1000 || issued > now+1000 || issued > math.MaxInt64-platformStartCommandWindowMS {
		return zero, errAppPlatformLeaseConflict
	}
	expires := issued + platformStartCommandWindowMS
	facts.intent.command.IssuedMS, facts.intent.command.ExpiresMS = strconv.FormatInt(issued, 10), strconv.FormatInt(expires, 10)
	pending := platformStartPendingReceipt(facts.reservation, max(facts.signedUntil, expires))
	changed, err := e.exec(`UPDATE app_platform_operation_effects SET receipt_json=?
		WHERE operation_id=? AND stage='start' AND effect_number=1 AND effect_id=? AND state='unknown' AND receipt_json=?`,
		pending, c.lease.id, facts.intent.effectID, facts.receipt)
	if err := platformLeaseCAS(changed, err); err != nil {
		return zero, err
	}
	key := ed25519.NewKeyFromSeed(seed)
	defer clear(key)
	if facts.intent.raw, facts.intent.signature, err = control.SignUpdateStart(facts.intent.command, key, facts.intent.authority, issued); err != nil {
		return zero, err
	}
	if facts.intent.readSignature, err = control.SignUpdateStartRead(facts.intent.raw, key, facts.intent.authority, issued); err != nil {
		return zero, err
	}
	if err := tx.Commit(); err != nil {
		return zero, err
	}
	// The effect now carries this read's expiry; the facts keep the expiry of every earlier command.
	facts.receipt = pending
	return facts, nil
}

// reconcilePlatformRecoveryResumes settles what it can. It returns the lease of a resume it reopened, so
// the scheduler can run its remaining stages, and nothing otherwise.
func (s *Server) reconcilePlatformRecoveryResumes(ctx context.Context, endpoint platformRuntimeEndpoint, worker string) (platformInstallSelection, error) {
	var zero platformInstallSelection
	if endpoint.path == "" || !platformValidUUID(worker) || !s.platformRecoveryPaused(ctx) {
		return zero, nil
	}
	candidates, err := s.platformRecoveryStartCandidates(ctx)
	if err != nil {
		return zero, err
	}
	for _, c := range candidates {
		selected, err := s.reconcilePlatformRecoveryResume(ctx, c, endpoint, worker)
		if err != nil {
			if ctx.Err() != nil {
				return zero, ctx.Err()
			}
			log.Printf("app-platform recovery resume reconcile %s: %v", c.lease.id, err)
			continue
		}
		if selected.lease.id != "" {
			return selected, nil
		}
	}
	return zero, nil
}

func (s *Server) reconcilePlatformRecoveryResume(ctx context.Context, c platformRecoveryStartCandidate, endpoint platformRuntimeEndpoint, worker string) (platformInstallSelection, error) {
	var zero platformInstallSelection
	facts, err := s.signPlatformRecoveryStartRead(ctx, c)
	if err != nil {
		return zero, err
	}
	observation, err := s.platformStartReceiptRead()(ctx, endpoint, facts.intent)
	if errors.Is(err, errPlatformStartAbsent) {
		if facts.reservation == "" {
			return zero, s.revertPlatformRecoveryStart(ctx, c, facts)
		}
		return zero, s.recordPlatformRecoveryStartJournalLoss(ctx, c, facts)
	}
	var heldStart *journal.UpdateStartHeldError
	if errors.As(err, &heldStart) {
		return zero, s.settlePlatformRecoveryHeldStart(ctx, c, facts, endpoint, heldStart.State)
	}
	if err != nil {
		return zero, nil
	}
	return s.commitPlatformRecoveryStartApplied(ctx, c, facts, endpoint, observation, worker)
}

// settlePlatformRecoveryHeldStart acts on what the runner says it holds for a resume's start without a running
// receipt, once no command signed for the start can still be admitted and a create the runner gave up on can no
// longer be carried out.
//
//   - orphaned: claimed with nothing executing it. The runner is asked to settle it (it only looks, by name and
//     label, and records what is there); the next read shows a running receipt or a never-run start. The start is
//     never sent.
//   - never_ran: the runner has settled the start as not run and released what it held, which is as definite as an
//     absence: the effect is reverted and the installation returned to review, so a new resume can be reviewed.
//   - reserved, pending: nothing is claimed, or the start may be executing; it is read again later.
func (s *Server) settlePlatformRecoveryHeldStart(ctx context.Context, c platformRecoveryStartCandidate, signed platformRecoveryStartFacts, endpoint platformRuntimeEndpoint, state string) error {
	definiteAfter := max(signed.signedUntil, c.completedMS+platformSigningSlackMS+platformStartCommandWindowMS+platformImportAbsenceMarginMS+platformCreateGraceMS+control.CommandClockSkewMS)
	if time.Now().UnixMilli() <= definiteAfter {
		return nil
	}
	switch state {
	case "never_ran":
		return s.revertPlatformRecoveryStart(ctx, c, signed)
	case "orphaned":
		tx, e, runtime, current, now, err := s.platformRecoveryStartTransaction(ctx, c, signed)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		dek, err := s.dek()
		if err != nil {
			return err
		}
		_, seed, err := s.platformDataRuntime(e, c.lease.controllerEpoch, dek)
		if err != nil {
			return err
		}
		defer clear(seed)
		issued := time.Now().UnixMilli()
		if issued < now-1000 || issued > now+1000 || issued > math.MaxInt64-platformStartCommandWindowMS {
			return errAppPlatformLeaseConflict
		}
		key := ed25519.NewKeyFromSeed(seed)
		defer clear(key)
		_, intent, err := buildPlatformSettleStartCommand(current.intent, key, c.lease.id, c.lease.fence, control.SettleModeSettle, issued)
		if err != nil {
			return err
		}
		// Nothing was written; the transaction is not held across the runner calls.
		tx.Rollback()
		if err := s.platformMigrationRunner().CheckRoot(ctx, c.lease.id, runtime, current.intent.authority, endpoint); err != nil {
			return err
		}
		// Whatever it answers, the next read of the start itself decides what follows.
		_, _ = s.platformSettleStartRun()(ctx, endpoint, intent)
	}
	return nil
}

// platformRecoveryStartContext opens the transaction both settlements run in and re-proves the facts the
// read was signed from.
func (s *Server) platformRecoveryStartTransaction(ctx context.Context, c platformRecoveryStartCandidate, signed platformRecoveryStartFacts) (*sql.Tx, migExec, platformRuntimeRegistration, platformRecoveryStartFacts, int64, error) {
	var zeroFacts platformRecoveryStartFacts
	var zeroRuntime platformRuntimeRegistration
	var zeroExec migExec
	dek, err := s.dek()
	if err != nil {
		return nil, zeroExec, zeroRuntime, zeroFacts, 0, err
	}
	tx, err := s.st.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, zeroExec, zeroRuntime, zeroFacts, 0, err
	}
	ok := false
	defer func() {
		if !ok {
			tx.Rollback()
		}
	}()
	e := migExec{s: s.st, ex: platformContextTx{Tx: tx, ctx: ctx}}
	if err := lockOUMutation(e); err != nil {
		return nil, zeroExec, zeroRuntime, zeroFacts, 0, err
	}
	if err := lockPlatformPublisherTrust(e); err != nil {
		return nil, zeroExec, zeroRuntime, zeroFacts, 0, err
	}
	if paused, err := platformRecoveryPaused(e); err != nil || !paused {
		return nil, zeroExec, zeroRuntime, zeroFacts, 0, errAppPlatformControlPaused
	}
	runtime, seed, err := s.platformDataRuntime(e, c.lease.controllerEpoch, dek)
	if err != nil {
		return nil, zeroExec, zeroRuntime, zeroFacts, 0, err
	}
	clear(seed)
	now, err := platformDatabaseNow(e)
	if err != nil {
		return nil, zeroExec, zeroRuntime, zeroFacts, 0, err
	}
	current, err := s.loadPlatformRecoveryStartFacts(e, c, runtime)
	if err != nil {
		return nil, zeroExec, zeroRuntime, zeroFacts, 0, err
	}
	signedCommand, currentCommand := signed.intent.command, current.intent.command
	signedCommand.IssuedMS, signedCommand.ExpiresMS, currentCommand.IssuedMS, currentCommand.ExpiresMS = "", "", "", ""
	if current.intent.effectID != signed.intent.effectID || !platformUpdateStartCommandsEqual(signedCommand, currentCommand) ||
		current.receipt != signed.receipt || current.admittedFence != signed.admittedFence {
		return nil, zeroExec, zeroRuntime, zeroFacts, 0, errAppPlatformStageEvidence
	}
	ok = true
	return tx, e, runtime, current, now, nil
}

// recordPlatformRecoveryStartJournalLoss handles the runner's statement that it holds no row for a start
// effect the Portal has a reservation for. A reservation is the runner's own receipt that it admitted the
// command, so its journal cannot truthfully lack the row: the runner journal lost it, and the start may
// have run on a host nobody can now ask. That is a journal-integrity failure, not an absence. Nothing is
// reverted and no new resume is admitted: the effect stays unknown and the operation recovery_required,
// which only abandoning the runtime settles. The finding is audited once per effect.
//
// A start that never recorded a reservation is read too, because the runner may have accepted the reserve
// and the reply been lost; only its typed absence, after every signed command has expired, reverts it.
func (s *Server) recordPlatformRecoveryStartJournalLoss(ctx context.Context, c platformRecoveryStartCandidate, signed platformRecoveryStartFacts) error {
	tx, e, _, current, now, err := s.platformRecoveryStartTransaction(ctx, c, signed)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var recorded int
	if err := e.queryRow(`SELECT COUNT(*) FROM audit_log WHERE action='app_platform.restore_start_journal_lost' AND target_type='app_platform_operation' AND target_id=? AND detail LIKE ?`,
		c.lease.id, "%"+current.intent.effectID+"%").Scan(&recorded); err != nil {
		return err
	}
	if recorded != 0 {
		return nil
	}
	detail, _ := json.Marshal(struct {
		EffectID string `json:"effect_id"`
	}{current.intent.effectID})
	if _, err := e.exec(`INSERT INTO audit_log(at,actor,actor_ou,action,target_type,target_id,detail,ip) VALUES(?,'system',0,'app_platform.restore_start_journal_lost','app_platform_operation',?,?,'')`,
		time.UnixMilli(now).UTC().Format(time.RFC3339), c.lease.id, string(detail)); err != nil {
		return err
	}
	return tx.Commit()
}

// revertPlatformRecoveryStart settles a definite absence for a start with no recorded reservation, which the
// runner may have accepted before a lost reply. It is final only after every command signed for
// the effect before this read has expired: the original commands, none of which was signed after the
// operation ended, and every earlier recovery read. Until then it changes nothing.
func (s *Server) revertPlatformRecoveryStart(ctx context.Context, c platformRecoveryStartCandidate, signed platformRecoveryStartFacts) error {
	tx, e, _, current, now, err := s.platformRecoveryStartTransaction(ctx, c, signed)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if now <= max(signed.signedUntil, c.completedMS+platformStartCommandWindowMS)+platformImportAbsenceMarginMS {
		return nil
	}
	changed, err := e.exec(`UPDATE app_platform_operation_effects SET state='reverted',updated_ms=?
		WHERE operation_id=? AND stage='start' AND effect_number=1 AND effect_id=? AND state='unknown' AND receipt_json=?`,
		now, c.lease.id, current.intent.effectID, current.receipt)
	if err := platformLeaseCAS(changed, err); err != nil {
		return err
	}
	if err := returnPlatformResumeToReview(e, c.lease.installationID, c.lease.controllerEpoch, current.plan, now); err != nil {
		return err
	}
	if err := settlePlatformRecoveryOperation(e, c.lease.id, "", now); err != nil {
		return err
	}
	detail, _ := json.Marshal(struct {
		EffectID string `json:"effect_id"`
	}{current.intent.effectID})
	if _, err := e.exec(`INSERT INTO audit_log(at,actor,actor_ou,action,target_type,target_id,detail,ip) VALUES(?,'system',0,'app_platform.restore_start_reverted','app_platform_operation',?,?,'')`,
		time.UnixMilli(now).UTC().Format(time.RFC3339), c.lease.id, string(detail)); err != nil {
		return err
	}
	return tx.Commit()
}

// commitPlatformRecoveryStartApplied settles an applied running receipt: the effect, and the operation, which
// returns to the probe stage on a lease at the fence the effect was admitted under. It continues only when
// that fence is still the installation's own: a fence that moved belongs to someone else's work.
func (s *Server) commitPlatformRecoveryStartApplied(ctx context.Context, c platformRecoveryStartCandidate, signed platformRecoveryStartFacts, endpoint platformRuntimeEndpoint, observation platformUpdateStartRunningObservation, worker string) (platformInstallSelection, error) {
	var zero platformInstallSelection
	if signed.intent.reservation == nil {
		// An applied answer for a start the Portal holds no reservation for cannot be validated against
		// one; the effect stays unknown until the runtime is abandoned.
		return zero, nil
	}
	running, ok := validPlatformUpdateStartRunningObservation(observation, signed.intent, endpoint)
	if !ok {
		return zero, errAppPlatformStageEvidence
	}
	encoded, err := json.Marshal(running)
	if err != nil || len(encoded) > 16384 {
		return zero, errAppPlatformStageEvidence
	}
	tx, e, _, current, now, err := s.platformRecoveryStartTransaction(ctx, c, signed)
	if err != nil {
		return zero, err
	}
	defer tx.Rollback()
	if current.controlFence != c.lease.fence || current.admittedFence != c.lease.fence {
		return zero, nil
	}
	model, err := appplatform.OperationFromSnapshot(c.lease.snapshot)
	if err != nil {
		return zero, errAppPlatformStageEvidence
	}
	reopened, err := model.Reopen(c.lease.snapshot.Revision)
	if err != nil || reopened.Snapshot().Stage != appplatform.StageProbe {
		return zero, errAppPlatformStageEvidence
	}
	changed, err := e.exec(`UPDATE app_platform_operation_effects SET state='applied',observed_fence=?,receipt_json=?,updated_ms=?
		WHERE operation_id=? AND stage='start' AND effect_number=1 AND effect_id=? AND kind='restore-start'
		AND payload_sha256=? AND controller_epoch=? AND admitted_fence=? AND state='unknown' AND observed_fence=? AND receipt_json=?`,
		current.admittedFence, string(encoded), now, c.lease.id, current.intent.effectID, current.intent.command.PayloadSHA256,
		c.lease.controllerEpoch, current.admittedFence, current.admittedFence, current.receipt)
	if err := platformLeaseCAS(changed, err); err != nil {
		return zero, err
	}
	changed, err = e.exec(`UPDATE app_platform_operations SET stage=?,state_revision=?,row_revision=row_revision+1,error_code=NULL,completed_ms=NULL,
		lease_owner=?,lease_until_ms=?,updated_ms=?
		WHERE id=? AND installation_id=? AND controller_epoch=? AND stage='recovery_required' AND state_revision=? AND row_revision=?
		AND fence=? AND completed_ms IS NOT NULL AND lease_owner IS NULL`,
		string(reopened.Snapshot().Stage), int64(reopened.Snapshot().Revision), worker, now+appPlatformLeaseMS, now,
		c.lease.id, c.lease.installationID, c.lease.controllerEpoch, int64(c.lease.snapshot.Revision), c.lease.rowRevision, c.lease.fence)
	if err := platformLeaseCAS(changed, err); err != nil {
		return zero, err
	}
	detail, _ := json.Marshal(struct {
		ResourceID string `json:"resource_id"`
		NativeID   string `json:"native_id"`
	}{current.intent.resourceID, running.NativeID})
	if _, err := e.exec(`INSERT INTO audit_log(at,actor,actor_ou,action,target_type,target_id,detail,ip) VALUES(?,'system',0,'app_platform.restore_start_reconciled','app_platform_operation',?,?,'')`,
		time.UnixMilli(now).UTC().Format(time.RFC3339), c.lease.id, string(detail)); err != nil {
		return zero, err
	}
	facts, err := loadPlatformOperationLease(e, c.lease.id)
	if err != nil {
		return zero, err
	}
	if facts.lease.owner != worker || facts.lease.fence != c.lease.fence || facts.lease.snapshot.Stage != appplatform.StageProbe {
		return zero, errAppPlatformLeaseConflict
	}
	if _, err := finalPlatformOperationLeaseCheck(e, facts); err != nil {
		return zero, err
	}
	if err := tx.Commit(); err != nil {
		return zero, err
	}
	return platformInstallSelection{lease: facts.lease, operationID: c.lease.id}, nil
}
