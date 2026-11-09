package app

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/transport"
)

// platformFakeManagedRuntime is the observation a runner reports for its own profile: the one thing a retry's
// review and commit read from the runner. Everything else in those routes is the Portal's own code.
type platformFakeManagedRuntime struct {
	profile   transport.ManagedRuntimeProfile
	authority control.Authority
	endpoint  transport.CandidateReceiptEndpoint
	at        time.Time
}

func (o platformFakeManagedRuntime) Profile() transport.ManagedRuntimeProfile { return o.profile }
func (o platformFakeManagedRuntime) Controller() control.Authority            { return o.authority }
func (o platformFakeManagedRuntime) Endpoint() transport.CandidateReceiptEndpoint {
	return o.endpoint
}
func (o platformFakeManagedRuntime) ReceivedAt() time.Time { return o.at }

// reportManagedRuntime makes the runner answer review and commit with the profile of the installed runtime.
func reportManagedRuntime(f *settleFixture, profile transport.ManagedRuntimeProfile) {
	f.s.platformBackendEndpoint.Store(&f.endpoint)
	f.s.platformReadManagedRuntime = func(_ context.Context, e platformRuntimeEndpoint, a control.Authority) (platformManagedRuntimeObserved, error) {
		return platformFakeManagedRuntime{
			profile: profile, authority: a, at: time.Now(),
			endpoint: transport.CandidateReceiptEndpoint{Path: e.path, ControllerUID: e.controllerUID, ControllerGID: e.controllerGID, RootController: true},
		}, nil
	}
}

func (h migrationHTTP) get(path string) map[string]any {
	h.t.Helper()
	return h.json(sessionTestRequest(h.mux, "GET", path, h.cookie, ""), http.StatusOK)
}

// retryChoice reads the retry the settings route offers for the installation.
func (h migrationHTTP) retryChoice() map[string]any {
	h.t.Helper()
	settings := h.get("/api/admin/app-platform/installations/" + h.f.lease.installationID + "/settings")
	recovery, _ := settings["migration_recovery"].(map[string]any)
	choices, _ := recovery["choices"].([]any)
	for _, choice := range choices {
		if c, _ := choice.(map[string]any); c["kind"] == "retry" {
			return c
		}
	}
	h.t.Fatalf("the settings route offers no retry: %v", settings)
	return nil
}

// retryOverHTTP reviews and commits the retry the settings route offers, through the routes an administrator
// uses, and returns the admitted operation.
func (h migrationHTTP) retryOverHTTP(key string) string {
	h.t.Helper()
	choice := h.retryChoice()
	review := h.json(h.post("/api/admin/app-platform/plans", `{"action":"update","installation_id":"`+h.f.lease.installationID+
		`","release_sha256":"`+h.str(choice, "release_sha256")+`","trust_revision":"`+h.str(choice, "trust_revision")+
		`","recovers_operation_id":"`+h.str(choice, "recovers_operation_id")+`"}`), http.StatusOK)
	if reviewed, _ := review["review"].(map[string]any); reviewed["recovers_operation_id"] != choice["recovers_operation_id"] {
		h.t.Fatalf("the review does not name the update it recovers: %v", review)
	}
	commit := h.json(h.post("/api/admin/app-platform/operations", `{"plan_id":"`+h.str(review, "plan_id")+`","plan_sha256":"`+h.str(review, "plan_sha256")+
		`","idempotency_key":"`+key+`"}`), http.StatusAccepted)
	return h.str(commit, "operation_id")
}

// activateMigrated sends the retry's migration, then starts and activates its target. It returns the payloads the
// migration was sent with.
func (h migrationHTTP) activateMigrated(t *testing.T, lease platformOperationLease, state string) []control.MigrateDataPayload {
	t.Helper()
	f := h.f
	var sent []control.MigrateDataPayload
	f.fake.executeMigrate = func(i platformMigrateIntent) (platformMigrateObservation, error) {
		sent = append(sent, i.command.Payload)
		return platformMigrateAnswers(i, f.endpoint, state, map[string]string{"migrated": "", "failed": "exit_nonzero"}[state]), nil
	}
	start, err := f.s.dispatchPlatformMigrateData(context.Background(), lease, f.endpoint)
	if state != "migrated" {
		if !errors.Is(err, errPlatformMigrationEnded) {
			t.Fatal(err)
		}
		return sent
	}
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
	return sent
}

