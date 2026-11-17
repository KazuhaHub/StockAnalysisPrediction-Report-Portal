package appplatform

import (
	"strings"
	"testing"
	"time"

	protocol "github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal-Apps/packages/extensionprotocol"
)

func testPublic() Public {
	return Public{Routes: []PublicRoute{
		{ID: "history", Path: "/v1/public/history", Query: []string{"from", "to"}, CacheSeconds: 10, ResponseBytes: 1000},
		{ID: "status", Path: "/v1/public/status", CacheSeconds: 10, ResponseBytes: 1000},
	}, Document: "public/index.html"}
}

func TestPublicPolicyRoundTripAndBounds(t *testing.T) {
	p := testPublic()
	def := DefaultPublicPolicy(p)
	if def.ClientPerMinute != 60 || def.BackendPerSecond != 5 || !def.Document || len(def.Routes) != 2 {
		t.Fatalf("%+v", def)
	}
	raw := CanonicalPublicPolicy(def)
	got, err := ParsePublicPolicy(raw, p)
	if err != nil || CanonicalPublicPolicy(got) != raw {
		t.Fatal(err)
	}
	bad := []string{
		strings.Replace(raw, `"client_per_minute":60`, `"client_per_minute":601`, 1),
		strings.Replace(raw, `"client_per_minute":60`, `"client_per_minute":0`, 1),
		strings.Replace(raw, `"backend_per_second":5`, `"backend_per_second":21`, 1),
		strings.Replace(raw, `"history"`, `"nope"`, 1),
		strings.Replace(raw, `["history","status"]`, `["status","history"]`, 1),
		strings.Replace(raw, `["history","status"]`, `[]`, 1),
		strings.Replace(raw, `["history","status"]`, `["history","history"]`, 1),
		strings.Replace(raw, `1-dev.1`, `1-dev.2`, 1),
		strings.Replace(raw, `{`, `{"extra":1,`, 1),
		` ` + raw, raw + ` `, `{}`, `null`, ``,
	}
	for _, b := range bad {
		if _, err := ParsePublicPolicy(b, p); err == nil {
			t.Fatalf("accepted %q", b)
		}
	}
	noDoc := p
	noDoc.Document = ""
	if _, err := ParsePublicPolicy(raw, noDoc); err == nil {
		t.Fatal("document enabled without a declared document")
	}
	if d := DefaultPublicPolicy(noDoc); d.Document {
		t.Fatal("default enabled a missing document")
	}
}

func TestCanonicalPublicQuery(t *testing.T) {
	declared := []string{"from", "limit", "to"}
	good := map[string]string{"": "", "to=b&from=a": "from=a&to=b", "limit=10": "limit=10", "from=a%20b": "from=a+b", "from=": "from="}
	for in, want := range good {
		got, err := CanonicalPublicQuery(declared, in)
		if err != nil || got != want {
			t.Fatalf("%q: %q %v", in, got, err)
		}
	}
	bad := []string{"x=1", "from=a&from=b", "from", "=1", "from=\x01", "from=%00", "from=%ff", "from=" + strings.Repeat("a", 129), "from=a&&to=b", "from=%zz",
		"from=" + strings.Repeat("a", 128) + "&to=" + strings.Repeat("b", 128) + "&limit=" + strings.Repeat("c", 128) + "&" + strings.Repeat("z", 200), "From=1", "from=é", "from=a#b"}
	for _, in := range bad {
		if _, err := CanonicalPublicQuery(declared, in); err == nil {
			t.Fatalf("accepted %q", in)
		}
	}
	if _, err := CanonicalPublicQuery(nil, "a=1"); err == nil {
		t.Fatal("a route with no declared names accepted a query")
	}
	// Three 128 byte values stay inside the 512 byte total; a fourth name cannot exist.
	long := "from=" + strings.Repeat("a", 128) + "&limit=" + strings.Repeat("b", 128) + "&to=" + strings.Repeat("c", 128)
	if _, err := CanonicalPublicQuery(declared, long); err != nil {
		t.Fatal(err)
	}
}

func testPublicSigner() protocol.Signer {
	return protocol.Signer{Issuer: "portal.app/d", Audience: "portal.app/i", AppID: "status", InstanceID: "inst", BootID: "boot", KeyID: "k", Key: []byte(strings.Repeat("k", 32))}
}

