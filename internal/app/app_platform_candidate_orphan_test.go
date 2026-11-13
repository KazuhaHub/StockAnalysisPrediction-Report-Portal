package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/journal"
)

// orphanFixture is an install operation, plain or writable, claimed by a scheduler, with the open start effect row
// the runner's orphan receipt answers.
type orphanFixture struct {
	refusedEffectFixture
}

// newOrphanFixture is a managed install operation at its start stage with the start effect row the Portal wrote for
// the real signed command (plain candidate, or writable candidate), claimed by a scheduler of the matching kind.
func newOrphanFixture(t *testing.T, writable bool) orphanFixture {
	t.Helper()
	var s *Server
	var lease platformOperationLease
	if writable {
		var err error
		s, lease, _ = platformWritableStartFixture(t, false)
		if _, err = s.preparePlatformWritableStart(context.Background(), lease); err != nil {
			t.Fatal(err)
		}
	} else {
		var err error
		s, lease = platformCandidateIntentFixture(t, false)
		if _, err = s.preparePlatformInstallCandidate(context.Background(), lease); err != nil {
			t.Fatal(err)
		}
	}
	dir, err := os.MkdirTemp("/tmp", "orphan-worker-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	endpoint := platformRuntimeEndpoint{path: dir + "/absent.sock", controllerUID: 41001, controllerGID: 41001}
	w, err := newPlatformInstallScheduler(s, endpoint, platformFixtureID(931), 50*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	w.writable, w.active = writable, lease
	f := refusedEffectFixture{s: s, w: w, op: lease.id}
	if err := s.st.queryRow(`SELECT effect_id,payload_sha256 FROM app_platform_operation_effects WHERE operation_id=? AND stage='start' AND effect_number=1 AND state='prepared'`, lease.id).Scan(&f.effect, &f.payload); err != nil {
		t.Fatal("no prepared start effect:", err)
	}
	return orphanFixture{f}
}

func (f orphanFixture) receipt() journal.CandidateOrphanReceipt {
	l := f.w.active
	return journal.CandidateOrphanReceipt{
		Format: "portal.runner.candidate-orphan-receipt/1-dev.1", DeploymentID: platformFixtureID(1), ControllerEpoch: l.controllerEpoch,
		InstallationID: l.installationID, OperationID: l.id, TargetGeneration: strconv.FormatUint(l.snapshot.Generation, 10), Stage: "start",
		EffectNumber: "1", EffectID: f.effect, Kind: "start-candidate", PayloadSHA256: f.payload, ResourceID: platformFixtureID(6),
		Outcome: journal.CandidateOrphanOutcome, SettledMS: strconv.FormatInt(time.Now().UnixMilli(), 10),
	}
}

// settleInTx runs only the settlement, in its own transaction under the verified lease: the way the plain candidate
// start, which has no scheduler route that can end its operation, reaches it.
func (f orphanFixture) settleInTx(r journal.CandidateOrphanReceipt) error {
	tx, err := f.s.st.db.BeginTx(context.Background(), nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	e := migExec{s: f.s.st, ex: platformContextTx{Tx: tx, ctx: context.Background()}}
	facts, err := loadPlatformBoundOperationLease(e, f.w.active)
	if err != nil {
		return err
	}
	if err := settleOrphanedStartEffect(e, facts, r); err != nil {
		return err
	}
	return tx.Commit()
}

func (f orphanFixture) ended(t *testing.T) (stage, code string) {
	t.Helper()
	if err := f.s.st.queryRow(`SELECT stage,COALESCE(error_code,'') FROM app_platform_operations WHERE id=?`, f.op).Scan(&stage, &code); err != nil {
		t.Fatal(err)
	}
	return stage, code
}

func (f orphanFixture) audits(t *testing.T) int {
	return scalar[int](t, f.s.st, `SELECT COUNT(*) FROM audit_log WHERE action='app_platform.candidate_never_ran' AND target_id=?`, f.op)
}

// The runner proved the start never ran: the Portal's effect row is reverted with the typed receipt and the operation
// ends failed with a closed code, in one transaction, and the audit log records it. Fails if the row is left
// unknown, the operation ends for review without the code, or the receipt is not kept.
func TestWritableCandidateOrphanSettlesTheStartEffectAndFailsTheOperation(t *testing.T) {
	f := newOrphanFixture(t, true)
	r := f.receipt()
	if err := f.w.finishOrphan(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	state, receipt := f.effectState(t)
	parsed, err := journal.ParseCandidateOrphanReceipt([]byte(receipt))
	if state != "reverted" || err != nil || parsed != r {
		t.Fatalf("effect %s %s %v", state, receipt, err)
	}
	if stage, code := f.ended(t); (stage != "failed" && stage != "recovery_required") || code != "start_never_ran" || !f.finished(t) {
		t.Fatalf("operation %s %s", stage, code)
	}
	if f.audits(t) != 1 {
		t.Fatal("the audit log does not record the settlement once")
	}
	if f.w.active.id != "" || f.w.currentStatus().code != "review_required" {
		t.Fatalf("worker state %+v", f.w.currentStatus())
	}
}

// The plain candidate start settles the same row the same way. Its operation is not ended here: no scheduler route
// dispatches the plain candidate start, and the install review finisher accepts only the writable format.
func TestPlainCandidateOrphanSettlesTheStartEffect(t *testing.T) {
	f := newOrphanFixture(t, false)
	r := f.receipt()
	if err := f.settleInTx(r); err != nil {
		t.Fatal(err)
	}
	state, receipt := f.effectState(t)
	parsed, err := journal.ParseCandidateOrphanReceipt([]byte(receipt))
	if state != "reverted" || err != nil || parsed != r || f.audits(t) != 1 {
		t.Fatalf("effect %s %s %v audits=%d", state, receipt, err, f.audits(t))
	}
}

// The scheduler turns the runner's typed error, wrapped however a stage wraps it, into that end instead of a retry.
func TestSchedulerTickSettlesAnOrphanedStart(t *testing.T) {
	f := newOrphanFixture(t, true)
	r := f.receipt()
	f.w.runOverride = func(context.Context, platformOperationLease) (platformInstallRun, error) {
		return platformInstallRun{}, fmt.Errorf("start stage: %w", &journal.CandidateOrphanSettledError{Receipt: r})
	}
	if err := f.w.tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if state, _ := f.effectState(t); state != "reverted" {
		t.Fatalf("effect %s", state)
	}
	if _, code := f.ended(t); code != "start_never_ran" || f.w.attempts != 0 {
		t.Fatalf("code %s attempts %d", code, f.w.attempts)
	}
}

// A replay settles exactly once and does not error: the same receipt again, in the settlement itself.
func TestCandidateOrphanReplayIsIdempotent(t *testing.T) {
	for _, writable := range []bool{false, true} {
		t.Run(fmt.Sprintf("writable=%v", writable), func(t *testing.T) {
			f := newOrphanFixture(t, writable)
			r := f.receipt()
			if err := f.settleInTx(r); err != nil {
				t.Fatal(err)
			}
			_, first := f.effectState(t)
			if err := f.settleInTx(r); err != nil {
				t.Fatalf("a replay failed: %v", err)
			}
			if state, second := f.effectState(t); state != "reverted" || second != first || f.audits(t) != 1 {
				t.Fatalf("a replay changed the books: %s audits=%d", state, f.audits(t))
			}
		})
	}
}

// Nothing is settled when the receipt does not name the open start effect exactly, or the lease is not current.
func TestCandidateOrphanSettlesNothingOnAMismatch(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*orphanFixture, *journal.CandidateOrphanReceipt)
	}{
		{"wrong_effect", func(_ *orphanFixture, r *journal.CandidateOrphanReceipt) { r.EffectID = platformFixtureID(7777) }},
		{"wrong_payload", func(_ *orphanFixture, r *journal.CandidateOrphanReceipt) { r.PayloadSHA256 = strings.Repeat("c", 64) }},
		{"wrong_operation", func(_ *orphanFixture, r *journal.CandidateOrphanReceipt) { r.OperationID = platformFixtureID(7778) }},
		{"wrong_installation", func(_ *orphanFixture, r *journal.CandidateOrphanReceipt) { r.InstallationID = platformFixtureID(7779) }},
		{"wrong_epoch", func(_ *orphanFixture, r *journal.CandidateOrphanReceipt) { r.ControllerEpoch = platformFixtureID(7780) }},
		{"wrong_effect_number", func(_ *orphanFixture, r *journal.CandidateOrphanReceipt) { r.EffectNumber = "2" }},
		{"invalid_receipt", func(_ *orphanFixture, r *journal.CandidateOrphanReceipt) { r.Outcome = "applied" }},
		{"effect_already_applied", func(f *orphanFixture, _ *journal.CandidateOrphanReceipt) {
			if _, err := f.s.st.db.Exec(f.s.st.bind(`UPDATE app_platform_operation_effects SET state='applied',receipt_json='{"a":"b"}' WHERE effect_id=?`), f.effect); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		for _, writable := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/writable=%v", tc.name, writable), func(t *testing.T) {
				f := newOrphanFixture(t, writable)
				r := f.receipt()
				tc.mutate(&f, &r)
				beforeState, beforeReceipt := f.effectState(t)
				err := f.settleInTx(r)
				if writable {
					err = f.w.finishOrphan(context.Background(), r)
				}
				if !errors.Is(err, errAppPlatformStageEvidence) {
					t.Fatalf("err = %v", err)
				}
				if state, receipt := f.effectState(t); state != beforeState || receipt != beforeReceipt {
					t.Fatalf("the effect changed: %s %s", state, receipt)
				}
				if f.finished(t) || f.audits(t) != 0 {
					t.Fatal("the operation ended or was audited on a mismatch")
				}
			})
		}
	}
}

// A mismatching receipt is not a dead end: the tick treats it as the ordinary failure and retries.
func TestSchedulerTickRetriesAnOrphanReceiptThatDoesNotMatch(t *testing.T) {
	f := newOrphanFixture(t, true)
	r := f.receipt()
	r.EffectID = platformFixtureID(7777)
	f.w.runOverride = func(context.Context, platformOperationLease) (platformInstallRun, error) {
		return platformInstallRun{}, &journal.CandidateOrphanSettledError{Receipt: r}
	}
	if err := f.w.tick(context.Background()); err == nil || f.w.attempts != 1 || f.finished(t) {
		t.Fatalf("err=%v attempts=%d", err, f.w.attempts)
	}
}

func TestCandidateOrphanWithAStaleLeaseSettlesNothing(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*platformOperationLease)
	}{
		{"moved_fence", func(l *platformOperationLease) { l.fence++ }},
		{"other_owner", func(l *platformOperationLease) { l.owner = platformFixtureID(8888) }},
		{"other_epoch", func(l *platformOperationLease) { l.controllerEpoch = platformFixtureID(8889) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newOrphanFixture(t, true)
			r := f.receipt()
			tc.mutate(&f.w.active)
			if err := f.w.finishOrphan(context.Background(), r); err == nil {
				t.Fatal("a stale lease ended the operation")
			}
			if err := f.settleInTx(r); err == nil {
				t.Fatal("a stale lease settled")
			}
			if state, receipt := f.effectState(t); state != "prepared" || receipt != "{}" || f.finished(t) || f.audits(t) != 0 {
				t.Fatalf("a stale lease settled: %s %s", state, receipt)
			}
		})
	}
}

// If ending the operation fails after the row was settled, the row is not settled: one transaction, no audit.
func TestCandidateOrphanSettlesNothingWhenTheOperationCannotEnd(t *testing.T) {
	f := newOrphanFixture(t, true)
	if _, err := f.s.st.db.Exec(`CREATE TRIGGER refuse_operation_end BEFORE UPDATE ON app_platform_operations WHEN NEW.completed_ms IS NOT NULL BEGIN SELECT RAISE(ABORT, 'operation cannot end'); END`); err != nil {
		t.Fatal(err)
	}
	if err := f.w.finishOrphan(context.Background(), f.receipt()); err == nil {
		t.Fatal("the operation ended")
	}
	if state, receipt := f.effectState(t); state != "prepared" || receipt != "{}" || f.audits(t) != 0 || f.finished(t) {
		t.Fatalf("partial settlement: %s %s", state, receipt)
	}
}

func TestStartNeverRanIsAPublicOperationErrorCode(t *testing.T) {
	if platformPublicOperationErrorCode("start_never_ran") != "start_never_ran" || isPlatformCapacityErrorCode("start_never_ran") {
		t.Fatal("the code is not in the public allow-list, or is mistaken for a capacity code")
	}
}