// C1 -> settle -> retry, all through the routes an administrator uses: the settings route offers the retry, its
// review and commit are HTTP calls, and once the retry's migration, start and activation have run the application
// launches. No operation is written by the test.
func TestMigrationC1RetryReviewCommitLaunchHTTP(t *testing.T) {
	platformWritableUpdateFixtures(t)
	f := settledFixture(t, false)
	h := newMigrationHTTP(t, f)
	reportManagedRuntime(f, lastPlatformUpdateFixture.target.Profile)
	if h.launch() == 200 {
		t.Fatal("an application launched while its failed update was unresolved")
	}
	operation := h.retryOverHTTP(platformFixtureID(7600))
	lease := driveRetryToMigrate(t, f, operation)
	h.activateMigrated(t, lease, "migrated")
	if got := h.launch(); got != 200 {
		t.Fatalf("launch after the retry answered %d", got)
	}
	if scalar[string](t, f.s.st, `SELECT phase FROM app_platform_installations WHERE id=?`, f.lease.installationID) != "ready" {
		t.Fatal("the installation is not ready after the retry")
	}
	if settings, err := f.s.platformInstallationSettings(platformManagedFixtureSession(t, f.s), f.lease.installationID); err != nil || settings.MigrationRecovery != nil {
		t.Fatalf("a recovered installation still offers choices: %v", err)
	}
}

// A retry that fails in turn is settled and retried again, still through the routes: the second retry names the
// first, and its completion leaves the application launching.
func TestMigrationC1RetryOfRetryReviewCommitLaunchHTTP(t *testing.T) {
	platformWritableUpdateFixtures(t)
	f := settledFixture(t, false)
	h := newMigrationHTTP(t, f)
	reportManagedRuntime(f, lastPlatformUpdateFixture.target.Profile)
	first := h.retryOverHTTP(platformFixtureID(7610))
	lease := driveRetryToMigrate(t, f, first)
	h.activateMigrated(t, lease, "failed")
	f.fake.executeRestore = func(i platformRestoreIntent) (platformSettlingRestoreObservation, error) {
		return restoreAnswer(i, f.endpoint), nil
	}
	settled := &settleFixture{s: f.s, lease: f.lease, endpoint: f.endpoint, fake: f.fake, op: first}
	settled.step(t)
	settled.step(t)
	if settled.settleState(t) != "settled" {
		t.Fatal("the failed retry did not settle", settled.settleState(t))
	}
	if h.launch() == 200 {
		t.Fatal("an application launched while its failed retry was unresolved")
	}
	choice := h.retryChoice()
	if choice["recovers_operation_id"] != first {
		t.Fatalf("the second retry does not name the first: %v", choice)
	}
	second := h.retryOverHTTP(platformFixtureID(7620))
	lease = driveRetryToMigrate(t, f, second)
	h.activateMigrated(t, lease, "migrated")
	if got := h.launch(); got != 200 {
		t.Fatalf("launch after the retry of a retry answered %d", got)
	}
	for _, failed := range []string{f.op, first} {
		if effectState(t, f.s, failed, "recovered", 1) != "applied" {
			t.Fatalf("%s was left unresolved", failed)
		}
	}
}

