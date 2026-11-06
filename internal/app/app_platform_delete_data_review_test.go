package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

func platformDeleteDataFixture(
	t *testing.T, pg bool,
) (*Server, platformActorSession, string, string) {
	t.Helper()
	s, actor, lease := platformManagedRemovalStopFixtureAction(
		t, pg, appplatform.ActionDisable)
	intent, err := s.preparePlatformManagedRemovalIntent(
		context.Background(), lease)
	if err != nil {
		t.Fatal(err)
	}
	endpoint := platformManagedRemovalFixtureEndpoint()
	receipt, ok := validPlatformManagedRemovalObservation(
		platformManagedRemovalObservationFixture(intent, endpoint),
		intent, endpoint)
	if !ok {
		t.Fatal("disable stop receipt")
	}
	completed, err := s.settlePlatformManagedRemoval(
		context.Background(), lease, intent, receipt)
	if err != nil || completed.snapshot.Stage != appplatform.StageComplete {
		t.Fatal("disable completion", completed.snapshot, err)
	}
	namespace := scalar[string](t, s.st, `SELECT id
		FROM app_platform_data_namespaces WHERE origin_installation_id=?`,
		lease.installationID)
	if !platformValidUUID(namespace) ||
		scalar[string](t, s.st, `SELECT desired_state||':'||phase
			FROM app_platform_installations WHERE id=?`,
			lease.installationID) != "disabled:disabled" {
		t.Fatal("disabled retained-data fixture")
	}
	return s, actor, lease.installationID, namespace
}

func testPlatformDeleteDataReview(t *testing.T, pg bool) {
	s, actor, installation, namespace := platformDeleteDataFixture(t, pg)
	beforeRevision := scalar[int64](t, s.st, `SELECT revision
		FROM app_platform_installations WHERE id=?`, installation)
	beforeFence := scalar[int64](t, s.st, `SELECT writer_fence
		FROM app_platform_data_namespaces WHERE id=?`, namespace)
	review, err := s.createPlatformDeleteDataReview(
		context.Background(), actor, installation, namespace)
	if err != nil {
		t.Fatal("delete-data review", err)
	}
	var p platformDeleteDataReviewProjection
	if err := json.Unmarshal([]byte(review.JSON), &p); err != nil {
		t.Fatal(err)
	}
	if p.Format != platformDeleteDataReviewFormat ||
		p.Action != appplatform.ActionDeleteData || !p.Managed ||
		p.InstallationID != installation || p.Data.ID != namespace ||
		p.Generation < 1 || p.TargetGeneration != p.Generation+1 ||
		p.Data.Format < 1 || p.Data.WriterFence != beforeFence ||
		p.Data.RunnerWriterFence != p.Data.WriterFence-1 ||
		p.Data.ProjectID == "" || p.Data.Device == "" ||
		p.Data.NamespaceInode == "" || p.Data.QuotaInodes == "" ||
		p.Data.SourcePayloadSHA256 == "" ||
		p.Data.SourceReceiptSHA256 == "" ||
		p.Data.OwnershipSHA256 == "" ||
		!platformValidSHA(p.RecoverySetsSHA256) {
		t.Fatalf("incomplete delete-data review: %+v", p)
	}
	if scalar[int64](t, s.st, `SELECT revision
		FROM app_platform_installations WHERE id=?`,
		installation) != beforeRevision ||
		scalar[string](t, s.st, `SELECT state||':'||owner_installation_id
			FROM app_platform_data_namespaces WHERE id=?`,
			namespace) != "active:"+installation ||
		scalar[int](t, s.st, `SELECT COUNT(*)
			FROM app_platform_operations WHERE action='delete_data'`) != 0 {
		t.Fatal("review changed authority or data")
	}

	commit, err := s.consumePlatformDeleteDataReview(
		actor, review.ID, review.SHA256, platformFixtureID(1800))
	if err != nil {
		t.Fatal("delete-data admission", err)
	}
	if commit.OperationID == "" || commit.Generation != p.TargetGeneration ||
		commit.Stage != appplatform.StageQueued {
		t.Fatal("delete-data commit", commit)
	}
	if scalar[string](t, s.st, `SELECT action||':'||managed||':'||stage
		FROM app_platform_operations WHERE id=?`,
		commit.OperationID) != "delete_data:1:queued" ||
		scalar[int64](t, s.st, `SELECT generation
			FROM app_platform_installations WHERE id=?`,
			installation) != p.TargetGeneration ||
		scalar[string](t, s.st, `SELECT state||':'||
			COALESCE(owner_installation_id,'')
			FROM app_platform_data_namespaces WHERE id=?`,
			namespace) != "deleting:" ||
		scalar[int64](t, s.st, `SELECT writer_fence
			FROM app_platform_data_namespaces WHERE id=?`,
			namespace) != beforeFence+1 {
		t.Fatal("delete-data admission did not close authority")
	}
	replayed, err := s.consumePlatformDeleteDataReview(
		actor, review.ID, review.SHA256, platformFixtureID(1800))
	if err != nil || !replayed.Replayed ||
		replayed.OperationID != commit.OperationID {
		t.Fatal("delete-data replay", replayed, err)
	}
	if _, err := s.consumePlatformDeleteDataReview(
		actor, review.ID, review.SHA256, platformFixtureID(1801)); !errors.Is(err, errAppPlatformOperationConflict) {
		t.Fatal("second delete-data confirmation accepted", err)
	}
}

