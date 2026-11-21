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

type platformQuotaTestObservation struct {
	receipt journal.SetDataQuotaReceipt
	// status, when set, makes the answer a typed status (pending, orphaned, absent or failed) and not a receipt.
	status     *journal.MigrateDataStatus
	authority  control.Authority
	endpoint   transport.CandidateReceiptEndpoint
	receivedAt time.Time
	commandSHA string
}

func (o platformQuotaTestObservation) SetDataQuotaReceipt() (journal.SetDataQuotaReceipt, error) {
	if o.status != nil {
		return journal.SetDataQuotaReceipt{}, errors.New("a status is not a receipt")
	}
	return o.receipt, nil
}
func (o platformQuotaTestObservation) DataEffectStatus() (journal.MigrateDataStatus, error) {
	if o.status == nil {
		return journal.MigrateDataStatus{}, errors.New("no status")
	}
	return *o.status, nil
}
func (o platformQuotaTestObservation) Controller() control.Authority { return o.authority }
func (o platformQuotaTestObservation) Endpoint() transport.CandidateReceiptEndpoint {
	return o.endpoint
}
func (o platformQuotaTestObservation) ReceivedAt() time.Time { return o.receivedAt }
func (o platformQuotaTestObservation) CommandSHA256() string { return o.commandSHA }

type platformQueryTestObservation struct {
	report     journal.DataQueryReport
	authority  control.Authority
	endpoint   transport.CandidateReceiptEndpoint
	receivedAt time.Time
	commandSHA string
}

func (o platformQueryTestObservation) DataQueryReport() (journal.DataQueryReport, error) {
	return o.report, nil
}
func (o platformQueryTestObservation) Controller() control.Authority { return o.authority }
func (o platformQueryTestObservation) Endpoint() transport.CandidateReceiptEndpoint {
	return o.endpoint
}
func (o platformQueryTestObservation) ReceivedAt() time.Time { return o.receivedAt }
func (o platformQueryTestObservation) CommandSHA256() string { return o.commandSHA }

// platformFakeDataRunner answers the data queries of a review from memory: capacity per the given fit, and an inbox
// that holds the given archive, or none.
type platformFakeDataRunner struct {
	fits    bool
	archive *journal.DataQueryReport
	removed []string
}

func (f *platformFakeDataRunner) install(t *testing.T) {
	t.Helper()
	previous := platformDataQueryRunner
	t.Cleanup(func() { platformDataQueryRunner = previous })
	platformDataQueryRunner = func(_ context.Context, endpoint platformRuntimeEndpoint, request platformDataQueryRequest) (platformDataQueryObservation, error) {
		c := request.command
		report := journal.DataQueryReport{
			Format: "portal.runner.data-query-report/1-dev.1", DeploymentID: c.DeploymentID, ControllerEpoch: c.ControllerEpoch,
			InstallationID: c.InstallationID, OperationID: c.OperationID, EffectID: c.EffectID, PayloadSHA256: c.PayloadSHA256,
			Operation: c.Payload.Operation, ArchiveBytes: "0", ArchiveEntries: "0", ContentBytes: "0", SchemaMarker: "0",
			CapacityRequired: "0", CapacityLeft: "0", ObservedMS: strconv.FormatInt(time.Now().UnixMilli(), 10),
		}
		switch c.Payload.Operation {
		case control.DataQueryQuotaCapacity:
			report.CapacityFits = f.fits
			if !f.fits {
				report.CapacityDim, report.CapacityRequired, report.CapacityLeft = journal.CapacityDataQuota, "6442450944", "1073741824"
			}
		case control.DataQueryInspectImport:
			if f.archive != nil {
				report.ArchiveFound, report.ArchiveBytes, report.ArchiveEntries = true, f.archive.ArchiveBytes, f.archive.ArchiveEntries
				report.ContentBytes, report.SchemaMarker = f.archive.ContentBytes, f.archive.SchemaMarker
			}
		case control.DataQueryRemoveImport:
			f.removed = append(f.removed, c.Payload.ArchiveSHA256)
		}
		return platformQueryTestObservation{
			report: report, authority: request.authority,
			endpoint:   transport.CandidateReceiptEndpoint{Path: endpoint.path, RunnerUID: 0, ControllerUID: endpoint.controllerUID, ControllerGID: endpoint.controllerGID, RootController: true},
			receivedAt: time.Now().UTC(), commandSHA: platformDigest(request.raw),
		}, nil
	}
}

