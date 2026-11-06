package app

import (
	"context"
	"encoding/hex"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

func (f *platformImportFixtureState) abandon(t *testing.T) (platformAbandonResult, error) {
	t.Helper()
	_, digest, err := f.s.previewPlatformAbandonRuntime(context.Background(), f.actor, f.replacement.id)
	if err != nil {
		return platformAbandonResult{}, err
	}
	result, _, err := f.s.confirmPlatformAbandonRuntime(context.Background(), f.actor, f.replacement.id, digest)
	return result, err
}

// The owner's decision: the replacement host is discarded. Everything that rested on it is settled
// without asking it anything, and a new replacement runtime can be enrolled and the import redone.
func TestAbandonReplacementRuntimeFreesGrantsAndInstallations(t *testing.T) {
	f, operation := unknownImportFixture(t)
	// A second operation is still in flight on the same runtime.
	generation := scalar[int64](t, f.s.st, `SELECT generation FROM app_platform_installations WHERE id=?`, f.installation)
	got, err := f.abandon(t)
	if err != nil || got.RuntimeID != f.replacement.id || got.Grants != 1 || got.Effects != 1 || got.Installations != 1 {
		t.Fatalf("abandon: %+v %v", got, err)
	}
	if scalar[int](t, f.s.st, `SELECT COUNT(*) FROM app_platform_runtime_bindings WHERE id=? AND enabled=0 AND sealed_control_key IS NULL AND verification_key_fingerprint IS NULL`, f.replacement.id) != 1 ||
		scalar[string](t, f.s.st, `SELECT state FROM app_platform_import_grants`) != "revoked" ||
		scalar[string](t, f.s.st, `SELECT state FROM app_platform_operation_effects WHERE operation_id=?`, operation) != "reverted" ||
		scalar[int](t, f.s.st, `SELECT COUNT(*) FROM app_platform_operation_effects WHERE operation_id=? AND receipt_json LIKE '%runtime abandoned%'`, operation) != 1 {
		t.Fatal("the runtime was not retired, its grant revoked and its effect reverted with the reason")
	}
	if scalar[string](t, f.s.st, `SELECT desired_state||':'||phase FROM app_platform_installations WHERE id=?`, f.installation) != "disabled:review_required" ||
		scalar[int64](t, f.s.st, `SELECT generation FROM app_platform_installations WHERE id=?`, f.installation) != generation+1 ||
		scalar[int](t, f.s.st, `SELECT COUNT(*) FROM audit_log WHERE action='app_platform.runtime_abandoned' AND target_id=?`, f.replacement.id) != 1 {
		t.Fatal("the installation did not return to review, or the abandon left no audit row")
	}
	// A new replacement with a fresh deployment is enrolled and the import is redone on it.
	reg, err := f.s.preparePlatformReplacementRuntime(f.actor, platformFixtureID(5102))
	if err != nil {
		t.Fatal("a new replacement could not be enrolled after the abandon:", err)
	}
	if f.replacement, err = f.s.confirmPlatformObservedReplacementRuntime(f.actor, reg, runtimeObservationFixture(reg)); err != nil {
		t.Fatal(err)
	}
	review, err := f.s.createPlatformRestoreImportReview(context.Background(), f.actor, f.installation, f.recoverySet)
	if err != nil {
		t.Fatal("the import could not be reviewed again after the abandon:", err)
	}
	retry, err := f.s.consumePlatformRestoreImportReview(context.Background(), f.actor, review.ID, review.SHA256, platformFixtureID(5621))
	if err != nil {
		t.Fatal(err)
	}
	if done, err := f.drive(t, retry.OperationID); err != nil || done.snapshot.Stage != appplatform.StageComplete {
		t.Fatalf("the redone import did not complete: %v", err)
	}
}

func TestAbandonEndsAnOperationInFlightAndRefusesOutsideRecovery(t *testing.T) {
	f := platformRestoreImportFixture(t, false)
	commit := f.reviewAndConsume(t)
	platformSetPause(t, f.s.st, "0")
	if _, err := f.abandon(t); err == nil {
		t.Fatal("a Portal that is not in recovery abandoned a runtime")
	}
	if scalar[int](t, f.s.st, `SELECT enabled FROM app_platform_runtime_bindings WHERE id=?`, f.replacement.id) != 1 {
		t.Fatal("a refused abandon retired the runtime")
	}
	platformSetPause(t, f.s.st, "1")
	if got, err := f.abandon(t); err != nil || got.Operations != 1 {
		t.Fatalf("abandon: %+v %v", got, err)
	}
	if scalar[string](t, f.s.st, `SELECT stage FROM app_platform_operations WHERE id=?`, commit.OperationID) != "failed" ||
		scalar[int](t, f.s.st, `SELECT COUNT(*) FROM app_platform_operations WHERE id=? AND completed_ms IS NOT NULL AND lease_owner IS NULL AND error_code='runtime_abandoned'`, commit.OperationID) != 1 ||
		scalar[string](t, f.s.st, `SELECT state FROM app_platform_import_grants`) != "revoked" {
		t.Fatal("the in-flight import was not ended with its grant revoked")
	}
	if _, err := f.abandon(t); !errors.Is(err, errAppPlatformAbandonUnavailable) {
		t.Fatalf("a second abandon found a runtime to abandon: %v", err)
	}
}

// The listing is a contract: a confirmation is refused when what it listed has changed, and a repeated
// confirmation of the same listing replays the first answer rather than acting again.
func TestAbandonConfirmationIsRefusedWhenTheListedWorkChanged(t *testing.T) {
	f, _ := unknownImportFixture(t)
	_, digest, err := f.s.previewPlatformAbandonRuntime(context.Background(), f.actor, f.replacement.id)
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, f.s.st, `UPDATE app_platform_import_grants SET state='revoked'`)
	if _, _, err := f.s.confirmPlatformAbandonRuntime(context.Background(), f.actor, f.replacement.id, digest); !errors.Is(err, errAppPlatformPlanChanged) {
		t.Fatalf("a stale abandon confirmation was accepted: %v", err)
	}
	if scalar[int](t, f.s.st, `SELECT enabled FROM app_platform_runtime_bindings WHERE id=?`, f.replacement.id) != 1 {
		t.Fatal("a refused abandon retired the runtime")
	}
}

