package app

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/journal"
)

// platformFakeRaiseRunner answers the raise of an update from memory. Every call is recorded; a nil step fails the
// test, so a runner call the test did not expect cannot pass unnoticed.
type platformFakeRaiseRunner struct {
	t        *testing.T
	endpoint platformRuntimeEndpoint
	execute  func(platformSetDataQuotaIntent) (platformSetDataQuotaObservation, error)
	read     func(platformSetDataQuotaIntent) (platformSetDataQuotaObservation, error)
	calls    []string
	sent     []platformSetDataQuotaIntent
}

func (f *platformFakeRaiseRunner) count(name string) int {
	n := 0
	for _, c := range f.calls {
		if c == name {
			n++
		}
	}
	return n
}

func (f *platformFakeRaiseRunner) install(t *testing.T) {
	t.Helper()
	executeBefore, readBefore := platformSetDataQuotaExecute, platformSetDataQuotaRead
	t.Cleanup(func() { platformSetDataQuotaExecute, platformSetDataQuotaRead = executeBefore, readBefore })
	platformSetDataQuotaExecute = func(_ context.Context, _ platformRuntimeEndpoint, i platformSetDataQuotaIntent) (platformSetDataQuotaObservation, error) {
		f.calls = append(f.calls, "execute")
		f.sent = append(f.sent, i)
		if f.execute == nil {
			t.Errorf("unscripted raise execute")
			return nil, errors.New("unscripted execute")
		}
		return f.execute(i)
	}
	platformSetDataQuotaRead = func(_ context.Context, _ platformRuntimeEndpoint, i platformSetDataQuotaIntent) (platformSetDataQuotaObservation, error) {
		f.calls = append(f.calls, "read")
		if f.read == nil {
			t.Errorf("unscripted raise read")
			return nil, errors.New("unscripted read")
		}
		return f.read(i)
	}
}

func (f *platformFakeRaiseRunner) receipt(i platformSetDataQuotaIntent) platformSetDataQuotaObservation {
	return platformQuotaObservation(i, f.endpoint)
}

func (f *platformFakeRaiseRunner) status(i platformSetDataQuotaIntent, state, code string) platformSetDataQuotaObservation {
	observation := platformQuotaObservation(i, f.endpoint)
	h := i.command.CommandHeader
	observation.status = &journal.MigrateDataStatus{
		Format: "portal.runner.migration-status/1-dev.1", Kind: h.Kind, DeploymentID: h.DeploymentID, ControllerEpoch: h.ControllerEpoch,
		InstallationID: h.InstallationID, OperationID: h.OperationID, TargetGeneration: h.TargetGeneration, Stage: h.Stage,
		EffectNumber: h.EffectNumber, EffectID: h.EffectID, PayloadSHA256: h.PayloadSHA256, State: state, Code: code,
		ObservedMS: strconv.FormatInt(observation.receivedAt.UnixMilli(), 10),
	}
	return observation
}

// A fake runner's status is observed no later than the Portal receives it. The runner answers at one instant; reading
// the clock once for the observation and again for the status put the status a millisecond after its own receipt
// whenever a millisecond boundary fell between the two reads, and the Portal rightly refused such a status.
func TestPlatformFakeRaiseStatusIsObservedBeforeItIsReceived(t *testing.T) {
	saved := platformQuotaClock
	t.Cleanup(func() { platformQuotaClock = saved })
	platformQuotaClock = func() time.Time {
		time.Sleep(1200 * time.Microsecond) // every read lands in a later millisecond than the one before
		return time.Now().UTC()
	}
	observation, ok := (&platformFakeRaiseRunner{}).status(platformSetDataQuotaIntent{}, "absent", "").(platformQuotaTestObservation)
	if !ok || observation.status == nil {
		t.Fatal("the fake runner did not answer with a status")
	}
	observed, err := strconv.ParseInt(observation.status.ObservedMS, 10, 64)
	if err != nil || observed > observation.receivedAt.UnixMilli() {
		t.Fatal("a status was observed after it was received", observed, observation.receivedAt.UnixMilli(), err)
	}
}

// platformUpdateRaiseFixture brings a reviewed managed update, whose target asks for a larger quota (and for a data
// migration when migrating), to the migrate stage with the source removal applied.
func platformUpdateRaiseFixture(t *testing.T, pg, migrating bool) (*Server, platformOperationLease, platformRuntimeEndpoint, *platformFakeRaiseRunner, *platformFakeMigrationRunner) {
	t.Helper()
	platformFakeObservedAt.Clear()
	(&platformFakeDataRunner{fits: true}).install(t)
	mutate := func(m *appplatform.Manifest) {
		if migrating {
			platformMigrationManifest(nil)(m)
		}
		m.Data.Quota = &appplatform.DataQuota{Bytes: 1 << 30, Inodes: 100000}
	}
	s, lease, quiesce, endpoint := platformManagedUpdateQuiesceFixtureWith(t, pg, mutate)
	snapshot, err := s.commitPlatformManagedUpdateQuiesce(context.Background(), lease, quiesce, platformManagedUpdateQuiesceReceipt(quiesce))
	if err != nil {
		t.Fatal(err)
	}
	checkpointEndpoint := platformCheckpointEndpointFixture()
	checkpoint, err := s.preparePlatformCheckpoint(context.Background(), snapshot)
	if err != nil {
		t.Fatal(err)
	}
	migrate, err := s.commitPlatformCheckpoint(context.Background(), snapshot, checkpoint, checkpointEndpoint,
		platformCheckpointObservationFixture(checkpoint, checkpointEndpoint))
	if err != nil || migrate.snapshot.Stage != appplatform.StageMigrate {
		t.Fatal("checkpoint did not reach the migrate stage", migrate.snapshot, err)
	}
	removal, err := s.preparePlatformManagedUpdateMigrate(context.Background(), migrate)
	if err != nil {
		t.Fatal(err)
	}
	after, err := s.commitPlatformManagedUpdateQuiesce(context.Background(), migrate, removal, platformManagedUpdateQuiesceReceipt(removal))
	if err != nil {
		t.Fatal(err)
	}
	if after.snapshot.Stage != appplatform.StageMigrate || effectState(t, s, after.id, "migrate", 1) != "applied" {
		t.Fatal("the removal moved the operation of a plan that raises the quota", after.snapshot)
	}
	migration := &platformFakeMigrationRunner{t: t}
	s.platformMigration = migration
	s.platformSettleStart = platformFakeSettleStart(migration, nil)
	raise := &platformFakeRaiseRunner{t: t, endpoint: endpoint}
	raise.install(t)
	return s, after, endpoint, raise, migration
}