// platformDataActionFixture is an enabled, ready managed installation with its runtime target.
func platformDataActionFixture(t *testing.T, pg bool) (*Server, platformActorSession, platformOperationLease, *platformManagedInstallTarget, platformRuntimeRegistration) {
	t.Helper()
	s, actor, firstEnable := testPlatformManagedEnableSyntheticStart(t, pg)
	var previous platformManagedUpdatePlan
	var raw string
	if err := s.st.queryRow(`SELECT p.plan_json FROM app_platform_operations o JOIN app_platform_review_plans p ON p.id=o.review_plan_id
		WHERE o.installation_id=? AND o.action='enable' AND o.stage='complete' ORDER BY o.completed_ms DESC LIMIT 1`, firstEnable.installationID).Scan(&raw); err != nil ||
		json.Unmarshal([]byte(raw), &previous) != nil || previous.TargetRuntime == nil {
		t.Fatal("data action target", err)
	}
	epoch := scalar[string](t, s.st, `SELECT controller_epoch FROM app_platform_installations WHERE id=?`, firstEnable.installationID)
	dek, err := s.dek()
	if err != nil {
		t.Fatal(err)
	}
	runtime, seed, err := s.platformDataRuntime(s.st.poolExec(), epoch, dek)
	clear(seed)
	if err != nil {
		t.Fatal(err)
	}
	return s, actor, firstEnable, previous.TargetRuntime, runtime
}

func platformQuotaObservation(intent platformSetDataQuotaIntent, endpoint platformRuntimeEndpoint) platformQuotaTestObservation {
	now := strconv.FormatInt(time.Now().UTC().UnixMilli(), 10)
	c, p := intent.command, intent.command.Payload
	return platformQuotaTestObservation{
		receipt: journal.SetDataQuotaReceipt{
			Format: "portal.runner.set-data-quota-receipt/1-dev.1", DeploymentID: c.DeploymentID, ControllerEpoch: c.ControllerEpoch,
			InstallationID: c.InstallationID, OperationID: c.OperationID, TargetGeneration: c.TargetGeneration, Stage: c.Stage,
			EffectID: c.EffectID, PayloadSHA256: c.PayloadSHA256, NamespaceID: p.NamespaceID, ReleaseSHA256: p.ReleaseSHA256,
			DataFormat: p.DataFormat, WriterFence: p.WriterFence, ProjectID: p.ProjectID, NamespaceInode: p.NamespaceInode, Device: p.Device,
			OldBytes: p.OldBytes, OldInodes: p.OldInodes, NewBytes: p.NewBytes, NewInodes: p.NewInodes,
			UsedBytes: "4096", UsedInodes: "3", State: "raised", ObservedMS: now, SettledMS: now,
		},
		authority:  intent.authority,
		endpoint:   transport.CandidateReceiptEndpoint{Path: endpoint.path, RunnerUID: 0, ControllerUID: endpoint.controllerUID, ControllerGID: endpoint.controllerGID, RootController: true},
		receivedAt: time.Now().UTC(), commandSHA: platformDigest(intent.raw),
	}
}

// platformRaiseQuota runs a raise from review to completion and returns the finished lease.
func platformRaiseQuota(t *testing.T, s *Server, actor platformActorSession, installation string, target *platformManagedInstallTarget, runtime platformRuntimeRegistration, newBytes, newInodes int64, key int) platformOperationLease {
	t.Helper()
	endpoint := platformCheckpointEndpointFixture()
	review, err := s.createPlatformSetDataQuotaReviewWithRuntimeContext(context.Background(), actor, installation, newBytes, newInodes, target, runtime, endpoint)
	if err != nil {
		t.Fatal("raise review", err)
	}
	commit, err := s.consumePlatformSetDataQuotaReviewWithRuntimeContext(context.Background(), actor, review.ID, review.SHA256, platformFixtureID(key), target, runtime)
	if err != nil || commit.Stage != appplatform.StageQueued {
		t.Fatal("raise admission", commit, err)
	}
	selected, err := s.claimNextPlatformSetDataQuota(context.Background(), platformFixtureID(key+1))
	if err != nil || selected.lease.id != commit.OperationID {
		t.Fatal("raise claim", selected, err)
	}
	current := selected.lease
	for current.snapshot.Stage != appplatform.StagePrepareData {
		if current, err = s.advancePlatformSetDataQuotaStage(context.Background(), current); err != nil {
			t.Fatal("raise stage", err)
		}
	}
	intent, err := s.preparePlatformSetDataQuota(context.Background(), current)
	if err != nil {
		t.Fatal("raise intent", err)
	}
	done, err := s.commitPlatformSetDataQuota(context.Background(), current, intent, endpoint, platformQuotaObservation(intent, endpoint))
	if err != nil || done.snapshot.Stage != appplatform.StageComplete {
		t.Fatal("raise commit", done.snapshot, err)
	}
	return done
}

