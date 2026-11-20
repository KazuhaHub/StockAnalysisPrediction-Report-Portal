package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

// DATA-08: retention defaults and the sweeper (doc 167 section 5, doc 172). Each test runs on SQLite and, when
// TEST_POSTGRES_DSN is set, on PostgreSQL, and every one drives the sweep with a synthetic clock.

func bothDrivers(t *testing.T, run func(t *testing.T, pg bool)) {
	t.Helper()
	t.Run("sqlite", func(t *testing.T) { run(t, false) })
	t.Run("postgres", func(t *testing.T) { run(t, true) })
}

func TestPlatformRetentionReviewPlans(t *testing.T) { bothDrivers(t, testPlatformRetentionReviewPlans) }

func testPlatformRetentionReviewPlans(t *testing.T, pg bool) {
	f := newRetFixture(t, pg)
	live := f.liveInstallation(1)
	hour := int64(60 * 60 * 1000)
	expiredLong := f.plan(live, "install", retNow-25*hour, 0)
	expiredShort := f.plan(live, "install", retNow-23*hour, 0)
	unexpired := f.plan(live, "install", retNow+hour, 0)
	referenced := f.plan(live, "install", retNow-30*retDay, 0)
	f.capabilityGrant(live, referenced)
	consumedOld := f.plan(live, "install", retNow-30*retDay, 1)

	result := f.sweep(retNow)

	for id, want := range map[string]bool{expiredLong: false, expiredShort: true, unexpired: true, referenced: true, consumedOld: true} {
		if got := f.exists("review_plans", id); got != want {
			t.Errorf("plan %s exists=%v, want %v", id, got, want)
		}
	}
	if result.Plans != 1 {
		t.Errorf("swept %d unconsumed plans, want 1", result.Plans)
	}
}

// opSet names the operations of one retention scenario and whether the sweep must remove each.
type opSet struct {
	f       *retFixture
	deleted []string
	kept    []string
	plans   map[string]string
}

func (s *opSet) expect(op, plan string, deleted bool) {
	s.plans[op] = plan
	if deleted {
		s.deleted = append(s.deleted, op)
	} else {
		s.kept = append(s.kept, op)
	}
}

func (s *opSet) check(t *testing.T) {
	t.Helper()
	for _, op := range s.deleted {
		if s.f.exists("operations", op) {
			t.Errorf("operation %s was kept, want deleted", op)
		}
		if n := scalar[int](t, s.f.st, `SELECT COUNT(*) FROM app_platform_operation_effects WHERE operation_id=?`, op); n != 0 {
			t.Errorf("operation %s left %d effects behind", op, n)
		}
		if s.f.exists("review_plans", s.plans[op]) {
			t.Errorf("operation %s left its plan behind", op)
		}
	}
	for _, op := range s.kept {
		if !s.f.exists("operations", op) {
			t.Errorf("operation %s was deleted, want kept", op)
		}
		if !s.f.exists("review_plans", s.plans[op]) {
			t.Errorf("operation %s lost its plan", op)
		}
	}
}

func TestPlatformRetentionOperations(t *testing.T) { bothDrivers(t, testPlatformRetentionOperations) }

