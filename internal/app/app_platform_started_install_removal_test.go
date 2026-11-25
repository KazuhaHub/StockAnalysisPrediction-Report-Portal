package app

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

// newStartedFailedInstall is a writable managed install whose candidate container started and whose readiness probe
// then failed: the start effect is applied with its running receipt, and the install ended recovery_required with
// the generic execution-review code. The candidate is still running on the runner.
func newStartedFailedInstall(t *testing.T, pg bool) refusedInstall {
	t.Helper()
	s, lease, _ := platformWritableProbeFixture(t, pg)
	if err := s.finishPlatformInstallForReview(context.Background(), lease); err != nil {
		t.Fatal(err)
	}
	f := refusedInstall{s: s, actor: platformManagedFixtureSession(t, s), installation: lease.installationID, install: lease.id}
	f.startEffect = scalar[string](t, s.st, `SELECT effect_id FROM app_platform_operation_effects WHERE operation_id=? AND kind='start-candidate'`, lease.id)
	if got := scalar[string](t, s.st, `SELECT stage||':'||error_code FROM app_platform_operations WHERE id=?`, lease.id); got != "recovery_required:install_execution_review_required" {
		t.Fatalf("the install ended %s", got)
	}
	if got := scalar[string](t, s.st, `SELECT state FROM app_platform_operation_effects WHERE effect_id=?`, f.startEffect); got != "applied" {
		t.Fatalf("the start is %s, not applied", got)
	}
	return f
}

// walkStartedRemoval runs an admitted started-install uninstall through stop, remove_code and remove_data with the
// exact runner observations. It returns the removal operation and the deleted namespace.
func (f refusedInstall) walkStartedRemoval(t *testing.T, base int) (string, string) {
	t.Helper()
	_, commit := f.uninstall(t, platformFixtureID(base))
	lease := f.claimAtStop(t, platformFixtureID(base+1))
	endpoint := platformManagedRemovalFixtureEndpoint()
	for _, stage := range []appplatform.Stage{appplatform.StageStop, appplatform.StageRemoveCode} {
		intent, err := f.s.preparePlatformManagedRemovalIntent(context.Background(), lease)
		if err != nil {
			t.Fatalf("the %s intent was refused: %v", stage, err)
		}
		if intent.command.Stage != string(stage) || intent.sourceOperationID != f.install || intent.command.Payload.SourceEffectID != f.startEffect {
			t.Fatalf("the %s intent is not bound to the failed install's start: %+v", stage, intent.command)
		}
		receipt, ok := validPlatformManagedRemovalObservation(platformManagedRemovalObservationFixture(intent, endpoint), intent, endpoint)
		if !ok {
			t.Fatalf("the exact %s observation was refused", stage)
		}
		if lease, err = f.s.settlePlatformManagedRemoval(context.Background(), lease, intent, receipt); err != nil {
			t.Fatal(err)
		}
	}
	if lease.snapshot.Stage != appplatform.StageRemoveData {
		t.Fatalf("the removal is at %s", lease.snapshot.Stage)
	}
	intent, err := f.s.preparePlatformDeleteDataIntent(context.Background(), lease)
	if err != nil {
		t.Fatalf("the delete-data intent was refused: %v", err)
	}
	receipt, ok := validPlatformDeleteDataObservation(platformDeleteDataObservationFixture(intent, endpoint), intent, endpoint)
	if !ok {
		t.Fatal("the exact delete-data observation was refused")
	}
	if _, err := f.s.settlePlatformDeleteData(context.Background(), lease, intent, receipt); err != nil {
		t.Fatal(err)
	}
	return commit.OperationID, intent.command.Payload.NamespaceID
}

