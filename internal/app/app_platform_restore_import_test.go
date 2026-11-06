package app

import (
	"bytes"
	"context"
	"crypto/ed25519"
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

type platformImportTestObservation struct {
	receipt    journal.ImportCheckpointReceipt
	authority  control.Authority
	endpoint   transport.CandidateReceiptEndpoint
	receivedAt time.Time
	commandSHA string
}

func (o platformImportTestObservation) ImportCheckpointReceipt() (journal.ImportCheckpointReceipt, error) {
	return o.receipt, nil
}
func (o platformImportTestObservation) Controller() control.Authority { return o.authority }
func (o platformImportTestObservation) Endpoint() transport.CandidateReceiptEndpoint {
	return o.endpoint
}
func (o platformImportTestObservation) ReceivedAt() time.Time { return o.receivedAt }
func (o platformImportTestObservation) CommandSHA256() string { return o.commandSHA }

func platformImportObservationFixture(intent platformImportIntent, endpoint platformRuntimeEndpoint) platformImportTestObservation {
	now := time.Now().UTC().UnixMilli()
	c, p := intent.command, intent.command.Payload
	stamp := strconv.FormatInt(now, 10)
	return platformImportTestObservation{
		receipt: journal.ImportCheckpointReceipt{
			Format: platformImportReceiptFormat, DeploymentID: c.DeploymentID, ControllerEpoch: c.ControllerEpoch,
			InstallationID: c.InstallationID, OperationID: c.OperationID, TargetGeneration: c.TargetGeneration, Stage: c.Stage,
			EffectID: c.EffectID, PayloadSHA256: c.PayloadSHA256, GrantID: p.GrantID, RecoverySetID: p.RecoverySetID,
			CheckpointID: p.CheckpointID, NamespaceID: p.NamespaceID, ReleaseSHA256: p.ReleaseSHA256, ManifestSHA256: p.ManifestSHA256,
			ArchiveBytes: p.ArchiveBytes, DataFormat: p.DataFormat, WriterFence: p.WriterFence,
			// The new root's own identity: deliberately unlike the old host's provenance values.
			ProjectID: "424242", NamespaceInode: "777", Device: "9", QuotaBytes: p.QuotaBytes, QuotaInodes: p.QuotaInodes,
			Entries: "3", UsedBytes: "4096", UsedInodes: "12", State: "imported", ObservedMS: stamp, SettledMS: stamp,
			Claim: journal.ImportCheckpointClaim{
				Format: "portal.runner.import-checkpoint-claim/1-dev.1", ClaimID: platformFixtureID(70), EffectID: c.EffectID,
				ResourceID: p.NamespaceID, OwnershipSHA256: platformDigest([]byte("owner")), AdmittedFence: c.InstallationFence,
				ClaimedMS: stamp, GrantID: p.GrantID, RecoverySetID: p.RecoverySetID, CheckpointID: p.CheckpointID,
				NamespaceID: p.NamespaceID, ProjectID: "424242", RootFilesystemUUID: platformFixtureID(77), RootInode: "555",
			},
		},
		authority: intent.authority,
		endpoint: transport.CandidateReceiptEndpoint{
			Path: endpoint.path, RunnerUID: 0, ControllerUID: endpoint.controllerUID,
			ControllerGID: endpoint.controllerGID, RootController: true,
		},
		receivedAt: time.Now().UTC(), commandSHA: platformDigest(intent.raw),
	}
}

type platformImportTestExecutor struct {
	calls, reads int
	last         platformImportIntent
	mutate       func(*journal.ImportCheckpointReceipt)
	err          error
}

func (f *platformImportTestExecutor) observe(endpoint platformRuntimeEndpoint, intent platformImportIntent) (platformImportObservation, error) {
	f.last = intent
	if f.err != nil {
		return nil, f.err
	}
	o := platformImportObservationFixture(intent, endpoint)
	if f.mutate != nil {
		f.mutate(&o.receipt)
	}
	return o, nil
}
func (f *platformImportTestExecutor) Execute(_ context.Context, endpoint platformRuntimeEndpoint, intent platformImportIntent) (platformImportObservation, error) {
	f.calls++
	return f.observe(endpoint, intent)
}
func (f *platformImportTestExecutor) Read(_ context.Context, endpoint platformRuntimeEndpoint, intent platformImportIntent) (platformImportObservation, error) {
	f.reads++
	return f.observe(endpoint, intent)
}

type platformImportFixtureState struct {
	s                                     *Server
	actor                                 platformActorSession
	installation, recoverySet, oldRuntime string
	replacement                           platformRuntimeRegistration
	checkpointOperation                   string
	executor                              *platformImportTestExecutor
	review                                platformRemovalReview
}

// platformRestoreImportFixture is a restored, paused Portal that has enrolled and confirmed a
// replacement runtime and still holds a retained, verified recovery set for one managed installation.
func platformRestoreImportFixture(t *testing.T, pg bool) *platformImportFixtureState {
	t.Helper()
	s, actor, updateLease, recoverySetID := platformManagedRestoreReviewFixture(t, pg)
	f := &platformImportFixtureState{s: s, actor: actor, installation: updateLease.installationID, recoverySet: recoverySetID, checkpointOperation: updateLease.id}
	f.oldRuntime = scalar[string](t, s.st, `SELECT id FROM app_platform_runtime_bindings`)
	platformSimulateRestore(t, s)
	reg, err := s.preparePlatformReplacementRuntime(actor, platformFixtureID(5100))
	if err != nil {
		t.Fatal(err)
	}
	if f.replacement, err = s.confirmPlatformObservedReplacementRuntime(actor, reg, runtimeObservationFixture(reg)); err != nil {
		t.Fatal(err)
	}
	f.executor = &platformImportTestExecutor{}
	s.platformImportCheckpoint = f.executor
	return f
}

func (f *platformImportFixtureState) reviewAndConsume(t *testing.T) platformRemovalCommit {
	t.Helper()
	review, err := f.s.createPlatformRestoreImportReview(context.Background(), f.actor, f.installation, f.recoverySet)
	if err != nil {
		t.Fatal("restore import review:", err)
	}
	f.review = review
	commit, err := f.s.consumePlatformRestoreImportReview(context.Background(), f.actor, review.ID, review.SHA256, platformFixtureID(5200))
	if err != nil {
		t.Fatal("restore import consume:", err)
	}
	return commit
}

// drive claims the operation and runs it until it stops advancing, returning the final lease.
func (f *platformImportFixtureState) drive(t *testing.T, operation string) (platformOperationLease, error) {
	t.Helper()
	lease, err := f.s.st.claimPlatformOperation(operation, platformFixtureID(5300))
	if err != nil {
		return lease, err
	}
	for lease.snapshot.Stage != appplatform.StageComplete {
		next, err := f.s.runPlatformRestoreImport(context.Background(), lease, platformRestoreEndpointFixture())
		if err != nil {
			return lease, err
		}
		if next.id != "" {
			lease = next
		} else {
			break
		}
	}
	return lease, nil
}

func testRestoreImportRepointsRuntimeAndIssuesGrant(t *testing.T, pg bool) {
	f := platformRestoreImportFixture(t, pg)
	s := f.s
	epoch := s.st.GetSetting("app_platform_controller_epoch", "")
	generation := scalar[int64](t, s.st, `SELECT generation FROM app_platform_installations WHERE id=?`, f.installation)
	if scalar[string](t, s.st, `SELECT runtime_id FROM app_platform_installations WHERE id=?`, f.installation) != f.oldRuntime {
		t.Fatal("fixture installation does not start on the old runtime")
	}
	review, err := s.createPlatformRestoreImportReview(context.Background(), f.actor, f.installation, f.recoverySet)
	if err != nil {
		t.Fatal(err)
	}
	var plan platformRestoreImportPlan
	if json.Unmarshal([]byte(review.JSON), &plan) != nil || plan.Format != platformRestoreImportFormat ||
		plan.RuntimeID != f.replacement.id || plan.SourceRuntimeID != f.oldRuntime || plan.DeploymentID != f.replacement.deploymentID ||
		plan.RecoverySetID != f.recoverySet || plan.ControllerEpoch != epoch || plan.SourceGeneration != generation || plan.TargetGeneration != generation+1 {
		t.Fatalf("review projection: %+v", plan)
	}
	if scalar[string](t, s.st, `SELECT action FROM app_platform_review_plans WHERE id=?`, review.ID) != "restore_import" ||
		scalar[string](t, s.st, `SELECT runtime_id FROM app_platform_installations WHERE id=?`, f.installation) != f.oldRuntime ||
		scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_import_grants`) != 0 {
		t.Fatal("review changed the installation or issued a grant")
	}
	key := platformFixtureID(5200)
	commit, err := s.consumePlatformRestoreImportReview(context.Background(), f.actor, review.ID, review.SHA256, key)
	if err != nil || commit.Replayed || commit.InstallationID != f.installation || commit.Generation != generation+1 ||
		commit.Revision != 1 || commit.Stage != appplatform.StageQueued {
		t.Fatalf("consume: %+v %v", commit, err)
	}
	if scalar[string](t, s.st, `SELECT runtime_id FROM app_platform_installations WHERE id=?`, f.installation) != f.replacement.id {
		t.Fatal("installation was not repointed at the replacement runtime")
	}
	if scalar[string](t, s.st, `SELECT phase FROM app_platform_installations WHERE id=?`, f.installation) != "review_required" {
		t.Fatal("importing a checkpoint resumed the installation")
	}
	var grantState, grantRuntime, grantEpoch, grantSet string
	if err := s.st.queryRow(`SELECT state,runtime_id,controller_epoch,recovery_set_id FROM app_platform_import_grants`).Scan(&grantState, &grantRuntime, &grantEpoch, &grantSet); err != nil ||
		grantState != "issued" || grantRuntime != f.replacement.id || grantEpoch != epoch || grantSet != f.recoverySet ||
		scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_import_grants`) != 1 {
		t.Fatalf("grant: %s %s %s %s %v", grantState, grantRuntime, grantEpoch, grantSet, err)
	}
	if scalar[string](t, s.st, `SELECT action||':'||stage||':'||managed FROM app_platform_operations WHERE id=?`, commit.OperationID) != "restore_import:queued:1" ||
		scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_review_plans WHERE id=? AND consumed_ms IS NOT NULL`, review.ID) != 1 {
		t.Fatal("operation not queued from the consumed review")
	}
	replay, err := s.consumePlatformRestoreImportReview(context.Background(), f.actor, review.ID, review.SHA256, key)
	if err != nil || !replay.Replayed || replay.OperationID != commit.OperationID || scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_import_grants`) != 1 {
		t.Fatalf("replay: %+v %v", replay, err)
	}
	if _, err := s.consumePlatformRestoreImportReview(context.Background(), f.actor, review.ID, review.SHA256, platformFixtureID(5201)); !errors.Is(err, errAppPlatformOperationConflict) {
		t.Fatalf("a second key consumed the review: %v", err)
	}
}

