package app

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

// This seeds a coherent activation fixture to exercise the read gate. It is not
// an installer/provider completion path or proof of physical readiness.
func platformLaunchFixture(t *testing.T, pg bool) (*Server, platformActorSession, string) {
	return platformLaunchFixtureWithArchive(t, pg, nil)
}
func platformLaunchFixtureWithArchive(t *testing.T, pg bool, archive []byte) (*Server, platformActorSession, string) {
	t.Helper()
	s, session, release := platformInstallFixture(t, pg)
	if archive != nil {
		raw, _, _ := platformSignedReleaseFixture(t, "1.0.0-beta.2")
		var manifest appplatform.Manifest
		if err := json.Unmarshal(raw, &manifest); err != nil {
			t.Fatal(err)
		}
		manifest.Compatibility.PlatformAPI = ">=2.0.0-0 <3.0.0"
		manifest.Capabilities.Required = []string{}
		manifest.Frontend.SHA256 = platformDigest(archive)
		raw, err := json.Marshal(manifest)
		if err != nil {
			t.Fatal(err)
		}
		release, err = s.stagePlatformRelease(session, raw, platformSignTestDescriptor(t, raw), archive)
		if err != nil {
			t.Fatal(err)
		}
	}

	review, err := s.createPlatformNativeInstallReview(session, release.sha256, 1)
	if err != nil {
		t.Fatal(err)
	}
	committed, err := s.consumePlatformInstallReview(session, review.ID, review.SHA256, platformFixtureID(610))
	if err != nil {
		t.Fatal(err)
	}
	id := committed.InstallationID
	mustExec(t, s.st, `UPDATE app_platform_operations SET stage='complete',completed_ms=1,lease_owner=NULL,lease_until_ms=NULL WHERE installation_id=?`, id)
	mustExec(t, s.st, `UPDATE app_platform_installations SET phase='ready' WHERE id=?`, id)
	platformTestInsert(t, s.st.poolExec(), "data_namespaces", map[string]any{"id": platformFixtureID(611), "origin_installation_id": id, "owner_installation_id": id, "state": "active", "data_format": 1, "quota_bytes": 256 << 20, "observed_bytes": 0, "writer_fence": 1, "revision": 1, "retained_ms": nil, "deleted_ms": nil, "created_ms": 1, "updated_ms": 1})
	platformTestInsert(t, s.st.poolExec(), "activations", map[string]any{"id": platformFixtureID(612), "installation_id": id, "release_sha256": release.sha256, "generation": 1, "policy_revision": 1, "data_namespace_id": platformFixtureID(611), "data_format": 1, "state": "active", "readiness_evidence": "{}", "observed_ms": 1, "created_ms": 1, "updated_ms": 1})
	// The reader is a normal user, not the administrator who admitted the fixture.
	user := "platform-launch-reader"
	if err := s.st.UpsertUser(User{Username: user, PasswordHash: "fixture", Role: "user", Active: true}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.st.DeleteUser(user); err != nil {
			t.Error(err)
		}
	})
	account := s.st.GetUser(user)
	generation, err := notificationAccountGeneration(s.st.poolExec(), user)
	if err != nil {
		t.Fatal(err)
	}
	reader := platformActorSession{username: user, created: account.CreatedAt, sessionID: strings.Repeat("c", 64), sessionRevision: account.SessionRev, generation: generation, expires: time.Unix(time.Now().Add(time.Hour).Unix(), 0)}
	platformSeedActorSession(t, s.st, reader)
	return s, reader, id
}
func platformGrantLaunchUser(t *testing.T, s *Server, reader platformActorSession, id string) {
	t.Helper()
	platformTestInsert(t, s.st.poolExec(), "access_grants", map[string]any{"id": platformFixtureID(613), "installation_id": id, "principal_type": "user", "username": reader.username, "account_created": reader.created, "account_generation": reader.generation, "group_id": nil, "group_created": nil, "revision": 1, "created_ms": 1, "updated_ms": 1})
}
func testPlatformLaunchUserAccess(t *testing.T, pg bool) {
	s, reader, id := platformLaunchFixture(t, pg)
	if result, err := s.inspectPlatformLaunchAccess(reader, id); err == nil || !reflect.DeepEqual(result, platformLaunchAccess{}) {
		t.Fatal("ungranted reader learned app launch metadata")
	}
	platformGrantLaunchUser(t, s, reader, id)
	before := dumpOf(t, s.st)
	result, err := s.inspectPlatformLaunchAccess(reader, id)
	if err != nil {
		t.Fatal(err)
	}
	if result.installationID != id || result.generation != "1" || result.policyRevision != "1" || result.entry != "index.html" || !platformValidSHA(result.releaseSHA) || !platformValidSHA(result.frontendSHA) {
		t.Fatalf("incomplete access binding: %+v", result)
	}
	if !bytes.Equal(stripHeader(before), stripHeader(dumpOf(t, s.st))) {
		t.Fatal("inspection changed durable app state")
	}
	mustExec(t, s.st, `UPDATE app_platform_access_grants SET account_generation=account_generation+1 WHERE installation_id=?`, id)
	if r, err := s.inspectPlatformLaunchAccess(reader, id); err == nil || !reflect.DeepEqual(r, platformLaunchAccess{}) {
		t.Fatal("recreated principal inherited access")
	}
}
func TestPlatformLaunchUserAccessSQLite(t *testing.T)   { testPlatformLaunchUserAccess(t, false) }
func TestPlatformLaunchUserAccessPostgres(t *testing.T) { testPlatformLaunchUserAccess(t, true) }

