package app

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/journal"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/transport"
)

type platformDataImportTestObservation struct {
	receipt    journal.ImportDataReceipt
	status     *journal.MigrateDataStatus
	authority  control.Authority
	endpoint   transport.CandidateReceiptEndpoint
	receivedAt time.Time
	commandSHA string
}

func (o platformDataImportTestObservation) ImportDataReceipt() (journal.ImportDataReceipt, error) {
	return o.receipt, nil
}
func (o platformDataImportTestObservation) DataEffectStatus() (journal.MigrateDataStatus, error) {
	if o.status == nil {
		return journal.MigrateDataStatus{}, errors.New("no status")
	}
	return *o.status, nil
}
func (o platformDataImportTestObservation) Controller() control.Authority { return o.authority }
func (o platformDataImportTestObservation) Endpoint() transport.CandidateReceiptEndpoint {
	return o.endpoint
}
func (o platformDataImportTestObservation) ReceivedAt() time.Time { return o.receivedAt }
func (o platformDataImportTestObservation) CommandSHA256() string { return o.commandSHA }

func platformDataImportObservation(intent platformImportDataIntent, endpoint platformRuntimeEndpoint) platformDataImportTestObservation {
	now := strconv.FormatInt(time.Now().UTC().UnixMilli(), 10)
	c, p := intent.command, intent.command.Payload
	return platformDataImportTestObservation{
		receipt: journal.ImportDataReceipt{
			Format: "portal.runner.import-data-receipt/1-dev.1", DeploymentID: c.DeploymentID, ControllerEpoch: c.ControllerEpoch,
			InstallationID: c.InstallationID, OperationID: c.OperationID, TargetGeneration: c.TargetGeneration, Stage: c.Stage,
			EffectID: c.EffectID, PayloadSHA256: c.PayloadSHA256, NamespaceID: p.NamespaceID, ReleaseSHA256: p.ReleaseSHA256,
			DataFormat: p.DataFormat, WriterFence: p.WriterFence, ProjectID: p.ProjectID, NamespaceInode: p.NamespaceInode, Device: p.Device,
			QuotaBytes: p.QuotaBytes, QuotaInodes: p.QuotaInodes, ArchiveSHA256: p.ArchiveSHA256, ArchiveBytes: p.ArchiveBytes,
			ArchiveEntries: p.ArchiveEntries, ContentBytes: p.ContentBytes, SourceCheckpointOperationID: p.SourceCheckpointOperationID,
			SourceCheckpointEffectID: p.SourceCheckpointEffectID, SourceCheckpointReceiptSHA: p.SourceCheckpointReceiptSHA,
			Entries: p.ArchiveEntries, UsedBytes: "786432", UsedInodes: "13", State: "imported", ObservedMS: now, SettledMS: now,
		},
		authority:  intent.authority,
		endpoint:   transport.CandidateReceiptEndpoint{Path: endpoint.path, RunnerUID: 0, ControllerUID: endpoint.controllerUID, ControllerGID: endpoint.controllerGID, RootController: true},
		receivedAt: time.Now().UTC(), commandSHA: platformDigest(intent.raw),
	}
}

var platformTestArchiveSHA = strings.Repeat("c", 64)

func platformTestArchive(format int64) *journal.DataQueryReport {
	return &journal.DataQueryReport{ArchiveBytes: "786432", ArchiveEntries: "12", ContentBytes: "700000", SchemaMarker: strconv.FormatInt(format, 10)}
}

// platformImportDisabledFixture is a disabled installation whose runtime target is known, with an inbox archive.
func platformImportDisabledFixture(t *testing.T, pg bool) (*Server, platformActorSession, platformOperationLease, *platformManagedInstallTarget, platformRuntimeRegistration, *platformFakeDataRunner) {
	t.Helper()
	s, actor, enabled, target, runtime := platformDataActionFixture(t, pg)
	platformCompleteSyntheticDisable(t, s, actor, enabled)
	format := scalar[int64](t, s.st, `SELECT data_format FROM app_platform_data_namespaces WHERE owner_installation_id=?`, enabled.installationID)
	runner := &platformFakeDataRunner{fits: true, archive: platformTestArchive(format)}
	runner.install(t)
	return s, actor, enabled, target, runtime, runner
}

