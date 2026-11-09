package app

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// The recovery bundle (doc 167 section 1, doc 168 step 13) is what an operator carries from a lost host
// to a fresh one: the Portal dump, the keyring it was sealed with, every retained checkpoint archive and
// the old runner's journal. The manifest names each file with its SHA-256 so the fresh host can prove it
// received what the old one held. The journal is evidence about the old host and is carried as
// provenance only: it is copied as bytes and never opened, because opening a journal creates files beside
// it and takes a lock that belongs to a runner.

const platformRecoveryBundleFormat = "portal.recovery-bundle/1-dev.1"

const (
	platformBundleManifestName = "manifest.json"
	platformBundleDumpName     = "portal.dump"
	platformBundleKeyringName  = "keyring.json"
	platformBundleJournalName  = "provenance/runner-journal.sqlite3"
)

// Where a release's content comes from after a restore.
const (
	// A static frontend is stored in the Portal's database and travels in the dump.
	platformBlobInDump = "in_dump"
	// A managed backend's content is outside the database. The inventory marks it pending until the writer
	// resolves it: carried in the bundle, or a development reference that names no content.
	platformBlobPending = "pending"
	platformBlobNone    = "none"
	// The image blobs of a managed release travel in the bundle under app-blobs/<sha256>.
	platformBlobInBundle = "in_bundle"
	// A development release names a registry reference: there is no content to carry, and the image must
	// already exist on the new host.
	platformBlobReference = "reference"
)

type platformBundleFile struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Bytes  int64  `json:"bytes"`
}

type platformBundleCheckpoint struct {
	RecoverySetID  string             `json:"recovery_set_id"`
	InstallationID string             `json:"installation_id"`
	CheckpointID   string             `json:"checkpoint_id"`
	NamespaceID    string             `json:"namespace_id"`
	ReleaseSHA256  string             `json:"release_sha256"`
	Archive        platformBundleFile `json:"archive"`
}

type platformReleaseBlob struct {
	ReleaseSHA256  string   `json:"release_sha256"`
	Reasons        []string `json:"reasons"`
	FrontendSHA256 string   `json:"frontend_sha256"`
	Frontend       string   `json:"frontend"`
	Backend        string   `json:"backend"`
	// ImageBlobs names the blobs of an in-bundle backend, by digest.
	ImageBlobs []string `json:"image_blobs,omitempty"`
}

type platformRecoveryBundleManifest struct {
	Format        string                     `json:"format"`
	Dump          platformBundleFile         `json:"portal_dump"`
	Keyring       platformBundleFile         `json:"keyring"`
	Checkpoints   []platformBundleCheckpoint `json:"checkpoints"`
	RunnerJournal *platformBundleFile        `json:"runner_journal,omitempty"`
	Releases      []platformReleaseBlob      `json:"required_releases"`
	// ImageBlobs are the managed image blobs of the required releases, one file per digest.
	ImageBlobs []platformBundleFile `json:"image_blobs,omitempty"`
}

type platformRecoveryBundleInput struct {
	// Archives maps each retained recovery set to the file that holds its checkpoint archive on the host.
	Archives map[string]string
	// RunnerJournal is the old runner's journal file. Empty when the host has none to carry.
	RunnerJournal string
}

var errPlatformBundle = errors.New("recovery bundle is incomplete or inconsistent")

// platformRetainedRecoverySets are the recovery sets whose archive a restored Portal can still import:
// cut and verified, not deleted. A set that is being cut has no digest to carry.
const platformRetainedRecoverySets = `state IN ('verified','restoring','review_required') AND deleted_ms IS NULL AND manifest_sha256 IS NOT NULL`

type platformRetainedSet struct {
	id, installation, checkpoint, namespace, release, manifest string
	bytes                                                      int64
}

