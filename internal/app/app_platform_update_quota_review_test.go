package app

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/transport"
)

// platformRaiseReviewHTTP stages an update whose target asks for a larger quota and returns the routes an
// administrator uses to review and commit it, with the runner's capacity answer as given.
func platformRaiseReviewHTTP(t *testing.T, pg, fits bool) (platformRecoveryHTTP, platformManagedUpdateReviewFixture, *platformFakeDataRunner) {
	t.Helper()
	return platformReviewHTTPFor(t, pg, fits, &appplatform.DataQuota{Bytes: platformRaisedBytes, Inodes: platformRaisedInodes})
}

// platformReviewHTTPFor is platformRaiseReviewHTTP for a target that asks for the given quota (none when nil).
func platformReviewHTTPFor(t *testing.T, pg, fits bool, quota *appplatform.DataQuota) (platformRecoveryHTTP, platformManagedUpdateReviewFixture, *platformFakeDataRunner) {
	t.Helper()
	runner := &platformFakeDataRunner{fits: fits}
	runner.install(t)
	f := platformManagedUpdateProjectionFixtureWith(t, pg, func(m *appplatform.Manifest) { m.Data.Quota = quota })
	s := f.server
	endpoint := platformManagedRemovalFixtureEndpoint()
	s.platformBackendEndpoint.Store(&endpoint)
	s.platformReadManagedRuntime = func(_ context.Context, e platformRuntimeEndpoint, a control.Authority) (platformManagedRuntimeObserved, error) {
		return platformFakeManagedRuntime{
			profile: f.target.Profile, authority: a, at: time.Now(),
			endpoint: transport.CandidateReceiptEndpoint{Path: e.path, ControllerUID: e.controllerUID, ControllerGID: e.controllerGID, RootController: true},
		}, nil
	}
	actor := platformManagedFixtureSession(t, s)
	mux := http.NewServeMux()
	s.wireRoutes(mux)
	cookie := s.managedCookie(*s.st.GetUser(actor.username), actor.sessionID, actor.expires.Unix())
	return platformRecoveryHTTP{t: t, mux: mux, cookie: cookie}, f, runner
}

func platformReviewBody(f platformManagedUpdateReviewFixture) string {
	return `{"action":"update","installation_id":"` + f.installation + `","release_sha256":"` + f.release.sha256 +
		`","trust_revision":"` + strconv.FormatInt(f.release.trustRevision, 10) + `"}`
}

// Tests 1, 2 and 19: the review shows the raise with decimal strings and the runner's answer, and a review the
// runner's capacity cannot hold is never admitted.
func testUpdateReviewShowsTheRaiseAndTheRunnersCapacity(t *testing.T, pg bool) {
	const plans, operations = "/api/admin/app-platform/plans", "/api/admin/app-platform/operations"
	h, f, runner := platformRaiseReviewHTTP(t, pg, true)
	review := h.json(h.post(plans, platformReviewBody(f)), http.StatusOK)
	body, _ := review["review"].(map[string]any)
	quota, _ := body["data_quota"].(map[string]any)
	want := map[string]any{
		"current_bytes": "268435456", "current_inodes": "65536", "requested_bytes": "1073741824", "requested_inodes": "100000",
		"new_bytes": "1073741824", "new_inodes": "100000", "raise_applied": true, "capacity": "fits",
	}
	for key, value := range want {
		if quota[key] != value {
			t.Fatalf("review data_quota %s = %v, want %v: %v", key, quota[key], value, quota)
		}
	}
	commit := h.json(h.post(operations, `{"plan_id":"`+h.str(review, "plan_id")+`","plan_sha256":"`+h.str(review, "plan_sha256")+`","idempotency_key":"`+platformFixtureID(7701)+`"}`), http.StatusAccepted)
	plan, _ := platformPlanOf(t, f.server, h.str(commit, "operation_id"))
	if plan.DataQuota == nil || !plan.DataQuota.RaiseApplied || scalar[int](t, f.server.st, `SELECT COUNT(*) FROM app_platform_operations WHERE action='update'`) != 1 {
		t.Fatal("the admitted update lost its review")
	}

	// The runner is short: the review names the shortfall, and consuming it admits nothing.
	runner.fits = false
	short := h.json(h.post(plans, platformReviewBody(f)), http.StatusOK)
	shortBody, _ := short["review"].(map[string]any)
	shortQuota, _ := shortBody["data_quota"].(map[string]any)
	if shortQuota["capacity"] != "short" || shortQuota["capacity_dimension"] != "data_quota" || shortQuota["capacity_remaining"] != "1073741824" || shortQuota["capacity_requested"] != "6442450944" {
		t.Fatalf("the shortfall is not named: %v", shortQuota)
	}
	before := scalar[int](t, f.server.st, `SELECT COUNT(*) FROM app_platform_operations`)
	refused := h.post(operations, `{"plan_id":"`+h.str(short, "plan_id")+`","plan_sha256":"`+h.str(short, "plan_sha256")+`","idempotency_key":"`+platformFixtureID(7702)+`"}`)
	if refused.Code != http.StatusConflict || !strings.Contains(refused.Body.String(), "data_quota_capacity") {
		t.Fatalf("a raise beyond capacity was confirmed: %d %s", refused.Code, refused.Body.String())
	}
	if scalar[int](t, f.server.st, `SELECT COUNT(*) FROM app_platform_operations`) != before ||
		scalar[int](t, f.server.st, `SELECT COUNT(*) FROM app_platform_review_plans WHERE id=? AND consumed_ms IS NOT NULL`, h.str(short, "plan_id")) != 0 {
		t.Fatal("a refused review admitted an operation or was consumed")
	}
}