func platformNamespaceOf(t *testing.T, s *Server, installation string) string {
	t.Helper()
	return scalar[string](t, s.st, `SELECT id FROM app_platform_data_namespaces WHERE owner_installation_id=?`, installation)
}

func platformPlanOf(t *testing.T, s *Server, operation string) (platformManagedUpdatePlan, string) {
	t.Helper()
	raw := scalar[string](t, s.st, `SELECT p.plan_json FROM app_platform_operations o JOIN app_platform_review_plans p ON p.id=o.review_plan_id WHERE o.id=?`, operation)
	var plan platformManagedUpdatePlan
	if err := json.Unmarshal([]byte(raw), &plan); err != nil {
		t.Fatal(err)
	}
	return plan, raw
}

// platformCompleteUpdateFromStart runs an update that reached the start through registration and activation.
func platformCompleteUpdateFromStart(t *testing.T, s *Server, start platformOperationLease, endpoint platformRuntimeEndpoint) (platformOperationLease, platformUpdateStartIntent) {
	t.Helper()
	activate, registration, observation, intent := platformMigratedUpdateToRegistration(t, s, start, endpoint)
	registered, err := s.commitPlatformUpdateStartRegistration(context.Background(), activate, registration, endpoint, observation)
	if err != nil {
		t.Fatal("registration", err)
	}
	applied, boot := platformUpdateStartBootFixture(t, s, registered, endpoint)
	defer applied.registration.Close()
	result, err := s.commitPlatformUpdateStartActivation(context.Background(), registered, applied, boot)
	if err != nil || result.snapshot.Stage != appplatform.StageComplete {
		t.Fatal("activation", result, err)
	}
	return registered, intent
}

const (
	platformRaisedBytes  = 1 << 30
	platformRaisedInodes = 100000
)

// Test 3: an update that raises nothing is exactly what it was: no raise row, a plan without data_quota, and a
// registration that counts four effects.
func testUpdateWithoutRaiseIsUnchanged(t *testing.T, pg bool) {
	s, lease, endpoint, _ := platformMigrationStageFixture(t, pg, false)
	if lease.snapshot.Stage != appplatform.StageStart {
		t.Fatal("an update with no raise and no migration did not move to the start", lease.snapshot)
	}
	if scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_operation_effects WHERE operation_id=? AND kind='set-data-quota'`, lease.id) != 0 {
		t.Fatal("an update that raises nothing recorded a raise")
	}
	plan, raw := platformPlanOf(t, s, lease.id)
	if plan.DataQuota != nil || strings.Contains(raw, "data_quota") {
		t.Fatal("the finalized plan of an update that raises nothing carries data_quota", raw)
	}
	registered, _ := platformCompleteUpdateFromStart(t, s, lease, endpoint)
	if scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_operation_effects WHERE operation_id=? AND kind NOT IN (`+platformInstallationNeutralEffectKinds+`)`, registered.id) != 5 {
		t.Fatal("the update did not carry exactly the stop, snapshot, removal, start and registration")
	}
}

func TestUpdateWithoutRaiseIsUnchangedSQLite(t *testing.T) {
	t.Parallel()
	testUpdateWithoutRaiseIsUnchanged(t, false)
}
func TestUpdateWithoutRaiseIsUnchangedPostgres(t *testing.T) {
	t.Parallel()
	testUpdateWithoutRaiseIsUnchanged(t, true)
}