func TestRestoreImportRepointsRuntimeAndIssuesGrantSQLite(t *testing.T) {
	testRestoreImportRepointsRuntimeAndIssuesGrant(t, false)
}
func TestRestoreImportRepointsRuntimeAndIssuesGrantPostgres(t *testing.T) {
	testRestoreImportRepointsRuntimeAndIssuesGrant(t, true)
}

func testRestoreImportReviewRefusals(t *testing.T, pg bool) {
	for _, kind := range []string{"not_paused", "recovery_set_verified", "no_manifest", "phase_ready", "no_replacement", "wrong_installation", "checkpoint_receipt_mismatch", "prior_grant", "work_in_flight"} {
		t.Run(kind, func(t *testing.T) {
			f := platformRestoreImportFixture(t, pg)
			s, installation := f.s, f.installation
			switch kind {
			case "not_paused":
				platformSetPause(t, s.st, "0")
			case "recovery_set_verified":
				mustExec(t, s.st, `UPDATE app_platform_recovery_sets SET state='verified'`)
			case "no_manifest":
				mustExec(t, s.st, `UPDATE app_platform_recovery_sets SET manifest_sha256=NULL`)
			case "phase_ready":
				mustExec(t, s.st, `UPDATE app_platform_installations SET phase='ready'`)
			case "no_replacement":
				mustExec(t, s.st, `UPDATE app_platform_runtime_bindings SET enabled=0,sealed_control_key=NULL,verification_key_fingerprint=NULL WHERE id=?`, f.replacement.id)
			case "wrong_installation":
				installation = platformFixtureID(5999)
			case "checkpoint_receipt_mismatch":
				mustExec(t, s.st, `UPDATE app_platform_operation_effects SET receipt_json=REPLACE(receipt_json,'"manifest_sha256":"','"manifest_sha256":"0') WHERE operation_id=? AND stage='snapshot'`, f.checkpointOperation)
			case "prior_grant":
				// A live grant for the recovery set blocks a second one; a revoked grant does not (see
				// TestRevokedImportGrantAllowsRetry).
				mustExec(t, s.st, `INSERT INTO app_platform_import_grants(id,recovery_set_id,runtime_id,controller_epoch,state,consumed_ms,created_ms,updated_ms) VALUES(?,?,?,?,'issued',NULL,1,1)`,
					platformFixtureID(5400), f.recoverySet, f.replacement.id, s.st.GetSetting("app_platform_controller_epoch", ""))
			case "work_in_flight":
				mustExec(t, s.st, `UPDATE app_platform_operations SET stage='quiesce',completed_ms=NULL,error_code=NULL,state_revision=4 WHERE id=?`, f.checkpointOperation)
			}
			before := dumpOf(t, s.st)
			if review, err := s.createPlatformRestoreImportReview(context.Background(), f.actor, installation, f.recoverySet); err == nil {
				t.Fatalf("review admitted despite %s: %+v", kind, review)
			}
			if string(stripHeader(before)) != string(stripHeader(dumpOf(t, s.st))) {
				t.Fatal("refused review changed state")
			}
		})
	}
}

