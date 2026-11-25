package app

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/config"
)

// The operator command for the recovery bundle: `recovery-bundle write` on the old host and
// `recovery-bundle restore` on the fresh one. These tests drive the functions behind the command; the
// bundle format and its writer are covered by the bundle tests.

// bundleHostFixture is a source Portal with two retained recovery sets, a journal file and the checkpoint
// directory an operator would point the command at.
func bundleHostFixture(t *testing.T, pg bool) (*platformBundleSource, string) {
	t.Helper()
	b := platformBundleFixtureOn(t, pg)
	b.retain(t, 1, "verified", b.release, []byte("checkpoint archive one"))
	b.retain(t, 2, "verified", b.release, []byte("a second, longer checkpoint archive"))
	journal := filepath.Join(b.hostDir, "journal.sqlite3")
	if err := os.WriteFile(journal, []byte("pretend runner journal"), 0o600); err != nil {
		t.Fatal(err)
	}
	return b, journal
}

func TestRecoveryBundleCommandRoundTripRestoresIntoAFreshStore(t *testing.T) {
	t.Parallel()
	b, journal := bundleHostFixture(t, false)
	s := b.s
	credentialID := platformFixtureID(7401)
	sealed, err := s.sealPlatformCredentialValue(credentialID, 1, []byte("round-trip-secret"))
	if err != nil {
		t.Fatal(err)
	}
	platformTestInsert(t, s.st.poolExec(), "credentials", map[string]any{
		"id": credentialID, "label": "Round trip", "state": "active", "sealed_value": sealed,
		"value_revision": 1, "revision": 1, "created_ms": 1, "updated_ms": 1, "revoked_ms": nil,
	})
	dir := t.TempDir()
	manifest, err := s.writeRecoveryBundleFromHost(dir, recoveryBundleHost{Checkpoints: []string{b.hostDir}, RunnerJournal: journal})
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.Checkpoints) != 2 || manifest.RunnerJournal == nil {
		t.Fatalf("manifest: %+v", manifest)
	}

	// The fresh host: an empty database and the original secret key, configured the way an operator would.
	freshDir := t.TempDir()
	cfgPath := filepath.Join(freshDir, "config.yaml")
	dbPath := filepath.Join(freshDir, "portal.db")
	yaml := "listen: \":0\"\nsecret_key: \"" + rotOldKey + "\"\ndb_driver: \"sqlite\"\ndb_path: \"" + dbPath + "\"\n"
	if err := os.WriteFile(cfgPath, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	// Without --force it is a dry run: the bundle is verified and the dump parsed, and nothing is written.
	if err := RecoveryBundleCommand(cfgPath, []string{"restore", dir}, &out); err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if !strings.Contains(out.String(), "DRY RUN") {
		t.Fatalf("dry run output: %s", out.String())
	}
	fresh, err := OpenStore("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if n := scalar[int](t, fresh, `SELECT COUNT(*) FROM app_platform_recovery_sets`); n != 0 {
		t.Fatalf("the dry run wrote %d recovery sets", n)
	}
	fresh.Close()

	out.Reset()
	if err := RecoveryBundleCommand(cfgPath, []string{"restore", dir, "--force"}, &out); err != nil {
		t.Fatalf("restore: %v", err)
	}
	fresh, err = OpenStore("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer fresh.Close()
	// Equality with the source: every retained recovery set, the release inventory and the keyring.
	if got, want := mustRetained(t, fresh), mustRetained(t, s.st); !reflect.DeepEqual(got, want) {
		t.Fatalf("retained sets differ after restore:\n%+v\n%+v", got, want)
	}
	restored, err := platformRequiredReleaseBlobs(fresh.poolExec())
	if err != nil || !reflect.DeepEqual(restored, manifest.Releases) {
		t.Fatalf("release inventory differs: %v\n%+v\n%+v", err, restored, manifest.Releases)
	}
	sourceKeyring, err := platformBundleKeyring(s.st.poolExec())
	if err != nil {
		t.Fatal(err)
	}
	restoredKeyring, err := platformBundleKeyring(fresh.poolExec())
	if err != nil || !bytes.Equal(sourceKeyring, restoredKeyring) {
		t.Fatalf("keyring differs after restore: %v", err)
	}
	// The restored Portal is paused, the credential opens with the same key, and the archives named by the
	// recovery sets are in the bundle with the recorded digests, ready for the runner host.
	if v := scalar[string](t, fresh, `SELECT v FROM meta WHERE k='app_platform_restore_paused'`); v != "1" {
		t.Fatalf("restored Portal is not paused: %q", v)
	}
	server := &Server{st: fresh, cfg: &config.Config{SecretKey: rotOldKey}}
	var sealedBack string
	if err := fresh.queryRow(`SELECT sealed_value FROM app_platform_credentials WHERE id=?`, credentialID).Scan(&sealedBack); err != nil {
		t.Fatal(err)
	}
	plain, err := server.openPlatformCredentialValue(credentialID, 1, sealedBack)
	if err != nil || string(plain) != "round-trip-secret" {
		t.Fatalf("credential did not decrypt: %v", err)
	}
	for _, entry := range manifest.Checkpoints {
		raw, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(entry.Archive.Path)))
		if err != nil || platformDigest(raw) != scalar[string](t, fresh, `SELECT manifest_sha256 FROM app_platform_recovery_sets WHERE id=?`, entry.RecoverySetID) {
			t.Fatalf("archive %s in the bundle is not the recorded checkpoint: %v", entry.Archive.Path, err)
		}
		if !strings.Contains(out.String(), entry.Archive.Path) {
			t.Fatalf("the restore output does not name %s for the runner host:\n%s", entry.Archive.Path, out.String())
		}
	}
}