// Test 4 and 6, through the scheduler's own stage function: the raise follows the source removal and precedes the
// start, the namespace row takes the new limits with one more revision, the start carries them, the registration
// accepts five effects and every later reader works.
func testUpdateRaisesTheQuotaWithoutAMigration(t *testing.T, pg bool) {
	platformWritableUpdateFixtures(t)
	s, lease, endpoint, raise, _ := platformUpdateRaiseFixture(t, pg, false)
	installation := lease.installationID
	namespace := platformNamespaceOf(t, s, installation)
	revision := scalar[int64](t, s.st, `SELECT revision FROM app_platform_data_namespaces WHERE id=?`, namespace)
	bytes0 := scalar[int64](t, s.st, `SELECT quota_bytes FROM app_platform_data_namespaces WHERE id=?`, namespace)
	audited := scalar[int](t, s.st, `SELECT COUNT(*) FROM audit_log WHERE action='app_platform.data_quota_raised' AND target_id=?`, lease.id)
	raise.execute = func(i platformSetDataQuotaIntent) (platformSetDataQuotaObservation, error) {
		if effectState(t, s, lease.id, "migrate", platformUpdateRaiseEffectNumber) != "prepared" {
			t.Error("the raise was sent before its effect was recorded")
		}
		if h := i.command.CommandHeader; h.Stage != "prepare_data" || h.EffectNumber != "1" || h.Kind != "set-data-quota" || h.OperationID != lease.id {
			t.Errorf("the command header is not the runner's slot: %+v", h)
		}
		return raise.receipt(i), nil
	}
	next, err := s.runPlatformManagedUpdate(context.Background(), lease, endpoint)
	if err != nil || next.snapshot.Stage != appplatform.StageStart || operationStage(t, s, lease.id) != "start" {
		t.Fatalf("the stage function did not raise and move to the start: %v %v", next.snapshot, err)
	}
	if raise.count("execute") != 1 || effectState(t, s, lease.id, "migrate", 1) != "applied" ||
		effectState(t, s, lease.id, "migrate", platformUpdateRaiseEffectNumber) != "applied" || effectState(t, s, lease.id, "migrate", 2) != "" {
		t.Fatalf("the migrate stage did not run removal then raise: %v", raise.calls)
	}
	if scalar[int64](t, s.st, `SELECT quota_bytes FROM app_platform_data_namespaces WHERE id=?`, namespace) != platformRaisedBytes ||
		scalar[int64](t, s.st, `SELECT quota_inodes FROM app_platform_data_namespaces WHERE id=?`, namespace) != platformRaisedInodes ||
		scalar[int64](t, s.st, `SELECT revision FROM app_platform_data_namespaces WHERE id=?`, namespace) != revision+1 ||
		scalar[int64](t, s.st, `SELECT observed_bytes FROM app_platform_data_namespaces WHERE id=?`, namespace) != 4096 {
		t.Fatal("the namespace row does not hold the raised limits, one more revision and the receipt's usage")
	}
	if scalar[int](t, s.st, `SELECT COUNT(*) FROM audit_log WHERE action='app_platform.data_quota_raised' AND target_id=?`, lease.id) != audited+1 {
		t.Fatal("the raise is not audited exactly once")
	}
	rowsBefore := platformAppliedRaiseRows(t, s)
	registered, startIntent := platformCompleteUpdateFromStart(t, s, next, endpoint)
	if startIntent.command.Payload.QuotaBytes != strconv.Itoa(platformRaisedBytes) || startIntent.command.Payload.QuotaInodes != strconv.Itoa(platformRaisedInodes) {
		t.Fatalf("the start carries %s/%s, not the raised quota", startIntent.command.Payload.QuotaBytes, startIntent.command.Payload.QuotaInodes)
	}
	if scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_operation_effects WHERE operation_id=? AND kind NOT IN (`+platformInstallationNeutralEffectKinds+`)`, registered.id) != 6 {
		t.Fatal("the registration did not count the raise")
	}

	// Test 6: every reader works after the completed raised update.
	bytes, inodes, chain, err := platformCurrentNamespaceQuota(s.st.poolExec(), installation, namespace)
	if err != nil || bytes != platformRaisedBytes || inodes != platformRaisedInodes || len(chain) != 2 || chain[0].Bytes != bytes0 {
		t.Fatal("chain after the update", bytes, inodes, chain, err)
	}
	actor := platformManagedFixtureSession(t, s)
	if launch, err := s.inspectPlatformLaunchAccess(actor, installation); err != nil || launch.generation != "2" {
		t.Fatal("launch after a raised update", launch, err)
	}
	access, err := s.preparePlatformWritableBackendAccess(context.Background(), actor, installation)
	if err != nil {
		t.Fatal("launch history after a raised update", err)
	}
	access.Close()
	// The next update review starts from the new value, and a reviewed raise sees it as the current one and refuses
	// to lower it.
	var projection platformUpdateProjection
	f := lastPlatformUpdateFixture.get(t)
	projection, err = platformBuildManagedUpdateProjection(s.st.poolExec(), installation, f.source, f.revision, f.policy, f.format,
		platformActorFacts{authorityFingerprint: f.sourcePlan.ActorAuthority}, platformActorControl{epoch: f.activeEpoch, nowMS: 1}, f.target, f.release)
	if err == nil && projection.DataQuota != nil && projection.DataQuota.CurrentBytes != platformRaisedBytes {
		t.Fatal("the next update review does not start from the raised quota", projection.DataQuota)
	}
	platformRequireSameRaiseRows(t, rowsBefore, platformAppliedRaiseRows(t, s))
}

func TestUpdateRaisesTheQuotaWithoutAMigrationSQLite(t *testing.T) {
	testUpdateRaisesTheQuotaWithoutAMigration(t, false)
}
func TestUpdateRaisesTheQuotaWithoutAMigrationPostgres(t *testing.T) {
	testUpdateRaisesTheQuotaWithoutAMigration(t, true)
}

// Test 5: with a migration the raise comes first, the migrate-data payload carries the new quota, and the raise
// refuses to be prepared once a migrate-data row exists.
func testUpdateRaisesTheQuotaBeforeAMigration(t *testing.T, pg bool) {
	s, lease, endpoint, raise, migration := platformUpdateRaiseFixture(t, pg, true)
	raise.execute = func(i platformSetDataQuotaIntent) (platformSetDataQuotaObservation, error) {
		return raise.receipt(i), nil
	}
	next, err := s.runPlatformManagedUpdate(context.Background(), lease, endpoint)
	if err != nil || next.snapshot.Stage != appplatform.StageMigrate || effectState(t, s, lease.id, "migrate", platformUpdateRaiseEffectNumber) != "applied" ||
		effectState(t, s, lease.id, "migrate", 2) != "" {
		t.Fatalf("the raise did not run first and leave the migration to the stage: %v %v", next.snapshot, err)
	}
	var sent platformMigrateIntent
	migration.executeMigrate = func(i platformMigrateIntent) (platformMigrateObservation, error) {
		sent = i
		return platformMigrateAnswers(i, endpoint, "migrated", ""), nil
	}
	started, err := s.runPlatformManagedUpdate(context.Background(), next, endpoint)
	if err != nil || started.snapshot.Stage != appplatform.StageStart {
		t.Fatal("the migration did not follow the raise", started.snapshot, err)
	}
	if sent.command.Payload.QuotaBytes != strconv.Itoa(platformRaisedBytes) || sent.command.Payload.QuotaInodes != strconv.Itoa(platformRaisedInodes) {
		t.Fatalf("migrate-data carries %s/%s, not the raised quota", sent.command.Payload.QuotaBytes, sent.command.Payload.QuotaInodes)
	}
	if raise.count("execute") != 1 {
		t.Fatalf("the raise was sent %d times", raise.count("execute"))
	}
}

func TestUpdateRaisesTheQuotaBeforeAMigrationSQLite(t *testing.T) {
	testUpdateRaisesTheQuotaBeforeAMigration(t, false)
}
func TestUpdateRaisesTheQuotaBeforeAMigrationPostgres(t *testing.T) {
	testUpdateRaisesTheQuotaBeforeAMigration(t, true)
}

func testUpdateRaiseRefusesOutOfOrder(t *testing.T, pg bool) {
	s, lease, _, _, _ := platformUpdateRaiseFixture(t, pg, true)
	// A migrate-data row already present: the raise would come after the migration whose payload carries the quota.
	mustExec(t, s.st, `INSERT INTO app_platform_operation_effects(operation_id,stage,effect_number,effect_id,kind,payload_sha256,runtime_id,controller_epoch,admitted_fence,observed_fence,state,receipt_json,created_ms,updated_ms)
		SELECT operation_id,stage,2,?,'migrate-data',payload_sha256,runtime_id,controller_epoch,admitted_fence,0,'prepared','{}',created_ms,updated_ms
		FROM app_platform_operation_effects WHERE operation_id=? AND stage='migrate' AND effect_number=1`, platformFixtureID(7001), lease.id)
	if _, err := s.preparePlatformUpdateRaise(context.Background(), lease); err == nil {
		t.Fatal("a raise was prepared after a migrate-data row exists")
	}
	if effectState(t, s, lease.id, "migrate", platformUpdateRaiseEffectNumber) != "" {
		t.Fatal("a refused raise left a row")
	}
	mustExec(t, s.st, `DELETE FROM app_platform_operation_effects WHERE operation_id=? AND stage='migrate' AND effect_number=2`, lease.id)
	// Without the applied removal there is nothing to raise after.
	mustExec(t, s.st, `UPDATE app_platform_operation_effects SET state='prepared' WHERE operation_id=? AND stage='migrate' AND effect_number=1`, lease.id)
	if _, err := s.preparePlatformUpdateRaise(context.Background(), lease); err == nil {
		t.Fatal("a raise was prepared before the source was removed")
	}
}

func TestUpdateRaiseRefusesOutOfOrderSQLite(t *testing.T) { testUpdateRaiseRefusesOutOfOrder(t, false) }
func TestUpdateRaiseRefusesOutOfOrderPostgres(t *testing.T) {
	testUpdateRaiseRefusesOutOfOrder(t, true)
}

// Tests 8 and 9: the runner's definite answers end the update at once, with the row reverted, the code that says why,
// and the namespace and the chain as they were. The rollback is then allowed.
func testUpdateRaiseDefiniteEnds(t *testing.T, pg bool) {
	for name, c := range map[string]struct {
		answer func(*platformFakeRaiseRunner, platformSetDataQuotaIntent) (platformSetDataQuotaObservation, error)
		code   string
	}{
		"typed failure": {func(r *platformFakeRaiseRunner, i platformSetDataQuotaIntent) (platformSetDataQuotaObservation, error) {
			return r.status(i, "failed", "quota_raise_unsafe"), nil
		}, "data_quota_unavailable"},
		"capacity refusal": {func(r *platformFakeRaiseRunner, i platformSetDataQuotaIntent) (platformSetDataQuotaObservation, error) {
			return nil, &journal.CapacityRefusalError{Receipt: platformRaiseRefusalFor(i)}
		}, "data_quota_capacity"},
	} {
		t.Run(name, func(t *testing.T) {
			s, lease, endpoint, raise, _ := platformUpdateRaiseFixture(t, pg, false)
			namespace := platformNamespaceOf(t, s, lease.installationID)
			bytes0 := scalar[int64](t, s.st, `SELECT quota_bytes FROM app_platform_data_namespaces WHERE id=?`, namespace)
			raise.execute = func(i platformSetDataQuotaIntent) (platformSetDataQuotaObservation, error) { return c.answer(raise, i) }
			refused := scalar[int](t, s.st, `SELECT COUNT(*) FROM audit_log WHERE action='app_platform.capacity_refused' AND target_id=?`, lease.id)
			_, err := s.runPlatformManagedUpdate(context.Background(), lease, endpoint)
			if !errors.Is(err, errPlatformMigrationEnded) {
				t.Fatalf("the update did not end at once: %v", err)
			}
			if raise.count("execute") != 1 {
				t.Fatalf("calls: %v", raise.calls)
			}
			if operationStage(t, s, lease.id) != "recovery_required" || effectState(t, s, lease.id, "migrate", platformUpdateRaiseEffectNumber) != "reverted" ||
				scalar[string](t, s.st, `SELECT error_code FROM app_platform_operations WHERE id=?`, lease.id) != c.code ||
				scalar[string](t, s.st, `SELECT phase FROM app_platform_installations WHERE id=?`, lease.installationID) != "recovery_required" {
				t.Fatal("the update did not end recovery_required with its row reverted and its code")
			}
			if scalar[int64](t, s.st, `SELECT quota_bytes FROM app_platform_data_namespaces WHERE id=?`, namespace) != bytes0 {
				t.Fatal("a definite end changed the namespace")
			}
			if _, _, chain, err := platformCurrentNamespaceQuota(s.st.poolExec(), lease.installationID, namespace); err != nil || len(chain) != 1 {
				t.Fatal("a reverted raise entered the chain", chain, err)
			}
			if err := platformRequireSettledRaise(s.st.poolExec(), lease.id); err != nil {
				t.Fatal("a reverted raise blocks the recovery choice", err)
			}
			if name == "capacity refusal" && scalar[int](t, s.st, `SELECT COUNT(*) FROM audit_log WHERE action='app_platform.capacity_refused' AND target_id=?`, lease.id) != refused+1 {
				t.Fatal("the refusal was not recorded")
			}
			// The rollback is allowed: the restore review loads for the update's recovery set.
			platformRequireRestoreCandidate(t, s, lease, true)
		})
	}
}

func TestUpdateRaiseDefiniteEndsSQLite(t *testing.T)   { testUpdateRaiseDefiniteEnds(t, false) }
func TestUpdateRaiseDefiniteEndsPostgres(t *testing.T) { testUpdateRaiseDefiniteEnds(t, true) }

func platformRaiseRefusalFor(i platformSetDataQuotaIntent) journal.CapacityRefusal {
	h := i.command.CommandHeader
	return journal.CapacityRefusal{
		Format: "portal.runner.capacity-refusal/1-dev.1", Outcome: "capacity_exhausted", Dimension: journal.CapacityDataQuota,
		Requested: "1073741824", Remaining: "1048576", Stage: h.Stage, EffectNumber: h.EffectNumber, EffectID: h.EffectID, Kind: h.Kind,
		PayloadSHA256: h.PayloadSHA256, DeploymentID: h.DeploymentID, ControllerEpoch: h.ControllerEpoch, InstallationID: h.InstallationID,
		OperationID: h.OperationID, TargetGeneration: h.TargetGeneration, SettledMS: strconv.FormatInt(time.Now().UnixMilli(), 10),
	}
}

// platformRequireRestoreCandidate checks whether the restore review of the failed update's recovery set loads.
func platformRequireRestoreCandidate(t *testing.T, s *Server, lease platformOperationLease, want bool) {
	t.Helper()
	recovery := scalar[string](t, s.st, `SELECT id FROM app_platform_recovery_sets WHERE operation_id=? AND state='verified'`, lease.id)
	epoch := scalar[string](t, s.st, `SELECT controller_epoch FROM app_platform_installations WHERE id=?`, lease.installationID)
	dek, err := s.dek()
	if err != nil {
		t.Fatal(err)
	}
	runtime, seed, err := s.platformDataRuntime(s.st.poolExec(), epoch, dek)
	clear(seed)
	if err != nil {
		t.Fatal(err)
	}
	now, err := platformDatabaseNow(s.st.poolExec())
	if err != nil {
		t.Fatal(err)
	}
	_, err = loadPlatformManagedRestoreReviewCandidate(s.st.poolExec(), lease.installationID, recovery, runtime, now)
	if want && err != nil {
		t.Fatalf("the rollback was refused: %v", err)
	}
	if !want && !errors.Is(err, errAppPlatformQuotaRaiseUnsettled) {
		t.Fatalf("the rollback was not refused for the unsettled raise: %v", err)
	}
}

// Test 10: attempts that run out end the update recovery_required with the raise unknown; the restore review waits;
// the convergence step settles it from the runner's answer.
func testUpdateRaiseUnknownConverges(t *testing.T, pg bool) {
	for name, c := range map[string]struct {
		read    func(*platformFakeRaiseRunner, platformSetDataQuotaIntent) (platformSetDataQuotaObservation, error)
		applied bool
		reverts bool
	}{
		"receipt": {func(r *platformFakeRaiseRunner, i platformSetDataQuotaIntent) (platformSetDataQuotaObservation, error) {
			return r.receipt(i), nil
		}, true, false},
		"failed status": {func(r *platformFakeRaiseRunner, i platformSetDataQuotaIntent) (platformSetDataQuotaObservation, error) {
			return r.status(i, "failed", "quota_raise_lower"), nil
		}, false, true},
		"pending": {func(r *platformFakeRaiseRunner, i platformSetDataQuotaIntent) (platformSetDataQuotaObservation, error) {
			return r.status(i, "pending", ""), nil
		}, false, false},
		"absent in time": {func(r *platformFakeRaiseRunner, i platformSetDataQuotaIntent) (platformSetDataQuotaObservation, error) {
			return r.status(i, "absent", ""), nil
		}, false, false},
	} {
		t.Run(name, func(t *testing.T) {
			s, lease, endpoint, raise, _ := platformUpdateRaiseFixture(t, pg, false)
			namespace := platformNamespaceOf(t, s, lease.installationID)
			raise.execute = func(platformSetDataQuotaIntent) (platformSetDataQuotaObservation, error) {
				return nil, errors.New("connection reset")
			}
			raise.read = func(platformSetDataQuotaIntent) (platformSetDataQuotaObservation, error) {
				return nil, errors.New("read failed too")
			}
			if _, err := s.runPlatformManagedUpdate(context.Background(), lease, endpoint); err == nil || errors.Is(err, errPlatformMigrationEnded) {
				t.Fatalf("a raise with no answer is neither a verdict nor an end: %v", err)
			}
			// The scheduler's attempts run out: the update ends for review with the raise unknown.
			current := platformLeaseOf(t, s, lease.id)
			if err := s.finishPlatformUpdateForReview(context.Background(), current); err != nil {
				t.Fatal(err)
			}
			if operationStage(t, s, lease.id) != "recovery_required" || effectState(t, s, lease.id, "migrate", platformUpdateRaiseEffectNumber) != "unknown" ||
				scalar[string](t, s.st, `SELECT error_code FROM app_platform_operations WHERE id=?`, lease.id) != "update_recovery_required" {
				t.Fatal("an unknown raise did not end the update recovery_required with the row unknown")
			}
			platformRequireRestoreCandidate(t, s, lease, false)
			if _, err := s.st.exec(`UPDATE app_platform_operation_effects SET receipt_json=? WHERE operation_id=? AND stage='migrate' AND effect_number=?`,
				platformImportPendingReceipt(time.Now().UnixMilli()+int64(time.Hour/time.Millisecond)), lease.id, platformUpdateRaiseEffectNumber); err != nil {
				t.Fatal(err)
			}
			raise.read = func(i platformSetDataQuotaIntent) (platformSetDataQuotaObservation, error) { return c.read(raise, i) }
			progressed, err := s.settlePlatformQuotaRaises(context.Background(), endpoint)
			if err != nil {
				t.Fatal(err)
			}
			state := effectState(t, s, lease.id, "migrate", platformUpdateRaiseEffectNumber)
			switch {
			case c.applied:
				if progressed != 1 || state != "applied" || scalar[int64](t, s.st, `SELECT quota_bytes FROM app_platform_data_namespaces WHERE id=?`, namespace) != platformRaisedBytes {
					t.Fatal("a receipt did not apply the raise", progressed, state)
				}
				if _, _, chain, err := platformCurrentNamespaceQuota(s.st.poolExec(), lease.installationID, namespace); err != nil || len(chain) != 2 {
					t.Fatal("the converged raise is not a link", chain, err)
				}
				platformRequireRestoreCandidate(t, s, lease, true)
			case c.reverts:
				if progressed != 1 || state != "reverted" || scalar[int64](t, s.st, `SELECT quota_bytes FROM app_platform_data_namespaces WHERE id=?`, namespace) == platformRaisedBytes {
					t.Fatal("a failed status did not revert the raise", progressed, state)
				}
				platformRequireRestoreCandidate(t, s, lease, true)
			default:
				if progressed != 0 || state != "unknown" {
					t.Fatalf("an answer that settles nothing changed the row: %d %s", progressed, state)
				}
			}
			if operationStage(t, s, lease.id) != "recovery_required" {
				t.Fatal("convergence moved the update out of recovery_required")
			}
			if raise.count("execute") != 1 {
				t.Fatalf("convergence sent the command: %v", raise.calls)
			}
		})
	}
}

func TestUpdateRaiseUnknownConvergesSQLite(t *testing.T)   { testUpdateRaiseUnknownConverges(t, false) }
func TestUpdateRaiseUnknownConvergesPostgres(t *testing.T) { testUpdateRaiseUnknownConverges(t, true) }

// platformLeaseOf reads a running operation as the lease its worker holds.
func platformLeaseOf(t *testing.T, s *Server, operation string) platformOperationLease {
	t.Helper()
	facts, err := loadPlatformOperationLease(s.st.poolExec(), operation)
	if err != nil {
		t.Fatal(err)
	}
	return facts.lease
}

// An absent effect is final only after every signed window has passed; an orphaned one is finished by the same command.
func testUpdateRaiseConvergenceAbsentAndOrphaned(t *testing.T, pg bool) {
	s, lease, endpoint, raise, _ := platformUpdateRaiseFixture(t, pg, false)
	namespace := platformNamespaceOf(t, s, lease.installationID)
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
	slot := strconv.Itoa(platformUpdateRaiseEffectNumber)
	setWindow := func(until int64) {
		mustExec(t, s.st, `UPDATE app_platform_operation_effects SET receipt_json=? WHERE operation_id=? AND stage='migrate' AND effect_number=`+slot, platformImportPendingReceipt(until), lease.id)
	}
	// Absent while a signed window is open: wait.
	setWindow(time.Now().UnixMilli() + int64(time.Hour/time.Millisecond))
	raise.read = func(i platformSetDataQuotaIntent) (platformSetDataQuotaObservation, error) {
		return raise.status(i, "absent", ""), nil
	}
	if progressed, err := s.settlePlatformQuotaRaises(context.Background(), endpoint); err != nil || progressed != 0 || effectState(t, s, lease.id, "migrate", platformUpdateRaiseEffectNumber) != "unknown" {
		t.Fatal("an absence inside a signed window was taken as final", progressed, err)
	}
	// Orphaned: the same command is sent again and finishes under the runner's claim.
	raise.read = func(i platformSetDataQuotaIntent) (platformSetDataQuotaObservation, error) {
		return raise.status(i, "orphaned", ""), nil
	}
	var first platformSetDataQuotaIntent
	if len(raise.sent) > 0 {
		first = raise.sent[0]
	}
	raise.execute = func(i platformSetDataQuotaIntent) (platformSetDataQuotaObservation, error) {
		if first.effectID != "" && (i.effectID != first.effectID || i.command.PayloadSHA256 != first.command.PayloadSHA256) {
			t.Error("the resend is not the same effect")
		}
		return raise.receipt(i), nil
	}
	if progressed, err := s.settlePlatformQuotaRaises(context.Background(), endpoint); err != nil || progressed != 1 || effectState(t, s, lease.id, "migrate", platformUpdateRaiseEffectNumber) != "applied" {
		t.Fatal("an orphaned raise was not finished by sending the same command again", progressed, err)
	}
	if scalar[int64](t, s.st, `SELECT quota_bytes FROM app_platform_data_namespaces WHERE id=?`, namespace) != platformRaisedBytes {
		t.Fatal("the finished raise did not reach the namespace")
	}
	// A converged row is never touched again.
	if progressed, err := s.settlePlatformQuotaRaises(context.Background(), endpoint); err != nil || progressed != 0 {
		t.Fatal("a settled raise was settled again", progressed, err)
	}
}

func TestUpdateRaiseConvergenceAbsentAndOrphanedSQLite(t *testing.T) {
	testUpdateRaiseConvergenceAbsentAndOrphaned(t, false)
}
func TestUpdateRaiseConvergenceAbsentAndOrphanedPostgres(t *testing.T) {
	testUpdateRaiseConvergenceAbsentAndOrphaned(t, true)
}

func testUpdateRaiseConvergenceAbsentAfterTheWindow(t *testing.T, pg bool) {
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
	mustExec(t, s.st, `UPDATE app_platform_operation_effects SET receipt_json=? WHERE operation_id=? AND stage='migrate' AND effect_number=?`,
		platformImportPendingReceipt(time.Now().UnixMilli()-int64(time.Minute/time.Millisecond)), lease.id, platformUpdateRaiseEffectNumber)
	raise.read = func(i platformSetDataQuotaIntent) (platformSetDataQuotaObservation, error) {
		return raise.status(i, "absent", ""), nil
	}
	if progressed, err := s.settlePlatformQuotaRaises(context.Background(), endpoint); err != nil || progressed != 1 ||
		effectState(t, s, lease.id, "migrate", platformUpdateRaiseEffectNumber) != "reverted" {
		t.Fatal("a final absence did not revert the raise", progressed, err)
	}
	var receipt string
	if err := s.st.queryRow(`SELECT receipt_json FROM app_platform_operation_effects WHERE operation_id=? AND stage='migrate' AND effect_number=?`, lease.id, platformUpdateRaiseEffectNumber).Scan(&receipt); err != nil ||
		receipt != platformRaiseAbsentReceipt {
		t.Fatal("the absence was not kept", receipt, err)
	}
	platformRequireRestoreCandidate(t, s, lease, true)
}

func TestUpdateRaiseConvergenceAbsentAfterTheWindowSQLite(t *testing.T) {
	testUpdateRaiseConvergenceAbsentAfterTheWindow(t, false)
}
func TestUpdateRaiseConvergenceAbsentAfterTheWindowPostgres(t *testing.T) {
	testUpdateRaiseConvergenceAbsentAfterTheWindow(t, true)
}

// Test 11: a lost acknowledgement after the runner applied is read back and committed exactly once; the stage does
// not send again.
func testUpdateRaiseLostAckCommitsOnce(t *testing.T, pg bool) {
	s, lease, endpoint, raise, _ := platformUpdateRaiseFixture(t, pg, false)
	revision := scalar[int64](t, s.st, `SELECT revision FROM app_platform_data_namespaces WHERE owner_installation_id=?`, lease.installationID)
	audited := scalar[int](t, s.st, `SELECT COUNT(*) FROM audit_log WHERE action='app_platform.data_quota_raised' AND target_id=?`, lease.id)
	raise.execute = func(platformSetDataQuotaIntent) (platformSetDataQuotaObservation, error) {
		return nil, errors.New("connection reset after the raise")
	}
	raise.read = func(i platformSetDataQuotaIntent) (platformSetDataQuotaObservation, error) {
		return raise.receipt(i), nil
	}
	next, err := s.runPlatformManagedUpdate(context.Background(), lease, endpoint)
	if err != nil || next.snapshot.Stage != appplatform.StageStart || raise.count("execute") != 1 || raise.count("read") != 1 {
		t.Fatalf("read-back did not commit: %v %v %v", next.snapshot, err, raise.calls)
	}
	if scalar[int](t, s.st, `SELECT COUNT(*) FROM audit_log WHERE action='app_platform.data_quota_raised' AND target_id=?`, lease.id) != audited+1 ||
		scalar[int64](t, s.st, `SELECT revision FROM app_platform_data_namespaces WHERE owner_installation_id=?`, lease.installationID) != revision+1 {
		t.Fatal("the raise was committed more than once", revision, scalar[int64](t, s.st, `SELECT revision FROM app_platform_data_namespaces WHERE owner_installation_id=?`, lease.installationID),
			scalar[int](t, s.st, `SELECT COUNT(*) FROM audit_log WHERE action='app_platform.data_quota_raised' AND target_id=?`, lease.id))
	}
	// A later dispatch of the stage sends nothing: the row is applied and the operation is past the stage.
	if _, err := s.dispatchPlatformUpdateRaise(context.Background(), lease, endpoint); err == nil {
		t.Fatal("a raise was dispatched again over an applied row")
	}
	if raise.count("execute") != 1 {
		t.Fatal("an applied raise was sent again")
	}
}

func TestUpdateRaiseLostAckCommitsOnceSQLite(t *testing.T) {
	testUpdateRaiseLostAckCommitsOnce(t, false)
}
func TestUpdateRaiseLostAckCommitsOncePostgres(t *testing.T) {
	testUpdateRaiseLostAckCommitsOnce(t, true)
}

// Test 12: a lease reclaimed mid-raise reuses the effect id and digest; a stored row that was changed is refused.
func testUpdateRaiseLeaseReclaim(t *testing.T, pg bool) {
	s, lease, endpoint, raise, _ := platformUpdateRaiseFixture(t, pg, false)
	raise.execute = func(platformSetDataQuotaIntent) (platformSetDataQuotaObservation, error) {
		return nil, errors.New("lost")
	}
	raise.read = func(platformSetDataQuotaIntent) (platformSetDataQuotaObservation, error) {
		return nil, errors.New("lost")
	}
	for range 2 {
		if _, err := s.runPlatformManagedUpdate(context.Background(), lease, endpoint); err == nil {
			t.Fatal("an unanswered raise succeeded")
		}
	}
	if len(raise.sent) != 2 || raise.sent[0].effectID != raise.sent[1].effectID || raise.sent[0].command.PayloadSHA256 != raise.sent[1].command.PayloadSHA256 ||
		scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_operation_effects WHERE operation_id=? AND kind='set-data-quota'`, lease.id) != 1 {
		t.Fatal("a retry did not reuse the effect")
	}
	firstExpires, _ := strconv.ParseInt(raise.sent[0].command.ExpiresMS, 10, 64)
	if raise.sent[1].priorSignedUntil < firstExpires {
		t.Fatal("the second signing does not know the first window")
	}
	// A stored row that was changed is refused, whichever of its identity fields moved.
	slot := strconv.Itoa(platformUpdateRaiseEffectNumber)
	var digest, kind string
	var fence int64
	if err := s.st.queryRow(`SELECT payload_sha256,kind,admitted_fence FROM app_platform_operation_effects WHERE operation_id=? AND stage='migrate' AND effect_number=`+slot, lease.id).Scan(&digest, &kind, &fence); err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string][2]string{
		"digest": {`payload_sha256='` + strings.Repeat("b", 64) + `'`, `payload_sha256='` + digest + `'`},
		"fence":  {`admitted_fence=admitted_fence+1`, `admitted_fence=` + strconv.FormatInt(fence, 10)},
		"kind":   {`kind='migrate-data'`, `kind='` + kind + `'`},
	} {
		mustExec(t, s.st, `UPDATE app_platform_operation_effects SET `+change[0]+` WHERE operation_id=? AND stage='migrate' AND effect_number=`+slot, lease.id)
		if _, err := s.preparePlatformUpdateRaise(context.Background(), lease); !errors.Is(err, errAppPlatformStageEvidence) {
			t.Fatalf("a row with a changed %s was accepted: %v", name, err)
		}
		mustExec(t, s.st, `UPDATE app_platform_operation_effects SET `+change[1]+` WHERE operation_id=? AND stage='migrate' AND effect_number=`+slot, lease.id)
	}
	if _, err := s.preparePlatformUpdateRaise(context.Background(), lease); err != nil {
		t.Fatalf("the restored row was refused: %v", err)
	}
}

