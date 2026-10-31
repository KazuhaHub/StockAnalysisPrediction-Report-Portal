package app

import (
	"bytes"
	"testing"
)

func platformSecretBindingFixture(t *testing.T, pg bool) (*Server, platformActorSession, string, platformCredentialMetadata) {
	t.Helper()
	s, admin, release := platformInstallFixture(t, pg)
	review, err := s.createPlatformNativeInstallReview(admin, release.sha256, 1)
	if err != nil {
		t.Fatal(err)
	}
	commit, err := s.consumePlatformInstallReview(admin, review.ID, review.SHA256, platformFixtureID(4101))
	if err != nil {
		t.Fatal(err)
	}
	installation := commit.InstallationID
	now, err := platformDatabaseNow(s.st.poolExec())
	if err != nil {
		t.Fatal(err)
	}
	epoch := scalar[string](t, s.st, `SELECT v FROM meta WHERE k='app_platform_controller_epoch'`)
	runtimeID := platformFixtureID(4102)
	mustExec(t, s.st, `INSERT INTO app_platform_runtime_bindings(id,deployment_id,controller_epoch,control_revision,enabled,verification_key_fingerprint,sealed_control_key,revision,created_ms,updated_ms)
		VALUES(?,?,?,1,1,?,?,1,?,?)`, runtimeID, platformFixtureID(4103), epoch, platformDigest([]byte("runtime-key")), sealPrefix+"fixture", now, now)
	mustExec(t, s.st, `UPDATE app_platform_installations SET runtime_id=?,desired_state='enabled',phase='ready',updated_ms=? WHERE id=?`, runtimeID, now, installation)
	mustExec(t, s.st, `INSERT INTO app_platform_activations(id,installation_id,release_sha256,generation,policy_revision,data_namespace_id,data_format,state,readiness_evidence,observed_ms,created_ms,updated_ms)
		VALUES(?,?,?,1,1,NULL,0,'active','{}',?,?,?)`, platformFixtureID(4104), installation, release.sha256, now, now, now)
	planID := scalar[string](t, s.st, `SELECT review_plan_id FROM app_platform_operations WHERE installation_id=? AND action='install'`, installation)
	mustExec(t, s.st, `INSERT INTO app_platform_capability_grants(installation_id,capability,granted,policy_json,policy_revision,review_plan_id,created_ms,updated_ms)
		VALUES(?,'network.http',1,?,1,?,?,?)`, installation, platformNetworkPolicyFixture(t), planID, now, now)
	mustExec(t, s.st, `INSERT INTO app_platform_capability_grants(installation_id,capability,granted,policy_json,policy_revision,review_plan_id,created_ms,updated_ms)
		VALUES(?,'secrets.use',1,'{}',1,?,?,?)`, installation, planID, now, now)
	credential, err := s.changePlatformCredential(admin, platformCredentialChange{kind: "create", label: "Upstream fixture", value: []byte("disposable-upstream-value")})
	if err != nil {
		t.Fatal(err)
	}
	return s, admin, installation, credential
}

func platformActivateSecretBindingFixture(t *testing.T, s *Server, admin platformActorSession, installation string, credential platformCredentialMetadata) platformSecretBindingMetadata {
	t.Helper()
	plan, err := s.createPlatformSecretBindingReview(admin, installation, "upstream-api", credential.ID, "http_bearer")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains([]byte(plan.JSON), []byte("disposable-upstream-value")) {
		t.Fatal("binding review contains credential value")
	}
	binding, err := s.consumePlatformSecretBindingReview(admin, plan.ID, plan.SHA256)
	if err != nil {
		t.Fatal(err)
	}
	if binding.State != "active" || binding.Revision < 1 {
		t.Fatal("binding was not activated", binding)
	}
	return binding
}

