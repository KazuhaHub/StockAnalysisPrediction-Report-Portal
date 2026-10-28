package app

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/transport"
)

func TestManagedInstallRequiresOpaqueRuntimeFacts(t *testing.T) {
	s, session, release := platformInstallFixture(t, false)
	if _, err := s.createPlatformManagedInstallReview(context.Background(), session, release.sha256, 1, nil, platformRuntimeEndpoint{}); err == nil {
		t.Fatal("missing runtime minted managed review")
	}
	if _, err := s.consumePlatformManagedInstallReview(context.Background(), session, platformFixtureID(880), release.sha256, platformFixtureID(881), platformRuntimeEndpoint{}); err == nil {
		t.Fatal("missing runtime consumed managed review")
	}
	if _, err := s.createPlatformInstallReviewForFormatContext(context.Background(), session, release.sha256, 1, nil, platformManagedInstallFormat); err == nil {
		t.Fatal("caller format minted managed review")
	}
	if _, err := managedInstallTarget(platformManagedRuntimeFacts{}, platformRuntimeEndpoint{}); err == nil {
		t.Fatal("zero facts minted target")
	}
	if scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_review_plans`)+scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_installations`) != 0 {
		t.Fatal("refused managed review created authority")
	}
}
func testManagedInstallTargetValidation(t *testing.T, pg bool) {
	s, _, lease := platformDataIntentFixture(t, pg)
	intent, err := s.preparePlatformInstallData(context.Background(), lease)
	if err != nil {
		t.Fatal(err)
	}
	r := intent.runtime
	target := platformManagedInstallTarget{RuntimeID: r.id, DeploymentID: r.deploymentID, ControllerEpoch: r.controllerEpoch, BindingRevision: r.revision, ControlRevision: r.controlRevision, Fingerprint: r.fingerprint, Profile: transport.ManagedRuntimeProfile{Protocol: transport.ManagedRunnerProtocol, Platform: "linux/amd64", BackendProtocol: transport.ManagedBackendProtocol, SandboxProfile: control.CandidateSandboxProfile, RootFilesystemUUID: platformFixtureID(999), RootInode: "10"}}
	e := migExec{s: s.st, ex: s.st.db}
	if err := validateManagedInstallTarget(e, &target, lease.controllerEpoch); err != nil {
		t.Fatal("synthetic target metadata", err)
	}
	for _, mode := range []string{"runtime", "epoch", "binding", "control", "fingerprint", "ready", "platform", "root", "inode", "sandbox"} {
		copy := target
		switch mode {
		case "runtime":
			copy.RuntimeID = platformFixtureID(900)
		case "epoch":
			copy.ControllerEpoch = platformFixtureID(900)
		case "binding":
			copy.BindingRevision++
		case "control":
			copy.ControlRevision++
		case "fingerprint":
			copy.Fingerprint = ""
		case "ready":
			copy.Profile.ExecutionReady = true
		case "platform":
			copy.Profile.Platform = "linux/other"
		case "root":
			copy.Profile.RootFilesystemUUID = ""
		case "inode":
			copy.Profile.RootInode = "0"
		case "sandbox":
			copy.Profile.SandboxProfile = "unrestricted"
		}
		if err := validateManagedInstallTarget(e, &copy, lease.controllerEpoch); err == nil {
			t.Fatal("changed target accepted", mode)
		}
	}
	mustExec(t, s.st, `UPDATE app_platform_runtime_bindings SET enabled=0`)
	if err := validateManagedInstallTarget(e, &target, lease.controllerEpoch); err == nil {
		t.Fatal("disabled target accepted")
	}
}
func TestManagedInstallTargetSQLite(t *testing.T)   { testManagedInstallTargetValidation(t, false) }
func TestManagedInstallTargetPostgres(t *testing.T) { testManagedInstallTargetValidation(t, true) }
func TestManagedInstallRequirementsDoNotGrantProviders(t *testing.T) {
	raw, err := os.ReadFile("../appplatform/testdata/descriptors/managed.json")
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := appplatform.ParseDescriptor(raw)
	if err != nil {
		t.Fatal(err)
	}
	m := parsed.Manifest
	m.Compatibility.PlatformAPI = ">=2.0.0-0 <3.0.0"
	m.Compatibility.RunnerProtocol = ">=1.0.0-0 <2.0.0"
	m.Capabilities.Required = []string{}
	target := &platformManagedInstallTarget{Profile: transport.ManagedRuntimeProfile{Protocol: transport.ManagedRunnerProtocol, Platform: "linux/amd64", BackendProtocol: transport.ManagedBackendProtocol}}
	if err := installRequirementsForTarget(m, platformManagedInstallFormat, target); err != nil {
		t.Fatal(err)
	}
	m.Capabilities.Required = []string{"storage.files"}
	if err := installRequirementsForTarget(m, platformManagedInstallFormat, target); err == nil {
		t.Fatal("missing required provider admitted")
	}
	m.Capabilities.Required = []string{}
	if err := installRequirementsForTarget(m, platformFrontendInstallFormat, target); err == nil {
		t.Fatal("managed metadata entered frontend profile")
	}
	m.Backend = nil
	if err := installRequirementsForTarget(m, platformManagedInstallFormat, target); err == nil {
		t.Fatal("static release became managed")
	}
}

func platformManagedReleaseFixture(t *testing.T, platform string) ([]byte, []byte) {
	t.Helper()
	raw, _, archive := platformSignedReleaseFixture(t, "1.0.0-beta.3")
	var m appplatform.Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	m.ID = "notes-managed-data"
	m.Compatibility.PlatformAPI = ">=2.0.0-0 <3.0.0"
	m.Compatibility.RunnerProtocol = ">=1.0.0-0 <2.0.0"
	m.Capabilities.Required = []string{}
	m.Backend = &appplatform.Backend{Kind: "oci", Image: "registry.k8s.io/pause@sha256:" + strings.Repeat("a", 64), Platforms: []string{platform}, Protocol: transport.ManagedBackendProtocol, Resources: appplatform.Resources{MemoryMiB: 256, CPUMillis: 500, PIDs: 64}}
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return raw, archive
}

func TestManagedInstallSignedRelease(t *testing.T) {
	s, session := platformTrustTestServer(t, false)
	platformRegisterTestPublisher(t, s, session)
	raw, archive := platformManagedReleaseFixture(t, "linux/amd64")
	release, err := s.stagePlatformRelease(session, raw, platformSignTestDescriptor(t, raw), archive)
	if err != nil {
		t.Fatal("managed signed release", err)
	}
	if release.appID != "notes-managed-data" {
		t.Fatal("wrong managed fixture identity")
	}
	if _, err := s.createPlatformNativeInstallReview(session, release.sha256, 1); err == nil {
		t.Fatal("legacy admitted managed fixture")
	}
	if scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_installations`) != 0 {
		t.Fatal("release staging granted installation")
	}
}
