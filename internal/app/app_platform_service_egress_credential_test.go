package app

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/transport"
)

const platformServiceCredentialValue = "disposable-upstream-value"

var platformServiceCredentialResources = []string{"network.http", "notifications.publish", "secrets.use"}

// platformServiceCredentialFixture is the egress installation of
// platformServiceEgressFixture with secrets.use granted, a credential and one
// active binding named upstream-api for the given principals ("" is the
// viewer-only binding every binding was before doc 176).
func platformServiceCredentialFixture(t *testing.T, pg bool, principals string) (*Server, platformActorSession, string, platformCredentialMetadata) {
	t.Helper()
	platformFixtureManifestHook = func(m *appplatform.Manifest) {
		m.Capabilities.Required = []string{"background.run", "network.http", "notifications.publish", "secrets.use"}
		m.Capabilities.Optional = nil
	}
	platformFixtureNetworkTarget = true
	t.Cleanup(func() { platformFixtureManifestHook, platformFixtureNetworkTarget = nil, false })
	s, actor, lease := platformWritableAccessFixture(t, pg)
	mustExec(t, s.st, `UPDATE app_platform_capability_grants SET granted=1 WHERE installation_id=?`, lease.installationID)
	credential, err := s.changePlatformCredential(actor, platformCredentialChange{kind: "create", label: "Upstream fixture", value: []byte(platformServiceCredentialValue)})
	if err != nil {
		t.Fatal("credential:", err)
	}
	plan, err := s.createPlatformSecretBindingReviewPrincipals(actor, lease.installationID, "upstream-api", credential.ID, "http_bearer", principals)
	if err != nil {
		t.Fatal("binding review:", err)
	}
	if _, err := s.consumePlatformSecretBindingReview(actor, plan.ID, plan.SHA256); err != nil {
		t.Fatal("binding commit:", err)
	}
	return s, actor, lease.installationID, credential
}

func platformServiceCredentialAdmit() transport.EgressAdmit {
	admit := platformServiceEgressAdmit()
	admit.Credential = "upstream-api"
	return admit
}

// platformServiceCredentialSign loads the authority for a credential call the
// way an admit does, and signs it.
func platformServiceCredentialSign(t *testing.T, s *Server, state platformServiceEgressSessionState) (control.NetworkHTTPRequestCommand, []byte, []byte, platformSecretBindingUse) {
	t.Helper()
	a, err := s.loadServiceEgressForSessionCredential(context.Background(), state, "upstream-api", true)
	if err != nil {
		t.Fatal("load credential authority:", err)
	}
	defer a.Close()
	command, raw, sig, err := a.signServiceEgressCommand(state.sessionID, platformServiceCredentialAdmit(), time.Now().UnixMilli(), state.expiresMS)
	if err != nil {
		t.Fatal("sign credential call:", err)
	}
	return command, raw, sig, a.binding
}

func testPlatformServiceIdentitySecretsUseOnlyWhenGranted(t *testing.T, pg bool) {
	// An installation that never declared secrets.use cannot be issued it.
	plain, plainInstallation := platformServiceEgressFixture(t, pg, true)
	if _, _, err := plain.issuePlatformServiceIdentity(context.Background(), plainInstallation, platformFixtureID(7710), []string{"network.http", "secrets.use"}, time.Minute); err == nil {
		t.Fatal("secrets.use issued without a grant")
	}
	platformFixtureManifestHook, platformFixtureNetworkTarget = nil, false
	s, _, installation, _ := platformServiceCredentialFixture(t, pg, platformPrincipalsServiceOnly)
	ctx := context.Background()
	raw, grant, err := s.issuePlatformServiceIdentity(ctx, installation, platformFixtureID(7711), []string{"secrets.use", "network.http"}, time.Minute)
	if err != nil || strings.Join(grant.Resources, ",") != "network.http,secrets.use" {
		t.Fatal("secrets.use refused although granted", err)
	}
	if verified, err := s.verifyPlatformServiceIdentity(ctx, raw, "secrets.use"); err != nil || verified.InstallationID != installation {
		t.Fatal("assertion with secrets.use does not verify", err)
	}
	mustExec(t, s.st, `UPDATE app_platform_capability_grants SET granted=0 WHERE installation_id=? AND capability='secrets.use'`, installation)
	if _, _, err := s.issuePlatformServiceIdentity(ctx, installation, platformFixtureID(7712), []string{"network.http", "secrets.use"}, time.Minute); err == nil {
		t.Fatal("secrets.use issued after the grant was revoked")
	}
	if _, err := s.verifyPlatformServiceIdentity(ctx, raw, "secrets.use"); err == nil {
		t.Fatal("an assertion with secrets.use survived the revocation")
	}
}

