package app

import (
	"context"
	"slices"
	"testing"
)

// The settlement drivers read their candidates in a window of eight, starting after the key the previous window ended
// at and wrapping to the start of the key space. Every candidate is reached within a bounded number of ticks however
// the earlier ones behave, and a row that leaves the set (the cursor's own row included) cannot make the cursor skip
// or repeat the others.
func TestKeysetWindowReachesEveryCandidateWrapsAndSurvivesRemovedRows(t *testing.T) {
	eachPlatformDriver(t, func(t *testing.T, pg bool) {
		s, operation, _, _ := orphanedEnableFixture(t, pg)
		all := cloneHeldStarts(t, s, operation, 19)
		slices.Sort(all)
		window := func() []string {
			t.Helper()
			ids, err := s.platformHeldStartsToSettle(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if len(ids) > platformHeldStartWindow {
				t.Fatalf("a window of %d: %v", len(ids), ids)
			}
			return ids
		}
		// 20 candidates: two windows walk the key space in order, and the third takes the last four and wraps to the
		// first four, so no tick is wasted and none repeats a key inside a window.
		w1, w2, w3 := window(), window(), window()
		if !slices.Equal(w1, all[:8]) || !slices.Equal(w2, all[8:16]) || !slices.Equal(w3, append(slices.Clone(all[16:]), all[:4]...)) {
			t.Fatalf("the windows did not walk the key space in order and wrap: %v %v %v", w1, w2, w3)
		}
		if w4 := window(); !slices.Equal(w4, all[4:12]) {
			t.Fatalf("the window after a wrap did not continue after the last key: %v", w4)
		}
		// The row the cursor stands on leaves the set between two ticks: the walk goes on past it.
		next := window()
		last := next[len(next)-1]
		mustExec(t, s.st, `UPDATE app_platform_operation_effects SET state='reverted' WHERE operation_id=? AND stage='start'`, last)
		after := window()
		if slices.Contains(after, last) {
			t.Fatal("a closed start was selected")
		}
		remaining := slices.DeleteFunc(slices.Clone(all), func(id string) bool { return id == last })
		seen := map[string]bool{}
		for range 4 {
			for _, id := range window() {
				seen[id] = true
			}
		}
		for _, id := range remaining {
			if !seen[id] {
				t.Fatalf("%s was never reached after a row left the set", id)
			}
		}
	})
}

// A failing start in every position still lets the others be reached: the failures do not hold the window.
func TestMigrationSettlementReachesEveryFailedUpdate(t *testing.T) {
	eachPlatformDriver(t, func(t *testing.T, pg bool) {
		f := settleAfterEnd(t, pg, orphanedScript)
		all := cloneOperations(t, f.s, f.op, 19, `stage='migrate' AND effect_number=2`)
		slices.Sort(all)
		candidates, err := f.s.platformMigrationSettlementCandidatesAll(context.Background())
		if err != nil || len(candidates) != 20 {
			t.Fatalf("the failed updates are not all candidates: %d %v", len(candidates), err)
		}
		seen := map[string]bool{}
		for range 3 {
			ids, err := f.s.platformMigrationsToSettle(context.Background())
			if err != nil || len(ids) > platformMigrationSettlementWindow {
				t.Fatalf("window %v %v", ids, err)
			}
			for _, id := range ids {
				seen[id] = true
			}
		}
		for _, id := range all {
			if !seen[id] {
				t.Fatalf("the failed update %s was not reached in three ticks", id)
			}
		}
	})
}

func TestRecoveryResumeCandidatesReachEveryResume(t *testing.T) {
	eachPlatformDriver(t, func(t *testing.T, pg bool) {
		f, operation, _ := unknownResumeStartFixtureFor(t, pg)
		all := cloneOperations(t, f.s, operation, 19, `stage='start' AND effect_number=1`)
		slices.Sort(all)
		seen := map[string]bool{}
		for range 3 {
			window, err := f.s.platformRecoveryStartsToReconcile(context.Background())
			if err != nil || len(window) > platformRecoveryStartWindow {
				t.Fatalf("window %v %v", window, err)
			}
			for _, c := range window {
				seen[c.lease.id] = true
			}
		}
		for _, id := range all {
			if !seen[id] {
				t.Fatalf("the resume %s was not reached in three ticks", id)
			}
		}
	})
}