func testPlatformRetentionOperations(t *testing.T, pg bool) {
	f := newRetFixture(t, pg)
	set := &opSet{f: f, plans: map[string]string{}}
	old := retNow - 100*retDay
	recent := retNow - 10*retDay
	cut := retNow - 90*retDay

	add := func(installation, action, stage string, completed, generation int64, deleted bool) string {
		op, plan := f.operation(installation, action, stage, completed, generation)
		set.expect(op, plan, deleted)
		return op
	}

	t.Run("a removed installation with nothing left is swept past the limit only", func(t *testing.T) {
		removed := f.removedInstallation()
		eligible := add(removed, "install", "complete", old, 1, true)
		f.effect(eligible, "prepare-data", "applied", "{}")
		add(removed, "disable", "failed", old, 1, true)
		add(removed, "uninstall", "complete", recent, 1, false)
		add(removed, "delete_data", "complete", cut, 1, false)
		add(removed, "disable", "complete", cut-1, 1, true)
	})
	t.Run("an unresolved effect pins its operation", func(t *testing.T) {
		removed := f.removedInstallation()
		prepared := add(removed, "install", "complete", old, 1, false)
		f.effect(prepared, "prepare-data", "prepared", "{}")
		unknown := add(removed, "disable", "complete", old, 1, false)
		f.effect(unknown, "stop", "unknown", "{}")
		add(removed, "uninstall", "complete", old, 1, true)
	})
	t.Run("retained or unresolved data pins the installation", func(t *testing.T) {
		for _, state := range []string{"active", "retained", "deleting", "review_required"} {
			removed := f.removedInstallation()
			f.namespace(removed, state)
			add(removed, "install", "complete", old, 1, false)
			add(removed, "uninstall", "complete", old, 1, false)
		}
		removed := f.removedInstallation()
		f.namespace(removed, "deleted")
		add(removed, "install", "complete", old, 1, true)
	})
	t.Run("an unfinished or recovery-required operation pins the whole chain", func(t *testing.T) {
		running := f.removedInstallation()
		add(running, "install", "complete", old, 1, false)
		add(running, "uninstall", "queued", 0, 1, false)
		recovery := f.removedInstallation()
		add(recovery, "install", "complete", old, 1, false)
		add(recovery, "restore", "recovery_required", old, 1, false)
		add(recovery, "disable", "failed", old, 1, false)
	})
	t.Run("a recovery set pins the operation that cut it", func(t *testing.T) {
		removed := f.removedInstallation()
		namespace := f.namespace(removed, "deleted")
		for _, state := range []string{"verified", "restoring", "review_required", "preparing", "deleted"} {
			op := add(removed, "update", "complete", old, 2, false)
			f.recoverySet(removed, namespace, op, state, 1)
			// The snapshot receipt names the operation itself as its source, so only that operation is pinned.
			data := f.effect(op, "prepare-data", "applied", "{}")
			f.effectAt(op, "snapshot", 1, "checkpoint", "applied", `{"source_operation_id":"`+op+`","source_data_effect_id":"`+data+`"}`)
		}
		add(removed, "disable", "complete", old, 2, true)
	})
	t.Run("an import grant pins the operation of its recovery set", func(t *testing.T) {
		removed := f.removedInstallation()
		namespace := f.namespace(removed, "deleted")
		for _, grant := range []string{"issued", "consumed", "revoked"} {
			op := add(removed, "update", "complete", old, 2, false)
			f.importGrant(f.recoverySet(removed, namespace, op, "deleted", 1), grant)
		}
	})
	t.Run("a retained checkpoint pins the source operation its receipt names", func(t *testing.T) {
		removed := f.removedInstallation()
		namespace := f.namespace(removed, "deleted")
		source := add(removed, "install", "complete", old, 1, false)
		dataOwner := add(removed, "update", "complete", old, 2, false)
		dataEffect := f.effect(dataOwner, "prepare-data", "applied", "{}")
		checkpointOp := add(removed, "update", "complete", old, 3, false)
		f.recoverySet(removed, namespace, checkpointOp, "verified", 1)
		f.effectAt(checkpointOp, "snapshot", 1, "checkpoint", "applied",
			`{"source_operation_id":"`+source+`","source_data_effect_id":"`+dataEffect+`"}`)
		add(removed, "disable", "complete", old, 3, true)

		unreadable := f.removedInstallation()
		namespace = f.namespace(unreadable, "deleted")
		op := add(unreadable, "update", "complete", old, 3, false)
		f.recoverySet(unreadable, namespace, op, "verified", 1)
		f.effectAt(op, "snapshot", 1, "checkpoint", "applied", "not json")
		add(unreadable, "disable", "complete", old, 3, false)

		missing := f.removedInstallation()
		namespace = f.namespace(missing, "deleted")
		op = add(missing, "update", "complete", old, 3, false)
		f.recoverySet(missing, namespace, op, "verified", 1)
		add(missing, "disable", "complete", old, 3, false)
	})
	t.Run("an operation whose plan a grant still names is read through that plan", func(t *testing.T) {
		removed := f.removedInstallation()
		f.namespace(removed, "deleted")
		granted := add(removed, "install", "complete", old, 1, false)
		f.capabilityGrant(removed, set.plans[granted])
		add(removed, "disable", "complete", old, 1, true)
	})
	t.Run("a live installation keeps the chain its readers use", func(t *testing.T) {
		live := f.liveInstallation(3)
		add(live, "install", "complete", retNow-200*retDay, 1, false)
		add(live, "update", "complete", retNow-190*retDay, 2, true)
		add(live, "disable", "complete", retNow-180*retDay, 2, true)
		add(live, "update", "failed", retNow-170*retDay, 2, true)
		add(live, "update", "complete", retNow-160*retDay, 3, false)
		add(live, "disable", "complete", retNow-150*retDay, 2, false)
		add(live, "update", "failed", retNow-120*retDay, 3, false)
		// An operation of the current generation is part of the chain even when a later one of its action exists.
		add(live, "enable", "complete", retNow-140*retDay, 3, false)
		add(live, "enable", "complete", retNow-130*retDay, 3, false)
		add(live, "repair", "complete", retNow-135*retDay, 1, true)
		add(live, "repair", "complete", retNow-134*retDay, 1, false)
	})
	t.Run("an image still on the runner keeps the operations that loaded it", func(t *testing.T) {
		removed := f.removedInstallation()
		f.namespace(removed, "deleted")
		config := func(seed string) string { return platformDigest([]byte(seed)) }
		open := add(removed, "install", "complete", old, 1, false)
		f.effect(open, "load_image", "applied", f.loadReceipt(open, removed, config("open")))

		loaded := add(removed, "update", "complete", old, 2, true)
		f.effect(loaded, "load_image", "applied", f.loadReceipt(loaded, removed, config("closed")))
		removal := add(removed, "update", "complete", old, 3, true)
		f.effect(removal, "remove_image", "applied", f.removeReceipt(config("closed")))

		first := add(removed, "update", "complete", old, 4, false)
		f.effect(first, "load_image", "applied", f.loadReceipt(first, removed, config("reloaded")))
		second := add(removed, "update", "complete", old, 5, false)
		f.effect(second, "remove_image", "applied", f.removeReceipt(config("reloaded")))
		third := add(removed, "update", "complete", old, 6, false)
		f.effect(third, "load_image", "applied", f.loadReceipt(third, removed, config("reloaded")))

		unreadable := add(removed, "update", "complete", old, 7, false)
		f.effect(unreadable, "load_image", "applied", "{}")

		reverted := add(removed, "update", "complete", old, 8, true)
		f.effect(reverted, "load_image", "reverted", f.loadReceipt(reverted, removed, config("reverted")))
	})

	f.sweep(retNow)
	set.check(t)
}

func TestPlatformRetentionHonoursTheConfiguredPeriod(t *testing.T) {
	f := newRetFixture(t, false)
	removed := f.removedInstallation()
	op, _ := f.operation(removed, "disable", "complete", retNow-100*retDay, 1)
	f.s.st.SetSetting(platformRetentionOperationDaysKey, "180")
	f.sweep(retNow)
	if !f.exists("operations", op) {
		t.Fatal("a 100-day-old operation was removed under a 180-day period")
	}
	f.sweep(retNow + 81*retDay)
	if f.exists("operations", op) {
		t.Fatal("the operation outlived the configured period")
	}
}

func TestPlatformRetentionIdempotency(t *testing.T) { bothDrivers(t, testPlatformRetentionIdempotency) }

func testPlatformRetentionIdempotency(t *testing.T, pg bool) {
	f := newRetFixture(t, pg)
	removed := f.removedInstallation()
	inside, _ := f.operation(removed, "disable", "complete", retNow-60*retDay, 1)
	outside, _ := f.operation(removed, "disable", "complete", retNow-100*retDay, 1)
	replay := func(op string) (platformRemovalCommit, bool) {
		t.Helper()
		found, exists, err := platformFindRemovalCommit(f.st.poolExec(), f.hash, f.keyOf(op), f.hash)
		if err != nil {
			t.Fatal(err)
		}
		return found, exists
	}
	for _, op := range []string{inside, outside} {
		if found, exists := replay(op); !exists || found.OperationID != op || !found.Replayed {
			t.Fatalf("before the sweep the key of %s did not replay: %+v %v", op, found, exists)
		}
	}
	f.sweep(retNow)
	if found, exists := replay(inside); !exists || found.OperationID != inside {
		t.Fatalf("a key inside the period no longer returns the original operation: %+v %v", found, exists)
	}
	if _, exists := replay(outside); exists {
		t.Fatal("a key past the period still resolves to the swept operation; it must behave as a new request")
	}
}

