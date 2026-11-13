package app

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

// refusedInstall is a writable managed install whose start the runner refused for capacity. The data stage and
// the writer ran, so the install owns an active namespace (and the runner holds its disk reservation), and the
// install operation ended with the typed capacity code: nothing ever started.
type refusedInstall struct {
	s            *Server
	actor        platformActorSession
	installation string
	install      string
	startEffect  string
}

func newRefusedInstall(t *testing.T, pg bool) refusedInstall {
	t.Helper()
	s, lease, _ := platformWritableStartFixture(t, pg)
	start, err := s.preparePlatformWritableStart(context.Background(), lease)
	if err != nil {
		t.Fatal(err)
	}
	refusal := capacityRefusalFixture("memory")
	refusal.EffectID, refusal.PayloadSHA256 = start.command.EffectID, start.command.PayloadSHA256
	if err := s.finishPlatformInstallWithCode(withPlatformRefusedEffect(context.Background(), refusal), lease, "capacity_exhausted_memory"); err != nil {
		t.Fatal(err)
	}
	f := refusedInstall{s: s, actor: platformManagedFixtureSession(t, s), installation: lease.installationID, install: lease.id, startEffect: start.command.EffectID}
	if got := scalar[string](t, s.st, `SELECT state FROM app_platform_operation_effects WHERE effect_id=?`, f.startEffect); got != "reverted" {
		t.Fatalf("the refused start is %s, not reverted", got)
	}
	return f
}

func (f refusedInstall) review(action appplatform.Action) (platformRemovalReview, error) {
	return f.s.createPlatformRemovalReview(f.actor, f.installation, action)
}

func (f refusedInstall) uninstall(t *testing.T, key string) (platformRemovalReview, platformRemovalCommit) {
	t.Helper()
	plan, err := f.review(appplatform.ActionUninstall)
	if err != nil {
		t.Fatalf("the refused install cannot be uninstalled: %v", err)
	}
	commit, err := f.s.consumePlatformRemovalReview(f.actor, plan.ID, plan.SHA256, key)
	if err != nil {
		t.Fatal(err)
	}
	return plan, commit
}

// claim leases the uninstall and walks it to the stop stage the way the scheduler does.
func (f refusedInstall) claimAtStop(t *testing.T, worker string) platformOperationLease {
	t.Helper()
	selected, err := f.s.claimNextPlatformManagedUninstall(context.Background(), worker)
	if err != nil || selected.lease.id == "" {
		t.Fatal("the uninstall of the refused install was not claimed", selected, err)
	}
	current := selected.lease
	for range 2 {
		if current, err = f.s.st.advancePlatformRemovalControl(current); err != nil {
			t.Fatal(err)
		}
	}
	current, err = f.s.advancePlatformManagedRemovalStage(context.Background(), current, appplatform.StageStop)
	if err != nil || current.snapshot.Stage != appplatform.StageStop {
		t.Fatal("the uninstall did not enter stop", current.snapshot, err)
	}
	return current
}

func (f refusedInstall) effects(t *testing.T, operation, where string) int {
	t.Helper()
	return scalar[int](t, f.s.st, `SELECT COUNT(*) FROM app_platform_operation_effects WHERE operation_id=? AND `+where, operation)
}

