package app

import (
	"context"
	"errors"
	"testing"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/journal"
)

// platformRaisedSettledFixture is an update that raised the quota, whose migration then failed and was settled: the
// checkpoint was restored, and the quota stays raised.
func platformRaisedSettledFixture(t *testing.T, pg bool) *settleFixture {
	t.Helper()
	platformWritableUpdateFixtures(t)
	s, lease, endpoint, raise, migration := platformUpdateRaiseFixture(t, pg, true)
	raise.execute = func(i platformSetDataQuotaIntent) (platformSetDataQuotaObservation, error) {
		return raise.receipt(i), nil
	}
	next, err := s.runPlatformManagedUpdate(context.Background(), lease, endpoint)
	if err != nil || next.snapshot.Stage != appplatform.StageMigrate {
		t.Fatal("the raise did not run", next.snapshot, err)
	}
	failedScript(migration, endpoint)
	if _, err := s.dispatchPlatformMigrateData(context.Background(), next, endpoint); !errors.Is(err, errPlatformMigrationEnded) {
		t.Fatal(err)
	}
	migration.calls = nil
	migration.executeMigrate, migration.readMigrate = nil, nil
	f := &settleFixture{s: s, lease: lease, endpoint: endpoint, fake: migration, op: lease.id}
	migration.executeRestore = func(i platformRestoreIntent) (platformSettlingRestoreObservation, error) {
		return restoreAnswer(i, endpoint), nil
	}
	f.step(t)
	f.step(t)
	if f.settleState(t) != "settled" {
		t.Fatal("the fixture did not settle", f.settleState(t))
	}
	return f
}

// Test 13 (through a failed migration, which also follows the raise): the quota stays raised after the rollback,
// the rollback's start carries the raised quota, the history loaders pass and the application launches.
func testRaisedQuotaStaysAfterTheRollback(t *testing.T, pg bool) {
	f := platformRaisedSettledFixture(t, pg)
	s, installation := f.s, f.lease.installationID
	namespace := platformNamespaceOf(t, s, installation)
	rowsBefore := platformAppliedRaiseRows(t, s)
	h := newMigrationHTTP(t, f)
	if h.launch() == 200 {
		t.Fatal("an application launched while its failed update was unresolved")
	}
	rollBackOverHTTP(t, f, h)
	if got := h.launch(); got != 200 {
		t.Fatalf("launch after the rollback answered %d", got)
	}
	if scalar[int64](t, s.st, `SELECT quota_bytes FROM app_platform_data_namespaces WHERE id=?`, namespace) != platformRaisedBytes ||
		scalar[int64](t, s.st, `SELECT quota_inodes FROM app_platform_data_namespaces WHERE id=?`, namespace) != platformRaisedInodes {
		t.Fatal("the rollback lowered the quota")
	}
	if _, _, chain, err := platformCurrentNamespaceQuota(s.st.poolExec(), installation, namespace); err != nil || len(chain) != 2 {
		t.Fatal("the chain after the rollback", chain, err)
	}
	var receipt string
	if err := s.st.queryRow(`SELECT receipt_json FROM app_platform_operation_effects WHERE kind='restore-start' AND state='applied'`).Scan(&receipt); err != nil {
		t.Fatal("the rollback's start", err)
	}
	running, err := journal.ParseUpdateStartRunningReceipt([]byte(receipt))
	if err != nil || running.Claim.Preflight == nil || running.Claim.Preflight.QuotaBytes != "1073741824" || running.Claim.Preflight.QuotaInodes != "100000" {
		t.Fatalf("the rollback's start does not carry the raised quota: %+v %v", running.Claim.Preflight, err)
	}
	access, err := s.preparePlatformWritableBackendAccess(context.Background(), platformManagedFixtureSession(t, s), installation)
	if err != nil {
		t.Fatal("history after the rollback", err)
	}
	access.Close()
	platformRequireSameRaiseRows(t, rowsBefore, platformAppliedRaiseRows(t, s))
}

func TestRaisedQuotaStaysAfterTheRollbackSQLite(t *testing.T) {
	testRaisedQuotaStaysAfterTheRollback(t, false)
}
func TestRaisedQuotaStaysAfterTheRollbackPostgres(t *testing.T) {
	testRaisedQuotaStaysAfterTheRollback(t, true)
}

// Test 14, the raise applied: the retry's review sees the raised quota, so its plan has no data_quota, its stage
// raises nothing and goes straight to the migration.
func testRetryOfAnUpdateWhoseRaiseApplied(t *testing.T, pg bool) {
	f := platformRaisedSettledFixture(t, pg)
	h := newMigrationHTTP(t, f)
	reportManagedRuntime(f, lastPlatformUpdateFixture.target.Profile)
	(&platformFakeDataRunner{fits: true}).install(t)
	raise := &platformFakeRaiseRunner{t: t, endpoint: f.endpoint}
	raise.install(t)
	operation := h.retryOverHTTP(platformFixtureID(7800))
	plan, raw := platformPlanOf(t, f.s, operation)
	if plan.DataQuota != nil {
		t.Fatal("the retry of an update that raised the quota raises it again", raw)
	}
	lease := driveRetryToMigrate(t, f, operation)
	h.activateMigrated(t, lease, "migrated")
	if raise.count("execute") != 0 || scalar[int](t, f.s.st, `SELECT COUNT(*) FROM app_platform_operation_effects WHERE operation_id=? AND kind='set-data-quota'`, operation) != 0 {
		t.Fatal("the retry sent or recorded a raise")
	}
	if got := h.launch(); got != 200 {
		t.Fatalf("launch after the retry answered %d", got)
	}
}