func testPlatformImportDataReplacesContentAndCompletes(t *testing.T, pg bool) {
	s, actor, enabled, target, runtime, runner := platformImportDisabledFixture(t, pg)
	installation := enabled.installationID
	endpoint := platformCheckpointEndpointFixture()
	review, err := s.createPlatformManagedImportDataReviewWithRuntimeContext(context.Background(), actor, installation, platformTestArchiveSHA, target, runtime, endpoint)
	if err != nil {
		t.Fatal("import review", err)
	}
	var plan platformManagedUpdatePlan
	if json.Unmarshal([]byte(review.JSON), &plan) != nil || plan.Format != platformManagedImportDataFormat || plan.Action != appplatform.ActionImportData ||
		plan.Import == nil || plan.Import.ArchiveSHA256 != platformTestArchiveSHA || plan.Import.Entries != 12 || plan.Import.DataFormat != plan.DataFormat ||
		plan.SourceReleaseSHA256 != plan.TargetReleaseSHA256 {
		t.Fatal("import review plan", plan)
	}
	commit, err := s.consumePlatformManagedSameReleaseReviewWithRuntimeContext(context.Background(), actor, review.ID, review.SHA256, platformFixtureID(1440), target, runtime, appplatform.ActionImportData)
	if err != nil || commit.Stage != appplatform.StageQueued {
		t.Fatal("import admission", commit, err)
	}
	selected, err := s.claimNextPlatformManagedSameRelease(context.Background(), platformFixtureID(1441), appplatform.ActionImportData)
	if err != nil || selected.lease.id != commit.OperationID {
		t.Fatal("import claim", selected, err)
	}
	// queued -> validate (the runner is asked again) -> quiesce -> snapshot
	current, err := s.advancePlatformManagedEnableStage(context.Background(), selected.lease)
	if err != nil || current.snapshot.Stage != appplatform.StageValidate {
		t.Fatal("import validate", current.snapshot, err)
	}
	runner.archive.ArchiveEntries = "13"
	if _, err := s.validatePlatformImportData(context.Background(), current, endpoint); !errors.Is(err, errPlatformDataImportArchiveInvalid) {
		t.Fatalf("a changed archive was validated: %v", err)
	}
	runner.archive.ArchiveEntries = "12"
	runner.archive.SchemaMarker = "99"
	if _, err := s.validatePlatformImportData(context.Background(), current, endpoint); !errors.Is(err, errPlatformDataFormatMismatch) {
		t.Fatalf("a data format mismatch was validated: %v", err)
	}
	runner.archive.SchemaMarker = strconv.FormatInt(plan.DataFormat, 10)
	saved := runner.archive
	runner.archive = nil
	if _, err := s.validatePlatformImportData(context.Background(), current, endpoint); !errors.Is(err, errPlatformDataImportArchiveMissing) {
		t.Fatalf("a missing archive was validated: %v", err)
	}
	// The scheduler's stage loop asks the runner at validate too: it must not move past a missing archive.
	if after, err := s.runPlatformManagedSameRelease(context.Background(), current, endpoint); !errors.Is(err, errPlatformDataImportArchiveMissing) ||
		after.snapshot.Stage != appplatform.StageValidate {
		t.Fatalf("the stage loop moved an import past validate without the runner's check: %v %v", after.snapshot.Stage, err)
	}
	runner.archive = saved
	if current, err = s.validatePlatformImportData(context.Background(), current, endpoint); err != nil || current.snapshot.Stage != appplatform.StageQuiesce {
		t.Fatal("import validated", current.snapshot, err)
	}
	if current, err = s.advancePlatformManagedEnableStage(context.Background(), current); err != nil || current.snapshot.Stage != appplatform.StageSnapshot {
		t.Fatal("import quiesce", current.snapshot, err)
	}
	checkpoint, err := s.preparePlatformCheckpoint(context.Background(), current)
	if err != nil {
		t.Fatal("import checkpoint", err)
	}
	importLease, err := s.commitPlatformCheckpoint(context.Background(), current, checkpoint, endpoint, platformCheckpointObservationFixture(checkpoint, endpoint))
	if err != nil || importLease.snapshot.Stage != appplatform.StageImport {
		t.Fatal("the checkpoint is not followed by the import", importLease.snapshot, err)
	}
	intent, err := s.preparePlatformImportData(context.Background(), importLease)
	if err != nil {
		t.Fatal("import intent", err)
	}
	if intent.command.Payload.ArchiveSHA256 != platformTestArchiveSHA || intent.command.Payload.SourceCheckpointEffectID != checkpoint.effectID {
		t.Fatal("the import is not bound to the reviewed archive and the pre-import checkpoint")
	}
	if scalar[string](t, s.st, `SELECT stage||':'||kind FROM app_platform_operation_effects WHERE effect_id=?`, intent.effectID) != "import:import-data" {
		t.Fatal("the import effect has the wrong durable stage")
	}
	startLease, err := s.commitPlatformImportData(context.Background(), importLease, intent, endpoint, platformDataImportObservation(intent, endpoint))
	if err != nil || startLease.snapshot.Stage != appplatform.StageStart {
		t.Fatal("import commit", startLease.snapshot, err)
	}
	// The import is not complete yet: its archive stays.
	s.removePlatformImportArchive(context.Background(), startLease, endpoint)
	if len(runner.removed) != 0 {
		t.Fatalf("the inbox archive was deleted before the import completed: %v", runner.removed)
	}
	namespace := plan.DataNamespaceID
	if scalar[int64](t, s.st, `SELECT observed_bytes FROM app_platform_data_namespaces WHERE id=?`, namespace) != 786432 {
		t.Fatal("observed_bytes is not what the project quota reported")
	}
	if scalar[int](t, s.st, `SELECT COUNT(*) FROM audit_log WHERE action='app_platform.data_imported' AND target_id=?`, startLease.id) != 1 ||
		scalar[int](t, s.st, `SELECT COUNT(*) FROM audit_log WHERE action='app_platform.data_import_review'`) != 1 ||
		scalar[int](t, s.st, `SELECT COUNT(*) FROM audit_log WHERE action='app_platform.data_import_intent' AND target_id=?`, startLease.id) != 1 {
		t.Fatal("the import is not audited at every stage")
	}
	start, err := s.preparePlatformUpdateStart(context.Background(), startLease)
	if err != nil || start.command.Kind != "enable-start" {
		t.Fatal("import start intent", start.command.Kind, err)
	}
	result := platformRunSyntheticManagedEnable(t, s, startLease)
	if result.snapshot.Stage != appplatform.StageComplete ||
		scalar[string](t, s.st, `SELECT desired_state||':'||phase FROM app_platform_installations WHERE id=?`, installation) != "enabled:ready" ||
		scalar[int64](t, s.st, `SELECT generation FROM app_platform_installations WHERE id=?`, installation) != plan.TargetGeneration {
		t.Fatal("the import did not activate the retained tuple")
	}
	// A runner that cannot delete the archive does not undo the completed import: the failure is audited and the
	// archive stays.
	healthy := platformDataQueryRunner
	platformDataQueryRunner = func(context.Context, platformRuntimeEndpoint, platformDataQueryRequest) (platformDataQueryObservation, error) {
		return nil, errors.New("runner unavailable")
	}
	s.removePlatformImportArchive(context.Background(), startLease, endpoint)
	platformDataQueryRunner = healthy
	if len(runner.removed) != 0 ||
		scalar[int](t, s.st, `SELECT COUNT(*) FROM audit_log WHERE action='app_platform.data_import_archive_remove_failed' AND target_id=?`, startLease.id) != 1 ||
		scalar[int](t, s.st, `SELECT COUNT(*) FROM audit_log WHERE action='app_platform.data_import_archive_removed'`) != 0 ||
		scalar[string](t, s.st, `SELECT stage FROM app_platform_operations WHERE id=?`, startLease.id) != string(appplatform.StageComplete) {
		t.Fatal("a failed archive removal was not recorded or undid the import")
	}
	w := platformSchedulerFixture(t, s)
	w.afterOperationComplete(context.Background(), startLease)
	if len(runner.removed) != 1 || runner.removed[0] != platformTestArchiveSHA ||
		scalar[int](t, s.st, `SELECT COUNT(*) FROM audit_log WHERE action='app_platform.data_import_archive_removed' AND target_id=?`, startLease.id) != 1 {
		t.Fatalf("the inbox archive was not deleted on completion: %v", runner.removed)
	}
	access, err := s.preparePlatformWritableBackendAccess(context.Background(), actor, installation)
	if err != nil {
		t.Fatal("backend history after an import", err)
	}
	access.Close()
}