// Refused for capacity, then uninstalled through the review and consumption API, the install ends removed: the
// stop and remove-code stages run no runner effect, the signed delete-data command is bound to the failed
// install's applied prepare_data effect, and the namespace is gone. Fails while the review is refused as a changed
// plan, which is what leaves the runner's disk reservation held for good.
func testRefusedInstallIsUninstalled(t *testing.T, pg bool) {
	f := newRefusedInstall(t, pg)
	namespace := scalar[string](t, f.s.st, `SELECT id FROM app_platform_data_namespaces WHERE owner_installation_id=?`, f.installation)
	if scalar[string](t, f.s.st, `SELECT state FROM app_platform_data_namespaces WHERE id=?`, namespace) != "active" {
		t.Fatal("the refused install does not own an active namespace")
	}
	key := platformFixtureID(7100)
	plan, commit := f.uninstall(t, key)
	var projection platformRemovalProjection
	if err := json.Unmarshal([]byte(plan.JSON), &projection); err != nil || !strings.Contains(plan.JSON, `"never_started"`) {
		t.Fatalf("the review does not name the never-started variant: %s", plan.JSON)
	}
	if commit.Replayed || commit.Generation != 2 || commit.Stage != appplatform.StageQueued {
		t.Fatalf("invalid commit: %+v", commit)
	}
	// Replay: the same key returns the same operation and admits no second one.
	replay, err := f.s.consumePlatformRemovalReview(f.actor, plan.ID, plan.SHA256, key)
	if err != nil || !replay.Replayed || replay.OperationID != commit.OperationID ||
		scalar[int](t, f.s.st, `SELECT COUNT(*) FROM app_platform_operations WHERE installation_id=?`, f.installation) != 2 {
		t.Fatal("replay admitted a second uninstall", replay, err)
	}
	if got := scalar[string](t, f.s.st, `SELECT desired_state||':'||phase FROM app_platform_installations WHERE id=?`, f.installation); got != "removed:pending" {
		t.Fatalf("admission left the installation %s", got)
	}
	if scalar[int](t, f.s.st, `SELECT COUNT(*) FROM app_platform_activations WHERE installation_id=? AND state IN ('active','candidate')`, f.installation) != 0 {
		t.Fatal("admission left a live activation of an installation that never ran")
	}

	lease := f.claimAtStop(t, platformFixtureID(7101))
	for _, want := range []appplatform.Stage{appplatform.StageRemoveCode, appplatform.StageRemoveData} {
		if lease, err = f.s.advancePlatformNeverStartedRemoval(context.Background(), lease); err != nil || lease.snapshot.Stage != want {
			t.Fatalf("the never-started removal did not skip to %s: %v", want, err)
		}
	}
	if f.effects(t, lease.id, `stage IN ('stop','remove_code')`) != 0 {
		t.Fatal("a stop or remove-code effect was written for code that never ran")
	}

	intent, err := f.s.preparePlatformDeleteDataIntent(context.Background(), lease)
	if err != nil {
		t.Fatalf("the delete-data intent was refused: %v", err)
	}
	if intent.command.Payload.NamespaceID != namespace || intent.command.Payload.WriterFence != "2" ||
		intent.command.Stage != "remove_data" || intent.command.Kind != "delete-data" || intent.command.OperationID != lease.id ||
		intent.sourceOperationID != f.install || !platformValidSHA(intent.dataReceiptSHA256) ||
		f.effects(t, lease.id, `stage='remove_data' AND kind='delete-data' AND state='prepared'`) != 1 {
		t.Fatalf("invalid delete-data intent: %+v", intent.command)
	}
	// The binding is to the failed install's applied prepare_data effect, byte for byte.
	if intent.dataReceiptSHA256 != platformDigest([]byte(scalar[string](t, f.s.st, `SELECT receipt_json FROM app_platform_operation_effects WHERE operation_id=? AND stage='prepare_data' AND effect_number=1`, f.install))) {
		t.Fatal("the delete-data command is not bound to the install's preparation receipt")
	}
	endpoint := platformManagedRemovalFixtureEndpoint()
	receipt, ok := validPlatformDeleteDataObservation(platformDeleteDataObservationFixture(intent, endpoint), intent, endpoint)
	if !ok {
		t.Fatal("the exact delete-data observation was refused")
	}
	completed, err := f.s.settlePlatformDeleteData(context.Background(), lease, intent, receipt)
	if err != nil || completed.snapshot.Stage != appplatform.StageComplete {
		t.Fatal("the delete-data settlement failed", completed.snapshot, err)
	}
	for _, check := range []struct {
		name, query string
		want        any
	}{
		{"installation", `SELECT desired_state||':'||phase FROM app_platform_installations WHERE id=?`, "removed:removed"},
		{"namespace", `SELECT state||':'||COALESCE(owner_installation_id,'') FROM app_platform_data_namespaces WHERE id=?`, "deleted:"},
		{"operation", `SELECT stage||':'||COALESCE(error_code,'') FROM app_platform_operations WHERE id=?`, "complete:"},
	} {
		arg := f.installation
		switch check.name {
		case "namespace":
			arg = namespace
		case "operation":
			arg = lease.id
		}
		if got := scalar[string](t, f.s.st, check.query, arg); got != check.want {
			t.Fatalf("%s = %q, want %q", check.name, got, check.want)
		}
	}
	if f.effects(t, lease.id, `kind='delete-data' AND state='applied'`) != 1 || f.effects(t, lease.id, `1=1`) != 1 {
		t.Fatal("the uninstall must hold exactly the applied delete-data effect")
	}
	if scalar[int](t, f.s.st, `SELECT COUNT(*) FROM app_platform_access_grants WHERE installation_id=?`, f.installation) != 0 ||
		scalar[int](t, f.s.st, `SELECT COUNT(*) FROM app_platform_capability_grants WHERE installation_id=?`, f.installation) != 0 {
		t.Fatal("authority outlived the removal")
	}
	// The failed install's own history is untouched evidence.
	if scalar[string](t, f.s.st, `SELECT stage||':'||error_code FROM app_platform_operations WHERE id=?`, f.install) != "recovery_required:capacity_exhausted_memory" {
		t.Fatal("the removal rewrote the failed install operation")
	}
}