// After the abandon a receipt that arrives late from the discarded host settles nothing.
func TestAbandonedRuntimeCannotSettleLateReceipt(t *testing.T) {
	f, operation := unknownImportFixture(t)
	candidates, err := f.s.platformRecoveryImportCandidates(context.Background())
	if err != nil || len(candidates) != 1 {
		t.Fatalf("candidates: %v %v", candidates, err)
	}
	intent, err := f.s.signPlatformRecoveryImportRead(context.Background(), candidates[0])
	if err != nil {
		t.Fatal(err)
	}
	late := platformImportObservationFixture(intent, platformRestoreEndpointFixture())
	if _, err := f.abandon(t); err != nil {
		t.Fatal(err)
	}
	if done, err := f.s.commitPlatformRecoveryImport(context.Background(), candidates[0], intent, platformRestoreEndpointFixture(), late); err == nil || done {
		t.Fatalf("a late applied receipt from the abandoned runtime was accepted: %v", err)
	}
	if settled, err := f.s.reconcilePlatformRecoveryImports(context.Background(), platformRestoreEndpointFixture()); err != nil || settled != 0 {
		t.Fatalf("reconcile settled something on an abandoned runtime: %d %v", settled, err)
	}
	if scalar[string](t, f.s.st, `SELECT state FROM app_platform_operation_effects WHERE operation_id=?`, operation) != "reverted" ||
		scalar[string](t, f.s.st, `SELECT state FROM app_platform_import_grants`) != "revoked" ||
		scalar[string](t, f.s.st, `SELECT stage FROM app_platform_operations WHERE id=?`, operation) != "failed" {
		t.Fatal("a late receipt changed the effect, the grant or the operation")
	}
}

