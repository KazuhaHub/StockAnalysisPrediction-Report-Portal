package app

import (
	"bytes"
	"context"
	"reflect"
	"strings"
	"testing"

	protocol "github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal-Apps/packages/extensionprotocol"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

func testPlatformViewerIdentity(t *testing.T, pg bool) {
	s, reader, id := platformLaunchFixture(t, pg)
	if v, err := s.readPlatformLaunchViewerContext(context.Background(), reader, id); err == nil || !reflect.DeepEqual(v, protocol.Viewer{}) {
		t.Fatal("ungranted reader received identity")
	}
	platformGrantLaunchUser(t, s, reader, id)
	before := dumpOf(t, s.st)
	v, err := s.readPlatformLaunchViewerContext(context.Background(), reader, id)
	if err != nil {
		t.Fatal(err)
	}
	if len(v.Subject) != 64 || len(v.OUs) != 1 || len(v.OUs[0]) != 64 || v.Role != "user" || v.SessionRevision != reader.sessionRevision {
		t.Fatal("incorrect current viewer", v)
	}
	if !bytes.Equal(stripHeader(before), stripHeader(dumpOf(t, s.st))) {
		t.Fatal("identity read wrote durable state")
	}
	// A coherent *seeded* new generation checks namespace stability at this read
	// boundary. It is not evidence of a completed normal update implementation.
	mustExec(t, s.st, `UPDATE app_platform_installations SET generation=2,policy_revision=2 WHERE id=?`, id)
	mustExec(t, s.st, `UPDATE app_platform_activations SET generation=2,policy_revision=2 WHERE installation_id=?`, id)
	mustExec(t, s.st, `UPDATE app_platform_capability_grants SET policy_revision=2 WHERE installation_id=?`, id)
	mustExec(t, s.st, `UPDATE users SET role='admin' WHERE username=?`, reader.username)
	current, err := s.readPlatformLaunchViewerContext(context.Background(), reader, id)
	if err != nil || current.Subject != v.Subject || !reflect.DeepEqual(current.OUs, v.OUs) || current.Role != "admin" {
		t.Fatal("update changed identity or cached role", err)
	}
	child, err := s.st.CreateUserGroup("Viewer actual root", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := s.st.SetPrimaryGroups([]string{reader.username}, 0); err != nil {
			t.Error(err)
		}
		if _, _, err := s.st.DeleteUserGroup(child); err != nil {
			t.Error(err)
		}
	})
	if _, err := s.st.SetPrimaryGroups([]string{reader.username}, child); err != nil {
		t.Fatal(err)
	}
	moved, err := s.readPlatformLaunchViewerContext(context.Background(), reader, id)
	if err != nil || moved.Subject != v.Subject || len(moved.OUs) != 1 || moved.OUs[0] == v.OUs[0] {
		t.Fatal("OU membership was cached or default fallback became ancestry", err)
	}
	mustExec(t, s.st, `UPDATE user_groups SET created_at='new-creation-identity' WHERE id=?`, child)
	recreated, err := s.readPlatformLaunchViewerContext(context.Background(), reader, id)
	if err != nil || recreated.Subject != v.Subject || recreated.OUs[0] == moved.OUs[0] {
		t.Fatal("recreated OU reused identity", err)
	}
	// Caller mutation must not poison later derived facts.
	recreated.OUs[0] = "mutated"
	again, err := s.readPlatformLaunchViewerContext(context.Background(), reader, id)
	if err != nil || again.OUs[0] == "mutated" {
		t.Fatal("viewer output aliases cached data", err)
	}
}
func TestPlatformViewerIdentitySQLite(t *testing.T)   { testPlatformViewerIdentity(t, false) }
func TestPlatformViewerIdentityPostgres(t *testing.T) { testPlatformViewerIdentity(t, true) }

