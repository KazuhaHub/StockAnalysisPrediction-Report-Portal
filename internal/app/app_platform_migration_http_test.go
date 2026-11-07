package app

import (
	"context"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/journal"
)

// settleOrphaned settles C3: the runner was killed after the job exited and before it acknowledged, so the
// migration is orphaned, discarded, and the checkpoint restored.
func settleOrphaned(t *testing.T, pg bool) *settleFixture {
	t.Helper()
	f := settleAfterEnd(t, pg, orphanedScript)
	f.fake.readMigrate = func(i platformMigrateIntent) (platformMigrateObservation, error) {
		return platformMigrateStatusAnswer(i, f.endpoint, "orphaned"), nil
	}
	f.fake.readDiscard = func(platformDiscardIntent) (platformDiscardObservation, error) {
		return nil, errPlatformMigrationAbsent
	}
	f.fake.executeDiscard = func(i platformDiscardIntent) (platformDiscardObservation, error) {
		o := platformFakeProof(i.authority, f.endpoint, i.raw)
		c := i.command
		now := strconv.FormatInt(time.Now().UTC().UnixMilli(), 10)
		o.discard = &journal.DiscardMigrationReceipt{
			Format: "portal.runner.discard-migration-receipt/1-dev.1", DeploymentID: c.DeploymentID, ControllerEpoch: c.ControllerEpoch,
			InstallationID: c.InstallationID, OperationID: c.OperationID, TargetGeneration: c.TargetGeneration, Stage: "migrate", EffectID: c.EffectID,
			PayloadSHA256: c.PayloadSHA256, NamespaceID: c.Payload.NamespaceID, ResourceID: c.Payload.ResourceID, MigrateEffectID: c.Payload.MigrateEffectID,
			MigratePayloadSHA256: c.Payload.MigratePayloadSHA256, MigrateReceiptSHA256: platformDigest([]byte("migrate-receipt")), State: "discarded",
			ObservedMS: now, SettledMS: now,
		}
		return o, nil
	}
	f.fake.executeRestore = func(i platformRestoreIntent) (platformSettlingRestoreObservation, error) {
		return restoreAnswer(i, f.endpoint), nil
	}
	for range 4 {
		f.step(t)
	}
	if f.settleState(t) != "settled" {
		t.Fatalf("C3 did not settle: %s %v", f.settleState(t), f.fake.calls)
	}
	return f
}

type migrationHTTP struct {
	platformRecoveryHTTP
	f *settleFixture
}

func newMigrationHTTP(t *testing.T, f *settleFixture) migrationHTTP {
	t.Helper()
	actor := platformManagedFixtureSession(t, f.s)
	mux := http.NewServeMux()
	f.s.wireRoutes(mux)
	return migrationHTTP{platformRecoveryHTTP{t: t, mux: mux, cookie: f.s.managedCookie(*f.s.st.GetUser(actor.username), actor.sessionID, actor.expires.Unix())}, f}
}

func (h migrationHTTP) launch() int {
	return sessionTestRequest(h.mux, "GET", "/api/app-platform/installations/"+h.f.lease.installationID+"/launch", h.cookie, "").Code
}

// Through the HTTP routes an administrator uses, a roll back is refused until the migration settles, and the
// application does not launch meanwhile.
func TestMigrationRollbackRefusedBeforeSettlementHTTP(t *testing.T) {
	platformWritableUpdateFixtures(t)
	f := settleAfterEnd(t, false, orphanedScript)
	h := newMigrationHTTP(t, f)
	const plans = "/api/admin/app-platform/plans"
	set := recoverySetOf(t, f.s, f.op)
	refused := h.post(plans, `{"action":"restore","installation_id":"`+f.lease.installationID+`","recovery_set_id":"`+set+`"}`)
	if refused.Code != 409 {
		t.Fatalf("a roll back before settlement answered %d: %s", refused.Code, refused.Body.String())
	}
	if h.launch() == 200 {
		t.Fatal("an application launched while its migration was unsettled")
	}
}