func testPlatformSetDataQuotaRaisesAndSurvivesTheLifecycle(t *testing.T, pg bool) {
	s, actor, enabled, target, runtime := platformDataActionFixture(t, pg)
	(&platformFakeDataRunner{fits: true}).install(t)
	installation := enabled.installationID
	namespace := scalar[string](t, s.st, `SELECT id FROM app_platform_data_namespaces WHERE owner_installation_id=?`, installation)
	beforeBytes := scalar[int64](t, s.st, `SELECT quota_bytes FROM app_platform_data_namespaces WHERE id=?`, namespace)
	beforeInodes := scalar[int64](t, s.st, `SELECT quota_inodes FROM app_platform_data_namespaces WHERE id=?`, namespace)
	generation := scalar[int64](t, s.st, `SELECT generation FROM app_platform_installations WHERE id=?`, installation)

	// A raise is online: the installation keeps running at its generation.
	newBytes, newInodes := beforeBytes*4, beforeInodes*2
	done := platformRaiseQuota(t, s, actor, installation, target, runtime, newBytes, newInodes, 1400)
	if done.snapshot.Action != appplatform.ActionSetDataQuota ||
		scalar[string](t, s.st, `SELECT desired_state||':'||phase FROM app_platform_installations WHERE id=?`, installation) != "enabled:ready" ||
		scalar[int64](t, s.st, `SELECT generation FROM app_platform_installations WHERE id=?`, installation) != generation {
		t.Fatal("a raise changed the installation")
	}
	if scalar[int64](t, s.st, `SELECT quota_bytes FROM app_platform_data_namespaces WHERE id=?`, namespace) != newBytes ||
		scalar[int64](t, s.st, `SELECT quota_inodes FROM app_platform_data_namespaces WHERE id=?`, namespace) != newInodes ||
		scalar[int64](t, s.st, `SELECT observed_bytes FROM app_platform_data_namespaces WHERE id=?`, namespace) != 4096 {
		t.Fatal("the namespace row does not hold the raised limits and the observed usage")
	}
	if scalar[int](t, s.st, `SELECT COUNT(*) FROM audit_log WHERE action='app_platform.data_quota_raised' AND target_id=?`, done.id) != 1 ||
		scalar[int](t, s.st, `SELECT COUNT(*) FROM audit_log WHERE action IN ('app_platform.data_quota_review','app_platform.data_quota_intent','app_platform.data_quota_claimed') `) != 3 {
		t.Fatal("a raise is not audited at every stage")
	}
	bytes, inodes, chain, err := platformCurrentNamespaceQuota(s.st.poolExec(), installation, namespace)
	if err != nil || bytes != newBytes || inodes != newInodes || len(chain) != 2 || chain[0].Bytes != beforeBytes {
		t.Fatal("quota chain", bytes, inodes, chain, err)
	}

	// Every history loader reads the current quota: the install-time backend history, then a new start.
	access, err := s.preparePlatformWritableBackendAccess(context.Background(), actor, installation)
	if err != nil {
		t.Fatal("backend history after a raise", err)
	}
	access.Close()
	platformCompleteSyntheticDisable(t, s, actor, enabled)
	var previous platformManagedUpdatePlan
	var raw string
	if err := s.st.queryRow(`SELECT p.plan_json FROM app_platform_operations o JOIN app_platform_review_plans p ON p.id=o.review_plan_id
		WHERE o.installation_id=? AND o.action='enable' AND o.stage='complete' ORDER BY o.completed_ms DESC LIMIT 1`, installation).Scan(&raw); err != nil ||
		json.Unmarshal([]byte(raw), &previous) != nil {
		t.Fatal(err)
	}
	review, err := s.createPlatformManagedEnableReviewWithRuntimeContext(context.Background(), actor, installation, target, runtime)
	if err != nil {
		t.Fatal("enable review after a raise", err)
	}
	commit, err := s.consumePlatformManagedEnableReviewWithRuntimeContext(context.Background(), actor, review.ID, review.SHA256, platformFixtureID(1410), target, runtime)
	if err != nil {
		t.Fatal("enable admission after a raise", err)
	}
	selected, err := s.claimNextPlatformManagedEnable(context.Background(), platformFixtureID(1411))
	if err != nil || selected.lease.id != commit.OperationID {
		t.Fatal("enable claim", selected, err)
	}
	snapshot := selected.lease
	for snapshot.snapshot.Stage == appplatform.StageQueued || snapshot.snapshot.Stage == appplatform.StageValidate {
		if snapshot, err = s.advancePlatformManagedEnableStage(context.Background(), snapshot); err != nil {
			t.Fatal(err)
		}
	}
	checkpoint, err := s.preparePlatformCheckpoint(context.Background(), snapshot)
	if err != nil {
		t.Fatal("checkpoint after a raise", err)
	}
	checkpointEndpoint := platformCheckpointEndpointFixture()
	startLease, err := s.commitPlatformCheckpoint(context.Background(), snapshot, checkpoint, checkpointEndpoint, platformCheckpointObservationFixture(checkpoint, checkpointEndpoint))
	if err != nil {
		t.Fatal(err)
	}
	start, err := s.preparePlatformUpdateStart(context.Background(), startLease)
	if err != nil || start.command.Payload.QuotaBytes != strconv.FormatInt(newBytes, 10) || start.command.Payload.QuotaInodes != strconv.FormatInt(newInodes, 10) {
		t.Fatalf("a start after a raise carries %s/%s, want %d/%d: %v", start.command.Payload.QuotaBytes, start.command.Payload.QuotaInodes, newBytes, newInodes, err)
	}
	result := platformRunSyntheticManagedEnable(t, s, startLease)
	if result.snapshot.Stage != appplatform.StageComplete {
		t.Fatal("enable after a raise did not complete")
	}
	access, err = s.preparePlatformWritableBackendAccess(context.Background(), actor, installation)
	if err != nil {
		t.Fatal("update history after a raise", err)
	}
	access.Close()
}