func TestRefusedInstallIsUninstalledSQLite(t *testing.T)   { testRefusedInstallIsUninstalled(t, false) }
func TestRefusedInstallIsUninstalledPostgres(t *testing.T) { testRefusedInstallIsUninstalled(t, true) }

// The scheduler's own loop takes the never-started path: it passes stop and remove_code without a runner call and
// stops at the delete-data effect. Fails if the loop still prepares a stop intent for code that never ran.
func TestRefusedInstallRemovalLoopRunsNoStopOrRemoveCode(t *testing.T) {
	f := newRefusedInstall(t, false)
	_, commit := f.uninstall(t, platformFixtureID(7110))
	selected, err := f.s.claimNextPlatformManagedUninstall(context.Background(), platformFixtureID(7111))
	if err != nil || selected.lease.id != commit.OperationID {
		t.Fatal("not claimed", selected, err)
	}
	run, err := f.s.runPlatformManagedUninstall(context.Background(), selected.lease, platformManagedRemovalFixtureEndpoint())
	if err == nil {
		t.Fatal("the unreachable fixture endpoint unexpectedly completed the removal")
	}
	if run.lease.snapshot.Stage != appplatform.StageRemoveData || f.effects(t, commit.OperationID, `stage IN ('stop','remove_code')`) != 0 ||
		f.effects(t, commit.OperationID, `stage='remove_data' AND kind='delete-data'`) != 1 {
		t.Fatalf("the loop stopped at %s with stop/remove-code effects or without the delete-data intent: %v", run.lease.snapshot.Stage, err)
	}
}

// An install whose start is anything other than definitively reverted is not a never-started install. It stays
// refused with the existing typed error until the start is settled; settling it (to reverted) is what opens the
// variant. The real orphan settlement does exactly that for a start the runner proves never ran.
func TestRefusedInstallWithAnUnsettledStartIsNotRemovable(t *testing.T) {
	for _, state := range []string{"prepared", "unknown", "applied"} {
		t.Run(state, func(t *testing.T) {
			f := newRefusedInstall(t, false)
			mustExec(t, f.s.st, `UPDATE app_platform_operation_effects SET state=? WHERE effect_id=?`, state, f.startEffect)
			if _, err := f.review(appplatform.ActionUninstall); !errors.Is(err, errAppPlatformPlanChanged) {
				t.Fatalf("a %s start was accepted: %v", state, err)
			}
			mustExec(t, f.s.st, `UPDATE app_platform_operation_effects SET state='reverted' WHERE effect_id=?`, f.startEffect)
			if _, err := f.review(appplatform.ActionUninstall); err != nil {
				t.Fatalf("a reverted start was refused: %v", err)
			}
		})
	}
}

