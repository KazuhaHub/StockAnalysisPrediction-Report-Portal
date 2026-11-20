package app

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

const (
	acs3FixtureKeyID  = "LTAIportalfixture"
	acs3FixtureSecret = "portal-fixture-access-key-secret"
	acs3FixtureToken  = "STS.portal-fixture-security-token-0123456789"
	acs3FixtureNonce  = "00112233445566778899aabbccddeeff"
)

var acs3FixtureNow = time.Date(2026, 11, 18, 3, 4, 5, 0, time.UTC)

func acs3CredentialJSON(token string) []byte {
	raw, _ := json.Marshal(map[string]string{"access_key_id": acs3FixtureKeyID, "access_key_secret": acs3FixtureSecret, "security_token": token})
	return raw
}

// acs3Fixture is an installation with a credential holding an AccessKey and an STS token.
func acs3Fixture(t *testing.T, pg bool, token string) (*Server, platformActorSession, string, platformCredentialMetadata) {
	t.Helper()
	s, admin, installation, _ := platformSecretBindingFixture(t, pg)
	credential, err := s.changePlatformCredential(admin, platformCredentialChange{kind: "create", label: "Qwen billing", value: acs3CredentialJSON(token)})
	if err != nil {
		t.Fatal(err)
	}
	return s, admin, installation, credential
}

func acs3Pairs(items ...string) []platformACS3Action {
	var out []platformACS3Action
	for _, item := range items {
		action, version, _ := strings.Cut(item, "@")
		out = append(out, platformACS3Action{action, version})
	}
	return out
}

func acs3Activate(t *testing.T, s *Server, admin platformActorSession, installation string, credential platformCredentialMetadata, principals string, actions ...string) platformSecretBindingMetadata {
	t.Helper()
	plan, err := s.createPlatformSecretBindingReviewWith(admin, installation, "qwen-billing", credential.ID, "acs3_hmac_sha256", principals, acs3Pairs(actions...))
	if err != nil {
		t.Fatal("review:", err)
	}
	for _, secret := range []string{acs3FixtureSecret, acs3FixtureToken} {
		if strings.Contains(plan.JSON, secret) {
			t.Fatal("binding review contains a secret")
		}
	}
	binding, err := s.consumePlatformSecretBindingReview(admin, plan.ID, plan.SHA256)
	if err != nil {
		t.Fatal("commit:", err)
	}
	return binding
}

func testPlatformACS3BindingReview(t *testing.T, pg bool) {
	s, admin, installation, credential := acs3Fixture(t, pg, acs3FixtureToken)
	// Refused shapes never reach a plan.
	for name, actions := range map[string][]platformACS3Action{
		"no actions":  nil,
		"unsorted":    acs3Pairs("B@2017-12-14", "A@2017-12-14"),
		"duplicate":   acs3Pairs("A@2017-12-14", "A@2017-12-14"),
		"bad action":  acs3Pairs("A B@2017-12-14"),
		"bad version": acs3Pairs("A@2017"),
	} {
		if _, err := s.createPlatformSecretBindingReviewWith(admin, installation, "qwen-billing", credential.ID, "acs3_hmac_sha256", platformPrincipalsServiceOnly, actions); err == nil {
			t.Fatalf("%s: review accepted", name)
		}
	}
	// A plain (non-JSON) credential cannot back an ACS3 binding: the value is validated in custody.
	plain, err := s.changePlatformCredential(admin, platformCredentialChange{kind: "create", label: "Plain", value: []byte("disposable-upstream-value")})
	if err != nil {
		t.Fatal(err)
	}
	before := scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_review_plans`)
	if _, err := s.createPlatformSecretBindingReviewWith(admin, installation, "qwen-billing", plain.ID, "acs3_hmac_sha256", "", acs3Pairs("A@2017-12-14")); err == nil {
		t.Fatal("a credential that is not an AccessKey backed an ACS3 binding")
	}
	if scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_review_plans`) != before {
		t.Fatal("a refused review left a plan")
	}
	// An AccessKey value is never reviewed as a bearer or API-key header: it would be sent whole.
	for _, operation := range []string{"http_bearer", "http_api_key"} {
		if _, err := s.createPlatformSecretBindingReviewWith(admin, installation, "qwen-billing", credential.ID, operation, "", nil); err == nil {
			t.Fatalf("an AccessKey credential was reviewed as %s", operation)
		}
	}
	// An action list on another operation is refused.
	if _, err := s.createPlatformSecretBindingReviewWith(admin, installation, "qwen-billing", credential.ID, "http_bearer", "", acs3Pairs("A@2017-12-14")); err == nil {
		t.Fatal("actions accepted on http_bearer")
	}

	for _, c := range []struct{ principals, stored string }{
		{platformPrincipalsServiceOnly, `"principals":["service"]`},
		{platformPrincipalsBoth, `"principals":["service","viewer"]`},
		{"", `"principals":["viewer"]`},
	} {
		binding := acs3Activate(t, s, admin, installation, credential, c.principals, "QueryAccountBalance@2017-12-14")
		if binding.Operation != "acs3_hmac_sha256" || len(binding.ACS3Actions) != 1 || binding.ACS3Actions[0] != (platformACS3Action{"QueryAccountBalance", "2017-12-14"}) {
			t.Fatalf("binding metadata %+v", binding)
		}
		if scalar[string](t, s.st, `SELECT operation FROM app_platform_secret_bindings WHERE id=?`, binding.ID) != "acs3_hmac_sha256" {
			t.Fatal("operation not stored")
		}
		policy := scalar[string](t, s.st, `SELECT policy_json FROM app_platform_secret_bindings WHERE id=?`, binding.ID)
		if !strings.Contains(policy, c.stored) || !strings.Contains(policy, `"acs3":{"actions":[{"action":"QueryAccountBalance","version":"2017-12-14"}]}`) {
			t.Fatal("stored policy", policy)
		}
		list, err := s.listPlatformSecretBindings(admin, installation)
		if err != nil || len(list) != 1 || len(list[0].ACS3Actions) != 1 {
			t.Fatal("list", list, err)
		}
		detail := scalar[string](t, s.st, `SELECT detail FROM audit_log WHERE action='app_platform.secret_binding_active' AND target_id=? ORDER BY id DESC LIMIT 1`, binding.ID)
		if !strings.Contains(detail, `QueryAccountBalance`) || !strings.Contains(detail, `2017-12-14`) {
			t.Fatal("audit does not record the allowed actions:", detail)
		}
		for _, secret := range []string{acs3FixtureSecret, acs3FixtureToken, acs3FixtureKeyID} {
			if strings.Contains(detail, secret) || strings.Contains(policy, secret) {
				t.Fatal("a secret reached the policy or the audit")
			}
		}
	}
	// Adding an action is a new review of the same binding.
	before2 := scalar[int64](t, s.st, `SELECT revision FROM app_platform_secret_bindings WHERE name='qwen-billing'`)
	widened := acs3Activate(t, s, admin, installation, credential, platformPrincipalsServiceOnly, "DescribeBill@2017-12-14", "QueryAccountBalance@2017-12-14")
	if widened.Revision != before2+1 || len(widened.ACS3Actions) != 2 {
		t.Fatalf("widened binding %+v (was revision %d)", widened, before2)
	}
}

