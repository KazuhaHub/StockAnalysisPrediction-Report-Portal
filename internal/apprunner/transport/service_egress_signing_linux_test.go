//go:build linux

package transport

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
)

const egrSigningRequest = `{"requestId":"req-1","method":"GET","url":"https://api.example.com/v1/x","accept":"application/json","contentType":"","bodyBase64":"","signing":{"credential":"qwen-billing","scheme":"acs3","action":"QueryAccountBalance","version":"2017-12-14"}}`

// egrPlaceSigning is what Portal does for a signed call: it computes the ACS3 headers for the request
// the admit described, with a key the runner never sees.
func egrPlaceSigning(name, action, version string) func(*control.NetworkHTTPRequestCommand) {
	return func(c *control.NetworkHTTPRequestCommand) {
		headers, err := appplatform.ACS3Sign(appplatform.ACS3Input{
			Method: c.Method, Target: c.URL, ContentType: c.ContentType, BodySHA256: c.BodySHA256,
			Action: action, Version: version, Date: time.Now().UTC(), Nonce: strings.Repeat("ab", 16),
			AccessKeyID: "LTAIfixture", SecurityToken: []byte("STS.fixture-token"),
		}, []byte("fixture-secret"))
		if err != nil {
			panic(err)
		}
		c.CredentialName, c.CredentialHeader, c.CredentialValue = name, "", nil
		c.SigningScheme = appplatform.ACS3Scheme
		c.SetSignedHeaders(headers)
	}
}

func TestServiceEgressAdmitSigningCanonical(t *testing.T) {
	base := EgressAdmit{Method: "GET", URL: "https://api.example.com/v1/x", Accept: "application/json", BodySHA256: strings.Repeat("a", 64), Credential: "qwen-billing"}
	signed := base
	signed.SigningScheme, signed.SigningAction, signed.SigningVersion = "acs3", "QueryAccountBalance", "2017-12-14"
	raw, err := EncodeEgressAdmit(signed)
	if err != nil || !strings.HasSuffix(string(raw), `"credential":"qwen-billing","signingScheme":"acs3","signingAction":"QueryAccountBalance","signingVersion":"2017-12-14"}`) {
		t.Fatal("signing members not last in the admit", string(raw), err)
	}
	if got, err := DecodeEgressAdmit(raw); err != nil || got != signed {
		t.Fatal("admit roundtrip", err)
	}
	plain, _ := EncodeEgressAdmit(base)
	if strings.Contains(string(plain), "signing") {
		t.Fatal("an admit without signing changed shape")
	}
	for name, mutate := range map[string]func(*EgressAdmit){
		"scheme without an action":    func(a *EgressAdmit) { a.SigningAction = "" },
		"scheme without a version":    func(a *EgressAdmit) { a.SigningVersion = "" },
		"scheme without a credential": func(a *EgressAdmit) { a.Credential = "" },
		"unknown scheme":              func(a *EgressAdmit) { a.SigningScheme = "aws4" },
		"bad action":                  func(a *EgressAdmit) { a.SigningAction = "Query Account" },
		"bad version":                 func(a *EgressAdmit) { a.SigningVersion = "2017" },
		"action without the scheme":   func(a *EgressAdmit) { a.SigningScheme = "" },
		"version without the scheme":  func(a *EgressAdmit) { a.SigningScheme, a.SigningAction = "", "" },
	} {
		bad := signed
		mutate(&bad)
		if _, err := EncodeEgressAdmit(bad); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestServiceEgressSignedRequestReachesPortalAsAnAdmitAndTheExecutorGetsOnlyTheSignedHeaders(t *testing.T) {
	f := newEgrFixture(t)
	p := f.open(egrOptions{resources: egrCredentialResources})
	p.mu.Lock()
	p.mutate = egrPlaceSigning("qwen-billing", "QueryAccountBalance", "2017-12-14")
	p.mu.Unlock()
	var seen control.NetworkHTTPRequestCommand
	egrHooks.Store(f, egrBackgroundHook(func(_ context.Context, c control.NetworkHTTPRequestCommand) { seen = c }))
	t.Cleanup(func() { egrHooks.Delete(f) })
	reply := f.network(t, egrSigningRequest)
	if reply.status != 200 || strings.Contains(reply.body, "fixture-secret") || strings.Contains(reply.body, "STS.fixture-token") {
		t.Fatalf("reply %d %s", reply.status, reply.body)
	}
	select {
	case admit := <-p.admits:
		if admit.Credential != "qwen-billing" || admit.SigningScheme != "acs3" || admit.SigningAction != "QueryAccountBalance" || admit.SigningVersion != "2017-12-14" {
			t.Fatalf("Portal was not told the signing: %+v", admit)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no admit")
	}
	headers, ok := seen.SignedHeaderList()
	if !ok || seen.SigningScheme != "acs3" || seen.CredentialName != "qwen-billing" || seen.CredentialHeader != "" || len(seen.CredentialValue) != 0 {
		t.Fatalf("the executor did not receive the signed command: %+v", seen)
	}
	if action, version := appplatform.ACS3ActionOf(headers); action != "QueryAccountBalance" || version != "2017-12-14" {
		t.Fatal("signed action differs from the admit")
	}
}

func TestServiceEgressSignedRequestNeedsSecretsUseInTheSession(t *testing.T) {
	f := newEgrFixture(t)
	p := f.open(egrOptions{})
	egrError(t, f.network(t, egrSigningRequest), 403, "denied")
	select {
	case admit := <-p.admits:
		t.Fatalf("a signing request reached Portal from a session without secrets.use: %+v", admit)
	case <-time.After(200 * time.Millisecond):
	}
	if f.executed.Load() != 0 {
		t.Fatal("executed")
	}
}

// The runner holds no key, so what it can do is refuse a command that does not carry exactly the
// signing the admit asked for.
func TestServiceEgressSignedCommandMustMatchTheAdmit(t *testing.T) {
	for name, c := range map[string]struct {
		request string
		mutate  func(*control.NetworkHTTPRequestCommand)
	}{
		"other binding name":          {egrSigningRequest, egrPlaceSigning("other", "QueryAccountBalance", "2017-12-14")},
		"other action":                {egrSigningRequest, egrPlaceSigning("qwen-billing", "DescribeInstances", "2017-12-14")},
		"other version":               {egrSigningRequest, egrPlaceSigning("qwen-billing", "QueryAccountBalance", "2014-05-26")},
		"signing asked, none":         {egrSigningRequest, nil},
		"signing given, none asked":   {egrGetRequest, egrPlaceSigning("qwen-billing", "QueryAccountBalance", "2017-12-14")},
		"a bearer credential instead": {egrSigningRequest, egrPlaceCredential("qwen-billing", "Authorization", "Bearer s3cret-token-value")},
		"credential and signing": {egrSigningRequest, func(c *control.NetworkHTTPRequestCommand) {
			egrPlaceSigning("qwen-billing", "QueryAccountBalance", "2017-12-14")(c)
			c.CredentialHeader, c.CredentialValue = "X-API-Key", []byte("k")
		}},
	} {
		t.Run(name, func(t *testing.T) {
			f := newEgrFixture(t)
			p := f.open(egrOptions{resources: egrCredentialResources})
			p.mu.Lock()
			p.mutate = c.mutate
			p.mu.Unlock()
			reply := f.network(t, c.request)
			egrError(t, reply, 403, "denied")
			if f.executed.Load() != 0 || strings.Contains(reply.body, "fixture-secret") {
				t.Fatal("a mismatched command executed or leaked")
			}
		})
	}
}