func TestUpdateReviewShowsTheRaiseAndTheRunnersCapacitySQLite(t *testing.T) {
	testUpdateReviewShowsTheRaiseAndTheRunnersCapacity(t, false)
}
func TestUpdateReviewShowsTheRaiseAndTheRunnersCapacityPostgres(t *testing.T) {
	testUpdateReviewShowsTheRaiseAndTheRunnersCapacity(t, true)
}

// An update that raises nothing shows no data_quota and asks the runner nothing.
func TestUpdateReviewWithoutARaiseShowsNoQuotaAndAsksNothing(t *testing.T) {
	h, f, _ := platformReviewHTTPFor(t, false, true, nil)
	asked := 0
	ask := platformDataQueryRunner
	platformDataQueryRunner = func(ctx context.Context, endpoint platformRuntimeEndpoint, request platformDataQueryRequest) (platformDataQueryObservation, error) {
		asked++
		return ask(ctx, endpoint, request)
	}
	t.Cleanup(func() { platformDataQueryRunner = ask })
	review := h.json(h.post("/api/admin/app-platform/plans", platformReviewBody(f)), http.StatusOK)
	if body, _ := review["review"].(map[string]any); body["data_quota"] != nil {
		t.Fatalf("an update that raises nothing shows a quota: %v", body["data_quota"])
	}
	if asked != 0 {
		t.Fatalf("the runner was asked %d times about an update that raises nothing", asked)
	}
}

// Decision 5: a review stored before the update raised the quota (raise_applied false) is refused at consumption and
// has to be reviewed again.
func testLegacyUnconsumedReviewMustBeReviewedAgain(t *testing.T, pg bool) {
	const plans, operations = "/api/admin/app-platform/plans", "/api/admin/app-platform/operations"
	h, f, _ := platformRaiseReviewHTTP(t, pg, true)
	review := h.json(h.post(plans, platformReviewBody(f)), http.StatusOK)
	planID := h.str(review, "plan_id")
	raw := scalar[string](t, f.server.st, `SELECT plan_json FROM app_platform_review_plans WHERE id=?`, planID)
	var projection platformUpdateProjection
	if err := json.Unmarshal([]byte(raw), &projection); err != nil || projection.DataQuota == nil {
		t.Fatal("the review has no quota", err)
	}
	q := projection.DataQuota
	projection.DataQuota = &platformUpdateQuotaReview{
		CurrentBytes: q.CurrentBytes, CurrentInodes: q.CurrentInodes, RequestedBytes: q.RequestedBytes, RequestedInodes: q.RequestedInodes,
		RaiseBytes: q.RaiseBytes, RaiseInodes: q.RaiseInodes,
	}
	legacy, _ := json.Marshal(projection)
	mustExec(t, f.server.st, `UPDATE app_platform_review_plans SET plan_json=?,plan_sha256=? WHERE id=?`, string(legacy), platformDigest(legacy), planID)
	refused := h.post(operations, `{"plan_id":"`+planID+`","plan_sha256":"`+platformDigest(legacy)+`","idempotency_key":"`+platformFixtureID(7710)+`"}`)
	if refused.Code != http.StatusConflict || !strings.Contains(refused.Body.String(), "app_platform_plan_changed") {
		t.Fatalf("a legacy review was consumed: %d %s", refused.Code, refused.Body.String())
	}
	if scalar[int](t, f.server.st, `SELECT COUNT(*) FROM app_platform_operations WHERE action='update'`) != 0 {
		t.Fatal("a legacy review admitted an operation")
	}
}