// An import whose answer is lost wedges recovery. The owner abandons the replacement runtime through the
// HTTP surface, enrolls a new one, redoes the import and the resume, and recovery ends.
func TestRecoveryAbandonThroughHTTP(t *testing.T) {
	s, actor, updateLease, recoverySetID := platformManagedRestoreReviewFixture(t, false)
	installation := updateLease.installationID
	platformSimulateRestore(t, s)
	endpoint := platformUpdateStartEndpointFixture()
	s.platformBackendEndpoint.Store(&endpoint)
	executor := &platformImportTestExecutor{}
	s.platformImportCheckpoint = executor
	var registered platformRuntimeRegistration
	s.platformObserveRuntime = func(context.Context, platformRuntimeEndpoint) (platformRuntimeObservation, error) {
		return runtimeObservationFixture(registered), nil
	}
	mux := http.NewServeMux()
	s.wireRoutes(mux)
	h := platformRecoveryHTTP{t: t, mux: mux, cookie: s.managedCookie(*s.st.GetUser(actor.username), actor.sessionID, actor.expires.Unix())}
	const (
		replacement = "/api/admin/app-platform/control/replacement-runtime"
		plans       = "/api/admin/app-platform/plans"
		operations  = "/api/admin/app-platform/operations"
	)
	epoch := s.st.GetSetting("app_platform_controller_epoch", "")
	enroll := func(deployment string) {
		prepared := h.json(h.post(replacement, `{"action":"prepare","deployment_id":"`+deployment+`"}`), 200)
		pub, err := hex.DecodeString(h.str(prepared, "public_key"))
		if err != nil {
			t.Fatal(err)
		}
		registered = platformRuntimeRegistration{id: h.str(prepared, "runtime_id"), deploymentID: deployment, controllerEpoch: epoch, controlRevision: 1, revision: 1,
			fingerprint: h.str(prepared, "verification_key_fingerprint"), publicKey: pub}
		h.json(h.post(replacement, `{"action":"confirm","deployment_id":"`+deployment+`"}`), 200)
		registered.revision = 2
	}
	importOnce := func(key string) string {
		review := h.json(h.post(plans, `{"action":"restore_import","installation_id":"`+installation+`","recovery_set_id":"`+recoverySetID+`"}`), 200)
		body := `{"plan_id":"` + h.str(review, "plan_id") + `","plan_sha256":"` + h.str(review, "plan_sha256") + `","idempotency_key":"` + key + `"}`
		return h.str(h.json(h.post(operations, body), 202), "operation_id")
	}
	stage := func(operation string) string {
		return scalar[string](t, s.st, `SELECT stage FROM app_platform_operations WHERE id=?`, operation)
	}

	// The first import is lost: the runner answers nothing, now or later.
	enroll(platformFixtureID(5100))
	executor.err, executor.readErr = errors.New("lost reply"), errors.New("endpoint unreachable")
	firstImport := importOnce(platformFixtureID(5800))
	worker, err := newPlatformWritableInstallScheduler(s, platformRestoreEndpointFixture(), platformFixtureID(5805), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 20 && stage(firstImport) != "recovery_required"; i++ {
		_ = worker.tick(context.Background())
	}
	if stage(firstImport) != "recovery_required" || scalar[string](t, s.st, `SELECT state FROM app_platform_import_grants`) != "issued" {
		t.Fatalf("the import did not wedge as the scenario requires: %s", stage(firstImport))
	}
	h.endRecovery(409)
	if got := h.post(replacement, `{"action":"prepare","deployment_id":"`+platformFixtureID(5101)+`"}`); got.Code != 409 {
		t.Fatalf("a second replacement was enrolled beside the live one: %d", got.Code)
	}

	// Abandon: listed, then confirmed against that listing; confirming it again replays.
	const abandon = "/api/admin/app-platform/control/abandon-runtime"
	listing := h.json(h.post(abandon, `{"action":"prepare","runtime_id":"`+registered.id+`"}`), 200)
	if r, _ := listing["review"].(map[string]any); r["action"] != "abandon_runtime" || r["runtime_id"] != registered.id || len(r["grants"].([]any)) != 1 || len(r["effects"].([]any)) != 1 {
		t.Fatalf("abandon listing: %v", listing)
	}
	if got := h.post(abandon, `{"action":"prepare","runtime_id":"`+platformFixtureID(9999)+`"}`); got.Code != 409 {
		t.Fatalf("a listing named a runtime that is not enrolled: %d", got.Code)
	}
	if got := h.post(abandon, `{"action":"confirm","runtime_id":"`+registered.id+`","plan_sha256":"`+platformDigest([]byte("other"))+`"}`); got.Code != 409 {
		t.Fatalf("a confirmation of another listing was accepted: %d", got.Code)
	}
	confirmBody := `{"action":"confirm","runtime_id":"` + registered.id + `","plan_sha256":"` + h.str(listing, "plan_sha256") + `"}`
	if abandoned := h.json(h.post(abandon, confirmBody), 202); abandoned["abandoned"] != true || abandoned["runtime_id"] != registered.id || abandoned["replayed"] != false {
		t.Fatalf("abandon confirmation: %v", abandoned)
	}
	if replay := h.json(h.post(abandon, confirmBody), 202); replay["abandoned"] != true || replay["replayed"] != true {
		t.Fatalf("an abandon confirmation did not replay: %v", replay)
	}
	if scalar[int](t, s.st, `SELECT COUNT(*) FROM audit_log WHERE action='app_platform.runtime_abandoned' AND target_id=?`, registered.id) != 1 {
		t.Fatal("the abandon acted twice")
	}
	if got := h.post(plans, `{"action":"abandon_runtime","runtime_id":"`+registered.id+`"}`); got.Code == 200 {
		t.Fatal("the plans route still reviews an abandon")
	}

	// A new replacement runtime, a fresh deployment, and the import is redone.
	executor.err, executor.readErr = nil, nil
	enroll(platformFixtureID(5101))
	secondImport := importOnce(platformFixtureID(5801))
	worker, err = newPlatformWritableInstallScheduler(s, platformRestoreEndpointFixture(), platformFixtureID(5806), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 12 && stage(secondImport) != "complete"; i++ {
		if err := worker.tick(context.Background()); err != nil {
			t.Fatal("import tick:", err)
		}
	}
	if stage(secondImport) != "complete" {
		t.Fatalf("the redone import did not complete: %s", stage(secondImport))
	}
	state := &platformResumeFixtureState{platformImportFixtureState: &platformImportFixtureState{replacement: registered}, endpoint: endpoint}
	s.platformRecoveryTarget = func(context.Context, platformActorSession, platformRuntimeEndpoint) (*platformManagedInstallTarget, error) {
		return state.target(), nil
	}
	resumeReview := h.json(h.post(plans, `{"action":"resume","installation_id":"`+installation+`"}`), 200)
	resumeBody := `{"plan_id":"` + h.str(resumeReview, "plan_id") + `","plan_sha256":"` + h.str(resumeReview, "plan_sha256") + `","idempotency_key":"` + platformFixtureID(5802) + `"}`
	resumeOp := h.str(h.json(h.post(operations, resumeBody), 202), "operation_id")
	for i := 0; i < 8 && stage(resumeOp) != "start"; i++ {
		if err := worker.tick(context.Background()); err != nil {
			t.Fatal("resume tick:", err)
		}
	}
	if stage(resumeOp) != "start" || worker.active.id != resumeOp {
		t.Fatal("the resume did not reach its start stage")
	}
	if done := platformResumeStartToReady(t, s, worker.active, endpoint); done.snapshot.Stage != appplatform.StageComplete {
		t.Fatal("the resume did not complete")
	}
	h.endRecovery(200)
	if scalar[string](t, s.st, `SELECT phase FROM app_platform_installations WHERE id=?`, installation) != "ready" ||
		scalar[string](t, s.st, `SELECT v FROM meta WHERE k='app_platform_restore_paused'`) != "0" {
		t.Fatal("the installation is not ready or recovery did not end")
	}
	// The abandoned import and the lost first one left nothing that blocks launch: once the publisher
	// is trusted again the resumed app launches.
	if platformOperationBlocksLaunch(t, s, firstImport) {
		t.Fatal("the abandoned import still blocks launch")
	}
	publisher := scalar[string](t, s.st, `SELECT publisher_id FROM app_platform_installations WHERE id=?`, installation)
	if got := sessionTestRequest(mux, "POST", "/api/admin/app-platform/publishers", h.cookie, platformReenablePublisherRequest(t, s, publisher)); got.Code != 200 {
		t.Fatalf("trust review: %d %s", got.Code, got.Body.String())
	}
	if got := sessionTestRequest(mux, "GET", "/api/app-platform/installations/"+installation+"/launch", h.cookie, ""); got.Code != 200 {
		_, why := s.inspectPlatformLaunchAccess(actor, installation)
		t.Fatalf("a resumed app did not launch after an abandoned import: %d %s (%v)", got.Code, got.Body.String(), why)
	}
}

// Abandoning is a deployment-level decision. The review-plan table is installation-shaped (it demands
// an application and a publisher), so the abandon must not write one with borrowed values: nothing in
// that table may name a publisher the abandon has no business with.
func TestAbandonWritesNoReviewPlanAndIsIdempotent(t *testing.T) {
	f, _ := unknownImportFixture(t)
	plans := func() int { return scalar[int](t, f.s.st, `SELECT COUNT(*) FROM app_platform_review_plans`) }
	before := plans()
	plan, digest, err := f.s.previewPlatformAbandonRuntime(context.Background(), f.actor, f.replacement.id)
	if err != nil || plan.RuntimeID != f.replacement.id || len(plan.Grants) != 1 {
		t.Fatalf("preview: %+v %v", plan, err)
	}
	if again, sum, err := f.s.previewPlatformAbandonRuntime(context.Background(), f.actor, f.replacement.id); err != nil || sum != digest || len(again.Effects) != len(plan.Effects) {
		t.Fatalf("a preview is not deterministic: %v", err)
	}
	first, replayed, err := f.s.confirmPlatformAbandonRuntime(context.Background(), f.actor, f.replacement.id, digest)
	if err != nil || replayed || first.Grants != 1 {
		t.Fatalf("confirm: %+v replayed=%v %v", first, replayed, err)
	}
	// The same confirmation again answers what the first did, and acts on nothing.
	generation := scalar[int64](t, f.s.st, `SELECT generation FROM app_platform_installations WHERE id=?`, f.installation)
	again, replayed, err := f.s.confirmPlatformAbandonRuntime(context.Background(), f.actor, f.replacement.id, digest)
	if err != nil || !replayed || again != first {
		t.Fatalf("replay: %+v replayed=%v %v", again, replayed, err)
	}
	if scalar[int64](t, f.s.st, `SELECT generation FROM app_platform_installations WHERE id=?`, f.installation) != generation ||
		scalar[int](t, f.s.st, `SELECT COUNT(*) FROM audit_log WHERE action='app_platform.runtime_abandoned' AND target_id=?`, f.replacement.id) != 1 {
		t.Fatal("a replayed confirmation acted again")
	}
	// A different listing for a runtime that was already abandoned is a conflict, not a second abandon.
	if _, _, err := f.s.confirmPlatformAbandonRuntime(context.Background(), f.actor, f.replacement.id, platformDigest([]byte("other"))); !errors.Is(err, errAppPlatformPlanChanged) {
		t.Fatalf("a different listing replayed an abandon: %v", err)
	}
	if plans() != before || scalar[int](t, f.s.st, `SELECT COUNT(*) FROM app_platform_review_plans WHERE app_id='recovery' OR (installation_id IS NULL AND action='disable')`) != 0 {
		t.Fatal("the abandon wrote a review plan row")
	}
}

// An operation that had already ended recovery_required when the runtime was abandoned is settled too:
// every effect it left is reverted, so it ends failed and no longer blocks launch.
func TestAbandonSettlesAFinishedUnknownImportAndFreesLaunch(t *testing.T) {
	f, operation := unknownImportFixture(t)
	if !platformOperationBlocksLaunch(t, f.s, operation) {
		t.Fatal("fixture: an unsettled import does not block launch")
	}
	if _, err := f.abandon(t); err != nil {
		t.Fatal(err)
	}
	if scalar[string](t, f.s.st, `SELECT stage FROM app_platform_operations WHERE id=?`, operation) != "failed" ||
		scalar[int](t, f.s.st, `SELECT COUNT(*) FROM app_platform_operations WHERE id=? AND error_code='runtime_abandoned' AND completed_ms IS NOT NULL`, operation) != 1 {
		t.Fatal("an abandoned finished import did not end failed")
	}
	if platformOperationBlocksLaunch(t, f.s, operation) {
		t.Fatal("an abandoned import still blocks launch")
	}
}

// The record of an abandon belongs to one controller epoch. After another restore the same runtime id
// and listing are a different decision, so the recorded outcome is not replayed for them.
func TestAbandonConfirmationIsNotReplayedInAnotherEpoch(t *testing.T) {
	f, _ := unknownImportFixture(t)
	_, digest, err := f.s.previewPlatformAbandonRuntime(context.Background(), f.actor, f.replacement.id)
	if err != nil {
		t.Fatal(err)
	}
	if _, replayed, err := f.s.confirmPlatformAbandonRuntime(context.Background(), f.actor, f.replacement.id, digest); err != nil || replayed {
		t.Fatalf("confirm: replayed=%v %v", replayed, err)
	}
	before := f.s.st.GetSetting("app_platform_controller_epoch", "")
	platformSimulateRestore(t, f.s)
	if f.s.st.GetSetting("app_platform_controller_epoch", "") == before {
		t.Fatal("fixture: the second restore did not change the controller epoch")
	}
	if _, replayed, err := f.s.confirmPlatformAbandonRuntime(context.Background(), f.actor, f.replacement.id, digest); replayed || !errors.Is(err, errAppPlatformAbandonUnavailable) {
		t.Fatalf("a confirmation from another epoch was answered from the earlier record: replayed=%v %v", replayed, err)
	}
}