func testStartedFailedInstallIsUninstalled(t *testing.T, pg bool) {
	f := newStartedFailedInstall(t, pg)
	plan, err := f.review(appplatform.ActionUninstall)
	if err != nil {
		t.Fatalf("the started-failed install cannot be uninstalled: %v", err)
	}
	if !strings.Contains(plan.JSON, `"start_effect_id":"`+f.startEffect+`"`) {
		t.Fatalf("the review does not bind the applied start: %s", plan.JSON)
	}
	removal, namespace := f.walkStartedRemoval(t, 7400)
	if f.effects(t, removal, `stage='stop' AND kind='stop' AND state='applied'`) != 1 ||
		f.effects(t, removal, `stage='remove_code' AND kind='remove-code' AND state='applied'`) != 1 ||
		f.effects(t, removal, `stage='remove_data' AND kind='delete-data' AND state='applied'`) != 1 {
		t.Fatal("the removal did not stop and remove the candidate on the runner before deleting data")
	}
	for _, check := range []struct{ query, arg, want string }{
		{`SELECT desired_state||':'||phase FROM app_platform_installations WHERE id=?`, f.installation, "removed:removed"},
		{`SELECT state||':'||COALESCE(owner_installation_id,'') FROM app_platform_data_namespaces WHERE id=?`, namespace, "deleted:"},
		{`SELECT stage||':'||COALESCE(error_code,'') FROM app_platform_operations WHERE id=?`, removal, "complete:"},
		{`SELECT stage||':'||error_code FROM app_platform_operations WHERE id=?`, f.install, "failed:install_execution_review_required"},
	} {
		if got := scalar[string](t, f.s.st, check.query, check.arg); got != check.want {
			t.Fatalf("%q = %q, want %q", check.query, got, check.want)
		}
	}
	app := scalar[string](t, f.s.st, `SELECT app_id FROM app_platform_installations WHERE id=?`, f.installation)
	if err := platformNoPriorInstall(migExec{s: f.s.st, ex: f.s.st.db}, app); err != nil {
		t.Fatalf("the application stayed occupied after the removal: %v", err)
	}
}

func TestStartedFailedInstallIsUninstalledSQLite(t *testing.T) {
	t.Parallel()
	testStartedFailedInstallIsUninstalled(t, false)
}
func TestStartedFailedInstallIsUninstalledPostgres(t *testing.T) {
	t.Parallel()
	testStartedFailedInstallIsUninstalled(t, true)
}

// The scheduler's loop must take the ordinary stop path for a started install: it prepares a stop intent (and so
// would send the runner command) instead of skipping to delete-data.
func TestStartedFailedInstallRemovalLoopStopsTheCandidate(t *testing.T) {
	t.Parallel()
	f := newStartedFailedInstall(t, false)
	_, commit := f.uninstall(t, platformFixtureID(7410))
	selected, err := f.s.claimNextPlatformManagedUninstall(context.Background(), platformFixtureID(7411))
	if err != nil || selected.lease.id != commit.OperationID {
		t.Fatal("not claimed", selected, err)
	}
	run, err := f.s.runPlatformManagedUninstall(context.Background(), selected.lease, platformManagedRemovalFixtureEndpoint())
	if err == nil {
		t.Fatal("the unreachable fixture endpoint unexpectedly completed the removal")
	}
	if run.lease.snapshot.Stage != appplatform.StageStop || f.effects(t, commit.OperationID, `stage='stop' AND kind='stop' AND state='prepared'`) != 1 {
		t.Fatalf("the loop did not prepare the candidate stop at %s: %v", run.lease.snapshot.Stage, err)
	}
	// The never-started skip refuses to run for a started install.
	if _, err := f.s.advancePlatformNeverStartedRemoval(context.Background(), run.lease); err == nil {
		t.Fatal("the never-started skip passed a started install's stop stage")
	}
}