func TestPlatformRetentionTasks(t *testing.T) { bothDrivers(t, testPlatformRetentionTasks) }

func testPlatformRetentionTasks(t *testing.T, pg bool) {
	f := newRetFixture(t, pg)
	live := f.liveInstallation(1)
	old, recent := retNow-40*retDay, retNow-10*retDay

	reduced := f.task(live, "old-succeeded", "succeeded", old, true)
	cancelled := f.task(live, "old-cancelled", "cancelled", old, false)
	failed := f.task(live, "old-failed", "failed", old, true)
	fresh := f.task(live, "recent-failed", "failed", recent, true)
	waiting := f.task(live, "waiting", "waiting", 0, false)
	uncertain := f.task(live, "uncertain", "uncertain", 0, true)
	review := f.task(live, "review", "review_required", 0, true)
	tombstoneOld := f.task(live, "tombstone-old", "succeeded", retNow-100*retDay, false)
	tombstoneNew := f.task(live, "tombstone-new", "succeeded", retNow-50*retDay, false)
	for _, id := range []string{tombstoneOld, tombstoneNew} {
		if _, err := f.st.exec(`UPDATE app_platform_tasks SET payload=`+retEmptyBlob(f.st)+`,result_json=NULL WHERE id=?`, id); err != nil {
			t.Fatal(err)
		}
	}
	before := map[string]retTask{}
	for _, id := range []string{fresh, waiting, uncertain, review} {
		before[id] = f.taskRow(id)
	}

	result := f.sweep(retNow)

	for _, id := range []string{reduced, cancelled, failed} {
		row := f.taskRow(id)
		if !row.exists || row.payloadLen != 0 || row.hasResult || row.attempts != 0 {
			t.Errorf("task %s was not reduced to a tombstone: %+v", id, row)
		}
		if row.occurrence == "" || row.handler == "" || len(row.payload) != 64 {
			t.Errorf("task %s lost its occurrence, handler or payload hash: %+v", id, row)
		}
	}
	for id, was := range before {
		if got := f.taskRow(id); got != was {
			t.Errorf("task %s changed: %+v -> %+v", id, was, got)
		}
	}
	if f.taskRow(tombstoneOld).exists {
		t.Error("a tombstone past the operations period was kept")
	}
	if !f.taskRow(tombstoneNew).exists {
		t.Error("a tombstone inside the operations period was removed")
	}
	if result.TasksReduced != 3 || result.TaskAttempts != 2 || result.Tombstones != 1 {
		t.Errorf("counts = reduced %d attempts %d tombstones %d, want 3, 2, 1", result.TasksReduced, result.TaskAttempts, result.Tombstones)
	}
}

func retEmptyBlob(st *Store) string {
	if st.driver == "postgres" {
		return `decode('','hex')`
	}
	return `X''`
}

func TestPlatformRetentionTaskCapPerInstallation(t *testing.T) {
	bothDrivers(t, func(t *testing.T, pg bool) {
		f := newRetFixture(t, pg)
		crowded, quiet := f.liveInstallation(1), f.liveInstallation(1)
		var ids []string
		for i := 0; i < 5; i++ {
			ids = append(ids, f.task(crowded, "crowded-"+strconv.Itoa(i), "succeeded", retNow-int64(5-i)*retDay, true))
		}
		var others []string
		for i := 0; i < 3; i++ {
			others = append(others, f.task(quiet, "quiet-"+strconv.Itoa(i), "succeeded", retNow-int64(5-i)*retDay, false))
		}
		result, err := f.s.sweepPlatformRetention(context.Background(), retNow, platformRetentionLimits{Batch: 500, TaskCap: 3})
		if err != nil {
			t.Fatal(err)
		}
		for i, id := range ids {
			if reduced := f.taskRow(id).payloadLen == 0; reduced != (i < 2) {
				t.Errorf("task %d reduced=%v, want the two oldest only", i, reduced)
			}
		}
		for _, id := range others {
			if f.taskRow(id).payloadLen == 0 {
				t.Error("an installation inside the cap lost a task")
			}
		}
		if result.TasksCapped != 2 || result.TasksReduced != 0 {
			t.Errorf("counts = capped %d reduced %d, want 2 and 0", result.TasksCapped, result.TasksReduced)
		}
	})
}

func TestPlatformRetentionDefaultTaskCap(t *testing.T) {
	if limits := defaultPlatformRetentionLimits(); limits.TaskCap != 10000 || limits.Batch != 500 {
		t.Fatalf("default limits = %+v, want a 10000-row cap and 500-row batches", limits)
	}
}

func TestPlatformRetentionWorksInBoundedBatches(t *testing.T) {
	f := newRetFixture(t, false)
	live := f.liveInstallation(1)
	for i := 0; i < 5; i++ {
		f.task(live, "old-"+strconv.Itoa(i), "succeeded", retNow-40*retDay, false)
	}
	limits := platformRetentionLimits{Batch: 2, TaskCap: 10000}
	reduced := 0
	for round, want := range []struct {
		n       int64
		backlog bool
	}{{2, true}, {2, true}, {1, false}} {
		result, err := f.s.sweepPlatformRetention(context.Background(), retNow, limits)
		if err != nil {
			t.Fatal(err)
		}
		if result.TasksReduced != want.n || result.Backlog != want.backlog {
			t.Fatalf("round %d: reduced %d backlog %v, want %d %v", round, result.TasksReduced, result.Backlog, want.n, want.backlog)
		}
		reduced += int(result.TasksReduced)
	}
	if reduced != 5 {
		t.Fatalf("reduced %d tasks in all", reduced)
	}
}

