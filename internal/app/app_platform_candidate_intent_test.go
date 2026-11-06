package app

import (
	"bytes"
	"context"
	"encoding/json"
	"strconv"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/transport"
)

// Seeds repository metadata for SQL/intent tests only. The data observation is
// synthetic; this helper cannot prove runtime admission, execution or readiness.
func platformCandidateIntentFixture(t *testing.T, pg bool) (*Server, platformOperationLease) {
	t.Helper()
	s, session, lease := platformDataIntentFixture(t, pg)
	e := migExec{s: s.st, ex: s.st.db}
	dek, err := s.dek()
	if err != nil {
		t.Fatal(err)
	}
	runtime, seed, err := s.platformDataRuntime(e, lease.controllerEpoch, dek)
	clear(seed)
	if err != nil {
		t.Fatal(err)
	}
	var p platformInstallProjection
	original := scalar[string](t, s.st, `SELECT p.plan_json FROM app_platform_review_plans p JOIN app_platform_operations o ON o.review_plan_id=p.id WHERE o.id=?`, lease.id)
	if err := json.Unmarshal([]byte(original), &p); err != nil {
		t.Fatal(err)
	}
	raw, archive := platformManagedReleaseFixture(t, "linux/amd64")
	var manifest appplatform.Manifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	manifest.ID = p.AppID
	raw, err = json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	release, err := s.stagePlatformRelease(session, raw, platformSignTestDescriptor(t, raw), archive)
	if err != nil {
		t.Fatal(err)
	}
	p.Format, p.ReleaseSHA256 = platformManagedInstallFormat, release.sha256
	p.Runtime = &platformManagedInstallTarget{RuntimeID: runtime.id, DeploymentID: runtime.deploymentID, ControllerEpoch: runtime.controllerEpoch, BindingRevision: runtime.revision, ControlRevision: runtime.controlRevision, Fingerprint: runtime.fingerprint, Profile: transport.ManagedRuntimeProfile{Protocol: transport.ManagedRunnerProtocol, Platform: "linux/amd64", BackendProtocol: transport.ManagedBackendProtocol, SandboxProfile: control.CandidateSandboxProfile, RootFilesystemUUID: platformFixtureID(999), RootInode: "10"}}
	encoded, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	planID := scalar[string](t, s.st, `SELECT review_plan_id FROM app_platform_operations WHERE id=?`, lease.id)
	digest := platformDigest(encoded)
	request, _ := json.Marshal([]string{"portal-operation-commit/v1", planID, digest})
	mustExec(t, s.st, `UPDATE app_platform_review_plans SET plan_json=?,plan_sha256=?,target_release_sha256=? WHERE id=?`, string(encoded), digest, release.sha256, planID)
	mustExec(t, s.st, `UPDATE app_platform_operations SET managed=1,request_sha256=? WHERE id=?`, platformDigest(request), lease.id)
	mustExec(t, s.st, `UPDATE app_platform_installations SET runtime_id=? WHERE id=?`, runtime.id, lease.installationID)
	lease.snapshot.Managed = true
	intent, err := s.preparePlatformInstallData(context.Background(), lease)
	if err != nil {
		t.Fatal(err)
	}
	endpoint := platformRuntimeEndpoint{path: "/run/portal-apps/runner.sock", controllerUID: 41001, controllerGID: 41001}
	lease, err = s.commitPlatformObservedData(context.Background(), lease, intent, endpoint, platformDataObservationFixture(t, intent, endpoint))
	if err != nil || lease.snapshot.Stage != appplatform.StageStart {
		t.Fatal("synthetic data stage", err)
	}
	return s, lease
}