func TestLegacyUnconsumedReviewMustBeReviewedAgainSQLite(t *testing.T) {
	testLegacyUnconsumedReviewMustBeReviewedAgain(t, false)
}
func TestLegacyUnconsumedReviewMustBeReviewedAgainPostgres(t *testing.T) {
	testLegacyUnconsumedReviewMustBeReviewedAgain(t, true)
}

// Test 15 and decision 5: an update admitted from a legacy review runs to its start with no raise, and the finalized
// plan carries none.
func testLegacyInFlightUpdateRunsWithNoRaise(t *testing.T, pg bool) {
	platformFakeObservedAt.Clear()
	s, operation := platformManagedUpdateOperationFixtureWith(t, pg, func(m *appplatform.Manifest) {
		m.Data.Quota = &appplatform.DataQuota{Bytes: platformRaisedBytes, Inodes: platformRaisedInodes}
	})
	raw := scalar[string](t, s.st, `SELECT p.plan_json FROM app_platform_operations o JOIN app_platform_review_plans p ON p.id=o.review_plan_id WHERE o.id=?`, operation.id)
	var projection platformUpdateProjection
	if err := json.Unmarshal([]byte(raw), &projection); err != nil || projection.DataQuota == nil || !projection.DataQuota.RaiseApplied {
		t.Fatal("the fixture's review has no raise", err)
	}
	q := projection.DataQuota
	projection.DataQuota = &platformUpdateQuotaReview{
		CurrentBytes: q.CurrentBytes, CurrentInodes: q.CurrentInodes, RequestedBytes: q.RequestedBytes, RequestedInodes: q.RequestedInodes,
		RaiseBytes: q.RaiseBytes, RaiseInodes: q.RaiseInodes,
	}
	legacy, _ := json.Marshal(projection)
	mustExec(t, s.st, `UPDATE app_platform_review_plans SET plan_json=?,plan_sha256=? WHERE id=(SELECT review_plan_id FROM app_platform_operations WHERE id=?)`, string(legacy), platformDigest(legacy), operation.id)
	selected, err := s.claimNextPlatformManagedUpdate(context.Background(), platformFixtureID(7720))
	if err != nil || selected.lease.id != operation.id {
		t.Fatal("claim", err)
	}
	lease := selected.lease
	for range 4 {
		if lease, err = s.advancePlatformManagedUpdateStage(context.Background(), lease); err != nil {
			t.Fatal("a legacy review no longer admits its in-flight update", err)
		}
	}
	quiesce, err := s.preparePlatformManagedUpdateQuiesce(context.Background(), lease)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := s.commitPlatformManagedUpdateQuiesce(context.Background(), lease, quiesce, platformManagedUpdateQuiesceReceipt(quiesce))
	if err != nil {
		t.Fatal(err)
	}
	endpoint := platformCheckpointEndpointFixture()
	checkpoint, err := s.preparePlatformCheckpoint(context.Background(), snapshot)
	if err != nil {
		t.Fatal(err)
	}
	migrate, err := s.commitPlatformCheckpoint(context.Background(), snapshot, checkpoint, endpoint, platformCheckpointObservationFixture(checkpoint, endpoint))
	if err != nil || migrate.snapshot.Stage != appplatform.StageMigrate {
		t.Fatal("checkpoint", migrate.snapshot, err)
	}
	if plan, rawPlan := platformPlanOf(t, s, operation.id); plan.DataQuota != nil {
		t.Fatal("a legacy review's finalized plan carries a raise", rawPlan)
	}
	removal, err := s.preparePlatformManagedUpdateMigrate(context.Background(), migrate)
	if err != nil {
		t.Fatal(err)
	}
	next, err := s.commitPlatformManagedUpdateQuiesce(context.Background(), migrate, removal, platformManagedUpdateQuiesceReceipt(removal))
	if err != nil || next.snapshot.Stage != appplatform.StageStart ||
		scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_operation_effects WHERE operation_id=? AND kind='set-data-quota'`, operation.id) != 0 {
		t.Fatalf("a legacy update did not go on to its start with no raise: %v %v", next.snapshot, err)
	}
}

func TestLegacyInFlightUpdateRunsWithNoRaiseSQLite(t *testing.T) {
	testLegacyInFlightUpdateRunsWithNoRaise(t, false)
}
func TestLegacyInFlightUpdateRunsWithNoRaisePostgres(t *testing.T) {
	testLegacyInFlightUpdateRunsWithNoRaise(t, true)
}

// Risk 2: at the validate stage, before anything stops, the update asks the runner again; when it is short the
// update ends failed with no effects and the code that names the capacity.
func testUpdateRaiseCapacityIsAskedAgainBeforeAnythingStops(t *testing.T, pg bool) {
	platformFakeObservedAt.Clear()
	runner := &platformFakeDataRunner{fits: true}
	runner.install(t)
	s, operation := platformManagedUpdateOperationFixtureWith(t, pg, func(m *appplatform.Manifest) {
		m.Data.Quota = &appplatform.DataQuota{Bytes: platformRaisedBytes, Inodes: platformRaisedInodes}
	})
	endpoint := platformManagedRemovalFixtureEndpoint()
	selected, err := s.claimNextPlatformManagedUpdate(context.Background(), platformFixtureID(7730))
	if err != nil || selected.lease.id != operation.id {
		t.Fatal("claim", err)
	}
	lease, err := s.runPlatformManagedUpdate(context.Background(), selected.lease, endpoint)
	if err != nil || lease.snapshot.Stage != appplatform.StageValidate {
		t.Fatal("queued", lease.snapshot, err)
	}
	runner.fits = false
	if _, err := s.runPlatformManagedUpdate(context.Background(), lease, endpoint); !errors.Is(err, errPlatformMigrationEnded) {
		t.Fatalf("a short runner did not end the update: %v", err)
	}
	if operationStage(t, s, operation.id) != "failed" || scalar[string](t, s.st, `SELECT error_code FROM app_platform_operations WHERE id=?`, operation.id) != "data_quota_capacity" ||
		scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_operation_effects WHERE operation_id=?`, operation.id) != 0 ||
		scalar[string](t, s.st, `SELECT phase FROM app_platform_installations WHERE id=?`, operation.installationID) != "ready" {
		t.Fatal("the update did not fail cleanly before anything stopped")
	}
}