func loadPlatformRetainedSets(e migExec) ([]platformRetainedSet, error) {
	rows, err := e.query(`SELECT id,installation_id,checkpoint_id,data_namespace_id,release_sha256,manifest_sha256,bytes
		FROM app_platform_recovery_sets WHERE ` + platformRetainedRecoverySets + ` ORDER BY created_ms,id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var sets []platformRetainedSet
	for rows.Next() {
		var r platformRetainedSet
		if err := rows.Scan(&r.id, &r.installation, &r.checkpoint, &r.namespace, &r.release, &r.manifest, &r.bytes); err != nil {
			return nil, err
		}
		sets = append(sets, r)
	}
	return sets, rows.Err()
}

// platformRequiredReleaseBlobs lists the releases a restored Portal must be able to run again: the last
// activation of every installation that still exists, and every release a retained recovery set pins
// (restoring that set re-activates its release).
func platformRequiredReleaseBlobs(e migExec) ([]platformReleaseBlob, error) {
	pins, err := loadPlatformReleasePins(e, 0)
	if err != nil {
		return nil, err
	}
	reasons := map[string]map[string]bool{}
	for release, why := range pins {
		for _, reason := range []string{"active", "pinned"} {
			if why[reason] {
				if reasons[release] == nil {
					reasons[release] = map[string]bool{}
				}
				reasons[release][reason] = true
			}
		}
	}
	blobs := make([]platformReleaseBlob, 0, len(reasons))
	for release, set := range reasons {
		blob := platformReleaseBlob{ReleaseSHA256: release, Backend: platformBlobNone}
		for _, reason := range []string{"active", "pinned"} {
			if set[reason] {
				blob.Reasons = append(blob.Reasons, reason)
			}
		}
		var managed int
		var inDump int
		if err := e.queryRow(`SELECT r.frontend_sha256,r.managed,(SELECT COUNT(*) FROM app_platform_frontend_artifacts f WHERE f.sha256=r.frontend_sha256)
			FROM app_platform_releases r WHERE r.sha256=?`, release).Scan(&blob.FrontendSHA256, &managed, &inDump); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil, fmt.Errorf("%w: release %s is referenced but not stored", errPlatformBundle, release)
			}
			return nil, err
		}
		if inDump != 1 {
			return nil, fmt.Errorf("%w: the frontend of release %s is not in the database", errPlatformBundle, release)
		}
		blob.Frontend = platformBlobInDump
		if managed == 1 {
			blob.Backend = platformBlobPending
		}
		blobs = append(blobs, blob)
	}
	sort.Slice(blobs, func(i, j int) bool { return blobs[i].ReleaseSHA256 < blobs[j].ReleaseSHA256 })
	return blobs, nil
}

// platformPendingReleaseBlobs names the releases whose managed backend content nothing yet carries.
func platformPendingReleaseBlobs(blobs []platformReleaseBlob) []string {
	var pending []string
	for _, blob := range blobs {
		if blob.Backend == platformBlobPending {
			pending = append(pending, blob.ReleaseSHA256)
		}
	}
	return pending
}

func platformBundleKeyring(e migExec) ([]byte, error) {
	record, err := readStoredKeyring(e)
	if err != nil {
		return nil, err
	}
	if !record.exists {
		return nil, fmt.Errorf("%w: the Portal has no keyring", errPlatformBundle)
	}
	return json.Marshal(struct {
		Format  string `json:"format"`
		Salt    string `json:"keyring_salt"`
		Wrapped string `json:"keyring_wrapped_dek"`
	}{platformRecoveryBundleFormat + "/keyring", record.salt, record.wrapped})
}

// copyPlatformBundleFile copies source into the bundle as bytes. It opens nothing but the two files, and
// refuses anything that is not a regular file so a link cannot redirect the read.
func copyPlatformBundleFile(dir, relative, source string) (platformBundleFile, error) {
	var zero platformBundleFile
	info, err := os.Lstat(source)
	if err != nil || !info.Mode().IsRegular() {
		return zero, fmt.Errorf("%w: %s is not a regular file", errPlatformBundle, filepath.Base(source))
	}
	in, err := os.Open(source)
	if err != nil {
		return zero, err
	}
	defer in.Close()
	return writePlatformBundleFile(dir, relative, in)
}

func writePlatformBundleFile(dir, relative string, in io.Reader) (platformBundleFile, error) {
	return streamPlatformBundleFile(dir, relative, func(w io.Writer) error {
		_, err := io.Copy(w, in)
		return err
	})
}

// streamPlatformBundleFile creates a new file inside the bundle, lets fill write it, and returns its
// size and digest as written.
func streamPlatformBundleFile(dir, relative string, fill func(io.Writer) error) (platformBundleFile, error) {
	var zero platformBundleFile
	path := filepath.Join(dir, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return zero, err
	}
	out, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return zero, err
	}
	hash := sha256.New()
	counter := &platformByteCounter{}
	fillErr := fill(io.MultiWriter(out, hash, counter))
	syncErr := out.Sync()
	closeErr := out.Close()
	if err := errors.Join(fillErr, syncErr, closeErr); err != nil {
		return zero, err
	}
	return platformBundleFile{Path: relative, SHA256: hex.EncodeToString(hash.Sum(nil)), Bytes: counter.n}, nil
}

type platformByteCounter struct{ n int64 }

func (c *platformByteCounter) Write(p []byte) (int, error) {
	c.n += int64(len(p))
	return len(p), nil
}

// writePlatformRecoveryBundle writes the bundle into an empty directory. It refuses, and removes what it
// wrote, if any retained checkpoint has no archive, an archive that is not the recorded one, or the
// database changed while it was being read.
func (s *Server) writePlatformRecoveryBundle(dir string, in platformRecoveryBundleInput) (manifest platformRecoveryBundleManifest, err error) {
	entries, readErr := os.ReadDir(dir)
	if readErr != nil || len(entries) != 0 {
		return manifest, fmt.Errorf("%w: the bundle directory must exist and be empty", errPlatformBundle)
	}
	defer func() {
		if err != nil {
			// Only what this call created: the directory itself was empty when it started.
			if left, readErr := os.ReadDir(dir); readErr == nil {
				for _, entry := range left {
					os.RemoveAll(filepath.Join(dir, entry.Name()))
				}
			}
		}
	}()
	pool := s.st.poolExec()
	before, err := loadPlatformRetainedSets(pool)
	if err != nil {
		return manifest, err
	}
	known := map[string]bool{}
	for _, set := range before {
		known[set.id] = true
	}
	for id := range in.Archives {
		if !known[id] {
			return manifest, fmt.Errorf("%w: an archive was given for a recovery set that is not retained", errPlatformBundle)
		}
	}
	manifest.Format = platformRecoveryBundleFormat
	if manifest.Dump, err = streamPlatformBundleFile(dir, platformBundleDumpName, func(w io.Writer) error {
		_, _, dumpErr := s.st.dumpTo(w)
		return dumpErr
	}); err != nil {
		return manifest, err
	}
	keyring, err := platformBundleKeyring(pool)
	if err != nil {
		return manifest, err
	}
	if manifest.Keyring, err = writePlatformBundleFile(dir, platformBundleKeyringName, bytes.NewReader(keyring)); err != nil {
		return manifest, err
	}
	manifest.Checkpoints = []platformBundleCheckpoint{}
	for _, set := range before {
		source, ok := in.Archives[set.id]
		if !ok {
			return manifest, fmt.Errorf("%w: no archive for recovery set %s", errPlatformBundle, set.id)
		}
		file, err := copyPlatformBundleFile(dir, "checkpoints/"+set.checkpoint+".archive", source)
		if err != nil {
			return manifest, err
		}
		if file.SHA256 != set.manifest || file.Bytes != set.bytes {
			return manifest, fmt.Errorf("%w: the archive for recovery set %s is not the checkpoint the database recorded", errPlatformBundle, set.id)
		}
		manifest.Checkpoints = append(manifest.Checkpoints, platformBundleCheckpoint{
			RecoverySetID: set.id, InstallationID: set.installation, CheckpointID: set.checkpoint, NamespaceID: set.namespace,
			ReleaseSHA256: set.release, Archive: file,
		})
	}
	if in.RunnerJournal != "" {
		for _, sidecar := range []string{"-wal", "-shm"} {
			if info, statErr := os.Lstat(in.RunnerJournal + sidecar); statErr == nil && info.Size() > 0 {
				return manifest, fmt.Errorf("%w: the runner journal has live %s content; stop the runner first", errPlatformBundle, sidecar)
			}
		}
		file, err := copyPlatformBundleFile(dir, platformBundleJournalName, in.RunnerJournal)
		if err != nil {
			return manifest, err
		}
		manifest.RunnerJournal = &file
	}
	if manifest.Releases, err = platformRequiredReleaseBlobs(pool); err != nil {
		return manifest, err
	}
	if manifest.ImageBlobs, err = s.bundlePlatformImageBlobs(dir, manifest.Releases); err != nil {
		return manifest, err
	}
	after, err := loadPlatformRetainedSets(pool)
	if err != nil || !samePlatformRetainedSets(before, after) {
		return manifest, fmt.Errorf("%w: the retained recovery sets changed while the bundle was written", errPlatformBundle)
	}
	encoded, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return manifest, err
	}
	if _, err = writePlatformBundleFile(dir, platformBundleManifestName, bytes.NewReader(append(encoded, '\n'))); err != nil {
		return manifest, err
	}
	return manifest, nil
}

func samePlatformRetainedSets(a, b []platformRetainedSet) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// verifyPlatformRecoveryBundle checks a bundle on the receiving host: the manifest is the one this code
// writes, and every file it lists is present with the recorded size and digest. It reads files as bytes;
// the runner journal in particular is hashed and never opened as a database.
func verifyPlatformRecoveryBundle(dir string) (platformRecoveryBundleManifest, error) {
	var manifest platformRecoveryBundleManifest
	raw, err := os.ReadFile(filepath.Join(dir, platformBundleManifestName))
	if err != nil {
		return manifest, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil || manifest.Format != platformRecoveryBundleFormat {
		return manifest, fmt.Errorf("%w: unreadable manifest", errPlatformBundle)
	}
	files := []platformBundleFile{manifest.Dump, manifest.Keyring}
	for _, checkpoint := range manifest.Checkpoints {
		files = append(files, checkpoint.Archive)
	}
	if manifest.RunnerJournal != nil {
		files = append(files, *manifest.RunnerJournal)
	}
	carried := map[string]bool{}
	for _, blob := range manifest.ImageBlobs {
		// The name of a blob is its digest, so a file cannot be listed under another content's name.
		if blob.Path != "app-blobs/"+blob.SHA256 || !platformValidSHA(blob.SHA256) {
			return manifest, fmt.Errorf("%w: %q is not an image blob entry", errPlatformBundle, blob.Path)
		}
		carried[blob.SHA256] = true
		files = append(files, blob)
	}
	for _, release := range manifest.Releases {
		if release.Backend == platformBlobPending {
			return manifest, fmt.Errorf("%w: release %s names backend content nothing carries", errPlatformBundle, release.ReleaseSHA256)
		}
		for _, digest := range release.ImageBlobs {
			if !carried[digest] {
				return manifest, fmt.Errorf("%w: release %s needs blob %s the bundle does not list", errPlatformBundle, release.ReleaseSHA256, digest)
			}
		}
	}
	for _, file := range files {
		clean := filepath.Clean(filepath.FromSlash(file.Path))
		if file.Path == "" || filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
			return manifest, fmt.Errorf("%w: %q is not a path inside the bundle", errPlatformBundle, file.Path)
		}
		in, err := os.Open(filepath.Join(dir, clean))
		if err != nil {
			return manifest, err
		}
		hash := sha256.New()
		n, copyErr := io.Copy(hash, in)
		in.Close()
		if copyErr != nil || n != file.Bytes || hex.EncodeToString(hash.Sum(nil)) != file.SHA256 {
			return manifest, fmt.Errorf("%w: %s does not match its manifest entry", errPlatformBundle, file.Path)
		}
	}
	return manifest, nil
}

// loadPlatformReleasePins is the one computation of which releases must keep their content. The recovery bundle
// and the image blob sweep both use it, so the sweep can never remove what the bundle would need. It returns each
// release with the reasons that pin it:
//
//   - active: the last activation of every installation that still exists (the bundle's rule);
//   - pinned: a retained recovery set restores it (the bundle's rule);
//   - candidate: it has a candidate or active activation, at any generation;
//   - recovery_set: a recovery set that is not deleted names it, retained or still being cut;
//   - operation: a non-terminal operation moves from or to it;
//   - plan: an open review plan names it.
//
// The sweep uses every reason, the bundle only the first two. nowMS bounds which plans are open; zero ignores
// plans, which the bundle does not need.
func loadPlatformReleasePins(e migExec, nowMS int64) (map[string]map[string]bool, error) {
	pins := map[string]map[string]bool{}
	collect := func(reason, query string, args ...any) error {
		rows, err := e.query(query, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var release string
			if err := rows.Scan(&release); err != nil {
				return err
			}
			if pins[release] == nil {
				pins[release] = map[string]bool{}
			}
			pins[release][reason] = true
		}
		return rows.Err()
	}
	if err := collect("active", `SELECT a.release_sha256 FROM app_platform_activations a
		JOIN app_platform_installations i ON i.id=a.installation_id
		WHERE i.desired_state<>'removed'
		AND a.generation=(SELECT MAX(b.generation) FROM app_platform_activations b WHERE b.installation_id=a.installation_id)`); err != nil {
		return nil, err
	}
	if err := collect("pinned", `SELECT release_sha256 FROM app_platform_recovery_sets WHERE `+platformRetainedRecoverySets); err != nil {
		return nil, err
	}
	if nowMS == 0 {
		return pins, nil
	}
	steps := []struct{ reason, query string }{
		{"candidate", `SELECT release_sha256 FROM app_platform_activations WHERE state IN ('candidate','active')`},
		{"recovery_set", `SELECT release_sha256 FROM app_platform_recovery_sets WHERE state<>'deleted'`},
		{"operation", `SELECT p.source_release_sha256 FROM app_platform_operations o JOIN app_platform_review_plans p ON p.id=o.review_plan_id WHERE o.completed_ms IS NULL AND p.source_release_sha256 IS NOT NULL`},
		{"operation", `SELECT p.target_release_sha256 FROM app_platform_operations o JOIN app_platform_review_plans p ON p.id=o.review_plan_id WHERE o.completed_ms IS NULL AND p.target_release_sha256 IS NOT NULL`},
	}
	for _, step := range steps {
		if err := collect(step.reason, step.query); err != nil {
			return nil, err
		}
	}
	for _, column := range []string{"source_release_sha256", "target_release_sha256"} {
		if err := collect("plan", `SELECT `+column+` FROM app_platform_review_plans WHERE consumed_ms IS NULL AND expires_ms>? AND `+column+` IS NOT NULL`, nowMS); err != nil {
			return nil, err
		}
	}
	return pins, nil
}
