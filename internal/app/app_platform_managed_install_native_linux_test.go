//go:build linux && (amd64 || arm64)

package app

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

// Uses a real root endpoint and current non-root Portal/admin context. No
// synthetic native observation or direct runner journal access is supplied here.
func testNativePortalManagedInstallData(t *testing.T, s *Server, session platformActorSession, endpoint platformRuntimeEndpoint) {
	t.Helper()
	ctx := context.Background()
	facts, err := s.observePlatformManagedRuntime(ctx, session, endpoint)
	if err != nil {
		t.Fatal(err)
	}
	raw, archive := platformManagedReleaseFixture(t, facts.observation.Profile().Platform)
	var manifest appplatform.Manifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	manifest.Backend.Image = os.Getenv("PORTAL_MANAGED_IMAGE")
	raw, err = json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	release, err := s.stagePlatformRelease(session, raw, platformSignTestDescriptor(t, raw), archive)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.createPlatformNativeInstallReview(session, release.sha256, 1); err == nil {
		t.Fatal("legacy review accepted backend")
	}
	plan, err := s.createPlatformManagedInstallReview(ctx, session, release.sha256, 1, nil, endpoint)
	if err != nil {
		t.Fatal("managed review", err)
	}
	var projection platformInstallProjection
	if err := json.Unmarshal([]byte(plan.JSON), &projection); err != nil || projection.Runtime == nil || projection.Runtime.RuntimeID != facts.runtime.id || projection.Runtime.Profile.ExecutionReady {
		t.Fatal("review lost exact runtime target", err)
	}
	if _, err := s.consumePlatformInstallReview(session, plan.ID, plan.SHA256, platformFixtureID(880)); err == nil {
		t.Fatal("legacy consumption promoted managed review")
	}
	before := scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_operations`)
	mustExec(t, s.st, `UPDATE app_platform_runtime_bindings SET enabled=0 WHERE id=?`, facts.runtime.id)
	if _, err := s.consumePlatformManagedInstallReview(ctx, session, plan.ID, plan.SHA256, platformFixtureID(880), endpoint); err == nil {
		t.Fatal("disabled runtime consumed review")
	}
	if scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_operations`) != before {
		t.Fatal("failed consumption partially installed app")
	}
	mustExec(t, s.st, `UPDATE app_platform_runtime_bindings SET enabled=1 WHERE id=?`, facts.runtime.id)
	admitted, err := s.consumePlatformManagedInstallReview(ctx, session, plan.ID, plan.SHA256, platformFixtureID(880), endpoint)
	if err != nil {
		t.Fatal("managed admission", err)
	}
	replay, err := s.consumePlatformManagedInstallReview(ctx, session, plan.ID, plan.SHA256, platformFixtureID(880), endpoint)
	if err != nil || !replay.Replayed || replay.OperationID != admitted.OperationID {
		t.Fatal("managed admission replay", err)
	}
	if scalar[int](t, s.st, `SELECT managed FROM app_platform_operations WHERE id=?`, admitted.OperationID) != 1 || scalar[string](t, s.st, `SELECT runtime_id FROM app_platform_installations WHERE id=?`, admitted.InstallationID) != facts.runtime.id {
		t.Fatal("managed/runtime identity not persisted")
	}
	lease, err := s.st.claimPlatformOperation(admitted.OperationID, platformFixtureID(881))
	if err != nil {
		t.Fatal(err)
	}
	if !lease.snapshot.Managed {
		t.Fatal("managed operation became static")
	}
	for _, stage := range []appplatform.Stage{appplatform.StageValidate, appplatform.StageDownload, appplatform.StageStage, appplatform.StagePrepareData} {
		lease, err = s.st.advancePlatformInstallStaging(lease)
		if err != nil || lease.snapshot.Stage != stage {
			t.Fatal("managed staging", stage, err)
		}
	}
	next, err := s.dispatchPlatformInstallData(ctx, lease, endpoint)
	if err != nil {
		t.Fatal("managed native data", err)
	}
	if !next.snapshot.Managed || next.snapshot.Stage != appplatform.StageStart || scalar[string](t, s.st, `SELECT stage FROM app_platform_operations WHERE id=?`, admitted.OperationID) != "start" {
		t.Fatal("managed data skipped start/probe")
	}
	if scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_operation_effects WHERE operation_id=? AND stage='prepare_data' AND state='applied'`, admitted.OperationID) != 1 || scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_data_namespaces WHERE owner_installation_id=? AND state='active' AND revision=2`, admitted.InstallationID) != 1 {
		t.Fatal("managed native data not settled")
	}
	if scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_access_grants WHERE installation_id=?`, admitted.InstallationID)+scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_activations WHERE installation_id=?`, admitted.InstallationID) != 0 || scalar[string](t, s.st, `SELECT phase FROM app_platform_installations WHERE id=?`, admitted.InstallationID) != "pending" {
		t.Fatal("data stage granted access")
	}
	if _, err := s.activatePlatformStaticInstall(ctx, next); err == nil {
		t.Fatal("static activator accepted managed stage")
	}
	if _, err := s.dispatchPlatformInstallData(ctx, lease, endpoint); err == nil {
		t.Fatal("stale managed lease repeated data settlement")
	}
	t.Log("MANAGED_INSTALL_DATA_READY")
	intent, err := s.preparePlatformInstallCandidate(ctx, next)
	if err != nil {
		t.Fatal("managed candidate intent", err)
	}
	public, _ := json.Marshal(struct{ Raw, Signature []byte }{intent.raw, intent.signature})
	if err := os.WriteFile(filepath.Join(os.Getenv("TMPDIR"), "candidate-command.json"), public, 0600); err != nil {
		t.Fatal(err)
	}
	// Force Portal settlement to roll back after the root runner has actually
	// started the process. The retry must read that original native result.
	mustExec(t, s.st, `CREATE TRIGGER candidate_settlement_failure BEFORE INSERT ON audit_log WHEN NEW.action='app_platform.operation_stage' AND NEW.target_id='`+next.id+`' AND json_extract(NEW.detail,'$.stage')='probe' BEGIN SELECT RAISE(ABORT,'candidate settlement fixture'); END`)
	if _, err := s.dispatchPlatformInstallCandidate(ctx, next, endpoint); err == nil {
		t.Fatal("candidate settlement failure not exercised")
	}
	if scalar[string](t, s.st, `SELECT stage FROM app_platform_operations WHERE id=?`, next.id) != "start" || scalar[string](t, s.st, `SELECT state FROM app_platform_operation_effects WHERE operation_id=? AND stage='start'`, next.id) != "prepared" {
		t.Fatal("failed candidate settlement partially committed")
	}
	mustExec(t, s.st, `DROP TRIGGER candidate_settlement_failure`)
	probe, err := s.completePlatformInstallCandidate(ctx, next, endpoint)
	if err != nil || probe.snapshot.Stage != appplatform.StageProbe {
		t.Fatal("candidate receipt recovery", err)
	}
	proof, err := platformStartCandidate(ctx, endpoint, intent)
	if err != nil {
		t.Fatal("native candidate replay", err)
	}
	stored := scalar[string](t, s.st, `SELECT receipt_json FROM app_platform_operation_effects WHERE operation_id=? AND stage='start'`, next.id)
	encoded, _ := json.Marshal(proof.Receipt())
	if string(encoded) != stored {
		t.Fatal("candidate replay replaced original native observation")
	}
	if scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_activations WHERE installation_id=? AND state='candidate' AND readiness_evidence='{}' AND observed_ms IS NULL`, next.installationID) != 1 || scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_access_grants WHERE installation_id=?`, next.installationID) != 0 || scalar[string](t, s.st, `SELECT phase FROM app_platform_installations WHERE id=?`, next.installationID) != "pending" {
		t.Fatal("candidate start became readiness or access")
	}
	if _, err := s.dispatchPlatformInstallCandidate(ctx, next, endpoint); err == nil {
		t.Fatal("stale candidate stage dispatched")
	}
	t.Log("MANAGED_INSTALL_CANDIDATE_PROBE")
	if os.Getenv("PORTAL_MANAGED_READY") == "1" {
		var activated platformOperationLease
		deadline := time.Now().Add(3 * time.Second)
		for {
			activated, err = s.probePlatformInstallCandidate(ctx, probe, endpoint)
			if err == nil {
				break
			}
			if !time.Now().Before(deadline) {
				t.Fatal("actual backend readiness", err)
			}
			time.Sleep(20 * time.Millisecond)
		}
		if activated.snapshot.Stage != appplatform.StageActivate || scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_activations WHERE installation_id=? AND state='candidate' AND readiness_evidence<>'{}' AND observed_ms IS NOT NULL`, probe.installationID) != 1 || scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_access_grants WHERE installation_id=?`, probe.installationID) != 0 || scalar[string](t, s.st, `SELECT phase FROM app_platform_installations WHERE id=?`, probe.installationID) != "pending" {
			t.Fatal("readiness granted use or lost evidence")
		}
		if _, err := s.activatePlatformStaticInstall(ctx, activated); err == nil {
			t.Fatal("readiness bypassed managed activation")
		}
		if _, err := s.probePlatformInstallCandidate(ctx, probe, endpoint); err == nil {
			t.Fatal("stale readiness stage recommitted")
		}
		t.Log("MANAGED_INSTALL_READINESS")
		registration, err := s.preparePlatformBackendRegistration(ctx, activated)
		if err != nil {
			t.Fatal("managed registration material", err)
		}
		defer registration.Close()
		metadata, err := registration.Metadata()
		if err != nil || metadata.Identity.InstallationID != activated.installationID || metadata.Identity.ReleaseSHA256 != intent.command.Payload.ReleaseSHA256 {
			t.Fatal("registration lost managed tuple", err)
		}
		document, err := registration.Document()
		if err != nil {
			t.Fatal(err)
		}
		defer clear(document)
		retry, err := s.preparePlatformBackendRegistration(ctx, activated)
		if err != nil {
			t.Fatal("registration retry", err)
		}
		defer retry.Close()
		again, err := retry.Document()
		defer clear(again)
		if err != nil || !bytes.Equal(document, again) {
			t.Fatal("registration retry changed key identity", err)
		}
		if scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_operation_effects WHERE operation_id=?`, activated.id) != 2 || scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_access_grants WHERE installation_id=?`, activated.installationID) != 0 {
			t.Fatal("registration preparation granted authority")
		}
		t.Log("MANAGED_BACKEND_REGISTRATION_MATERIAL")
		testNativeRegistrationRPC(t, endpoint, intent, raw, document)
	}
}