func TestPlatformSetDataQuotaRaisesAndSurvivesTheLifecycleSQLite(t *testing.T) {
	testPlatformSetDataQuotaRaisesAndSurvivesTheLifecycle(t, false)
}

func TestPlatformSetDataQuotaRaisesAndSurvivesTheLifecyclePostgres(t *testing.T) {
	testPlatformSetDataQuotaRaisesAndSurvivesTheLifecycle(t, true)
}

func testPlatformSetDataQuotaRefusals(t *testing.T, pg bool) {
	s, actor, enabled, target, runtime := platformDataActionFixture(t, pg)
	installation := enabled.installationID
	endpoint := platformCheckpointEndpointFixture()
	namespace := scalar[string](t, s.st, `SELECT id FROM app_platform_data_namespaces WHERE owner_installation_id=?`, installation)
	bytes := scalar[int64](t, s.st, `SELECT quota_bytes FROM app_platform_data_namespaces WHERE id=?`, namespace)
	inodes := scalar[int64](t, s.st, `SELECT quota_inodes FROM app_platform_data_namespaces WHERE id=?`, namespace)
	runner := &platformFakeDataRunner{fits: true}
	runner.install(t)
	review := func(newBytes, newInodes int64) (platformRemovalReview, error) {
		return s.createPlatformSetDataQuotaReviewWithRuntimeContext(context.Background(), actor, installation, newBytes, newInodes, target, runtime, endpoint)
	}
	if _, err := review(bytes/2, inodes); !errors.Is(err, errPlatformDataQuotaLower) {
		t.Fatalf("a lower byte quota was reviewed: %v", err)
	}
	if _, err := review(bytes, inodes/2); !errors.Is(err, errPlatformDataQuotaLower) {
		t.Fatalf("a lower inode quota was reviewed: %v", err)
	}
	if _, err := review(bytes, inodes); !errors.Is(err, errPlatformDataQuotaInvalid) {
		t.Fatalf("an unchanged quota was reviewed: %v", err)
	}
	if _, err := review(appplatform.DataQuotaMaxBytes+64<<20, inodes); !errors.Is(err, errPlatformDataQuotaInvalid) {
		t.Fatalf("a quota above the platform maximum was reviewed: %v", err)
	}
	if _, err := review(bytes+1, inodes); !errors.Is(err, errPlatformDataQuotaInvalid) {
		t.Fatalf("a quota off the 64 MiB grid was reviewed: %v", err)
	}

	// The runner's capacity answer is part of the review. A raise it cannot hold is reviewed, with the shortfall
	// named, and cannot be confirmed.
	runner.fits = false
	short, err := review(bytes*2, inodes)
	if err != nil {
		t.Fatal(err)
	}
	var plan platformDataQuotaPlan
	if json.Unmarshal([]byte(short.JSON), &plan) != nil || plan.Capacity != "short" || plan.CapacityDimension != journal.CapacityDataQuota || plan.CapacityRemaining != 1073741824 {
		t.Fatalf("the shortfall is not named: %+v", plan)
	}
	if _, err := s.consumePlatformSetDataQuotaReviewWithRuntimeContext(context.Background(), actor, short.ID, short.SHA256, platformFixtureID(1420), target, runtime); !errors.Is(err, errPlatformDataQuotaCapacity) {
		t.Fatalf("a raise beyond capacity was confirmed: %v", err)
	}
	runner.fits = true

	// A confirmed raise is the installation's only pending operation: a second review is refused until it ends.
	first, err := review(bytes*2, inodes)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.consumePlatformSetDataQuotaReviewWithRuntimeContext(context.Background(), actor, first.ID, first.SHA256, platformFixtureID(1421), target, runtime); err != nil {
		t.Fatal(err)
	}
	if _, err := review(bytes*3, inodes); err == nil {
		t.Fatal("a second raise was reviewed beside a pending one")
	}
}