func TestPlatformDeleteDataReviewSQLite(t *testing.T) {
	testPlatformDeleteDataReview(t, false)
}

func TestPlatformDeleteDataReviewPostgres(t *testing.T) {
	testPlatformDeleteDataReview(t, true)
}

func testPlatformDeleteDataStaleReview(t *testing.T, pg bool) {
	for name, mutate := range map[string]func(*testing.T, *Server) error{
		"namespace revision": func(t *testing.T, s *Server) error {
			mustExec(t, s.st, `UPDATE app_platform_data_namespaces
				SET revision=revision+1`)
			return errAppPlatformPlanChanged
		},
		"namespace owner": func(t *testing.T, s *Server) error {
			other := platformFixtureID(1811)
			platformSeedInstallation(t, s.st.poolExec(), other,
				"fixture-publisher", "delete-data-other")
			mustExec(t, s.st, `UPDATE app_platform_data_namespaces
				SET owner_installation_id=?`, other)
			return errAppPlatformPlanChanged
		},
		"release": func(t *testing.T, s *Server) error {
			mustExec(t, s.st, `UPDATE app_platform_activations
				SET data_format=data_format+1 WHERE state='retired'`)
			return errAppPlatformPlanChanged
		},
		"install revision": func(t *testing.T, s *Server) error {
			mustExec(t, s.st, `UPDATE app_platform_installations
				SET revision=revision+1`)
			return errAppPlatformPlanChanged
		},
	} {
		t.Run(name, func(t *testing.T) {
			s, actor, installation, namespace :=
				platformDeleteDataFixture(t, pg)
			review, err := s.createPlatformDeleteDataReview(
				context.Background(), actor, installation, namespace)
			if err != nil {
				t.Fatal(err)
			}
			want := mutate(t, s)
			before := dumpOf(t, s.st)
			_, err = s.consumePlatformDeleteDataReview(
				actor, review.ID, review.SHA256,
				platformFixtureID(1812))
			if err == nil || want != nil && !errors.Is(err, want) {
				t.Fatalf("stale delete-data review: %v, want %v", err, want)
			}
			if !bytes.Equal(stripHeader(before), stripHeader(dumpOf(t, s.st))) {
				t.Fatal("refused delete-data review mutated state")
			}
		})
	}
}

func TestPlatformDeleteDataStaleReviewSQLite(t *testing.T) {
	testPlatformDeleteDataStaleReview(t, false)
}

func TestPlatformDeleteDataStaleReviewPostgres(t *testing.T) {
	testPlatformDeleteDataStaleReview(t, true)
}