func testPlatformSecretBindingLifecycle(t *testing.T, pg bool) {
	s, admin, installation, credential := platformSecretBindingFixture(t, pg)
	binding := platformActivateSecretBindingFixture(t, s, admin, installation, credential)
	if binding.Revision != 1 {
		t.Fatal("initial binding revision", binding.Revision)
	}
	e := s.st.poolExec()
	facts, err := loadPlatformSecretBindingFacts(e, installation, false)
	if err != nil {
		t.Fatal(err)
	}
	use, sealed, err := loadPlatformSecretBindingUse(e, installation, binding.Name, facts)
	if err != nil {
		t.Fatal(err)
	}
	commandUse, header, headerValue, err := s.resolvePlatformSecretBindingCommand(e, installation, binding.Name)
	if err != nil || commandUse.id != use.id || header != "Authorization" || string(headerValue) != "Bearer disposable-upstream-value" {
		t.Fatal("private command credential", commandUse, header, string(headerValue), err)
	}
	clear(headerValue)
	plain, err := s.openPlatformCredentialValue(use.credentialID, use.credentialValueRevision, sealed)
	if err != nil || string(plain) != "disposable-upstream-value" {
		t.Fatal("binding did not resolve the reviewed value", err)
	}
	clear(plain)
	if scalar[int](t, s.st, `SELECT COUNT(*) FROM audit_log WHERE action='app_platform.secret_binding_active' AND detail LIKE '%disposable-upstream-value%'`) != 0 {
		t.Fatal("binding audit exposed credential value")
	}

	rotation, err := s.previewPlatformCredentialChange(admin, credential.ID, credential.Revision, credential.ValueRevision, "replace")
	if err != nil || rotation.Direct || rotation.ID == "" || len(rotation.Affected) != 1 || rotation.Affected[0].ID != binding.ID {
		t.Fatal("credential rotation review", rotation, err)
	}
	rotated, err := s.consumePlatformCredentialReview(admin, rotation.ID, rotation.SHA256, []byte("replacement-upstream-value"))
	if err != nil || rotated.ValueRevision != credential.ValueRevision+1 {
		t.Fatal("credential rotation", rotated, err)
	}
	if scalar[string](t, s.st, `SELECT state FROM app_platform_secret_bindings WHERE id=?`, binding.ID) != "review_required" {
		t.Fatal("rotation left binding active")
	}
	if _, _, err := loadPlatformSecretBindingUse(e, installation, binding.Name, facts); err == nil {
		t.Fatal("old binding authorized the replacement value")
	}
	reapproved := platformActivateSecretBindingFixture(t, s, admin, installation, rotated)
	if reapproved.Revision != 3 {
		t.Fatal("reapproval revision", reapproved.Revision)
	}
	use, sealed, err = loadPlatformSecretBindingUse(e, installation, binding.Name, facts)
	if err != nil {
		t.Fatal(err)
	}
	plain, err = s.openPlatformCredentialValue(use.credentialID, use.credentialValueRevision, sealed)
	if err != nil || string(plain) != "replacement-upstream-value" {
		t.Fatal("reapproved binding used the wrong value", err)
	}
	clear(plain)

	revocation, err := s.previewPlatformCredentialChange(admin, rotated.ID, rotated.Revision, rotated.ValueRevision, "revoke")
	if err != nil || revocation.Direct || len(revocation.Affected) != 1 {
		t.Fatal("credential revocation review", revocation, err)
	}
	revoked, err := s.consumePlatformCredentialReview(admin, revocation.ID, revocation.SHA256, nil)
	if err != nil || revoked.State != "revoked" {
		t.Fatal("credential revocation", revoked, err)
	}
	if scalar[string](t, s.st, `SELECT state FROM app_platform_secret_bindings WHERE id=?`, binding.ID) != "revoked" {
		t.Fatal("credential revocation left binding active")
	}
	if _, _, err := loadPlatformSecretBindingUse(e, installation, binding.Name, facts); err == nil {
		t.Fatal("revoked credential remained usable")
	}
}

func TestPlatformSecretBindingLifecycleSQLite(t *testing.T) {
	testPlatformSecretBindingLifecycle(t, false)
}
func TestPlatformSecretBindingLifecyclePostgres(t *testing.T) {
	testPlatformSecretBindingLifecycle(t, true)
}

func TestPlatformSecretBindingReviewRefusesDrift(t *testing.T) {
	s, admin, installation, credential := platformSecretBindingFixture(t, false)
	plan, err := s.createPlatformSecretBindingReview(admin, installation, "upstream-api", credential.ID, "http_api_key")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.st.exec(`UPDATE app_platform_capability_grants SET policy_json=? WHERE installation_id=? AND capability='network.http'`, `{"format":"portal.http-policy/1-dev.1"}`, installation); err != nil {
		t.Fatal(err)
	}
	if _, err := s.consumePlatformSecretBindingReview(admin, plan.ID, plan.SHA256); err == nil {
		t.Fatal("binding review survived network policy drift")
	}
	if scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_secret_bindings`) != 0 {
		t.Fatal("stale binding review wrote a row")
	}

	plan, err = s.createPlatformSecretBindingReview(admin, installation, "upstream-api", credential.ID, "http_bearer")
	if err == nil {
		t.Fatal("binding review accepted a malformed network policy")
	}
	_ = plan
}

func TestPlatformSecretPolicyRequiresReviewedNetworkDigest(t *testing.T) {
	raw, err := canonicalPlatformSecretBindingPolicy(platformSecretBindingPolicy{operation: "http_bearer", networkPolicySHA256: platformDigest([]byte("policy"))})
	if err != nil {
		t.Fatal(err)
	}
	if err := validatePlatformHTTPSecretBindingPolicy(raw, "http_bearer", platformDigest([]byte("other"))); err == nil {
		t.Fatal("binding policy accepted another network digest")
	}
	if _, err := canonicalPlatformSecretBindingPolicy(platformSecretBindingPolicy{operation: "raw_read"}); err != nil {
		t.Fatal(err)
	}
	if err := validatePlatformHTTPSecretBindingPolicy(raw, "raw_read", ""); err == nil {
		t.Fatal("raw read policy became an HTTP binding")
	}
}

func TestPlatformSecretHeaderFixedShapes(t *testing.T) {
	name, value, ok := platformSecretHeader("http_bearer", []byte("disposable"))
	if !ok || name != "Authorization" || string(value) != "Bearer disposable" {
		t.Fatal("bearer header", name, string(value), ok)
	}
	name, value, ok = platformSecretHeader("http_api_key", []byte("disposable"))
	if !ok || name != "X-API-Key" || string(value) != "disposable" {
		t.Fatal("api key header", name, string(value), ok)
	}
	for _, operation := range []string{"raw_read", "http_cookie", ""} {
		if name, value, ok := platformSecretHeader(operation, []byte("disposable")); ok || name != "" || len(value) != 0 {
			t.Fatal("unsupported operation shaped a header", operation)
		}
	}
	if _, value, ok := platformSecretHeader("http_bearer", []byte("bad\nvalue")); ok || len(value) != 0 {
		t.Fatal("control byte became header data")
	}
}