func testPlatformViewerOUGrant(t *testing.T, pg bool) {
	s, reader, id := platformLaunchFixture(t, pg)
	parent, err := s.st.CreateUserGroup("Viewer grant root", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	child, err := s.st.CreateUserGroup("Viewer grant child", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := s.st.SetPrimaryGroups([]string{reader.username}, 0); err != nil {
			t.Error(err)
		}
		for _, group := range []int64{child, parent} {
			if _, _, err := s.st.DeleteUserGroup(group); err != nil {
				t.Error(err)
			}
		}
	})
	mustExec(t, s.st, `UPDATE user_groups SET parent_id=? WHERE id=?`, parent, child)
	if _, err := s.st.SetPrimaryGroups([]string{reader.username}, child); err != nil {
		t.Fatal(err)
	}
	created := scalar[string](t, s.st, `SELECT created_at FROM user_groups WHERE id=?`, parent)
	platformTestInsert(t, s.st.poolExec(), "access_grants", map[string]any{"id": platformFixtureID(993), "installation_id": id, "principal_type": "ou", "username": nil, "account_created": nil, "account_generation": nil, "group_id": parent, "group_created": created, "revision": 1, "created_ms": 1, "updated_ms": 1})
	v, err := s.readPlatformLaunchViewerContext(context.Background(), reader, id)
	if err != nil || len(v.OUs) != 2 {
		t.Fatal("actual ancestor grant denied", err)
	}
	mustExec(t, s.st, `UPDATE user_groups SET parent_id=NULL WHERE id=?`, child)
	if v, err := s.readPlatformLaunchViewerContext(context.Background(), reader, id); err == nil || !reflect.DeepEqual(v, protocol.Viewer{}) {
		t.Fatal("former ancestor retained viewer access")
	}
	mustExec(t, s.st, `UPDATE user_groups SET parent_id=? WHERE id=?`, parent, child)
	mustExec(t, s.st, `UPDATE user_groups SET created_at='new-ancestor' WHERE id=?`, parent)
	if v, err := s.readPlatformLaunchViewerContext(context.Background(), reader, id); err == nil || !reflect.DeepEqual(v, protocol.Viewer{}) {
		t.Fatal("recreated ancestor inherited old grant")
	}
}
func TestPlatformViewerOUGrantSQLite(t *testing.T)   { testPlatformViewerOUGrant(t, false) }
func TestPlatformViewerOUGrantPostgres(t *testing.T) { testPlatformViewerOUGrant(t, true) }