func testPlatformConcurrentDeleteDataCommit(t *testing.T, pg bool) {
	s, actor, installation, namespace := platformDeleteDataFixture(t, pg)
	review, err := s.createPlatformDeleteDataReview(
		context.Background(), actor, installation, namespace)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make(chan platformRemovalCommit, 8)
	errs := make(chan error, 8)
	for range 8 {
		wg.Go(func() {
			result, err := s.consumePlatformDeleteDataReview(
				actor, review.ID, review.SHA256,
				platformFixtureID(1820))
			results <- result
			errs <- err
		})
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	id, fresh := "", 0
	for result := range results {
		if !result.Replayed {
			fresh++
		}
		if id != "" && id != result.OperationID {
			t.Fatal("delete-data confirmation created multiple operations")
		}
		id = result.OperationID
	}
	if fresh != 1 || id == "" ||
		scalar[int](t, s.st, `SELECT COUNT(*)
			FROM app_platform_operations WHERE action='delete_data'`) != 1 {
		t.Fatal("concurrent delete-data confirmation", fresh, id)
	}
}

func TestPlatformConcurrentDeleteDataCommitSQLite(t *testing.T) {
	testPlatformConcurrentDeleteDataCommit(t, false)
}

func TestPlatformConcurrentDeleteDataCommitPostgres(t *testing.T) {
	testPlatformConcurrentDeleteDataCommit(t, true)
}

func testPlatformDeleteDataOperatorConflict(t *testing.T, pg bool) {
	s, actor, installation, namespace := platformDeleteDataFixture(t, pg)
	first, err := s.createPlatformDeleteDataReview(
		context.Background(), actor, installation, namespace)
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.createPlatformDeleteDataReview(
		context.Background(), actor, installation, namespace)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.consumePlatformDeleteDataReview(
		actor, first.ID, first.SHA256, platformFixtureID(1825)); err != nil {
		t.Fatal("first delete-data operator", err)
	}
	if _, err := s.consumePlatformDeleteDataReview(
		actor, second.ID, second.SHA256, platformFixtureID(1826)); !errors.Is(err, errAppPlatformPlanChanged) &&
		!errors.Is(err, errAppPlatformOperationConflict) {
		t.Fatal("second delete-data operator accepted stale scope", err)
	}
	if scalar[int](t, s.st, `SELECT COUNT(*)
		FROM app_platform_operations WHERE action='delete_data'`) != 1 ||
		scalar[int](t, s.st, `SELECT COUNT(*)
			FROM app_platform_review_plans WHERE action='delete_data'
			AND consumed_ms IS NOT NULL`) != 1 {
		t.Fatal("concurrent operators admitted duplicate deletion")
	}
}

func TestPlatformDeleteDataOperatorConflictSQLite(t *testing.T) {
	testPlatformDeleteDataOperatorConflict(t, false)
}

func TestPlatformDeleteDataOperatorConflictPostgres(t *testing.T) {
	testPlatformDeleteDataOperatorConflict(t, true)
}

func testPlatformConcurrentDeleteDataOperators(t *testing.T, pg bool) {
	s, firstActor, installation, namespace :=
		platformDeleteDataFixture(t, pg)
	secondActor := platformSecondAdminActor(
		t, s, "delete-data-admin-two")
	first, err := s.createPlatformDeleteDataReview(
		context.Background(), firstActor, installation, namespace)
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.createPlatformDeleteDataReview(
		context.Background(), secondActor, installation, namespace)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, item := range []struct {
		actor platformActorSession
		plan  platformRemovalReview
		key   string
	}{
		{firstActor, first, platformFixtureID(1828)},
		{secondActor, second, platformFixtureID(1829)},
	} {
		wg.Go(func() {
			result, err := s.consumePlatformDeleteDataReview(
				item.actor, item.plan.ID, item.plan.SHA256, item.key)
			if err != nil {
				results <- err
				return
			}
			if result.OperationID == "" {
				results <- errAppPlatformOperationConflict
				return
			}
			results <- nil
		})
	}
	wg.Wait()
	close(results)
	success, refused := 0, 0
	for err := range results {
		switch {
		case err == nil:
			success++
		case errors.Is(err, errAppPlatformPlanChanged),
			errors.Is(err, errAppPlatformOperationConflict):
			refused++
		default:
			t.Fatal(err)
		}
	}
	if success != 1 || refused != 1 ||
		scalar[int](t, s.st, `SELECT COUNT(*)
			FROM app_platform_operations WHERE action='delete_data'`) != 1 ||
		scalar[int](t, s.st, `SELECT COUNT(*)
			FROM app_platform_review_plans WHERE action='delete_data'
			AND consumed_ms IS NOT NULL`) != 1 {
		t.Fatal("concurrent operators", success, refused)
	}
}

func TestPlatformConcurrentDeleteDataOperatorsSQLite(t *testing.T) {
	testPlatformConcurrentDeleteDataOperators(t, false)
}

func TestPlatformConcurrentDeleteDataOperatorsPostgres(t *testing.T) {
	testPlatformConcurrentDeleteDataOperators(t, true)
}

func testPlatformDeleteDataCompletion(t *testing.T, pg bool) {
	s, actor, installation, namespace := platformDeleteDataFixture(t, pg)
	appID := scalar[string](t, s.st, `SELECT app_id
		FROM app_platform_installations WHERE id=?`, installation)
	plugin, err := readPlatformPluginTable(
		s.st.poolExec(), appID, false)
	if err != nil || plugin.TableName == "" {
		t.Fatal("plugin table fixture", err)
	}
	pluginRows := scalar[int](t, s.st,
		`SELECT COUNT(*) FROM `+plugin.TableName)
	installOperation := scalar[string](t, s.st, `SELECT id
		FROM app_platform_operations WHERE installation_id=?
		AND action='install' ORDER BY completed_ms DESC LIMIT 1`,
		installation)
	release := scalar[string](t, s.st, `SELECT release_sha256
		FROM app_platform_activations WHERE installation_id=?
		AND data_namespace_id=? ORDER BY generation DESC LIMIT 1`,
		installation, namespace)
	recoveryID := platformFixtureID(1830)
	platformTestInsert(t, s.st.poolExec(), "recovery_sets", map[string]any{
		"id": recoveryID, "installation_id": installation,
		"data_namespace_id": namespace, "operation_id": installOperation,
		"release_sha256": release, "data_format": 1, "checkpoint_id": platformFixtureID(1831), "manifest_sha256": nil, "bytes": 0,
		"state": "preparing", "controller_epoch": scalar[string](t, s.st, `SELECT controller_epoch
			FROM app_platform_installations WHERE id=?`, installation),
		"writer_fence": 1, "verified_ms": nil, "deleted_ms": nil,
		"created_ms": 1, "updated_ms": 1,
	})
	review, err := s.createPlatformDeleteDataReview(
		context.Background(), actor, installation, namespace)
	if err != nil {
		t.Fatal(err)
	}
	commit, err := s.consumePlatformDeleteDataReview(
		actor, review.ID, review.SHA256, platformFixtureID(1832))
	if err != nil {
		t.Fatal(err)
	}
	selected, err := s.claimNextPlatformDeleteData(
		context.Background(), platformFixtureID(1833))
	if err != nil || selected.lease.id != commit.OperationID {
		t.Fatal("delete-data claim", selected, err)
	}
	current := selected.lease
	for range 3 {
		current, err = s.st.advancePlatformDeleteDataControl(current)
		if err != nil {
			t.Fatal("delete-data control", current.snapshot.Stage, err)
		}
	}
	if current.snapshot.Stage != appplatform.StageRemoveData {
		t.Fatal("delete-data did not reach remove_data", current.snapshot)
	}
	intent, err := s.preparePlatformStandaloneDeleteDataIntent(
		context.Background(), current)
	if err != nil {
		t.Fatal("delete-data intent", err)
	}
	endpoint := platformManagedRemovalFixtureEndpoint()
	receipt, ok := validPlatformDeleteDataObservation(
		platformDeleteDataObservationFixture(intent, endpoint),
		intent, endpoint)
	if !ok {
		t.Fatal("delete-data receipt")
	}
	if pg {
		mustExec(t, s.st, `CREATE FUNCTION
			fixture_standalone_delete_audit_failure()
			RETURNS trigger LANGUAGE plpgsql AS $$
			BEGIN RAISE EXCEPTION 'fixture standalone delete failure';
			END $$`)
		mustExec(t, s.st, `CREATE TRIGGER
			fixture_standalone_delete_audit_failure
			BEFORE INSERT ON audit_log FOR EACH ROW
			WHEN (NEW.action='app_platform.data_deleted')
			EXECUTE FUNCTION
			fixture_standalone_delete_audit_failure()`)
		t.Cleanup(func() {
			mustExec(t, s.st, `DROP TRIGGER IF EXISTS
				fixture_standalone_delete_audit_failure ON audit_log`)
			mustExec(t, s.st, `DROP FUNCTION IF EXISTS
				fixture_standalone_delete_audit_failure()`)
		})
	} else {
		mustExec(t, s.st, `CREATE TRIGGER
			fixture_standalone_delete_audit_failure
			BEFORE INSERT ON audit_log
			WHEN NEW.action='app_platform.data_deleted'
			BEGIN SELECT RAISE(ABORT,
			'fixture standalone delete failure'); END`)
		t.Cleanup(func() {
			mustExec(t, s.st, `DROP TRIGGER IF EXISTS
				fixture_standalone_delete_audit_failure`)
		})
	}
	if _, err := s.settlePlatformStandaloneDeleteData(
		context.Background(), current, intent, receipt); err == nil {
		t.Fatal("delete-data completion without settlement audit")
	}
	if scalar[string](t, s.st, `SELECT state
		FROM app_platform_data_namespaces WHERE id=?`,
		namespace) != "deleting" ||
		scalar[string](t, s.st, `SELECT state
			FROM app_platform_recovery_sets WHERE id=?`,
			recoveryID) != "preparing" ||
		scalar[string](t, s.st, `SELECT state
			FROM app_platform_operation_effects WHERE effect_id=?`,
			intent.command.EffectID) != "prepared" {
		t.Fatal("failed delete-data settlement partially committed")
	}
	if pg {
		mustExec(t, s.st, `DROP TRIGGER
			fixture_standalone_delete_audit_failure ON audit_log`)
		mustExec(t, s.st, `DROP FUNCTION
			fixture_standalone_delete_audit_failure()`)
	} else {
		mustExec(t, s.st, `DROP TRIGGER
			fixture_standalone_delete_audit_failure`)
	}
	completed, err := s.settlePlatformStandaloneDeleteData(
		context.Background(), current, intent, receipt)
	if err != nil || completed.snapshot.Stage != appplatform.StageComplete {
		t.Fatal("delete-data completion", completed.snapshot, err)
	}
	if scalar[string](t, s.st, `SELECT state||':'||
		COALESCE(owner_installation_id,'')||':'||observed_bytes||':'||
		(CASE WHEN deleted_ms IS NULL THEN 0 ELSE 1 END)
		FROM app_platform_data_namespaces WHERE id=?`,
		namespace) != "deleted::0:1" ||
		scalar[string](t, s.st, `SELECT state||':'||
			(CASE WHEN deleted_ms IS NULL THEN 0 ELSE 1 END)
			FROM app_platform_recovery_sets WHERE id=?`,
			recoveryID) != "deleted:1" ||
		scalar[string](t, s.st, `SELECT stage||':'||
			(CASE WHEN completed_ms IS NULL THEN 0 ELSE 1 END)
			FROM app_platform_operations WHERE id=?`,
			commit.OperationID) != "complete:1" {
		t.Fatal("delete-data did not settle exact namespace")
	}
	if scalar[int](t, s.st,
		`SELECT COUNT(*) FROM `+plugin.TableName) != pluginRows ||
		scalar[int](t, s.st, `SELECT COUNT(*)
			FROM app_platform_plugin_tables WHERE app_id=?`, appID) != 1 {
		t.Fatal("namespace deletion cleared unrelated plugin data")
	}
}

func TestPlatformDeleteDataCompletionSQLite(t *testing.T) {
	testPlatformDeleteDataCompletion(t, false)
}

func TestPlatformDeleteDataCompletionPostgres(t *testing.T) {
	testPlatformDeleteDataCompletion(t, true)
}

func TestPlatformDeleteDataUncertaintyRequiresReview(t *testing.T) {
	s, actor, installation, namespace := platformDeleteDataFixture(t, false)
	review, err := s.createPlatformDeleteDataReview(
		context.Background(), actor, installation, namespace)
	if err != nil {
		t.Fatal(err)
	}
	commit, err := s.consumePlatformDeleteDataReview(
		actor, review.ID, review.SHA256, platformFixtureID(1840))
	if err != nil {
		t.Fatal(err)
	}
	selected, err := s.claimNextPlatformDeleteData(
		context.Background(), platformFixtureID(1841))
	if err != nil || selected.lease.id != commit.OperationID {
		t.Fatal("delete-data claim", selected, err)
	}
	current := selected.lease
	for range 3 {
		current, err = s.st.advancePlatformDeleteDataControl(current)
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.preparePlatformStandaloneDeleteDataIntent(
		context.Background(), current); err != nil {
		t.Fatal(err)
	}
	if err := s.finishPlatformDeleteDataForReview(
		context.Background(), current); err != nil {
		t.Fatal(err)
	}
	if scalar[string](t, s.st, `SELECT stage||':'||error_code||':'||
		(CASE WHEN completed_ms IS NULL THEN 0 ELSE 1 END)
		FROM app_platform_operations WHERE id=?`,
		commit.OperationID) != "recovery_required:data_delete_recovery_required:1" ||
		scalar[string](t, s.st, `SELECT state
			FROM app_platform_data_namespaces WHERE id=?`,
			namespace) != "deleting" ||
		scalar[string](t, s.st, `SELECT state
			FROM app_platform_operation_effects WHERE
			operation_id=? AND stage='remove_data'`,
			commit.OperationID) != "prepared" {
		t.Fatal("uncertain delete-data did not stop for review")
	}
	retried, err := s.claimNextPlatformDeleteData(
		context.Background(), platformFixtureID(1842))
	if err != nil || retried.lease.id != "" {
		t.Fatal("uncertain delete-data was automatically reissued", retried, err)
	}
}

func TestPlatformDeleteDataManagementHTTP(t *testing.T) {
	s, actor, installation, namespace := platformDeleteDataFixture(t, false)
	mux := http.NewServeMux()
	s.wireRoutes(mux)
	cookie := s.managedCookie(
		*s.st.GetUser(actor.username), actor.sessionID, actor.expires.Unix())
	body := fmt.Sprintf(
		`{"action":"delete_data","installation_id":%q,"data_namespace_id":%q}`,
		installation, namespace)
	plan := sessionTestRequest(
		mux, http.MethodPost, "/api/admin/app-platform/plans",
		cookie, body)
	if plan.Code != http.StatusOK ||
		!strings.Contains(plan.Body.String(), `"action":"delete_data"`) ||
		!strings.Contains(plan.Body.String(), namespace) ||
		strings.Contains(plan.Body.String(), "actor_authority") {
		t.Fatalf("delete-data review HTTP: %d %s",
			plan.Code, plan.Body.String())
	}
	var review struct {
		ID  string `json:"plan_id"`
		SHA string `json:"plan_sha256"`
	}
	if err := json.Unmarshal(plan.Body.Bytes(), &review); err != nil ||
		!platformValidUUID(review.ID) || !platformValidSHA(review.SHA) {
		t.Fatal("delete-data HTTP review", plan.Body.String(), err)
	}
	commitBody := fmt.Sprintf(
		`{"plan_id":%q,"plan_sha256":%q,"idempotency_key":%q}`,
		review.ID, review.SHA, platformFixtureID(1850))
	commit := sessionTestRequest(
		mux, http.MethodPost, "/api/admin/app-platform/operations",
		cookie, commitBody)
	if commit.Code != http.StatusAccepted ||
		!strings.Contains(commit.Body.String(), `"stage":"queued"`) {
		t.Fatalf("delete-data commit HTTP: %d %s",
			commit.Code, commit.Body.String())
	}
	replay := sessionTestRequest(
		mux, http.MethodPost, "/api/admin/app-platform/operations",
		cookie, commitBody)
	if replay.Code != http.StatusAccepted ||
		!strings.Contains(replay.Body.String(), `"replayed":true`) ||
		scalar[int](t, s.st, `SELECT COUNT(*)
			FROM app_platform_operations WHERE action='delete_data'`) != 1 {
		t.Fatalf("delete-data replay HTTP: %d %s",
			replay.Code, replay.Body.String())
	}
}
