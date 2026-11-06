package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/journal"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/transport"
)

// Synthetic native observation for repository transaction tests only. This
// constructs the published ownership tuple; it is not native/transport evidence.
func platformDataObservationFixture(t *testing.T, intent platformDataIntent, endpoint platformRuntimeEndpoint) platformDataObservation {
	t.Helper()
	c := intent.command
	owner := fmt.Sprintf(`{"format":"portal.runner.data-ownership/1-dev.1","deployment_id":%q,"controller_epoch":%q,"control_revision":%q,"installation_id":%q,"namespace_id":%q,"native_id":"xfs-project/v1/1","effect_id":%q,"payload_sha256":%q,"generation":%q,"admitted_fence":%q,"release_sha256":%q,"data_format":%q,"quota_bytes":%q,"quota_inodes":%q,"writer_fence":%q}`, c.DeploymentID, c.ControllerEpoch, c.ControlRevision, c.InstallationID, c.Payload.DataNamespaceID, c.EffectID, c.PayloadSHA256, c.TargetGeneration, c.InstallationFence, c.Payload.ReleaseSHA256, c.Payload.DataFormat, c.Payload.QuotaBytes, c.Payload.QuotaInodes, c.Payload.WriterFence)
	digest := sha256.Sum256([]byte(owner))
	receipt := journal.DataPreparationReceipt{Format: "portal.runner.prepare-data-receipt/1-dev.1", DeploymentID: c.DeploymentID, ControllerEpoch: c.ControllerEpoch, ControlRevision: c.ControlRevision, InstallationID: c.InstallationID, PayloadSHA256: c.PayloadSHA256, ReleaseSHA256: c.Payload.ReleaseSHA256, DataFormat: c.Payload.DataFormat, Generation: c.TargetGeneration, WriterFence: c.Payload.WriterFence, ProjectID: "1", NamespaceInode: "11", Device: "7", QuotaBytes: c.Payload.QuotaBytes, QuotaInodes: c.Payload.QuotaInodes, UsedBytes: "0", UsedInodes: "1", ObservedMS: strconv.FormatInt(time.Now().UnixMilli(), 10)}
	receipt.Claim.Format = "portal.runner.prepare-data-claim/1-dev.1"
	receipt.Claim.ClaimID = platformFixtureID(998)
	receipt.Claim.EffectID = c.EffectID
	receipt.Claim.ResourceID = c.Payload.DataNamespaceID
	receipt.Claim.OwnershipSHA256 = hex.EncodeToString(digest[:])
	receipt.Claim.AdmittedFence = c.InstallationFence
	receipt.Claim.ClaimedMS = c.IssuedMS
	receipt.Claim.ExpiresMS = c.ExpiresMS
	receipt.Claim.RootFilesystemUUID = platformFixtureID(999)
	receipt.Claim.RootInode = "10"
	if !receipt.MatchesCommand(c) {
		t.Fatal("synthetic ownership fixture disagrees with shared contract")
	}
	return platformDataObservation{receipt: receipt, authority: intent.authority, endpoint: transport.DataReceiptEndpoint{Path: endpoint.path, RunnerUID: 0, ControllerUID: endpoint.controllerUID, ControllerGID: endpoint.controllerGID, RootController: true}, receivedAt: time.Now(), requestSHA: strings.Repeat("a", 64), commandSHA: platformDigest(intent.raw)}
}
func testPlatformDataStage(t *testing.T, pg bool) {
	s, _, lease := platformDataIntentFixture(t, pg)
	intent, err := s.preparePlatformInstallData(context.Background(), lease)
	if err != nil {
		t.Fatal(err)
	}
	endpoint := platformRuntimeEndpoint{path: "/run/portal-apps/runner.sock", controllerUID: 41001, controllerGID: 41001}
	proof := platformDataObservationFixture(t, intent, endpoint)
	next, err := s.commitPlatformObservedData(context.Background(), lease, intent, endpoint, proof)
	if err != nil {
		t.Fatal(err)
	}
	if next.snapshot.Stage != appplatform.StageActivate || next.snapshot.Revision != lease.snapshot.Revision+1 || next.rowRevision != lease.rowRevision+1 || next.fence != lease.fence || next.owner != lease.owner || next.leaseUntilMS != lease.leaseUntilMS {
		t.Fatal("data completion lost lease/model tuple")
	}
	if scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_data_namespaces WHERE state='active' AND revision=2 AND data_format>0 AND writer_fence=1`) != 1 || scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_operation_effects WHERE state='applied' AND observed_fence=admitted_fence AND receipt_json<>'{}'`) != 1 {
		t.Fatal("data/effect not committed together")
	}
	if scalar[string](t, s.st, `SELECT phase FROM app_platform_installations`) != "pending" || scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_activations`)+scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_access_grants`) != 0 {
		t.Fatal("data receipt activated application")
	}
	before := dumpOf(t, s.st)
	if _, err := s.commitPlatformObservedData(context.Background(), lease, intent, endpoint, proof); err == nil {
		t.Fatal("stale prepare stage committed twice")
	}
	if !bytes.Equal(stripHeader(before), stripHeader(dumpOf(t, s.st))) {
		t.Fatal("stale stage retry changed state")
	}
}
func TestPlatformDataStageSQLite(t *testing.T)   { testPlatformDataStage(t, false) }
func TestPlatformDataStagePostgres(t *testing.T) { testPlatformDataStage(t, true) }
func testPlatformDataStageRefusals(t *testing.T, pg bool) {
	for _, kind := range []string{"zero", "old_proof", "future_proof", "private_profile", "endpoint", "controller_uid", "root_uid", "authority", "command_digest", "request_digest", "physical_quota", "effect", "namespace", "runtime_disabled", "sealed_key", "sponsor", "lease", "pause"} {
		t.Run(kind, func(t *testing.T) {
			s, _, lease := platformDataIntentFixture(t, pg)
			intent, err := s.preparePlatformInstallData(context.Background(), lease)
			if err != nil {
				t.Fatal(err)
			}
			endpoint := platformRuntimeEndpoint{path: "/run/portal-apps/runner.sock", controllerUID: 41001, controllerGID: 41001}
			proof := platformDataObservationFixture(t, intent, endpoint)
			switch kind {
			case "zero":
				proof = platformDataObservation{}
			case "old_proof":
				proof.receivedAt = time.Now().Add(-4 * time.Second)
			case "future_proof":
				proof.receivedAt = time.Now().Add(time.Second)
			case "private_profile":
				proof.endpoint.RootController = false
			case "endpoint":
				proof.endpoint.Path += "-other"
			case "controller_uid":
				proof.endpoint.ControllerUID++
			case "root_uid":
				proof.endpoint.RunnerUID = 1000
			case "authority":
				proof.authority.Fingerprint = strings.Repeat("b", 64)
			case "command_digest":
				proof.commandSHA = strings.Repeat("b", 64)
			case "request_digest":
				proof.requestSHA = ""
			case "physical_quota":
				proof.receipt.QuotaBytes = "512"
			case "effect":
				mustExec(t, s.st, `UPDATE app_platform_operation_effects SET observed_fence=1`)
			case "namespace":
				mustExec(t, s.st, `UPDATE app_platform_data_namespaces SET writer_fence=1`)
			case "runtime_disabled":
				mustExec(t, s.st, `UPDATE app_platform_runtime_bindings SET enabled=0`)
			case "sealed_key":
				mustExec(t, s.st, `UPDATE app_platform_runtime_bindings SET enabled=0,sealed_control_key=NULL`)
			case "sponsor":
				mustExec(t, s.st, `UPDATE users SET active=0 WHERE username=(SELECT username FROM app_platform_review_plans)`)
			case "lease":
				mustExec(t, s.st, `UPDATE app_platform_operations SET lease_until_ms=0`)
			case "pause":
				mustExec(t, s.st, `UPDATE meta SET v='1' WHERE k='app_platform_restore_paused'`)
			}
			before := dumpOf(t, s.st)
			if _, err := s.commitPlatformObservedData(context.Background(), lease, intent, endpoint, proof); err == nil {
				t.Fatal("invalid receipt stage committed", kind)
			}
			if !bytes.Equal(stripHeader(before), stripHeader(dumpOf(t, s.st))) {
				t.Fatal("refused stage partly committed", kind)
			}
		})
	}
}
func TestPlatformDataStageRefusalsSQLite(t *testing.T)   { testPlatformDataStageRefusals(t, false) }
func TestPlatformDataStageRefusalsPostgres(t *testing.T) { testPlatformDataStageRefusals(t, true) }
func testPlatformDataStageRollback(t *testing.T, pg bool) {
	for _, table := range []string{"app_platform_operation_effects", "app_platform_operations"} {
		t.Run(table, func(t *testing.T) {
			s, _, lease := platformDataIntentFixture(t, pg)
			intent, err := s.preparePlatformInstallData(context.Background(), lease)
			if err != nil {
				t.Fatal(err)
			}
			endpoint := platformRuntimeEndpoint{path: "/run/portal-apps/runner.sock", controllerUID: 41001, controllerGID: 41001}
			proof := platformDataObservationFixture(t, intent, endpoint)
			when := ""
			if table == "app_platform_operations" {
				when = " WHEN NEW.stage='activate'"
			}
			if pg {
				mustExec(t, s.st, `CREATE FUNCTION fixture_data_stage_failure() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'fixture data write failure';END $$`)
				condition := "NEW.state='applied'"
				if table == "app_platform_operations" {
					condition = "NEW.stage='activate'"
				}
				mustExec(t, s.st, `CREATE TRIGGER fixture_data_failure BEFORE UPDATE ON `+table+` FOR EACH ROW WHEN (`+condition+`) EXECUTE FUNCTION fixture_data_stage_failure()`)
				t.Cleanup(func() { mustExec(t, s.st, `DROP FUNCTION IF EXISTS fixture_data_stage_failure() CASCADE`) })
			} else {
				mustExec(t, s.st, `CREATE TRIGGER fixture_data_failure BEFORE UPDATE ON `+table+when+` BEGIN SELECT RAISE(ABORT,'fixture data write failure');END`)
			}
			if _, err := s.commitPlatformObservedData(context.Background(), lease, intent, endpoint, proof); err == nil {
				t.Fatal("injected failure ignored")
			}
			if scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_data_namespaces WHERE state='review_required' AND revision=1`) != 1 || scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_operation_effects WHERE state='prepared' AND receipt_json='{}'`) != 1 || scalar[string](t, s.st, `SELECT stage FROM app_platform_operations`) != "prepare_data" {
				t.Fatal("failure partly committed data/effect/stage")
			}
			if pg {
				mustExec(t, s.st, `DROP TRIGGER fixture_data_failure ON `+table)
			} else {
				mustExec(t, s.st, `DROP TRIGGER fixture_data_failure`)
			}
			proof = platformDataObservationFixture(t, intent, endpoint)
			if _, err := s.commitPlatformObservedData(context.Background(), lease, intent, endpoint, proof); err != nil {
				t.Fatal("retry after rollback", err)
			}
		})
	}
}

