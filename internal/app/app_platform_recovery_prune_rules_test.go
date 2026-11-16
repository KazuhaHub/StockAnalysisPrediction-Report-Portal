package app

import (
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/journal"
)

// Keep-N counts verified sets only. Fails if a set that is not verified takes a verified set's place, which would let
// the last verified fallback be pruned.
func TestPruneKeepsTheLatestVerifiedSetsWhateverTheOtherStatesAre(t *testing.T) {
	for name, state := range map[string]string{"review_required": "review_required", "restoring": "restoring", "preparing": "preparing"} {
		t.Run(name, func(t *testing.T) {
			p := newPruneFixture(t, false)
			fallback := p.set(t, "verified", 100)
			other := p.set(t, state, 200)
			p.prune(t)
			if len(p.runner.sent) != 0 || p.state(t, fallback) != "verified" || p.state(t, other) != state {
				t.Fatalf("the last verified set was pruned because of a %s set: %v", state, p.runner.calls)
			}
		})
	}
	t.Run("a granted verified set takes its place but is never pruned", func(t *testing.T) {
		p := newPruneFixture(t, false)
		older := p.set(t, "verified", 100)
		granted := p.set(t, "verified", 200)
		p.grant(t, granted, "issued")
		p.prune(t)
		if p.state(t, older) != "deleted" || p.state(t, granted) != "verified" {
			t.Fatalf("states %s %s", p.state(t, older), p.state(t, granted))
		}
	})
}

// Newer is the runner's order: created_ms greater, or equal and the checkpoint id smaller. With keep=1 and three sets of
// one created_ms, the set with the smallest checkpoint id is the one kept.
func TestPruneBreaksTiesBetweenSetsOfOneCreatedMsLikeTheRunner(t *testing.T) {
	p := newPruneFixture(t, false)
	a, b, c := p.set(t, "verified", 100), p.set(t, "verified", 100), p.set(t, "verified", 100)
	if !(p.checkpoint(t, a) < p.checkpoint(t, b) && p.checkpoint(t, b) < p.checkpoint(t, c)) {
		t.Fatal("setup: the checkpoint ids are not ordered")
	}
	p.prune(t)
	if p.state(t, a) != "verified" || p.state(t, b) != "deleted" || p.state(t, c) != "deleted" {
		t.Fatalf("states %s %s %s: the set the runner calls newest (smallest checkpoint id) must be the one kept", p.state(t, a), p.state(t, b), p.state(t, c))
	}
}

// The runner refuses to delete the newest checkpoint of a namespace whose volume still exists, per namespace, while
// keep is per installation. The selection mirrors it, so such a refusal never burns an effect number.
func TestPruneNeverSelectsTheNewestVerifiedSetOfAnOlderNamespaceThatStillExists(t *testing.T) {
	eachPlatformDriver(t, func(t *testing.T, pg bool) {
		f := newRetFixture(t, pg)
		live := f.liveInstallation(1)
		older := f.namespace(live, "retained")
		current := f.namespace(live, "active")
		op, _ := f.operation(live, "install", "complete", retNow-1*retDay, 1)
		oldest := f.recoverySet(live, older, op, "verified", 1)
		first := f.recoverySet(live, current, op, "verified", 2)
		newest := f.recoverySet(live, current, op, "verified", 3)
		beyond := func() []string {
			ids, err := platformRecoverySetsBeyondRetention(f.st.poolExec(), 1)
			if err != nil {
				t.Fatal(err)
			}
			return ids
		}
		if got := beyond(); len(got) != 1 || got[0] != first {
			t.Fatalf("beyond = %v, want only %s: %s is the newest of a namespace that still exists and %s is kept", got, first, oldest, newest)
		}
		mustExec(t, f.st, `UPDATE app_platform_data_namespaces SET state='deleted' WHERE id=?`, older)
		if got := beyond(); len(got) != 2 {
			t.Fatalf("with the older namespace deleted its newest set is selectable: %v", got)
		}
	})
}

