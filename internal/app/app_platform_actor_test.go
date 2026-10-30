package app

import (
	"bytes"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/config"
)

func platformActorTestServer(t *testing.T, postgres bool) (*Server, platformActorSession) {
	t.Helper()
	return platformActorTestServerWithStore(t, platformTestStore(t, postgres))
}

func platformActorTestServerWithStore(t *testing.T, st *Store) (*Server, platformActorSession) {
	t.Helper()
	for _, key := range []string{setKeyringSalt, setKeyringDEK, "app_platform_actor_seed", "app_platform_controller_epoch", "app_platform_restore_paused"} {
		var value sql.NullString
		err := st.queryRow(`SELECT v FROM meta WHERE k=?`, key).Scan(&value)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			t.Fatal(err)
		}
		existed := err == nil
		t.Cleanup(func() {
			if existed {
				if _, err := st.exec(`INSERT INTO meta(k,v) VALUES(?,?) ON CONFLICT(k) DO UPDATE SET v=excluded.v`, key, value); err != nil {
					t.Error(err)
				}
			} else if _, err := st.exec(`DELETE FROM meta WHERE k=?`, key); err != nil {
				t.Error(err)
			}
		})
		mustExec(t, st, `DELETE FROM meta WHERE k=?`, key)
	}
	const user = "platform-review-actor"
	if err := st.UpsertUser(User{Username: user, PasswordHash: "fixture", Role: "admin"}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := st.DeleteUser(user); err != nil {
			t.Error(err)
		}
	})
	u := st.GetUser(user)
	generation, err := notificationAccountGeneration(st.poolExec(), user)
	if err != nil {
		t.Fatal(err)
	}
	actor := platformActorSession{username: user, created: u.CreatedAt, sessionID: strings.Repeat("a", 64), sessionRevision: u.SessionRev, generation: generation, expires: time.Unix(time.Now().Add(time.Hour).Unix(), 0)}
	platformSeedActorSession(t, st, actor)
	return &Server{st: st, cfg: &config.Config{SecretKey: rotOldKey}}, actor
}

func platformSeedActorSession(t *testing.T, st *Store, session platformActorSession) {
	t.Helper()
	mustExec(t, st, `INSERT INTO user_sessions(id,username,revision,created_at,last_seen,expires_at,ip,client) VALUES(?,?,?,1,1,?,'','{}') ON CONFLICT(id) DO UPDATE SET revision=excluded.revision,expires_at=excluded.expires_at,revoked=0`, session.sessionID, session.username, session.sessionRevision, session.expires.Unix())
}

func platformReadActorForTest(t *testing.T, s *Server, session platformActorSession) platformActorFacts {
	t.Helper()
	var actor platformActorFacts
	if err := s.withAppPlatformActor(session, false, func(_ migExec, facts platformActorFacts, _ platformActorControl) error { actor = facts; return nil }); err != nil {
		t.Fatal(err)
	}
	return actor
}

func testPlatformActorIdentity(t *testing.T, postgres bool) {
	s, session := platformActorTestServer(t, postgres)
	if err := s.initializeAppPlatformControl(session); err != nil {
		t.Fatal(err)
	}
	before := platformReadActorForTest(t, s, session)
	sealed := s.st.GetSetting("app_platform_actor_seed", "")
	if !strings.HasPrefix(sealed, sealPrefix) {
		t.Fatal("actor seed not encrypted")
	}
	plain, err := s.openSecret("app-platform", "actor-seed", sealed)
	if err != nil || len(plain) != 32 {
		t.Fatalf("seed cannot be recovered: %v", err)
	}
	if strings.Contains(sealed, plain) {
		t.Fatal("seed appears as plaintext")
	}
	fresh := &Server{st: s.st, cfg: s.cfg}
	if after := platformReadActorForTest(t, fresh, session); after != before {
		t.Fatal("fresh server changed actor identity")
	}
	if err := s.st.SetUserPassword(session.username, "replacement"); err != nil {
		t.Fatal(err)
	}
	if err := s.withAppPlatformActor(session, false, nil); !errors.Is(err, errAppPlatformActorUnavailable) {
		t.Fatalf("old session accepted: %v", err)
	}
	session.sessionRevision = s.st.GetUser(session.username).SessionRev
	platformSeedActorSession(t, s.st, session)
	after := platformReadActorForTest(t, s, session)
	if after.scope != before.scope || after.authorityFingerprint == before.authorityFingerprint {
		t.Fatal("session change changed lifecycle scope or failed to invalidate authority")
	}
	if err := s.st.DeleteUser(session.username); err != nil {
		t.Fatal(err)
	}
	if err := s.st.UpsertUser(User{Username: session.username, PasswordHash: "fixture", Role: "admin"}); err != nil {
		t.Fatal(err)
	}
	if err := s.withAppPlatformActor(session, false, nil); !errors.Is(err, errAppPlatformActorUnavailable) {
		t.Fatalf("recreated username accepted old proof: %v", err)
	}
	u := s.st.GetUser(session.username)
	session.created, session.sessionRevision = u.CreatedAt, u.SessionRev
	session.generation, err = notificationAccountGeneration(s.st.poolExec(), session.username)
	if err != nil {
		t.Fatal(err)
	}
	platformSeedActorSession(t, s.st, session)
	if recreated := platformReadActorForTest(t, s, session); recreated.scope == before.scope {
		t.Fatal("recreated account inherited actor scope")
	}
}

