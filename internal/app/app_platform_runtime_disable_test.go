package app

import (
	"bytes"
	"errors"
	"math"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"
)

func runtimeConfirmedFixture(t *testing.T, pg bool) (*Server, platformActorSession, platformRuntimeRegistration) {
	t.Helper()
	s, session := runtimeRegistrationFixture(t, pg)
	original, err := s.preparePlatformRuntimeBinding(session, platformFixtureID(400))
	if err != nil {
		t.Fatal(err)
	}
	confirmed, err := s.confirmPlatformObservedRuntime(session, original, runtimeObservationFixture(original))
	if err != nil {
		t.Fatal(err)
	}
	return s, session, confirmed
}
func testPlatformRuntimeRevocation(t *testing.T, pg bool) {
	for _, pending := range []bool{false, true} {
		t.Run(map[bool]string{false: "confirmed", true: "pending"}[pending], func(t *testing.T) {
			s, session := runtimeRegistrationFixture(t, pg)
			initial, err := s.preparePlatformRuntimeBinding(session, platformFixtureID(400))
			if err != nil {
				t.Fatal(err)
			}
			expected := initial
			if !pending {
				expected, err = s.confirmPlatformObservedRuntime(session, initial, runtimeObservationFixture(initial))
				if err != nil {
					t.Fatal(err)
				}
			}
			audits := scalar[int](t, s.st, `SELECT COUNT(*) FROM audit_log WHERE action='app_platform.runtime_control_revoked'`)
			result, err := s.revokePlatformRuntimeControl(session, expected.id, expected.revision)
			if err != nil {
				t.Fatal(err)
			}
			if result.id != expected.id || result.revision != expected.revision+1 || result.controlRevision != 2 {
				t.Fatalf("incorrect revocation: %+v", result)
			}
			if scalar[int](t, s.st, `SELECT enabled FROM app_platform_runtime_bindings`) != 0 || scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_runtime_bindings WHERE sealed_control_key IS NOT NULL`) != 0 {
				t.Fatal("revocation retained signing authority")
			}
			if scalar[string](t, s.st, `SELECT verification_key_fingerprint FROM app_platform_runtime_bindings`) != initial.fingerprint {
				t.Fatal("revocation discarded public reconciliation pin")
			}
			repeat, err := s.revokePlatformRuntimeControl(session, expected.id, expected.revision)
			if err != nil || repeat != result {
				t.Fatalf("lost reply retry changed revocation: %v", err)
			}
			if scalar[int](t, s.st, `SELECT COUNT(*) FROM audit_log WHERE action='app_platform.runtime_control_revoked'`) != audits+1 {
				t.Fatal("duplicate revocation audit")
			}
			if _, err := s.preparePlatformRuntimeBinding(session, initial.deploymentID); !errors.Is(err, errAppPlatformRuntimeConflict) {
				t.Fatalf("revoked identity regenerated: %v", err)
			}
			if _, err := s.confirmPlatformObservedRuntime(session, initial, runtimeObservationFixture(initial)); !errors.Is(err, errAppPlatformRuntimeConflict) {
				t.Fatalf("old live observation reenabled revoked runtime: %v", err)
			}
			if _, err := s.revokePlatformRuntimeControl(session, result.id, result.revision); !errors.Is(err, errAppPlatformRuntimeConflict) {
				t.Fatal("already revoked binding advanced again")
			}
			if scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_operations`)+scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_installations`) != 0 {
				t.Fatal("revocation claimed physical lifecycle work")
			}
		})
	}
}
func TestPlatformRuntimeRevocationSQLite(t *testing.T)   { testPlatformRuntimeRevocation(t, false) }
func TestPlatformRuntimeRevocationPostgres(t *testing.T) { testPlatformRuntimeRevocation(t, true) }
func testPlatformRuntimeRevocationRefusals(t *testing.T, pg bool) {
	for _, kind := range []string{"bad_id", "stale_revision", "zero_revision", "revision_overflow", "control_overflow", "epoch", "paused", "demoted", "foreign_revocation"} {
		t.Run(kind, func(t *testing.T) {
			s, session, expected := runtimeConfirmedFixture(t, pg)
			id, revision := expected.id, expected.revision
			switch kind {
			case "bad_id":
				id = "invalid"
			case "stale_revision":
				revision++
			case "zero_revision":
				revision = 0
			case "revision_overflow":
				revision = math.MaxInt64
				mustExec(t, s.st, `UPDATE app_platform_runtime_bindings SET revision=?`, revision)
			case "control_overflow":
				mustExec(t, s.st, `UPDATE app_platform_runtime_bindings SET control_revision=?`, int64(math.MaxInt64))
			case "epoch":
				mustExec(t, s.st, `UPDATE app_platform_runtime_bindings SET controller_epoch=?`, platformFixtureID(999))
			case "paused":
				mustExec(t, s.st, `UPDATE meta SET v='1' WHERE k='app_platform_restore_paused'`)
			case "demoted":
				mustExec(t, s.st, `UPDATE users SET role='user' WHERE username=?`, session.username)
			case "foreign_revocation":
				mustExec(t, s.st, `UPDATE app_platform_runtime_bindings SET enabled=0,sealed_control_key=NULL,revision=revision+1,control_revision=control_revision+1`)
			}
			before := dumpOf(t, s.st)
			r, err := s.revokePlatformRuntimeControl(session, id, revision)
			if err == nil || !reflect.DeepEqual(r, platformRuntimeRevocation{}) {
				t.Fatal("invalid revocation accepted")
			}
			if !bytes.Equal(stripHeader(before), stripHeader(dumpOf(t, s.st))) {
				t.Fatal("refusal mutated runtime")
			}
		})
	}
}
func TestPlatformRuntimeRevocationRefusalsSQLite(t *testing.T) {
	testPlatformRuntimeRevocationRefusals(t, false)
}
func TestPlatformRuntimeRevocationRefusalsPostgres(t *testing.T) {
	testPlatformRuntimeRevocationRefusals(t, true)
}
func testPlatformRuntimeRevocationConcurrent(t *testing.T, pg bool) {
	s, session, expected := runtimeConfirmedFixture(t, pg)
	results := make(chan platformRuntimeRevocation, 8)
	errs := make(chan error, 8)
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			r, err := s.revokePlatformRuntimeControl(session, expected.id, expected.revision)
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
	var first platformRuntimeRevocation
	for r := range results {
		if first.id == "" {
			first = r
		}
		if r != first {
			t.Fatal("concurrent revocation split revision")
		}
	}
}
func TestPlatformRuntimeRevocationConcurrentSQLite(t *testing.T) {
	testPlatformRuntimeRevocationConcurrent(t, false)
}
func TestPlatformRuntimeRevocationConcurrentPostgres(t *testing.T) {
	testPlatformRuntimeRevocationConcurrent(t, true)
}

func testPlatformRuntimeRevocationAuditRollback(t *testing.T, pg bool) {
	s, session, expected := runtimeConfirmedFixture(t, pg)
	if pg {
		mustExec(t, s.st, `CREATE FUNCTION fixture_runtime_revoke_failure() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='app_platform.runtime_control_revoked' THEN RAISE EXCEPTION 'fixture revoke failure'; END IF; RETURN NEW; END $$`)
		mustExec(t, s.st, `CREATE TRIGGER fixture_runtime_revoke_failure BEFORE INSERT ON audit_log FOR EACH ROW EXECUTE FUNCTION fixture_runtime_revoke_failure()`)
		t.Cleanup(func() {
			if _, err := s.st.exec(`DROP TRIGGER IF EXISTS fixture_runtime_revoke_failure ON audit_log`); err != nil {
				t.Error(err)
			}
			if _, err := s.st.exec(`DROP FUNCTION IF EXISTS fixture_runtime_revoke_failure()`); err != nil {
				t.Error(err)
			}
		})
	} else {
		mustExec(t, s.st, `CREATE TRIGGER fixture_runtime_revoke_failure BEFORE INSERT ON audit_log WHEN NEW.action='app_platform.runtime_control_revoked' BEGIN SELECT RAISE(ABORT,'fixture revoke failure');END`)
	}
	before := dumpOf(t, s.st)
	r, err := s.revokePlatformRuntimeControl(session, expected.id, expected.revision)
	if err == nil || r != (platformRuntimeRevocation{}) || !bytes.Equal(stripHeader(before), stripHeader(dumpOf(t, s.st))) {
		t.Fatal("audit failure left partial signer revocation")
	}
}
func TestPlatformRuntimeRevocationRollbackSQLite(t *testing.T) {
	testPlatformRuntimeRevocationAuditRollback(t, false)
}
func TestPlatformRuntimeRevocationRollbackPostgres(t *testing.T) {
	testPlatformRuntimeRevocationAuditRollback(t, true)
}

func TestPostgresRuntimeRevocationExpiryAfterAuditWait(t *testing.T) {
	s, session, expected := runtimeConfirmedFixture(t, true)
	now, err := platformDatabaseNow(s.st.poolExec())
	if err != nil {
		t.Fatal(err)
	}
	session.expires = time.UnixMilli(now + 2000).Truncate(time.Second)
	platformSeedActorSession(t, s.st, session)
	before := dumpOf(t, s.st)
	holder, err := s.st.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Rollback()
	if _, err := holder.Exec(`LOCK TABLE audit_log IN SHARE MODE`); err != nil {
		t.Fatal(err)
	}
	type outcome struct {
		result platformRuntimeRevocation
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		r, err := s.revokePlatformRuntimeControl(session, expected.id, expected.revision)
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
		t.Fatalf("revocation did not reach audit lock: %+v", <-done)
	}
	for time.Now().Before(session.expires) {
		time.Sleep(5 * time.Millisecond)
	}
	if err := holder.Commit(); err != nil {
		t.Fatal(err)
	}
	got := <-done
	if !errors.Is(got.err, errAppPlatformActorUnavailable) || got.result != (platformRuntimeRevocation{}) {
		t.Fatalf("expired administrator revoked signing authority: %v", got.err)
	}
	if !bytes.Equal(stripHeader(before), stripHeader(dumpOf(t, s.st))) {
		t.Fatal("expired administrator committed revocation")
	}
}

func TestPlatformRuntimeRevocationNearCounterLimit(t *testing.T) {
	s, session, expected := runtimeConfirmedFixture(t, false)
	mustExec(t, s.st, `UPDATE app_platform_runtime_bindings SET revision=?,control_revision=?`, int64(math.MaxInt64-1), int64(math.MaxInt64-1))
	first, err := s.revokePlatformRuntimeControl(session, expected.id, math.MaxInt64-1)
	if err != nil || first.revision != math.MaxInt64 || first.controlRevision != math.MaxInt64 {
		t.Fatalf("valid final revision refused: %v", err)
	}
	repeat, err := s.revokePlatformRuntimeControl(session, expected.id, math.MaxInt64-1)
	if err != nil || first != repeat {
		t.Fatal("final revision retry overflowed/refused")
	}
}
func TestPlatformRuntimeRevocationDoesNotNeedHealthySigningEnvelope(t *testing.T) {
	s, session, expected := runtimeConfirmedFixture(t, false)
	mustExec(t, s.st, `UPDATE app_platform_runtime_bindings SET sealed_control_key='corrupt-envelope'`)
	if _, err := s.revokePlatformRuntimeControl(session, expected.id, expected.revision); err != nil {
		t.Fatalf("denial-only revocation tried to use damaged signer: %v", err)
	}
}

func TestPlatformRuntimeRevocationSurvivesSQLiteReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "revocation.db")
	first, err := OpenStore("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { first.Close() })
	s, session := platformActorTestServerWithStore(t, first)
	if err := s.initializeAppPlatformControl(session); err != nil {
		t.Fatal(err)
	}
	initial, err := s.preparePlatformRuntimeBinding(session, platformFixtureID(400))
	if err != nil {
		t.Fatal(err)
	}
	confirmed, err := s.confirmPlatformObservedRuntime(session, initial, runtimeObservationFixture(initial))
	if err != nil {
		t.Fatal(err)
	}
	expected, err := s.revokePlatformRuntimeControl(session, confirmed.id, confirmed.revision)
	if err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenStore("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	other := &Server{st: reopened, cfg: s.cfg}
	got, err := other.revokePlatformRuntimeControl(session, confirmed.id, confirmed.revision)
	if err != nil || got != expected {
		t.Fatalf("reopen forgot exact revocation: %v", err)
	}
	if _, err := other.confirmPlatformObservedRuntime(session, initial, runtimeObservationFixture(initial)); err == nil {
		t.Fatal("reopen allowed stale reenrollment")
	}
	mustExec(t, reopened, `UPDATE audit_log SET detail='{}' WHERE action='app_platform.runtime_control_revoked' AND target_id=?`, expected.id)
	before := dumpOf(t, reopened)
	if _, err := other.revokePlatformRuntimeControl(session, confirmed.id, confirmed.revision); err == nil {
		t.Fatal("replay trusted row without exact matching audit")
	}
	if !bytes.Equal(stripHeader(before), stripHeader(dumpOf(t, reopened))) {
		t.Fatal("missing evidence repaired itself")
	}
}
