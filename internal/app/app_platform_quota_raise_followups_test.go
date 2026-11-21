package app

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"
)

// Item 3: a raise row written before signing windows were recorded carries receipt {}. Its absence is not final at
// once after an upgrade: a command could have been signed up to 25 s after the row was last written.
func testRaiseRowWithNoRecordedWindowWaitsOutTheOldLifetime(t *testing.T, pg bool) {
	s, lease, endpoint, raise, _ := platformUpdateRaiseFixture(t, pg, false)
	raise.execute = func(platformSetDataQuotaIntent) (platformSetDataQuotaObservation, error) {
		return nil, errors.New("lost")
	}
	raise.read = func(platformSetDataQuotaIntent) (platformSetDataQuotaObservation, error) {
		return nil, errors.New("lost")
	}
	_, _ = s.runPlatformManagedUpdate(context.Background(), lease, endpoint)
	if err := s.finishPlatformUpdateForReview(context.Background(), platformLeaseOf(t, s, lease.id)); err != nil {
		t.Fatal(err)
	}
	raise.read = func(i platformSetDataQuotaIntent) (platformSetDataQuotaObservation, error) {
		return raise.status(i, "absent", ""), nil
	}
	set := func(age time.Duration) {
		mustExec(t, s.st, `UPDATE app_platform_operation_effects SET receipt_json='{}',updated_ms=? WHERE operation_id=? AND stage='migrate' AND effect_number=?`,
			time.Now().Add(-age).UnixMilli(), lease.id, platformUpdateRaiseEffectNumber)
	}
	set(10 * time.Second)
	if progressed, err := s.settlePlatformQuotaRaises(context.Background(), endpoint); err != nil || progressed != 0 ||
		effectState(t, s, lease.id, "migrate", platformUpdateRaiseEffectNumber) != "unknown" {
		t.Fatalf("an absence inside the old command lifetime was final: %d %v", progressed, err)
	}
	set(40 * time.Second)
	if progressed, err := s.settlePlatformQuotaRaises(context.Background(), endpoint); err != nil || progressed != 1 ||
		effectState(t, s, lease.id, "migrate", platformUpdateRaiseEffectNumber) != "reverted" {
		t.Fatalf("an absence past the old lifetime and the margin did not revert the row: %d %v", progressed, err)
	}
}

func TestRaiseRowWithNoRecordedWindowWaitsOutTheOldLifetimeSQLite(t *testing.T) {
	testRaiseRowWithNoRecordedWindowWaitsOutTheOldLifetime(t, false)
}
func TestRaiseRowWithNoRecordedWindowWaitsOutTheOldLifetimePostgres(t *testing.T) {
	testRaiseRowWithNoRecordedWindowWaitsOutTheOldLifetime(t, true)
}

