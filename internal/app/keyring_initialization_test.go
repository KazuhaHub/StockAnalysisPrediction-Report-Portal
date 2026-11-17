package app

import (
	"bytes"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/config"
)

func keyringInitializationStore(t *testing.T, postgres bool) *Store {
	t.Helper()
	var st *Store
	if postgres {
		st = pgStore(t)
	} else {
		st = newTestStore(t)
	}
	for _, key := range []string{setKeyringSalt, setKeyringDEK} {
		var value sql.NullString
		err := st.queryRow(`SELECT v FROM meta WHERE k=?`, key).Scan(&value)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			t.Fatal(err)
		}
		existed := err == nil
		t.Cleanup(func() {
			if existed {
				_, err := st.exec(`INSERT INTO meta(k,v) VALUES(?,?) ON CONFLICT(k) DO UPDATE SET v=excluded.v`, key, value)
				if err != nil {
					t.Error(err)
				}
			} else if _, err := st.exec(`DELETE FROM meta WHERE k=?`, key); err != nil {
				t.Error(err)
			}
		})
		mustExec(t, st, `DELETE FROM meta WHERE k=?`, key)
	}
	return st
}

func testConcurrentKeyringInitialization(t *testing.T, postgres bool) {
	st := keyringInitializationStore(t, postgres)
	const processes = 8
	start := make(chan struct{})
	type result struct {
		sealed string
		key    []byte
		err    error
	}
	results := make(chan result, processes)
	var wg sync.WaitGroup
	for range processes {
		wg.Go(func() {
			s := &Server{st: st, cfg: &config.Config{SecretKey: rotOldKey}}
			<-start
			key, err := s.dek()
			if err != nil {
				results <- result{err: err}
				return
			}
			sealed, err := s.sealSecret("keyring-test", "concurrent", "fixture-secret")
			results <- result{sealed: sealed, key: key, err: err}
		})
	}
	close(start)
	wg.Wait()
	close(results)
	fresh := &Server{st: st, cfg: &config.Config{SecretKey: rotOldKey}}
	stored, err := fresh.dek()
	if err != nil {
		t.Fatal(err)
	}
	for result := range results {
		if result.err != nil {
			t.Error(result.err)
			continue
		}
		if !bytes.Equal(result.key, stored) {
			t.Error("initializer cached an unpersisted data key")
		}
		if value, err := fresh.openSecret("keyring-test", "concurrent", result.sealed); err != nil || value != "fixture-secret" {
			t.Errorf("another process cannot open newly sealed secret: %v", err)
		}
	}
}

func TestKeyringConcurrentInitialization(t *testing.T) { testConcurrentKeyringInitialization(t, false) }
func TestPostgresKeyringConcurrentInitialization(t *testing.T) {
	testConcurrentKeyringInitialization(t, true)
}

func TestKeyringCreationRollsBackBothRows(t *testing.T) {
	st := keyringInitializationStore(t, false)
	mustExec(t, st, fmt.Sprintf(`CREATE TRIGGER fixture_keyring_failure BEFORE INSERT ON meta
        WHEN NEW.k='%s' BEGIN SELECT RAISE(ABORT,'fixture write failure'); END`, setKeyringDEK))
	if err := st.SaveKeyring("fixture-salt", "fixture-key"); err == nil {
		t.Fatal("injected write failure ignored")
	}
	if scalar[int](t, st, `SELECT COUNT(*) FROM meta WHERE k IN (?,?)`, setKeyringSalt, setKeyringDEK) != 0 {
		t.Fatal("failed creation left a partial keyring")
	}
}

func testPartialKeyringRefused(t *testing.T, postgres bool) {
	for _, fixture := range []struct {
		name   string
		values map[string]any
	}{
		{"missing_salt", map[string]any{setKeyringDEK: "fixture-existing-value"}},
		{"missing_key", map[string]any{setKeyringSalt: "fixture-existing-value"}},
		{"null_salt", map[string]any{setKeyringSalt: nil, setKeyringDEK: "fixture-existing-value"}},
		{"null_key", map[string]any{setKeyringSalt: "fixture-existing-value", setKeyringDEK: nil}},
		{"empty_salt", map[string]any{setKeyringSalt: "", setKeyringDEK: "fixture-existing-value"}},
		{"empty_key", map[string]any{setKeyringSalt: "fixture-existing-value", setKeyringDEK: ""}},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			st := keyringInitializationStore(t, postgres)
			for key, value := range fixture.values {
				mustExec(t, st, `INSERT INTO meta(k,v) VALUES(?,?)`, key, value)
			}
			before := dumpOf(t, st)
			s := &Server{st: st, cfg: &config.Config{SecretKey: rotOldKey}}
			if _, err := s.dek(); err == nil {
				t.Error("partial keyring regenerated")
			}
			if err := st.SaveKeyring("replacement", "replacement"); err == nil {
				t.Error("partial keyring replaced")
			}
			if !bytes.Equal(stripHeader(before), stripHeader(dumpOf(t, st))) {
				t.Fatal("refused initialization changed existing key material")
			}
		})
	}
}

func TestKeyringPartialInitializationRefused(t *testing.T) { testPartialKeyringRefused(t, false) }
func TestPostgresKeyringPartialInitializationRefused(t *testing.T) {
	testPartialKeyringRefused(t, true)
}

func TestKeyringConcurrentCreatePreservesPair(t *testing.T) {
	st := keyringInitializationStore(t, false)
	var wg sync.WaitGroup
	errors := make(chan error, 8)
	for index := range 8 {
		wg.Go(func() { errors <- st.SaveKeyring(fmt.Sprintf("salt-%d", index), fmt.Sprintf("wrapped-%d", index)) })
	}
	wg.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
	salt, wrapped, ok := st.Keyring()
	if !ok || strings.TrimPrefix(salt, "salt-") != strings.TrimPrefix(wrapped, "wrapped-") {
		t.Fatal("keyring contains values from different creators")
	}
}
