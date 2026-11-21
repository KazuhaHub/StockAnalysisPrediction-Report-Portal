package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/transport"
)

// Fails if the console plans a runner-backed install in a format the operation engine does not run, or plans
// one at all on a runner without the writable sandbox (nothing would ever execute it).
func TestRunnerInstallFormatFollowsTheWritableSandbox(t *testing.T) {
	profile := transport.ManagedRuntimeProfile{SandboxProfile: control.CandidateSandboxProfile}
	if _, err := platformRunnerInstallFormat(profile); err == nil {
		t.Fatal("a runner without the writable sandbox got an install format")
	}
	profile.WritableSandboxProfile = control.WritableCandidateSandboxProfile
	if format, err := platformRunnerInstallFormat(profile); err != nil || format != platformWritableInstallFormat {
		t.Fatal(format, err)
	}
}

// Fails if a queued managed-format install (no executor runs that format) stays queued with no trace instead
// of being closed for review, or if closing it touches a writable install.
func TestOperationEngineClosesAnUnexecutableManagedFormatInstall(t *testing.T) {
	eachPlatformDriver(t, func(t *testing.T, pg bool) {
		s, lease := platformCandidateIntentFixture(t, pg)
		mustExec(t, s.st, `DELETE FROM app_platform_operation_effects WHERE operation_id=?`, lease.id)
		mustExec(t, s.st, `DELETE FROM app_platform_data_namespaces WHERE owner_installation_id=?`, lease.installationID)
		mustExec(t, s.st, `UPDATE app_platform_installations SET revision=1,control_fence=0 WHERE id=?`, lease.installationID)
		mustExec(t, s.st, `UPDATE app_platform_operations SET stage='queued',state_revision=1,row_revision=1,fence=0,lease_owner=NULL,lease_until_ms=NULL WHERE id=?`, lease.id)
		if profile := scalar[string](t, s.st, `SELECT substr(p.plan_json,1,60) FROM app_platform_review_plans p JOIN app_platform_operations o ON o.review_plan_id=p.id WHERE o.id=?`, lease.id); profile[:len(`{"format":"`+platformManagedInstallFormat)] != `{"format":"`+platformManagedInstallFormat {
			t.Fatal("fixture is not a managed-format plan", profile)
		}
		selected, err := s.claimNextPlatformWritableInstall(context.Background(), platformFixtureID(951))
		if err != nil || !selected.reviewed || selected.operationID != lease.id || selected.lease.id != "" {
			t.Fatal("the unexecutable install was not closed for review", selected, err)
		}
		if scalar[string](t, s.st, `SELECT error_code FROM app_platform_operations WHERE id=?`, lease.id) != "install_format_unsupported" ||
			scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_operations WHERE id=? AND completed_ms IS NOT NULL`, lease.id) != 1 {
			t.Fatal("closed install is not visible as a failed operation")
		}
		// It had no effects, so it is removed outright: nothing is left that blocks planning the app again.
		if scalar[string](t, s.st, `SELECT desired_state||'/'||phase FROM app_platform_installations WHERE id=?`, lease.installationID) != "removed/removed" ||
			scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_installations WHERE app_id=(SELECT app_id FROM app_platform_installations WHERE id=?) AND desired_state<>'removed'`, lease.installationID) != 0 {
			t.Fatal("the closed install still blocks a new install of the app")
		}
		again, err := s.claimNextPlatformWritableInstall(context.Background(), platformFixtureID(952))
		if err != nil || again.reviewed || again.lease.id != "" {
			t.Fatal("a closed install was selected again", again, err)
		}
	})
}

// Fails if the console's own routes plan a runner-backed install the operation engine does not run (the
// v2026.47.6 bug), or plan one on a runner without the writable sandbox. This is the route-level guard that
// runs in every CI: the runner's observation is the only thing faked.
func TestConsolePlansARunnerInstallTheEngineClaims(t *testing.T) {
	platformWritableUpdateFixtures(t)
	h, f, _ := platformReviewHTTPFor(t, false, true, nil)
	s := f.server
	raw, archive := platformManagedReleaseFixture(t, f.target.Profile.Platform)
	var manifest appplatform.Manifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	manifest.ID, manifest.Publisher = "notes-console-install", f.sourcePlan.PublisherID
	raw, _ = json.Marshal(manifest)
	release, err := s.stagePlatformRelease(f.actor, raw, platformSignTestDescriptor(t, raw), archive)
	if err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf(`{"action":"install","release_sha256":%q,"trust_revision":%q,"audience":[{"type":"user","username":%q}]}`, release.sha256, strconv.FormatInt(release.trustRevision, 10), f.actor.username)

	writable := *f.target
	writable.Profile.WritableSandboxProfile = control.WritableCandidateSandboxProfile
	readonly := writable
	readonly.Profile.WritableSandboxProfile = ""
	observe := func(profile transport.ManagedRuntimeProfile) {
		s.platformReadManagedRuntime = func(_ context.Context, e platformRuntimeEndpoint, a control.Authority) (platformManagedRuntimeObserved, error) {
			return platformFakeManagedRuntime{profile: profile, authority: a, at: time.Now(), endpoint: transport.CandidateReceiptEndpoint{Path: e.path, ControllerUID: e.controllerUID, ControllerGID: e.controllerGID, RootController: true}}, nil
		}
	}

	observe(readonly.Profile)
	if got := h.post("/api/admin/app-platform/plans", body); got.Code != http.StatusConflict || !strings.Contains(got.Body.String(), "app_platform_requirements_unavailable") {
		t.Fatal("a runner without the writable sandbox got an install plan", got.Code, got.Body.String())
	}

	observe(writable.Profile)
	review := h.json(h.post("/api/admin/app-platform/plans", body), http.StatusOK)
	plan := h.str(review, "plan_id")
	if format, err := platformParseInstallProfile(scalar[string](t, s.st, `SELECT plan_json FROM app_platform_review_plans WHERE id=?`, plan)); err != nil || format != platformWritableInstallFormat {
		t.Fatal("console planned a format the engine does not run", format, err)
	}
	committed := h.json(h.post("/api/admin/app-platform/operations", fmt.Sprintf(`{"plan_id":%q,"plan_sha256":%q,"idempotency_key":%q}`, plan, h.str(review, "plan_sha256"), platformFixtureID(961))), http.StatusAccepted)
	selected, err := s.claimNextPlatformWritableInstall(context.Background(), platformFixtureID(962))
	if err != nil || selected.lease.id != h.str(committed, "operation_id") {
		t.Fatal("the engine did not claim the console's install", selected, err)
	}
}