func mustRetained(t *testing.T, st *Store) []platformRetainedSet {
	t.Helper()
	sets, err := loadPlatformRetainedSets(st.poolExec())
	if err != nil {
		t.Fatal(err)
	}
	return sets
}

// Fails if the bundle is written from reads that are not one snapshot, or a database that moved is accepted.
func TestRecoveryBundleRefusesADatabaseThatMovesWhileItIsWritten(t *testing.T) {
	t.Parallel()
	eachPlatformDriver(t, func(t *testing.T, pg bool) {
		for name, move := range map[string]func(*testing.T, *platformBundleSource){
			"a retained set is deleted": func(t *testing.T, b *platformBundleSource) {
				mustExec(t, b.s.st, `UPDATE app_platform_recovery_sets SET state='deleted',deleted_ms=2 WHERE id=?`, platformFixtureID(7100+1))
			},
			"a set is retained": func(t *testing.T, b *platformBundleSource) {
				b.retain(t, 3, "verified", b.release, []byte("a late checkpoint"))
			},
		} {
			t.Run(name, func(t *testing.T) {
				b, journal := bundleHostFixture(t, pg)
				dir := t.TempDir()
				_, err := b.s.writePlatformRecoveryBundle(dir, platformRecoveryBundleInput{
					Archives: b.archives, RunnerJournal: journal,
					afterSnapshot: func() { move(t, b) },
				})
				if !errors.Is(err, errPlatformBundle) || !strings.Contains(err.Error(), "changed while the bundle was written") {
					t.Fatalf("a moving database was not refused: %v", err)
				}
				if left := platformBundleFiles(t, dir); len(left) != 0 {
					t.Fatalf("a refused bundle left files behind: %v", left)
				}
			})
		}
	})
}