func testPlatformServiceEgressSecretsUseOnlyWithNetwork(t *testing.T, pg bool) {
	s, _, installation, _ := platformServiceCredentialFixture(t, pg, platformPrincipalsServiceOnly)
	ctx := context.Background()
	d, err := s.loadPlatformServiceEgressCurrent(ctx, installation, platformServiceEgressLoad{})
	if err != nil || strings.Join(d.resources, ",") != strings.Join(platformServiceCredentialResources, ",") {
		t.Fatal("discovery", d.resources, err)
	}
	d.Close()
	// Holding the resource does not choose a binding, and an explicit session
	// needs network.http beside it.
	if a, err := s.loadPlatformServiceEgressCurrent(ctx, installation, platformServiceEgressLoad{resources: []string{"notifications.publish", "secrets.use"}}); err == nil {
		a.Close()
		t.Fatal("secrets.use without network.http accepted")
	}
	mustExec(t, s.st, `UPDATE app_platform_capability_grants SET granted=0 WHERE installation_id=? AND capability='network.http'`, installation)
	d, err = s.loadPlatformServiceEgressCurrent(ctx, installation, platformServiceEgressLoad{})
	if err != nil || strings.Join(d.resources, ",") != "notifications.publish" {
		t.Fatal("secrets.use planned without network.http", d.resources, err)
	}
	d.Close()
}

func testPlatformServiceEgressCredentialPlacedForServiceBinding(t *testing.T, pg bool) {
	for _, principals := range []string{platformPrincipalsServiceOnly, platformPrincipalsBoth} {
		s, _, installation, _ := platformServiceCredentialFixture(t, pg, principals)
		state := platformServiceEgressState(t, s, installation, platformServiceCredentialResources...)
		before := platformCredentialOpenCount.Load()
		command, raw, _, binding := platformServiceCredentialSign(t, s, state)
		if platformCredentialOpenCount.Load() != before+1 {
			t.Fatal("the value was not opened exactly once for the call")
		}
		if command.CredentialName != "upstream-api" || command.CredentialHeader != "Authorization" || string(command.CredentialValue) != "Bearer "+platformServiceCredentialValue {
			t.Fatal("command does not carry the reviewed Bearer header")
		}
		if command.ActorScope != platformServiceEgressActorScope(state.sessionID) || binding.principals != principals {
			t.Fatal("call is not the service's", principals)
		}
		// The value is in the signed command and nowhere in Portal's session
		// state: the pin and the session state hold names, revisions and digests.
		if bytes.Contains([]byte(strings.Join(state.resources, ",")), []byte(platformServiceCredentialValue)) || bytes.Contains(state.identity, []byte(platformServiceCredentialValue)) {
			t.Fatal("value in session state")
		}
		if !bytes.Contains(raw, []byte("credential_name")) {
			t.Fatal("command lacks the binding name")
		}
	}
}

func testPlatformServiceEgressCredentialDeniedBeforeDecrypt(t *testing.T, pg bool) {
	s, admin, installation, credential := platformServiceCredentialFixture(t, pg, "")
	state := platformServiceEgressState(t, s, installation, platformServiceCredentialResources...)
	ctx := context.Background()
	denied := func(what string, state platformServiceEgressSessionState, name string) {
		t.Helper()
		before := platformCredentialOpenCount.Load()
		a, err := s.loadServiceEgressForSessionCredential(ctx, state, name, true)
		if err == nil {
			a.Close()
			t.Fatal(what, "was not refused")
		}
		if !errors.Is(err, errPlatformServiceEgressDenied) {
			t.Fatal(what, "is not a denial:", err)
		}
		if platformCredentialOpenCount.Load() != before {
			t.Fatal(what, "reached the credential custody")
		}
	}
	// A viewer-only (v1) binding is not usable in the background.
	denied("viewer-only binding", state, "upstream-api")
	denied("unknown binding", state, "no-such-binding")
	// A session that never held secrets.use cannot name a binding at all.
	noSecrets := platformServiceEgressState(t, s, installation, "network.http", "notifications.publish")
	denied("session without secrets.use", noSecrets, "upstream-api")
	// Reviewed for the service: usable.
	plan, err := s.createPlatformSecretBindingReviewPrincipals(admin, installation, "upstream-api", credential.ID, "http_bearer", platformPrincipalsServiceOnly)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.consumePlatformSecretBindingReview(admin, plan.ID, plan.SHA256); err != nil {
		t.Fatal(err)
	}
	if a, err := s.loadServiceEgressForSessionCredential(ctx, state, "upstream-api", true); err != nil {
		t.Fatal("service binding refused:", err)
	} else {
		a.Close()
	}
	// A binding reviewed for another network policy (target) is not usable for
	// this one, whatever its state says.
	other := platformDigest([]byte("another network policy"))
	otherPolicy, err := platformSecretBindingPolicyFor("http_bearer", other, platformPrincipalsServiceOnly)
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, s.st, `UPDATE app_platform_secret_bindings SET policy_json=? WHERE installation_id=? AND name='upstream-api'`, string(otherPolicy), installation)
	denied("binding bound to another target", state, "upstream-api")
}