func TestOrphanSettledInstallBecomesRemovable(t *testing.T) {
	o := newOrphanFixture(t, true)
	mustExec(t, o.s.st, `UPDATE app_platform_operation_effects SET state='unknown' WHERE effect_id=?`, o.effect)
	if err := o.w.finishOrphan(context.Background(), o.receipt()); err != nil {
		t.Fatal(err)
	}
	f := refusedInstall{s: o.s, actor: platformManagedFixtureSession(t, o.s), installation: o.w.active.installationID}
	f.installation = scalar[string](t, o.s.st, `SELECT installation_id FROM app_platform_operations WHERE id=?`, o.op)
	if _, err := f.review(appplatform.ActionUninstall); err != nil {
		t.Fatalf("an install settled by the orphan settlement stayed unremovable: %v", err)
	}
}

// Every condition of the eligibility fails closed on its own.
func TestNeverStartedRemovalFailsClosed(t *testing.T) {
	for _, tc := range []struct{ name, statement string }{
		{"install_not_failed", `UPDATE app_platform_operations SET error_code=NULL WHERE action='install'`},
		{"install_complete", `UPDATE app_platform_operations SET stage='complete',error_code=NULL WHERE action='install'`},
		{"prepare_data_not_applied", `UPDATE app_platform_operation_effects SET state='reverted' WHERE kind='prepare-data'`},
		{"prepare_data_unknown", `UPDATE app_platform_operation_effects SET state='unknown' WHERE kind='prepare-data'`},
		{"another_effect_unsettled", `UPDATE app_platform_operation_effects SET state='prepared' WHERE kind='activate'`},
		{"probe_effect_present", `UPDATE app_platform_operation_effects SET stage='probe' WHERE kind='activate'`},
		{"namespace_not_active", `UPDATE app_platform_data_namespaces SET state='review_required'`},
		{"namespace_retained", `UPDATE app_platform_data_namespaces SET state='retained'`},
		{"active_activation", `UPDATE app_platform_activations SET state='active'`},
		{"not_the_first_generation", `UPDATE app_platform_installations SET generation=2`},
		{"enabled_removed", `UPDATE app_platform_installations SET desired_state='disabled'`},
		{"not_managed", `UPDATE app_platform_installations SET runtime_id=NULL`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newRefusedInstall(t, false)
			mustExec(t, f.s.st, tc.statement)
			if _, err := f.review(appplatform.ActionUninstall); err == nil {
				t.Fatal("the review was admitted")
			}
		})
	}
	t.Run("another_operation", func(t *testing.T) {
		f := newRefusedInstall(t, false)
		mustExec(t, f.s.st, `INSERT INTO app_platform_review_plans(id,installation_id,app_id,publisher_id,action,source_release_sha256,target_release_sha256,installation_revision,policy_revision,trust_revision,controller_epoch,actor_scope,username,account_created,account_generation,authority_fingerprint,plan_sha256,plan_json,expires_ms,consumed_ms,created_ms,updated_ms)
			SELECT ?,installation_id,app_id,publisher_id,action,source_release_sha256,target_release_sha256,installation_revision,policy_revision,trust_revision,controller_epoch,actor_scope,username,account_created,account_generation,authority_fingerprint,plan_sha256,plan_json,expires_ms,consumed_ms,created_ms,updated_ms
			FROM app_platform_review_plans WHERE id=(SELECT review_plan_id FROM app_platform_operations WHERE action='install')`, platformFixtureID(7191))
		mustExec(t, f.s.st, `INSERT INTO app_platform_operations(id,installation_id,review_plan_id,actor_scope,idempotency_sha256,request_sha256,action,managed,source_generation,target_generation,stage,state_revision,row_revision,fence,controller_epoch,created_ms,updated_ms,completed_ms,error_code)
			SELECT ?,installation_id,?,actor_scope,?,?,action,managed,source_generation,target_generation,stage,state_revision,row_revision,fence,controller_epoch,created_ms+1,updated_ms,completed_ms,error_code FROM app_platform_operations WHERE action='install'`,
			platformFixtureID(7190), platformFixtureID(7191), strings.Repeat("1", 64), strings.Repeat("2", 64))
		if _, err := f.review(appplatform.ActionUninstall); err == nil {
			t.Fatal("an installation with a later operation was admitted")
		}
	})
	t.Run("disable_is_never_the_variant", func(t *testing.T) {
		f := newRefusedInstall(t, false)
		if _, err := f.review(appplatform.ActionDisable); !errors.Is(err, errAppPlatformPlanChanged) {
			t.Fatalf("disable of a refused install: %v", err)
		}
	})
}

