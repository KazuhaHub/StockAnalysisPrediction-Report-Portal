package app

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/journal"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/transport"
)

// Seeds canonical repository history only. Neither JSON nor these SQL rows can
// mint an opaque root observation, execute a backend or prove actual readiness.
func platformBackendDeliveryFixture(t *testing.T, pg bool) (*Server, platformOperationLease) {
	t.Helper()
	s, lease := platformCandidateIntentFixture(t, pg)
	intent, err := s.preparePlatformInstallCandidate(context.Background(), lease)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile("../apprunner/transport/testdata/candidate-receipt.json")
	if err != nil {
		t.Fatal(err)
	}
	r, err := journal.ParseCandidateRunningReceipt(raw)
	if err != nil {
		t.Fatal(err)
	}
	c, d := intent.command, intent.data
	now := time.Now().UnixMilli()
	r.DeploymentID, r.ControllerEpoch, r.ControlRevision, r.InstallationID, r.OperationID = c.DeploymentID, c.ControllerEpoch, c.ControlRevision, c.InstallationID, c.OperationID
	r.Generation, r.WriterFence, r.PayloadSHA256, r.ReleaseSHA256, r.DataNamespaceID, r.DataFormat = c.TargetGeneration, c.Payload.WriterFence, c.PayloadSHA256, c.Payload.ReleaseSHA256, c.Payload.DataNamespaceID, c.Payload.DataFormat
	r.Claim.ClaimID, r.Claim.EffectID, r.Claim.ResourceID, r.Claim.DataReceiptSHA256, r.Claim.AdmittedFence = platformFixtureID(9991), c.EffectID, c.Payload.ResourceID, intent.dataSHA, c.InstallationFence
	r.Claim.ClaimedMS, r.Claim.PreflightBoundMS, r.NativeObservedMS, r.SettledMS, r.Claim.ExpiresMS = strconv.FormatInt(now-4, 10), strconv.FormatInt(now-3, 10), strconv.FormatInt(now-2, 10), strconv.FormatInt(now-1, 10), strconv.FormatInt(now+29000, 10)
	p := r.Claim.Preflight
	p.Platform, p.RootFilesystemUUID, p.RootInode, p.ProjectID, p.Device, p.NamespaceInode, p.QuotaBytes, p.QuotaInodes = c.Payload.Platform, d.Claim.RootFilesystemUUID, d.Claim.RootInode, d.ProjectID, d.Device, d.NamespaceInode, d.QuotaBytes, d.QuotaInodes
	ownership := struct{ Format, Deployment, Epoch, Revision, Effect, Resource, Namespace, Release, Generation, Fence, Writer, Payload, DataReceipt string }{"portal.runner.candidate-ownership/1-dev.1", c.DeploymentID, c.ControllerEpoch, c.ControlRevision, c.EffectID, c.Payload.ResourceID, c.Payload.DataNamespaceID, c.Payload.ReleaseSHA256, c.TargetGeneration, c.InstallationFence, c.Payload.WriterFence, c.PayloadSHA256, intent.dataSHA}
	encoded, _ := json.Marshal(ownership)
	r.Claim.OwnershipSHA256 = platformDigest(encoded)
	if !r.MatchesCommand(c) {
		t.Fatal("synthetic candidate contract disagrees")
	}
	encoded, _ = json.Marshal(r)
	readiness := journal.CandidateReadiness{Format: "portal.runner.candidate-readiness/1-dev.1", PayloadSHA256: c.PayloadSHA256, RunningReceiptSHA256: platformDigest(encoded), NativeID: r.NativeID, Protocol: "portal.backend/1", DataFormat: c.Payload.DataFormat, ObservedMS: strconv.FormatInt(now, 10)}
	health, _ := json.Marshal(readiness)
	mustExec(t, s.st, `UPDATE app_platform_operation_effects SET state='applied',observed_fence=admitted_fence,receipt_json=?,updated_ms=? WHERE operation_id=? AND stage='start'`, string(encoded), now, lease.id)
	mustExec(t, s.st, `UPDATE app_platform_activations SET readiness_evidence=?,observed_ms=?,updated_ms=? WHERE id=?`, string(health), now, now, intent.resourceID)
	model, err := appplatform.OperationFromSnapshot(lease.snapshot)
	if err != nil {
		t.Fatal(err)
	}
	model, err = model.Advance(lease.snapshot.Revision, appplatform.StageProbe)
	if err != nil {
		t.Fatal(err)
	}
	model, err = model.Advance(model.Snapshot().Revision, appplatform.StageActivate)
	if err != nil {
		t.Fatal(err)
	}
	lease.snapshot = model.Snapshot()
	lease.rowRevision += 2
	mustExec(t, s.st, `UPDATE app_platform_operations SET stage='activate',state_revision=?,row_revision=?,updated_ms=? WHERE id=?`, int64(lease.snapshot.Revision), lease.rowRevision, now, lease.id)
	return s, lease
}
func stalePlatformDeliveryHealth(t *testing.T, s *Server, lease platformOperationLease) {
	t.Helper()
	var r journal.CandidateReadiness
	encoded := scalar[string](t, s.st, `SELECT readiness_evidence FROM app_platform_activations WHERE installation_id=?`, lease.installationID)
	if err := json.Unmarshal([]byte(encoded), &r); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-4 * time.Second).UnixMilli()
	r.ObservedMS = strconv.FormatInt(old, 10)
	raw, _ := json.Marshal(r)
	mustExec(t, s.st, `UPDATE app_platform_activations SET readiness_evidence=?,observed_ms=? WHERE installation_id=?`, string(raw), old, lease.installationID)
}
func testPlatformBackendDeliveryIntent(t *testing.T, pg bool) {
	s, lease := platformBackendDeliveryFixture(t, pg)
	intent, err := s.preparePlatformBackendDelivery(context.Background(), lease)
	if err != nil {
		t.Fatal(err)
	}
	defer intent.registration.Close()
	v, err := control.VerifyBackendRegistration(intent.raw, intent.signature, intent.candidate.authority, time.Now().UnixMilli())
	if err != nil || v.Command().PayloadSHA256 != intent.command.PayloadSHA256 {
		t.Fatal("signed Portal delivery", err)
	}
	document, err := intent.registration.Document()
	if err != nil {
		t.Fatal(err)
	}
	defer clear(document)
	parsed, err := control.ValidateBackendRegistrationDocument(v, intent.candidate.descriptor, document)
	if err != nil {
		t.Fatal(err)
	}
	parsed.Close()
	if intent.applied || scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_operation_effects WHERE operation_id=? AND stage='activate' AND kind='activate' AND state='prepared' AND observed_fence=0 AND receipt_json='{}'`, lease.id) != 1 {
		t.Fatal("intent fabricated delivery")
	}
	again, err := s.preparePlatformBackendDelivery(context.Background(), lease)
	if err != nil {
		t.Fatal(err)
	}
	defer again.registration.Close()
	if again.command.EffectID != intent.command.EffectID || again.command.PayloadSHA256 != intent.command.PayloadSHA256 || again.command.DocumentSHA256 != intent.command.DocumentSHA256 {
		t.Fatal("renewal replaced logical effect")
	}
	if scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_operation_effects WHERE operation_id=?`, lease.id) != 3 || scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_access_grants WHERE installation_id=?`, lease.installationID) != 0 {
		t.Fatal("delivery granted access or duplicated effect")
	}
	before := dumpOf(t, s.st)
	if _, err := s.commitPlatformBackendDelivery(context.Background(), lease, intent, platformRuntimeEndpoint{}, transport.BackendRegistrationObservation{}); err == nil {
		t.Fatal("zero observation applied delivery")
	}
	if !bytes.Equal(stripHeader(before), stripHeader(dumpOf(t, s.st))) {
		t.Fatal("zero observation changed state")
	}
	stalePlatformDeliveryHealth(t, s, lease)
	resumed, err := s.preparePlatformBackendDelivery(context.Background(), lease)
	if err != nil {
		t.Fatal("old health blocked existing intent reconciliation", err)
	}
	defer resumed.registration.Close()
	if resumed.command.PayloadSHA256 != intent.command.PayloadSHA256 {
		t.Fatal("reconciliation changed digest")
	}
	if r, err := s.preparePlatformBackendRegistration(context.Background(), lease); err == nil {
		r.Close()
		t.Fatal("old preparation path accepted a third effect")
	}
}
func TestPlatformBackendDeliveryIntentSQLite(t *testing.T) {
	testPlatformBackendDeliveryIntent(t, false)
}
func TestPlatformBackendDeliveryIntentPostgres(t *testing.T) {
	testPlatformBackendDeliveryIntent(t, true)
}
func testPlatformBackendDeliveryRefusals(t *testing.T, pg bool) {
	for _, mode := range []string{"fresh_stale", "runtime", "identity", "identity_changed", "pause", "digest", "partial", "candidate", "unexpected"} {
		t.Run(mode, func(t *testing.T) {
			s, lease := platformBackendDeliveryFixture(t, pg)
			if mode == "fresh_stale" {
				stalePlatformDeliveryHealth(t, s, lease)
			} else {
				intent, err := s.preparePlatformBackendDelivery(context.Background(), lease)
				if err != nil {
					t.Fatal(err)
				}
				intent.registration.Close()
			}
			switch mode {
			case "runtime":
				mustExec(t, s.st, `UPDATE app_platform_runtime_bindings SET enabled=0`)
			case "identity":
				mustExec(t, s.st, `UPDATE app_platform_installations SET sealed_identity_seed='broken' WHERE id=?`, lease.installationID)
			case "identity_changed":
				dek, err := s.dek()
				if err != nil {
					t.Fatal(err)
				}
				seed := bytes.Repeat([]byte{95}, 32)
				defer clear(seed)
				box, err := aesSeal(dek, seed, aad("app-platform", lease.installationID))
				if err != nil {
					t.Fatal(err)
				}
				mustExec(t, s.st, `UPDATE app_platform_installations SET sealed_identity_seed=? WHERE id=?`, sealPrefix+box, lease.installationID)
			case "pause":
				mustExec(t, s.st, `UPDATE meta SET v='1' WHERE k='app_platform_restore_paused'`)
			case "digest":
				mustExec(t, s.st, `UPDATE app_platform_operation_effects SET payload_sha256=? WHERE operation_id=? AND stage='activate'`, strings.Repeat("f", 64), lease.id)
			case "partial":
				mustExec(t, s.st, `UPDATE app_platform_operation_effects SET receipt_json='{"partial":true}' WHERE operation_id=? AND stage='activate'`, lease.id)
			case "candidate":
				mustExec(t, s.st, `UPDATE app_platform_operation_effects SET receipt_json='{}' WHERE operation_id=? AND stage='start'`, lease.id)
			case "unexpected":
				mustExec(t, s.st, `UPDATE app_platform_operation_effects SET stage='notify' WHERE operation_id=? AND stage='activate'`, lease.id)
			}
			before := dumpOf(t, s.st)
			if intent, err := s.preparePlatformBackendDelivery(context.Background(), lease); err == nil {
				intent.registration.Close()
				t.Fatal("changed authority/history accepted", mode)
			}
			if !bytes.Equal(stripHeader(before), stripHeader(dumpOf(t, s.st))) {
				t.Fatal("refusal partially changed state", mode)
			}
		})
	}
}
func TestPlatformBackendDeliveryRefusalsSQLite(t *testing.T) {
	testPlatformBackendDeliveryRefusals(t, false)
}
func TestPlatformBackendDeliveryRefusalsPostgres(t *testing.T) {
	testPlatformBackendDeliveryRefusals(t, true)
}

func testPlatformBackendDeliveryIntentRollback(t *testing.T, pg bool) {
	s, lease := platformBackendDeliveryFixture(t, pg)
	if pg {
		mustExec(t, s.st, `CREATE FUNCTION fixture_delivery_intent_failure() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'fixture delivery audit failure';END $$`)
		mustExec(t, s.st, `CREATE TRIGGER fixture_delivery_intent_failure BEFORE INSERT ON audit_log FOR EACH ROW WHEN (NEW.action='app_platform.backend_delivery_intent') EXECUTE FUNCTION fixture_delivery_intent_failure()`)
		t.Cleanup(func() { mustExec(t, s.st, `DROP FUNCTION IF EXISTS fixture_delivery_intent_failure() CASCADE`) })
	} else {
		mustExec(t, s.st, `CREATE TRIGGER fixture_delivery_intent_failure BEFORE INSERT ON audit_log WHEN NEW.action='app_platform.backend_delivery_intent' BEGIN SELECT RAISE(ABORT,'fixture delivery audit failure');END`)
	}
	before := dumpOf(t, s.st)
	if intent, err := s.preparePlatformBackendDelivery(context.Background(), lease); err == nil {
		intent.registration.Close()
		t.Fatal("late audit failure ignored")
	}
	if !bytes.Equal(stripHeader(before), stripHeader(dumpOf(t, s.st))) {
		t.Fatal("failed audit left a delivery effect")
	}
	if pg {
		mustExec(t, s.st, `DROP TRIGGER fixture_delivery_intent_failure ON audit_log`)
	} else {
		mustExec(t, s.st, `DROP TRIGGER fixture_delivery_intent_failure`)
	}
	intent, err := s.preparePlatformBackendDelivery(context.Background(), lease)
	if err != nil {
		t.Fatal(err)
	}
	intent.registration.Close()
}
func TestPlatformBackendDeliveryIntentRollbackSQLite(t *testing.T) {
	testPlatformBackendDeliveryIntentRollback(t, false)
}
func TestPlatformBackendDeliveryIntentRollbackPostgres(t *testing.T) {
	testPlatformBackendDeliveryIntentRollback(t, true)
}
