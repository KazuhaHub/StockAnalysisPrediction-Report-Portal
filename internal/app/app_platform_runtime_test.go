package app

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/journal"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/config"
)

func runtimeRegistrationFixture(t *testing.T, pg bool) (*Server, platformActorSession) {
	t.Helper()
	s, session := platformActorTestServer(t, pg)
	if err := s.initializeAppPlatformControl(session); err != nil {
		t.Fatal(err)
	}
	return s, session
}
func testPlatformRuntimeRegistration(t *testing.T, pg bool) {
	s, session := runtimeRegistrationFixture(t, pg)
	auditsBefore := scalar[int](t, s.st, `SELECT COUNT(*) FROM audit_log WHERE action='app_platform.runtime_prepared'`)
	deployment := platformFixtureID(400)
	first, err := s.preparePlatformRuntimeBinding(session, deployment)
	if err != nil {
		t.Fatal(err)
	}
	if !platformValidUUID(first.id) || first.deploymentID != deployment || first.controlRevision != 1 || first.revision != 1 || len(first.publicKey) != 32 || first.controllerEpoch != s.st.GetSetting("app_platform_controller_epoch", "") {
		t.Fatalf("invalid public registration: %+v", first)
	}
	sum := sha256.Sum256(first.publicKey)
	if hex.EncodeToString(sum[:]) != first.fingerprint {
		t.Fatal("public key pin mismatch")
	}
	if scalar[int](t, s.st, `SELECT enabled FROM app_platform_runtime_bindings`) != 0 {
		t.Fatal("registration enabled a runtime")
	}
	sealed := scalar[string](t, s.st, `SELECT sealed_control_key FROM app_platform_runtime_bindings`)
	seed, err := s.openSecret("app-platform", "runtime-control:"+first.id, sealed)
	if err != nil || len(seed) != 32 {
		t.Fatal("control seed not purpose sealed")
	}
	public := ed25519.NewKeyFromSeed([]byte(seed)).Public().(ed25519.PublicKey)
	if !bytes.Equal(public, first.publicKey) {
		t.Fatal("exported wrong enrollment key")
	}
	if _, err := s.openSecret("app-platform", "runtime-control:"+platformFixtureID(999), sealed); err == nil {
		t.Fatal("runtime key opened for another identity")
	}
	if strings.Contains(sealed, seed) {
		t.Fatal("stored clear control key")
	}
	repeat, err := s.preparePlatformRuntimeBinding(session, deployment)
	if err != nil || !reflect.DeepEqual(first, repeat) {
		t.Fatalf("retry replaced control identity: %v", err)
	}
	first.publicKey[0] ^= 1
	unchanged, err := s.preparePlatformRuntimeBinding(session, deployment)
	if err != nil || !reflect.DeepEqual(repeat, unchanged) {
		t.Fatal("caller mutation changed key")
	}
	if scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_runtime_bindings`) != 1 || scalar[int](t, s.st, `SELECT COUNT(*) FROM audit_log WHERE action='app_platform.runtime_prepared'`) != auditsBefore+1 {
		t.Fatal("retry duplicated binding/audit")
	}
	// This is an explicit isolated enrollment, not automatic registration side effect.
	dir := filepath.Join(t.TempDir(), "runtime-journal")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	dir, err = filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	j, err := journal.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	enrolled, created, err := j.EnrollController(context.Background(), journal.ControllerEnrollment{DeploymentID: repeat.deploymentID, ControllerEpoch: repeat.controllerEpoch, ControlRevision: repeat.controlRevision, PublicKey: repeat.publicKey, Fingerprint: repeat.fingerprint})
	if err != nil || !created || enrolled.Fingerprint != repeat.fingerprint {
		t.Fatalf("Portal public registration incompatible with runner: %v", err)
	}
}
func TestPlatformRuntimeRegistrationSQLite(t *testing.T)   { testPlatformRuntimeRegistration(t, false) }
func TestPlatformRuntimeRegistrationPostgres(t *testing.T) { testPlatformRuntimeRegistration(t, true) }

func testPlatformRuntimeRefusals(t *testing.T, pg bool) {
	for _, kind := range []string{"other_deployment", "enabled", "epoch", "missing_key", "bad_pin", "foreign_seal", "paused", "demoted"} {
		t.Run(kind, func(t *testing.T) {
			s, session := runtimeRegistrationFixture(t, pg)
			first, err := s.preparePlatformRuntimeBinding(session, platformFixtureID(400))
			if err != nil {
				t.Fatal(err)
			}
			deployment := first.deploymentID
			switch kind {
			case "other_deployment":
				deployment = platformFixtureID(401)
			case "enabled":
				mustExec(t, s.st, `UPDATE app_platform_runtime_bindings SET enabled=1`)
			case "epoch":
				mustExec(t, s.st, `UPDATE app_platform_runtime_bindings SET controller_epoch=?`, platformFixtureID(999))
			case "missing_key":
				mustExec(t, s.st, `UPDATE app_platform_runtime_bindings SET sealed_control_key=NULL`)
			case "bad_pin":
				mustExec(t, s.st, `UPDATE app_platform_runtime_bindings SET verification_key_fingerprint=?`, strings.Repeat("a", 64))
			case "foreign_seal":
				sealed, err := s.sealSecret("other-app", "runtime-control:"+first.id, strings.Repeat("x", 32))
				if err != nil {
					t.Fatal(err)
				}
				mustExec(t, s.st, `UPDATE app_platform_runtime_bindings SET sealed_control_key=?`, sealed)
			case "paused":
				mustExec(t, s.st, `UPDATE meta SET v='1' WHERE k='app_platform_restore_paused'`)
			case "demoted":
				mustExec(t, s.st, `UPDATE users SET role='user' WHERE username=?`, session.username)
			}
			before := dumpOf(t, s.st)
			result, err := s.preparePlatformRuntimeBinding(session, deployment)
			if err == nil || !reflect.DeepEqual(result, platformRuntimeRegistration{}) {
				t.Fatal("invalid registration exported authority")
			}
			if !bytes.Equal(stripHeader(before), stripHeader(dumpOf(t, s.st))) {
				t.Fatal("refusal changed binding/key")
			}
		})
	}
}
func TestPlatformRuntimeRefusalsSQLite(t *testing.T)   { testPlatformRuntimeRefusals(t, false) }
func TestPlatformRuntimeRefusalsPostgres(t *testing.T) { testPlatformRuntimeRefusals(t, true) }

func testPlatformRuntimeConcurrentRegistration(t *testing.T, pg bool) {
	s, session := runtimeRegistrationFixture(t, pg)
	results := make(chan platformRuntimeRegistration, 8)
	errs := make(chan error, 8)
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			r, err := s.preparePlatformRuntimeBinding(session, platformFixtureID(400))
			results <- r
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
	var first platformRuntimeRegistration
	for result := range results {
		if first.id == "" {
			first = result
		}
		if !reflect.DeepEqual(first, result) {
			t.Fatal("concurrent registration split identity")
		}
	}
	if scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_runtime_bindings`) != 1 {
		t.Fatal("duplicate runtime")
	}
}
func TestPlatformRuntimeConcurrentSQLite(t *testing.T) {
	testPlatformRuntimeConcurrentRegistration(t, false)
}
func TestPlatformRuntimeConcurrentPostgres(t *testing.T) {
	testPlatformRuntimeConcurrentRegistration(t, true)
}

