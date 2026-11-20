package control

import (
	"strings"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

// signedNetworkFixture is a command whose URL, content type and body digest match the signed headers,
// the way Portal builds it.
func signedNetworkFixture(t *testing.T, token string) NetworkHTTPRequestCommand {
	t.Helper()
	c, _, _, _, _ := networkFixture(t)
	headers, err := appplatform.ACS3Sign(appplatform.ACS3Input{
		Method: c.Method, Target: c.URL, ContentType: c.ContentType, BodySHA256: c.BodySHA256,
		Action: "QueryAccountBalance", Version: "2017-12-14", Date: time.Date(2026, 11, 17, 8, 30, 15, 0, time.UTC),
		Nonce: strings.Repeat("ab", 16), AccessKeyID: "LTAIfixture", SecurityToken: []byte(token),
	}, []byte("fixture-secret"))
	if err != nil {
		t.Fatal(err)
	}
	c.CredentialName = "qwen-billing"
	c.SigningScheme = appplatform.ACS3Scheme
	c.SetSignedHeaders(headers)
	return c
}

func TestSignedNetworkHTTPRequestIsAcceptedAndPrivate(t *testing.T) {
	for _, token := range []string{"", "STS.fixture-token"} {
		c := signedNetworkFixture(t, token)
		_, fixtureKey, fixtureA, fixtureNow, _ := networkFixture(t)
		raw, sig, err := SignNetworkHTTPRequest(c, fixtureKey, fixtureA, fixtureNow)
		if err != nil {
			t.Fatalf("token %q: %v", token, err)
		}
		v, err := VerifyNetworkHTTPRequest(raw, sig, fixtureA, fixtureNow)
		if err != nil {
			t.Fatal(err)
		}
		copy := v.Command()
		if copy.SigningScheme != "acs3" || len(copy.SignedHeaderNames) != len(c.SignedHeaderNames) {
			t.Fatal("signing lost")
		}
		copy.SignedHeaderValues[0][0] ^= 1
		if string(v.Command().SignedHeaderValues[0]) == string(copy.SignedHeaderValues[0]) {
			t.Fatal("signed header value aliased")
		}
		v.Clear()
		if len(v.Command().SignedHeaderValues) != 0 {
			t.Fatal("Clear left signed headers")
		}
	}
}

func TestSignedNetworkHTTPRequestRefusesAnythingTheRunnerCouldBeAskedToAlter(t *testing.T) {
	_, key, a, now, _ := networkFixture(t)
	for name, mutate := range map[string]func(*NetworkHTTPRequestCommand){
		"headers without the scheme": func(c *NetworkHTTPRequestCommand) { c.SigningScheme = "" },
		"unknown scheme":             func(c *NetworkHTTPRequestCommand) { c.SigningScheme = "aws4" },
		"scheme without headers":     func(c *NetworkHTTPRequestCommand) { c.SignedHeaderNames, c.SignedHeaderValues = nil, nil },
		"an extra header name": func(c *NetworkHTTPRequestCommand) {
			c.SignedHeaderNames, c.SignedHeaderValues = append(c.SignedHeaderNames, "x-evil"), append(c.SignedHeaderValues, []byte("1"))
		},
		"a credential header beside": func(c *NetworkHTTPRequestCommand) {
			c.CredentialHeader, c.CredentialValue = "Authorization", []byte("Bearer x")
		},
		"a credential value beside":  func(c *NetworkHTTPRequestCommand) { c.CredentialValue = []byte("x") },
		"no credential name":         func(c *NetworkHTTPRequestCommand) { c.CredentialName = "" },
		"a body digest that differs": func(c *NetworkHTTPRequestCommand) { c.BodySHA256 = strings.Repeat("c", 64) },
		"a different content type":   func(c *NetworkHTTPRequestCommand) { c.ContentType = "text/plain" },
		"a different host": func(c *NetworkHTTPRequestCommand) {
			c.URL = strings.Replace(c.URL, "api.example.com", "evil.example.com", 1)
		},
		"ten pairs": func(c *NetworkHTTPRequestCommand) {
			c.SignedHeaderNames, c.SignedHeaderValues = append(c.SignedHeaderNames, c.SignedHeaderNames[:3]...), append(c.SignedHeaderValues, c.SignedHeaderValues[:3]...)
		},
		"a header value changed": func(c *NetworkHTTPRequestCommand) { c.SignedHeaderValues[0] = append(c.SignedHeaderValues[0], ' ') },
		"a signed list that does not match": func(c *NetworkHTTPRequestCommand) {
			c.SignedHeaderNames, c.SignedHeaderValues = c.SignedHeaderNames[1:], c.SignedHeaderValues[1:]
		},
	} {
		c := signedNetworkFixture(t, "STS.fixture-token")
		mutate(&c)
		if raw, sig, err := SignNetworkHTTPRequest(c, key, a, now); err == nil || raw != nil || sig != nil {
			t.Errorf("%s: signed", name)
		}
	}
}

func TestSignedNetworkHTTPStreamRefusesSigning(t *testing.T) {
	c, key, a, now, _ := networkStreamFixture(t)
	headers, _ := appplatform.ACS3Sign(appplatform.ACS3Input{
		Method: c.Method, Target: c.URL, ContentType: c.ContentType, BodySHA256: c.BodySHA256,
		Action: "QueryAccountBalance", Version: "2017-12-14", Date: time.Unix(1700000000, 0), Nonce: strings.Repeat("ab", 16), AccessKeyID: "id",
	}, []byte("secret"))
	c.CredentialName, c.SigningScheme = "qwen-billing", "acs3"
	c.SetSignedHeaders(headers)
	if raw, _, err := SignNetworkHTTPStreamRequest(c, key, a, now); err == nil || raw != nil {
		t.Fatal("a stream command carried a signing")
	}
}
