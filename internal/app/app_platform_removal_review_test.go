package app

import (
	"archive/zip"
	"bytes"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

func platformRemovalFixture(t *testing.T, postgres, managed bool) (*Server, platformActorSession) {
	t.Helper()
	s, session := platformActorTestServer(t, postgres)
	if err := s.initializeAppPlatformControl(session); err != nil {
		t.Fatal(err)
	}
	e := s.st.poolExec()
	platformSeedPublisher(t, e, "fixture-publisher")
	platformSeedKey(t, s.st, bytes.Repeat([]byte{1}, 32))
	if err := platformSeedInstallation(t, e, platformFixtureID(1), "fixture-publisher", "notes-sample"); err != nil {
		t.Fatal(err)
	}
	epoch := s.st.GetSetting("app_platform_controller_epoch", "")
	mustExec(t, s.st, `UPDATE app_platform_installations SET controller_epoch=?,phase='ready'`, epoch)
	if managed {
		platformTestInsert(t, e, "runtime_bindings", map[string]any{"id": platformFixtureID(5), "deployment_id": platformFixtureID(6), "controller_epoch": epoch, "control_revision": 1, "enabled": 0,
			"verification_key_fingerprint": nil, "sealed_control_key": nil, "revision": 1, "created_ms": 1, "updated_ms": 1})
		mustExec(t, s.st, `UPDATE app_platform_installations SET runtime_id=?`, platformFixtureID(5))
	}
	var archive bytes.Buffer
	w := zip.NewWriter(&archive)
	f, err := w.Create("index.html")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write([]byte("<!doctype html><title>Fixture</title>")); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	frontend := platformDigest(archive.Bytes())
	filename := "static.json"
	if managed {
		filename = "managed.json"
	}
	raw, err := os.ReadFile("../appplatform/testdata/descriptors/" + filename)
	if err != nil {
		t.Fatal(err)
	}
	raw = bytes.ReplaceAll(raw, []byte("example-publisher"), []byte("fixture-publisher"))
	raw = bytes.ReplaceAll(raw, []byte(strings.Repeat("a", 64)), []byte(frontend))
	parsed, err := appplatform.ParseDescriptor(raw)
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, s.st, `UPDATE app_platform_installations SET app_id=?`, parsed.Manifest.ID)
	platformTestInsert(t, e, "frontend_artifacts", map[string]any{"sha256": frontend, "archive_bytes": archive.Len(), "content": archive.Bytes(), "created_ms": 1})
	// Public fixtures have synthetic signatures; these denial-only operations
	// never verify a publisher for new execution or start any package code.
	platformTestInsert(t, e, "releases", map[string]any{"sha256": parsed.SHA256, "app_id": parsed.Manifest.ID, "publisher_id": "fixture-publisher", "version": parsed.Manifest.Version, "descriptor": raw,
		"signature": bytes.Repeat([]byte{2}, 64), "verification_key_id": "fixture-key", "trust_revision": 1, "verified_ms": 1, "frontend_sha256": frontend, "managed": boolInt(managed), "created_ms": 1})
	platformTestInsert(t, e, "data_namespaces", map[string]any{"id": platformFixtureID(8), "origin_installation_id": platformFixtureID(1), "owner_installation_id": platformFixtureID(1), "state": "active",
		"data_format": 1, "quota_bytes": 256 << 20, "observed_bytes": 123, "writer_fence": 0, "revision": 1, "retained_ms": nil, "deleted_ms": nil, "created_ms": 1, "updated_ms": 1})
	platformTestInsert(t, e, "activations", map[string]any{"id": platformFixtureID(7), "installation_id": platformFixtureID(1), "release_sha256": parsed.SHA256, "generation": 1, "policy_revision": 1,
		"data_namespace_id": platformFixtureID(8), "data_format": 1, "state": "active", "readiness_evidence": "{}", "observed_ms": 1, "created_ms": 1, "updated_ms": 1})
	platformTestInsert(t, e, "access_grants", map[string]any{"id": platformFixtureID(12), "installation_id": platformFixtureID(1), "principal_type": "user", "username": session.username,
		"account_created": session.created, "account_generation": session.generation, "group_id": nil, "group_created": nil, "revision": 1, "created_ms": 1, "updated_ms": 1})
	return s, session
}