// A refusal that repeats must not use the effect numbers new deletions need: a retry never takes the reserved numbers
// and waits for the installation's next operation, while a first attempt still can.
func TestPruneRetriesLeaveTheReservedEffectNumbersToNewDeletions(t *testing.T) {
	p := newPruneFixture(t, false)
	refused := p.set(t, "verified", 100)
	p.set(t, "verified", 300)
	p.runner.refuse[p.checkpoint(t, refused)] = journal.DeleteOutcomeLatest
	for n := 2; n <= platformPruneMaxNumber-platformPruneRetryReserve-1; n++ {
		addEffectRow(t, p.s, p.hostOp, "delete-data", "remove_data", n, platformFixtureID(9500+n))
	}
	p.prune(t)
	if len(p.runner.sent) != 1 {
		t.Fatalf("the first attempt was not sent: %d", len(p.runner.sent))
	}
	if result := p.prune(t); result.Exhausted != 1 || len(p.runner.sent) != 1 {
		t.Fatalf("a retry took a reserved number: %+v, %d sent", result, len(p.runner.sent))
	}
	fresh := p.set(t, "verified", 50)
	if p.prune(t); !p.runner.effectIDs()[platformPruneEffectID(fresh, p.checkpoint(t, fresh), 0)] {
		t.Fatal("a first attempt could not use the reserved numbers")
	}
}

// Deletions that cannot progress hold no window slot for ever: with more of them than one window holds, new deletions
// still start and the open ones are visited in turn.
func TestPruneStuckDeletionsDoNotStallTheWindow(t *testing.T) {
	p := newPruneFixture(t, false)
	var sets []string
	for i := range platformPruneWindow + 2 {
		sets = append(sets, p.set(t, "verified", int64(10+i)))
	}
	p.set(t, "verified", 500)
	p.runner.pendingFor = 1000
	p.prune(t)
	p.prune(t)
	if len(p.runner.effectIDs()) != len(sets) {
		t.Fatalf("%d of %d deletions were started: the open ones stalled the window", len(p.runner.effectIDs()), len(sets))
	}
	p.runner.pendingFor = 0
	for range 4 {
		p.prune(t)
	}
	for _, id := range sets {
		if p.state(t, id) != "deleted" {
			t.Fatalf("set %s was never settled", id)
		}
	}
}

// A deletion open with no verdict for a day is reported for an operator, and is not reverted.
func TestPruneReportsADeletionOpenForADayAndDoesNotRevertIt(t *testing.T) {
	p := newPruneFixture(t, false)
	victim := p.set(t, "verified", 100)
	p.set(t, "verified", 300)
	p.runner.loseExecuteReplyOnce, p.runner.readFails = true, true
	p.prune(t)
	if refused, err := p.s.platformPruneRefusals(t.Context()); err != nil || len(refused) != 0 {
		t.Fatalf("a fresh deletion is reported: %+v %v", refused, err)
	}
	mustExec(t, p.s.st, `UPDATE app_platform_operation_effects SET updated_ms=1 WHERE kind='delete_checkpoint'`)
	refused, err := p.s.platformPruneRefusals(t.Context())
	if err != nil || len(refused) != 1 || refused[0].RecoverySetID != victim || refused[0].Reason != "outcome_unknown" || p.effects(t, "unknown") != 1 {
		t.Fatalf("%+v %v", refused, err)
	}
}

// Test for pin 9 on a leak-backlog set: the set is deleted from the start, so its refusals are pinned while no deletion
// of it is applied. Fails if they are swept (the attempt count resets and the same effect id is signed again under
// another host), and passes only once an applied deletion exists.
func TestRetentionKeepsTheRefusedDeletionsOfASetOfADeletedNamespaceUntilOneIsApplied(t *testing.T) {
	eachPlatformDriver(t, func(t *testing.T, pg bool) {
		f := newRetFixture(t, pg)
		old := retNow - 100*retDay
		removed := f.removedInstallation()
		namespace := f.namespace(removed, "deleted")
		cut, _ := f.operation(removed, "update", "complete", old, 2)
		leaked := f.recoverySet(removed, namespace, cut, "deleted", 1)
		host, _ := f.operation(removed, "disable", "complete", old, 3)
		named := `{"recovery_set_id":"` + leaked + `","outcome":"checkpoint_latest"}`
		f.effectAt(host, "remove_data", 2, "delete_checkpoint", "reverted", named)
		f.sweep(retNow)
		if !f.exists("operations", host) {
			t.Fatal("the host of a refused deletion of a leak-backlog set was swept: the attempt count would reset")
		}
		applied, _ := f.operation(removed, "disable", "complete", old, 4)
		f.effectAt(applied, "remove_data", 2, "delete_checkpoint", "applied", `{"recovery_set_id":"`+leaked+`","outcome":"removed"}`)
		f.sweep(retNow)
		if f.exists("operations", host) || !f.exists("operations", applied) {
			t.Fatal("after the deletion is applied the refusal may go and the applied host must stay")
		}
	})
}

