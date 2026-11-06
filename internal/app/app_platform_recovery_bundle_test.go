package app

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/journal"
)

// platformBundleSource is a Portal with one installation and the files an operator would find on the old
// host: one checkpoint archive per retained recovery set, and the runner's journal.
type platformBundleSource struct {
	s                                         *Server
	installation, namespace, operation, epoch string
	release                                   string
	archives                                  map[string]string
	archiveBytes                              map[string][]byte
	hostDir                                   string
}

func platformBundleFixture(t *testing.T) *platformBundleSource {
	t.Helper()
	s, _, installation := platformLaunchFixture(t, false)
	b := &platformBundleSource{s: s, installation: installation, archives: map[string]string{}, archiveBytes: map[string][]byte{}, hostDir: t.TempDir()}
	b.namespace = scalar[string](t, s.st, `SELECT data_namespace_id FROM app_platform_activations WHERE installation_id=? AND state='active'`, installation)
	b.operation = scalar[string](t, s.st, `SELECT id FROM app_platform_operations WHERE installation_id=? AND action='install'`, installation)
	b.release = scalar[string](t, s.st, `SELECT release_sha256 FROM app_platform_activations WHERE installation_id=? AND state='active'`, installation)
	b.epoch = scalar[string](t, s.st, `SELECT controller_epoch FROM app_platform_installations WHERE id=?`, installation)
	return b
}

// retain records a recovery set and writes the archive it describes to the old host's checkpoint root.
func (b *platformBundleSource) retain(t *testing.T, n int, state, release string, content []byte) string {
	t.Helper()
	id := platformFixtureID(7100 + n)
	manifest := interface{}(nil)
	if state != "preparing" {
		manifest = platformDigest(content)
	}
	var verified any
	if state == "verified" {
		verified = int64(1)
	}
	platformTestInsert(t, b.s.st.poolExec(), "recovery_sets", map[string]any{
		"id": id, "installation_id": b.installation, "data_namespace_id": b.namespace, "operation_id": b.operation,
		"release_sha256": release, "data_format": 1, "checkpoint_id": platformFixtureID(7200 + n), "manifest_sha256": manifest,
		"bytes": len(content), "state": state, "controller_epoch": b.epoch, "writer_fence": 1,
		"verified_ms": verified, "deleted_ms": nil, "created_ms": 1, "updated_ms": 1,
	})
	path := filepath.Join(b.hostDir, platformFixtureID(7200+n)+".tar")
	if err := os.WriteFile(path, content, 0o400); err != nil {
		t.Fatal(err)
	}
	b.archives[id], b.archiveBytes[id] = path, content
	return id
}

func platformBundleFiles(t *testing.T, dir string) []string {
	t.Helper()
	var names []string
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(dir, path)
		names = append(names, rel)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return names
}