// PostgreSQL only: SQLite's single connection makes a concurrent writer wait, so only PostgreSQL can show that
// the dump, the retained sets and the releases are one snapshot. A row written after the first read is in none.
func TestRecoveryBundleReadsEverythingInOneSnapshot(t *testing.T) {
	t.Parallel()
	b, journal := bundleHostFixture(t, true)
	dir := t.TempDir()
	_, err := b.s.writePlatformRecoveryBundle(dir, platformRecoveryBundleInput{
		Archives: b.archives, RunnerJournal: journal,
		inSnapshot: func() {
			mustExec(t, b.s.st, `INSERT INTO meta(k,v) VALUES('bundle_snapshot_probe','1') ON CONFLICT(k) DO UPDATE SET v=excluded.v`)
		},
	})
	t.Cleanup(func() { mustExec(t, b.s.st, `DELETE FROM meta WHERE k='bundle_snapshot_probe'`) })
	if err != nil {
		t.Fatal(err)
	}
	dump, err := os.ReadFile(filepath.Join(dir, platformBundleDumpName))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(dump, []byte("bundle_snapshot_probe")) {
		t.Fatal("the dump saw a row committed after the snapshot began")
	}
}

func TestRecoveryBundleCommandRefusesMissingAndTamperedCheckpoints(t *testing.T) {
	t.Parallel()
	t.Run("an archive is missing", func(t *testing.T) {
		b, journal := bundleHostFixture(t, false)
		if err := os.Remove(filepath.Join(b.hostDir, platformFixtureID(7201)+".tar")); err != nil {
			t.Fatal(err)
		}
		dir := t.TempDir()
		_, err := b.s.writeRecoveryBundleFromHost(dir, recoveryBundleHost{Checkpoints: []string{b.hostDir}, RunnerJournal: journal})
		if !errors.Is(err, errPlatformBundle) || !strings.Contains(err.Error(), platformFixtureID(7101)) {
			t.Fatalf("a missing archive was not refused by recovery set: %v", err)
		}
		if left := platformBundleFiles(t, dir); len(left) != 0 {
			t.Fatalf("files left: %v", left)
		}
	})
	t.Run("an archive no recovery set retains", func(t *testing.T) {
		b, journal := bundleHostFixture(t, false)
		stray := filepath.Join(t.TempDir(), platformFixtureID(7299)+".tar")
		if err := os.WriteFile(stray, []byte("stray"), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := b.s.writeRecoveryBundleFromHost(t.TempDir(), recoveryBundleHost{Checkpoints: []string{b.hostDir, stray}, RunnerJournal: journal})
		if !errors.Is(err, errPlatformBundle) {
			t.Fatalf("a stray archive was accepted: %v", err)
		}
	})
	t.Run("an archive is tampered with before writing", func(t *testing.T) {
		b, journal := bundleHostFixture(t, false)
		path := filepath.Join(b.hostDir, platformFixtureID(7201)+".tar")
		if err := os.Chmod(path, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("checkpoint archive 0ne"), 0o600); err != nil {
			t.Fatal(err)
		}
		dir := t.TempDir()
		if _, err := b.s.writeRecoveryBundleFromHost(dir, recoveryBundleHost{Checkpoints: []string{b.hostDir}, RunnerJournal: journal}); !errors.Is(err, errPlatformBundle) {
			t.Fatalf("a tampered archive was accepted: %v", err)
		}
		if left := platformBundleFiles(t, dir); len(left) != 0 {
			t.Fatalf("files left: %v", left)
		}
	})
	t.Run("an archive in the checkpoint root dot directory is found", func(t *testing.T) {
		b, journal := bundleHostFixture(t, false)
		root := t.TempDir()
		hidden := filepath.Join(root, ".portal-checkpoints")
		if err := os.Mkdir(hidden, 0o700); err != nil {
			t.Fatal(err)
		}
		for _, id := range []string{platformFixtureID(7201), platformFixtureID(7202)} {
			raw, _ := os.ReadFile(filepath.Join(b.hostDir, id+".tar"))
			if err := os.WriteFile(filepath.Join(hidden, id+".tar"), raw, 0o400); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := b.s.writeRecoveryBundleFromHost(t.TempDir(), recoveryBundleHost{Checkpoints: []string{root}, RunnerJournal: journal}); err != nil {
			t.Fatalf("the checkpoint root was not searched: %v", err)
		}
	})
	t.Run("the same archive in two places is ambiguous", func(t *testing.T) {
		b, journal := bundleHostFixture(t, false)
		other := t.TempDir()
		raw, _ := os.ReadFile(filepath.Join(b.hostDir, platformFixtureID(7201)+".tar"))
		if err := os.WriteFile(filepath.Join(other, platformFixtureID(7201)+".tar"), raw, 0o400); err != nil {
			t.Fatal(err)
		}
		if _, err := b.s.writeRecoveryBundleFromHost(t.TempDir(), recoveryBundleHost{Checkpoints: []string{b.hostDir, other}, RunnerJournal: journal}); !errors.Is(err, errPlatformBundle) {
			t.Fatalf("an ambiguous archive was accepted: %v", err)
		}
	})
}

func TestRecoveryBundleCommandRefusesAnUnreadableJournal(t *testing.T) {
	t.Parallel()
	b, _ := bundleHostFixture(t, false)
	for name, path := range map[string]string{
		"missing":   filepath.Join(b.hostDir, "nope.sqlite3"),
		"directory": b.hostDir,
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			if _, err := b.s.writeRecoveryBundleFromHost(dir, recoveryBundleHost{Checkpoints: []string{b.hostDir}, RunnerJournal: path}); !errors.Is(err, errPlatformBundle) {
				t.Fatalf("an unreadable journal was accepted: %v", err)
			}
			if left := platformBundleFiles(t, dir); len(left) != 0 {
				t.Fatalf("files left: %v", left)
			}
		})
	}
	t.Run("no journal and no explicit opt-out", func(t *testing.T) {
		if _, err := b.s.writeRecoveryBundleFromHost(t.TempDir(), recoveryBundleHost{Checkpoints: []string{b.hostDir}}); !errors.Is(err, errPlatformBundle) {
			t.Fatalf("a bundle without a journal was written by default: %v", err)
		}
	})
	t.Run("explicit opt-out", func(t *testing.T) {
		manifest, err := b.s.writeRecoveryBundleFromHost(t.TempDir(), recoveryBundleHost{Checkpoints: []string{b.hostDir}, NoRunnerJournal: true})
		if err != nil || manifest.RunnerJournal != nil {
			t.Fatalf("opt-out: %v %+v", err, manifest.RunnerJournal)
		}
	})
}