// The retained-set predicate and the admission helper read the effect ledger once per query and never per set: no
// correlated scan of receipt_json may come back.
func TestPruneDoesNotScanTheEffectLedgerPerSet(t *testing.T) {
	for _, file := range []string{"app_platform_recovery_prune.go", "app_platform_retention.go", "app_platform_recovery_bundle.go"} {
		raw, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), "receipt_json LIKE") {
			t.Errorf("%s matches receipt_json with LIKE: that is a scan of the effect ledger per set", file)
		}
	}
}

// The step is housekeeping: from every operation ending and the idle tick together it runs at most once a minute, and
// it defers while any operation is in flight. Fails if an operation ending runs it again at once, or if it runs (and
// takes its selection reads and locks) under churn.
func TestPruneTickIsThrottledGloballyAndDefersWhileAnOperationRuns(t *testing.T) {
	p := newPruneFixture(t, false)
	p.set(t, "verified", 100)
	p.set(t, "verified", 300)
	p.s.pruneRecoverySetsOnTick(t.Context(), p.endpoint)
	first := p.s.platformPruneLast.Load()
	if first == 0 || len(p.runner.sent) != 1 {
		t.Fatalf("the first tick did not run: %d sent", len(p.runner.sent))
	}
	p.set(t, "verified", 50)
	p.s.pruneRecoverySetsOnTick(t.Context(), p.endpoint)
	if len(p.runner.sent) != 1 || p.s.platformPruneLast.Load() != first {
		t.Fatal("a second tick inside the interval ran the step")
	}
	// Past the interval but with an operation in flight: deferred, nothing read.
	p.s.platformPruneLast.Store(first - 2*platformPruneInterval.Milliseconds())
	p.s.platformPruneMore.Store(false)
	deferred := p.s.platformPruneLast.Load()
	mustExec(t, p.s.st, `UPDATE app_platform_operations SET stage='queued',completed_ms=NULL WHERE id=?`, p.hostOp)
	p.s.pruneRecoverySetsOnTick(t.Context(), p.endpoint)
	if p.s.platformPruneLast.Load() != deferred || len(p.runner.sent) != 1 {
		t.Fatal("the step ran while an operation was in flight")
	}
	mustExec(t, p.s.st, `UPDATE app_platform_operations SET stage='complete',completed_ms=? WHERE id=?`, first, p.hostOp)
	p.s.pruneRecoverySetsOnTick(t.Context(), p.endpoint)
	if len(p.runner.sent) != 2 {
		t.Fatalf("the step did not run once the installation was idle: %d sent", len(p.runner.sent))
	}
}

// strand marks every open deletion of the fixture stranded, as the driver does when its host cannot settle it.
func (p *pruneFixture) strandAll(t *testing.T, updated int64) {
	t.Helper()
	rows, err := p.s.st.query(`SELECT effect_id,receipt_json FROM app_platform_operation_effects WHERE kind='delete_checkpoint' AND state='unknown'`)
	if err != nil {
		t.Fatal(err)
	}
	type row struct{ id, raw string }
	var list []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.raw); err != nil {
			t.Fatal(err)
		}
		list = append(list, r)
	}
	rows.Close()
	for _, r := range list {
		mustExec(t, p.s.st, `UPDATE app_platform_operation_effects SET receipt_json=?,updated_ms=? WHERE effect_id=?`, strings.TrimSuffix(r.raw, "}")+`,"stranded":true}`, updated, r.id)
	}
}