func TestUpdateRaiseLeaseReclaimSQLite(t *testing.T)   { testUpdateRaiseLeaseReclaim(t, false) }
func TestUpdateRaiseLeaseReclaimPostgres(t *testing.T) { testUpdateRaiseLeaseReclaim(t, true) }

// An update after an enable (or an earlier update) has a source whose fence is past the one the volume row was
// prepared with, and the runner refuses a raise above the volume's. The raise names the prepare-data receipt and
// carries that receipt's fence.
func TestUpdateRaiseFactsCarryTheVolumeFenceNotTheSourceFenceSQLite(t *testing.T) {
	t.Parallel()
	s, _, enabled, _, runtime := platformDataActionFixture(t, false)
	now, err := platformDatabaseNow(s.st.poolExec())
	if err != nil {
		t.Fatal(err)
	}
	probe := platformOperationLease{installationID: enabled.installationID, controllerEpoch: runtime.controllerEpoch, snapshot: appplatform.OperationSnapshot{
		Action: appplatform.ActionUpdate, Managed: true, Generation: 3, Revision: 1, Stage: appplatform.StageQueued}}
	source, err := loadPlatformCheckpointSourceMode(s.st.poolExec(), probe, runtime, now, false, 0)
	if err != nil {
		t.Fatal("update source after an enable", err)
	}
	receipt, err := journal.ParseDataPreparationReceipt([]byte(scalar[string](t, s.st, `SELECT receipt_json FROM app_platform_operation_effects WHERE effect_id=?`, source.dataEffectID)))
	if err != nil {
		t.Fatal(err)
	}
	if source.writerFence == receipt.WriterFence {
		t.Fatalf("the fixture's source fence %s is not past the volume's %s: the case is not reached", source.writerFence, receipt.WriterFence)
	}
	plan := platformManagedUpdatePlan{
		DataNamespaceID: source.namespaceID, SourceReleaseSHA256: source.releaseSHA256, DataFormat: mustPlatformCheckpointInt(source.dataFormat),
		SourceWriterFence: mustPlatformCheckpointInt(source.writerFence),
		DataQuota:         &platformUpdateQuotaReview{RaiseApplied: true, CurrentBytes: 1, CurrentInodes: 1, NewBytes: 2, NewInodes: 2},
	}
	checkpoint := journal.CheckpointReceipt{
		NamespaceID: source.namespaceID, ReleaseSHA256: source.releaseSHA256, SourceDataEffectID: source.dataEffectID,
		SourceDataReceiptSHA256: source.sourceDataReceiptSHA256, ProjectID: source.projectID, NamespaceInode: source.namespaceInode, Device: source.device,
	}
	facts, err := platformUpdateRaiseFacts(s.st.poolExec(), plan, checkpoint)
	if err != nil || strconv.FormatInt(facts.writerFence, 10) != receipt.WriterFence {
		t.Fatalf("the raise carries fence %d, not the volume's %s (%v)", facts.writerFence, receipt.WriterFence, err)
	}
	checkpoint.SourceDataReceiptSHA256 = strings.Repeat("a", 64)
	if _, err := platformUpdateRaiseFacts(s.st.poolExec(), plan, checkpoint); err == nil {
		t.Fatal("a raise over a receipt that does not match its digest was built")
	}
}