func TestPublicAssertionBindsRequestAndNeverVerifiesAsViewer(t *testing.T) {
	signer, now := testPublicSigner(), time.Unix(1_800_000_000, 0)
	token, err := SignPublicAssertion(signer, 3, "history", "/v1/public/history", "from=a", now)
	if err != nil || !IsPublicAssertion(token) || !backendRequestAssertion.MatchString(token) {
		t.Fatal(err, token)
	}
	claims, err := VerifyPublicAssertion(signer, token, "/v1/public/history?from=a", now.Add(time.Second))
	if err != nil || claims.Route != "history" || claims.Generation != 3 || claims.Installation != "i" || claims.Method != "GET" {
		t.Fatalf("%+v %v", claims, err)
	}
	for name, check := range map[string]func() error{
		"other query": func() error { _, e := VerifyPublicAssertion(signer, token, "/v1/public/history?from=b", now); return e },
		"other path":  func() error { _, e := VerifyPublicAssertion(signer, token, "/v1/public/status?from=a", now); return e },
		"expired": func() error {
			_, e := VerifyPublicAssertion(signer, token, "/v1/public/history?from=a", now.Add(31*time.Second))
			return e
		},
		"early": func() error {
			_, e := VerifyPublicAssertion(signer, token, "/v1/public/history?from=a", now.Add(-time.Minute))
			return e
		},
		"other key": func() error {
			o := signer
			o.Key = []byte(strings.Repeat("z", 32))
			_, e := VerifyPublicAssertion(o, token, "/v1/public/history?from=a", now)
			return e
		},
		"other boot": func() error {
			o := signer
			o.BootID = "x"
			_, e := VerifyPublicAssertion(o, token, "/v1/public/history?from=a", now)
			return e
		},
		"tampered": func() error {
			_, e := VerifyPublicAssertion(signer, token[:len(token)-2]+"AA", "/v1/public/history?from=a", now)
			return e
		},
		"no prefix": func() error {
			_, e := VerifyPublicAssertion(signer, strings.TrimPrefix(token, "pub."), "/v1/public/history?from=a", now)
			return e
		},
	} {
		if check() == nil {
			t.Fatal("accepted:", name)
		}
	}
	// Domain separation: the public MAC never verifies as a viewer assertion and the reverse.
	verifier := protocol.NewVerifier(protocol.VerifyConfig{Issuer: signer.Issuer, Audience: signer.Audience, AppID: signer.AppID, InstanceID: signer.InstanceID, BootID: signer.BootID, Keys: map[string][]byte{signer.KeyID: signer.Key}})
	if _, err := verifier.Verify(strings.TrimPrefix(token, "pub."), "GET", "/v1/public/history?from=a", nil, now); err == nil {
		t.Fatal("a public token verified as a viewer assertion")
	}
	viewer, err := signer.Sign("GET", "/v1/public/history?from=a", nil, protocol.Viewer{Subject: "s", Role: "user", OUs: []string{"o"}}, now)
	if err != nil {
		t.Fatal(err)
	}
	if IsPublicAssertion(viewer) {
		t.Fatal("a viewer token looks public")
	}
	if _, err := VerifyPublicAssertion(signer, "pub."+viewer, "/v1/public/history?from=a", now); err == nil {
		t.Fatal("a viewer token verified as public")
	}
	if _, err := SignPublicAssertion(signer, 0, "history", "/v1/public/history", "", now); err == nil {
		t.Fatal("generation 0 signed")
	}
	if _, err := SignPublicAssertion(signer, 1, "History", "/v1/public/history", "", now); err == nil {
		t.Fatal("bad route id signed")
	}
	if _, err := SignPublicAssertion(signer, 1, "history", "/v1/other", "", now); err == nil {
		t.Fatal("path outside public signed")
	}
}

func TestPublicBackendRequestDataAcceptsOnlyReadOnlyPublicTokens(t *testing.T) {
	signer, now := testPublicSigner(), time.Unix(1_800_000_000, 0)
	token, _ := SignPublicAssertion(signer, 1, "status", "/v1/public/status", "", now)
	if !ValidBackendRequestData("GET", "/v1/public/status", nil, token) {
		t.Fatal("public token refused")
	}
	for _, c := range []struct {
		method, path string
		body         []byte
	}{{"POST", "/v1/public/status", nil}, {"GET", "/v1/admin", nil}, {"GET", "/v1/public/status", []byte(`{}`)}} {
		if ValidBackendRequestData(c.method, c.path, c.body, token) {
			t.Fatal("public token carried a write or a private path", c)
		}
	}
}