func testPlatformActorGuards(t *testing.T, postgres bool) {
	for _, name := range []string{"reader", "inactive", "expired_session", "revoked_session", "paused", "missing_seed", "foreign_seed", "missing_epoch", "foreign_state"} {
		t.Run(name, func(t *testing.T) {
			s, session := platformActorTestServer(t, postgres)
			if err := s.initializeAppPlatformControl(session); err != nil {
				t.Fatal(err)
			}
			want := errAppPlatformControlPaused
			switch name {
			case "reader":
				mustExec(t, s.st, `UPDATE users SET role='user' WHERE username=?`, session.username)
				want = errAppPlatformActorUnavailable
			case "inactive":
				mustExec(t, s.st, `UPDATE users SET active=0 WHERE username=?`, session.username)
				want = errAppPlatformActorUnavailable
			case "expired_session":
				session.expires = time.Unix(1, 0)
				want = errAppPlatformActorUnavailable
			case "paused":
				mustExec(t, s.st, `UPDATE meta SET v='1' WHERE k='app_platform_restore_paused'`)
			case "revoked_session":
				mustExec(t, s.st, `UPDATE user_sessions SET revoked=1 WHERE id=?`, session.sessionID)
				want = errAppPlatformActorUnavailable
			case "missing_seed":
				mustExec(t, s.st, `DELETE FROM meta WHERE k='app_platform_actor_seed'`)
			case "missing_epoch":
				mustExec(t, s.st, `DELETE FROM meta WHERE k='app_platform_controller_epoch'`)
			case "foreign_seed":
				sealed, err := s.sealSecret("other-platform", "actor-seed", strings.Repeat("x", 32))
				if err != nil {
					t.Fatal(err)
				}
				mustExec(t, s.st, `UPDATE meta SET v=? WHERE k='app_platform_actor_seed'`, sealed)
			case "foreign_state":
				mustExec(t, s.st, `DELETE FROM meta WHERE k IN ('app_platform_actor_seed','app_platform_controller_epoch','app_platform_restore_paused')`)
				platformSeedPublisher(t, s.st.poolExec(), "fixture-publisher")
				if err := platformSeedInstallation(t, s.st.poolExec(), platformFixtureID(1), "fixture-publisher", "fixture-app"); err != nil {
					t.Fatal(err)
				}
			}
			before := dumpOf(t, s.st)
			if err := s.initializeAppPlatformControl(session); !errors.Is(err, want) {
				t.Fatalf("guard error: %v, want %v", err, want)
			}
			if !bytes.Equal(stripHeader(before), stripHeader(dumpOf(t, s.st))) {
				t.Fatal("refused initialization mutated authority")
			}
		})
	}
}

func TestAppPlatformActorIdentity(t *testing.T)         { testPlatformActorIdentity(t, false) }
func TestPostgresAppPlatformActorIdentity(t *testing.T) { testPlatformActorIdentity(t, true) }
func TestAppPlatformActorGuards(t *testing.T)           { testPlatformActorGuards(t, false) }
func TestPostgresAppPlatformActorGuards(t *testing.T)   { testPlatformActorGuards(t, true) }

func testPlatformActorAuthorityContinuity(t *testing.T, postgres bool) {
	s, session := platformActorTestServer(t, postgres)
	if err := s.initializeAppPlatformControl(session); err != nil {
		t.Fatal(err)
	}
	before := platformReadActorForTest(t, s, session)
	group, err := s.st.CreateUserGroup("Platform review authority fixture", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := s.st.SetPrimaryGroups([]string{session.username}, 0); err != nil {
			t.Error(err)
		}
		if _, _, err := s.st.DeleteUserGroup(group); err != nil {
			t.Error(err)
		}
	})
	if _, err := s.st.SetPrimaryGroups([]string{session.username}, group); err != nil {
		t.Fatal(err)
	}
	moved := platformReadActorForTest(t, s, session)
	if moved.scope != before.scope || moved.authorityFingerprint == before.authorityFingerprint {
		t.Fatal("OU change changed lifecycle identity or preserved stale authority")
	}
	seed := s.st.GetSetting("app_platform_actor_seed", "")
	rotated := &Server{st: s.st, cfg: &config.Config{SecretKey: rotNewKey, SecretKeyPrevious: rotOldKey}}
	if current := platformReadActorForTest(t, rotated, session); current != moved {
		t.Fatal("keyring rewrap rotated actor identity")
	}
	if seed != s.st.GetSetting("app_platform_actor_seed", "") {
		t.Fatal("keyring rewrap rewrote the actor seed")
	}
}

func TestAppPlatformActorAuthorityContinuity(t *testing.T) {
	testPlatformActorAuthorityContinuity(t, false)
}
func TestPostgresAppPlatformActorAuthorityContinuity(t *testing.T) {
	testPlatformActorAuthorityContinuity(t, true)
}

func TestAppPlatformActorInitializationRollback(t *testing.T) {
	s, session := platformActorTestServer(t, false)
	if _, err := s.dek(); err != nil {
		t.Fatal(err)
	}
	mustExec(t, s.st, `CREATE TRIGGER fixture_control_initialization_failure BEFORE INSERT ON meta WHEN NEW.k='app_platform_controller_epoch' BEGIN SELECT RAISE(ABORT,'fixture control failure');END`)
	before := dumpOf(t, s.st)
	if err := s.initializeAppPlatformControl(session); err == nil {
		t.Fatal("injected control initialization error ignored")
	}
	if !bytes.Equal(stripHeader(before), stripHeader(dumpOf(t, s.st))) {
		t.Fatal("failed initialization left partial control authority")
	}
}
