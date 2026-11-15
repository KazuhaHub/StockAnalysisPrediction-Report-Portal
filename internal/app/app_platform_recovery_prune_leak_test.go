package app

import (
	"context"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

// platformDeleteDataThenPrune drives a namespace through the real delete-data stage, with the recovery sets given
// retained on it first, and returns what the pruning step needs. The stage marks every set of the namespace deleted
// on the Portal and never touches an archive on the runner: that is the leak the pruning step closes.
type deleteDataLeak struct {
	s            *Server
	runner       *platformFakePruneRunner
	endpoint     platformRuntimeEndpoint
	installation string
	namespace    string
	sets         []string
	preparing    string
	hostOperaton string
}

func newDeleteDataLeak(t *testing.T, pg bool) *deleteDataLeak {
	t.Helper()
	s, actor, installation, namespace := platformDeleteDataFixture(t, pg)
	l := &deleteDataLeak{s: s, installation: installation, namespace: namespace, endpoint: platformManagedRemovalFixtureEndpoint()}
	installOperation := scalar[string](t, s.st, `SELECT id FROM app_platform_operations WHERE installation_id=? AND action='install' ORDER BY completed_ms DESC LIMIT 1`, installation)
	release := scalar[string](t, s.st, `SELECT release_sha256 FROM app_platform_activations WHERE installation_id=? AND data_namespace_id=? ORDER BY generation DESC LIMIT 1`, installation, namespace)
	epoch := scalar[string](t, s.st, `SELECT controller_epoch FROM app_platform_installations WHERE id=?`, installation)
	add := func(n int, state string) string {
		id := platformFixtureID(1900 + n)
		var manifest, verified any
		if state != "preparing" {
			manifest, verified = platformDigest([]byte("leak archive "+id)), int64(1)
		}
		platformTestInsert(t, s.st.poolExec(), "recovery_sets", map[string]any{
			"id": id, "installation_id": installation, "data_namespace_id": namespace, "operation_id": installOperation,
			"release_sha256": release, "data_format": 1, "checkpoint_id": platformFixtureID(1950 + n), "manifest_sha256": manifest,
			"bytes": 8192, "state": state, "controller_epoch": epoch, "writer_fence": 1, "verified_ms": verified, "deleted_ms": nil,
			"created_ms": int64(100 * n), "updated_ms": int64(100 * n),
		})
		return id
	}
	l.sets = []string{add(1, "verified"), add(2, "verified")}
	// A set that was never cut has no archive and no manifest: it cannot be named to the runner and is left alone.
	l.preparing = add(3, "preparing")

	review, err := s.createPlatformDeleteDataReview(context.Background(), actor, installation, namespace)
	if err != nil {
		t.Fatal(err)
	}
	commit, err := s.consumePlatformDeleteDataReview(actor, review.ID, review.SHA256, platformFixtureID(1932))
	if err != nil {
		t.Fatal(err)
	}
	selected, err := s.claimNextPlatformDeleteData(context.Background(), platformFixtureID(1933))
	if err != nil || selected.lease.id != commit.OperationID {
		t.Fatal("delete-data claim", selected, err)
	}
	current := selected.lease
	for range 3 {
		if current, err = s.st.advancePlatformDeleteDataControl(current); err != nil {
			t.Fatal("delete-data control", current.snapshot.Stage, err)
		}
	}
	if current.snapshot.Stage != appplatform.StageRemoveData {
		t.Fatal("delete-data did not reach remove_data", current.snapshot)
	}
	intent, err := s.preparePlatformStandaloneDeleteDataIntent(context.Background(), current)
	if err != nil {
		t.Fatal("delete-data intent", err)
	}
	receipt, ok := validPlatformDeleteDataObservation(platformDeleteDataObservationFixture(intent, l.endpoint), intent, l.endpoint)
	if !ok {
		t.Fatal("delete-data receipt")
	}
	completed, err := s.settlePlatformStandaloneDeleteData(context.Background(), current, intent, receipt)
	if err != nil || completed.snapshot.Stage != appplatform.StageComplete {
		t.Fatal("delete-data completion", completed.snapshot, err)
	}
	l.hostOperaton = commit.OperationID
	l.runner = newPlatformFakePruneRunner(t, l.endpoint)
	s.platformPruneRunner = l.runner
	noPruneBackoff(t)
	return l
}

func (l *deleteDataLeak) prune(t *testing.T) platformPruneResult {
	t.Helper()
	result, err := l.s.prunePlatformRecoverySets(context.Background(), l.endpoint)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

// Test 19 and test 22 together. A namespace deleted through the real delete-data stage leaves its sets deleted on the
// Portal and its archives on the runner; the pruning step sends one deletion per set, and after the applied
// receipts a second pass sends nothing, also after a retention sweep that is more than 400 days late. Fails if the
// sets are not selected, if one is sent twice, if the set that was never cut is sent, or if the retention sweep
// removes the operation holding the applied deletions (the sets would then be selected again, and the runner could
// only answer "absent" to each of them for ever).
func TestPruneClearsTheArchivesOfANamespaceDeletedByTheDeleteDataStage(t *testing.T) {
	eachPlatformDriver(t, func(t *testing.T, pg bool) {
		l := newDeleteDataLeak(t, pg)
		s := l.s
		for _, id := range append(append([]string{}, l.sets...), l.preparing) {
			if scalar[string](t, s.st, `SELECT state FROM app_platform_recovery_sets WHERE id=?`, id) != "deleted" {
				t.Fatalf("the delete-data stage left set %s retained", id)
			}
		}
		if len(l.runner.sent) != 0 {
			t.Fatal("the delete-data stage reached a runner command it does not own")
		}
		if result := l.prune(t); result.Settled != 2 {
			t.Fatalf("the leak backlog settled %d sets, want one per cut set: %+v", result.Settled, result)
		}
		if len(l.runner.sent) != 2 || len(l.runner.gone) != 2 || len(l.runner.effectIDs()) != 2 {
			t.Fatalf("sent %d commands (%d distinct), removed %v", len(l.runner.sent), len(l.runner.effectIDs()), l.runner.gone)
		}
		for _, c := range l.runner.sent {
			if c.Payload.RecoverySetID == l.preparing {
				t.Fatal("a set that was never cut was sent for deletion")
			}
			if c.OperationID != l.hostOperaton {
				t.Fatalf("the deletion is hosted by %s, not by the delete-data operation %s", c.OperationID, l.hostOperaton)
			}
			if c.Stage != "remove_data" || mustInt64(c.EffectNumber) < 2 {
				t.Fatalf("the deletion took the delete-data effect's identity: %+v", c.CommandHeader)
			}
		}
		if scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_operation_effects WHERE kind='delete_checkpoint' AND state='applied'`) != 2 ||
			scalar[int](t, s.st, `SELECT COUNT(*) FROM audit_log WHERE action='app_platform.recovery_set_pruned'`) != 2 {
			t.Fatal("the deletions are not settled and audited")
		}
		if again := l.prune(t); again.Settled != 0 || len(l.runner.sent) != 2 {
			t.Fatalf("a second pass sent more: %d commands", len(l.runner.sent))
		}

		// 400 days later the retention sweep runs. The installation is retired first, so that the sweep may take the
		// operations of a removed installation; the operation holding the deletions must stay.
		mustExec(t, s.st, `UPDATE app_platform_installations SET desired_state='removed',phase='removed' WHERE id=?`, l.installation)
		late := time.Now().UnixMilli() + 400*platformRetentionDayMS
		if _, err := s.sweepPlatformRetention(context.Background(), late, defaultPlatformRetentionLimits()); err != nil {
			t.Fatal(err)
		}
		if scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_operations WHERE id=?`, l.hostOperaton) != 1 ||
			scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_operation_effects WHERE kind='delete_checkpoint' AND state='applied'`) != 2 {
			t.Fatal("the retention sweep removed the operation holding the applied deletions")
		}
		if again := l.prune(t); again.Settled != 0 || len(l.runner.sent) != 2 {
			t.Fatalf("after the retention sweep a pass sent more: %d commands", len(l.runner.sent))
		}

		// What the pin prevents: with the applied rows gone, every deleted set is selected again.
		mustExec(t, s.st, `DELETE FROM app_platform_operation_effects WHERE kind='delete_checkpoint'`)
		if again := l.prune(t); again.Settled != 2 || len(l.runner.sent) != 4 {
			t.Fatalf("without the applied rows the sets were not selected again (%+v, %d sent)", again, len(l.runner.sent))
		}
		for _, c := range l.runner.sent[2:] {
			if c.Kind != "delete-checkpoint" {
				t.Fatal("not a deletion")
			}
		}
		// The runner answers absent: the second batch is applied with the outcome that says nothing was left to remove.
		if len(l.runner.gone) != 2 {
			t.Fatalf("the runner removed a checkpoint twice: %v", l.runner.gone)
		}
	})
}