func TestPlatformRetentionSweptTaskOccurrenceStaysDeduplicated(t *testing.T) {
	bothDrivers(t, func(t *testing.T, pg bool) {
		s, installation := platformServiceIdentityFixtureWithDatabase(t, pg)
		now, err := platformDatabaseNow(s.st.poolExec())
		if err != nil {
			t.Fatal(err)
		}
		in := platformTaskAdmission{OccurrenceID: "swept-occurrence", Handler: appplatform.TaskHandlerBackend, Payload: []byte(`{"a":1}`), ExpiresMS: now + 60000}
		id, created, err := s.admitPlatformTask(context.Background(), installation, in)
		if err != nil || !created {
			t.Fatalf("first admission: %v %v", created, err)
		}
		mustExec(t, s.st, `UPDATE app_platform_tasks SET state='succeeded',completed_ms=created_ms,result_json='{"ok":true}' WHERE id=?`, id)

		if _, err := s.sweepPlatformRetention(context.Background(), now+40*retDay, defaultPlatformRetentionLimits()); err != nil {
			t.Fatal(err)
		}
		var length int
		if err := s.st.queryRow(`SELECT `+platformByteLength("payload", s.st.driver)+` FROM app_platform_tasks WHERE id=?`, id).Scan(&length); err != nil || length != 0 {
			t.Fatalf("the task was not reduced: payload length %d, %v", length, err)
		}
		again, created, err := s.admitPlatformTask(context.Background(), installation, in)
		if err != nil || created || again != id {
			t.Fatalf("a replayed occurrence of a swept task was not deduplicated: %q %v %v", again, created, err)
		}
		in.Payload = []byte(`{"a":2}`)
		if _, _, err := s.admitPlatformTask(context.Background(), installation, in); !errors.Is(err, errAppPlatformTaskConflict) {
			t.Fatalf("a different payload for a swept occurrence = %v, want a conflict", err)
		}

		// Past the operations period the tombstone goes, and the same occurrence is a new request.
		if _, err := s.sweepPlatformRetention(context.Background(), now+100*retDay, defaultPlatformRetentionLimits()); err != nil {
			t.Fatal(err)
		}
		in.Payload = []byte(`{"a":1}`)
		fresh, created, err := s.admitPlatformTask(context.Background(), installation, in)
		if err != nil || !created || fresh == id {
			t.Fatalf("an occurrence past the period was not a new request: %q %v %v", fresh, created, err)
		}
	})
}

func TestPlatformRetentionNeverTouchesRetainedData(t *testing.T) {
	bothDrivers(t, func(t *testing.T, pg bool) {
		f := newRetFixture(t, pg)
		removed := f.removedInstallation()
		namespace := f.namespace(removed, "retained")
		op, _ := f.operation(removed, "uninstall", "complete", retNow-200*retDay, 1)
		set := f.recoverySet(removed, namespace, op, "verified", 1)
		before := scalar[string](t, f.st, `SELECT state||':'||revision||':'||observed_bytes||':'||COALESCE(owner_installation_id,'') FROM app_platform_data_namespaces WHERE id=?`, namespace)
		f.sweep(retNow + 400*retDay)
		if got := scalar[string](t, f.st, `SELECT state||':'||revision||':'||observed_bytes||':'||COALESCE(owner_installation_id,'') FROM app_platform_data_namespaces WHERE id=?`, namespace); got != before {
			t.Fatalf("a retained namespace changed: %s -> %s", before, got)
		}
		if !f.exists("recovery_sets", set) || !f.exists("operations", op) || !f.exists("installations", removed) {
			t.Fatal("the sweep removed the installation, its recovery set or the operation that holds them")
		}
		if state := scalar[string](t, f.st, `SELECT state FROM app_platform_recovery_sets WHERE id=?`, set); state != "verified" {
			t.Fatalf("recovery set state = %s", state)
		}
	})
}

func TestPlatformRetentionWritesOneAuditRowPerSweep(t *testing.T) {
	bothDrivers(t, func(t *testing.T, pg bool) {
		f := newRetFixture(t, pg)
		removed := f.removedInstallation()
		op, _ := f.operation(removed, "disable", "complete", retNow-100*retDay, 1)
		f.effect(op, "stop", "applied", "{}")
		f.plan(removed, "install", retNow-30*retDay, 0)
		f.task(removed, "swept", "succeeded", retNow-40*retDay, true)

		auditRows := func() int {
			return scalar[int](t, f.st, `SELECT COUNT(*) FROM audit_log WHERE action='app_platform.retention_sweep'`)
		}
		f.sweep(retNow)
		if auditRows() != 1 {
			t.Fatalf("audit rows = %d after one sweep", auditRows())
		}
		var actor, targetType, detail string
		if err := f.st.queryRow(`SELECT actor,target_type,detail FROM audit_log WHERE action='app_platform.retention_sweep'`).Scan(&actor, &targetType, &detail); err != nil {
			t.Fatal(err)
		}
		var counts map[string]any
		if err := json.Unmarshal([]byte(detail), &counts); err != nil {
			t.Fatalf("detail is not JSON: %v", err)
		}
		want := map[string]float64{"plans": 1, "operations": 1, "effects": 1, "operation_plans": 1, "tasks_reduced": 1, "task_attempts": 1, "tasks_capped": 0, "tombstones": 0, "operation_days": 90, "recovery_sets_kept": 2}
		for key, value := range want {
			if got, ok := counts[key].(float64); !ok || got != value {
				t.Errorf("detail %s = %v, want %v (%s)", key, counts[key], value, detail)
			}
		}
		if actor != "system" || targetType != "app_platform" {
			t.Errorf("audit actor=%q target=%q", actor, targetType)
		}
		f.sweep(retNow)
		if auditRows() != 2 {
			t.Fatalf("a sweep that removed nothing wrote %d rows in all, want one per sweep", auditRows())
		}
	})
}

