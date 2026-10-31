package app

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/transport"
)

func platformServiceIdentityFixture(t *testing.T) (*Server, string) {
	t.Helper()
	s, admin := platformActorTestServer(t, false)
	if err := s.initializeAppPlatformControl(admin); err != nil {
		t.Fatal(err)
	}
	platformRegisterTestPublisher(t, s, admin)
	raw, err := os.ReadFile("../appplatform/testdata/descriptors/managed.json")
	if err != nil {
		t.Fatal(err)
	}
	var manifest appplatform.Manifest
	if json.Unmarshal(raw, &manifest) != nil {
		t.Fatal("managed descriptor fixture")
	}
	manifest.Publisher = "fixture-publisher"
	manifest.Compatibility.PlatformAPI = ">=2.0.0-0 <3.0.0"
	manifest.Capabilities.Required = []string{"background.run", "notifications.publish", "reports.read"}
	manifest.Capabilities.Optional = nil
	_, _, archive := platformSignedReleaseFixture(t, "1.0.0-beta.1")
	manifest.Frontend.SHA256 = platformDigest(archive)
	raw, err = json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	release, err := s.stagePlatformRelease(admin, raw, platformSignTestDescriptor(t, raw), archive)
	if err != nil {
		t.Fatal(err)
	}
	now, err := platformDatabaseNow(s.st.poolExec())
	if err != nil {
		t.Fatal(err)
	}
	epoch := scalar[string](t, s.st, `SELECT v FROM meta WHERE k='app_platform_controller_epoch'`)
	installation := platformFixtureID(5101)
	runtimeID := platformFixtureID(5102)
	if err := platformSeedInstallation(t, s.st.poolExec(), installation, manifest.Publisher, manifest.ID); err != nil {
		t.Fatal(err)
	}
	mustExec(t, s.st, `INSERT INTO app_platform_runtime_bindings(id,deployment_id,controller_epoch,control_revision,enabled,verification_key_fingerprint,sealed_control_key,revision,created_ms,updated_ms)
		VALUES(?,?,?,1,1,?,?,1,?,?)`, runtimeID, platformFixtureID(5103), epoch, platformDigest([]byte("service-runtime")), sealPrefix+"fixture", now, now)
	mustExec(t, s.st, `UPDATE app_platform_installations SET runtime_id=?,phase='ready',controller_epoch=?,updated_ms=? WHERE id=?`, runtimeID, epoch, now, installation)
	dek, err := s.dek()
	if err != nil {
		t.Fatal(err)
	}
	seed := []byte(strings.Repeat("s", 32))
	sealedSeed, err := aesSeal(dek, seed, aad("app-platform", installation))
	clear(dek)
	clear(seed)
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, s.st, `UPDATE app_platform_installations SET sealed_identity_seed=? WHERE id=?`, sealPrefix+sealedSeed, installation)
	mustExec(t, s.st, `INSERT INTO app_platform_activations(id,installation_id,release_sha256,generation,policy_revision,data_namespace_id,data_format,state,readiness_evidence,observed_ms,created_ms,updated_ms)
		VALUES(?,?,?,1,1,NULL,0,'active','{}',?,?,?)`, platformFixtureID(5104), installation, release.sha256, now, now, now)
	target := &platformManagedInstallTarget{
		RuntimeID: runtimeID, DeploymentID: platformFixtureID(5103), ControllerEpoch: epoch,
		BindingRevision: 1, ControlRevision: 1, Fingerprint: platformDigest([]byte("service-runtime")),
		Profile: transport.ManagedRuntimeProfile{
			Protocol: transport.ManagedRunnerProtocol, Platform: "linux/amd64", BackendProtocol: transport.ManagedBackendProtocol,
			SandboxProfile: control.CandidateSandboxProfile, RootFilesystemUUID: platformFixtureID(5105), RootInode: "20",
		},
	}
	selection, err := platformInstallCapabilitySelection(manifest, target)
	if err != nil {
		t.Fatal(err)
	}
	plan := platformInstallProjection{
		Format: platformWritableInstallFormat, AppID: manifest.ID, PublisherID: manifest.Publisher,
		ReleaseSHA256: release.sha256, TrustRevision: 1, KeyRevision: 1, KeyID: "first-key",
		ControllerEpoch: epoch, PlatformAPI: platformDevelopmentAPI, Runtime: target,
	}
	planRaw, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	planID := platformFixtureID(5106)
	mustExec(t, s.st, `INSERT INTO app_platform_review_plans(id,installation_id,app_id,publisher_id,action,source_release_sha256,target_release_sha256,installation_revision,policy_revision,trust_revision,controller_epoch,actor_scope,username,account_created,account_generation,authority_fingerprint,plan_sha256,plan_json,expires_ms,consumed_ms,created_ms,updated_ms)
		VALUES(?,?,?,?,'install',NULL,?,1,1,1,?,?,NULL,'',0,?,?,?,?,?,?,?)`,
		planID, installation, manifest.ID, manifest.Publisher, release.sha256, epoch,
		platformDigest([]byte("service-actor")), platformDigest([]byte("service-plan")), platformDigest(planRaw), string(planRaw), now+60000, now, now, now)
	for _, capability := range selection {
		granted := capability.granted
		mustExec(t, s.st, `INSERT INTO app_platform_capability_grants(installation_id,capability,granted,policy_json,policy_revision,review_plan_id,created_ms,updated_ms) VALUES(?,?,?,?,1,?,?,?)`,
			installation, capability.capability, boolInt(granted), capability.policy, planID, now, now)
	}
	return s, installation
}