// A bundle damaged after it was written is refused on the fresh host before any table is touched.
func TestRecoveryBundleRestoreRefusesATamperedOrIncompleteBundle(t *testing.T) {
	t.Parallel()
	for name, damage := range map[string]func(t *testing.T, dir string, m platformRecoveryBundleManifest){
		"archive tampered": func(t *testing.T, dir string, m platformRecoveryBundleManifest) {
			rewriteBundleFile(t, filepath.Join(dir, m.Checkpoints[0].Archive.Path), []byte("tampered"))
		},
		"dump tampered": func(t *testing.T, dir string, m platformRecoveryBundleManifest) {
			rewriteBundleFile(t, filepath.Join(dir, m.Dump.Path), []byte("{}\n"))
		},
		"archive missing": func(t *testing.T, dir string, m platformRecoveryBundleManifest) {
			if err := os.Remove(filepath.Join(dir, m.Checkpoints[1].Archive.Path)); err != nil {
				t.Fatal(err)
			}
		},
		"manifest missing": func(t *testing.T, dir string, m platformRecoveryBundleManifest) {
			if err := os.Remove(filepath.Join(dir, platformBundleManifestName)); err != nil {
				t.Fatal(err)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			b, journal := bundleHostFixture(t, false)
			dir := t.TempDir()
			manifest, err := b.s.writeRecoveryBundleFromHost(dir, recoveryBundleHost{Checkpoints: []string{b.hostDir}, RunnerJournal: journal})
			if err != nil {
				t.Fatal(err)
			}
			damage(t, dir, manifest)
			freshDir := t.TempDir()
			cfgPath := filepath.Join(freshDir, "config.yaml")
			dbPath := filepath.Join(freshDir, "portal.db")
			yaml := "secret_key: \"" + rotOldKey + "\"\ndb_driver: \"sqlite\"\ndb_path: \"" + dbPath + "\"\n"
			if err := os.WriteFile(cfgPath, []byte(yaml), 0o600); err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			if err := RecoveryBundleCommand(cfgPath, []string{"restore", dir, "--force"}, &out); err == nil {
				t.Fatal("a damaged bundle was restored")
			}
			fresh, err := OpenStore("sqlite", dbPath)
			if err != nil {
				t.Fatal(err)
			}
			defer fresh.Close()
			if n := scalar[int](t, fresh, `SELECT COUNT(*) FROM app_platform_recovery_sets`); n != 0 {
				t.Fatalf("a refused restore wrote %d recovery sets", n)
			}
		})
	}
}