func testPlatformLaunchRefusals(t *testing.T, pg bool) {
	for _, kind := range []string{"disabled", "pending", "retired", "generation", "policy", "bad_namespace", "retained", "data_format", "expired", "revoked_session", "inactive", "bad_account", "bad_frontend", "bad_descriptor", "publisher_disabled", "publisher_key_revoked", "paused", "epoch", "unfinished_operation", "granted_capability", "zero_writer_fence", "unobserved", "future_observed", "recovery_operation", "pending_effect", "stale_capability_policy", "wrong_app", "account_expired", "session_revision", "missing_control", "invalid_id"} {
		t.Run(kind, func(t *testing.T) {
			s, reader, id := platformLaunchFixture(t, pg)
			platformGrantLaunchUser(t, s, reader, id)
			switch kind {
			case "disabled":
				mustExec(t, s.st, `UPDATE app_platform_installations SET desired_state='disabled'`)
			case "pending":
				mustExec(t, s.st, `UPDATE app_platform_installations SET phase='pending'`)
			case "retired":
				mustExec(t, s.st, `UPDATE app_platform_activations SET state='retired'`)
			case "generation":
				mustExec(t, s.st, `UPDATE app_platform_installations SET generation=2`)
			case "policy":
				mustExec(t, s.st, `UPDATE app_platform_installations SET policy_revision=2`)
			case "bad_namespace":
				mustExec(t, s.st, `UPDATE app_platform_activations SET data_namespace_id=NULL`)
			case "retained":
				mustExec(t, s.st, `UPDATE app_platform_data_namespaces SET state='retained'`)
			case "data_format":
				mustExec(t, s.st, `UPDATE app_platform_data_namespaces SET data_format=2`)
			case "expired":
				reader.expires = time.Unix(1, 0)
			case "revoked_session":
				mustExec(t, s.st, `UPDATE user_sessions SET revoked=1 WHERE id=?`, reader.sessionID)
			case "inactive":
				mustExec(t, s.st, `UPDATE users SET active=0 WHERE username=?`, reader.username)
			case "bad_account":
				reader.created += "old"
			case "bad_frontend":
				mustExec(t, s.st, `UPDATE app_platform_frontend_artifacts SET content=?,archive_bytes=?`, []byte("tampered"), 8)
			case "bad_descriptor":
				mustExec(t, s.st, `UPDATE app_platform_releases SET descriptor=?`, []byte(`{}`))
			case "publisher_disabled":
				mustExec(t, s.st, `UPDATE app_platform_publishers SET enabled=0`)
			case "publisher_key_revoked":
				mustExec(t, s.st, `UPDATE app_platform_publisher_keys SET revoked_ms=1`)
			case "paused":
				mustExec(t, s.st, `UPDATE meta SET v='1' WHERE k='app_platform_restore_paused'`)
			case "epoch":
				mustExec(t, s.st, `UPDATE app_platform_installations SET controller_epoch=?`, platformFixtureID(999))
			case "unfinished_operation":
				mustExec(t, s.st, `UPDATE app_platform_operations SET stage='prepare_data',completed_ms=NULL`)
			case "zero_writer_fence":
				mustExec(t, s.st, `UPDATE app_platform_data_namespaces SET writer_fence=0`)
			case "unobserved":
				mustExec(t, s.st, `UPDATE app_platform_activations SET observed_ms=NULL`)
			case "future_observed":
				mustExec(t, s.st, `UPDATE app_platform_activations SET observed_ms=?`, time.Now().Add(time.Hour).UnixMilli())
			case "recovery_operation":
				mustExec(t, s.st, `UPDATE app_platform_operations SET stage='recovery_required'`)
			case "pending_effect":
				var op, epoch string
				if err := s.st.queryRow(`SELECT id,controller_epoch FROM app_platform_operations WHERE installation_id=?`, id).Scan(&op, &epoch); err != nil {
					t.Fatal(err)
				}
				platformTestInsert(t, s.st.poolExec(), "operation_effects", map[string]any{"operation_id": op, "stage": "prepare_data", "effect_number": 1, "effect_id": platformFixtureID(615), "kind": "prepare_data", "payload_sha256": strings.Repeat("a", 64), "runtime_id": nil, "controller_epoch": epoch, "admitted_fence": 1, "observed_fence": 0, "state": "unknown", "receipt_json": "{}", "created_ms": 1, "updated_ms": 1})
			case "stale_capability_policy":
				mustExec(t, s.st, `UPDATE app_platform_capability_grants SET policy_revision=2`)
			case "wrong_app":
				mustExec(t, s.st, `UPDATE app_platform_installations SET app_id='notes-other'`)
			case "account_expired":
				mustExec(t, s.st, `UPDATE users SET expires_at='2000-01-01' WHERE username=?`, reader.username)
			case "session_revision":
				mustExec(t, s.st, `UPDATE users SET session_rev=session_rev+1 WHERE username=?`, reader.username)
			case "missing_control":
				mustExec(t, s.st, `DELETE FROM meta WHERE k='app_platform_controller_epoch'`)
			case "invalid_id":
				id = "invalid"
			case "granted_capability":
				if scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_capability_grants`) == 0 {
					t.Fatal("fixture has no optional capability")
				}
				mustExec(t, s.st, `UPDATE app_platform_capability_grants SET granted=1`)
			}
			before := dumpOf(t, s.st)
			result, err := s.inspectPlatformLaunchAccess(reader, id)
			if err == nil || !reflect.DeepEqual(result, platformLaunchAccess{}) {
				t.Fatal("invalid access/state/trust leaked launch metadata")
			}
			if !bytes.Equal(stripHeader(before), stripHeader(dumpOf(t, s.st))) {
				t.Fatal("failed inspection mutated state")
			}
		})
	}
}
func TestPlatformLaunchRefusalsSQLite(t *testing.T)   { testPlatformLaunchRefusals(t, false) }
func TestPlatformLaunchRefusalsPostgres(t *testing.T) { testPlatformLaunchRefusals(t, true) }

func testPlatformLaunchOUAccess(t *testing.T, pg bool) {
	s, reader, id := platformLaunchFixture(t, pg)
	parent, err := s.st.CreateUserGroup("Launch parent", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	child, err := s.st.CreateUserGroup("Launch child", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := s.st.SetPrimaryGroups([]string{reader.username}, 0); err != nil {
			t.Error(err)
		}
		for _, g := range []int64{child, parent} {
			if _, _, err := s.st.DeleteUserGroup(g); err != nil {
				t.Error(err)
			}
		}
	})
	mustExec(t, s.st, `UPDATE user_groups SET parent_id=? WHERE id=?`, parent, child)
	if _, err := s.st.SetPrimaryGroups([]string{reader.username}, child); err != nil {
		t.Fatal(err)
	}
	var created string
	if err := s.st.queryRow(`SELECT created_at FROM user_groups WHERE id=?`, parent).Scan(&created); err != nil {
		t.Fatal(err)
	}
	platformTestInsert(t, s.st.poolExec(), "access_grants", map[string]any{"id": platformFixtureID(614), "installation_id": id, "principal_type": "ou", "username": nil, "account_created": nil, "account_generation": nil, "group_id": parent, "group_created": created, "revision": 1, "created_ms": 1, "updated_ms": 1})
	if _, err := s.inspectPlatformLaunchAccess(reader, id); err != nil {
		t.Fatal("actual ancestor grant denied:", err)
	}
	mustExec(t, s.st, `UPDATE app_platform_access_grants SET group_created=?`, created+"old")
	if _, err := s.inspectPlatformLaunchAccess(reader, id); err == nil {
		t.Fatal("stale OU identity inherited grant")
	}
	mustExec(t, s.st, `UPDATE app_platform_access_grants SET group_created=?`, created)
	mustExec(t, s.st, `UPDATE user_groups SET parent_id=NULL WHERE id=?`, child)
	if _, err := s.inspectPlatformLaunchAccess(reader, id); err == nil {
		t.Fatal("old ancestor retained access after reparent")
	}
	// A Default policy fallback does not put an independent historical root in
	// the Default audience subtree.
	var defaultID int64
	if err := s.st.queryRow(`SELECT id,created_at FROM user_groups WHERE is_default=1`).Scan(&defaultID, &created); err != nil {
		t.Fatal(err)
	}
	mustExec(t, s.st, `UPDATE app_platform_access_grants SET group_id=?,group_created=?`, defaultID, created)
	if _, err := s.inspectPlatformLaunchAccess(reader, id); err == nil {
		t.Fatal("policy fallback manufactured OU audience ancestry")
	}
	if _, err := s.st.SetPrimaryGroups([]string{reader.username}, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := s.inspectPlatformLaunchAccess(reader, id); err != nil {
		t.Fatal("Default member denied Default grant:", err)
	}
	t.Cleanup(func() { mustExec(t, s.st, `UPDATE user_groups SET created_at=? WHERE id=?`, created, defaultID) })
	mustExec(t, s.st, `UPDATE user_groups SET created_at='' WHERE id=?`, defaultID)
	mustExec(t, s.st, `UPDATE app_platform_access_grants SET group_created=''`)
	if _, err := s.inspectPlatformLaunchAccess(reader, id); err == nil {
		t.Fatal("missing OU creation identity authorized access")
	}
	mustExec(t, s.st, `UPDATE user_groups SET created_at=? WHERE id=?`, created, defaultID)
	mustExec(t, s.st, `UPDATE app_platform_access_grants SET group_created=?`, created)
	mustExec(t, s.st, `UPDATE user_groups SET parent_id=? WHERE id=?`, defaultID, child)
	if _, err := s.st.SetPrimaryGroups([]string{reader.username}, child); err != nil {
		t.Fatal(err)
	}
	if _, err := s.inspectPlatformLaunchAccess(reader, id); err != nil {
		t.Fatal("actual Default descendant denied:", err)
	}
	mustExec(t, s.st, `UPDATE user_groups SET parent_id=? WHERE id=?`, child, child)
	if _, err := s.inspectPlatformLaunchAccess(reader, id); err == nil {
		t.Fatal("cyclic OU graph authorized access")
	}
	// Restore topology before lifecycle cleanup.
	mustExec(t, s.st, `UPDATE user_groups SET parent_id=NULL WHERE id=?`, child)
}
func TestPlatformLaunchOUAccessSQLite(t *testing.T)   { testPlatformLaunchOUAccess(t, false) }
func TestPlatformLaunchOUAccessPostgres(t *testing.T) { testPlatformLaunchOUAccess(t, true) }

func TestPlatformLaunchAdministratorNeedsAudienceGrant(t *testing.T) {
	s, reader, id := platformLaunchFixture(t, false)
	mustExec(t, s.st, `UPDATE users SET role='admin' WHERE username=?`, reader.username)
	if result, err := s.inspectPlatformLaunchAccess(reader, id); err == nil || result != (platformLaunchAccess{}) {
		t.Fatal("admin management implied audience access")
	}
}

func TestPlatformLaunchDecimalCounterBinding(t *testing.T) {
	s, reader, id := platformLaunchFixture(t, false)
	platformGrantLaunchUser(t, s, reader, id)
	const value int64 = 9007199254740993
	mustExec(t, s.st, `UPDATE app_platform_installations SET generation=?,policy_revision=?`, value, value)
	mustExec(t, s.st, `UPDATE app_platform_activations SET generation=?,policy_revision=?`, value, value)
	mustExec(t, s.st, `UPDATE app_platform_capability_grants SET policy_revision=?`, value)
	result, err := s.inspectPlatformLaunchAccess(reader, id)
	if err != nil || result.generation != "9007199254740993" || result.policyRevision != result.generation {
		t.Fatalf("counter precision lost: %+v %v", result, err)
	}
}

func TestPostgresPlatformLaunchExpiryAfterReleaseWait(t *testing.T) {
	for _, kind := range []string{"reader", "signing_key"} {
		t.Run(kind, func(t *testing.T) {
			s, reader, id := platformLaunchFixture(t, true)
			platformGrantLaunchUser(t, s, reader, id)
			now, err := platformDatabaseNow(s.st.poolExec())
			if err != nil {
				t.Fatal(err)
			}
			deadline := time.UnixMilli(now + 3000).Truncate(time.Second)
			if kind == "reader" {
				reader.expires = deadline
				platformSeedActorSession(t, s.st, reader)
			} else {
				mustExec(t, s.st, `UPDATE app_platform_publisher_keys SET not_after_ms=?`, deadline.UnixMilli())
			}
			before := dumpOf(t, s.st)
			holder, err := s.st.db.Begin()
			if err != nil {
				t.Fatal(err)
			}
			defer holder.Rollback()
			if _, err := holder.Exec(`SELECT sha256 FROM app_platform_releases FOR UPDATE`); err != nil {
				t.Fatal(err)
			}
			type outcome struct {
				result platformLaunchAccess
				err    error
			}
			done := make(chan outcome, 1)
			go func() { r, err := s.inspectPlatformLaunchAccess(reader, id); done <- outcome{r, err} }()
			blocked := false
			limit := time.Now().Add(5 * time.Second)
			for time.Now().Before(limit) {
				var n int
				if err := s.st.queryRow(`SELECT COUNT(*) FROM pg_stat_activity WHERE datname=current_database() AND pid<>pg_backend_pid() AND wait_event_type='Lock' AND query LIKE '%FOR SHARE OF r,f%'`).Scan(&n); err != nil {
					t.Fatal(err)
				}
				if n > 0 {
					blocked = true
					break
				}
				time.Sleep(5 * time.Millisecond)
			}
			if !blocked {
				holder.Rollback()
				t.Fatalf("inspection never reached release lock: %+v", <-done)
			}
			for time.Now().Before(deadline) {
				time.Sleep(5 * time.Millisecond)
			}
			if err := holder.Commit(); err != nil {
				t.Fatal(err)
			}
			got := <-done
			if got.err == nil || got.result != (platformLaunchAccess{}) {
				t.Fatalf("expired %s disclosed launch binding: %+v", kind, got)
			}
			if !bytes.Equal(stripHeader(before), stripHeader(dumpOf(t, s.st))) {
				t.Fatal("expired read changed durable state")
			}
		})
	}
}