// C5 -> settle -> retry -> launch, through the routes: readiness failed on migrated data, so the runner's
// migrate-data stays applied while settlement removes the target and restores the checkpoint. The retry that
// follows must still be admitted by the runner's rule (checked in its journal) and the portal must carry it.
func TestMigrationC5RetryReviewCommitLaunchHTTP(t *testing.T) {
	platformWritableUpdateFixtures(t)
	f := readinessFailedFixture(t, false)
	f.fake.removeTarget = func(i platformUpdateTargetRemovalIntent) (platformUpdateTargetRemovalObservation, error) {
		state := "stopped"
		if i.command.Stage == "remove_code" {
			state = "absent"
		}
		return platformUpdateTargetRemovalObservationFixture(i, f.endpoint, state), nil
	}
	f.fake.executeRestore = func(i platformRestoreIntent) (platformSettlingRestoreObservation, error) {
		return restoreAnswer(i, f.endpoint), nil
	}
	for range 5 {
		f.step(t)
	}
	if f.settleState(t) != "settled" || effectState(t, f.s, f.op, "migrate", 2) != "applied" {
		t.Fatalf("readiness failure did not settle with an applied migration: %s", f.settleState(t))
	}
	f.fake.calls = nil
	h := newMigrationHTTP(t, f)
	reportManagedRuntime(f, lastPlatformUpdateFixture.target.Profile)
	if h.launch() == 200 {
		t.Fatal("an application launched while its failed update was unresolved")
	}
	operation := h.retryOverHTTP(platformFixtureID(7630))
	lease := driveRetryToMigrate(t, f, operation)
	sent := h.activateMigrated(t, lease, "migrated")
	if len(sent) != 1 || sent[0].RecoversChain != f.op {
		t.Fatalf("the retry's migration does not name the update it recovers: %v", sent)
	}
	if got := h.launch(); got != 200 {
		t.Fatalf("launch after the retry answered %d", got)
	}
}

// settleNeverClaimed ends the leased migrate stage with its command lost in transit, then settles the update: the
// absence read back becomes definite once the signed window has passed, and nothing is restored.
func settleNeverClaimed(t *testing.T, f *settleFixture, lease platformOperationLease) {
	t.Helper()
	f.fake.executeMigrate = func(platformMigrateIntent) (platformMigrateObservation, error) {
		return nil, errors.New("lost in transit")
	}
	f.fake.readMigrate = func(platformMigrateIntent) (platformMigrateObservation, error) {
		return nil, errPlatformMigrationAbsent
	}
	if _, err := f.s.dispatchPlatformMigrateData(context.Background(), lease, f.endpoint); !errors.Is(err, errPlatformMigrationBusy) {
		t.Fatal(err)
	}
	if err := f.s.finishPlatformManagedUpdateForReview(context.Background(), lease); err != nil {
		t.Fatal(err)
	}
	f.fake.calls = nil
	past := strconv.FormatInt(time.Now().Add(-time.Minute).UnixMilli(), 10)
	mustExec(t, f.s.st, `UPDATE app_platform_operation_effects SET receipt_json=? WHERE operation_id=? AND stage='migrate' AND effect_number=2`,
		`{"signed_until_ms":`+past+`}`, lease.id)
	settled := &settleFixture{s: f.s, lease: f.lease, endpoint: f.endpoint, fake: f.fake, op: lease.id}
	settled.step(t)
	settled.step(t)
	if settled.settleState(t) != "settled" || f.fake.count("execute-restore") != 0 {
		t.Fatalf("a never-claimed migration did not settle without a restore: %s %v", settled.settleState(t), f.fake.calls)
	}
}

// The migration never reached the runner. Settlement restores nothing, and the retry offered afterwards goes through
// review, commit, migration, start and activation over the routes to a launching application.
func TestMigrationNeverClaimedRetryReviewCommitLaunchHTTP(t *testing.T) {
	platformWritableUpdateFixtures(t)
	s, lease, endpoint, fake := platformMigrationStageFixture(t, false, true)
	f := &settleFixture{s: s, lease: lease, endpoint: endpoint, fake: fake, op: lease.id}
	settleNeverClaimed(t, f, lease)
	h := newMigrationHTTP(t, f)
	reportManagedRuntime(f, lastPlatformUpdateFixture.target.Profile)
	if h.launch() == 200 {
		t.Fatal("an application launched while its failed update was unresolved")
	}
	operation := h.retryOverHTTP(platformFixtureID(7640))
	retry := driveRetryToMigrate(t, f, operation)
	sent := h.activateMigrated(t, retry, "migrated")
	if len(sent) != 1 || sent[0].RecoversChain != f.op {
		t.Fatalf("the retry's migration does not name the update it recovers: %+v", sent)
	}
	if got := h.launch(); got != 200 {
		t.Fatalf("launch after the retry answered %d", got)
	}
}

