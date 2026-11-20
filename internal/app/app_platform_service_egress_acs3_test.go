package app

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/transport"
)

// platformServiceACS3Fixture is the egress installation of platformServiceCredentialFixture with an
// ACS3 binding named qwen-billing reviewed for the given principals and actions.
func platformServiceACS3Fixture(t *testing.T, pg bool, principals string, actions ...string) (*Server, platformActorSession, string, platformCredentialMetadata) {
	t.Helper()
	platformACS3Now, platformACS3Nonce = func() time.Time { return acs3FixtureNow }, func() (string, error) { return acs3FixtureNonce, nil }
	t.Cleanup(func() { platformACS3Now, platformACS3Nonce = time.Now, platformACS3RandomNonce })
	s, admin, installation, _ := platformServiceCredentialFixture(t, pg, platformPrincipalsServiceOnly)
	credential, err := s.changePlatformCredential(admin, platformCredentialChange{kind: "create", label: "Qwen billing", value: acs3CredentialJSON(acs3FixtureToken)})
	if err != nil {
		t.Fatal(err)
	}
	acs3Activate(t, s, admin, installation, credential, principals, actions...)
	return s, admin, installation, credential
}

func platformServiceACS3Admit(action, version string) transport.EgressAdmit {
	admit := platformServiceEgressAdmit()
	admit.Credential = "qwen-billing"
	admit.SigningScheme, admit.SigningAction, admit.SigningVersion = "acs3", action, version
	return admit
}

// platformServiceACS3Sign loads the authority for a signed call the way an admit does and signs it.
func platformServiceACS3Sign(t *testing.T, s *Server, state platformServiceEgressSessionState, admit transport.EgressAdmit) (control.NetworkHTTPRequestCommand, []byte, []byte, platformSecretBindingUse) {
	t.Helper()
	a, err := s.loadServiceEgressForSessionAdmit(context.Background(), state, admit)
	if err != nil {
		t.Fatal("load signing authority:", err)
	}
	defer a.Close()
	command, raw, sig, err := a.signServiceEgressCommand(state.sessionID, admit, time.Now().UnixMilli(), state.expiresMS)
	if err != nil {
		t.Fatal("sign call:", err)
	}
	return command, raw, sig, a.binding
}

func testPlatformServiceEgressACS3Signed(t *testing.T, pg bool) {
	s, _, installation, _ := platformServiceACS3Fixture(t, pg, platformPrincipalsServiceOnly, "QueryAccountBalance@2017-12-14")
	state := platformServiceEgressState(t, s, installation, platformServiceCredentialResources...)
	admit := platformServiceACS3Admit("QueryAccountBalance", "2017-12-14")
	before := platformCredentialOpenCount.Load()
	command, raw, _, binding := platformServiceACS3Sign(t, s, state, admit)
	if platformCredentialOpenCount.Load() != before+1 {
		t.Fatal("the credential was not opened exactly once for the call")
	}
	if command.CredentialName != "qwen-billing" || command.SigningScheme != "acs3" || command.CredentialHeader != "" || len(command.CredentialValue) != 0 || binding.operation != "acs3_hmac_sha256" {
		t.Fatalf("command %+v", command)
	}
	if command.ActorScope != platformServiceEgressActorScope(state.sessionID) {
		t.Fatal("the call is not the service's")
	}
	headers, ok := command.SignedHeaderList()
	if !ok {
		t.Fatal("signed header arrays differ in length")
	}
	want, err := appplatform.ACS3Sign(appplatform.ACS3Input{Method: admit.Method, Target: admit.URL, ContentType: admit.ContentType, BodySHA256: admit.BodySHA256,
		Action: "QueryAccountBalance", Version: "2017-12-14", Date: acs3FixtureNow, Nonce: acs3FixtureNonce, AccessKeyID: acs3FixtureKeyID, SecurityToken: []byte(acs3FixtureToken)}, []byte(acs3FixtureSecret))
	if err != nil {
		t.Fatal(err)
	}
	for i := range want {
		if headers[i].Name != want[i].Name || !bytes.Equal(headers[i].Value, want[i].Value) {
			t.Fatalf("signed header %s differs from the independent computation", want[i].Name)
		}
	}
	// The AccessKey secret is in no byte of the command, in any encoding it could be carried in.
	for _, secret := range []string{acs3FixtureSecret, base64.StdEncoding.EncodeToString([]byte(acs3FixtureSecret)), hex.EncodeToString([]byte(acs3FixtureSecret))} {
		if bytes.Contains(raw, []byte(secret)) {
			t.Fatal("the AccessKey secret is in the signed command")
		}
	}
	if err := control.ValidateNetworkHTTPRequestMetadata(command, platformServiceEgressAuthorityOf(t, s, state), time.Now().UnixMilli()); err != nil {
		t.Fatal("the runner would refuse the command:", err)
	}
	// What the call keeps after the send holds neither the signature nor the token.
	kept := newPlatformServiceEgressCall(command, raw, binding)
	for _, h := range []string{"x-acs-security-token", "authorization"} {
		for i, name := range kept.command.SignedHeaderNames {
			if name == h && (bytes.Contains(kept.command.SignedHeaderValues[i], []byte(acs3FixtureToken)) || bytes.Contains(kept.command.SignedHeaderValues[i], appplatform.ACS3HeaderValue(headers, "authorization")[len(appplatform.ACS3HeaderValue(headers, "authorization"))-64:])) {
				t.Fatalf("the kept call still holds the %s value", h)
			}
		}
	}
	if !kept.signed || !kept.consistent {
		t.Fatalf("kept call %+v", kept)
	}
	nonce := hex.EncodeToString(bytes.Repeat([]byte{7}, 32))
	if _, _, err := s.approveServiceEgressCheck(context.Background(), state, kept, nonce); err != nil {
		t.Fatal("check refused for a good signed call:", err)
	}
}