func testPlatformRemovalReviewFlow(t *testing.T, postgres bool) {
	for _, managed := range []bool{false, true} {
		for _, action := range []appplatform.Action{appplatform.ActionDisable, appplatform.ActionUninstall} {
			t.Run(fmt.Sprintf("%s/managed=%t", action, managed), func(t *testing.T) {
				s, session := platformRemovalFixture(t, postgres, managed)
				plan, err := s.createPlatformRemovalReview(session, platformFixtureID(1), action)
				if err != nil {
					t.Fatal(err)
				}
				key := platformFixtureID(20)
				commit, err := s.consumePlatformRemovalReview(session, plan.ID, plan.SHA256, key)
				if err != nil {
					t.Fatal(err)
				}
				if commit.Replayed || commit.Generation != 2 || commit.Stage != appplatform.StageQueued {
					t.Fatalf("invalid commit: %+v", commit)
				}
				if _, err := s.consumePlatformRemovalReview(session, plan.ID, plan.SHA256, platformFixtureID(21)); !errors.Is(err, errAppPlatformOperationConflict) {
					t.Fatalf("consumed plan accepted another key or returned wrong conflict: %v", err)
				}
				if scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_activations WHERE state='active'`) != 0 {
					t.Fatal("old activation remained usable")
				}
				if scalar[int](t, s.st, `SELECT observed_bytes FROM app_platform_data_namespaces`) != 123 || scalar[string](t, s.st, `SELECT owner_installation_id FROM app_platform_data_namespaces`) != platformFixtureID(1) {
					t.Fatal("admission deleted/detached data before stop evidence")
				}
				if scalar[int](t, s.st, `SELECT writer_fence FROM app_platform_data_namespaces`) != 1 {
					t.Fatal("data authority not fenced")
				}
				want := "disabled"
				if action == appplatform.ActionUninstall {
					want = "removed"
				}
				if scalar[string](t, s.st, `SELECT desired_state FROM app_platform_installations`) != want {
					t.Fatal("denial intent not persisted")
				}
				mustExec(t, s.st, `UPDATE app_platform_review_plans SET expires_ms=0`)
				replay, err := s.consumePlatformRemovalReview(session, plan.ID, plan.SHA256, key)
				if err != nil {
					t.Fatal(err)
				}
				if !replay.Replayed || replay.OperationID != commit.OperationID || scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_operations`) != 1 {
					t.Fatal("expired consumed plan created duplicate operation")
				}
				if _, err := s.consumePlatformRemovalReview(session, plan.ID, strings.Repeat("f", 64), key); !errors.Is(err, errAppPlatformOperationConflict) {
					t.Fatalf("changed input reused idempotency key: %v", err)
				}
				lease, err := s.st.claimPlatformOperation(commit.OperationID, platformFixtureID(30))
				if err != nil {
					t.Fatal(err)
				}
				if lease.snapshot.Managed != managed || lease.snapshot.Generation != 2 {
					t.Fatal("lease lost reviewed execution mode/generation")
				}
			})
		}
	}
}

func testPlatformRemovalStaleReview(t *testing.T, postgres bool) {
	for _, fixture := range []struct {
		name, statement string
		want            error
	}{
		{"expiry", `UPDATE app_platform_review_plans SET expires_ms=0`, errAppPlatformPlanExpired},
		{"policy", `UPDATE app_platform_installations SET policy_revision=policy_revision+1`, errAppPlatformPlanChanged},
		{"trust", `UPDATE app_platform_publishers SET revision=revision+1`, errAppPlatformPlanChanged},
		{"data", `UPDATE app_platform_data_namespaces SET revision=revision+1`, errAppPlatformPlanChanged},
		{"audience", `UPDATE app_platform_access_grants SET revision=revision+1`, errAppPlatformPlanChanged},
		{"json", `UPDATE app_platform_review_plans SET plan_json='{}'`, errAppPlatformPlanChanged},
		{"demotion", `UPDATE users SET role='user' WHERE username='platform-review-actor'`, errAppPlatformActorUnavailable},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			s, session := platformRemovalFixture(t, postgres, false)
			plan, err := s.createPlatformRemovalReview(session, platformFixtureID(1), appplatform.ActionUninstall)
			if err != nil {
				t.Fatal(err)
			}
			mustExec(t, s.st, fixture.statement)
			before := dumpOf(t, s.st)
			if _, err := s.consumePlatformRemovalReview(session, plan.ID, plan.SHA256, platformFixtureID(20)); !errors.Is(err, fixture.want) {
				t.Fatalf("stale commit error: %v, want %v", err, fixture.want)
			}
			if !bytes.Equal(stripHeader(before), stripHeader(dumpOf(t, s.st))) {
				t.Fatal("refused commit mutated state")
			}
		})
	}
}

