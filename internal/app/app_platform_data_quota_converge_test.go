package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

// platformSetDataQuotaAtPrepare admits a reviewed raise and brings it to the prepare_data stage.
func platformSetDataQuotaAtPrepare(t *testing.T, s *Server, actor platformActorSession, installation string, target *platformManagedInstallTarget, runtime platformRuntimeRegistration, newBytes, newInodes int64, key int) platformOperationLease {
	t.Helper()
	endpoint := platformCheckpointEndpointFixture()
	review, err := s.createPlatformSetDataQuotaReviewWithRuntimeContext(context.Background(), actor, installation, newBytes, newInodes, target, runtime, endpoint)
	if err != nil {
		t.Fatal("raise review", err)
	}
	commit, err := s.consumePlatformSetDataQuotaReviewWithRuntimeContext(context.Background(), actor, review.ID, review.SHA256, platformFixtureID(key), target, runtime)
	if err != nil {
		t.Fatal("raise admission", err)
	}
	selected, err := s.claimNextPlatformSetDataQuota(context.Background(), platformFixtureID(key+1))
	if err != nil || selected.lease.id != commit.OperationID {
		t.Fatal("raise claim", selected, err)
	}
	current := selected.lease
	for current.snapshot.Stage != appplatform.StagePrepareData {
		if current, err = s.advancePlatformSetDataQuotaStage(context.Background(), current); err != nil {
			t.Fatal("raise stage", err)
		}
	}
	return current
}

// Decision 3: the runner's definite failure ends a reviewed raise at once, with its row reverted, nothing applied and
// the operation failed.
func testSetDataQuotaDefiniteFailureRevertsTheRow(t *testing.T, pg bool) {
	s, actor, enabled, target, runtime := platformDataActionFixture(t, pg)
	(&platformFakeDataRunner{fits: true}).install(t)
	raise := &platformFakeRaiseRunner{t: t, endpoint: platformCheckpointEndpointFixture()}
	raise.install(t)
	installation := enabled.installationID
	namespace := platformNamespaceOf(t, s, installation)
	bytes0 := scalar[int64](t, s.st, `SELECT quota_bytes FROM app_platform_data_namespaces WHERE id=?`, namespace)
	inodes0 := scalar[int64](t, s.st, `SELECT quota_inodes FROM app_platform_data_namespaces WHERE id=?`, namespace)
	current := platformSetDataQuotaAtPrepare(t, s, actor, installation, target, runtime, bytes0*2, inodes0, 1600)
	raise.execute = func(i platformSetDataQuotaIntent) (platformSetDataQuotaObservation, error) {
		return raise.status(i, "failed", "quota_raise_unsafe"), nil
	}
	if _, err := s.dispatchPlatformSetDataQuota(context.Background(), current, platformCheckpointEndpointFixture()); !errors.Is(err, errPlatformMigrationEnded) {
		t.Fatalf("a definite failure did not end the raise: %v", err)
	}
	if raise.count("execute") != 1 || raise.count("read") != 0 {
		t.Fatalf("calls: %v", raise.calls)
	}
	if operationStage(t, s, current.id) != "failed" || effectState(t, s, current.id, "prepare_data", 1) != "reverted" ||
		scalar[string](t, s.st, `SELECT error_code FROM app_platform_operations WHERE id=?`, current.id) != "data_quota_unavailable" ||
		scalar[int64](t, s.st, `SELECT quota_bytes FROM app_platform_data_namespaces WHERE id=?`, namespace) != bytes0 ||
		scalar[string](t, s.st, `SELECT desired_state||':'||phase FROM app_platform_installations WHERE id=?`, installation) != "enabled:ready" {
		t.Fatal("a definite failure did not leave a reverted row, a failed operation and an untouched installation")
	}
	if _, _, chain, err := platformCurrentNamespaceQuota(s.st.poolExec(), installation, namespace); err != nil || len(chain) != 1 {
		t.Fatal("a reverted raise entered the chain", chain, err)
	}
}

func TestSetDataQuotaDefiniteFailureRevertsTheRowSQLite(t *testing.T) {
	testSetDataQuotaDefiniteFailureRevertsTheRow(t, false)
}
func TestSetDataQuotaDefiniteFailureRevertsTheRowPostgres(t *testing.T) {
	testSetDataQuotaDefiniteFailureRevertsTheRow(t, true)
}