func TestPlatformImportDataReplacesContentAndCompletesSQLite(t *testing.T) {
	testPlatformImportDataReplacesContentAndCompletes(t, false)
}

func TestPlatformImportDataReplacesContentAndCompletesPostgres(t *testing.T) {
	testPlatformImportDataReplacesContentAndCompletes(t, true)
}

func testPlatformImportDataRefusals(t *testing.T, pg bool) {
	s, actor, enabled, target, runtime := platformDataActionFixture(t, pg)
	installation := enabled.installationID
	endpoint := platformCheckpointEndpointFixture()
	format := scalar[int64](t, s.st, `SELECT data_format FROM app_platform_data_namespaces WHERE owner_installation_id=?`, installation)
	runner := &platformFakeDataRunner{fits: true, archive: platformTestArchive(format)}
	runner.install(t)
	review := func() error {
		_, err := s.createPlatformManagedImportDataReviewWithRuntimeContext(context.Background(), actor, installation, platformTestArchiveSHA, target, runtime, endpoint)
		return err
	}
	// A running installation is refused: the import replaces the namespace while the backend is stopped.
	if err := review(); !errors.Is(err, errPlatformDataImportRequiresDisabled) {
		t.Fatalf("an enabled installation was reviewed: %v", err)
	}
	platformCompleteSyntheticDisable(t, s, actor, enabled)
	if _, err := s.createPlatformManagedImportDataReviewWithRuntimeContext(context.Background(), actor, installation, "short", target, runtime, endpoint); !errors.Is(err, errPlatformManagementInput) {
		t.Fatalf("a malformed digest was reviewed: %v", err)
	}
	runner.archive = nil
	if err := review(); !errors.Is(err, errPlatformDataImportArchiveMissing) {
		t.Fatalf("an unknown digest was reviewed: %v", err)
	}
	runner.archive = platformTestArchive(format + 1)
	if err := review(); !errors.Is(err, errPlatformDataFormatMismatch) {
		t.Fatalf("a data format mismatch was reviewed: %v", err)
	}
	runner.archive = platformTestArchive(format)
	quota := scalar[int64](t, s.st, `SELECT quota_bytes FROM app_platform_data_namespaces WHERE owner_installation_id=?`, installation)
	runner.archive.ContentBytes = strconv.FormatInt(quota+1, 10)
	if err := review(); !errors.Is(err, errPlatformDataImportTooLarge) {
		t.Fatalf("an archive larger than the quota was reviewed: %v", err)
	}
	runner.archive = platformTestArchive(format)
	inodes := scalar[int64](t, s.st, `SELECT quota_inodes FROM app_platform_data_namespaces WHERE owner_installation_id=?`, installation)
	runner.archive.ArchiveEntries = strconv.FormatInt(inodes, 10)
	if err := review(); !errors.Is(err, errPlatformDataImportTooLarge) {
		t.Fatalf("an archive with too many entries was reviewed: %v", err)
	}
	runner.archive = platformTestArchive(format)
	if err := review(); err != nil {
		t.Fatalf("a valid archive was refused: %v", err)
	}
}