func TestRestoreImportReviewRefusalsSQLite(t *testing.T)   { testRestoreImportReviewRefusals(t, false) }
func TestRestoreImportReviewRefusalsPostgres(t *testing.T) { testRestoreImportReviewRefusals(t, true) }

func testConsumedGrantRefused(t *testing.T, pg bool) {
	for _, state := range []string{"consumed", "revoked"} {
		t.Run(state, func(t *testing.T) {
			f := platformRestoreImportFixture(t, pg)
			commit := f.reviewAndConsume(t)
			if state == "consumed" {
				mustExec(t, f.s.st, `UPDATE app_platform_import_grants SET state='consumed',consumed_ms=5,updated_ms=5`)
			} else {
				mustExec(t, f.s.st, `UPDATE app_platform_import_grants SET state='revoked',updated_ms=5`)
			}
			_, err := f.drive(t, commit.OperationID)
			if !errors.Is(err, errAppPlatformStageEvidence) {
				t.Fatalf("a %s grant was honoured: %v", state, err)
			}
			if f.executor.calls != 0 {
				t.Fatal("a refused grant reached the runner")
			}
			if scalar[int](t, f.s.st, `SELECT COUNT(*) FROM app_platform_operation_effects WHERE operation_id=?`, commit.OperationID) != 0 {
				t.Fatal("a refused grant left an effect behind")
			}
		})
	}
	// The same grant cannot be asked for again once it exists in any state.
	f := platformRestoreImportFixture(t, pg)
	f.reviewAndConsume(t)
	mustExec(t, f.s.st, `UPDATE app_platform_import_grants SET state='consumed',consumed_ms=5,updated_ms=5`)
	mustExec(t, f.s.st, `UPDATE app_platform_operations SET stage='failed',state_revision=2,completed_ms=5 WHERE action='restore_import'`)
	if _, err := f.s.createPlatformRestoreImportReview(context.Background(), f.actor, f.installation, f.recoverySet); err == nil {
		t.Fatal("a second review was admitted for an already consumed grant")
	}
}