func TestPlatformRetentionDoesNothingWhilePaused(t *testing.T) {
	bothDrivers(t, func(t *testing.T, pg bool) {
		f := newRetFixture(t, pg)
		removed := f.removedInstallation()
		op, _ := f.operation(removed, "disable", "complete", retNow-100*retDay, 1)
		plan := f.plan(removed, "install", retNow-30*retDay, 0)
		task := f.task(removed, "t", "succeeded", retNow-40*retDay, false)
		f.st.SetSetting("app_platform_restore_paused", "1")

		result := f.sweep(retNow)
		if !result.Paused {
			t.Fatal("the sweep did not report the pause")
		}
		if !f.exists("operations", op) || !f.exists("review_plans", plan) || f.taskRow(task).payloadLen == 0 {
			t.Fatal("the sweep removed something during the restore pause")
		}
		if n := scalar[int](t, f.st, `SELECT COUNT(*) FROM audit_log WHERE action='app_platform.retention_sweep'`); n != 0 {
			t.Fatalf("a paused sweep wrote %d audit rows", n)
		}

		f.st.SetSetting("app_platform_restore_paused", "0")
		f.sweep(retNow)
		if f.exists("operations", op) || f.exists("review_plans", plan) {
			t.Fatal("the sweep did not run once the pause ended")
		}
	})
}

func TestPlatformRetentionFailsClosedWhenThePauseIsUnreadable(t *testing.T) {
	f := newRetFixture(t, false)
	removed := f.removedInstallation()
	op, _ := f.operation(removed, "disable", "complete", retNow-100*retDay, 1)
	mustExec(t, f.st, `ALTER TABLE meta RENAME TO meta_hidden`)
	_, err := f.s.sweepPlatformRetention(context.Background(), retNow, defaultPlatformRetentionLimits())
	mustExec(t, f.st, `ALTER TABLE meta_hidden RENAME TO meta`)
	if err == nil {
		t.Fatal("an unreadable pause marker did not stop the sweep")
	}
	if !f.exists("operations", op) {
		t.Fatal("the sweep removed rows with the pause marker unreadable")
	}
}

func TestPlatformRetentionConfigClampsOnRead(t *testing.T) {
	s := &Server{st: newTestStore(t)}
	for _, tc := range []struct {
		days, sets string
		wantDays   int
		wantSets   int
	}{
		{"", "", 90, 2},
		{"junk", "junk", 90, 2},
		{"30", "0", 90, 1},
		{"89", "-4", 90, 1},
		{"90", "1", 90, 1},
		{"200", "2", 200, 2},
		{"365", "2", 365, 2},
		{"9999", "9", 365, 2},
	} {
		s.st.SetSetting(platformRetentionOperationDaysKey, tc.days)
		s.st.SetSetting(platformRetentionRecoverySetsKey, tc.sets)
		got := s.platformRetentionConfigLoad()
		if got.OperationDays != tc.wantDays || got.RecoverySets != tc.wantSets {
			t.Errorf("stored %q/%q read as %+v, want %d/%d", tc.days, tc.sets, got, tc.wantDays, tc.wantSets)
		}
	}
}