// The first retry's migration is lost as well. The second retry names the first, and the application launches.
func TestMigrationNeverClaimedRetryOfRetryReviewCommitLaunchHTTP(t *testing.T) {
	platformWritableUpdateFixtures(t)
	s, lease, endpoint, fake := platformMigrationStageFixture(t, false, true)
	f := &settleFixture{s: s, lease: lease, endpoint: endpoint, fake: fake, op: lease.id}
	settleNeverClaimed(t, f, lease)
	h := newMigrationHTTP(t, f)
	reportManagedRuntime(f, lastPlatformUpdateFixture.target.Profile)
	first := h.retryOverHTTP(platformFixtureID(7650))
	retry := driveRetryToMigrate(t, f, first)
	settleNeverClaimed(t, f, retry)
	if h.launch() == 200 {
		t.Fatal("an application launched while its failed retry was unresolved")
	}
	if choice := h.retryChoice(); choice["recovers_operation_id"] != first {
		t.Fatalf("the second retry does not name the first: %v", choice)
	}
	second := h.retryOverHTTP(platformFixtureID(7660))
	lease2 := driveRetryToMigrate(t, f, second)
	sent := h.activateMigrated(t, lease2, "migrated")
	if len(sent) != 1 || sent[0].RecoversChain != f.op+","+first {
		t.Fatalf("the second retry's migration does not name the first: %+v", sent)
	}
	if got := h.launch(); got != 200 {
		t.Fatalf("launch after the retry of a retry answered %d", got)
	}
	for _, failed := range []string{f.op, first} {
		if effectState(t, f.s, failed, "recovered", 1) != "applied" {
			t.Fatalf("%s was left unresolved", failed)
		}
	}
}

// c5RetryAtStart brings a retry of a readiness failure to the point where its migration is applied and its start is
// about to be prepared, which is where it takes over the candidate activation the failed update left.
func c5RetryAtStart(t *testing.T) (*settleFixture, platformOperationLease) {
	t.Helper()
	platformWritableUpdateFixtures(t)
	f := readinessFailedFixture(t, false)
	f.fake.removeTarget = func(i platformUpdateTargetRemovalIntent) (platformUpdateTargetRemovalObservation, error) {
		state := "stopped"
		if i.command.Stage == "remove_code" {
			state = "absent"
		}
		return platformUpdateTargetRemovalObservationFixture(i, f.endpoint, state), nil
	}
	f.fake.executeRestore = func(i platformRestoreIntent) (platformSettlingRestoreObservation, error) {
		return restoreAnswer(i, f.endpoint), nil
	}
	for range 5 {
		f.step(t)
	}
	if f.settleState(t) != "settled" {
		t.Fatal("readiness failure did not settle")
	}
	f.fake.calls = nil
	h := newMigrationHTTP(t, f)
	reportManagedRuntime(f, lastPlatformUpdateFixture.target.Profile)
	lease := driveRetryToMigrate(t, f, h.retryOverHTTP(platformFixtureID(7670)))
	f.fake.executeMigrate = func(i platformMigrateIntent) (platformMigrateObservation, error) {
		return platformMigrateAnswers(i, f.endpoint, "migrated", ""), nil
	}
	start, err := f.s.dispatchPlatformMigrateData(context.Background(), lease, f.endpoint)
	if err != nil {
		t.Fatal(err)
	}
	return f, start
}