func TestConsumedGrantRefusedSQLite(t *testing.T)   { testConsumedGrantRefused(t, false) }
func TestConsumedGrantRefusedPostgres(t *testing.T) { testConsumedGrantRefused(t, true) }

func testForeignRuntimeGrantRefused(t *testing.T, pg bool) {
	f := platformRestoreImportFixture(t, pg)
	commit := f.reviewAndConsume(t)
	// A grant that names a different runtime than the installation now runs on is not this runner's.
	mustExec(t, f.s.st, `UPDATE app_platform_import_grants SET runtime_id=?`, f.oldRuntime)
	if _, err := f.drive(t, commit.OperationID); !errors.Is(err, errAppPlatformStageEvidence) {
		t.Fatalf("a grant for another runtime was honoured: %v", err)
	}
	mustExec(t, f.s.st, `UPDATE app_platform_import_grants SET runtime_id=?`, f.replacement.id)
	mustExec(t, f.s.st, `UPDATE app_platform_installations SET runtime_id=? WHERE id=?`, f.oldRuntime, f.installation)
	mustExec(t, f.s.st, `UPDATE app_platform_operations SET lease_owner=NULL,lease_until_ms=NULL WHERE id=?`, commit.OperationID)
	if _, err := f.drive(t, commit.OperationID); err == nil {
		t.Fatal("an installation on another runtime imported")
	}
	if f.executor.calls != 0 {
		t.Fatal("a foreign-runtime grant reached the runner")
	}
}

func TestForeignRuntimeGrantRefusedSQLite(t *testing.T)   { testForeignRuntimeGrantRefused(t, false) }
func TestForeignRuntimeGrantRefusedPostgres(t *testing.T) { testForeignRuntimeGrantRefused(t, true) }

