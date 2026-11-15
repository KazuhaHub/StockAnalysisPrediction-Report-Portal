package app

import (
	"bytes"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/config"
)

// Tests for the review of the recovery-bundle command: snapshot-consistent table list, the secret_key check,
// path safety, restore ordering, the target guard, a side-effect free dry run, the journal guard, modes and the
// verified-dump read.

const wrongSecretKey = "ffffffffffffffffffffffffffffffff"

func bundleTestConfig(t *testing.T, secret string) (cfgPath, dbPath, dir string) {
	t.Helper()
	dir = t.TempDir()
	cfgPath = filepath.Join(dir, "conf", "config.yaml")
	dbPath = filepath.Join(dir, "data", "portal.db")
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0o700); err != nil {
		t.Fatal(err)
	}
	yaml := "secret_key: \"" + secret + "\"\ndb_driver: \"sqlite\"\ndb_path: \"" + dbPath + "\"\n"
	if err := os.WriteFile(cfgPath, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	return cfgPath, dbPath, dir
}

// freshTarget is a Server over an empty file database, as on a fresh host.
func freshTarget(t *testing.T, secret string) *Server {
	t.Helper()
	dir := t.TempDir()
	st, err := OpenStore("sqlite", filepath.Join(dir, "portal.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return &Server{st: st, cfg: &config.Config{SecretKey: secret, DBPath: filepath.Join(dir, "portal.db")}, platformImageDir: t.TempDir()}
}

func writeGoodBundle(t *testing.T) (*platformBundleSource, string, platformRecoveryBundleManifest) {
	t.Helper()
	b, journal := bundleHostFixture(t, false)
	dir := t.TempDir()
	m, err := b.s.writeRecoveryBundleFromHost(dir, recoveryBundleHost{Checkpoints: []string{b.hostDir}, RunnerJournal: journal})
	if err != nil {
		t.Fatal(err)
	}
	return b, dir, m
}

// 1. The plugin-table list must come from the same snapshot as the rows (PostgreSQL shows the difference).
func TestBackupTableListFollowsTheReadSnapshot(t *testing.T) {
	st := platformTestStore(t, true)
	appID := platformFixtureID(7501)
	table := platformPluginDataTableName(appID)
	t.Cleanup(func() {
		_ = dropPlatformPluginDataTable(st.poolExec(), table)
		mustExec(t, st, `DELETE FROM app_platform_plugin_tables WHERE table_name=?`, table)
	})
	err := st.inReadSnapshot(func(tx *sql.Tx) error {
		var one int
		if err := tx.QueryRow(`SELECT 1`).Scan(&one); err != nil {
			return err
		}
		// An app registers a plugin table after the snapshot began.
		if err := createPlatformPluginDataTable(st.poolExec(), table); err != nil {
			return err
		}
		platformTestInsert(t, st.poolExec(), "plugin_tables", map[string]any{
			"id": platformFixtureID(7502), "app_id": appID, "table_name": table, "data_format": 1,
			"used_bytes": 0, "used_objects": 0, "revision": 1, "created_ms": 1, "updated_ms": 1,
		})
		inTx, err := st.backupTablesIn(migExec{s: st, ex: tx})
		if err != nil {
			return err
		}
		if containsString(inTx, table) {
			t.Fatal("the snapshot's table list saw a plugin table registered after it began")
		}
		outside, err := st.backupTables()
		if err != nil || !containsString(outside, table) {
			t.Fatalf("control: the pool list should see it: %v", err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// 2. The bundle keyring must open under the target's secret_key, before anything is written, dry run included.
func TestRecoveryBundleRestoreRefusesAWrongSecretKeyBeforeAnyWrite(t *testing.T) {
	_, dir, _ := writeGoodBundle(t)
	for _, args := range [][]string{{"restore", dir}, {"restore", dir, "--force"}} {
		cfgPath, dbPath, root := bundleTestConfig(t, wrongSecretKey)
		var out bytes.Buffer
		err := RecoveryBundleCommand(cfgPath, args, &out)
		if err == nil || !strings.Contains(err.Error(), "secret_key does not match the bundle") {
			t.Fatalf("%v: wrong secret_key was not refused clearly: %v", args, err)
		}
		if _, statErr := os.Stat(filepath.Dir(dbPath)); !os.IsNotExist(statErr) {
			t.Fatalf("%v: the refusal created %s", args, filepath.Dir(dbPath))
		}
		_ = root
	}
	cfgPath, _, _ := bundleTestConfig(t, rotOldKey)
	if err := RecoveryBundleCommand(cfgPath, []string{"restore", dir}, &bytes.Buffer{}); err != nil {
		t.Fatalf("the matching secret_key was refused: %v", err)
	}
}

// 3. A checkpoint id is a UUID before it names a file, on both sides.
func TestRecoveryBundleRefusesACheckpointIDThatIsNotAUUID(t *testing.T) {
	crafted := platformRetainedSet{id: platformFixtureID(7100), checkpoint: "../../outside", manifest: platformDigest([]byte("x"))}
	if err := validatePlatformRetainedSets([]platformRetainedSet{crafted}); !errors.Is(err, errPlatformBundle) {
		t.Fatalf("a crafted checkpoint id was accepted by the writer: %v", err)
	}
	if _, err := resolveRecoveryBundleArchives([]platformRetainedSet{crafted}, []string{t.TempDir()}); !errors.Is(err, errPlatformBundle) {
		t.Fatalf("a crafted checkpoint id was accepted by the resolver: %v", err)
	}
	_, dir, m := writeGoodBundle(t)
	m.Checkpoints[0].CheckpointID = "../../outside"
	if err := writeManifestForTest(dir, m); err != nil {
		t.Fatal(err)
	}
	if _, err := verifyPlatformRecoveryBundle(dir); !errors.Is(err, errPlatformBundle) {
		t.Fatalf("a crafted checkpoint id verified: %v", err)
	}
}

// 4. Placed blobs stay reserved until the dump has loaded.
func TestRecoveryBundleRestoreKeepsBlobsReservedUntilTheDumpLoaded(t *testing.T) {
	b, _, _ := bundleWithManagedReleases(t)
	dir := t.TempDir()
	if _, err := b.s.writePlatformRecoveryBundle(dir, platformRecoveryBundleInput{Archives: b.archives}); err != nil {
		t.Fatal(err)
	}
	target := freshTarget(t, rotOldKey)
	store, err := target.platformImages()
	if err != nil {
		t.Fatal(err)
	}
	var during int64 = -1
	_, err = target.restoreRecoveryBundle(dir, recoveryBundleRestoreOptions{Force: true, afterBlobs: func() {
		store.mu.Lock()
		during = store.reservedBlobs
		store.mu.Unlock()
	}})
	if err != nil {
		t.Fatal(err)
	}
	store.mu.Lock()
	after := store.reservedBlobs
	store.mu.Unlock()
	if during <= 0 || after != 0 {
		t.Fatalf("reservation during the load %d, after %d; want >0 then 0", during, after)
	}
}

// 5. --force against a populated database needs --replace-existing.
func TestRecoveryBundleRestoreRefusesANonEmptyTargetUnlessReplaceIsExplicit(t *testing.T) {
	_, dir, _ := writeGoodBundle(t)
	target := freshTarget(t, rotOldKey)
	if err := target.st.UpsertUser(User{Username: "someone", PasswordHash: "x", Role: "admin"}); err != nil {
		t.Fatal(err)
	}
	_, err := target.restoreRecoveryBundle(dir, recoveryBundleRestoreOptions{Force: true})
	if err == nil || !strings.Contains(err.Error(), "--replace-existing") || !strings.Contains(err.Error(), "users") {
		t.Fatalf("a populated target was not refused with the way out: %v", err)
	}
	if scalar[int](t, target.st, `SELECT COUNT(*) FROM users WHERE username='someone'`) != 1 || scalar[int](t, target.st, `SELECT COUNT(*) FROM app_platform_recovery_sets`) != 0 {
		t.Fatal("the refusal changed the target")
	}
	rep, err := target.restoreRecoveryBundle(dir, recoveryBundleRestoreOptions{Force: true, ReplaceExisting: true})
	if err != nil || !rep.Dump.Applied {
		t.Fatalf("explicit replace: %v", err)
	}
}

// 6. A dry run opens no target database and creates nothing.
func TestRecoveryBundleDryRunCreatesNothing(t *testing.T) {
	_, dir, _ := writeGoodBundle(t)
	cfgPath, dbPath, _ := bundleTestConfig(t, rotOldKey)
	var out bytes.Buffer
	if err := RecoveryBundleCommand(cfgPath, []string{"restore", dir}, &out); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Dir(dbPath)); !os.IsNotExist(err) {
		t.Fatalf("the dry run created the data directory: %v", err)
	}
	text := out.String()
	if !strings.Contains(text, "DRY RUN") || !strings.Contains(text, "was not opened") || strings.Contains(text, "DELETE") {
		t.Fatalf("dry run output is not accurate:\n%s", text)
	}
}

// 7. "Recovery mode" is printed only when the restore paused something.
func TestRecoveryBundleRestoreReportsRecoveryModeOnlyWhenPaused(t *testing.T) {
	_, dir, _ := writeGoodBundle(t)
	cfgPath, _, _ := bundleTestConfig(t, rotOldKey)
	var out bytes.Buffer
	if err := RecoveryBundleCommand(cfgPath, []string{"restore", dir, "--force"}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "recovery mode") {
		t.Fatalf("a paused restore did not say so:\n%s", out.String())
	}
	// A Portal with no app platform entities is not paused.
	plain := &Server{st: newTestStore(t), cfg: &config.Config{SecretKey: rotOldKey}, platformImageDir: t.TempDir()}
	if _, err := plain.dek(); err != nil {
		t.Fatal(err)
	}
	emptyDir := t.TempDir()
	if _, err := plain.writePlatformRecoveryBundle(emptyDir, platformRecoveryBundleInput{Archives: map[string]string{}}); err != nil {
		t.Fatal(err)
	}
	cfg2, _, _ := bundleTestConfig(t, rotOldKey)
	out.Reset()
	if err := RecoveryBundleCommand(cfg2, []string{"restore", emptyDir, "--force"}, &out); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "recovery mode") {
		t.Fatalf("a restore that paused nothing claimed recovery mode:\n%s", out.String())
	}
}

// 8. The journal guard covers a hot -journal and a change during the copy.
func TestRecoveryBundleJournalGuard(t *testing.T) {
	cases := map[string]struct {
		before func(t *testing.T, journal string)
		during func(t *testing.T, journal string)
	}{
		"hot rollback journal before": {before: func(t *testing.T, j string) { os.WriteFile(j+"-journal", []byte("hot"), 0o600) }},
		"journal appended during the copy": {during: func(t *testing.T, j string) {
			f, err := os.OpenFile(j, os.O_APPEND|os.O_WRONLY, 0o600)
			if err != nil {
				t.Fatal(err)
			}
			f.WriteString("more")
			f.Close()
		}},
		"hot journal appears during the copy": {during: func(t *testing.T, j string) { os.WriteFile(j+"-journal", []byte("hot"), 0o600) }},
		"wal appears during the copy":         {during: func(t *testing.T, j string) { os.WriteFile(j+"-wal", []byte("live"), 0o600) }},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			b, journal := bundleHostFixture(t, false)
			if c.before != nil {
				c.before(t, journal)
			}
			dir := t.TempDir()
			in := platformRecoveryBundleInput{Archives: b.archives, RunnerJournal: journal}
			if c.during != nil {
				in.afterJournalCopy = func() { c.during(t, journal) }
			}
			if _, err := b.s.writePlatformRecoveryBundle(dir, in); !errors.Is(err, errPlatformBundle) {
				t.Fatalf("an unstable journal was bundled: %v", err)
			}
			if left := platformBundleFiles(t, dir); len(left) != 0 {
				t.Fatalf("files left: %v", left)
			}
		})
	}
}

// 9. The bundle directory is owner-only, even when it already existed with a looser mode.
func TestRecoveryBundleModes(t *testing.T) {
	b, journal := bundleHostFixture(t, false)
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := b.s.writeRecoveryBundleFromHost(dir, recoveryBundleHost{Checkpoints: []string{b.hostDir}, RunnerJournal: journal}); err != nil {
		t.Fatal(err)
	}
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		want := os.FileMode(0o600)
		if d.IsDir() {
			want = 0o700
		}
		if info.Mode().Perm() != want {
			t.Errorf("%s has mode %v, want %v", path, info.Mode().Perm(), want)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// 10. A directory may hold archives of deleted recovery sets (the runner cannot delete a checkpoint), so they are
// left out and reported rather than refused.
func TestRecoveryBundleReportsIgnoredArchivesInADirectory(t *testing.T) {
	b, journal := bundleHostFixture(t, false)
	stray := filepath.Join(b.hostDir, platformFixtureID(7299)+".tar")
	if err := os.WriteFile(stray, []byte("a deleted set's archive"), 0o400); err != nil {
		t.Fatal(err)
	}
	report, err := b.s.writeRecoveryBundleFromHostReport(t.TempDir(), recoveryBundleHost{Checkpoints: []string{b.hostDir}, RunnerJournal: journal})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Manifest.Checkpoints) != 2 || len(report.Ignored) != 1 || report.Ignored[0] != stray {
		t.Fatalf("ignored archives: %+v", report.Ignored)
	}
}

// 11. The dump that is loaded is the dump that was verified.
func TestRecoveryBundleRestoreRefusesADumpSwappedAfterVerification(t *testing.T) {
	_, dir, m := writeGoodBundle(t)
	target := freshTarget(t, rotOldKey)
	_, err := target.restoreRecoveryBundle(dir, recoveryBundleRestoreOptions{Force: true, afterVerify: func() {
		path := filepath.Join(dir, filepath.FromSlash(m.Dump.Path))
		raw, _ := os.ReadFile(path)
		// Still a valid dump stream, but not the verified bytes.
		if err := os.WriteFile(path, append(raw, '\n'), 0o600); err != nil {
			t.Fatal(err)
		}
	}})
	if err == nil || !strings.Contains(err.Error(), "dump") {
		t.Fatalf("a swapped dump was loaded: %v", err)
	}
	if scalar[int](t, target.st, `SELECT COUNT(*) FROM app_platform_recovery_sets`) != 0 {
		t.Fatal("the refused restore committed rows")
	}
}
