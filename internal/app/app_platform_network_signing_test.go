package app

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

// The foreground request may ask for a signed call (doc 177): a closed nested object, never beside
// a plain credential. The gateway carries a name and a reviewed (action, version) only.
func TestPlatformNetworkHTTPDecoderSigning(t *testing.T) {
	binding := platformBackendRequestBinding{Generation: "1", PolicyRevision: "1", ReleaseSHA256: strings.Repeat("a", 64)}
	decode := func(raw string) (platformNetworkHTTPRequest, error) {
		return readPlatformNetworkHTTPRequest(httptest.NewRecorder(), httptest.NewRequest("POST", "/", strings.NewReader(raw)))
	}
	base := platformNetworkHTTPPayload(t, binding, nil)
	var fields map[string]any
	if json.Unmarshal([]byte(base), &fields) != nil {
		t.Fatal("fixture")
	}
	withSigning := func(signing any, extra map[string]any) string {
		f := map[string]any{}
		for k, v := range fields {
			f[k] = v
		}
		f["signing"] = signing
		for k, v := range extra {
			f[k] = v
		}
		raw, _ := json.Marshal(f)
		return string(raw)
	}
	good := map[string]any{"credential": "qwen-billing", "scheme": "acs3", "action": "QueryAccountBalance", "version": "2017-12-14"}
	parsed, err := decode(withSigning(good, nil))
	want := appplatform.SigningRequest{Credential: "qwen-billing", Scheme: "acs3", Action: "QueryAccountBalance", Version: "2017-12-14"}
	if err != nil || parsed.signing != want || parsed.credential != "" {
		t.Fatalf("%+v %v", parsed, err)
	}
	if plain, err := decode(base); err != nil || plain.signing != (appplatform.SigningRequest{}) {
		t.Fatal("a request without signing carries one", err)
	}
	mut := func(key string, value any) map[string]any {
		m := map[string]any{}
		for k, v := range good {
			m[k] = v
		}
		m[key] = value
		return m
	}
	for name, raw := range map[string]string{
		"beside a plain credential": withSigning(good, map[string]any{"credential": "qwen-billing"}),
		"null":                      withSigning(nil, nil),
		"a string":                  withSigning("acs3", nil),
		"empty object":              withSigning(map[string]any{}, nil),
		"unknown scheme":            withSigning(mut("scheme", "aws4"), nil),
		"bad action":                withSigning(mut("action", "Query Account"), nil),
		"bad version":               withSigning(mut("version", "2017"), nil),
		"bad credential":            withSigning(mut("credential", "Qwen"), nil),
		"extra member":              withSigning(mut("key", "secret"), nil),
		"non-string member":         withSigning(mut("action", 7), nil),
	} {
		if r, err := decode(raw); err == nil || len(r.body) != 0 || r.signing != (appplatform.SigningRequest{}) {
			t.Errorf("%s: accepted", name)
		}
	}
}

func testPlatformNetworkSignedIntent(t *testing.T, pg bool) {
	s, admin, installation, credential := platformServiceACS3Fixture(t, pg, platformPrincipalsBoth, "QueryAccountBalance@2017-12-14")
	binding := platformBackendRequestBinding{Generation: "1", PolicyRevision: "1", ReleaseSHA256: scalar[string](t, s.st, `SELECT release_sha256 FROM app_platform_activations WHERE installation_id=?`, installation)}
	signing := appplatform.SigningRequest{Credential: "qwen-billing", Scheme: "acs3", Action: "QueryAccountBalance", Version: "2017-12-14"}
	var body []byte // the fixture policy allows GET, which has no body
	prepare := func(signing appplatform.SigningRequest, credential string) (platformNetworkRequestIntent, error) {
		return s.preparePlatformNetworkSigned(context.Background(), admin, installation, binding, "GET", "https://api.example.com/v1/x?a=1", "application/json", "", credential, signing, body)
	}
	before := platformCredentialOpenCount.Load()
	intent, err := prepare(signing, "")
	if err != nil {
		t.Fatal("signed foreground request refused:", err)
	}
	defer intent.Close()
	if platformCredentialOpenCount.Load() != before+1 {
		t.Fatal("the credential was not opened exactly once")
	}
	c := intent.command
	headers, ok := c.SignedHeaderList()
	if !ok || c.SigningScheme != "acs3" || c.CredentialName != "qwen-billing" || c.CredentialHeader != "" || len(c.CredentialValue) != 0 || intent.secretBinding.operation != "acs3_hmac_sha256" {
		t.Fatalf("command %+v", c)
	}
	if action, version := appplatform.ACS3ActionOf(headers); action != "QueryAccountBalance" || version != "2017-12-14" ||
		string(appplatform.ACS3HeaderValue(headers, "x-acs-content-sha256")) != platformDigest(body) {
		t.Fatal("the signed headers do not state this request")
	}
	if strings.Contains(string(intent.raw), acs3FixtureSecret) {
		t.Fatal("the AccessKey secret is in the signed command")
	}
	// The same call, still current: nothing changed.
	if err := s.withPlatformNetworkRequestCurrent(context.Background(), admin, installation, intent, nil); err != nil {
		t.Fatal("current check refused:", err)
	}
	// Refusals reach no custody: an action outside the policy, a plain credential beside signing, a
	// service-only binding for a viewer.
	other := signing
	other.Action = "DescribeInstances"
	for name, c := range map[string]struct {
		signing    appplatform.SigningRequest
		credential string
	}{"an action outside the policy": {other, ""}, "signing beside a credential": {signing, "qwen-billing"}} {
		before := platformCredentialOpenCount.Load()
		if i, err := prepare(c.signing, c.credential); err == nil {
			i.Close()
			t.Fatal(name, "was not refused")
		}
		if platformCredentialOpenCount.Load() != before {
			t.Fatal(name, "reached the custody")
		}
	}
	acs3Activate(t, s, admin, installation, credential, platformPrincipalsServiceOnly, "QueryAccountBalance@2017-12-14")
	before = platformCredentialOpenCount.Load()
	if i, err := prepare(signing, ""); err == nil {
		i.Close()
		t.Fatal("a service-only binding signed for a viewer")
	}
	if platformCredentialOpenCount.Load() != before {
		t.Fatal("a service-only binding reached the custody for a viewer")
	}
	// The intent made before the re-review is no longer current.
	if err := s.withPlatformNetworkRequestCurrent(context.Background(), admin, installation, intent, nil); err == nil {
		t.Fatal("an intent survived the binding's re-review")
	}
}

func TestPlatformNetworkSignedIntentSQLite(t *testing.T)   { testPlatformNetworkSignedIntent(t, false) }
func TestPlatformNetworkSignedIntentPostgres(t *testing.T) { testPlatformNetworkSignedIntent(t, true) }