// Item 2: while a finished operation's raise has no verdict, nothing the runner would block is admitted, so the raise
// can still converge. Once it has, the installation is open again.
func testNothingIsAdmittedOverAnUnsettledRaise(t *testing.T, pg bool) {
	s, actor, enabled, target, runtime := platformDataActionFixture(t, pg)
	(&platformFakeDataRunner{fits: true}).install(t)
	endpoint := platformCheckpointEndpointFixture()
	raise := &platformFakeRaiseRunner{t: t, endpoint: endpoint}
	raise.install(t)
	installation := enabled.installationID
	namespace := platformNamespaceOf(t, s, installation)
	bytes0 := scalar[int64](t, s.st, `SELECT quota_bytes FROM app_platform_data_namespaces WHERE id=?`, namespace)
	inodes0 := scalar[int64](t, s.st, `SELECT quota_inodes FROM app_platform_data_namespaces WHERE id=?`, namespace)
	current := platformSetDataQuotaAtPrepare(t, s, actor, installation, target, runtime, bytes0*2, inodes0, 1720)
	raise.execute = func(platformSetDataQuotaIntent) (platformSetDataQuotaObservation, error) {
		return nil, errors.New("lost")
	}
	raise.read = func(platformSetDataQuotaIntent) (platformSetDataQuotaObservation, error) {
		return nil, errors.New("lost")
	}
	_, _ = s.dispatchPlatformSetDataQuota(context.Background(), current, endpoint)
	if err := s.finishPlatformSetDataQuotaForReview(context.Background(), current, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.createPlatformSetDataQuotaReviewWithRuntimeContext(context.Background(), actor, installation, bytes0*3, inodes0, target, runtime, endpoint); !errors.Is(err, errAppPlatformQuotaRaiseUnsettled) {
		t.Fatalf("a second raise was reviewed over an unsettled one: %v", err)
	}
	for _, action := range []string{"update", "enable", "repair", "restore", "import_data", "set_data_quota"} {
		err := insertPlatformOperation(s.st.poolExec(), platformOperationRow{ID: platformFixtureID(7900), InstallationID: installation, Action: action})
		if !errors.Is(err, errAppPlatformQuotaRaiseUnsettled) {
			t.Fatalf("%s was admitted over an unsettled raise: %v", action, err)
		}
	}
	for _, action := range []string{"disable", "uninstall"} {
		err := insertPlatformOperation(s.st.poolExec(), platformOperationRow{ID: platformFixtureID(7901), InstallationID: installation, Action: action})
		if errors.Is(err, errAppPlatformQuotaRaiseUnsettled) {
			t.Fatalf("%s, which the runner does not block, was refused", action)
		}
	}
	mustExec(t, s.st, `UPDATE app_platform_operation_effects SET receipt_json=? WHERE operation_id=? AND stage='prepare_data' AND effect_number=1`,
		platformImportPendingReceipt(time.Now().UnixMilli()+int64(time.Hour/time.Millisecond)), current.id)
	raise.read = func(i platformSetDataQuotaIntent) (platformSetDataQuotaObservation, error) {
		return raise.receipt(i), nil
	}
	if _, err := s.settlePlatformQuotaRaises(context.Background(), endpoint); err != nil || operationStage(t, s, current.id) != "complete" {
		t.Fatal("the raise did not converge", err)
	}
	if err := platformRefuseWhileRaiseUnsettled(s.st.poolExec(), installation); err != nil {
		t.Fatalf("the installation is still closed after the raise settled: %v", err)
	}
}

func TestNothingIsAdmittedOverAnUnsettledRaiseSQLite(t *testing.T) {
	testNothingIsAdmittedOverAnUnsettledRaise(t, false)
}
func TestNothingIsAdmittedOverAnUnsettledRaisePostgres(t *testing.T) {
	testNothingIsAdmittedOverAnUnsettledRaise(t, true)
}

// Item 2, the update review through the routes an administrator uses.
func testUpdateReviewIsRefusedOverAnUnsettledRaise(t *testing.T, pg bool) {
	h, f, _ := platformReviewHTTPFor(t, pg, true, nil)
	st := f.server.st
	install := scalar[string](t, st, `SELECT id FROM app_platform_operations WHERE installation_id=? AND action='install' LIMIT 1`, f.installation)
	mustExec(t, st, `INSERT INTO app_platform_operation_effects(operation_id,stage,effect_number,effect_id,kind,payload_sha256,runtime_id,controller_epoch,admitted_fence,observed_fence,state,receipt_json,created_ms,updated_ms)
		SELECT operation_id,'prepare_data',9,?,'set-data-quota',payload_sha256,runtime_id,controller_epoch,admitted_fence,0,'unknown','{}',created_ms,updated_ms
		FROM app_platform_operation_effects WHERE operation_id=? LIMIT 1`, platformFixtureID(7910), install)
	refused := h.post("/api/admin/app-platform/plans", platformReviewBody(f))
	if refused.Code != http.StatusConflict || !strings.Contains(refused.Body.String(), "app_platform_quota_raise_unsettled") {
		t.Fatalf("an update was reviewed over an unsettled raise: %d %s", refused.Code, refused.Body.String())
	}
	mustExec(t, st, `UPDATE app_platform_operation_effects SET state='reverted' WHERE effect_id=?`, platformFixtureID(7910))
	h.json(h.post("/api/admin/app-platform/plans", platformReviewBody(f)), http.StatusOK)
}

func TestUpdateReviewIsRefusedOverAnUnsettledRaiseSQLite(t *testing.T) {
	testUpdateReviewIsRefusedOverAnUnsettledRaise(t, false)
}
func TestUpdateReviewIsRefusedOverAnUnsettledRaisePostgres(t *testing.T) {
	testUpdateReviewIsRefusedOverAnUnsettledRaise(t, true)
}

// Item 4: once the installation is removed and its data deleted, nothing reads its quota chain and the pin ends.
func TestPlatformRetentionReleasesTheRaisePinWithTheInstallationsData(t *testing.T) {
	bothDrivers(t, func(t *testing.T, pg bool) {
		f := newRetFixture(t, pg)
		old := retNow - 100*retDay
		gone := f.removedInstallation()
		f.namespace(gone, "deleted")
		goneOp, _ := f.operation(gone, "set_data_quota", "complete", old, 1)
		f.effectAt(goneOp, "prepare_data", 1, "set-data-quota", "applied", "{}")
		kept := f.removedInstallation()
		f.namespace(kept, "retained")
		keptOp, _ := f.operation(kept, "set_data_quota", "complete", old, 1)
		f.effectAt(keptOp, "prepare_data", 1, "set-data-quota", "applied", "{}")
		f.sweep(retNow)
		if f.exists("operations", goneOp) {
			t.Error("the pin outlived the installation and its deleted data")
		}
		if !f.exists("operations", keptOp) {
			t.Error("the raise of an installation whose data is retained was swept")
		}
	})
}