// A started install, whatever phase it is pushed into, never takes the variant, and the ordinary review carries no
// trace of it (its JSON is the one that was reviewed before this change).
func TestStartedInstallKeepsTheOrdinaryPath(t *testing.T) {
	s, actor, lease := platformWritableAccessFixture(t, false)
	plan, err := s.createPlatformRemovalReview(actor, lease.installationID, appplatform.ActionUninstall)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(plan.JSON, "never_started") {
		t.Fatalf("the ordinary review names the variant: %s", plan.JSON)
	}
	for _, phase := range []string{"review_required", "recovery_required"} {
		mustExec(t, s.st, `UPDATE app_platform_installations SET phase=? WHERE id=?`, phase, lease.installationID)
		if _, err := s.createPlatformRemovalReview(actor, lease.installationID, appplatform.ActionUninstall); !errors.Is(err, errAppPlatformPlanChanged) {
			t.Fatalf("a started install in %s was admitted: %v", phase, err)
		}
	}
}

// Settlement is atomic: a failure after the runner proved the namespace gone rolls everything back, and the same
// receipt settles on retry. The prepared-effect crash fails closed to recovery without a second runner command.
func testRefusedInstallRemovalIsCrashSafe(t *testing.T, pg bool) {
	f := newRefusedInstall(t, pg)
	_, commit := f.uninstall(t, platformFixtureID(7120))
	lease := f.claimAtStop(t, platformFixtureID(7121))
	var err error
	for range 2 {
		if lease, err = f.s.advancePlatformNeverStartedRemoval(context.Background(), lease); err != nil {
			t.Fatal(err)
		}
	}
	intent, err := f.s.preparePlatformDeleteDataIntent(context.Background(), lease)
	if err != nil {
		t.Fatal(err)
	}
	endpoint := platformManagedRemovalFixtureEndpoint()
	receipt, ok := validPlatformDeleteDataObservation(platformDeleteDataObservationFixture(intent, endpoint), intent, endpoint)
	if !ok {
		t.Fatal("observation refused")
	}
	if pg {
		mustExec(t, f.s.st, `CREATE FUNCTION fixture_refused_removal_failure() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'fixture refused removal failure'; END $$`)
		mustExec(t, f.s.st, `CREATE TRIGGER fixture_refused_removal_failure BEFORE INSERT ON audit_log FOR EACH ROW WHEN (NEW.action='app_platform.data_deleted' AND NEW.target_id='`+commit.OperationID+`') EXECUTE FUNCTION fixture_refused_removal_failure()`)
		t.Cleanup(func() {
			mustExec(t, f.s.st, `DROP TRIGGER IF EXISTS fixture_refused_removal_failure ON audit_log`)
			mustExec(t, f.s.st, `DROP FUNCTION IF EXISTS fixture_refused_removal_failure()`)
		})
	} else {
		mustExec(t, f.s.st, `CREATE TRIGGER fixture_refused_removal_failure BEFORE INSERT ON audit_log WHEN NEW.action='app_platform.data_deleted' AND NEW.target_id='`+commit.OperationID+`' BEGIN SELECT RAISE(ABORT,'fixture refused removal failure'); END`)
		t.Cleanup(func() { mustExec(t, f.s.st, `DROP TRIGGER IF EXISTS fixture_refused_removal_failure`) })
	}
	if _, err := f.s.settlePlatformDeleteData(context.Background(), lease, intent, receipt); err == nil {
		t.Fatal("the removal completed without its audit entry")
	}
	if scalar[string](t, f.s.st, `SELECT stage FROM app_platform_operations WHERE id=?`, lease.id) != "remove_data" ||
		scalar[string](t, f.s.st, `SELECT state FROM app_platform_data_namespaces WHERE origin_installation_id=?`, f.installation) != "active" ||
		scalar[string](t, f.s.st, `SELECT desired_state||':'||phase FROM app_platform_installations WHERE id=?`, f.installation) != "removed:pending" ||
		f.effects(t, lease.id, `kind='delete-data' AND state='prepared'`) != 1 {
		t.Fatal("a failed settlement was partly applied")
	}
	if pg {
		mustExec(t, f.s.st, `DROP TRIGGER fixture_refused_removal_failure ON audit_log`)
		mustExec(t, f.s.st, `DROP FUNCTION fixture_refused_removal_failure()`)
	} else {
		mustExec(t, f.s.st, `DROP TRIGGER fixture_refused_removal_failure`)
	}
	completed, err := f.s.settlePlatformDeleteData(context.Background(), lease, intent, receipt)
	if err != nil || completed.snapshot.Stage != appplatform.StageComplete {
		t.Fatal("the retry of the settlement failed", completed.snapshot, err)
	}
	if _, err := f.s.settlePlatformDeleteData(context.Background(), lease, intent, receipt); err == nil {
		t.Fatal("a completed removal settled twice")
	}
}