func TestPlatformRetentionConfigAPIValidatesOnSave(t *testing.T) {
	s := &Server{st: newTestStore(t)}
	save := func(body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		s.apiPlatformRetentionSave(rec, httptest.NewRequest(http.MethodPut, "/api/admin/app-platform/retention", strings.NewReader(body)), "admin")
		return rec
	}
	for _, body := range []string{
		`{"operation_days":89}`, `{"operation_days":366}`, `{"operation_days":0}`, `{"recovery_sets":0}`, `{"recovery_sets":3}`,
		`{"operation_days":120,"recovery_sets":3}`, `{"operation_days":"x"}`,
	} {
		if rec := save(body); rec.Code != http.StatusBadRequest {
			t.Errorf("%s answered %d, want 400", body, rec.Code)
		}
	}
	if s.st.GetSetting(platformRetentionOperationDaysKey, "") != "" || s.st.GetSetting(platformRetentionRecoverySetsKey, "") != "" {
		t.Fatal("a rejected save persisted a value, even one that was valid on its own")
	}
	if rec := save(`{"operation_days":120}`); rec.Code != http.StatusOK {
		t.Fatalf("valid save answered %d: %s", rec.Code, rec.Body)
	}
	if rec := save(`{"recovery_sets":1}`); rec.Code != http.StatusOK {
		t.Fatalf("valid partial save answered %d: %s", rec.Code, rec.Body)
	}
	if got := s.platformRetentionConfigLoad(); got.OperationDays != 120 || got.RecoverySets != 1 {
		t.Fatalf("saved config = %+v", got)
	}

	rec := httptest.NewRecorder()
	s.apiPlatformRetentionGet(rec, httptest.NewRequest(http.MethodGet, "/api/admin/app-platform/retention", nil), "admin")
	var out struct {
		OperationDays int            `json:"operation_days"`
		RecoverySets  int            `json:"recovery_sets"`
		Bounds        map[string]int `json:"bounds"`
		Fixed         map[string]any `json:"fixed"`
		LastSweep     any            `json:"last_sweep"`
		RecoveryPrune string         `json:"recovery_set_pruning"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.OperationDays != 120 || out.RecoverySets != 1 || out.Bounds["operation_days_min"] != 90 || out.Bounds["operation_days_max"] != 365 ||
		out.Bounds["recovery_sets_min"] != 1 || out.Bounds["recovery_sets_max"] != 2 {
		t.Fatalf("GET answered %+v", out)
	}
	if out.LastSweep != nil {
		t.Fatalf("a portal that never swept reported %v", out.LastSweep)
	}
	for _, key := range []string{"review_plan_minutes", "review_plan_grace_hours", "task_days", "task_rows_per_installation", "runner_log_mib", "uninstalled_data"} {
		if _, ok := out.Fixed[key]; !ok {
			t.Errorf("the fixed values omit %s", key)
		}
	}
	if out.RecoveryPrune != "enabled" {
		t.Errorf("recovery_set_pruning = %q", out.RecoveryPrune)
	}
}

func TestPlatformRetentionConfigAPIReportsTheLastSweep(t *testing.T) {
	f := newRetFixture(t, false)
	removed := f.removedInstallation()
	f.operation(removed, "disable", "complete", retNow-100*retDay, 1)
	f.sweep(retNow)
	rec := httptest.NewRecorder()
	f.s.apiPlatformRetentionGet(rec, httptest.NewRequest(http.MethodGet, "/api/admin/app-platform/retention", nil), "admin")
	var out struct {
		LastSweep *struct {
			At     string         `json:"at"`
			Counts map[string]any `json:"counts"`
		} `json:"last_sweep"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || out.LastSweep == nil || out.LastSweep.At == "" || out.LastSweep.Counts["operations"] != float64(1) {
		t.Fatalf("last sweep = %+v, %v", out.LastSweep, err)
	}
}

// The recovery-set retention is selected on the Portal side and only reported by the sweep: prunePlatformRecoverySets,
// not the sweep, asks the runner to delete the archives, so the sweep marks nothing deleted.
func TestPlatformRetentionSelectsRecoverySetsBeyondTheLatestN(t *testing.T) {
	f := newRetFixture(t, false)
	live := f.liveInstallation(1)
	namespace := f.namespace(live, "active")
	op, _ := f.operation(live, "install", "complete", retNow-1*retDay, 1)
	var sets []string
	for i := 1; i <= 5; i++ {
		sets = append(sets, f.recoverySet(live, namespace, op, "verified", int64(i)))
	}
	other := f.liveInstallation(1)
	otherNamespace := f.namespace(other, "active")
	otherOp, _ := f.operation(other, "install", "complete", retNow-1*retDay, 1)
	f.recoverySet(other, otherNamespace, otherOp, "verified", 1)
	// The two oldest beyond the latest two are held: one by an import grant, one by a restore in progress.
	f.importGrant(sets[0], "issued")
	mustExec(t, f.st, `UPDATE app_platform_recovery_sets SET state='restoring' WHERE id=?`, sets[1])
	deleted := f.recoverySet(live, namespace, op, "deleted", 0)

	beyond, err := platformRecoverySetsBeyondRetention(f.st.poolExec(), 2)
	if err != nil {
		t.Fatal(err)
	}
	// Latest two are sets[4] and sets[3]; sets[2] is the only one beyond them that nothing holds.
	if len(beyond) != 1 || beyond[0] != sets[2] {
		t.Fatalf("beyond = %v, want only %s", beyond, sets[2])
	}
	beyond, err = platformRecoverySetsBeyondRetention(f.st.poolExec(), 1)
	if err != nil || len(beyond) != 2 {
		t.Fatalf("keeping one: %v, %v", beyond, err)
	}

	f.s.st.SetSetting(platformRetentionRecoverySetsKey, "1")
	result := f.sweep(retNow)
	if result.RecoverySetsBeyond != 2 {
		t.Errorf("the sweep reported %d sets beyond the retention, want 2", result.RecoverySetsBeyond)
	}
	for _, id := range append(sets, deleted) {
		want := "verified"
		if id == sets[1] {
			want = "restoring"
		}
		if id == deleted {
			want = "deleted"
		}
		if got := scalar[string](t, f.st, `SELECT state FROM app_platform_recovery_sets WHERE id=?`, id); got != want {
			t.Errorf("recovery set %s state = %s, want %s: the sweep must not mark a set deleted while no runner command removes its data", id, got, want)
		}
	}
}

func TestPlatformRetentionTickReachesTheSweep(t *testing.T) {
	f := newRetFixture(t, false)
	removed := f.removedInstallation()
	// The tick judges by the wall clock, so the fixture is stamped relative to it.
	long := time.Now().UnixMilli() - 120*retDay
	op, _ := f.operation(removed, "disable", "complete", long, 1)
	f.s.cleanupTick()
	if f.exists("operations", op) {
		t.Fatal("the cleanup tick did not reach the platform sweep")
	}
	if f.st.GetSetting(platformRetentionLastSweepKey, "") == "" {
		t.Fatal("the tick left no stamp of the sweep")
	}
}

func TestPlatformRetentionTickWaitsAnIntervalUnlessThereIsBacklog(t *testing.T) {
	f := newRetFixture(t, false)
	removed := f.removedInstallation()
	now := time.Now()
	long := now.UnixMilli() - 120*retDay
	first, _ := f.operation(removed, "disable", "complete", long, 1)
	f.s.platformRetentionTick(now)
	if f.exists("operations", first) {
		t.Fatal("the first tick did not sweep")
	}
	second, _ := f.operation(removed, "disable", "complete", long, 1)
	f.s.platformRetentionTick(now.Add(time.Minute))
	if !f.exists("operations", second) {
		t.Fatal("a second tick within the interval swept again")
	}
	f.s.platformRetentionTick(now.Add(platformRetentionInterval + time.Minute))
	if f.exists("operations", second) {
		t.Fatal("a tick after the interval did not sweep")
	}
}

// The validity of a review plan is one named value, written nowhere else as a literal.
func TestReviewPlanValidityIsOneNamedConstant(t *testing.T) {
	if platformReviewPlanValidityMS != 15*60*1000 {
		t.Fatalf("review plan validity = %d ms", platformReviewPlanValidityMS)
	}
	files, err := filepath.Glob("app_platform_*.go")
	if err != nil || len(files) == 0 {
		t.Fatal(err)
	}
	literal := regexp.MustCompile(`\b900000\b`)
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		raw, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		if literal.Match(raw) {
			t.Errorf("%s writes the review plan validity as a literal; use platformReviewPlanValidityMS", file)
		}
	}
}

// A closed image record is deleted whole or not at all: while a load of it is kept, its removal is kept too,
// or platformLoadedImages would report the image as loaded again and reclaim would remove it again, forever.
func TestPlatformRetentionKeepsTheRemovalOfAnImageWhoseLoadIsPinned(t *testing.T) {
	bothDrivers(t, func(t *testing.T, pg bool) {
		f := newRetFixture(t, pg)
		live := f.liveInstallation(2)
		config := platformDigest([]byte("pinned-load"))
		install, installPlan := f.operation(live, "install", "complete", retNow-200*retDay, 1)
		f.effect(install, "load_image", "applied", f.loadReceipt(install, live, config))
		removal, removalPlan := f.operation(live, "update", "complete", retNow-190*retDay, 1)
		f.effect(removal, "remove_image", "applied", f.removeReceipt(config))
		f.operation(live, "update", "complete", retNow-150*retDay, 2)

		f.sweep(retNow)

		if !f.exists("operations", install) || !f.exists("review_plans", installPlan) {
			t.Fatal("the install operation, pinned by the live chain, was removed")
		}
		if !f.exists("operations", removal) || !f.exists("review_plans", removalPlan) {
			t.Fatal("the removal of an image whose load is kept was deleted: the image would read as loaded again")
		}
		loaded, err := f.s.platformLoadedImages(context.Background())
		if err != nil || len(loaded) != 0 {
			t.Fatalf("after the sweep the image reads as loaded: %v, %v", loaded, err)
		}
	})
}

