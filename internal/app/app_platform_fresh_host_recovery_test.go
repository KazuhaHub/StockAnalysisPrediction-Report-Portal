package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/journal"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/config"
)

type platformRecoveryBundleMetadata struct {
	InstallationID        string `json:"installation_id"`
	RecoverySetID         string `json:"recovery_set_id"`
	CheckpointID          string `json:"checkpoint_id"`
	DataNamespaceID       string `json:"data_namespace_id"`
	ReleaseSHA256         string `json:"release_sha256"`
	DataManifestSHA256    string `json:"data_manifest_sha256"`
	CredentialID          string `json:"credential_id"`
	CredentialValue       int64  `json:"credential_value_revision"`
	SourceTaskID          string `json:"source_task_id"`
	EventSubscriptionID   string `json:"event_subscription_id"`
	ExpectedSecret        string `json:"expected_secret"`
	ExpectedDataSHA256    string `json:"expected_data_sha256"`
	SourceControllerEpoch string `json:"source_controller_epoch"`
	JournalSHA256         string `json:"journal_sha256"`
	JournalSourceDir      string `json:"journal_source_dir"`
}

func TestFreshHostPortalRecoveryBundleRestore(t *testing.T) {
	if os.Getenv("PORTAL_RECOVERY_CHILD") == "1" {
		t.Skip("parent recovery fixture only")
	}
	bundle := t.TempDir()
	s, _, installation := platformLaunchFixture(t, false)
	now, err := platformDatabaseNow(s.st.poolExec())
	if err != nil {
		t.Fatal(err)
	}
	var appID, publisherID, releaseSHA, epoch string
	var generation, policyRevision int64
	if err := s.st.queryRow(`SELECT app_id,publisher_id,generation,
		policy_revision,controller_epoch FROM app_platform_installations
		WHERE id=?`, installation).Scan(
		&appID, &publisherID, &generation, &policyRevision, &epoch,
	); err != nil {
		t.Fatal(err)
	}
	if err := s.st.queryRow(`SELECT release_sha256
		FROM app_platform_activations
		WHERE installation_id=? AND state='active'`,
		installation).Scan(&releaseSHA); err != nil {
		t.Fatal(err)
	}
	namespace := scalar[string](t, s.st, `SELECT data_namespace_id
		FROM app_platform_activations
		WHERE installation_id=? AND state='active'`, installation)
	operation := scalar[string](t, s.st, `SELECT id
		FROM app_platform_operations
		WHERE installation_id=? AND action='install' AND stage='complete'`,
		installation)
	if !platformValidUUID(namespace) || !platformValidUUID(operation) {
		t.Fatal("incomplete launch fixture")
	}
	data := []byte(`{"fixture":"fresh-host-recovery","value":7}`)
	dataSHA := platformDigest(data)
	// The old host: the checkpoint archive on its checkpoint root, and the runner's journal.
	oldHost := t.TempDir()
	archivePath := filepath.Join(oldHost, "checkpoint.archive")
	if err := os.WriteFile(archivePath, data, 0600); err != nil {
		t.Fatal(err)
	}
	journalDir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(journalDir, 0700); err != nil {
		t.Fatal(err)
	}
	oldJournal, err := journal.Open(journalDir)
	if errors.Is(err, journal.ErrUnsupported) {
		t.Skip("the runner journal is not supported on this platform")
	}
	if err != nil {
		t.Fatal(err)
	}
	if err := oldJournal.Close(); err != nil {
		t.Fatal(err)
	}
	journalPath := filepath.Join(journalDir, "journal.sqlite3")
	journalBytes, err := os.ReadFile(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	credentialID, checkpointID := platformFixtureID(7001), platformFixtureID(7002)
	taskID, recoveryID := platformFixtureID(7003), platformFixtureID(7004)
	eventSubscriptionID := platformFixtureID(7005)
	secret := "fresh-host-fixture-secret"
	sealed, err := s.sealPlatformCredentialValue(
		credentialID, 1, []byte(secret))
	if err != nil {
		t.Fatal(err)
	}
	platformTestInsert(t, s.st.poolExec(), "credentials", map[string]any{
		"id": credentialID, "label": "Fresh host fixture",
		"state": "active", "sealed_value": sealed,
		"value_revision": 1, "revision": 1,
		"created_ms": now, "updated_ms": now, "revoked_ms": nil,
	})
	taskPayload := []byte(`{"fixture":"restore-pauses-task"}`)
	platformTestInsert(t, s.st.poolExec(), "tasks", map[string]any{
		"id": taskID, "installation_id": installation,
		"app_id": appID, "publisher_id": publisherID,
		"occurrence_id": "fresh-host-recovery",
		"handler":       appplatform.TaskHandlerBackend,
		"payload":       taskPayload, "payload_sha256": platformDigest(taskPayload),
		"state": "waiting", "generation": generation,
		"policy_revision": policyRevision, "release_sha256": releaseSHA,
		"controller_epoch": epoch, "attempts": 0, "max_attempts": 3,
		"fence": 1, "revision": 1,
		"worker_id": nil, "lease_id": nil, "lease_until_ms": nil,
		"next_attempt_ms": now + 1000, "expires_ms": now + 3600000,
		"error_code": nil, "result_json": nil, "completed_ms": nil,
		"created_ms": now, "updated_ms": now,
	})
	platformTestInsert(t, s.st.poolExec(), "event_subscriptions", map[string]any{
		"id": eventSubscriptionID, "installation_id": installation,
		"app_id": appID, "publisher_id": publisherID,
		"event_type":    appplatform.EventReportIngested,
		"resource_type": "report", "resource_id": "42",
		"state": "active", "generation": generation,
		"policy_revision": policyRevision,
		"release_sha256":  releaseSHA, "controller_epoch": epoch,
		"max_inflight": 1, "revision": 1,
		"created_ms": now, "updated_ms": now,
	})
	platformTestInsert(t, s.st.poolExec(), "recovery_sets", map[string]any{
		"id": recoveryID, "installation_id": installation,
		"data_namespace_id": namespace, "operation_id": operation,
		"release_sha256": releaseSHA, "data_format": 1,
		"checkpoint_id":   checkpointID,
		"manifest_sha256": dataSHA,
		"bytes":           len(data), "state": "verified",
		"controller_epoch": epoch, "writer_fence": 1,
		"verified_ms": now, "deleted_ms": nil,
		"created_ms": now, "updated_ms": now,
	})
	manifest, err := s.writePlatformRecoveryBundle(bundle, platformRecoveryBundleInput{
		Archives:      map[string]string{recoveryID: archivePath},
		RunnerJournal: journalPath,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.Checkpoints) != 1 || manifest.Checkpoints[0].Archive.SHA256 != dataSHA || manifest.RunnerJournal == nil {
		t.Fatalf("bundle manifest: %+v", manifest)
	}
	metadata := platformRecoveryBundleMetadata{
		InstallationID: installation, RecoverySetID: recoveryID,
		CheckpointID: checkpointID, DataNamespaceID: namespace,
		ReleaseSHA256: releaseSHA, DataManifestSHA256: dataSHA,
		CredentialID: credentialID, CredentialValue: 1,
		SourceTaskID: taskID, EventSubscriptionID: eventSubscriptionID,
		ExpectedSecret:        secret,
		ExpectedDataSHA256:    platformDigest(data),
		SourceControllerEpoch: epoch,
		JournalSHA256:         platformDigest(journalBytes),
		JournalSourceDir:      journalDir,
	}
	encoded, err := json.Marshal(metadata)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(bundle, "metadata.json"), encoded, 0600,
	); err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	child := exec.CommandContext(
		ctx, executable,
		"-test.run=^TestFreshHostPortalRecoveryBundleChild$",
		"-test.v",
	)
	child.Env = append(os.Environ(),
		"PORTAL_RECOVERY_CHILD=1",
		"PORTAL_RECOVERY_BUNDLE="+bundle,
	)
	output, err := child.CombinedOutput()
	if err != nil {
		t.Fatalf("fresh recovery child failed: %v\n%s",
			err, output)
	}
	for _, marker := range []string{
		"FRESH_RECOVERY_BUNDLE_VERIFIED",
		"FRESH_RECOVERY_DB_RESTORED",
		"FRESH_RECOVERY_PAUSED",
		"FRESH_RECOVERY_SECRET_DECRYPTED",
		"FRESH_RECOVERY_DATA_MATCH",
		"FRESH_RECOVERY_RELEASES_MATCH",
		"FRESH_RECOVERY_JOURNAL_UNOPENED",
		"FRESH_RECOVERY_HEALTH_AVAILABLE",
	} {
		if !bytes.Contains(output, []byte(marker)) {
			t.Fatalf("fresh recovery marker missing: %s\n%s",
				marker, output)
		}
	}
}

func TestFreshHostPortalRecoveryBundleChild(t *testing.T) {
	if os.Getenv("PORTAL_RECOVERY_CHILD") != "1" {
		t.Skip("fresh recovery child only")
	}
	bundle := os.Getenv("PORTAL_RECOVERY_BUNDLE")
	if !filepath.IsAbs(bundle) {
		t.Fatal("missing recovery bundle")
	}
	var metadata platformRecoveryBundleMetadata
	raw, err := os.ReadFile(filepath.Join(bundle, "metadata.json"))
	if err != nil || json.Unmarshal(raw, &metadata) != nil {
		t.Fatal("invalid recovery metadata", err)
	}
	manifest, err := verifyPlatformRecoveryBundle(bundle)
	if err != nil {
		t.Fatal("bundle does not verify:", err)
	}
	if len(manifest.Checkpoints) != 1 || manifest.Checkpoints[0].RecoverySetID != metadata.RecoverySetID ||
		manifest.Checkpoints[0].Archive.SHA256 != metadata.ExpectedDataSHA256 || manifest.RunnerJournal == nil ||
		manifest.RunnerJournal.SHA256 != metadata.JournalSHA256 {
		t.Fatalf("bundle manifest lists the wrong evidence: %+v", manifest)
	}
	data, err := os.ReadFile(filepath.Join(bundle, filepath.FromSlash(manifest.Checkpoints[0].Archive.Path)))
	if err != nil || platformDigest(data) != metadata.ExpectedDataSHA256 {
		t.Fatal("copied data identity changed", err)
	}
	t.Log("FRESH_RECOVERY_BUNDLE_VERIFIED")
	dump, err := os.ReadFile(filepath.Join(bundle, filepath.FromSlash(manifest.Dump.Path)))
	if err != nil {
		t.Fatal(err)
	}
	store, err := OpenStore(
		"sqlite", filepath.Join(bundle, "restored.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.restoreFrom(bytes.NewReader(dump), true); err != nil {
		t.Fatal(err)
	}
	t.Log("FRESH_RECOVERY_DB_RESTORED")
	server := &Server{
		st: store, cfg: &config.Config{SecretKey: rotOldKey},
	}
	for setting, expected := range map[string]string{
		"app_platform_restore_paused":  "1",
		"notification_dispatch_paused": "1",
	} {
		var value string
		if err := store.queryRow(
			`SELECT v FROM meta WHERE k=?`, setting,
		).Scan(&value); err != nil || value != expected {
			t.Fatalf("restore setting %s = %q, %v",
				setting, value, err)
		}
	}
	if scalar[int](t, store, `SELECT COUNT(*) FROM app_platform_runtime_bindings
		WHERE enabled<>0 OR sealed_control_key IS NOT NULL`) != 0 {
		t.Fatal("restored runtime retained execution authority")
	}
	if scalar[string](t, store, `SELECT state
		FROM app_platform_tasks WHERE id=?`,
		metadata.SourceTaskID) != "review_required" {
		t.Fatal("restored pending task became executable")
	}
	if scalar[string](t, store, `SELECT state
		FROM app_platform_event_subscriptions WHERE id=?`,
		metadata.EventSubscriptionID) != "review_required" {
		t.Fatal("restored event subscription became executable")
	}
	if scalar[string](t, store, `SELECT state
		FROM app_platform_recovery_sets WHERE id=?`,
		metadata.RecoverySetID) != "review_required" {
		t.Fatal("restored recovery set was auto-approved")
	}
	if scalar[string](t, store, `SELECT phase
		FROM app_platform_installations WHERE id=?`,
		metadata.InstallationID) != "review_required" {
		t.Fatal("restored installation was auto-enabled")
	}
	t.Log("FRESH_RECOVERY_PAUSED")
	var sealed string
	var valueRevision int64
	if err := store.queryRow(`SELECT sealed_value,value_revision
		FROM app_platform_credentials WHERE id=?`,
		metadata.CredentialID).Scan(&sealed, &valueRevision); err != nil ||
		valueRevision != metadata.CredentialValue {
		t.Fatal("restored credential metadata", err)
	}
	plain, err := server.openPlatformCredentialValue(
		metadata.CredentialID, valueRevision, sealed)
	if err != nil || string(plain) != metadata.ExpectedSecret {
		clear(plain)
		t.Fatal("fixture secret did not decrypt", err)
	}
	clear(plain)
	t.Log("FRESH_RECOVERY_SECRET_DECRYPTED")
	var recoveryData, checkpoint, namespace, release string
	var restoredBytes int64
	if err := store.queryRow(`SELECT manifest_sha256,checkpoint_id,
		data_namespace_id,release_sha256,bytes
		FROM app_platform_recovery_sets WHERE id=?`,
		metadata.RecoverySetID).Scan(
		&recoveryData, &checkpoint, &namespace, &release,
		&restoredBytes,
	); err != nil || recoveryData != metadata.DataManifestSHA256 ||
		checkpoint != metadata.CheckpointID ||
		namespace != metadata.DataNamespaceID ||
		release != metadata.ReleaseSHA256 ||
		restoredBytes != int64(len(data)) {
		t.Fatal("recovery identity changed", err)
	}
	t.Log("FRESH_RECOVERY_DATA_MATCH")
	// The release inventory was computed on the old Portal; the restored one derives the same set, and
	// every static frontend it names came with the dump. Managed backend content is the open item.
	restored, err := platformRequiredReleaseBlobs(store.poolExec())
	if err != nil || !reflect.DeepEqual(restored, manifest.Releases) {
		t.Fatalf("release inventory differs after restore: %v\n%+v\n%+v", err, restored, manifest.Releases)
	}
	var carried bool
	for _, blob := range restored {
		if blob.ReleaseSHA256 == metadata.ReleaseSHA256 && blob.Frontend == platformBlobInDump && blob.Backend == platformBlobNone &&
			reflect.DeepEqual(blob.Reasons, []string{"active", "pinned"}) {
			carried = true
		}
	}
	if !carried || len(platformPendingReleaseBlobs(restored)) != 0 {
		t.Fatalf("the active release is not accounted for: %+v", restored)
	}
	t.Log("FRESH_RECOVERY_RELEASES_MATCH")
	// The keyring in the bundle is the one the restored database holds.
	keyringRaw, err := os.ReadFile(filepath.Join(bundle, filepath.FromSlash(manifest.Keyring.Path)))
	if err != nil {
		t.Fatal(err)
	}
	liveKeyring, err := platformBundleKeyring(store.poolExec())
	if err != nil || !bytes.Equal(liveKeyring, keyringRaw) {
		t.Fatalf("bundle keyring differs from the restored keyring: %v", err)
	}
	// The old runner's journal travelled as bytes: still identical, and nothing opened it (an opened
	// journal grows write-ahead files beside it, in the bundle or at the old host).
	copied, err := os.ReadFile(filepath.Join(bundle, filepath.FromSlash(manifest.RunnerJournal.Path)))
	if err != nil || platformDigest(copied) != metadata.JournalSHA256 {
		t.Fatalf("bundled journal is not the original: %v", err)
	}
	for _, dir := range []string{bundle, metadata.JournalSourceDir} {
		_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
			if err == nil && !d.IsDir() && (strings.HasSuffix(path, "-wal") || strings.HasSuffix(path, "-shm")) {
				t.Fatalf("a journal sidecar appeared: %s", path)
			}
			return nil
		})
	}
	t.Log("FRESH_RECOVERY_JOURNAL_UNOPENED")
	recorder := httptest.NewRecorder()
	server.handleHealthz(
		recorder,
		httptest.NewRequest(http.MethodGet, "/healthz", nil),
	)
	if recorder.Code != http.StatusOK {
		t.Fatalf("restored health = %d %s",
			recorder.Code, recorder.Body.String())
	}
	t.Log("FRESH_RECOVERY_HEALTH_AVAILABLE")
}