func TestPlatformDataStageRollbackSQLite(t *testing.T)   { testPlatformDataStageRollback(t, false) }
func TestPlatformDataStageRollbackPostgres(t *testing.T) { testPlatformDataStageRollback(t, true) }

func TestPlatformDataStageRequiresAuthenticatedEndpoint(t *testing.T) {
	s, _, lease := platformDataIntentFixture(t, false)
	intent, err := s.preparePlatformInstallData(context.Background(), lease)
	if err != nil {
		t.Fatal(err)
	}
	// A user-owned local directory cannot stand in for the provisioned root endpoint.
	endpoint := platformRuntimeEndpoint{path: filepath.Join(t.TempDir(), "runner.sock"), controllerUID: uint32(os.Geteuid()), controllerGID: uint32(os.Getgid())}
	before := dumpOf(t, s.st)
	if _, err := s.completePlatformInstallData(context.Background(), lease, intent, endpoint); err == nil {
		t.Fatal("missing authenticated endpoint completed data")
	}
	if !bytes.Equal(stripHeader(before), stripHeader(dumpOf(t, s.st))) {
		t.Fatal("endpoint failure committed stage")
	}
}

func TestPlatformDataStageLateAuditRefusalPostgres(t *testing.T) {
	for _, kind := range []string{"expired_proof", "canceled_context"} {
		t.Run(kind, func(t *testing.T) {
			s, _, lease := platformDataIntentFixture(t, true)
			intent, err := s.preparePlatformInstallData(context.Background(), lease)
			if err != nil {
				t.Fatal(err)
			}
			endpoint := platformRuntimeEndpoint{path: "/run/portal-apps/runner.sock", controllerUID: 41001, controllerGID: 41001}
			proof := platformDataObservationFixture(t, intent, endpoint)
			delay := "3.2"
			if kind == "canceled_context" {
				delay = "0.5"
			}
			mustExec(t, s.st, `CREATE FUNCTION fixture_slow_data_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN PERFORM pg_sleep(`+delay+`);RETURN NEW;END $$`)
			mustExec(t, s.st, `CREATE TRIGGER fixture_slow_data_audit BEFORE INSERT ON audit_log FOR EACH ROW WHEN (NEW.action='app_platform.operation_stage') EXECUTE FUNCTION fixture_slow_data_audit()`)
			t.Cleanup(func() {
				mustExec(t, s.st, `DROP TRIGGER IF EXISTS fixture_slow_data_audit ON audit_log`)
				mustExec(t, s.st, `DROP FUNCTION IF EXISTS fixture_slow_data_audit()`)
			})
			ctx := context.Background()
			cancel := func() {}
			if kind == "canceled_context" {
				ctx, cancel = context.WithTimeout(ctx, 100*time.Millisecond)
			}
			_, err = s.commitPlatformObservedData(ctx, lease, intent, endpoint, proof)
			cancel()
			if err == nil {
				t.Fatal("late audit accepted stale/canceled authority", kind)
			}
			if kind == "canceled_context" && !errors.Is(err, context.DeadlineExceeded) {
				t.Fatal("audit query ignored context", err)
			}
			if scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_data_namespaces WHERE state='review_required' AND revision=1`) != 1 || scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_operation_effects WHERE state='prepared' AND receipt_json='{}'`) != 1 || scalar[string](t, s.st, `SELECT stage FROM app_platform_operations`) != "prepare_data" {
				t.Fatal("late refusal partly committed")
			}
		})
	}
}