// rollBackOverHTTP reviews and commits a roll back through the routes an administrator uses and drives it to completion.
func rollBackOverHTTP(t *testing.T, f *settleFixture, h migrationHTTP) {
	t.Helper()
	const plans, operations = "/api/admin/app-platform/plans", "/api/admin/app-platform/operations"
	set := recoverySetOf(t, f.s, f.op)
	review := h.json(h.post(plans, `{"action":"restore","installation_id":"`+f.lease.installationID+`","recovery_set_id":"`+set+`"}`), 200)
	key := platformFixtureID(7400)
	commit := h.json(h.post(operations, `{"plan_id":"`+h.str(review, "plan_id")+`","plan_sha256":"`+h.str(review, "plan_sha256")+`","idempotency_key":"`+key+`"}`), 202)
	selected, err := f.s.claimNextPlatformManagedRestore(context.Background(), platformFixtureID(7401))
	if err != nil || selected.lease.id != h.str(commit, "operation_id") {
		t.Fatal("the roll back was not claimed", selected.lease.id, err)
	}
	lease := selected.lease
	for lease.snapshot.Stage != appplatform.StageStart {
		if lease, err = f.s.runPlatformManagedRestore(context.Background(), lease, f.endpoint); err != nil {
			t.Fatal(err)
		}
	}
	endpoint := platformUpdateStartEndpointFixture()
	s := f.s
	startIntent, err := s.preparePlatformRestoreStart(context.Background(), lease)
	if err != nil {
		t.Fatal(err)
	}
	reservation := platformUpdateStartReservationFixture(startIntent, endpoint).reservation
	reserved, err := s.commitPlatformRestoreStartReservation(context.Background(), lease, startIntent, endpoint, reservation)
	if err != nil {
		t.Fatal(err)
	}
	probeLease, err := s.commitPlatformRestoreStartRunning(context.Background(), reserved.lease, reserved.intent, endpoint, platformUpdateStartRunningFixture(reserved.intent, reservation, endpoint))
	if err != nil {
		t.Fatal(err)
	}
	probeIntent, err := s.preparePlatformRestoreProbe(context.Background(), probeLease)
	if err != nil {
		t.Fatal(err)
	}
	readiness := platformUpdateStartReservationFixture(probeIntent, endpoint)
	readiness.readiness = platformReadinessFor(probeIntent)
	activateLease, err := s.commitPlatformRestoreStartReadiness(context.Background(), probeLease, probeIntent, endpoint, readiness)
	if err != nil {
		t.Fatal(err)
	}
	registrationIntent, err := s.preparePlatformRestoreStartRegistration(context.Background(), activateLease)
	if err != nil {
		t.Fatal(err)
	}
	registered, err := s.commitPlatformRestoreStartRegistration(context.Background(), activateLease, registrationIntent, endpoint,
		platformRestoreStartRegistrationObservationFixture(t, registrationIntent, endpoint))
	if err != nil {
		t.Fatal(err)
	}
	applied, boot := platformRestoreStartBootFixture(t, s, registered, endpoint)
	defer applied.registration.Close()
	if _, err := s.commitPlatformRestoreStartActivation(context.Background(), registered, applied, boot); err != nil {
		t.Fatal(err)
	}
}

// C3 -> settle -> roll back, through the HTTP routes: the roll back is offered after settlement, takes its
// restore on the settlement's evidence, and leaves nothing unresolved.
func TestMigrationC3RollbackAfterSettlementHTTP(t *testing.T) {
	platformWritableUpdateFixtures(t)
	f := settleOrphaned(t, false)
	h := newMigrationHTTP(t, f)
	rollBackOverHTTP(t, f, h)
	s := f.s
	if n, err := platformUnresolvedOperationCount(s.st.poolExec(), f.lease.installationID); err != nil || n != 0 {
		t.Fatalf("the roll back left %d unresolved operations: %v", n, err)
	}
	if n, err := platformUnresolvedOperationEffectCount(s.st.poolExec(), f.lease.installationID); err != nil || n != 0 {
		t.Fatalf("the roll back left %d unsafe effects: %v", n, err)
	}
	if got := h.launch(); got != 200 {
		t.Fatalf("launch after the roll back answered %d", got)
	}
	if scalar[string](t, s.st, `SELECT phase FROM app_platform_installations WHERE id=?`, f.lease.installationID) != "ready" {
		t.Fatal("the installation is not ready after the roll back")
	}
	if settings, err := s.platformInstallationSettings(platformManagedFixtureSession(t, s), f.lease.installationID); err != nil || settings.MigrationRecovery != nil {
		t.Fatalf("a rolled back installation still offers choices: %v", err)
	}
}

