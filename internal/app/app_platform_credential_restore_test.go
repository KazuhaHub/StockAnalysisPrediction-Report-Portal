package app

import (
	"bytes"
	"encoding/json"
	"slices"
	"testing"
)

func testPlatformCredentialRestore(t *testing.T, pg bool) {
	t.Run("paused", func(t *testing.T) {
		st := platformTestStore(t, pg)
		platformSeedCredentialRecords(t, st)
		backup := dumpOf(t, st)
		if _, err := st.restoreFrom(bytes.NewReader(backup), true); err != nil {
			t.Fatal(err)
		}
		if scalar[string](t, st, `SELECT state FROM app_platform_credentials`) != "review_required" || scalar[string](t, st, `SELECT state FROM app_platform_secret_bindings`) != "review_required" {
			t.Fatal("restored custody/binding active")
		}
		if scalar[int](t, st, `SELECT revision FROM app_platform_credentials`) != 2 || scalar[int](t, st, `SELECT revision FROM app_platform_secret_bindings`) != 2 || scalar[int](t, st, `SELECT value_revision FROM app_platform_credentials`) != 1 {
			t.Fatal("restore did not invalidate authority or changed encryption identity")
		}
	})
	t.Run("floor", func(t *testing.T) {
		st := platformTestStore(t, pg)
		platformSeedCredentialRecords(t, st)
		backup := dumpOf(t, st)
		mustExec(t, st, `UPDATE app_platform_credentials SET state='revoked',sealed_value=NULL,revoked_ms=2,value_revision=2,revision=2,updated_ms=2`)
		mustExec(t, st, `UPDATE app_platform_secret_bindings SET state='revoked',revoked_ms=2,revision=2,updated_ms=2`)
		if _, err := st.restoreFrom(bytes.NewReader(backup), true); err != nil {
			t.Fatal(err)
		}
		if scalar[string](t, st, `SELECT state FROM app_platform_credentials`) != "revoked" || scalar[string](t, st, `SELECT state FROM app_platform_secret_bindings`) != "revoked" || scalar[int](t, st, `SELECT COUNT(*) FROM app_platform_credentials WHERE sealed_value IS NOT NULL`) != 0 || scalar[int](t, st, `SELECT value_revision FROM app_platform_credentials`) < 2 {
			t.Fatal("backup resurrected revocation")
		}
		empty := newTestStore(t)
		before := dumpOf(t, st)
		if _, err := st.restoreFrom(bytes.NewReader(dumpOf(t, empty)), true); err == nil {
			t.Fatal("restore discarded binding floor with missing parents")
		}
		if !bytes.Equal(stripHeader(before), stripHeader(dumpOf(t, st))) {
			t.Fatal("failed restore changed data")
		}
	})
	t.Run("overflow", func(t *testing.T) {
		st := platformTestStore(t, pg)
		platformSeedCredentialRecords(t, st)
		mustExec(t, st, `UPDATE app_platform_credentials SET revision=9223372036854775807`)
		before := dumpOf(t, st)
		if _, err := st.restoreFrom(bytes.NewReader(before), true); err == nil {
			t.Fatal("restore accepted revision overflow")
		}
		if !bytes.Equal(stripHeader(before), stripHeader(dumpOf(t, st))) {
			t.Fatal("failed restore changed data")
		}
	})
}
func TestPlatformCredentialRestoreSQLite(t *testing.T)   { testPlatformCredentialRestore(t, false) }
func TestPlatformCredentialRestorePostgres(t *testing.T) { testPlatformCredentialRestore(t, true) }

func testPlatformCredentialOlderBackup(t *testing.T, pg bool) {
	st := platformTestStore(t, pg)
	old := preCredentialMigrationDump(t)
	if _, err := st.restoreFrom(bytes.NewReader(old), true); err != nil {
		t.Fatal("pre-credential backup refused", err)
	}
	if scalar[int](t, st, `SELECT COUNT(*) FROM app_platform_credentials`) != 0 || scalar[int](t, st, `SELECT COUNT(*) FROM app_platform_secret_bindings`) != 0 || !recorded(st, "0016") {
		t.Fatal("older backup did not establish empty credential products")
	}
	var steps []string
	for _, m := range st.migrations() {
		steps = append(steps, m.id)
	}
	before := dumpOf(t, st)
	if _, err := st.restoreFrom(bytes.NewReader(withHeaderMigrations(t, old, steps)), true); err == nil {
		t.Fatal("new schema claim with missing products accepted")
	}
	if !bytes.Equal(stripHeader(before), stripHeader(dumpOf(t, st))) {
		t.Fatal("missing products failure wrote")
	}
}
func TestPlatformCredentialOlderBackupSQLite(t *testing.T) {
	testPlatformCredentialOlderBackup(t, false)
}
func TestPlatformCredentialOlderBackupPostgres(t *testing.T) {
	testPlatformCredentialOlderBackup(t, true)
}

func testPlatformCredentialMissingFloorRecord(t *testing.T, pg bool) {
	st := platformTestStore(t, pg)
	platformSeedCredentialRecords(t, st)
	var buf bytes.Buffer
	// A disposable second store produces an earlier snapshot with the same parents.
	source := newTestStore(t)
	platformSeedCredentialRecords(t, source)
	mustExec(t, source, `DELETE FROM app_platform_secret_bindings`)
	buf.Write(dumpOf(t, source))
	mustExec(t, st, `UPDATE app_platform_secret_bindings SET state='revoked',revoked_ms=2,revision=2,updated_ms=2`)
	if _, err := st.restoreFrom(bytes.NewReader(buf.Bytes()), true); err != nil {
		t.Fatal("missing binding floor with intact parents refused", err)
	}
	if scalar[string](t, st, `SELECT state FROM app_platform_secret_bindings`) != "revoked" || scalar[int](t, st, `SELECT revision FROM app_platform_secret_bindings`) < 2 {
		t.Fatal("missing binding floor not retained")
	}
}
func TestPlatformCredentialMissingFloorRecordSQLite(t *testing.T) {
	testPlatformCredentialMissingFloorRecord(t, false)
}
func TestPlatformCredentialMissingFloorRecordPostgres(t *testing.T) {
	testPlatformCredentialMissingFloorRecord(t, true)
}

func preCredentialMigrationDump(t *testing.T) []byte {
	t.Helper()
	source := newTestStore(t)
	var header backupHeader
	if err := json.NewDecoder(bytes.NewReader(dumpOf(t, source))).Decode(&header); err != nil {
		t.Fatal(err)
	}
	header.Tables = slices.DeleteFunc(header.Tables, func(name string) bool {
		return name == platformPrefix+"credentials" || name == platformPrefix+"secret_bindings" ||
			slices.Contains(migration0017().tables, name)
	})
	header.Migrations = slices.DeleteFunc(header.Migrations, func(id string) bool { return id == "0016" || id == "0017" })
	tx, err := source.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM meta WHERE k='mig:0013'`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`DELETE FROM meta WHERE k IN ('mig:0016','mig:0017')`); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	if err := enc.Encode(header); err != nil {
		t.Fatal(err)
	}
	for _, name := range header.Tables {
		if _, err := dumpTable(tx, enc, name); err != nil {
			t.Fatal(err)
		}
	}
	return buf.Bytes()
}
