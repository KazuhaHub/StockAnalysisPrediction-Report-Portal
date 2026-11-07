package app

import (
	"context"
	"errors"
	"net/http"
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

// activateMigrated sends the retry's migration, then starts and activates its target.
func (h migrationHTTP) activateMigrated(t *testing.T, lease platformOperationLease, state string) {
	t.Helper()
	f := h.f
	f.fake.executeMigrate = func(i platformMigrateIntent) (platformMigrateObservation, error) {
		return platformMigrateAnswers(i, f.endpoint, state, map[string]string{"migrated": "", "failed": "exit_nonzero"}[state]), nil
	}
	start, err := f.s.dispatchPlatformMigrateData(context.Background(), lease, f.endpoint)
	if state != "migrated" {
		if !errors.Is(err, errPlatformMigrationEnded) {
			t.Fatal(err)
		}
		return
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