func TestPlatformSetDataQuotaRefusalsSQLite(t *testing.T) { testPlatformSetDataQuotaRefusals(t, false) }
func TestPlatformSetDataQuotaRefusalsPostgres(t *testing.T) {
	testPlatformSetDataQuotaRefusals(t, true)
}

func TestPlatformSetDataQuotaFailureLeavesTheInstallationAndQuotaAlone(t *testing.T) {
	s, actor, enabled, target, runtime := platformDataActionFixture(t, false)
	(&platformFakeDataRunner{fits: true}).install(t)
	installation := enabled.installationID
	namespace := scalar[string](t, s.st, `SELECT id FROM app_platform_data_namespaces WHERE owner_installation_id=?`, installation)
	bytes := scalar[int64](t, s.st, `SELECT quota_bytes FROM app_platform_data_namespaces WHERE id=?`, namespace)
	inodes := scalar[int64](t, s.st, `SELECT quota_inodes FROM app_platform_data_namespaces WHERE id=?`, namespace)
	endpoint := platformCheckpointEndpointFixture()
	review, err := s.createPlatformSetDataQuotaReviewWithRuntimeContext(context.Background(), actor, installation, bytes*2, inodes, target, runtime, endpoint)
	if err != nil {
		t.Fatal(err)
	}
	commit, err := s.consumePlatformSetDataQuotaReviewWithRuntimeContext(context.Background(), actor, review.ID, review.SHA256, platformFixtureID(1430), target, runtime)
	if err != nil {
		t.Fatal(err)
	}
	selected, err := s.claimNextPlatformSetDataQuota(context.Background(), platformFixtureID(1431))
	if err != nil || selected.lease.id != commit.OperationID {
		t.Fatal(err)
	}
	if err := s.finishPlatformSetDataQuotaForReview(context.Background(), selected.lease, "data_quota_capacity"); err != nil {
		t.Fatal(err)
	}
	if scalar[string](t, s.st, `SELECT stage||':'||error_code FROM app_platform_operations WHERE id=?`, commit.OperationID) != "failed:data_quota_capacity" ||
		scalar[int64](t, s.st, `SELECT quota_bytes FROM app_platform_data_namespaces WHERE id=?`, namespace) != bytes ||
		scalar[string](t, s.st, `SELECT desired_state||':'||phase FROM app_platform_installations WHERE id=?`, installation) != "enabled:ready" {
		t.Fatal("a refused raise changed the quota or the installation")
	}
	if _, _, chain, err := platformCurrentNamespaceQuota(s.st.poolExec(), installation, namespace); err != nil || len(chain) != 1 {
		t.Fatal("a failed raise entered the quota chain", chain, err)
	}
	if !strings.Contains(platformPublicOperationErrorCode("data_quota_capacity"), "data_quota_capacity") {
		t.Fatal("the capacity code is not shown to the console")
	}
}