func TestPlatformACS3BindingReviewSQLite(t *testing.T)   { testPlatformACS3BindingReview(t, false) }
func TestPlatformACS3BindingReviewPostgres(t *testing.T) { testPlatformACS3BindingReview(t, true) }

// signPlatformACS3 is the one place the secret meets the request. The fixture clock and nonce make the
// result comparable with an independent computation.
func testPlatformACS3SignInForeground(t *testing.T, pg bool) {
	platformACS3Now, platformACS3Nonce = func() time.Time { return acs3FixtureNow }, func() (string, error) { return acs3FixtureNonce, nil }
	t.Cleanup(func() { platformACS3Now, platformACS3Nonce = time.Now, platformACS3RandomNonce })
	s, admin, installation, credential := acs3Fixture(t, pg, acs3FixtureToken)
	// The call's method and URL must be ones the grant selects: let the grant take POST too.
	post := platformTargetDefault()
	post.Methods = []string{"GET", "POST"}
	platformTargetSetGrant(t, s, installation, platformTargetV2(t, post))
	acs3Activate(t, s, admin, installation, credential, platformPrincipalsBoth, "QueryAccountBalance@2017-12-14")
	e := s.st.poolExec()
	body := []byte(`{"x":1}`)
	call := platformACS3Call{name: "qwen-billing", action: "QueryAccountBalance", version: "2017-12-14", method: "POST",
		target: "https://api.example.com/v1/x?a=1&b=2", contentType: "application/json", bodySHA256: platformDigest(body)}

	for _, principal := range []string{platformPrincipalViewer, platformPrincipalService} {
		before := platformCredentialOpenCount.Load()
		use, headers, err := s.signPlatformACS3(e, installation, principal, call)
		if err != nil {
			t.Fatal(principal, err)
		}
		if platformCredentialOpenCount.Load() != before+1 || use.operation != "acs3_hmac_sha256" || use.header != "" {
			t.Fatal("the value was not opened exactly once, or the use is not an ACS3 use", use)
		}
		want, err := appplatform.ACS3Sign(appplatform.ACS3Input{Method: "POST", Target: call.target, ContentType: call.contentType, BodySHA256: call.bodySHA256,
			Action: call.action, Version: call.version, Date: acs3FixtureNow, Nonce: acs3FixtureNonce, AccessKeyID: acs3FixtureKeyID, SecurityToken: []byte(acs3FixtureToken)}, []byte(acs3FixtureSecret))
		if err != nil {
			t.Fatal(err)
		}
		if len(headers) != len(want) {
			t.Fatalf("%d headers, want %d", len(headers), len(want))
		}
		for i := range want {
			if headers[i].Name != want[i].Name || !bytes.Equal(headers[i].Value, want[i].Value) {
				t.Fatalf("header %s differs", want[i].Name)
			}
		}
		if got := appplatform.ACS3HeaderValue(headers, "x-acs-security-token"); string(got) != acs3FixtureToken {
			t.Fatal("the security token is not signed in")
		}
		for _, h := range headers {
			if bytes.Contains(h.Value, []byte(acs3FixtureSecret)) {
				t.Fatalf("header %s contains the AccessKey secret", h.Name)
			}
		}
	}

	denied := func(what string, principal string, mutate func(*platformACS3Call)) {
		t.Helper()
		c := call
		mutate(&c)
		before := platformCredentialOpenCount.Load()
		_, headers, err := s.signPlatformACS3(e, installation, principal, c)
		if err == nil || headers != nil {
			t.Fatal(what, "was not refused")
		}
		if platformCredentialOpenCount.Load() != before {
			t.Fatal(what, "reached the credential custody")
		}
	}
	denied("an action outside the policy", platformPrincipalViewer, func(c *platformACS3Call) { c.action = "DescribeInstances" })
	denied("a version outside the policy", platformPrincipalViewer, func(c *platformACS3Call) { c.version = "2014-05-26" })
	denied("an unknown binding", platformPrincipalService, func(c *platformACS3Call) { c.name = "no-such-binding" })
	denied("an uncanonical target", platformPrincipalViewer, func(c *platformACS3Call) { c.target = "https://api.example.com/v1/x?b=2&a=1" })

	// A viewer-only binding is not usable for the service, and a service-only one not for the viewer.
	for principals, refused := range map[string]string{"": platformPrincipalService, platformPrincipalsServiceOnly: platformPrincipalViewer} {
		acs3Activate(t, s, admin, installation, credential, principals, "QueryAccountBalance@2017-12-14")
		denied("a principal the review did not name", refused, func(*platformACS3Call) {})
	}
	// An http_bearer binding is not an ACS3 binding, and an ACS3 binding is not a bearer one.
	bearer, err := s.changePlatformCredential(admin, platformCredentialChange{kind: "create", label: "Bearer", value: []byte("disposable-upstream-value")})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := s.createPlatformSecretBindingReviewPrincipals(admin, installation, "bearer-api", bearer.ID, "http_bearer", platformPrincipalsBoth)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.consumePlatformSecretBindingReview(admin, plan.ID, plan.SHA256); err != nil {
		t.Fatal(err)
	}
	denied("a bearer binding asked to sign", platformPrincipalViewer, func(c *platformACS3Call) { c.name = "bearer-api" })
	acs3Activate(t, s, admin, installation, credential, platformPrincipalsBoth, "QueryAccountBalance@2017-12-14")
	if _, _, value, err := s.resolvePlatformSecretBindingCommand(e, installation, "qwen-billing", platformBindingDestination{method: "GET", url: "https://api.example.com/v1/x"}); err == nil {
		clear(value)
		t.Fatal("an ACS3 binding was placed as a plain header")
	}
}