func TestRefusedInstallRemovalIsCrashSafeSQLite(t *testing.T) {
	testRefusedInstallRemovalIsCrashSafe(t, false)
}
func TestRefusedInstallRemovalIsCrashSafePostgres(t *testing.T) {
	testRefusedInstallRemovalIsCrashSafe(t, true)
}

func TestRefusedInstallRemovalCrashAfterThePreparedEffectFailsClosed(t *testing.T) {
	f := newRefusedInstall(t, false)
	_, commit := f.uninstall(t, platformFixtureID(7130))
	lease := f.claimAtStop(t, platformFixtureID(7131))
	var err error
	for range 2 {
		if lease, err = f.s.advancePlatformNeverStartedRemoval(context.Background(), lease); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.s.preparePlatformDeleteDataIntent(context.Background(), lease); err != nil {
		t.Fatal(err)
	}
	mustExec(t, f.s.st, `UPDATE app_platform_operations SET lease_until_ms=0 WHERE id=?`, commit.OperationID)
	reclaimed, err := f.s.claimNextPlatformManagedUninstall(context.Background(), platformFixtureID(7132))
	if err != nil || reclaimed.lease.id != commit.OperationID {
		t.Fatal("the crashed removal was stranded", reclaimed, err)
	}
	if _, err := f.s.preparePlatformDeleteDataIntent(context.Background(), reclaimed.lease); err == nil {
		t.Fatal("a second delete-data command was prepared for the same stage")
	}
	if err := f.s.finishPlatformManagedUninstallForReview(context.Background(), reclaimed.lease); err != nil {
		t.Fatal(err)
	}
	if scalar[string](t, f.s.st, `SELECT state FROM app_platform_data_namespaces WHERE origin_installation_id=?`, f.installation) != "active" ||
		scalar[string](t, f.s.st, `SELECT desired_state||':'||phase FROM app_platform_installations WHERE id=?`, f.installation) != "removed:recovery_required" ||
		f.effects(t, commit.OperationID, `kind='delete-data' AND state='prepared'`) != 1 {
		t.Fatal("the crash released data or hid the prepared command")
	}
}

// A stray effect in a stage that has none for this variant is evidence that disagrees with the plan.
func TestNeverStartedRemovalRefusesStrayEffects(t *testing.T) {
	f := newRefusedInstall(t, false)
	_, commit := f.uninstall(t, platformFixtureID(7140))
	lease := f.claimAtStop(t, platformFixtureID(7141))
	now := scalar[int64](t, f.s.st, `SELECT updated_ms FROM app_platform_operations WHERE id=?`, commit.OperationID)
	mustExec(t, f.s.st, `INSERT INTO app_platform_operation_effects(operation_id,stage,effect_number,effect_id,kind,payload_sha256,runtime_id,controller_epoch,admitted_fence,observed_fence,state,receipt_json,created_ms,updated_ms)
		SELECT operation_id,'stop',1,?,'stop',payload_sha256,runtime_id,controller_epoch,admitted_fence,0,'prepared','{}',?,? FROM app_platform_operation_effects WHERE kind='prepare-data' LIMIT 1`, platformFixtureID(7142), now, now)
	if _, err := f.s.advancePlatformNeverStartedRemoval(context.Background(), lease); err == nil {
		t.Fatal("the never-started removal advanced over a stop effect")
	}
}