func TestStartedFailedInstallEvidenceFailsClosed(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, statement string }{
		{"start_prepared", `UPDATE app_platform_operation_effects SET state='prepared' WHERE kind='start-candidate'`},
		{"start_unknown", `UPDATE app_platform_operation_effects SET state='unknown' WHERE kind='start-candidate'`},
		{"start_without_running_receipt", `UPDATE app_platform_operation_effects SET receipt_json='{}' WHERE kind='start-candidate'`},
		{"start_observed_at_another_fence", `UPDATE app_platform_operation_effects SET observed_fence=observed_fence+1 WHERE kind='start-candidate'`},
		{"prepare_data_unknown", `UPDATE app_platform_operation_effects SET state='unknown' WHERE kind='prepare-data'`},
		{"prepare_data_prepared", `UPDATE app_platform_operation_effects SET state='prepared' WHERE kind='prepare-data'`},
		{"effect_past_start", `UPDATE app_platform_operation_effects SET stage='activate' WHERE kind='activate'`},
		{"active_activation", `UPDATE app_platform_activations SET state='active'`},
		{"second_generation", `UPDATE app_platform_installations SET generation=2`},
		{"in_flight_lease", `UPDATE app_platform_operations SET stage='probe',error_code=NULL,completed_ms=NULL,lease_owner='` + platformFixtureID(7420) + `',lease_until_ms=99999999999999 WHERE action='install'`},
		{"install_without_error", `UPDATE app_platform_operations SET error_code=NULL WHERE action='install'`},
		{"install_not_recovery_required", `UPDATE app_platform_operations SET stage='complete',error_code=NULL WHERE action='install'`},
		{"namespace_not_active", `UPDATE app_platform_data_namespaces SET state='review_required'`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newStartedFailedInstall(t, false)
			if _, err := f.review(appplatform.ActionUninstall); err != nil {
				t.Fatalf("the unmodified install was refused: %v", err)
			}
			mustExec(t, f.s.st, tc.statement)
			if _, err := f.review(appplatform.ActionUninstall); !errors.Is(err, errAppPlatformPlanChanged) {
				t.Fatalf("the review was admitted or failed otherwise: %v", err)
			}
		})
	}
	t.Run("second_operation", func(t *testing.T) {
		f := newStartedFailedInstall(t, false)
		mustExec(t, f.s.st, `INSERT INTO app_platform_review_plans(id,installation_id,app_id,publisher_id,action,source_release_sha256,target_release_sha256,installation_revision,policy_revision,trust_revision,controller_epoch,actor_scope,username,account_created,account_generation,authority_fingerprint,plan_sha256,plan_json,expires_ms,consumed_ms,created_ms,updated_ms)
			SELECT ?,installation_id,app_id,publisher_id,action,source_release_sha256,target_release_sha256,installation_revision,policy_revision,trust_revision,controller_epoch,actor_scope,username,account_created,account_generation,authority_fingerprint,plan_sha256,plan_json,expires_ms,consumed_ms,created_ms,updated_ms
			FROM app_platform_review_plans WHERE id=(SELECT review_plan_id FROM app_platform_operations WHERE action='install')`, platformFixtureID(7431))
		mustExec(t, f.s.st, `INSERT INTO app_platform_operations(id,installation_id,review_plan_id,actor_scope,idempotency_sha256,request_sha256,action,managed,source_generation,target_generation,stage,state_revision,row_revision,fence,controller_epoch,created_ms,updated_ms,completed_ms,error_code)
			SELECT ?,installation_id,?,actor_scope,?,?,action,managed,source_generation,target_generation,stage,state_revision,row_revision,fence,controller_epoch,created_ms+1,updated_ms,completed_ms,error_code FROM app_platform_operations WHERE action='install'`,
			platformFixtureID(7430), platformFixtureID(7431), strings.Repeat("1", 64), strings.Repeat("2", 64))
		if _, err := f.review(appplatform.ActionUninstall); err == nil {
			t.Fatal("an installation with a later operation was admitted")
		}
	})
}

// A start that changes after review is refused at the stop stage by the same evidence comparison.
func TestStartedFailedInstallRemovalRefusesAStartChangedAfterReview(t *testing.T) {
	t.Parallel()
	f := newStartedFailedInstall(t, false)
	f.uninstall(t, platformFixtureID(7440))
	lease := f.claimAtStop(t, platformFixtureID(7441))
	mustExec(t, f.s.st, `UPDATE app_platform_operation_effects SET receipt_json=receipt_json||' ' WHERE kind='start-candidate'`)
	if _, err := f.s.preparePlatformManagedRemovalIntent(context.Background(), lease); err == nil {
		t.Fatal("the stop was prepared over a changed start receipt")
	}
	if f.effects(t, lease.id, `1=1`) != 0 {
		t.Fatal("a stop effect was written over changed evidence")
	}
}

func TestStartedFailedInstallCanBeInstalledAgainAfterItsRemoval(t *testing.T) {
	t.Parallel()
	f := newStartedFailedInstall(t, false)
	app := scalar[string](t, f.s.st, `SELECT app_id FROM app_platform_installations WHERE id=?`, f.installation)
	if err := platformNoPriorInstall(migExec{s: f.s.st, ex: f.s.st.db}, app); err == nil {
		t.Fatal("the application was free while its started-failed install still existed")
	}
	f.walkStartedRemoval(t, 7450)
	if err := platformNoPriorInstall(migExec{s: f.s.st, ex: f.s.st.db}, app); err != nil {
		t.Fatalf("the application stayed occupied: %v", err)
	}
}