func testRestoreImportCommandCarriesProvenance(t *testing.T, pg bool) {
	f := platformRestoreImportFixture(t, pg)
	commit := f.reviewAndConsume(t)
	lease, err := f.s.st.claimPlatformOperation(commit.OperationID, platformFixtureID(5300))
	if err != nil || lease.snapshot.Action != appplatform.ActionRestoreImport || lease.snapshot.Stage != appplatform.StageQueued {
		t.Fatal(lease, err)
	}
	for _, stage := range []appplatform.Stage{appplatform.StageValidate, appplatform.StageRestoreData} {
		if lease, err = f.s.runPlatformRestoreImport(context.Background(), lease, platformRestoreEndpointFixture()); err != nil || lease.snapshot.Stage != stage {
			t.Fatalf("advance to %s: %v", stage, err)
		}
	}
	intent, err := f.s.preparePlatformRestoreImport(context.Background(), lease)
	if err != nil {
		t.Fatal(err)
	}
	var raw string
	if err := f.s.st.queryRow(`SELECT receipt_json FROM app_platform_operation_effects WHERE operation_id=? AND stage='snapshot'`, f.checkpointOperation).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	old, err := journal.ParseCheckpointReceipt([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	c, p := intent.command, intent.command.Payload
	if c.Kind != "import-checkpoint" || c.Stage != "restore_data" || c.EffectNumber != "1" || c.DeploymentID != f.replacement.deploymentID ||
		c.OperationID != commit.OperationID || c.InstallationID != f.installation || c.ControllerEpoch != lease.controllerEpoch ||
		c.InstallationFence != strconv.FormatInt(lease.fence, 10) || c.TargetGeneration != strconv.FormatUint(lease.snapshot.Generation, 10) {
		t.Fatalf("command identity: %+v", c)
	}
	if p.GrantID != scalar[string](t, f.s.st, `SELECT id FROM app_platform_import_grants`) || p.RecoverySetID != f.recoverySet ||
		p.CheckpointID != old.CheckpointID || p.ManifestSHA256 != old.ManifestSHA256 || p.NamespaceID != old.NamespaceID ||
		p.ReleaseSHA256 != old.ReleaseSHA256 || p.ArchiveBytes != old.ArchiveBytes || p.DataFormat != old.DataFormat || p.WriterFence != old.WriterFence ||
		p.SourceProjectID != old.ProjectID || p.SourceNamespaceInode != old.NamespaceInode || p.SourceDevice != old.Device ||
		!platformValidUUID(p.SourceFilesystemUUID) || p.SourceRootInode == "" || p.QuotaBytes == "" || p.QuotaInodes != "65536" {
		t.Fatalf("command payload does not carry the recovery set and the old root's provenance: %+v", p)
	}
	if len(intent.raw) == 0 || len(intent.signature) != ed25519.SignatureSize ||
		func() bool {
			_, err := control.VerifyImportCheckpoint(intent.raw, intent.signature, intent.authority, time.Now().UnixMilli())
			return err != nil
		}() {
		t.Fatal("command is not signed by the replacement runtime's control key")
	}
	if scalar[string](t, f.s.st, `SELECT kind||':'||state||':'||runtime_id||':'||payload_sha256 FROM app_platform_operation_effects WHERE operation_id=? AND stage='restore_data' AND effect_number=1`, commit.OperationID) !=
		"import-checkpoint:prepared:"+f.replacement.id+":"+c.PayloadSHA256 {
		t.Fatal("effect bookkeeping does not match the command")
	}
	// The runner verifies the command against the release descriptor it names, so the intent must
	// carry the very descriptor the Portal staged for that release.
	stored, err := platformReleaseDescriptor(migExec{s: f.s.st, ex: f.s.st.db}, p.ReleaseSHA256)
	if err != nil || len(intent.descriptor) == 0 || !bytes.Equal(intent.descriptor, stored) {
		t.Fatalf("intent does not carry the release descriptor: %v", err)
	}
	again, err := f.s.preparePlatformRestoreImport(context.Background(), lease)
	if err != nil || again.effectID != intent.effectID || again.command.PayloadSHA256 != c.PayloadSHA256 {
		t.Fatalf("a retry minted a new effect identity: %v", err)
	}
	if scalar[int](t, f.s.st, `SELECT COUNT(*) FROM app_platform_operation_effects WHERE operation_id=?`, commit.OperationID) != 1 {
		t.Fatal("retry duplicated the effect")
	}
}

func TestRestoreImportCommandCarriesProvenanceSQLite(t *testing.T) {
	testRestoreImportCommandCarriesProvenance(t, false)
}
func TestRestoreImportCommandCarriesProvenancePostgres(t *testing.T) {
	testRestoreImportCommandCarriesProvenance(t, true)
}

func testGrantConsumedAtomicallyWithReceipt(t *testing.T, pg bool) {
	f := platformRestoreImportFixture(t, pg)
	commit := f.reviewAndConsume(t)
	lease, err := f.drive(t, commit.OperationID)
	if err != nil || lease.snapshot.Stage != appplatform.StageComplete || f.executor.calls != 1 {
		t.Fatalf("import did not complete: %v calls=%d stage=%s", err, f.executor.calls, lease.snapshot.Stage)
	}
	var effectState, receipt string
	var effectUpdated, consumed int64
	if err := f.s.st.queryRow(`SELECT state,receipt_json,updated_ms FROM app_platform_operation_effects WHERE operation_id=? AND stage='restore_data'`, commit.OperationID).Scan(&effectState, &receipt, &effectUpdated); err != nil {
		t.Fatal(err)
	}
	if err := f.s.st.queryRow(`SELECT consumed_ms FROM app_platform_import_grants WHERE state='consumed'`).Scan(&consumed); err != nil {
		t.Fatal("grant not consumed:", err)
	}
	if effectState != "applied" || consumed != effectUpdated {
		t.Fatalf("receipt and grant were not settled together: %s %d %d", effectState, effectUpdated, consumed)
	}
	if scalar[string](t, f.s.st, `SELECT stage FROM app_platform_operations WHERE id=?`, commit.OperationID) != "complete" ||
		scalar[string](t, f.s.st, `SELECT phase FROM app_platform_installations WHERE id=?`, f.installation) != "review_required" ||
		scalar[string](t, f.s.st, `SELECT state FROM app_platform_recovery_sets WHERE id=?`, f.recoverySet) != "review_required" {
		t.Fatal("completing an import must not resume the installation or alter the recovery set")
	}
	// Replay: the identical receipt settles to the same result; a different one is refused.
	intent := f.executor.last
	same := platformImportObservationFixture(intent, platformRestoreEndpointFixture())
	var stored journal.ImportCheckpointReceipt
	if err := json.Unmarshal([]byte(receipt), &stored); err != nil {
		t.Fatal(err)
	}
	same.receipt = stored
	if err := f.s.replayPlatformRestoreImport(context.Background(), commit.OperationID, intent, platformRestoreEndpointFixture(), same); err != nil {
		t.Fatalf("identical receipt replay refused: %v", err)
	}
	other := same
	other.receipt.UsedBytes = "8192"
	if err := f.s.replayPlatformRestoreImport(context.Background(), commit.OperationID, intent, platformRestoreEndpointFixture(), other); !errors.Is(err, errAppPlatformStageEvidence) {
		t.Fatalf("a different receipt was accepted as a replay: %v", err)
	}
	if scalar[int](t, f.s.st, `SELECT COUNT(*) FROM app_platform_import_grants WHERE state='consumed'`) != 1 {
		t.Fatal("replay changed the grant")
	}
}

func TestGrantConsumedAtomicallyWithReceiptSQLite(t *testing.T) {
	testGrantConsumedAtomicallyWithReceipt(t, false)
}
func TestGrantConsumedAtomicallyWithReceiptPostgres(t *testing.T) {
	testGrantConsumedAtomicallyWithReceipt(t, true)
}

// A failure at the very end of the commit must leave neither half behind: no applied receipt with an
// unconsumed grant, and no consumed grant without a receipt.
func TestGrantConsumedAtomicallyWithReceiptRollsBackTogether(t *testing.T) {
	f := platformRestoreImportFixture(t, false)
	commit := f.reviewAndConsume(t)
	lease, err := f.s.st.claimPlatformOperation(commit.OperationID, platformFixtureID(5300))
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if lease, err = f.s.runPlatformRestoreImport(context.Background(), lease, platformRestoreEndpointFixture()); err != nil {
			t.Fatal(err)
		}
	}
	mustExec(t, f.s.st, `CREATE TRIGGER fail_import_complete BEFORE UPDATE ON app_platform_operations
		WHEN NEW.stage='complete' BEGIN SELECT RAISE(ABORT,'simulated failure at the final write'); END`)
	if _, err := f.s.runPlatformRestoreImport(context.Background(), lease, platformRestoreEndpointFixture()); err == nil {
		t.Fatal("the simulated failure did not surface")
	}
	if f.executor.calls != 1 {
		t.Fatalf("runner calls = %d", f.executor.calls)
	}
	if scalar[string](t, f.s.st, `SELECT state FROM app_platform_operation_effects WHERE operation_id=? AND stage='restore_data'`, commit.OperationID) != "prepared" ||
		scalar[string](t, f.s.st, `SELECT state FROM app_platform_import_grants`) != "issued" ||
		scalar[string](t, f.s.st, `SELECT stage FROM app_platform_operations WHERE id=?`, commit.OperationID) != "restore_data" {
		t.Fatal("a partial commit survived the failure")
	}
	// The same lease retries the same effect and then settles both halves together.
	mustExec(t, f.s.st, `DROP TRIGGER fail_import_complete`)
	firstEffect := scalar[string](t, f.s.st, `SELECT effect_id FROM app_platform_operation_effects WHERE operation_id=?`, commit.OperationID)
	if _, err := f.s.runPlatformRestoreImport(context.Background(), lease, platformRestoreEndpointFixture()); err != nil {
		t.Fatal(err)
	}
	if scalar[string](t, f.s.st, `SELECT effect_id FROM app_platform_operation_effects WHERE operation_id=?`, commit.OperationID) != firstEffect ||
		f.executor.calls != 2 || f.executor.last.effectID != firstEffect {
		t.Fatal("the retry did not reuse the effect identity")
	}
	if scalar[string](t, f.s.st, `SELECT state FROM app_platform_import_grants`) != "consumed" ||
		scalar[string](t, f.s.st, `SELECT stage FROM app_platform_operations WHERE id=?`, commit.OperationID) != "complete" {
		t.Fatal("the retry did not settle")
	}
}

func TestRestoreImportRefusesAChangedReceipt(t *testing.T) {
	for name, mutate := range map[string]func(*journal.ImportCheckpointReceipt){
		"other manifest":   func(r *journal.ImportCheckpointReceipt) { r.ManifestSHA256 = strings.Repeat("c", 64) },
		"other checkpoint": func(r *journal.ImportCheckpointReceipt) { r.CheckpointID = platformFixtureID(1) },
		"other grant":      func(r *journal.ImportCheckpointReceipt) { r.GrantID = platformFixtureID(2) },
		"not imported":     func(r *journal.ImportCheckpointReceipt) { r.State = "restored" },
		"other effect":     func(r *journal.ImportCheckpointReceipt) { r.EffectID = platformFixtureID(3) },
		"other bytes":      func(r *journal.ImportCheckpointReceipt) { r.ArchiveBytes = "1" },
	} {
		t.Run(name, func(t *testing.T) {
			f := platformRestoreImportFixture(t, false)
			commit := f.reviewAndConsume(t)
			f.executor.mutate = mutate
			if _, err := f.drive(t, commit.OperationID); !errors.Is(err, errAppPlatformStageEvidence) {
				t.Fatalf("a receipt with %s settled the import: %v", name, err)
			}
			if scalar[string](t, f.s.st, `SELECT state FROM app_platform_import_grants`) != "issued" ||
				scalar[string](t, f.s.st, `SELECT state FROM app_platform_operation_effects WHERE operation_id=?`, commit.OperationID) != "prepared" {
				t.Fatal("an unacceptable receipt changed grant or effect state")
			}
		})
	}
}

func TestRestoreImportFinishForReview(t *testing.T) {
	t.Run("never dispatched", func(t *testing.T) {
		f := platformRestoreImportFixture(t, false)
		commit := f.reviewAndConsume(t)
		lease, err := f.s.st.claimPlatformOperation(commit.OperationID, platformFixtureID(5300))
		if err != nil {
			t.Fatal(err)
		}
		if err := f.s.finishPlatformRestoreImportForReview(context.Background(), lease); err != nil {
			t.Fatal(err)
		}
		if scalar[string](t, f.s.st, `SELECT stage FROM app_platform_operations WHERE id=?`, commit.OperationID) != "failed" ||
			scalar[string](t, f.s.st, `SELECT state FROM app_platform_import_grants`) != "revoked" {
			t.Fatal("an operation that never reached the runner must fail and give up its grant")
		}
	})
	t.Run("dispatched", func(t *testing.T) {
		f := platformRestoreImportFixture(t, false)
		commit := f.reviewAndConsume(t)
		f.executor.err = errors.New("runner unreachable")
		lease, err := f.drive(t, commit.OperationID)
		if err == nil {
			t.Fatal("unreachable runner did not surface")
		}
		if err := f.s.finishPlatformRestoreImportForReview(context.Background(), lease); err != nil {
			t.Fatal(err)
		}
		if scalar[string](t, f.s.st, `SELECT stage FROM app_platform_operations WHERE id=?`, commit.OperationID) != "recovery_required" ||
			scalar[string](t, f.s.st, `SELECT state FROM app_platform_import_grants`) != "issued" {
			t.Fatal("an effect that may have applied must wait for review with its grant intact")
		}
	})
}

func TestRestoreImportSchedulerDrivesOperation(t *testing.T) {
	f := platformRestoreImportFixture(t, false)
	commit := f.reviewAndConsume(t)
	worker, err := newPlatformWritableInstallScheduler(f.s, platformRestoreEndpointFixture(), platformFixtureID(5301), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 8 && scalar[string](t, f.s.st, `SELECT stage FROM app_platform_operations WHERE id=?`, commit.OperationID) != "complete"; i++ {
		if err := worker.tick(context.Background()); err != nil {
			t.Fatal(i, err)
		}
	}
	if scalar[string](t, f.s.st, `SELECT stage FROM app_platform_operations WHERE id=?`, commit.OperationID) != "complete" ||
		scalar[string](t, f.s.st, `SELECT state FROM app_platform_import_grants`) != "consumed" {
		t.Fatal("the scheduler did not carry a paused Portal's restore import to completion")
	}
}

// Step 12: a restore must not leave an issued grant usable against the restored database.
func testRestoreRevokesIssuedImportGrants(t *testing.T, pg bool) {
	f := platformRestoreImportFixture(t, pg)
	f.reviewAndConsume(t)
	mustExec(t, f.s.st, `INSERT INTO app_platform_import_grants(id,recovery_set_id,runtime_id,controller_epoch,state,consumed_ms,created_ms,updated_ms) VALUES(?,?,?,?,'consumed',7,1,1)`,
		platformFixtureID(5500), f.recoverySet, f.oldRuntime, platformFixtureID(5502))
	platformSimulateRestore(t, f.s)
	if scalar[string](t, f.s.st, `SELECT state FROM app_platform_import_grants WHERE runtime_id=?`, f.replacement.id) != "revoked" ||
		scalar[int](t, f.s.st, `SELECT COUNT(*) FROM app_platform_import_grants WHERE state='issued'`) != 0 {
		t.Fatal("restore left an issued grant")
	}
	if scalar[string](t, f.s.st, `SELECT state FROM app_platform_import_grants WHERE id=?`, platformFixtureID(5500)) != "consumed" ||
		scalar[int64](t, f.s.st, `SELECT consumed_ms FROM app_platform_import_grants WHERE id=?`, platformFixtureID(5500)) != 7 {
		t.Fatal("restore rewrote a consumed grant")
	}
}

func TestRestoreRevokesIssuedImportGrantsSQLite(t *testing.T) {
	testRestoreRevokesIssuedImportGrants(t, false)
}
func TestRestoreRevokesIssuedImportGrantsPostgres(t *testing.T) {
	testRestoreRevokesIssuedImportGrants(t, true)
}

// A revoked grant is history, not a reservation: it neither blocks a new review for the same recovery
// set and runtime nor is ever mistaken for the live grant, and the new grant is still single-use.
func testRevokedImportGrantAllowsRetry(t *testing.T, pg bool) {
	f := platformRestoreImportFixture(t, pg)
	epoch := f.s.st.GetSetting("app_platform_controller_epoch", "")
	revoked := platformFixtureID(5400)
	mustExec(t, f.s.st, `INSERT INTO app_platform_import_grants(id,recovery_set_id,runtime_id,controller_epoch,state,consumed_ms,created_ms,updated_ms) VALUES(?,?,?,?,'revoked',NULL,1,1)`,
		revoked, f.recoverySet, f.replacement.id, epoch)
	commit := f.reviewAndConsume(t)
	if scalar[int](t, f.s.st, `SELECT COUNT(*) FROM app_platform_import_grants WHERE recovery_set_id=? AND runtime_id=?`, f.recoverySet, f.replacement.id) != 2 ||
		scalar[int](t, f.s.st, `SELECT COUNT(*) FROM app_platform_import_grants WHERE state='issued'`) != 1 {
		t.Fatal("the retry did not issue exactly one new grant beside the revoked one")
	}
	lease, err := f.drive(t, commit.OperationID)
	if err != nil || lease.snapshot.Stage != appplatform.StageComplete || f.executor.calls != 1 {
		t.Fatalf("the retried import did not complete: %v calls=%d", err, f.executor.calls)
	}
	if f.executor.last.command.Payload.GrantID == revoked ||
		scalar[string](t, f.s.st, `SELECT state FROM app_platform_import_grants WHERE id=?`, revoked) != "revoked" ||
		scalar[int](t, f.s.st, `SELECT COUNT(*) FROM app_platform_import_grants WHERE state='consumed'`) != 1 {
		t.Fatal("the revoked grant was used or altered, or the new grant was not consumed")
	}
	// The live grant remains unique: a second issued grant for the same recovery set is refused.
	if _, err := f.s.st.db.Exec(f.s.st.bind(`INSERT INTO app_platform_import_grants(id,recovery_set_id,runtime_id,controller_epoch,state,consumed_ms,created_ms,updated_ms) VALUES(?,?,?,?,'issued',NULL,1,1)`),
		platformFixtureID(5401), f.recoverySet, f.replacement.id, epoch); err == nil {
		t.Fatal("two live grants coexisted for one recovery set and runtime")
	}
}

func TestRevokedImportGrantAllowsRetry(t *testing.T) { testRevokedImportGrantAllowsRetry(t, false) }
func TestRevokedImportGrantAllowsRetryPostgres(t *testing.T) {
	testRevokedImportGrantAllowsRetry(t, true)
}

// A failed import leaves the installation already pointed at the replacement runtime. Retrying must not
// need the old binding as a source, and must still refuse once a grant has been consumed.
func testRestoreImportRetryAfterFailedImport(t *testing.T, pg bool) {
	f := platformRestoreImportFixture(t, pg)
	first := f.reviewAndConsume(t)
	lease, err := f.s.st.claimPlatformOperation(first.OperationID, platformFixtureID(5300))
	if err != nil {
		t.Fatal(err)
	}
	if err := f.s.finishPlatformRestoreImportForReview(context.Background(), lease); err != nil {
		t.Fatal(err)
	}
	if scalar[string](t, f.s.st, `SELECT runtime_id FROM app_platform_installations WHERE id=?`, f.installation) != f.replacement.id ||
		scalar[string](t, f.s.st, `SELECT state FROM app_platform_import_grants`) != "revoked" {
		t.Fatal("fixture: the failed import must leave the installation on the replacement with a revoked grant")
	}
	review, err := f.s.createPlatformRestoreImportReview(context.Background(), f.actor, f.installation, f.recoverySet)
	if err != nil {
		t.Fatal("retry review refused:", err)
	}
	var plan platformRestoreImportPlan
	if json.Unmarshal([]byte(review.JSON), &plan) != nil || plan.SourceRuntimeID != f.replacement.id || plan.RuntimeID != f.replacement.id {
		t.Fatalf("retry plan: %+v", plan)
	}
	retry, err := f.s.consumePlatformRestoreImportReview(context.Background(), f.actor, review.ID, review.SHA256, platformFixtureID(5210))
	if err != nil {
		t.Fatal("retry consume refused:", err)
	}
	done, err := f.drive(t, retry.OperationID)
	if err != nil || done.snapshot.Stage != appplatform.StageComplete || f.executor.calls != 1 {
		t.Fatalf("the retried import did not complete: %v calls=%d", err, f.executor.calls)
	}
	if scalar[int](t, f.s.st, `SELECT COUNT(*) FROM app_platform_import_grants WHERE state='consumed'`) != 1 {
		t.Fatal("the retry did not consume exactly one grant")
	}
	// With a consumed grant on the replacement the installation has been imported: no third review.
	if _, err := f.s.createPlatformRestoreImportReview(context.Background(), f.actor, f.installation, f.recoverySet); err == nil {
		t.Fatal("a review was admitted for an installation whose grant is consumed")
	}
}

func TestRestoreImportRetryAfterFailedImport(t *testing.T) {
	testRestoreImportRetryAfterFailedImport(t, false)
}
func TestRestoreImportRetryAfterFailedImportPostgres(t *testing.T) {
	testRestoreImportRetryAfterFailedImport(t, true)
}