func testPlatformCandidateIntent(t *testing.T, pg bool) {
	s, lease := platformCandidateIntentFixture(t, pg)
	intent, err := s.preparePlatformInstallCandidate(context.Background(), lease)
	if err != nil {
		t.Fatal(err)
	}
	verified, err := control.VerifyCandidate(intent.raw, intent.signature, intent.authority, time.Now().UnixMilli())
	if err != nil || verified.Command() != intent.command || intent.command.Payload.ResourceID != intent.resourceID || intent.command.Payload.DataNamespaceID != intent.data.Claim.ResourceID || intent.command.Payload.ReleaseSHA256 != platformDigest(intent.descriptor) {
		t.Fatal("signed candidate identity", err)
	}
	expires, _ := strconv.ParseInt(intent.command.ExpiresMS, 10, 64)
	if expires > lease.leaseUntilMS-1000 {
		t.Fatal("candidate authority outlives lease")
	}
	again, err := s.preparePlatformInstallCandidate(context.Background(), lease)
	if err != nil || again.resourceID != intent.resourceID || again.effectID != intent.effectID || again.command.PayloadSHA256 != intent.command.PayloadSHA256 || again.dataSHA != intent.dataSHA {
		t.Fatal("retry replaced candidate intent", err)
	}
	if scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_activations WHERE state='candidate' AND readiness_evidence='{}' AND observed_ms IS NULL`) != 1 || scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_operation_effects WHERE stage='start' AND state='prepared' AND observed_fence=0 AND receipt_json='{}'`) != 1 {
		t.Fatal("intent fabricated native success")
	}
	if scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_access_grants`) != 0 || scalar[string](t, s.st, `SELECT stage FROM app_platform_operations WHERE id=?`, lease.id) != "start" {
		t.Fatal("intent activated app")
	}
	if scalar[int](t, s.st, `SELECT COUNT(*) FROM audit_log WHERE action='app_platform.candidate_intent' AND target_id=?`, lease.id) != 1 {
		t.Fatal("duplicate/missing intent audit")
	}
	before := dumpOf(t, s.st)
	if _, err := s.commitPlatformObservedCandidate(context.Background(), lease, intent, platformRuntimeEndpoint{}, transport.CandidateReceiptObservation{}); err == nil {
		t.Fatal("zero observation settled candidate")
	}
	if !bytes.Equal(stripHeader(before), stripHeader(dumpOf(t, s.st))) {
		t.Fatal("invalid candidate proof mutated state")
	}
}
func TestPlatformCandidateIntentSQLite(t *testing.T)   { testPlatformCandidateIntent(t, false) }
func TestPlatformCandidateIntentPostgres(t *testing.T) { testPlatformCandidateIntent(t, true) }

func testPlatformCandidateIntentRefusals(t *testing.T, pg bool) {
	for _, kind := range []string{"runtime", "key", "sponsor", "pause", "lease", "takeover", "namespace", "data_receipt", "candidate", "effect", "partial"} {
		t.Run(kind, func(t *testing.T) {
			s, lease := platformCandidateIntentFixture(t, pg)
			if _, err := s.preparePlatformInstallCandidate(context.Background(), lease); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "runtime":
				mustExec(t, s.st, `UPDATE app_platform_runtime_bindings SET enabled=0`)
			case "key":
				mustExec(t, s.st, `UPDATE app_platform_runtime_bindings SET sealed_control_key='sealed:invalid'`)
			case "sponsor":
				mustExec(t, s.st, `UPDATE users SET active=0 WHERE username=(SELECT username FROM app_platform_review_plans)`)
			case "pause":
				mustExec(t, s.st, `UPDATE meta SET v='1' WHERE k='app_platform_restore_paused'`)
			case "lease":
				mustExec(t, s.st, `UPDATE app_platform_operations SET lease_until_ms=0`)
			case "takeover":
				mustExec(t, s.st, `UPDATE app_platform_operations SET fence=fence+1`)
			case "namespace":
				mustExec(t, s.st, `UPDATE app_platform_data_namespaces SET writer_fence=2`)
			case "data_receipt":
				mustExec(t, s.st, `UPDATE app_platform_operation_effects SET receipt_json='{}' WHERE stage='prepare_data'`)
			case "candidate":
				mustExec(t, s.st, `UPDATE app_platform_activations SET state='active'`)
			case "effect":
				mustExec(t, s.st, `UPDATE app_platform_operation_effects SET state='unknown' WHERE stage='start'`)
			case "partial":
				mustExec(t, s.st, `DELETE FROM app_platform_operation_effects WHERE stage='start'`)
			}
			before := dumpOf(t, s.st)
			if _, err := s.preparePlatformInstallCandidate(context.Background(), lease); err == nil {
				t.Fatal("invalid candidate intent accepted", kind)
			}
			if !bytes.Equal(stripHeader(before), stripHeader(dumpOf(t, s.st))) {
				t.Fatal("refused intent changed state", kind)
			}
		})
	}
}
func TestPlatformCandidateIntentRefusalsSQLite(t *testing.T) {
	testPlatformCandidateIntentRefusals(t, false)
}
func TestPlatformCandidateIntentRefusalsPostgres(t *testing.T) {
	testPlatformCandidateIntentRefusals(t, true)
}

func TestPlatformCandidateRefusesStaticInstall(t *testing.T) {
	s, _, lease := platformDataIntentFixture(t, false)
	endpoint := platformRuntimeEndpoint{}
	if _, err := s.dispatchPlatformInstallCandidate(context.Background(), lease, endpoint); err == nil {
		t.Fatal("static data stage started backend")
	}
	if _, err := s.completePlatformInstallCandidate(context.Background(), lease, endpoint); err == nil {
		t.Fatal("static stage read candidate")
	}
}