func TestPlatformRuntimeRequiresExplicitControlInitialization(t *testing.T) {
	s, session := platformActorTestServer(t, false)
	if _, err := s.preparePlatformRuntimeBinding(session, platformFixtureID(400)); !errors.Is(err, errAppPlatformControlPaused) {
		t.Fatalf("implicitly initialized control: %v", err)
	}
	if scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_runtime_bindings`) != 0 {
		t.Fatal("created runtime without control initialization")
	}
}

func testPlatformRuntimeAuditRollback(t *testing.T, pg bool) {
	s, session := runtimeRegistrationFixture(t, pg)
	if pg {
		mustExec(t, s.st, `CREATE FUNCTION fixture_runtime_audit_failure() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='app_platform.runtime_prepared' THEN RAISE EXCEPTION 'fixture runtime failure'; END IF; RETURN NEW; END $$`)
		mustExec(t, s.st, `CREATE TRIGGER fixture_runtime_audit_failure BEFORE INSERT ON audit_log FOR EACH ROW EXECUTE FUNCTION fixture_runtime_audit_failure()`)
		t.Cleanup(func() {
			if _, err := s.st.exec(`DROP TRIGGER IF EXISTS fixture_runtime_audit_failure ON audit_log`); err != nil {
				t.Error(err)
			}
			if _, err := s.st.exec(`DROP FUNCTION IF EXISTS fixture_runtime_audit_failure()`); err != nil {
				t.Error(err)
			}
		})
	} else {
		mustExec(t, s.st, `CREATE TRIGGER fixture_runtime_audit_failure BEFORE INSERT ON audit_log WHEN NEW.action='app_platform.runtime_prepared' BEGIN SELECT RAISE(ABORT,'fixture runtime failure');END`)
	}
	before := dumpOf(t, s.st)
	result, err := s.preparePlatformRuntimeBinding(session, platformFixtureID(400))
	if err == nil || !reflect.DeepEqual(result, platformRuntimeRegistration{}) {
		t.Fatal("audit failure exported enrollment material")
	}
	if !bytes.Equal(stripHeader(before), stripHeader(dumpOf(t, s.st))) {
		t.Fatal("audit failure left binding or key")
	}
}
func TestPlatformRuntimeAuditRollbackSQLite(t *testing.T) { testPlatformRuntimeAuditRollback(t, false) }
func TestPlatformRuntimeAuditRollbackPostgres(t *testing.T) {
	testPlatformRuntimeAuditRollback(t, true)
}

func TestPostgresRuntimeRegistrationExpiryAfterAuditWait(t *testing.T) {
	s, session := runtimeRegistrationFixture(t, true)
	auditsBefore := scalar[int](t, s.st, `SELECT COUNT(*) FROM audit_log WHERE action='app_platform.runtime_prepared'`)
	now, err := platformDatabaseNow(s.st.poolExec())
	if err != nil {
		t.Fatal(err)
	}
	session.expires = time.UnixMilli(now + 2500).Truncate(time.Second)
	platformSeedActorSession(t, s.st, session)
	holder, err := s.st.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Rollback()
	if _, err := holder.Exec(`LOCK TABLE audit_log IN SHARE MODE`); err != nil {
		t.Fatal(err)
	}
	type outcome struct {
		result platformRuntimeRegistration
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		r, err := s.preparePlatformRuntimeBinding(session, platformFixtureID(400))
		done <- outcome{r, err}
	}()
	blocked := false
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var n int
		if err := s.st.queryRow(`SELECT COUNT(*) FROM pg_stat_activity WHERE datname=current_database() AND pid<>pg_backend_pid() AND wait_event_type='Lock' AND query LIKE '%INSERT INTO audit_log%'`).Scan(&n); err != nil {
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
		t.Fatalf("registration did not reach audit lock: %+v", <-done)
	}
	for {
		now, err = platformDatabaseNow(s.st.poolExec())
		if err != nil {
			t.Fatal(err)
		}
		if now >= session.expires.UnixMilli() {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if err := holder.Commit(); err != nil {
		t.Fatal(err)
	}
	outcomeResult := <-done
	if !errors.Is(outcomeResult.err, errAppPlatformActorUnavailable) || !reflect.DeepEqual(outcomeResult.result, platformRuntimeRegistration{}) {
		t.Fatalf("expired actor exported identity: %v", outcomeResult.err)
	}
	if scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_runtime_bindings`) != 0 {
		t.Fatal("expired registration committed a binding")
	}
	if scalar[int](t, s.st, `SELECT COUNT(*) FROM audit_log WHERE action='app_platform.runtime_prepared'`) != auditsBefore {
		t.Fatal("expired registration committed audit")
	}
}