func testPlatformViewerRefusals(t *testing.T, pg bool) {
	for _, mode := range []string{"revoked_session", "expired_session", "account_generation", "inactive", "grant_removed", "missing_ou_creation", "seed_missing", "seed_wrong_aad", "keyring_missing", "keyring_corrupt", "previous_key", "paused", "managed", "cancelled"} {
		t.Run(mode, func(t *testing.T) {
			s, reader, id := platformLaunchFixture(t, pg)
			platformGrantLaunchUser(t, s, reader, id)
			ctx := context.Background()
			switch mode {
			case "revoked_session":
				mustExec(t, s.st, `UPDATE user_sessions SET revoked=1 WHERE id=?`, reader.sessionID)
			case "expired_session":
				mustExec(t, s.st, `UPDATE user_sessions SET expires_at=0 WHERE id=?`, reader.sessionID)
			case "account_generation":
				reader.generation++
			case "inactive":
				mustExec(t, s.st, `UPDATE users SET active=0 WHERE username=?`, reader.username)
			case "grant_removed":
				mustExec(t, s.st, `DELETE FROM app_platform_access_grants WHERE installation_id=?`, id)
			case "missing_ou_creation":
				created := scalar[string](t, s.st, `SELECT created_at FROM user_groups WHERE is_default=1`)
				t.Cleanup(func() { mustExec(t, s.st, `UPDATE user_groups SET created_at=? WHERE is_default=1`, created) })
				mustExec(t, s.st, `UPDATE user_groups SET created_at='' WHERE is_default=1`)
			case "seed_missing":
				mustExec(t, s.st, `UPDATE app_platform_installations SET sealed_identity_seed='broken' WHERE id=?`, id)
			case "seed_wrong_aad":
				dek, err := s.dek()
				if err != nil {
					t.Fatal(err)
				}
				sealed, err := aesSeal(dek, bytes.Repeat([]byte{17}, 32), aad("app-platform", platformFixtureID(999)))
				if err != nil {
					t.Fatal(err)
				}
				mustExec(t, s.st, `UPDATE app_platform_installations SET sealed_identity_seed=? WHERE id=?`, sealPrefix+sealed, id)
			case "keyring_missing":
				mustExec(t, s.st, `DELETE FROM meta WHERE k IN (?,?)`, setKeyringSalt, setKeyringDEK)
			case "keyring_corrupt":
				mustExec(t, s.st, `UPDATE meta SET v='corrupt' WHERE k=?`, setKeyringDEK)
			case "previous_key":
				s.cfg.SecretKeyPrevious, s.cfg.SecretKey = s.cfg.SecretKey, "rotated-test-key"
			case "paused":
				mustExec(t, s.st, `UPDATE meta SET v='1' WHERE k='app_platform_restore_paused'`)
			case "managed":
				// Valid referenced runtime row; this does not provision anything.
				platformTestInsert(t, s.st.poolExec(), "runtime_bindings", map[string]any{"id": platformFixtureID(991), "deployment_id": platformFixtureID(992), "controller_epoch": scalar[string](t, s.st, `SELECT controller_epoch FROM app_platform_installations WHERE id=?`, id), "control_revision": 1, "enabled": 0, "verification_key_fingerprint": nil, "sealed_control_key": nil, "revision": 1, "created_ms": 1, "updated_ms": 1})
				mustExec(t, s.st, `UPDATE app_platform_installations SET runtime_id=? WHERE id=?`, platformFixtureID(991), id)
			case "cancelled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			before := dumpOf(t, s.st)
			v, err := s.readPlatformLaunchViewerContext(ctx, reader, id)
			if err == nil || !reflect.DeepEqual(v, protocol.Viewer{}) {
				t.Fatal("invalid authorization/identity emitted viewer")
			}
			if !bytes.Equal(stripHeader(before), stripHeader(dumpOf(t, s.st))) {
				t.Fatal("refused read repaired or mutated state")
			}
		})
	}
}
func TestPlatformViewerRefusalsSQLite(t *testing.T)   { testPlatformViewerRefusals(t, false) }
func TestPlatformViewerRefusalsPostgres(t *testing.T) { testPlatformViewerRefusals(t, true) }

func testPlatformViewerFinalRecheck(t *testing.T, pg bool) {
	s, reader, id := platformLaunchFixture(t, pg)
	platformGrantLaunchUser(t, s, reader, id)
	var derived protocol.Viewer
	result, err := s.withPlatformLaunchAccountReadContext(context.Background(), reader, id, func(e migExec, b platformLaunchAccess, m appplatform.Manifest, account platformActorAccount) error {
		var err error
		derived, err = s.platformViewerIdentity(e, account, b.installationID, m.ID)
		if err != nil {
			return err
		}
		_, err = e.exec(`UPDATE user_sessions SET revoked=1 WHERE id=?`, reader.sessionID)
		return err
	})
	if len(derived.Subject) != 64 || err == nil || result != (platformLaunchAccess{}) {
		t.Fatal("facts escaped a failed final session check")
	}
	if scalar[int](t, s.st, `SELECT revoked FROM user_sessions WHERE id=?`, reader.sessionID) != 0 {
		t.Fatal("failed boundary did not rollback")
	}
	if strings.Contains(derived.Subject, reader.username) {
		t.Fatal("raw principal exposed")
	}
}
func TestPlatformViewerFinalRecheckSQLite(t *testing.T)   { testPlatformViewerFinalRecheck(t, false) }
func TestPlatformViewerFinalRecheckPostgres(t *testing.T) { testPlatformViewerFinalRecheck(t, true) }