func TestPlatformServiceIdentityUsesNoUserAndRechecksCurrentState(t *testing.T) {
	s, installation := platformServiceIdentityFixture(t)
	raw, grant, err := s.issuePlatformServiceIdentity(context.Background(), installation, platformFixtureID(5110), []string{"reports.read", "background.run"}, time.Minute)
	if err == nil {
		t.Fatal("background.run was accepted as a workload resource")
	}
	_ = raw
	_ = grant
	raw, grant, err = s.issuePlatformServiceIdentity(context.Background(), installation, platformFixtureID(5110), []string{"reports.read"}, time.Minute)
	if err != nil || !platformValidSHA(grant.ReleaseSHA256) || grant.Generation != "1" {
		t.Fatal("issue service identity", grant, err)
	}
	verified, err := s.verifyPlatformServiceIdentity(context.Background(), raw, "reports.read")
	if err != nil || verified.InstallationID != installation || verified.LeaseID != platformFixtureID(5110) {
		t.Fatal("verify service identity", verified, err)
	}
	for _, forbidden := range []string{"username", "account_created", "viewer", "role", "session_id"} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatal("service identity impersonates a user", forbidden)
		}
	}
	if _, err := s.verifyPlatformServiceIdentity(context.Background(), raw, "network.http"); err == nil {
		t.Fatal("resource absent from lease was accepted")
	}
	mustExec(t, s.st, `UPDATE app_platform_capability_grants SET granted=0 WHERE installation_id=? AND capability='reports.read'`, installation)
	if _, err := s.verifyPlatformServiceIdentity(context.Background(), raw, "reports.read"); err == nil {
		t.Fatal("revoked service resource remained usable")
	}
}

func TestPlatformServiceIdentityInvalidatedByLifecycleAndRestore(t *testing.T) {
	for _, mode := range []string{"disable", "generation", "policy", "background_grant", "restore"} {
		t.Run(mode, func(t *testing.T) {
			s, installation := platformServiceIdentityFixture(t)
			raw, _, err := s.issuePlatformServiceIdentity(context.Background(), installation, platformFixtureID(5120), []string{"reports.read"}, time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "disable":
				mustExec(t, s.st, `UPDATE app_platform_installations SET desired_state='disabled',phase='disabled' WHERE id=?`, installation)
			case "generation":
				mustExec(t, s.st, `UPDATE app_platform_installations SET generation=generation+1 WHERE id=?`, installation)
			case "policy":
				mustExec(t, s.st, `UPDATE app_platform_installations SET policy_revision=policy_revision+1 WHERE id=?`, installation)
			case "background_grant":
				mustExec(t, s.st, `UPDATE app_platform_capability_grants SET granted=0 WHERE installation_id=? AND capability='background.run'`, installation)
			case "restore":
				mustExec(t, s.st, `UPDATE meta SET v='1' WHERE k='app_platform_restore_paused'`)
			}
			if _, err := s.verifyPlatformServiceIdentity(context.Background(), raw, "reports.read"); err == nil {
				t.Fatal("stale service identity remained usable", mode)
			}
		})
	}
}

func TestPlatformBackgroundProbeCannotSubmitAfterRevocation(t *testing.T) {
	s, installation := platformServiceIdentityFixture(t)
	raw, _, err := s.issuePlatformServiceIdentity(context.Background(), installation, platformFixtureID(5130), []string{"reports.read"}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	finish := make(chan error, 1)
	go func() {
		close(started)
		time.Sleep(10 * time.Millisecond)
		_, err := s.verifyPlatformServiceIdentity(context.Background(), raw, "reports.read")
		finish <- err
	}()
	<-started
	mustExec(t, s.st, `UPDATE app_platform_capability_grants SET granted=0 WHERE installation_id=? AND capability='reports.read'`, installation)
	if err := <-finish; err == nil {
		t.Fatal("background probe submitted a result after revocation")
	}
}
