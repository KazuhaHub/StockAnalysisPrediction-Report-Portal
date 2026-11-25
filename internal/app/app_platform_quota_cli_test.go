package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A production database at 0023 carries the shape step 0021 left. Inspecting it and applying 0024 must work.
func testSchemaUpgradeAt0023(t *testing.T, pg bool) {
	var cfg string
	var st *Store
	if pg {
		st = platformTestStore(t, true)
		cfg = filepath.Join(t.TempDir(), "config.yaml")
		if err := os.WriteFile(cfg, []byte("db_driver: postgres\ndb_dsn: "+testPostgresDSN(t)+"\n"), 0600); err != nil {
			t.Fatal(err)
		}
	} else {
		path := filepath.Join(t.TempDir(), "p.db")
		var err error
		if st, err = OpenStore("sqlite", path); err != nil {
			t.Fatal(err)
		}
		cfg = writeSQLiteConfig(t, path)
	}
	platformRewindToPre0024(t, st)
	if !pg {
		st.Close()
	}
	out, err := SchemaUpgradeCommand(cfg, nil)
	if err != nil || !strings.Contains(out, "[0024]") || !strings.Contains(out, "nothing was changed") {
		t.Fatalf("inspect a database at 0023: %q %v", out, err)
	}
	out, err = SchemaUpgradeCommand(cfg, []string{"--apply"})
	if err != nil || !strings.Contains(out, "[0024]") {
		t.Fatalf("--apply on a database at 0023: %q %v", out, err)
	}
	out, err = SchemaUpgradeCommand(cfg, nil)
	if err != nil || !strings.Contains(out, "no pending") {
		t.Fatalf("after apply: %q %v", out, err)
	}
}

func TestSchemaUpgradeAt0023SQLite(t *testing.T)   { t.Parallel(); testSchemaUpgradeAt0023(t, false) }
func TestSchemaUpgradeAt0023Postgres(t *testing.T) { t.Parallel(); testSchemaUpgradeAt0023(t, true) }