// A runtime that holds an effect with no verdict is left out of reclaim as a whole, so every image record on it is kept.
func TestPlatformRetentionKeepsEveryImageRecordOfABusyRuntime(t *testing.T) {
	bothDrivers(t, func(t *testing.T, pg bool) {
		f := newRetFixture(t, pg)
		removed := f.removedInstallation()
		f.namespace(removed, "deleted")
		runtime := f.runtimeBinding()
		config := platformDigest([]byte("busy"))
		old := retNow - 100*retDay
		load, _ := f.operation(removed, "update", "complete", old, 2)
		f.effectOn(load, runtime, "load_image", "applied", f.loadReceipt(load, removed, config))
		remove, _ := f.operation(removed, "update", "complete", old, 3)
		f.effectOn(remove, runtime, "remove_image", "applied", f.removeReceipt(config))
		pending, _ := f.operation(removed, "update", "complete", old, 4)
		f.effectOn(pending, runtime, "load_image", "unknown", "{}")
		other := f.runtimeBinding()
		free, _ := f.operation(removed, "update", "complete", old, 5)
		f.effectOn(free, other, "load_image", "applied", f.loadReceipt(free, removed, platformDigest([]byte("free"))))
		freeRemove, _ := f.operation(removed, "update", "complete", old, 6)
		f.effectOn(freeRemove, other, "remove_image", "applied", f.removeReceipt(platformDigest([]byte("free"))))

		f.sweep(retNow)

		for _, op := range []string{load, remove, pending} {
			if !f.exists("operations", op) {
				t.Errorf("operation %s of a busy runtime was deleted", op)
			}
		}
		for _, op := range []string{free, freeRemove} {
			if f.exists("operations", op) {
				t.Errorf("operation %s of a runtime with no open effect was kept", op)
			}
		}
	})
}

// Mutation guards: each of these rules once survived its removal.
func TestPlatformRetentionPlanReferencedBySecretBindingIsKept(t *testing.T) {
	bothDrivers(t, func(t *testing.T, pg bool) {
		f := newRetFixture(t, pg)
		removed := f.removedInstallation()
		f.namespace(removed, "deleted")
		// An operation whose plan only a secret binding names.
		op, plan := f.operation(removed, "install", "complete", retNow-100*retDay, 1)
		f.secretBinding(removed, plan)
		// An unconsumed expired plan only a secret binding names.
		unconsumed := f.plan(removed, "install", retNow-30*retDay, 0)
		f.secretBinding(removed, unconsumed)
		free := f.plan(removed, "install", retNow-30*retDay, 0)

		f.sweep(retNow)

		if !f.exists("operations", op) || !f.exists("review_plans", plan) {
			t.Error("an operation whose plan a secret binding names was removed")
		}
		if !f.exists("review_plans", unconsumed) {
			t.Error("an unconsumed plan a secret binding names was removed")
		}
		if f.exists("review_plans", free) {
			t.Error("an unreferenced expired plan was kept")
		}
	})
}

func TestPlatformRetentionKeepsOperationsOfALaterGenerationOnALiveInstallation(t *testing.T) {
	bothDrivers(t, func(t *testing.T, pg bool) {
		f := newRetFixture(t, pg)
		live := f.liveInstallation(3)
		ahead, _ := f.operation(live, "enable", "complete", retNow-141*retDay, 4)
		f.operation(live, "enable", "complete", retNow-130*retDay, 3)
		behind, _ := f.operation(live, "update", "complete", retNow-142*retDay, 2)
		f.operation(live, "update", "complete", retNow-120*retDay, 3)
		f.sweep(retNow)
		if !f.exists("operations", ahead) {
			t.Error("an operation of a generation past the installation's was removed")
		}
		if f.exists("operations", behind) {
			t.Error("a superseded operation of an earlier generation was kept")
		}
	})
}

// An applied set-data-quota effect is a link of the namespace's quota chain, which every loader derives from the
// effects' receipts. Retention must never delete the operation that holds one, even when a later operation of its
// action has completed: the chain would break and the installation would stop working.
func TestPlatformRetentionKeepsOperationsHoldingAnAppliedQuotaRaise(t *testing.T) {
	bothDrivers(t, func(t *testing.T, pg bool) {
		f := newRetFixture(t, pg)
		live := f.liveInstallation(3)
		firstRaise, _ := f.operation(live, "set_data_quota", "complete", retNow-150*retDay, 2)
		f.effectAt(firstRaise, "prepare_data", 1, "set-data-quota", "applied", "{}")
		secondRaise, _ := f.operation(live, "set_data_quota", "complete", retNow-140*retDay, 2)
		f.effectAt(secondRaise, "prepare_data", 1, "set-data-quota", "applied", "{}")
		raisedUpdate, _ := f.operation(live, "update", "complete", retNow-160*retDay, 2)
		f.effectAt(raisedUpdate, "migrate", 4, "set-data-quota", "applied", "{}")
		f.operation(live, "update", "complete", retNow-120*retDay, 3)
		revertedRaise, _ := f.operation(live, "set_data_quota", "complete", retNow-145*retDay, 2)
		f.effectAt(revertedRaise, "prepare_data", 1, "set-data-quota", "reverted", "{}")
		f.operation(live, "set_data_quota", "complete", retNow-130*retDay, 3)
		f.sweep(retNow)
		for name, op := range map[string]string{"first raise": firstRaise, "second raise": secondRaise, "update raise": raisedUpdate} {
			if !f.exists("operations", op) {
				t.Errorf("the %s holds an applied quota raise and was removed", name)
			}
		}
		if f.exists("operations", revertedRaise) {
			t.Error("an operation whose raise was reverted holds no link and should be swept")
		}
	})
}