func rewriteBundleFile(t *testing.T, path string, content []byte) {
	t.Helper()
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestRecoveryBundleCommandArguments(t *testing.T) {
	t.Parallel()
	ok := map[string]struct {
		args []string
		want recoveryBundleArgs
	}{
		"flags after the directory": {
			[]string{"write", "/out", "--runner-journal", "/j", "--checkpoints", "/c"},
			recoveryBundleArgs{Action: "write", Dir: "/out", Host: recoveryBundleHost{Checkpoints: []string{"/c"}, RunnerJournal: "/j"}},
		},
		"flags before the directory, repeated checkpoints": {
			[]string{"write", "--checkpoints", "/c1", "--checkpoints=/c2", "--no-runner-journal", "/out"},
			recoveryBundleArgs{Action: "write", Dir: "/out", Host: recoveryBundleHost{Checkpoints: []string{"/c1", "/c2"}, NoRunnerJournal: true}},
		},
		"restore dry run": {[]string{"restore", "/in"}, recoveryBundleArgs{Action: "restore", Dir: "/in"}},
		"restore replace": {[]string{"restore", "/in", "--force", "--replace-existing"}, recoveryBundleArgs{Action: "restore", Dir: "/in", Force: true, ReplaceExisting: true}},
		"restore force":   {[]string{"restore", "/in", "--force"}, recoveryBundleArgs{Action: "restore", Dir: "/in", Force: true}},
	}
	for name, c := range ok {
		t.Run(name, func(t *testing.T) {
			got, err := parseRecoveryBundleArgs(c.args)
			if err != nil || !reflect.DeepEqual(got, c.want) {
				t.Fatalf("got %+v, %v; want %+v", got, err, c.want)
			}
		})
	}
	for name, args := range map[string][]string{
		"no action":                      {},
		"unknown action":                 {"verify", "/x"},
		"write without directory":        {"write", "--runner-journal", "/j", "--checkpoints", "/c"},
		"write without checkpoint":       {"write", "/out", "--runner-journal", "/j"},
		"write without a journal choice": {"write", "/out", "--checkpoints", "/c"},
		"journal and opt-out":            {"write", "/out", "--checkpoints", "/c", "--runner-journal", "/j", "--no-runner-journal"},
		"two directories":                {"write", "/a", "/b", "--checkpoints", "/c", "--no-runner-journal"},
		"restore with write flag":        {"restore", "/in", "--checkpoints", "/c"},
		"write with force":               {"write", "/out", "--checkpoints", "/c", "--no-runner-journal", "--force"},
		"replace without force":          {"restore", "/in", "--replace-existing"},
		"unknown flag":                   {"write", "/out", "--bogus"},
	} {
		t.Run("refuses "+name, func(t *testing.T) {
			if _, err := parseRecoveryBundleArgs(args); err == nil {
				t.Fatalf("accepted %v", args)
			}
		})
	}
}
