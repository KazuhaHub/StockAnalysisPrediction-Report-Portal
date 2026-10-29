package app

import (
	"bytes"
	"testing"
)

func testPlatformCredentialSchema(t *testing.T, pg bool) {
	st := platformTestStore(t, pg)
	step := migration0013()
	if len(step.tables) != 2 || len(step.indices) != 4 || len(migration0012().tables) != 14 {
		t.Fatal("approved products or frozen control products changed")
	}
	if !recorded(st, "0013") {
		t.Fatal("credential migration not recorded")
	}
	if err := step.verify(st.poolExec()); err != nil {
		t.Fatal(err)
	}
	platformSeedCredentialRecords(t, st)
	for _, q := range []string{`UPDATE app_platform_credentials SET state='invented'`, `UPDATE app_platform_credentials SET sealed_value=NULL`, `UPDATE app_platform_credentials SET value_revision=0`, `UPDATE app_platform_credentials SET state='revoked',revoked_ms=2`, `UPDATE app_platform_secret_bindings SET operation='arbitrary_header'`, `UPDATE app_platform_secret_bindings SET revoked_ms=2`, `UPDATE app_platform_secret_bindings SET policy_revision=0`, `UPDATE app_platform_secret_bindings SET credential_id='00000000-0000-4000-8000-000000009999'`} {
		before := dumpOf(t, st)
		tx, err := st.db.Begin()
		if err != nil {
			t.Fatal(err)
		}
		_, err = tx.Exec(st.bind(q))
		if err == nil {
			err = tx.Commit()
		} else {
			tx.Rollback()
		}
		if err == nil {
			t.Fatal("invalid credential shape accepted", q)
		}
		if !bytes.Equal(stripHeader(before), stripHeader(dumpOf(t, st))) {
			t.Fatal("rejected transaction changed state")
		}
	}
	before := dumpOf(t, st)
	if _, err := st.restoreFrom(bytes.NewReader(before), false); err != nil {
		t.Fatal("credential backup shape", err)
	}
	if !bytes.Equal(stripHeader(before), stripHeader(dumpOf(t, st))) {
		t.Fatal("dry restore changed data")
	}
	// Deferred credential NO ACTION forbids purging a referenced value, while
	// installation deletion cascades only its bindings and retains custody.
	tx, err := st.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	_, err = tx.Exec(`DELETE FROM app_platform_credentials`)
	if err == nil {
		err = tx.Commit()
	} else {
		tx.Rollback()
	}
	if err == nil {
		t.Fatal("credential purge orphaned binding")
	}
}
func TestPlatformCredentialSchemaSQLite(t *testing.T)   { testPlatformCredentialSchema(t, false) }
func TestPlatformCredentialSchemaPostgres(t *testing.T) { testPlatformCredentialSchema(t, true) }

func platformSeedCredentialRecords(t *testing.T, st *Store) {
	t.Helper()
	platformSeedPendingControl(t, st)
	platformTestInsert(t, st.poolExec(), "credentials", map[string]any{"id": platformFixtureID(2001), "label": "Disposable fixture", "state": "active", "sealed_value": "fixture-ciphertext", "value_revision": 1, "revision": 1, "created_ms": 1, "updated_ms": 1, "revoked_ms": nil})
	platformTestInsert(t, st.poolExec(), "secret_bindings", map[string]any{"id": platformFixtureID(2002), "installation_id": platformFixtureID(1), "name": "fixture-key", "credential_id": platformFixtureID(2001), "credential_value_revision": 1, "operation": "http_bearer", "state": "active", "policy_json": "{}", "policy_revision": 1, "review_plan_id": platformFixtureID(2), "revision": 1, "created_ms": 1, "updated_ms": 1, "revoked_ms": nil})
}

func testPlatformCredentialSchemaDrift(t *testing.T, pg bool) {
	st := platformTestStore(t, pg)
	tx, err := st.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DROP INDEX uq_ap_secret_binding_name`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`CREATE INDEX uq_ap_secret_binding_name ON app_platform_secret_bindings(installation_id,name)`); err != nil {
		t.Fatal(err)
	}
	if err := migration0013().verify(migExec{s: st, ex: tx}); err == nil {
		t.Fatal("weakened uniqueness accepted")
	}
}
func TestPlatformCredentialSchemaDriftSQLite(t *testing.T) {
	testPlatformCredentialSchemaDrift(t, false)
}
func TestPlatformCredentialSchemaDriftPostgres(t *testing.T) {
	testPlatformCredentialSchemaDrift(t, true)
}

func testPlatformCredentialInstallationCascade(t *testing.T, pg bool) {
	st := platformTestStore(t, pg)
	platformSeedCredentialRecords(t, st)
	tx, err := st.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	// Existing review/operation references are independent lifecycle constraints.
	// Remove those fixtures in this transaction to isolate the new CASCADE.
	for _, q := range []string{`DELETE FROM app_platform_operations`, `UPDATE app_platform_review_plans SET installation_id=NULL`, `DELETE FROM app_platform_installations`} {
		if _, err := tx.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if scalar[int](t, st, `SELECT COUNT(*) FROM app_platform_secret_bindings`) != 0 || scalar[int](t, st, `SELECT COUNT(*) FROM app_platform_credentials`) != 1 {
		t.Fatal("installation cleanup violated custody boundary")
	}
}
func TestPlatformCredentialInstallationCascadeSQLite(t *testing.T) {
	testPlatformCredentialInstallationCascade(t, false)
}
func TestPlatformCredentialInstallationCascadePostgres(t *testing.T) {
	testPlatformCredentialInstallationCascade(t, true)
}