func testPlatformRemovalConcurrentCommit(t *testing.T, postgres bool) {
	s, session := platformRemovalFixture(t, postgres, false)
	plan, err := s.createPlatformRemovalReview(session, platformFixtureID(1), appplatform.ActionUninstall)
	if err != nil {
		t.Fatal(err)
	}
	type answer struct {
		commit platformRemovalCommit
		err    error
	}
	results := make(chan answer, 8)
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			commit, err := s.consumePlatformRemovalReview(session, plan.ID, plan.SHA256, platformFixtureID(20))
			results <- answer{commit, err}
		})
	}
	wg.Wait()
	close(results)
	winners := 0
	id := ""
	for result := range results {
		if result.err != nil {
			t.Fatal(result.err)
		}
		if !result.commit.Replayed {
			winners++
		}
		if id != "" && id != result.commit.OperationID {
			t.Fatal("concurrent commit returned multiple operations")
		}
		id = result.commit.OperationID
	}
	if winners != 1 || scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_operations`) != 1 {
		t.Fatalf("admitted %d initial operations", winners)
	}
}

func TestAppPlatformRemovalReviewFlow(t *testing.T)          { testPlatformRemovalReviewFlow(t, false) }
func TestPostgresAppPlatformRemovalReviewFlow(t *testing.T)  { testPlatformRemovalReviewFlow(t, true) }
func TestAppPlatformRemovalStaleReview(t *testing.T)         { testPlatformRemovalStaleReview(t, false) }
func TestPostgresAppPlatformRemovalStaleReview(t *testing.T) { testPlatformRemovalStaleReview(t, true) }
func TestAppPlatformRemovalConcurrentCommit(t *testing.T) {
	testPlatformRemovalConcurrentCommit(t, false)
}
func TestPostgresAppPlatformRemovalConcurrentCommit(t *testing.T) {
	testPlatformRemovalConcurrentCommit(t, true)
}

func TestAppPlatformRemovalCommitRollsBackDenial(t *testing.T) {
	s, session := platformRemovalFixture(t, false, false)
	plan, err := s.createPlatformRemovalReview(session, platformFixtureID(1), appplatform.ActionUninstall)
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, s.st, `CREATE TRIGGER fixture_operation_failure BEFORE INSERT ON app_platform_operations BEGIN SELECT RAISE(ABORT,'fixture operation failure');END`)
	before := dumpOf(t, s.st)
	if _, err := s.consumePlatformRemovalReview(session, plan.ID, plan.SHA256, platformFixtureID(20)); err == nil {
		t.Fatal("injected operation failure ignored")
	}
	if !bytes.Equal(stripHeader(before), stripHeader(dumpOf(t, s.st))) {
		t.Fatal("failed commit left consumed review or partial denial")
	}
}

func TestPostgresAppPlatformRemovalExpiryWhileWaitingForLock(t *testing.T) {
	s, session := platformRemovalFixture(t, true, false)
	plan, err := s.createPlatformRemovalReview(session, platformFixtureID(1), appplatform.ActionUninstall)
	if err != nil {
		t.Fatal(err)
	}
	holder, err := s.st.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Rollback()
	var locked string
	if err := holder.QueryRow(`SELECT id FROM app_platform_installations WHERE id=$1 FOR UPDATE`, platformFixtureID(1)).Scan(&locked); err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() {
		_, err := s.consumePlatformRemovalReview(session, plan.ID, plan.SHA256, platformFixtureID(20))
		result <- err
	}()
	blocked := false
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var count int
		if err := s.st.queryRow(`SELECT COUNT(*) FROM pg_stat_activity WHERE datname=current_database() AND pid<>pg_backend_pid()
            AND wait_event_type='Lock' AND query LIKE '%FROM app_platform_installations WHERE id=%FOR UPDATE%'`).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count > 0 {
			blocked = true
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !blocked {
		holder.Rollback()
		<-result
		t.Fatal("consumer did not reach the held installation lock")
	}
	now, err := platformDatabaseNow(s.st.poolExec())
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, s.st, `UPDATE app_platform_review_plans SET expires_ms=? WHERE id=?`, now+25, plan.ID)
	time.Sleep(40 * time.Millisecond)
	if err := holder.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := <-result; !errors.Is(err, errAppPlatformPlanExpired) {
		t.Fatalf("expired while waiting for lock: %v", err)
	}
	if scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_operations`) != 0 {
		t.Fatal("expired review admitted an operation")
	}
}