func testPlatformServiceEgressCredentialRefusedAfterChange(t *testing.T, pg bool) {
	for name, change := range map[string]func(t *testing.T, s *Server, admin platformActorSession, installation string, credential platformCredentialMetadata){
		"binding revoked": func(t *testing.T, s *Server, admin platformActorSession, installation string, _ platformCredentialMetadata) {
			id := scalar[string](t, s.st, `SELECT id FROM app_platform_secret_bindings WHERE installation_id=? AND name='upstream-api'`, installation)
			revision := scalar[int64](t, s.st, `SELECT revision FROM app_platform_secret_bindings WHERE id=?`, id)
			if err := s.revokePlatformSecretBinding(admin, id, revision); err != nil {
				t.Fatal(err)
			}
		},
		"credential rotated": func(t *testing.T, s *Server, admin platformActorSession, _ string, credential platformCredentialMetadata) {
			rotation, err := s.previewPlatformCredentialChange(admin, credential.ID, credential.Revision, credential.ValueRevision, "replace")
			if err != nil || rotation.Direct {
				t.Fatal("rotation review", err)
			}
			if _, err := s.consumePlatformCredentialReview(admin, rotation.ID, rotation.SHA256, []byte("replacement-upstream-value")); err != nil {
				t.Fatal(err)
			}
		},
		"credential revoked": func(t *testing.T, s *Server, admin platformActorSession, _ string, credential platformCredentialMetadata) {
			revocation, err := s.previewPlatformCredentialChange(admin, credential.ID, credential.Revision, credential.ValueRevision, "revoke")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.consumePlatformCredentialReview(admin, revocation.ID, revocation.SHA256, nil); err != nil {
				t.Fatal(err)
			}
		},
		"secrets.use revoked": func(t *testing.T, s *Server, _ platformActorSession, installation string, _ platformCredentialMetadata) {
			mustExec(t, s.st, `UPDATE app_platform_capability_grants SET granted=0 WHERE installation_id=? AND capability='secrets.use'`, installation)
		},
		"background.run removed": func(t *testing.T, s *Server, _ platformActorSession, installation string, _ platformCredentialMetadata) {
			mustExec(t, s.st, `UPDATE app_platform_capability_grants SET granted=0 WHERE installation_id=? AND capability='background.run'`, installation)
		},
		"service principal withdrawn": func(t *testing.T, s *Server, admin platformActorSession, installation string, credential platformCredentialMetadata) {
			plan, err := s.createPlatformSecretBindingReviewPrincipals(admin, installation, "upstream-api", credential.ID, "http_bearer", "")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.consumePlatformSecretBindingReview(admin, plan.ID, plan.SHA256); err != nil {
				t.Fatal(err)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			s, admin, installation, credential := platformServiceCredentialFixture(t, pg, platformPrincipalsServiceOnly)
			state := platformServiceEgressState(t, s, installation, platformServiceCredentialResources...)
			command, raw, _, binding := platformServiceCredentialSign(t, s, state)
			nonce := hex.EncodeToString(bytes.Repeat([]byte{7}, 32))
			// The call is good until something changes: every challenge is
			// answered from current state.
			if _, _, err := s.approveServiceEgressCheck(context.Background(), state, newPlatformServiceEgressCall(command, raw, binding), nonce); err != nil {
				t.Fatal("check refused before the change:", err)
			}
			change(t, s, admin, installation, credential)
			if _, _, err := s.approveServiceEgressCheck(context.Background(), state, newPlatformServiceEgressCall(command, raw, binding), nonce); err == nil {
				t.Fatal("a check was answered after the change")
			}
		})
	}
}

