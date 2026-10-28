//go:build linux && (amd64 || arm64)

package app

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

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
	raw, _, archive := platformSignedReleaseFixture(t, "1.0.0-beta.3")
	var m appplatform.Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	m.ID = "managed-data-sample"
	m.Compatibility.PlatformAPI = ">=2.0.0-0 <3.0.0"
	m.Compatibility.RunnerProtocol = ">=1.0.0-0 <2.0.0"
	m.Capabilities.Required = []string{}
	m.Backend = &appplatform.Backend{Kind: "oci", Image: "registry.k8s.io/pause@sha256:" + strings.Repeat("a", 64), Platforms: []string{facts.observation.Profile().Platform}, Protocol: facts.observation.Profile().BackendProtocol, Resources: appplatform.Resources{MemoryMiB: 256, CPUMillis: 500, PIDs: 64}}
	raw, err = json.Marshal(m)
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
}
