package app

import (
	"bytes"
	"encoding/json"
	"errors"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/config"
	"strings"
	"testing"
)

func testPlatformCredentialCustody(t *testing.T, pg bool) {
	s, actor := platformActorTestServer(t, pg)
	if err := s.initializeAppPlatformControl(actor); err != nil {
		t.Fatal(err)
	}
	secret := []byte("disposable-credential-value")
	created, err := s.changePlatformCredential(actor, platformCredentialChange{kind: "create", label: "Fixture", value: secret})
	if err != nil {
		t.Fatal(err)
	}
	if created.Revision != 1 || created.ValueRevision != 1 || created.State != "active" {
		t.Fatal("wrong initial metadata")
	}
	var sealed string
	if err := s.st.queryRow(`SELECT sealed_value FROM app_platform_credentials WHERE id=?`, created.ID).Scan(&sealed); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(sealed, sealPrefix) || strings.Contains(sealed, string(secret)) {
		t.Fatal("value was not sealed")
	}
	plain, err := s.openPlatformCredentialValue(created.ID, 1, sealed)
	if err != nil || !bytes.Equal(plain, secret) {
		t.Fatal("cannot recover sealed value", err)
	}
	clear(plain)
	for _, tc := range []struct {
		id       string
		revision int64
		sealed   string
	}{{platformFixtureID(902), 1, sealed}, {created.ID, 2, sealed}, {created.ID, 1, string(secret)}, {created.ID, 1, sealPrefix + "invalid"}} {
		if out, err := s.openPlatformCredentialValue(tc.id, tc.revision, tc.sealed); err == nil || len(out) != 0 {
			t.Fatal("invalid ciphertext/context returned value")
		}
	}
	bad := &Server{st: s.st, cfg: &config.Config{SecretKey: "wrong-disposable-test-key"}}
	if out, err := bad.openPlatformCredentialValue(created.ID, 1, sealed); !errors.Is(err, errAppPlatformCredentialUnavailable) || len(out) != 0 {
		t.Fatal("missing/wrong custody key did not fail closed")
	}
	page, err := s.listPlatformCredentials(actor, "", 100)
	if err != nil || len(page) != 1 {
		t.Fatal("metadata list", err)
	}
	encoded, _ := json.Marshal(page)
	if strings.Contains(string(encoded), string(secret)) || strings.Contains(string(encoded), sealed) || strings.Contains(string(encoded), "sealed_value") {
		t.Fatal("metadata exposed value")
	}
	// Mutation values are never serializable generic request/diagnostic objects.
	if _, err := json.Marshal(platformCredentialChange{value: secret}); err == nil {
		t.Fatal("custody change serialized")
	}
	old := dumpOf(t, s.st)
	if _, err := s.changePlatformCredential(actor, platformCredentialChange{kind: "replace", id: created.ID, revision: 2, valueRevision: 1, value: []byte("replacement")}); !errors.Is(err, errAppPlatformCredentialUnavailable) {
		t.Fatal("stale edit accepted", err)
	}
	if !bytes.Equal(stripHeader(old), stripHeader(dumpOf(t, s.st))) {
		t.Fatal("refused change wrote")
	}
	replaced, err := s.changePlatformCredential(actor, platformCredentialChange{kind: "replace", id: created.ID, revision: 1, valueRevision: 1, value: []byte("replacement")})
	if err != nil || replaced.ValueRevision != 2 || replaced.Revision != 2 {
		t.Fatal("replace", err)
	}
	revoked, err := s.changePlatformCredential(actor, platformCredentialChange{kind: "revoke", id: created.ID, revision: 2, valueRevision: 2})
	if err != nil || revoked.State != "revoked" || revoked.ValueRevision != 3 {
		t.Fatal("revoke", err)
	}
	if scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_credentials WHERE sealed_value IS NOT NULL`) != 0 {
		t.Fatal("revoke kept ciphertext")
	}
	if _, err := s.changePlatformCredential(actor, platformCredentialChange{kind: "replace", id: created.ID, revision: 3, valueRevision: 3, value: secret}); err == nil {
		t.Fatal("revoked identity reused")
	}
	mustExec(t, s.st, `UPDATE users SET role='user' WHERE username=?`, actor.username)
	if _, err := s.listPlatformCredentials(actor, "", 100); err == nil {
		t.Fatal("demoted admin enumerated custody")
	}
	if _, err := s.changePlatformCredential(actor, platformCredentialChange{kind: "create", label: "Other", value: secret}); err == nil {
		t.Fatal("demoted admin created custody")
	}
}
func TestPlatformCredentialCustodySQLite(t *testing.T)   { testPlatformCredentialCustody(t, false) }
func TestPlatformCredentialCustodyPostgres(t *testing.T) { testPlatformCredentialCustody(t, true) }

func testPlatformCredentialCustodyRefusals(t *testing.T, pg bool) {
	s, actor := platformActorTestServer(t, pg)
	if err := s.initializeAppPlatformControl(actor); err != nil {
		t.Fatal(err)
	}
	for _, change := range []platformCredentialChange{{kind: "create", label: "", value: []byte("x")}, {kind: "create", label: "Fixture", value: bytes.Repeat([]byte("x"), 16385)}, {kind: "create", label: "Fixture"}, {kind: "create", label: "bad\nlabel", value: []byte("x")}} {
		before := dumpOf(t, s.st)
		if _, err := s.changePlatformCredential(actor, change); err == nil {
			t.Fatal("invalid custody input accepted")
		}
		if !bytes.Equal(stripHeader(before), stripHeader(dumpOf(t, s.st))) {
			t.Fatal("invalid input wrote")
		}
	}
	// Rows seeded solely to exercise retained-record capacity. No seeded value is
	// decryptable; this fixture never admits an app operation.
	tx, err := s.st.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 256; i++ {
		_, err := tx.Exec(s.st.bind(`INSERT INTO app_platform_credentials(id,label,state,sealed_value,value_revision,revision,created_ms,updated_ms,revoked_ms) VALUES(?,'Retained','revoked',NULL,1,1,1,1,1)`), platformFixtureID(3000+i))
		if err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	before := dumpOf(t, s.st)
	if _, err := s.changePlatformCredential(actor, platformCredentialChange{kind: "create", label: "Over capacity", value: []byte("x")}); err == nil {
		t.Fatal("revoked records did not count toward capacity")
	}
	if !bytes.Equal(stripHeader(before), stripHeader(dumpOf(t, s.st))) {
		t.Fatal("capacity failure wrote")
	}
	page, err := s.listPlatformCredentials(actor, "", 100)
	if err != nil || len(page) != 100 {
		t.Fatal("page one", err)
	}
	next, err := s.listPlatformCredentials(actor, page[99].ID, 100)
	if err != nil || len(next) != 100 || next[0].ID <= page[99].ID {
		t.Fatal("page two", err)
	}
	if _, err := s.listPlatformCredentials(actor, "", 101); err == nil {
		t.Fatal("oversized metadata page accepted")
	}
}
func TestPlatformCredentialCustodyRefusalsSQLite(t *testing.T) {
	testPlatformCredentialCustodyRefusals(t, false)
}
func TestPlatformCredentialCustodyRefusalsPostgres(t *testing.T) {
	testPlatformCredentialCustodyRefusals(t, true)
}

func testPlatformCredentialCustodyAuditRollback(t *testing.T, pg bool) {
	s, actor := platformActorTestServer(t, pg)
	if err := s.initializeAppPlatformControl(actor); err != nil {
		t.Fatal(err)
	}
	created, err := s.changePlatformCredential(actor, platformCredentialChange{kind: "create", label: "Fixture", value: []byte("fixture-value")})
	if err != nil {
		t.Fatal(err)
	}
	if pg {
		mustExec(t, s.st, `CREATE FUNCTION fixture_credential_audit_failure() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='app_platform.credential_changed' THEN RAISE EXCEPTION 'fixture audit failure'; END IF; RETURN NEW; END $$`)
		mustExec(t, s.st, `CREATE TRIGGER fixture_credential_audit_failure BEFORE INSERT ON audit_log FOR EACH ROW EXECUTE FUNCTION fixture_credential_audit_failure()`)
		t.Cleanup(func() {
			mustExec(t, s.st, `DROP TRIGGER IF EXISTS fixture_credential_audit_failure ON audit_log`)
			mustExec(t, s.st, `DROP FUNCTION IF EXISTS fixture_credential_audit_failure()`)
		})
	} else {
		mustExec(t, s.st, `CREATE TRIGGER fixture_credential_audit_failure BEFORE INSERT ON audit_log WHEN NEW.action='app_platform.credential_changed' BEGIN SELECT RAISE(ABORT,'fixture audit failure');END`)
	}
	before := dumpOf(t, s.st)
	if _, err := s.changePlatformCredential(actor, platformCredentialChange{kind: "revoke", id: created.ID, revision: 1, valueRevision: 1}); err == nil {
		t.Fatal("failed audit committed")
	}
	if !bytes.Equal(stripHeader(before), stripHeader(dumpOf(t, s.st))) {
		t.Fatal("audit failure left credential changed")
	}
}
func TestPlatformCredentialCustodyAuditRollbackSQLite(t *testing.T) {
	testPlatformCredentialCustodyAuditRollback(t, false)
}
func TestPlatformCredentialCustodyAuditRollbackPostgres(t *testing.T) {
	testPlatformCredentialCustodyAuditRollback(t, true)
}

func testPlatformCredentialBoundChangeDenied(t *testing.T, pg bool) {
	s, actor := platformActorTestServer(t, pg)
	if err := s.initializeAppPlatformControl(actor); err != nil {
		t.Fatal(err)
	}
	created, err := s.changePlatformCredential(actor, platformCredentialChange{kind: "create", label: "Bound fixture", value: []byte("disposable")})
	if err != nil {
		t.Fatal(err)
	}
	platformSeedCredentialRecords(t, s.st)
	mustExec(t, s.st, `UPDATE app_platform_secret_bindings SET credential_id=?`, created.ID)
	for _, kind := range []string{"replace", "revoke"} {
		change := platformCredentialChange{kind: kind, id: created.ID, revision: 1, valueRevision: 1}
		if kind == "replace" {
			change.value = []byte("replacement")
		}
		before := dumpOf(t, s.st)
		if _, err := s.changePlatformCredential(actor, change); err == nil {
			t.Fatal("bound change bypassed affected-installation review")
		}
		if !bytes.Equal(stripHeader(before), stripHeader(dumpOf(t, s.st))) {
			t.Fatal("unreviewed bound change wrote")
		}
	}
}
func TestPlatformCredentialBoundChangeDeniedSQLite(t *testing.T) {
	testPlatformCredentialBoundChangeDenied(t, false)
}
func TestPlatformCredentialBoundChangeDeniedPostgres(t *testing.T) {
	testPlatformCredentialBoundChangeDenied(t, true)
}

func testPlatformCredentialConcurrentMutation(t *testing.T, pg bool) {
	s, actor := platformActorTestServer(t, pg)
	if err := s.initializeAppPlatformControl(actor); err != nil {
		t.Fatal(err)
	}
	created, err := s.changePlatformCredential(actor, platformCredentialChange{kind: "create", label: "Concurrent fixture", value: []byte("initial")})
	if err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	for _, kind := range []string{"replace", "revoke"} {
		go func(kind string) {
			<-start
			c := platformCredentialChange{kind: kind, id: created.ID, revision: 1, valueRevision: 1}
			if kind == "replace" {
				c.value = []byte("replacement")
			}
			_, err := s.changePlatformCredential(actor, c)
			results <- err
		}(kind)
	}
	close(start)
	success := 0
	for range 2 {
		if err := <-results; err == nil {
			success++
		} else if !errors.Is(err, errAppPlatformCredentialUnavailable) {
			t.Fatal("unexpected concurrent failure", err)
		}
	}
	if success != 1 || scalar[int](t, s.st, `SELECT revision FROM app_platform_credentials`) != 2 || scalar[int](t, s.st, `SELECT value_revision FROM app_platform_credentials`) != 2 || scalar[int](t, s.st, `SELECT COUNT(*) FROM audit_log WHERE action='app_platform.credential_changed'`) != 2 {
		t.Fatal("concurrent stale mutation committed")
	}
}
func TestPlatformCredentialConcurrentMutationSQLite(t *testing.T) {
	testPlatformCredentialConcurrentMutation(t, false)
}
func TestPlatformCredentialConcurrentMutationPostgres(t *testing.T) {
	testPlatformCredentialConcurrentMutation(t, true)
}