func TestPlatformACS3SignInForegroundSQLite(t *testing.T) { testPlatformACS3SignInForeground(t, false) }
func TestPlatformACS3SignInForegroundPostgres(t *testing.T) {
	testPlatformACS3SignInForeground(t, true)
}

func testPlatformACS3NoTokenSigns(t *testing.T, pg bool) {
	s, admin, installation, credential := acs3Fixture(t, pg, "")
	acs3Activate(t, s, admin, installation, credential, platformPrincipalsServiceOnly, "QueryAccountBalance@2017-12-14")
	call := platformACS3Call{name: "qwen-billing", action: "QueryAccountBalance", version: "2017-12-14", method: "GET", target: "https://api.example.com/v1/x", bodySHA256: platformDigest(nil)}
	_, headers, err := s.signPlatformACS3(s.st.poolExec(), installation, platformPrincipalService, call)
	if err != nil || appplatform.ACS3HeaderValue(headers, "x-acs-security-token") != nil {
		t.Fatal("a credential without a token signed a token header", err)
	}
	if a := appplatform.ACS3HeaderValue(headers, "x-acs-signature-nonce"); len(a) != 32 {
		t.Fatalf("nonce %q", a)
	}
	// A fresh nonce for every signature.
	_, again, err := s.signPlatformACS3(s.st.poolExec(), installation, platformPrincipalService, call)
	if err != nil || bytes.Equal(appplatform.ACS3HeaderValue(headers, "x-acs-signature-nonce"), appplatform.ACS3HeaderValue(again, "x-acs-signature-nonce")) {
		t.Fatal("the nonce repeated", err)
	}
	if _, err := hex.DecodeString(string(appplatform.ACS3HeaderValue(again, "x-acs-signature-nonce"))); err != nil {
		t.Fatal("nonce is not hex")
	}
}

func TestPlatformACS3NoTokenSignsSQLite(t *testing.T)   { testPlatformACS3NoTokenSigns(t, false) }
func TestPlatformACS3NoTokenSignsPostgres(t *testing.T) { testPlatformACS3NoTokenSigns(t, true) }