// The period counts from the moment the operation was resolved: a later recovery reconcile updates updated_ms and
// leaves completed_ms where it was.
func TestPlatformRetentionCountsFromTheLatestOfCompletedAndUpdated(t *testing.T) {
	f := newRetFixture(t, false)
	removed := f.removedInstallation()
	op, _ := f.operation(removed, "disable", "complete", retNow-100*retDay, 1)
	mustExec(t, f.st, `UPDATE app_platform_operations SET updated_ms=? WHERE id=?`, retNow-10*retDay, op)
	f.sweep(retNow)
	if !f.exists("operations", op) {
		t.Fatal("an operation resolved 10 days ago was removed because it completed 100 days ago")
	}
	f.sweep(retNow + 81*retDay)
	if f.exists("operations", op) {
		t.Fatal("the operation outlived its period counted from its resolution")
	}
}

// One class failing does not stop the others, and the audit row names the failure.
func TestPlatformRetentionClassesRunIndependently(t *testing.T) {
	f := newRetFixture(t, false)
	removed := f.removedInstallation()
	op, _ := f.operation(removed, "disable", "complete", retNow-100*retDay, 1)
	task := f.task(removed, "t", "succeeded", retNow-40*retDay, true)
	mustExec(t, f.st, `ALTER TABLE app_platform_data_namespaces RENAME TO app_platform_data_namespaces_hidden`)
	result, err := f.s.sweepPlatformRetention(context.Background(), retNow, defaultPlatformRetentionLimits())
	mustExec(t, f.st, `ALTER TABLE app_platform_data_namespaces_hidden RENAME TO app_platform_data_namespaces`)
	if err == nil || result.Error == "" {
		t.Fatalf("the failing class was not reported: %v %q", err, result.Error)
	}
	if !f.exists("operations", op) {
		t.Fatal("the failed class removed rows")
	}
	if f.taskRow(task).payloadLen != 0 || result.TasksReduced != 1 {
		t.Fatalf("the task class did not run after the operations class failed: %+v", result)
	}
	var detail string
	if err := f.st.queryRow(`SELECT detail FROM audit_log WHERE action='app_platform.retention_sweep'`).Scan(&detail); err != nil || !strings.Contains(detail, `"error"`) {
		t.Fatalf("the audit detail does not carry the error: %q %v", detail, err)
	}
}

// Each class re-checks the pause inside its own transaction.
func TestPlatformRetentionClassesRecheckThePauseInsideTheirTransaction(t *testing.T) {
	f := newRetFixture(t, false)
	removed := f.removedInstallation()
	plan := f.plan(removed, "install", retNow-30*retDay, 0)
	task := f.task(removed, "t", "succeeded", retNow-40*retDay, false)
	f.st.SetSetting("app_platform_restore_paused", "1")
	limits := defaultPlatformRetentionLimits()
	if _, err := f.s.sweepRetentionPlans(context.Background(), retNow, limits); !errors.Is(err, errPlatformRetentionPaused) {
		t.Fatalf("plans class = %v, want the pause", err)
	}
	var result platformRetentionResult
	cfg := f.s.platformRetentionConfigLoad()
	if err := f.s.sweepRetentionOperations(context.Background(), retNow, cfg, limits, &result); !errors.Is(err, errPlatformRetentionPaused) {
		t.Fatalf("operations class = %v, want the pause", err)
	}
	if err := f.s.sweepRetentionTasks(context.Background(), retNow, cfg, limits, &result); !errors.Is(err, errPlatformRetentionPaused) {
		t.Fatalf("tasks class = %v, want the pause", err)
	}
	if !f.exists("review_plans", plan) || f.taskRow(task).payloadLen == 0 {
		t.Fatal("a class changed rows under the pause")
	}
}

// retWaiters counts backends waiting for a lock the test did not grant.
func retWaiters(t *testing.T, st *Store) int {
	t.Helper()
	return scalar[int](t, st, `SELECT COUNT(*) FROM pg_locks WHERE NOT granted AND locktype IN ('advisory','relation')`)
}

// retBlocked starts fn, waits until a backend is waiting for a lock, runs release, and expects fn to finish. It
// polls the lock table rather than sleeping.
func retBlocked(t *testing.T, st *Store, fn func(), release func()) {
	t.Helper()
	done := make(chan struct{})
	go func() { defer close(done); fn() }()
	deadline := time.Now().Add(20 * time.Second)
	for retWaiters(t, st) == 0 {
		select {
		case <-done:
			t.Fatal("the sweep finished without waiting for the admission lock")
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("the sweep never waited for the lock")
		}
		runtime.Gosched()
	}
	release()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("the sweep did not resume once the lock was released")
	}
}

// The sweep takes the locks the admission paths take, so a decision to delete cannot be overtaken by an admission.
func TestPlatformRetentionSerializesWithAdmissionOnPostgres(t *testing.T) {
	f := newRetFixture(t, true)
	removed := f.removedInstallation()
	f.namespace(removed, "deleted")
	op, _ := f.operation(removed, "disable", "complete", retNow-100*retDay, 1)
	hold := func(statement string) (*sql.Tx, func()) {
		tx, err := f.st.db.Begin()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(statement); err != nil {
			t.Fatal(err)
		}
		return tx, func() {
			if err := tx.Commit(); err != nil {
				t.Error(err)
			}
		}
	}
	sweep := func() {
		if _, err := f.s.sweepPlatformRetention(context.Background(), retNow, defaultPlatformRetentionLimits()); err != nil {
			t.Error(err)
		}
	}

	t.Run("the trust lock operation admission takes", func(t *testing.T) {
		_, release := hold(`LOCK TABLE app_platform_publishers,app_platform_publisher_keys IN SHARE ROW EXCLUSIVE MODE`)
		retBlocked(t, f.st, sweep, release)
		if f.exists("operations", op) {
			t.Fatal("the sweep did not delete once the lock was released")
		}
	})
	t.Run("the task admission lock", func(t *testing.T) {
		task := f.task(removed, "serialized", "succeeded", retNow-40*retDay, false)
		_, release := hold(`SELECT pg_advisory_xact_lock(780719)`)
		retBlocked(t, f.st, sweep, release)
		if f.taskRow(task).payloadLen != 0 {
			t.Fatal("the sweep did not reduce the task once the lock was released")
		}
	})
}