// Decision 2: a reviewed raise whose effect has no verdict ends recovery_required with the row unknown, and the
// convergence step settles it from the runner's answer, with the failed and the unknown kept exactly apart.
func testSetDataQuotaUnknownConverges(t *testing.T, pg bool) {
	for name, c := range map[string]struct {
		answer func(*platformFakeRaiseRunner, platformSetDataQuotaIntent) (platformSetDataQuotaObservation, error)
		window time.Duration
		want   string
	}{
		"receipt completes it": {func(r *platformFakeRaiseRunner, i platformSetDataQuotaIntent) (platformSetDataQuotaObservation, error) {
			return r.receipt(i), nil
		}, time.Hour, "complete"},
		"definite failure fails it": {func(r *platformFakeRaiseRunner, i platformSetDataQuotaIntent) (platformSetDataQuotaObservation, error) {
			return r.status(i, "failed", "quota_raise_lower"), nil
		}, time.Hour, "failed"},
		"absence after the window fails it": {func(r *platformFakeRaiseRunner, i platformSetDataQuotaIntent) (platformSetDataQuotaObservation, error) {
			return r.status(i, "absent", ""), nil
		}, -time.Minute, "failed"},
		"absence inside the window waits": {func(r *platformFakeRaiseRunner, i platformSetDataQuotaIntent) (platformSetDataQuotaObservation, error) {
			return r.status(i, "absent", ""), nil
		}, time.Hour, "recovery_required"},
		"a runner still working waits": {func(r *platformFakeRaiseRunner, i platformSetDataQuotaIntent) (platformSetDataQuotaObservation, error) {
			return r.status(i, "pending", ""), nil
		}, time.Hour, "recovery_required"},
	} {
		t.Run(name, func(t *testing.T) {
			s, actor, enabled, target, runtime := platformDataActionFixture(t, pg)
			(&platformFakeDataRunner{fits: true}).install(t)
			endpoint := platformCheckpointEndpointFixture()
			raise := &platformFakeRaiseRunner{t: t, endpoint: endpoint}
			raise.install(t)
			installation := enabled.installationID
			namespace := platformNamespaceOf(t, s, installation)
			bytes0 := scalar[int64](t, s.st, `SELECT quota_bytes FROM app_platform_data_namespaces WHERE id=?`, namespace)
			inodes0 := scalar[int64](t, s.st, `SELECT quota_inodes FROM app_platform_data_namespaces WHERE id=?`, namespace)
			current := platformSetDataQuotaAtPrepare(t, s, actor, installation, target, runtime, bytes0*2, inodes0, 1610)
			raise.execute = func(platformSetDataQuotaIntent) (platformSetDataQuotaObservation, error) {
				return nil, errors.New("connection reset")
			}
			raise.read = func(platformSetDataQuotaIntent) (platformSetDataQuotaObservation, error) {
				return nil, errors.New("read failed too")
			}
			if _, err := s.dispatchPlatformSetDataQuota(context.Background(), current, endpoint); err == nil || errors.Is(err, errPlatformMigrationEnded) {
				t.Fatalf("an unanswered raise is neither a verdict nor an end: %v", err)
			}
			if err := s.finishPlatformSetDataQuotaForReview(context.Background(), current, ""); err != nil {
				t.Fatal(err)
			}
			if operationStage(t, s, current.id) != "recovery_required" || effectState(t, s, current.id, "prepare_data", 1) != "unknown" ||
				scalar[string](t, s.st, `SELECT error_code FROM app_platform_operations WHERE id=?`, current.id) != "data_quota_recovery_required" {
				t.Fatal("an unknown raise did not end recovery_required with the row unknown")
			}
			if _, _, chain, err := platformCurrentNamespaceQuota(s.st.poolExec(), installation, namespace); err != nil || len(chain) != 1 {
				t.Fatal("an unknown raise entered the chain", chain, err)
			}
			mustExec(t, s.st, `UPDATE app_platform_operation_effects SET receipt_json=? WHERE operation_id=? AND stage='prepare_data' AND effect_number=1`,
				platformImportPendingReceipt(time.Now().Add(c.window).UnixMilli()), current.id)
			raise.read = func(i platformSetDataQuotaIntent) (platformSetDataQuotaObservation, error) { return c.answer(raise, i) }
			if _, err := s.settlePlatformQuotaRaises(context.Background(), endpoint); err != nil {
				t.Fatal(err)
			}
			if got := operationStage(t, s, current.id); got != c.want {
				t.Fatalf("the operation is %s, want %s", got, c.want)
			}
			switch c.want {
			case "complete":
				if scalar[int64](t, s.st, `SELECT quota_bytes FROM app_platform_data_namespaces WHERE id=?`, namespace) != bytes0*2 ||
					effectState(t, s, current.id, "prepare_data", 1) != "applied" ||
					scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_operations WHERE id=? AND error_code IS NULL AND completed_ms IS NOT NULL`, current.id) != 1 {
					t.Fatal("a receipt did not complete the raise")
				}
				if _, _, chain, err := platformCurrentNamespaceQuota(s.st.poolExec(), installation, namespace); err != nil || len(chain) != 2 {
					t.Fatal("the converged raise is not a link", chain, err)
				}
				access, err := s.preparePlatformWritableBackendAccess(context.Background(), actor, installation)
				if err != nil {
					t.Fatal("history after a converged raise", err)
				}
				access.Close()
			case "failed":
				if scalar[int64](t, s.st, `SELECT quota_bytes FROM app_platform_data_namespaces WHERE id=?`, namespace) != bytes0 ||
					effectState(t, s, current.id, "prepare_data", 1) != "reverted" ||
					scalar[string](t, s.st, `SELECT error_code FROM app_platform_operations WHERE id=?`, current.id) != "data_quota_unavailable" {
					t.Fatal("a definite answer did not fail the raise with its row reverted")
				}
			default:
				if effectState(t, s, current.id, "prepare_data", 1) != "unknown" || scalar[int64](t, s.st, `SELECT quota_bytes FROM app_platform_data_namespaces WHERE id=?`, namespace) != bytes0 {
					t.Fatal("an answer that settles nothing changed the row or the namespace")
				}
			}
			if raise.count("execute") != 1 {
				t.Fatalf("convergence sent the command: %v", raise.calls)
			}
		})
	}
}

func TestSetDataQuotaUnknownConvergesSQLite(t *testing.T) { testSetDataQuotaUnknownConverges(t, false) }
func TestSetDataQuotaUnknownConvergesPostgres(t *testing.T) {
	testSetDataQuotaUnknownConverges(t, true)
}

// An orphaned reviewed raise is finished by sending the same command again, under the same effect.
func testSetDataQuotaOrphanedIsFinishedBySending(t *testing.T, pg bool) {
	s, actor, enabled, target, runtime := platformDataActionFixture(t, pg)
	(&platformFakeDataRunner{fits: true}).install(t)
	endpoint := platformCheckpointEndpointFixture()
	raise := &platformFakeRaiseRunner{t: t, endpoint: endpoint}
	raise.install(t)
	installation := enabled.installationID
	namespace := platformNamespaceOf(t, s, installation)
	bytes0 := scalar[int64](t, s.st, `SELECT quota_bytes FROM app_platform_data_namespaces WHERE id=?`, namespace)
	inodes0 := scalar[int64](t, s.st, `SELECT quota_inodes FROM app_platform_data_namespaces WHERE id=?`, namespace)
	current := platformSetDataQuotaAtPrepare(t, s, actor, installation, target, runtime, bytes0, inodes0*2, 1620)
	raise.execute = func(platformSetDataQuotaIntent) (platformSetDataQuotaObservation, error) {
		return nil, errors.New("connection reset")
	}
	raise.read = func(platformSetDataQuotaIntent) (platformSetDataQuotaObservation, error) {
		return nil, errors.New("read failed")
	}
	_, _ = s.dispatchPlatformSetDataQuota(context.Background(), current, endpoint)
	if err := s.finishPlatformSetDataQuotaForReview(context.Background(), current, ""); err != nil {
		t.Fatal(err)
	}
	first := raise.sent[0]
	raise.read = func(i platformSetDataQuotaIntent) (platformSetDataQuotaObservation, error) {
		return raise.status(i, "orphaned", ""), nil
	}
	raise.execute = func(i platformSetDataQuotaIntent) (platformSetDataQuotaObservation, error) {
		if i.effectID != first.effectID || i.command.PayloadSHA256 != first.command.PayloadSHA256 {
			t.Error("the resend is not the same effect")
		}
		return raise.receipt(i), nil
	}
	if progressed, err := s.settlePlatformQuotaRaises(context.Background(), endpoint); err != nil || progressed != 1 || operationStage(t, s, current.id) != "complete" {
		t.Fatal("an orphaned raise was not finished", progressed, err)
	}
	if scalar[int64](t, s.st, `SELECT quota_inodes FROM app_platform_data_namespaces WHERE id=?`, namespace) != inodes0*2 {
		t.Fatal("the finished raise did not reach the namespace")
	}
}

func TestSetDataQuotaOrphanedIsFinishedBySendingSQLite(t *testing.T) {
	testSetDataQuotaOrphanedIsFinishedBySending(t, false)
}
func TestSetDataQuotaOrphanedIsFinishedBySendingPostgres(t *testing.T) {
	testSetDataQuotaOrphanedIsFinishedBySending(t, true)
}