func testPlatformServiceEgressCheckBindsTheCallsCredential(t *testing.T, pg bool) {
	s, _, installation, _ := platformServiceCredentialFixture(t, pg, platformPrincipalsServiceOnly)
	state := platformServiceEgressState(t, s, installation, platformServiceCredentialResources...)
	command, raw, _, binding := platformServiceCredentialSign(t, s, state)
	nonce := hex.EncodeToString(bytes.Repeat([]byte{7}, 32))
	ctx := context.Background()
	// A command that names a binding is not checkable without the binding the
	// admit resolved, and not against another one.
	if _, _, err := s.approveServiceEgressCheck(ctx, state, newPlatformServiceEgressCall(command, raw, platformSecretBindingUse{}), nonce); err == nil {
		t.Fatal("credential command checked without its binding")
	}
	other := binding
	other.revision++
	if _, _, err := s.approveServiceEgressCheck(ctx, state, newPlatformServiceEgressCall(command, raw, other), nonce); err == nil {
		t.Fatal("credential command checked against a different binding revision")
	}
	// A call without a credential is not checkable against a binding.
	plainCommand, plainRaw, _ := platformServiceEgressSignFor(t, s, state)
	if _, _, err := s.approveServiceEgressCheck(ctx, state, newPlatformServiceEgressCall(plainCommand, plainRaw, binding), nonce); err == nil {
		t.Fatal("plain command checked against a binding")
	}
	if _, _, err := s.approveServiceEgressCheck(ctx, state, newPlatformServiceEgressCall(plainCommand, plainRaw, platformSecretBindingUse{}), nonce); err != nil {
		t.Fatal("plain command refused:", err)
	}
}

func TestPlatformServiceIdentitySecretsUseOnlyWhenGrantedSQLite(t *testing.T) {
	testPlatformServiceIdentitySecretsUseOnlyWhenGranted(t, false)
}
func TestPlatformServiceIdentitySecretsUseOnlyWhenGrantedPostgres(t *testing.T) {
	testPlatformServiceIdentitySecretsUseOnlyWhenGranted(t, true)
}
func TestPlatformServiceEgressSecretsUseOnlyWithNetworkSQLite(t *testing.T) {
	testPlatformServiceEgressSecretsUseOnlyWithNetwork(t, false)
}
func TestPlatformServiceEgressSecretsUseOnlyWithNetworkPostgres(t *testing.T) {
	testPlatformServiceEgressSecretsUseOnlyWithNetwork(t, true)
}
func TestPlatformServiceEgressCredentialPlacedForServiceBindingSQLite(t *testing.T) {
	testPlatformServiceEgressCredentialPlacedForServiceBinding(t, false)
}
func TestPlatformServiceEgressCredentialPlacedForServiceBindingPostgres(t *testing.T) {
	testPlatformServiceEgressCredentialPlacedForServiceBinding(t, true)
}
func TestPlatformServiceEgressCredentialDeniedBeforeDecryptSQLite(t *testing.T) {
	testPlatformServiceEgressCredentialDeniedBeforeDecrypt(t, false)
}
func TestPlatformServiceEgressCredentialDeniedBeforeDecryptPostgres(t *testing.T) {
	testPlatformServiceEgressCredentialDeniedBeforeDecrypt(t, true)
}
func TestPlatformServiceEgressCredentialRefusedAfterChangeSQLite(t *testing.T) {
	testPlatformServiceEgressCredentialRefusedAfterChange(t, false)
}
func TestPlatformServiceEgressCredentialRefusedAfterChangePostgres(t *testing.T) {
	testPlatformServiceEgressCredentialRefusedAfterChange(t, true)
}
func TestPlatformServiceEgressCheckBindsTheCallsCredentialSQLite(t *testing.T) {
	testPlatformServiceEgressCheckBindsTheCallsCredential(t, false)
}
func TestPlatformServiceEgressCheckBindsTheCallsCredentialPostgres(t *testing.T) {
	testPlatformServiceEgressCheckBindsTheCallsCredential(t, true)
}