// The retry takes over the failed update's candidate activation only when settlement shows that update removed the
// target the row stands for. Each thing the evidence must show, withheld, refuses the start.
func TestRetryTakesOverTheCandidateOnlyWithSettlementEvidence(t *testing.T) {
	f, start := c5RetryAtStart(t)
	if _, err := f.s.preparePlatformUpdateStart(context.Background(), start); err != nil {
		t.Fatalf("a retry with the settlement's evidence was refused the failed update's candidate: %v", err)
	}
	for name, mutate := range map[string]func(f *settleFixture){
		"the target was never stopped": func(f *settleFixture) {
			mustExec(t, f.s.st, `UPDATE app_platform_operation_effects SET state='prepared' WHERE operation_id=? AND stage='settle' AND effect_number=1`, f.op)
		},
		"the target's removal is not applied": func(f *settleFixture) {
			mustExec(t, f.s.st, `UPDATE app_platform_operation_effects SET state='prepared' WHERE operation_id=? AND stage='settle' AND effect_number=2`, f.op)
		},
		"the evidence belongs to an update outside the chain": func(f *settleFixture) {
			other := scalar[string](t, f.s.st, `SELECT id FROM app_platform_operations WHERE installation_id=? AND action='install'`, f.lease.installationID)
			mustExec(t, f.s.st, `UPDATE app_platform_operation_effects SET operation_id=? WHERE operation_id=? AND stage='settle'`, other, f.op)
		},
		"the removed target was another resource": func(f *settleFixture) {
			mustExec(t, f.s.st, `UPDATE app_platform_operation_effects SET receipt_json=replace(receipt_json,?,?) WHERE operation_id=? AND stage='settle' AND effect_number=2`,
				scalar[string](t, f.s.st, `SELECT id FROM app_platform_activations WHERE installation_id=? AND state='candidate'`, f.lease.installationID), platformFixtureID(8810), f.op)
		},
		"the removed target ran another release": func(f *settleFixture) {
			mustExec(t, f.s.st, `UPDATE app_platform_activations SET release_sha256=(SELECT release_sha256 FROM app_platform_activations WHERE installation_id=? AND state='active') WHERE installation_id=? AND state='candidate'`,
				f.lease.installationID, f.lease.installationID)
		},
		"the removed target held another namespace": func(f *settleFixture) {
			mustExec(t, f.s.st, `UPDATE app_platform_operation_effects SET receipt_json=replace(receipt_json,?,?) WHERE operation_id=? AND stage='settle' AND effect_number=2`,
				scalar[string](t, f.s.st, `SELECT data_namespace_id FROM app_platform_activations WHERE installation_id=? AND state='candidate'`, f.lease.installationID), platformFixtureID(8811), f.op)
		},
	} {
		t.Run(name, func(t *testing.T) {
			f, start := c5RetryAtStart(t)
			mutate(f)
			if _, err := f.s.preparePlatformUpdateStart(context.Background(), start); err == nil {
				t.Fatal("a retry took over a candidate activation without the evidence that it was the failed update's removed target")
			}
		})
	}
}

// failRetryAndSettle sends the retry's migration, which fails, and settles it: its checkpoint is restored.
func failRetryAndSettle(t *testing.T, f *settleFixture, h migrationHTTP, operation string) {
	t.Helper()
	lease := driveRetryToMigrate(t, f, operation)
	h.activateMigrated(t, lease, "failed")
	f.fake.executeRestore = func(i platformRestoreIntent) (platformSettlingRestoreObservation, error) {
		return restoreAnswer(i, f.endpoint), nil
	}
	settled := &settleFixture{s: f.s, lease: f.lease, endpoint: f.endpoint, fake: f.fake, op: operation}
	settled.step(t)
	settled.step(t)
	if settled.settleState(t) != "settled" {
		t.Fatalf("the failed retry did not settle: %s", settled.settleState(t))
	}
}

// Whichever recovery choice finishes a chain resolves the whole chain: rolling back the last retry resolves it and
// every update it recovered, so the installation launches and nothing is left unresolved.
func testRollbackOfAFailedRetryResolvesTheChain(t *testing.T, retries int) {
	platformWritableUpdateFixtures(t)
	f := settledFixture(t, false)
	h := newMigrationHTTP(t, f)
	reportManagedRuntime(f, lastPlatformUpdateFixture.target.Profile)
	chain := []string{f.op}
	for i := range retries {
		operation := h.retryOverHTTP(platformFixtureID(7700 + 10*i))
		failRetryAndSettle(t, f, h, operation)
		chain = append(chain, operation)
	}
	if h.launch() == 200 {
		t.Fatal("an application launched while its failed retry was unresolved")
	}
	rollBackFailedOverHTTP(t, f, h, chain[len(chain)-1])
	if n, err := platformUnresolvedOperationCount(f.s.st.poolExec(), f.lease.installationID); err != nil || n != 0 {
		t.Fatalf("the roll back left %d unresolved operations: %v", n, err)
	}
	if n, err := platformUnresolvedOperationEffectCount(f.s.st.poolExec(), f.lease.installationID); err != nil || n != 0 {
		t.Fatalf("the roll back left %d unsafe effects: %v", n, err)
	}
	if got := h.launch(); got != 200 {
		t.Fatalf("launch after the roll back of a failed retry answered %d", got)
	}
}