func TestRecoveryBundleManifestListsEveryRetainedCheckpoint(t *testing.T) {
	b := platformBundleFixture(t)
	s := b.s
	first := b.retain(t, 1, "verified", b.release, []byte("checkpoint archive one"))
	second := b.retain(t, 2, "verified", b.release, []byte("a second, longer checkpoint archive"))
	// A set that is still being cut, and a deleted one, are not retained: they have no archive to carry.
	preparing := b.retain(t, 3, "preparing", b.release, []byte("half written"))
	delete(b.archives, preparing)
	deleted := b.retain(t, 4, "verified", b.release, []byte("deleted archive"))
	mustExec(t, s.st, `UPDATE app_platform_recovery_sets SET state='deleted',deleted_ms=2 WHERE id=?`, deleted)
	delete(b.archives, deleted)
	dir := t.TempDir()
	manifest, err := s.writePlatformRecoveryBundle(dir, platformRecoveryBundleInput{Archives: b.archives})
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.Checkpoints) != 2 {
		t.Fatalf("the manifest lists %d checkpoints, want the 2 retained ones", len(manifest.Checkpoints))
	}
	for _, id := range []string{first, second} {
		var entry *platformBundleCheckpoint
		for i := range manifest.Checkpoints {
			if manifest.Checkpoints[i].RecoverySetID == id {
				entry = &manifest.Checkpoints[i]
			}
		}
		content := b.archiveBytes[id]
		if entry == nil || entry.Archive.SHA256 != platformDigest(content) || entry.Archive.Bytes != int64(len(content)) ||
			entry.InstallationID != b.installation || entry.NamespaceID != b.namespace || entry.ReleaseSHA256 != b.release {
			t.Fatalf("manifest entry for %s: %+v", id, entry)
		}
		// The copy in the bundle is the archive, and its digest is the one the database recorded.
		copied, err := os.ReadFile(filepath.Join(dir, entry.Archive.Path))
		if err != nil || string(copied) != string(content) ||
			entry.Archive.SHA256 != scalar[string](t, s.st, `SELECT manifest_sha256 FROM app_platform_recovery_sets WHERE id=?`, id) {
			t.Fatalf("bundled archive for %s differs from the recorded checkpoint: %v", id, err)
		}
	}
	// The dump and keyring are listed with the digest of what is on disk.
	for name, file := range map[string]platformBundleFile{"dump": manifest.Dump, "keyring": manifest.Keyring} {
		raw, err := os.ReadFile(filepath.Join(dir, file.Path))
		if err != nil || platformDigest(raw) != file.SHA256 || int64(len(raw)) != file.Bytes || file.Bytes == 0 {
			t.Fatalf("%s entry %+v: %v", name, file, err)
		}
	}
	// A bundle verifies against its own manifest, and any later change to it is caught.
	if _, err := verifyPlatformRecoveryBundle(dir); err != nil {
		t.Fatal("a fresh bundle did not verify:", err)
	}
	target := filepath.Join(dir, manifest.Checkpoints[0].Archive.Path)
	if err := os.Chmod(target, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("tampered"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := verifyPlatformRecoveryBundle(dir); err == nil {
		t.Fatal("a tampered archive verified")
	}
}

func TestRecoveryBundleRefusesWrongOrMissingArchives(t *testing.T) {
	for name, mutate := range map[string]func(*testing.T, *platformBundleSource, string){
		"archive missing": func(t *testing.T, b *platformBundleSource, id string) { delete(b.archives, id) },
		"digest differs": func(t *testing.T, b *platformBundleSource, id string) {
			if err := os.Chmod(b.archives[id], 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(b.archives[id], []byte("checkpoint archive 0ne"), 0o600); err != nil {
				t.Fatal(err)
			}
		},
		"size differs": func(t *testing.T, b *platformBundleSource, id string) {
			if err := os.Chmod(b.archives[id], 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(b.archives[id], []byte("checkpoint archive one plus"), 0o600); err != nil {
				t.Fatal(err)
			}
		},
		"archive for an unknown set": func(t *testing.T, b *platformBundleSource, id string) {
			b.archives[platformFixtureID(7999)] = b.archives[id]
		},
		"archive is a symlink": func(t *testing.T, b *platformBundleSource, id string) {
			link := filepath.Join(b.hostDir, "link.tar")
			if err := os.Symlink(b.archives[id], link); err != nil {
				t.Fatal(err)
			}
			b.archives[id] = link
		},
	} {
		t.Run(name, func(t *testing.T) {
			b := platformBundleFixture(t)
			id := b.retain(t, 1, "verified", b.release, []byte("checkpoint archive one"))
			mutate(t, b, id)
			dir := t.TempDir()
			if _, err := b.s.writePlatformRecoveryBundle(dir, platformRecoveryBundleInput{Archives: b.archives}); err == nil {
				t.Fatal("a bundle was written despite it")
			}
			if left := platformBundleFiles(t, dir); len(left) != 0 {
				t.Fatalf("a refused bundle left files behind: %v", left)
			}
		})
	}
}

// The old runner's journal is evidence about the old host. It is copied byte for byte and never opened:
// opening it would create write-ahead files beside it and take a lock that belongs to a runner.
func TestRecoveryBundleCopiesOldJournalWithoutOpeningIt(t *testing.T) {
	b := platformBundleFixture(t)
	b.retain(t, 1, "verified", b.release, []byte("checkpoint archive one"))
	oldHost, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(oldHost, 0o700); err != nil {
		t.Fatal(err)
	}
	store, err := journal.Open(oldHost)
	if errors.Is(err, journal.ErrUnsupported) {
		t.Skip("the runner journal is not supported on this platform")
	}
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(oldHost, "journal.sqlite3")
	original, err := os.ReadFile(source)
	if err != nil || len(original) == 0 {
		t.Fatal("fixture journal missing", err)
	}
	beforeHost := platformBundleFiles(t, oldHost)
	dir := t.TempDir()
	manifest, err := b.s.writePlatformRecoveryBundle(dir, platformRecoveryBundleInput{Archives: b.archives, RunnerJournal: source})
	if err != nil {
		t.Fatal(err)
	}
	if manifest.RunnerJournal == nil || manifest.RunnerJournal.SHA256 != platformDigest(original) || manifest.RunnerJournal.Bytes != int64(len(original)) {
		t.Fatalf("journal entry: %+v", manifest.RunnerJournal)
	}
	copied, err := os.ReadFile(filepath.Join(dir, manifest.RunnerJournal.Path))
	if err != nil || string(copied) != string(original) {
		t.Fatalf("the bundled journal is not a byte copy: %v", err)
	}
	if _, err := verifyPlatformRecoveryBundle(dir); err != nil {
		t.Fatal(err)
	}
	for _, name := range platformBundleFiles(t, dir) {
		if strings.HasSuffix(name, "-wal") || strings.HasSuffix(name, "-shm") || strings.HasSuffix(name, "-journal") {
			t.Fatalf("a sidecar file appeared in the bundle: %s", name)
		}
	}
	if after := platformBundleFiles(t, oldHost); !reflect.DeepEqual(after, beforeHost) {
		t.Fatalf("the old host's directory changed: %v -> %v", beforeHost, after)
	}
	if now, _ := os.ReadFile(source); string(now) != string(original) {
		t.Fatal("the source journal was modified")
	}
	// A journal that still has write-ahead content belongs to a runner that has not stopped: refuse.
	if err := os.WriteFile(source+"-wal", []byte("live"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := b.s.writePlatformRecoveryBundle(t.TempDir(), platformRecoveryBundleInput{Archives: b.archives, RunnerJournal: source}); err == nil {
		t.Fatal("a journal with live write-ahead content was copied")
	}
}

func TestRequiredReleaseBlobsCoverActiveAndPinnedReleases(t *testing.T) {
	b := platformBundleFixture(t)
	s := b.s
	copyRelease := func(sha, version string, managed int) {
		mustExec(t, s.st, `INSERT INTO app_platform_releases(sha256,app_id,publisher_id,version,descriptor,signature,verification_key_id,trust_revision,verified_ms,frontend_sha256,managed,created_ms)
			SELECT ?,app_id,publisher_id,?,descriptor,signature,verification_key_id,trust_revision,verified_ms,frontend_sha256,?,created_ms FROM app_platform_releases WHERE sha256=?`,
			sha, version, managed, b.release)
	}
	pinned, current, unrelated := platformDigest([]byte("pinned")), platformDigest([]byte("current")), platformDigest([]byte("unrelated"))
	copyRelease(pinned, "0.9.0", 0)
	copyRelease(current, "1.1.0", 1)
	copyRelease(unrelated, "0.1.0", 0)
	// Generation 1 (the fixture's release) is retired; generation 2 is the installation's last activation.
	mustExec(t, s.st, `UPDATE app_platform_activations SET state='retired' WHERE installation_id=?`, b.installation)
	platformTestInsert(t, s.st.poolExec(), "activations", map[string]any{
		"id": platformFixtureID(7301), "installation_id": b.installation, "release_sha256": current, "generation": 2, "policy_revision": 1,
		"data_namespace_id": b.namespace, "data_format": 1, "state": "active", "readiness_evidence": "{}", "observed_ms": 1, "created_ms": 2, "updated_ms": 2,
	})
	// The superseded release is held by a retained recovery set, and so is one that was never active.
	b.retain(t, 1, "verified", b.release, []byte("one"))
	b.retain(t, 2, "verified", pinned, []byte("two"))
	b.retain(t, 3, "verified", current, []byte("three"))
	blobs, err := platformRequiredReleaseBlobs(s.st.poolExec())
	if err != nil {
		t.Fatal(err)
	}
	byRelease := map[string]platformReleaseBlob{}
	for _, blob := range blobs {
		byRelease[blob.ReleaseSHA256] = blob
	}
	if len(blobs) != 3 || len(byRelease) != 3 {
		t.Fatalf("inventory has %d entries: %+v", len(blobs), blobs)
	}
	if _, ok := byRelease[unrelated]; ok {
		t.Fatal("a staged release nothing refers to was inventoried")
	}
	want := map[string][]string{current: {"active", "pinned"}, b.release: {"pinned"}, pinned: {"pinned"}}
	for release, reasons := range want {
		if got := byRelease[release].Reasons; !reflect.DeepEqual(got, reasons) {
			t.Fatalf("release %s: reasons %v, want %v", release[:8], got, reasons)
		}
	}
	// A static frontend travels in the dump (it is stored in the database); a managed backend's content
	// has no blob store yet and is reported as pending rather than silently omitted.
	for _, blob := range blobs {
		frontend := scalar[string](t, s.st, `SELECT frontend_sha256 FROM app_platform_releases WHERE sha256=?`, blob.ReleaseSHA256)
		if blob.Frontend != platformBlobInDump || blob.FrontendSHA256 != frontend {
			t.Fatalf("frontend of %s: %+v", blob.ReleaseSHA256[:8], blob)
		}
	}
	if byRelease[current].Backend != platformBlobPending || byRelease[pinned].Backend != platformBlobNone || byRelease[b.release].Backend != platformBlobNone {
		t.Fatalf("backend states: %+v", blobs)
	}
	if !reflect.DeepEqual(platformPendingReleaseBlobs(blobs), []string{current}) {
		t.Fatal("the pending list does not name the managed release")
	}
	// A removed installation needs nothing from its last activation, but its pinned sets still count.
	mustExec(t, s.st, `UPDATE app_platform_installations SET desired_state='removed',phase='removed' WHERE id=?`, b.installation)
	blobs, err = platformRequiredReleaseBlobs(s.st.poolExec())
	if err != nil {
		t.Fatal(err)
	}
	for _, blob := range blobs {
		if blob.ReleaseSHA256 == current && !reflect.DeepEqual(blob.Reasons, []string{"pinned"}) {
			t.Fatalf("a removed installation's activation still counts as active: %+v", blob)
		}
	}
}