func TestPlatformRuntimeAcrossIndependentSQLiteStores(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.db")
	first, err := OpenStore("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { first.Close() })
	s, session := platformActorTestServerWithStore(t, first)
	if err := s.initializeAppPlatformControl(session); err != nil {
		t.Fatal(err)
	}
	second, err := OpenStore("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	other := &Server{st: second, cfg: s.cfg}
	results := make(chan platformRuntimeRegistration, 2)
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for _, server := range []*Server{s, other} {
		wg.Go(func() {
			r, err := server.preparePlatformRuntimeBinding(session, platformFixtureID(400))
			results <- r
			errs <- err
		})
	}
	wg.Wait()
	for range 2 {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	expected := <-results
	if got := <-results; !reflect.DeepEqual(got, expected) {
		t.Fatal("independent stores split pending key")
	}
	reopened, err := OpenStore("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	fresh := &Server{st: reopened, cfg: s.cfg}
	got, err := fresh.preparePlatformRuntimeBinding(session, platformFixtureID(400))
	if err != nil || !reflect.DeepEqual(expected, got) {
		t.Fatalf("reopen replaced identity: %v", err)
	}
}

func TestPlatformRuntimeKeyringRewrapPreservesIdentity(t *testing.T) {
	s, session := runtimeRegistrationFixture(t, false)
	expected, err := s.preparePlatformRuntimeBinding(session, platformFixtureID(400))
	if err != nil {
		t.Fatal(err)
	}
	sealed := scalar[string](t, s.st, `SELECT sealed_control_key FROM app_platform_runtime_bindings`)
	rotated := &Server{st: s.st, cfg: &config.Config{SecretKey: rotNewKey, SecretKeyPrevious: rotOldKey}}
	got, err := rotated.preparePlatformRuntimeBinding(session, expected.deploymentID)
	if err != nil || !reflect.DeepEqual(got, expected) {
		t.Fatalf("keyring rewrap replaced controller: %v", err)
	}
	if sealed != scalar[string](t, s.st, `SELECT sealed_control_key FROM app_platform_runtime_bindings`) {
		t.Fatal("keyring rewrap rewrote runtime seed")
	}
}
func testPlatformRestoredRuntimeDoesNotRegenerate(t *testing.T, pg bool) {
	s, session := runtimeRegistrationFixture(t, pg)
	original, err := s.preparePlatformRuntimeBinding(session, platformFixtureID(400))
	if err != nil {
		t.Fatal(err)
	}
	snapshot := dumpOf(t, s.st)
	if _, err := s.st.restoreFrom(bytes.NewReader(snapshot), true); err != nil {
		t.Fatal(err)
	}
	if _, err := s.preparePlatformRuntimeBinding(session, original.deploymentID); err == nil {
		t.Fatal("restored paused runtime exported fresh material")
	}
	// Even an operator resuming the global marker and a new current login cannot
	// turn an old empty-key binding into a new initial enrollment automatically.
	current := s.st.GetUser(session.username)
	session.created = current.CreatedAt
	session.sessionRevision = current.SessionRev
	generation, err := notificationAccountGeneration(s.st.poolExec(), session.username)
	if err != nil {
		t.Fatal(err)
	}
	session.generation = generation
	platformSeedActorSession(t, s.st, session)
	mustExec(t, s.st, `UPDATE meta SET v='0' WHERE k='app_platform_restore_paused'`)
	before := dumpOf(t, s.st)
	result, err := s.preparePlatformRuntimeBinding(session, original.deploymentID)
	if !errors.Is(err, errAppPlatformRuntimeConflict) || !reflect.DeepEqual(result, platformRuntimeRegistration{}) {
		t.Fatalf("restored key regenerated: %v", err)
	}
	if !bytes.Equal(stripHeader(before), stripHeader(dumpOf(t, s.st))) {
		t.Fatal("restored registration changed paused identity")
	}
	if scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_runtime_bindings WHERE sealed_control_key IS NOT NULL OR enabled<>0`) != 0 {
		t.Fatal("restored runtime obtained signing authority")
	}
}
func TestPlatformRuntimeRestoreSQLite(t *testing.T) {
	testPlatformRestoredRuntimeDoesNotRegenerate(t, false)
}
func TestPlatformRuntimeRestorePostgres(t *testing.T) {
	testPlatformRestoredRuntimeDoesNotRegenerate(t, true)
}