// Only refusals count toward the attempt cap. A set whose eight earlier rows are all stranded still gets its retry,
// which settles it and closes them. Fails if the cap counts stranded rows (the set would stay decided for ever).
func TestPruneStrandedRowsNeverCountTowardTheAttemptCap(t *testing.T) {
	eachPlatformDriver(t, func(t *testing.T, pg bool) {
		p := newPruneFixture(t, pg)
		victim := p.set(t, "verified", 100)
		p.set(t, "verified", 300)
		for n := 2; n < 2+platformPruneMaxAttempts; n++ {
			platformInsertDeleteCheckpointRow(t, p.s, p.hostOp, victim, "unknown", n)
		}
		p.strandAll(t, time.Now().UnixMilli())
		p.prune(t)
		if p.state(t, victim) != "deleted" || p.effects(t, "applied") != 1 || p.effects(t, "unknown") != 0 {
			t.Fatal("a set with eight stranded rows was not retried to a verdict")
		}
		if scalar[int](t, p.s.st, `SELECT COUNT(*) FROM app_platform_operation_effects WHERE receipt_json LIKE '%superseded_applied%'`) != platformPruneMaxAttempts {
			t.Fatal("the stranded rows were not closed by the verdict")
		}
	})
}

// A stranded row that has waited a day is reported like any other unknown.
func TestPruneReportsAStaleStrandedRow(t *testing.T) {
	p := newPruneFixture(t, false)
	victim := p.set(t, "verified", 100)
	p.set(t, "verified", 300)
	platformInsertDeleteCheckpointRow(t, p.s, p.hostOp, victim, "unknown", 2)
	p.strandAll(t, 1)
	refused, err := p.s.platformPruneRefusals(t.Context())
	if err != nil || len(refused) != 1 || refused[0].RecoverySetID != victim || refused[0].Reason != "outcome_unknown" {
		t.Fatalf("%+v %v", refused, err)
	}
}

// A pass that made progress lets the next one follow after the interval even while operations run, so a backlog
// drains at a bounded rate; a pass that did nothing defers while any operation runs. Fails if progress is ignored
// (the backlog would drain one window an hour under churn).
func TestPruneAfterProgressContinuesWhileOperationsRun(t *testing.T) {
	p := newPruneFixture(t, false)
	p.set(t, "verified", 100)
	p.set(t, "verified", 300)
	mustExec(t, p.s.st, `UPDATE app_platform_operations SET stage='queued',completed_ms=NULL WHERE id=?`, p.hostOp)
	past := time.Now().UnixMilli() - 5*platformPruneInterval.Milliseconds()
	p.s.platformPruneLast.Store(past)
	p.s.pruneRecoverySetsOnTick(t.Context(), p.endpoint)
	if p.s.platformPruneLast.Load() != past {
		t.Fatal("a pass with no progress ran while an operation was in flight")
	}
	p.s.platformPruneMore.Store(true)
	p.s.pruneRecoverySetsOnTick(t.Context(), p.endpoint)
	if p.s.platformPruneLast.Load() == past {
		t.Fatal("a pass after progress was deferred by an operation in flight")
	}
	if p.s.platformPruneMore.Load() {
		t.Fatal("a pass that did nothing kept the progress flag")
	}
}

// The reads behind the retained predicate and the selection are bounded by the sets they ask about: a long history of
// deletions of other sets changes neither the answer nor the statements (no scan of the ledger by kind and state).
func TestPruneReadsAreBoundedByTheSetsNotTheLedger(t *testing.T) {
	eachPlatformDriver(t, func(t *testing.T, pg bool) {
		f := newRetFixture(t, pg)
		removed := f.removedInstallation()
		namespace := f.namespace(removed, "active")
		cut, _ := f.operation(removed, "update", "complete", retNow-1*retDay, 2)
		decided := f.recoverySet(removed, namespace, cut, "verified", 1)
		plain := f.recoverySet(removed, namespace, cut, "verified", 2)
		platformInsertDeleteCheckpointRow(t, f.s, cut, decided, "applied", 2)
		// A long history: applied deletions of sets that no longer matter, each on a stage of its own.
		for i := range 400 {
			f.effectAt(cut, "history"+strconv.Itoa(i), 2, "delete_checkpoint", "applied", `{"recovery_set_id":"`+f.id()+`","outcome":"removed"}`)
		}
		e := f.st.poolExec()
		retained, err := loadPlatformRetainedSets(e)
		if err != nil || len(retained) != 1 || retained[0].id != plain {
			t.Fatalf("retained = %+v, %v", retained, err)
		}
	})
	raw, err := os.ReadFile("app_platform_recovery_prune.go")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "state IN ('prepared','unknown','applied')") {
		t.Error("a read scans the applied effects of every set by state")
	}
}
