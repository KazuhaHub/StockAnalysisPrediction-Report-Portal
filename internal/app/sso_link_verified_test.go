package app

import "testing"

// Matching by email is only as safe as the address the IdP vouches for. An OIDC provider says so
// in a claim; a login whose email it does not vouch for must not adopt anybody (the nOAuth class).
//
// Entra is the case that motivates it: there, email is a directory field the tenant's admin fills
// in, unverified, and Entra never sends email_verified. Its own signal is xms_edov ("email domain
// owner verified"), an optional claim the app registration has to request.

func oidcEmailLogin(issuer string, claims map[string]any) ssoIdentity {
	if claims == nil {
		claims = map[string]any{}
	}
	claims["email"] = "kazuha@corp.example"
	return ssoIdentity{Provider: "oidc", Issuer: issuer, Subject: "opaque-sub", Claims: claims}
}

var oidcEmailProvider = SSOProvider{Kind: "oidc", Slug: "oidc", LinkBy: LinkByEmail, AttrEmail: "email"}

func TestLinkByEmailOIDCNeedsAVerifiedEmail(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		claims map[string]any
		adopt  bool
	}{
		{"claim absent", nil, false},
		{"claim false", map[string]any{"email_verified": false}, false},
		{"claim the string false", map[string]any{"email_verified": "false"}, false},
		{"claim true", map[string]any{"email_verified": true}, true},
		{"claim the string true", map[string]any{"email_verified": "true"}, true},
		{"only xms_edov, not an Entra issuer", map[string]any{"xms_edov": true}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := linkByServer(t)
			name, _, err := s.resolveSSOAccount(oidcEmailProvider, oidcEmailLogin("https://idp.example", tc.claims))
			if tc.adopt && (err != nil || name != "kazuha") {
				t.Fatalf("a verified email did not adopt: %q, %v", name, err)
			}
			if !tc.adopt {
				if err == nil {
					t.Fatalf("an unverified email adopted %q", name)
				}
				if resolveFailCode(err) != "sso_email_unverified" {
					t.Errorf("code %q, want sso_email_unverified", resolveFailCode(err))
				}
				if _, ok := s.st.FindIdentity("oidc", "https://idp.example", "opaque-sub"); ok {
					t.Error("a refused login still wrote a binding")
				}
			}
		})
	}
}

func TestLinkByEmailEntraNeedsXmsEdov(t *testing.T) {
	t.Parallel()
	for _, issuer := range []string{
		"https://login.microsoftonline.com/11111111-2222-3333-4444-555555555555/v2.0",
		"https://sts.windows.net/11111111-2222-3333-4444-555555555555/",
		"https://login.microsoftonline.us/11111111-2222-3333-4444-555555555555/v2.0",
		"https://login.partner.microsoftonline.cn/11111111-2222-3333-4444-555555555555/v2.0",
	} {
		t.Run(issuer, func(t *testing.T) {
			t.Parallel()
			s := linkByServer(t)
			// email_verified alone does not count for Entra: it is not Entra's signal, and accepting
			// it would let a claims-mapping policy that emits it stand in for domain ownership.
			if _, _, err := s.resolveSSOAccount(oidcEmailProvider,
				oidcEmailLogin(issuer, map[string]any{"email_verified": true})); err == nil {
				t.Fatal("Entra login without xms_edov adopted an account")
			}
			if _, _, err := s.resolveSSOAccount(oidcEmailProvider,
				oidcEmailLogin(issuer, map[string]any{"xms_edov": false})); err == nil {
				t.Fatal("Entra login with xms_edov=false adopted an account")
			}
			name, _, err := s.resolveSSOAccount(oidcEmailProvider,
				oidcEmailLogin(issuer, map[string]any{"xms_edov": true}))
			if err != nil || name != "kazuha" {
				t.Fatalf("xms_edov=true did not adopt: %q, %v", name, err)
			}
		})
	}
}

// The check is about what decides the account. An existing identity link is not a match on email,
// so a user already bound keeps signing in whatever the token says about verification.
func TestLinkedOIDCLoginDoesNotNeedAVerifiedEmail(t *testing.T) {
	t.Parallel()
	s := linkByServer(t)
	if err := s.st.LinkIdentity(Identity{Username: "kazuha", Provider: "oidc",
		Issuer: "https://idp.example", Subject: "opaque-sub", ProviderSlug: "oidc"}); err != nil {
		t.Fatal(err)
	}
	name, _, err := s.resolveSSOAccount(oidcEmailProvider, oidcEmailLogin("https://idp.example", nil))
	if err != nil || name != "kazuha" {
		t.Fatalf("a linked login was refused: %q, %v", name, err)
	}
}