func TestUpdateRaiseCapacityIsAskedAgainBeforeAnythingStopsSQLite(t *testing.T) {
	testUpdateRaiseCapacityIsAskedAgainBeforeAnythingStops(t, false)
}
func TestUpdateRaiseCapacityIsAskedAgainBeforeAnythingStopsPostgres(t *testing.T) {
	testUpdateRaiseCapacityIsAskedAgainBeforeAnythingStops(t, true)
}

// An update that fits passes the validate stage on its way.
func TestUpdateRaiseCapacityThatFitsAdvancesTheValidateStage(t *testing.T) {
	platformFakeObservedAt.Clear()
	(&platformFakeDataRunner{fits: true}).install(t)
	s, operation := platformManagedUpdateOperationFixtureWith(t, false, func(m *appplatform.Manifest) {
		m.Data.Quota = &appplatform.DataQuota{Bytes: platformRaisedBytes, Inodes: platformRaisedInodes}
	})
	selected, err := s.claimNextPlatformManagedUpdate(context.Background(), platformFixtureID(7740))
	if err != nil {
		t.Fatal(err)
	}
	lease := selected.lease
	for lease.snapshot.Stage != appplatform.StageQuiesce {
		if lease, err = s.runPlatformManagedUpdate(context.Background(), lease, platformManagedRemovalFixtureEndpoint()); err != nil {
			t.Fatal(err)
		}
	}
	if operationStage(t, s, operation.id) != "quiesce" {
		t.Fatal("a fitting update did not pass the validate stage")
	}
}