// A rolled back installation launches only on the evidence its roll back recorded: a start effect whose payload no
// longer matches the command rebuilt from the checkpoint is not served.
func TestMigrationRollbackLaunchRefusesAlteredStartEvidenceHTTP(t *testing.T) {
	platformWritableUpdateFixtures(t)
	f := settleOrphaned(t, false)
	h := newMigrationHTTP(t, f)
	rollBackOverHTTP(t, f, h)
	if got := h.launch(); got != 200 {
		t.Fatalf("launch after the roll back answered %d", got)
	}
	if _, err := f.s.st.db.Exec(f.s.st.bind(`UPDATE app_platform_operation_effects SET payload_sha256=? WHERE kind='restore-start'`), platformDigest([]byte("altered"))); err != nil {
		t.Fatal(err)
	}
	if got := h.launch(); got == 200 {
		t.Fatal("a roll back with altered start evidence launched")
	}
}

// C1 -> settle -> retry -> launch. The retry is admitted behind a review of its own and takes its removal
// evidence from the failed update.
func TestMigrationC1SettleRetryLaunchHTTP(t *testing.T) {
	platformWritableUpdateFixtures(t)
	f := settledFixture(t, false)
	h := newMigrationHTTP(t, f)
	fx := lastPlatformUpdateFixture
	// The review reads the runner's profile, which has no peer in a test; its precondition is the same one
	// the admitted retry is held to.
	if err := requirePlatformRetrySource(f.s.st.poolExec(), f.lease.installationID, f.op); err != nil {
		t.Fatal(err)
	}
	lease := retryToMigrate(t, f, fx, platformFixtureID(7510), platformFixtureID(7511), f.op)
	f.fake.executeMigrate = func(i platformMigrateIntent) (platformMigrateObservation, error) {
		return platformMigrateAnswers(i, f.endpoint, "migrated", ""), nil
	}
	start, err := f.s.dispatchPlatformMigrateData(context.Background(), lease, f.endpoint)
	if err != nil {
		t.Fatal(err)
	}
	activate, registration, observation, _ := platformMigratedUpdateToRegistration(t, f.s, start, f.endpoint)
	registered, err := f.s.commitPlatformUpdateStartRegistration(context.Background(), activate, registration, f.endpoint, observation)
	if err != nil {
		t.Fatal(err)
	}
	applied, boot := platformUpdateStartBootFixture(t, f.s, registered, f.endpoint)
	defer applied.registration.Close()
	if _, err := f.s.commitPlatformUpdateStartActivation(context.Background(), registered, applied, boot); err != nil {
		t.Fatal(err)
	}
	if got := h.launch(); got != 200 {
		t.Fatalf("launch after the retry answered %d", got)
	}
}

// C7: once the migration has been discarded or has failed, no start can be built on it. The start binds an
// applied migration receipt, and neither a reverted migration nor an absent one is that.
func TestMigrationC7StartAfterDiscardRefused(t *testing.T) {
	f := settleOrphaned(t, false)
	var plan platformManagedUpdatePlan
	raw := scalar[string](t, f.s.st, `SELECT p.plan_json FROM app_platform_operations o JOIN app_platform_review_plans p ON p.id=o.review_plan_id WHERE o.id=?`, f.op)
	if err := jsonUnmarshalPlan(raw, &plan); err != nil {
		t.Fatal(err)
	}
	if got, err := loadPlatformAppliedMigration(f.s.st.poolExec(), f.op, plan); err == nil || got != nil {
		t.Fatalf("a discarded migration was accepted as evidence for a start: %v", err)
	}
	if effectState(t, f.s, f.op, "migrate", 2) != "reverted" {
		t.Fatal("the discarded migration is not reverted")
	}
}