func platformServiceEgressAuthorityOf(t *testing.T, s *Server, state platformServiceEgressSessionState) control.Authority {
	t.Helper()
	a, err := s.loadServiceEgressForSession(context.Background(), state)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	return a.authority
}

func testPlatformServiceEgressACS3DeniedBeforeDecrypt(t *testing.T, pg bool) {
	s, admin, installation, credential := platformServiceACS3Fixture(t, pg, platformPrincipalsServiceOnly, "QueryAccountBalance@2017-12-14")
	state := platformServiceEgressState(t, s, installation, platformServiceCredentialResources...)
	ctx := context.Background()
	denied := func(what string, admit transport.EgressAdmit) {
		t.Helper()
		before := platformCredentialOpenCount.Load()
		a, err := s.loadServiceEgressForSessionAdmit(ctx, state, admit)
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
	denied("an action outside the policy", platformServiceACS3Admit("DescribeInstances", "2017-12-14"))
	denied("a version outside the policy", platformServiceACS3Admit("QueryAccountBalance", "2014-05-26"))
	plain := platformServiceCredentialAdmit()
	plain.Credential = "qwen-billing"
	denied("a plain credential call against a signing binding", plain)
	other := platformServiceACS3Admit("QueryAccountBalance", "2017-12-14")
	other.Credential = "upstream-api"
	denied("a signing call against a bearer binding", other)
	badTarget := platformServiceACS3Admit("QueryAccountBalance", "2017-12-14")
	badTarget.URL = "https://api.example.com/v1/x?b=2&a=1"
	denied("a target that is not in canonical form", badTarget)
	// Not for the service: viewer only.
	acs3Activate(t, s, admin, installation, credential, "", "QueryAccountBalance@2017-12-14")
	denied("a viewer-only binding", platformServiceACS3Admit("QueryAccountBalance", "2017-12-14"))
	// A binding whose credential holds something else than an AccessKey is a denial, not a signature.
	bad, err := s.changePlatformCredential(admin, platformCredentialChange{kind: "create", label: "Not a key", value: []byte("disposable-upstream-value")})
	if err != nil {
		t.Fatal(err)
	}
	acs3Activate(t, s, admin, installation, credential, platformPrincipalsServiceOnly, "QueryAccountBalance@2017-12-14")
	mustExec(t, s.st, `UPDATE app_platform_secret_bindings SET credential_id=?,credential_value_revision=? WHERE name='qwen-billing'`, bad.ID, bad.ValueRevision)
	if a, err := s.loadServiceEgressForSessionAdmit(ctx, state, platformServiceACS3Admit("QueryAccountBalance", "2017-12-14")); err == nil {
		a.Close()
		t.Fatal("signed with a value that is not an AccessKey")
	}
}

func testPlatformServiceEgressACS3RefusedAfterChange(t *testing.T, pg bool) {
	for name, change := range map[string]func(t *testing.T, s *Server, admin platformActorSession, installation string, credential platformCredentialMetadata){
		"binding revoked": func(t *testing.T, s *Server, admin platformActorSession, installation string, _ platformCredentialMetadata) {
			id := scalar[string](t, s.st, `SELECT id FROM app_platform_secret_bindings WHERE installation_id=? AND name='qwen-billing'`, installation)
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
			if _, err := s.consumePlatformCredentialReview(admin, rotation.ID, rotation.SHA256, acs3CredentialJSON("")); err != nil {
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
			acs3Activate(t, s, admin, installation, credential, "", "QueryAccountBalance@2017-12-14")
		},
		"actions re-reviewed": func(t *testing.T, s *Server, admin platformActorSession, installation string, credential platformCredentialMetadata) {
			acs3Activate(t, s, admin, installation, credential, platformPrincipalsServiceOnly, "DescribeBill@2017-12-14", "QueryAccountBalance@2017-12-14")
		},
		"action removed from the stored policy": func(t *testing.T, s *Server, _ platformActorSession, installation string, _ platformCredentialMetadata) {
			current, err := parsePlatformSecretBindingPolicy([]byte(scalar[string](t, s.st, `SELECT policy_json FROM app_platform_secret_bindings WHERE name='qwen-billing'`)))
			if err != nil {
				t.Fatal(err)
			}
			policy, err := platformSecretBindingPolicyWith("acs3_hmac_sha256", current.networkPolicySHA256, platformPrincipalsServiceOnly, "DescribeBill@2017-12-14")
			if err != nil {
				t.Fatal(err)
			}
			mustExec(t, s.st, `UPDATE app_platform_secret_bindings SET policy_json=? WHERE installation_id=? AND name='qwen-billing'`, string(policy), installation)
		},
	} {
		t.Run(name, func(t *testing.T) {
			s, admin, installation, credential := platformServiceACS3Fixture(t, pg, platformPrincipalsServiceOnly, "QueryAccountBalance@2017-12-14")
			state := platformServiceEgressState(t, s, installation, platformServiceCredentialResources...)
			command, raw, _, binding := platformServiceACS3Sign(t, s, state, platformServiceACS3Admit("QueryAccountBalance", "2017-12-14"))
			nonce := hex.EncodeToString(bytes.Repeat([]byte{7}, 32))
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

// A signed command is checkable only as signed, against the ACS3 binding the admit resolved, and a
// plain command only against a plain binding.
func testPlatformServiceEgressACS3CheckBindsTheCallsKind(t *testing.T, pg bool) {
	s, _, installation, _ := platformServiceACS3Fixture(t, pg, platformPrincipalsServiceOnly, "QueryAccountBalance@2017-12-14")
	state := platformServiceEgressState(t, s, installation, platformServiceCredentialResources...)
	command, raw, _, binding := platformServiceACS3Sign(t, s, state, platformServiceACS3Admit("QueryAccountBalance", "2017-12-14"))
	nonce := hex.EncodeToString(bytes.Repeat([]byte{7}, 32))
	ctx := context.Background()
	if _, _, err := s.approveServiceEgressCheck(ctx, state, newPlatformServiceEgressCall(command, raw, platformSecretBindingUse{}), nonce); err == nil {
		t.Fatal("signed command checked without its binding")
	}
	other := binding
	other.revision++
	if _, _, err := s.approveServiceEgressCheck(ctx, state, newPlatformServiceEgressCall(command, raw, other), nonce); err == nil {
		t.Fatal("signed command checked against a different binding revision")
	}
	// The same command re-labelled as an unsigned one is not checkable against the ACS3 binding.
	relabelled := command
	relabelled.SigningScheme = ""
	relabelled.SignedHeaderNames, relabelled.SignedHeaderValues = nil, nil
	if _, _, err := s.approveServiceEgressCheck(ctx, state, newPlatformServiceEgressCall(relabelled, raw, binding), nonce); err == nil {
		t.Fatal("an unsigned command was checked against an ACS3 binding")
	}
}

func TestPlatformServiceEgressACS3SignedSQLite(t *testing.T) {
	testPlatformServiceEgressACS3Signed(t, false)
}
func TestPlatformServiceEgressACS3SignedPostgres(t *testing.T) {
	testPlatformServiceEgressACS3Signed(t, true)
}
func TestPlatformServiceEgressACS3DeniedBeforeDecryptSQLite(t *testing.T) {
	testPlatformServiceEgressACS3DeniedBeforeDecrypt(t, false)
}
func TestPlatformServiceEgressACS3DeniedBeforeDecryptPostgres(t *testing.T) {
	testPlatformServiceEgressACS3DeniedBeforeDecrypt(t, true)
}
func TestPlatformServiceEgressACS3RefusedAfterChangeSQLite(t *testing.T) {
	testPlatformServiceEgressACS3RefusedAfterChange(t, false)
}
func TestPlatformServiceEgressACS3RefusedAfterChangePostgres(t *testing.T) {
	testPlatformServiceEgressACS3RefusedAfterChange(t, true)
}
func TestPlatformServiceEgressACS3CheckBindsTheCallsKindSQLite(t *testing.T) {
	testPlatformServiceEgressACS3CheckBindsTheCallsKind(t, false)
}
func TestPlatformServiceEgressACS3CheckBindsTheCallsKindPostgres(t *testing.T) {
	testPlatformServiceEgressACS3CheckBindsTheCallsKind(t, true)
}