// Matching never moves an account off the identity it is already bound to. LinkIdentity replaces
// the binding, so adopting here would hand an account owned through IdP A to whoever IdP B says
// shares its email or name, and lock the owner out of SSO. Moving it is the admin's unlink.
func TestLinkByRefusesAnAccountBoundToAnotherIdentity(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		p    SSOProvider
		id   ssoIdentity
	}{
		{"email", SSOProvider{Kind: "saml", Slug: "b", LinkBy: LinkByEmail, AttrEmail: "email"},
			ssoIdentity{Provider: "saml", Issuer: "https://idp-b", Subject: "x",
				Claims: map[string]any{"email": "kazuha@corp.example"}}},
		{"username", SSOProvider{Kind: "saml", Slug: "b", LinkBy: LinkByUsername},
			ssoIdentity{Provider: "saml", Issuer: "https://idp-b", Subject: "kazuha"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := linkByServer(t)
			if err := s.st.LinkIdentity(Identity{Username: "kazuha", Provider: "saml",
				Issuer: "https://idp-a", Subject: "kazuha-at-a", ProviderSlug: "a"}); err != nil {
				t.Fatal(err)
			}
			if name, _, err := s.resolveSSOAccount(tc.p, tc.id); err == nil {
				t.Fatalf("adopted %q away from its existing identity", name)
			} else if resolveFailCode(err) != "not_provisioned" {
				t.Errorf("code %q, want not_provisioned: this refusal depends on the account", resolveFailCode(err))
			}
			if u, ok := s.st.FindIdentity("saml", "https://idp-a", "kazuha-at-a"); !ok || u != "kazuha" {
				t.Error("the original binding was replaced")
			}
		})
	}
}

// SSO step-up calls matchExistingAccount directly, for an account that IS linked to the very
// identity re-proving. The bound-elsewhere guard must only refuse a DIFFERENT identity.
func TestMatchAcceptsTheIdentityTheAccountIsAlreadyBoundTo(t *testing.T) {
	t.Parallel()
	s := linkByServer(t)
	p := SSOProvider{Kind: "saml", Slug: "a", LinkBy: LinkByEmail, AttrEmail: "email"}
	id := ssoIdentity{Provider: "saml", Issuer: "https://idp-a", Subject: "kazuha-at-a",
		Claims: map[string]any{"email": "kazuha@corp.example"}}
	if err := s.st.LinkIdentity(Identity{Username: "kazuha", Provider: id.Provider,
		Issuer: id.Issuer, Subject: id.Subject, ProviderSlug: "a"}); err != nil {
		t.Fatal(err)
	}
	if name, err := s.matchExistingAccount(p, id); err != nil || name != "kazuha" {
		t.Fatalf("step-up match for the bound identity = %q, %v", name, err)
	}
}

// The flag vouches for the email claim and nothing else. A value mapped from another claim
// (preferred_username, upn), or the email-shaped subject fallback, is not what the OP verified.
func TestLinkByEmailOIDCMatchesOnlyTheVouchedClaim(t *testing.T) {
	t.Parallel()
	s := linkByServer(t)
	p := SSOProvider{Kind: "oidc", Slug: "oidc", LinkBy: LinkByEmail, AttrEmail: "preferred_username"}
	id := ssoIdentity{Provider: "oidc", Issuer: "https://idp.example", Subject: "opaque-sub",
		Claims: map[string]any{"email": "someone@else.example", "email_verified": true,
			"preferred_username": "kazuha@corp.example"}}
	if name, _, err := s.resolveSSOAccount(p, id); err == nil {
		t.Fatalf("adopted %q on an unvouched preferred_username", name)
	}
	unmapped := SSOProvider{Kind: "oidc", Slug: "oidc", LinkBy: LinkByEmail}
	if name, _, err := s.resolveSSOAccount(unmapped, ssoIdentity{Provider: "oidc", Issuer: "https://idp.example",
		Subject: "kazuha@corp.example", Claims: map[string]any{"email_verified": true}}); err == nil {
		t.Fatalf("adopted %q on an email-shaped subject the OP never vouched for", name)
	}
	// Mapped to a different claim that happens to carry the same, vouched, address: fine.
	id.Claims["email"] = "KAZUHA@corp.example"
	if name, _, err := s.resolveSSOAccount(p, id); err != nil || name != "kazuha" {
		t.Fatalf("a value equal to the vouched email was refused: %q, %v", name, err)
	}
}

// Refusing is deliberate even when no account would match and JIT is on: falling through would
// create an account on an address the OP does not vouch for, and checking "would it match" first
// would make the refusal depend on which accounts exist.
func TestLinkByEmailUnverifiedDoesNotFallThroughToJIT(t *testing.T) {
	t.Parallel()
	s := linkByServer(t)
	p := oidcEmailProvider
	p.Provisioning = "jit"
	id := oidcEmailLogin("https://idp.example", nil)
	id.Claims["email"] = "newcomer@corp.example"
	_, created, err := s.resolveSSOAccount(p, id)
	if err == nil || created || resolveFailCode(err) != "sso_email_unverified" {
		t.Fatalf("created=%v err=%v, want a sso_email_unverified refusal and no account", created, err)
	}
}

// The host check parses the URL: case and port do not matter, and a lookalike is not Entra.
func TestIsEntraIssuer(t *testing.T) {
	t.Parallel()
	for issuer, want := range map[string]bool{
		"https://LOGIN.microsoftonline.com/t/v2.0":        true,
		"https://login.microsoftonline.com:443/t/v2.0":    true,
		"https://sts.microsoftonline.us/t/":               true,
		"https://sts.chinacloudapi.cn/t/":                 true,
		"https://login.chinacloudapi.cn/t/v2.0":           true,
		"https://login.microsoftonline.com.evil.example/": false,
		"https://evil.example/login.microsoftonline.com":  false,
		"https://idp.example":                             false,
	} {
		if got := isEntraIssuer(issuer); got != want {
			t.Errorf("isEntraIssuer(%q) = %v, want %v", issuer, got, want)
		}
	}
}
