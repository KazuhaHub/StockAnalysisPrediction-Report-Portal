package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/journal"
)

func runtimeObservationFixture(r platformRuntimeRegistration) platformRuntimeObservation {
	return platformRuntimeObservation{controller: journal.ControllerEnrollment{DeploymentID: r.deploymentID, ControllerEpoch: r.controllerEpoch, ControlRevision: r.controlRevision, PublicKey: bytes.Clone(r.publicKey), Fingerprint: r.fingerprint}, receivedAt: time.Now()}
}
func testPlatformRuntimeConfirmation(t *testing.T, pg bool) {
	s, session := runtimeRegistrationFixture(t, pg)
	expected, err := s.preparePlatformRuntimeBinding(session, platformFixtureID(400))
	if err != nil {
		t.Fatal(err)
	}
	audits := scalar[int](t, s.st, `SELECT COUNT(*) FROM audit_log WHERE action='app_platform.runtime_confirmed'`)
	result, err := s.confirmPlatformObservedRuntime(session, expected, runtimeObservationFixture(expected))
	if err != nil {
		t.Fatal(err)
	}
	if result.revision != 2 || result.id != expected.id || scalar[int](t, s.st, `SELECT enabled FROM app_platform_runtime_bindings`) != 1 {
		t.Fatal("matching observed enrollment did not confirm binding")
	}
	repeat, err := s.confirmPlatformObservedRuntime(session, expected, runtimeObservationFixture(expected))
	if err != nil || !reflect.DeepEqual(result, repeat) {
		t.Fatalf("confirmation retry changed identity: %v", err)
	}
	if scalar[int](t, s.st, `SELECT COUNT(*) FROM audit_log WHERE action='app_platform.runtime_confirmed'`) != audits+1 {
		t.Fatal("retry duplicated confirmation audit")
	}
	managedRaw, err := os.ReadFile("../appplatform/testdata/descriptors/managed.json")
	if err != nil {
		t.Fatal(err)
	}
	managed, err := appplatform.ParseDescriptor(managedRaw)
	if err != nil {
		t.Fatal(err)
	}
	if err := platformInstallRequirements(managed.Manifest); err == nil {
		t.Fatal("enrollment-only confirmation admitted unsupported managed execution")
	}
	if scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_installations`)+scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_operations`) != 0 {
		t.Fatal("identity confirmation created application work")
	}
}
func TestPlatformRuntimeConfirmationSQLite(t *testing.T)   { testPlatformRuntimeConfirmation(t, false) }
func TestPlatformRuntimeConfirmationPostgres(t *testing.T) { testPlatformRuntimeConfirmation(t, true) }
func testPlatformRuntimeConfirmationRefusals(t *testing.T, pg bool) {
	for _, kind := range []string{"zero", "stale", "future", "deployment", "epoch", "revision", "public_key", "pin", "stale_row", "cleared_key", "paused", "demoted"} {
		t.Run(kind, func(t *testing.T) {
			s, session := runtimeRegistrationFixture(t, pg)
			expected, err := s.preparePlatformRuntimeBinding(session, platformFixtureID(400))
			if err != nil {
				t.Fatal(err)
			}
			proof := runtimeObservationFixture(expected)
			switch kind {
			case "zero":
				proof = platformRuntimeObservation{}
			case "stale":
				proof.receivedAt = time.Now().Add(-4 * time.Second)
			case "future":
				proof.receivedAt = time.Now().Add(time.Second)
			case "deployment":
				proof.controller.DeploymentID = platformFixtureID(401)
			case "epoch":
				proof.controller.ControllerEpoch = platformFixtureID(999)
			case "revision":
				proof.controller.ControlRevision++
			case "public_key":
				proof.controller.PublicKey[0] ^= 1
			case "pin":
				proof.controller.Fingerprint = ""
			case "stale_row":
				mustExec(t, s.st, `UPDATE app_platform_runtime_bindings SET revision=3`)
			case "cleared_key":
				mustExec(t, s.st, `UPDATE app_platform_runtime_bindings SET sealed_control_key=NULL`)
			case "paused":
				mustExec(t, s.st, `UPDATE meta SET v='1' WHERE k='app_platform_restore_paused'`)
			case "demoted":
				mustExec(t, s.st, `UPDATE users SET role='user' WHERE username=?`, session.username)
			}
			before := dumpOf(t, s.st)
			r, err := s.confirmPlatformObservedRuntime(session, expected, proof)
			if err == nil || !reflect.DeepEqual(r, platformRuntimeRegistration{}) {
				t.Fatal("invalid observation confirmed runtime")
			}
			if !bytes.Equal(stripHeader(before), stripHeader(dumpOf(t, s.st))) {
				t.Fatal("refused observation changed state")
			}
		})
	}
}
func TestPlatformRuntimeConfirmationRefusalsSQLite(t *testing.T) {
	testPlatformRuntimeConfirmationRefusals(t, false)
}
func TestPlatformRuntimeConfirmationRefusalsPostgres(t *testing.T) {
	testPlatformRuntimeConfirmationRefusals(t, true)
}