func TestMigrationRollbackOfFailedRetryResolvesItsAncestorsHTTP(t *testing.T) {
	testRollbackOfAFailedRetryResolvesTheChain(t, 1)
}

func TestMigrationRollbackOfFailedRetryOfRetryResolvesTheChainHTTP(t *testing.T) {
	testRollbackOfAFailedRetryResolvesTheChain(t, 2)
}

// reviewRetry asks for the review of a retry of the update, through the route, and answers the status.
func (h migrationHTTP) reviewRetry(recovers string) (int, map[string]any) {
	h.t.Helper()
	choice := h.retryChoice()
	rec := h.post("/api/admin/app-platform/plans", `{"action":"update","installation_id":"`+h.f.lease.installationID+
		`","release_sha256":"`+h.str(choice, "release_sha256")+`","trust_revision":"`+h.str(choice, "trust_revision")+
		`","recovers_operation_id":"`+recovers+`"}`)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func (h migrationHTTP) commitPlan(review map[string]any, key string) int {
	h.t.Helper()
	return h.post("/api/admin/app-platform/operations", `{"plan_id":"`+h.str(review, "plan_id")+`","plan_sha256":"`+h.str(review, "plan_sha256")+
		`","idempotency_key":"`+key+`"}`).Code
}

// An update is recovered by one retry. A second retry naming it, whether reviewed before or after the first was
// committed, is refused: only the head of a chain, the latest unresolved update, can be recovered.
func TestMigrationOnlyOneRetryMayRecoverAnUpdateHTTP(t *testing.T) {
	platformWritableUpdateFixtures(t)
	f := settledFixture(t, false)
	h := newMigrationHTTP(t, f)
	reportManagedRuntime(f, lastPlatformUpdateFixture.target.Profile)
	code, first := h.reviewRetry(f.op)
	code2, second := h.reviewRetry(f.op)
	if code != http.StatusOK || code2 != http.StatusOK {
		t.Fatalf("reviews of an unrecovered update: %d %d", code, code2)
	}
	if got := h.commitPlan(first, platformFixtureID(7800)); got != http.StatusAccepted {
		t.Fatalf("the first retry answered %d", got)
	}
	if got := h.commitPlan(second, platformFixtureID(7801)); got == http.StatusAccepted {
		t.Fatal("a second retry of the same update was committed")
	}
	if code, _ := h.reviewRetry(f.op); code == http.StatusOK {
		t.Fatal("a second retry of an update that already has one was reviewed")
	}
	if n := scalar[int](t, f.s.st, `SELECT COUNT(*) FROM app_platform_operations o JOIN app_platform_review_plans p ON p.id=o.review_plan_id WHERE o.action='update' AND p.plan_json LIKE ?`,
		`%"recovers_operation_id":"`+f.op+`"%`); n != 1 {
		t.Fatalf("%d retries recover the update", n)
	}
}

// A chain holds at most 16 updates. The 17th retry is refused when it is reviewed, before anything can be committed.
func TestMigrationSeventeenthRetryRefusedAtReviewHTTP(t *testing.T) {
	platformWritableUpdateFixtures(t)
	f := settledFixture(t, false)
	h := newMigrationHTTP(t, f)
	reportManagedRuntime(f, lastPlatformUpdateFixture.target.Profile)
	head := f.op
	for i := range 16 {
		operation := h.retryOverHTTP(platformFixtureID(7900 + i))
		failRetryAndSettle(t, f, h, operation)
		head = operation
	}
	if code, _ := h.reviewRetry(head); code == http.StatusOK {
		t.Fatal("a seventeenth retry was reviewed")
	}
	if n := scalar[int](t, f.s.st, `SELECT COUNT(*) FROM app_platform_review_plans WHERE plan_json LIKE ?`, `%"recovers_operation_id":"`+head+`"%`); n != 0 {
		t.Fatal("a refused review left a plan behind")
	}
}