func TestRetryOfAnUpdateWhoseRaiseAppliedSQLite(t *testing.T) {
	testRetryOfAnUpdateWhoseRaiseApplied(t, false)
}
func TestRetryOfAnUpdateWhoseRaiseAppliedPostgres(t *testing.T) {
	testRetryOfAnUpdateWhoseRaiseApplied(t, true)
}

// Test 14, the raise reverted: the update has no migration to settle, so there is nothing to retry from. The only way
// on is the rollback, and a retry review of it is refused.
func testRetryOfAnUpdateWhoseRaiseRevertedIsRefused(t *testing.T, pg bool) {
	s, lease, endpoint, raise, _ := platformUpdateRaiseFixture(t, pg, true)
	raise.execute = func(i platformSetDataQuotaIntent) (platformSetDataQuotaObservation, error) {
		return raise.status(i, "failed", "quota_raise_unsafe"), nil
	}
	if _, err := s.runPlatformManagedUpdate(context.Background(), lease, endpoint); !errors.Is(err, errPlatformMigrationEnded) {
		t.Fatal(err)
	}
	if err := requirePlatformRetrySource(s.st.poolExec(), lease.installationID, lease.id); !errors.Is(err, errAppPlatformPlanChanged) {
		t.Fatalf("a retry of an update whose raise reverted was offered: %v", err)
	}
	platformRequireRestoreCandidate(t, s, lease, true)
}

func TestRetryOfAnUpdateWhoseRaiseRevertedIsRefusedSQLite(t *testing.T) {
	testRetryOfAnUpdateWhoseRaiseRevertedIsRefused(t, false)
}
func TestRetryOfAnUpdateWhoseRaiseRevertedIsRefusedPostgres(t *testing.T) {
	testRetryOfAnUpdateWhoseRaiseRevertedIsRefused(t, true)
}

// Test 17: a raise row that is not applied still counts in the registration's total, so the comparison with the base
// fails closed.
func testRegistrationRefusesAnExtraRaiseRow(t *testing.T, pg bool) {
	s, lease, endpoint, raise, _ := platformUpdateRaiseFixture(t, pg, false)
	raise.execute = func(i platformSetDataQuotaIntent) (platformSetDataQuotaObservation, error) {
		return raise.receipt(i), nil
	}
	start, err := s.runPlatformManagedUpdate(context.Background(), lease, endpoint)
	if err != nil || start.snapshot.Stage != appplatform.StageStart {
		t.Fatal(start.snapshot, err)
	}
	activate, _, _, _ := platformMigratedUpdateToRegistration(t, s, start, endpoint)
	if _, err := s.preparePlatformUpdateStartRegistration(context.Background(), activate); err != nil {
		t.Fatal("registration with the one applied raise", err)
	}
	mustExec(t, s.st, `INSERT INTO app_platform_operation_effects(operation_id,stage,effect_number,effect_id,kind,payload_sha256,runtime_id,controller_epoch,admitted_fence,observed_fence,state,receipt_json,created_ms,updated_ms)
		SELECT operation_id,stage,5,?,kind,payload_sha256,runtime_id,controller_epoch,admitted_fence,0,'prepared','{}',created_ms,updated_ms
		FROM app_platform_operation_effects WHERE operation_id=? AND stage='migrate' AND effect_number=?`, platformFixtureID(7810), lease.id, platformUpdateRaiseEffectNumber)
	if _, err := s.preparePlatformUpdateStartRegistration(context.Background(), activate); err == nil {
		t.Fatal("a registration was accepted with an extra raise row")
	}
}

func TestRegistrationRefusesAnExtraRaiseRowSQLite(t *testing.T) {
	testRegistrationRefusesAnExtraRaiseRow(t, false)
}
func TestRegistrationRefusesAnExtraRaiseRowPostgres(t *testing.T) {
	testRegistrationRefusesAnExtraRaiseRow(t, true)
}

// The settings route's retry choice and the restore review both load for a failed update that carries a code of its
// own: the recovery is the same, only the reason shown differs.
func TestRecoveryReviewsAcceptTheQuotaCodes(t *testing.T) {
	for _, code := range []string{"update_recovery_required", "data_quota_capacity", "data_quota_unavailable"} {
		if !platformUpdateRecoveryCodes(code) {
			t.Fatalf("%s is refused by the recovery reviews", code)
		}
	}
	for _, code := range []string{"", "update_retry_exhausted", "data_quota_recovery_required", "restore_recovery_required"} {
		if platformUpdateRecoveryCodes(code) {
			t.Fatalf("%q is accepted by the recovery reviews", code)
		}
	}
}