func testPlatformRuntimeConfirmationAuditRollback(t *testing.T, pg bool) {
	s, session := runtimeRegistrationFixture(t, pg)
	expected, err := s.preparePlatformRuntimeBinding(session, platformFixtureID(400))
	if err != nil {
		t.Fatal(err)
	}
	if pg {
		mustExec(t, s.st, `CREATE FUNCTION fixture_runtime_confirm_failure() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='app_platform.runtime_confirmed' THEN RAISE EXCEPTION 'fixture confirmation failure'; END IF; RETURN NEW; END $$`)
		mustExec(t, s.st, `CREATE TRIGGER fixture_runtime_confirm_failure BEFORE INSERT ON audit_log FOR EACH ROW EXECUTE FUNCTION fixture_runtime_confirm_failure()`)
		t.Cleanup(func() {
			if _, err := s.st.exec(`DROP TRIGGER IF EXISTS fixture_runtime_confirm_failure ON audit_log`); err != nil {
				t.Error(err)
			}
			if _, err := s.st.exec(`DROP FUNCTION IF EXISTS fixture_runtime_confirm_failure()`); err != nil {
				t.Error(err)
			}
		})
	} else {
		mustExec(t, s.st, `CREATE TRIGGER fixture_runtime_confirm_failure BEFORE INSERT ON audit_log WHEN NEW.action='app_platform.runtime_confirmed' BEGIN SELECT RAISE(ABORT,'fixture confirmation failure');END`)
	}
	before := dumpOf(t, s.st)
	r, err := s.confirmPlatformObservedRuntime(session, expected, runtimeObservationFixture(expected))
	if err == nil || !reflect.DeepEqual(r, platformRuntimeRegistration{}) || !bytes.Equal(stripHeader(before), stripHeader(dumpOf(t, s.st))) {
		t.Fatal("audit failure committed confirmation")
	}
}
func TestPlatformRuntimeConfirmationRollbackSQLite(t *testing.T) {
	testPlatformRuntimeConfirmationAuditRollback(t, false)
}
func TestPlatformRuntimeConfirmationRollbackPostgres(t *testing.T) {
	testPlatformRuntimeConfirmationAuditRollback(t, true)
}

func TestPostgresRuntimeConfirmationExpiryAfterAuditWait(t *testing.T) {
	for _, kind := range []string{"session", "observation"} {
		t.Run(kind, func(t *testing.T) {
			s, session := runtimeRegistrationFixture(t, true)
			expected, err := s.preparePlatformRuntimeBinding(session, platformFixtureID(400))
			if err != nil {
				t.Fatal(err)
			}
			if kind == "session" {
				now, err := platformDatabaseNow(s.st.poolExec())
				if err != nil {
					t.Fatal(err)
				}
				session.expires = time.UnixMilli(now + 2000).Truncate(time.Second)
				platformSeedActorSession(t, s.st, session)
			}
			proof := runtimeObservationFixture(expected)
			expire := proof.receivedAt.Add(3100 * time.Millisecond)
			if kind == "session" {
				expire = session.expires
			}
			audits := scalar[int](t, s.st, `SELECT COUNT(*) FROM audit_log WHERE action='app_platform.runtime_confirmed'`)
			holder, err := s.st.db.Begin()
			if err != nil {
				t.Fatal(err)
			}
			defer holder.Rollback()
			if _, err := holder.Exec(`LOCK TABLE audit_log IN SHARE MODE`); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() {
				r, err := s.confirmPlatformObservedRuntime(session, expected, proof)
				if !reflect.DeepEqual(r, platformRuntimeRegistration{}) {
					done <- fmt.Errorf("expired proof exported confirmed identity")
					return
				}
				done <- err
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
				t.Fatalf("confirmation did not wait on audit: %v", <-done)
			}
			for time.Now().Before(expire) {
				time.Sleep(5 * time.Millisecond)
			}
			if err := holder.Commit(); err != nil {
				t.Fatal(err)
			}
			err = <-done
			expectedError := errAppPlatformRuntimeConflict
			if kind == "session" {
				expectedError = errAppPlatformActorUnavailable
			}
			if !errors.Is(err, expectedError) {
				t.Fatalf("expired %s confirmed identity: %v", kind, err)
			}
			if scalar[int](t, s.st, `SELECT enabled FROM app_platform_runtime_bindings`) != 0 || scalar[int](t, s.st, `SELECT revision FROM app_platform_runtime_bindings`) != 1 || scalar[int](t, s.st, `SELECT COUNT(*) FROM audit_log WHERE action='app_platform.runtime_confirmed'`) != audits {
				t.Fatal("expired proof/actor committed binding")
			}
		})
	}
}

func TestPlatformRuntimeConfirmationEndpointRefusesInvalidBootstrap(t *testing.T) {
	s, session := runtimeRegistrationFixture(t, false)
	expected, err := s.preparePlatformRuntimeBinding(session, platformFixtureID(400))
	if err != nil {
		t.Fatal(err)
	}
	for _, endpoint := range []platformRuntimeEndpoint{{path: "/not-a-runtime/control.sock"}, {path: "/not-a-runtime/control.sock", controllerUID: 41001, controllerGID: 41001}} {
		before := dumpOf(t, s.st)
		result, err := s.confirmPlatformRuntimeBinding(context.Background(), session, expected, endpoint)
		if err == nil || !reflect.DeepEqual(result, platformRuntimeRegistration{}) {
			t.Fatal("invalid bootstrap bypassed native probe")
		}
		if !bytes.Equal(stripHeader(before), stripHeader(dumpOf(t, s.st))) {
			t.Fatal("failed endpoint probe mutated binding")
		}
	}
}