func TestPlatformImportDataRefusalsSQLite(t *testing.T)   { testPlatformImportDataRefusals(t, false) }
func TestPlatformImportDataRefusalsPostgres(t *testing.T) { testPlatformImportDataRefusals(t, true) }

func testPlatformImportDataFailureAfterImportRestoresTheCheckpoint(t *testing.T, pg bool) {
	s, actor, enabled, target, runtime, _ := platformImportDisabledFixture(t, pg)
	installation := enabled.installationID
	endpoint := platformCheckpointEndpointFixture()
	review, err := s.createPlatformManagedImportDataReviewWithRuntimeContext(context.Background(), actor, installation, platformTestArchiveSHA, target, runtime, endpoint)
	if err != nil {
		t.Fatal(err)
	}
	commit, err := s.consumePlatformManagedSameReleaseReviewWithRuntimeContext(context.Background(), actor, review.ID, review.SHA256, platformFixtureID(1450), target, runtime, appplatform.ActionImportData)
	if err != nil {
		t.Fatal(err)
	}
	selected, err := s.claimNextPlatformManagedSameRelease(context.Background(), platformFixtureID(1451), appplatform.ActionImportData)
	if err != nil || selected.lease.id != commit.OperationID {
		t.Fatal(err)
	}
	current := selected.lease
	for _, stage := range []appplatform.Stage{appplatform.StageValidate} {
		if current, err = s.advancePlatformManagedEnableStage(context.Background(), current); err != nil || current.snapshot.Stage != stage {
			t.Fatal("advance", current.snapshot, err)
		}
	}
	if current, err = s.validatePlatformImportData(context.Background(), current, endpoint); err != nil {
		t.Fatal(err)
	}
	if current, err = s.advancePlatformManagedEnableStage(context.Background(), current); err != nil {
		t.Fatal(err)
	}
	checkpoint, err := s.preparePlatformCheckpoint(context.Background(), current)
	if err != nil {
		t.Fatal(err)
	}
	importLease, err := s.commitPlatformCheckpoint(context.Background(), current, checkpoint, endpoint, platformCheckpointObservationFixture(checkpoint, endpoint))
	if err != nil {
		t.Fatal(err)
	}
	// Before the import began there is nothing to undo.
	if _, needed, err := s.preparePlatformImportRollback(context.Background(), importLease); err != nil || needed {
		t.Fatalf("a rollback was prepared before the import: needed=%v %v", needed, err)
	}
	intent, err := s.preparePlatformImportData(context.Background(), importLease)
	if err != nil {
		t.Fatal(err)
	}
	startLease, err := s.commitPlatformImportData(context.Background(), importLease, intent, endpoint, platformDataImportObservation(intent, endpoint))
	if err != nil {
		t.Fatal(err)
	}
	// The start fails: the checkpoint the operation kept is restored.
	rollback, needed, err := s.preparePlatformImportRollback(context.Background(), startLease)
	if err != nil || !needed {
		t.Fatal("rollback intent", needed, err)
	}
	if rollback.command.EffectNumber != "2" || rollback.command.Payload.CheckpointID != checkpoint.command.Payload.CheckpointID {
		t.Fatal("the rollback is not a restore of the pre-import checkpoint under its own effect number", rollback.command.EffectNumber)
	}
	again, _, err := s.preparePlatformImportRollback(context.Background(), startLease)
	if err != nil || again.effectID != rollback.effectID {
		t.Fatal("a retried rollback was sent under another identity", err)
	}
	if err := s.commitPlatformImportRollback(context.Background(), startLease, rollback, endpoint, platformRestoreObservationFixture(rollback, endpoint)); err != nil {
		t.Fatal("rollback commit", err)
	}
	if err := s.finishPlatformManagedSameReleaseForReview(withPlatformImportRolledBack(context.Background()), startLease); err != nil {
		t.Fatal("finish", err)
	}
	if scalar[string](t, s.st, `SELECT stage||':'||error_code FROM app_platform_operations WHERE id=?`, commit.OperationID) != "failed:managed_import_data_rolled_back" ||
		scalar[string](t, s.st, `SELECT desired_state||':'||phase FROM app_platform_installations WHERE id=?`, installation) != "disabled:disabled" ||
		scalar[int64](t, s.st, `SELECT generation FROM app_platform_installations WHERE id=?`, installation) != scalar[int64](t, s.st, `SELECT target_generation FROM app_platform_operations WHERE id=?`, commit.OperationID) ||
		scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_operation_effects WHERE operation_id=? AND stage='import' AND state='applied'`, commit.OperationID) != 2 {
		t.Fatal("a restored import did not leave the installation as it was")
	}
	if scalar[int](t, s.st, `SELECT COUNT(*) FROM audit_log WHERE action='app_platform.data_import_rolled_back' AND target_id=?`, commit.OperationID) != 1 {
		t.Fatal("the rollback is not audited")
	}
	// The installation can be reviewed for another import: it is exactly as it was.
	if _, err := s.createPlatformManagedImportDataReviewWithRuntimeContext(context.Background(), actor, installation, platformTestArchiveSHA, target, runtime, endpoint); err != nil {
		t.Fatalf("a restored installation cannot be reviewed again: %v", err)
	}
}

func TestPlatformImportDataFailureAfterImportRestoresTheCheckpointSQLite(t *testing.T) {
	testPlatformImportDataFailureAfterImportRestoresTheCheckpoint(t, false)
}

func TestPlatformImportDataFailureAfterImportRestoresTheCheckpointPostgres(t *testing.T) {
	testPlatformImportDataFailureAfterImportRestoresTheCheckpoint(t, true)
}

func TestPlatformImportDataFailureWithoutRollbackRequiresRecovery(t *testing.T) {
	s, actor, enabled, target, runtime, runner := platformImportDisabledFixture(t, false)
	installation := enabled.installationID
	endpoint := platformCheckpointEndpointFixture()
	review, err := s.createPlatformManagedImportDataReviewWithRuntimeContext(context.Background(), actor, installation, platformTestArchiveSHA, target, runtime, endpoint)
	if err != nil {
		t.Fatal(err)
	}
	commit, err := s.consumePlatformManagedSameReleaseReviewWithRuntimeContext(context.Background(), actor, review.ID, review.SHA256, platformFixtureID(1460), target, runtime, appplatform.ActionImportData)
	if err != nil {
		t.Fatal(err)
	}
	selected, err := s.claimNextPlatformManagedSameRelease(context.Background(), platformFixtureID(1461), appplatform.ActionImportData)
	if err != nil || selected.lease.id != commit.OperationID {
		t.Fatal(err)
	}
	// A failure before any data effect: nothing to restore, the installation is exactly as it was.
	if err := s.finishPlatformManagedSameReleaseForReview(context.Background(), selected.lease); err != nil {
		t.Fatal(err)
	}
	if scalar[string](t, s.st, `SELECT stage||':'||error_code FROM app_platform_operations WHERE id=?`, commit.OperationID) != "failed:managed_import_data_unavailable" ||
		scalar[string](t, s.st, `SELECT desired_state||':'||phase FROM app_platform_installations WHERE id=?`, installation) != "disabled:disabled" {
		t.Fatal("a failure before the import left the installation changed")
	}
	// A failed import keeps its archive for another try.
	s.removePlatformImportArchive(context.Background(), selected.lease, endpoint)
	if len(runner.removed) != 0 || scalar[int](t, s.st, `SELECT COUNT(*) FROM audit_log WHERE action LIKE 'app_platform.data_import_archive_%'`) != 0 {
		t.Fatalf("a failed import lost its archive: %v", runner.removed)
	}
}
