package appplatform

import (
	"encoding/base64"
	"encoding/json"
	"regexp"
	"strings"
	"testing"
	"time"

	protocol "github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal-Apps/packages/extensionprotocol"
)

func testMachine() Machine {
	return Machine{Routes: []MachineRoute{
		{ID: "bars", Path: "/v1/machine/bars", Query: []string{"from", "limit", "symbol", "to"}, ResponseBytes: 1000, TimeoutMS: 5000},
		{ID: "margin", Path: "/v1/machine/margin/latest", Query: []string{"date"}, ResponseBytes: 1000, TimeoutMS: 5000},
	}}
}

func TestMachinePolicyRoundTripAndBounds(t *testing.T) {
	m := testMachine()
	def := DefaultMachinePolicy(m)
	if def.InstallationPerSecond != 5 || def.InstallationConcurrency != 4 || len(def.Routes) != 2 {
		t.Fatalf("%+v", def)
	}
	raw := CanonicalMachinePolicy(def)
	if got, err := ParseMachinePolicy(raw, m); err != nil || CanonicalMachinePolicy(got) != raw || !ValidMachinePolicyShape(raw) {
		t.Fatal(err)
	}
	for _, b := range []string{
		strings.Replace(raw, `"installation_per_second":5`, `"installation_per_second":51`, 1),
		strings.Replace(raw, `"installation_per_second":5`, `"installation_per_second":0`, 1),
		strings.Replace(raw, `"installation_concurrency":4`, `"installation_concurrency":17`, 1),
		strings.Replace(raw, `"bars"`, `"nope"`, 1),
		strings.Replace(raw, `["bars","margin"]`, `["margin","bars"]`, 1),
		strings.Replace(raw, `["bars","margin"]`, `[]`, 1),
		strings.Replace(raw, `1-dev.1`, `1-dev.2`, 1),
		strings.Replace(raw, `{`, `{"extra":1,`, 1),
		` ` + raw, `{}`, `null`, ``,
	} {
		if _, err := ParseMachinePolicy(b, m); err == nil {
			t.Fatalf("accepted %q", b)
		}
	}
}

func TestCanonicalMachineQuery(t *testing.T) {
	declared := []string{"date", "from", "to"}
	for in, want := range map[string]string{"": "", "to=b&from=a": "from=a&to=b", "from=a%20b": "from=a+b", "date=": "date="} {
		if got, err := CanonicalMachineQuery(declared, in); err != nil || got != want {
			t.Fatalf("%q: %q %v", in, got, err)
		}
	}
	for _, in := range []string{"x=1", "from=a&from=b", "from", "=1", "from=\x01", "from=%00", "from=%ff", "from=" + strings.Repeat("a", 257), "from=a&&to=b", "From=1", "from=é", "from=a#b",
		"from=" + strings.Repeat("a", 256) + "&to=" + strings.Repeat("b", 256) + "&date=" + strings.Repeat("c", 256) + "&" + strings.Repeat("z", 400)} {
		if _, err := CanonicalMachineQuery(declared, in); err == nil {
			t.Fatalf("accepted %q", in)
		}
	}
	// 256 bytes is allowed for a value where the public API allows 128.
	if _, err := CanonicalMachineQuery(declared, "from="+strings.Repeat("a", 256)); err != nil {
		t.Fatal(err)
	}
}

func TestMachineTokenFormatAndAuthorization(t *testing.T) {
	token, handle, digest, err := NewMachineToken()
	if err != nil || len(token) != 60 || !strings.HasPrefix(token, "pmr_") || len(handle) != 12 || len(digest) != 64 || token[16] != '_' {
		t.Fatal(err, token, handle, digest)
	}
	h, secret, err := ParseMachineAuthorization("Bearer " + token)
	if err != nil || h != handle || len(secret) != 43 || MachineSecretDigest(secret) != digest {
		t.Fatal(err)
	}
	other, _, _, _ := NewMachineToken()
	if token == other {
		t.Fatal("tokens repeat")
	}
	bad := []string{"", "Bearer", "Bearer ", token, "bearer " + token, "Bearer  " + token, "Bearer " + token + " ", "Bearer " + token[:59], "Bearer " + token + "x",
		"Bearer xyz_" + token[4:], "Bearer pmr_" + strings.ToUpper(handle) + token[16:], "Bearer " + token[:16] + "-" + token[17:], "Bearer " + token[:30] + "+" + token[31:],
		"Bearer " + strings.Repeat("a", 200), "Basic " + token, "Bearer abcdef0123456789abcdef0123456789abcdef0123456789"}
	for _, b := range bad {
		if _, _, err := ParseMachineAuthorization(b); err == nil {
			t.Fatalf("accepted %q", b)
		}
	}
}

func TestMachineAssertionBindsRequestAndIsSeparateFromOtherKinds(t *testing.T) {
	signer, now := testPublicSigner(), time.Unix(1_800_000_000, 0)
	token, err := SignMachineAssertion(signer, 3, "bars", "/v1/machine/bars", "from=a", "abcdefghijkl", "Dify production", now)
	if err != nil || !IsMachineAssertion(token) || IsPublicAssertion(token) || !backendRequestAssertion.MatchString(token) {
		t.Fatal(err, token)
	}
	claims, err := VerifyMachineAssertion(signer, token, "/v1/machine/bars?from=a", now.Add(time.Second))
	if err != nil || claims.Route != "bars" || claims.Generation != 3 || claims.ClientID != "abcdefghijkl" || claims.ClientLabel != "Dify production" || claims.Method != "GET" || claims.RequestID == "" {
		t.Fatalf("%+v %v", claims, err)
	}
	if parsed, err := ParseMachineClaims(token); err != nil || parsed != claims {
		t.Fatal(err)
	}
	for name, check := range map[string]func() error{
		"other query": func() error { _, e := VerifyMachineAssertion(signer, token, "/v1/machine/bars?from=b", now); return e },
		"other path":  func() error { _, e := VerifyMachineAssertion(signer, token, "/v1/machine/other?from=a", now); return e },
		"expired": func() error {
			_, e := VerifyMachineAssertion(signer, token, "/v1/machine/bars?from=a", now.Add(31*time.Second))
			return e
		},
		"other key": func() error {
			o := signer
			o.Key = []byte(strings.Repeat("z", 32))
			_, e := VerifyMachineAssertion(o, token, "/v1/machine/bars?from=a", now)
			return e
		},
		"tampered": func() error {
			_, e := VerifyMachineAssertion(signer, token[:len(token)-2]+"AA", "/v1/machine/bars?from=a", now)
			return e
		},
	} {
		if check() == nil {
			t.Fatal("accepted:", name)
		}
	}
	// Domain separation in every direction: viewer, public and machine verifiers accept only their own.
	verifier := protocol.NewVerifier(protocol.VerifyConfig{Issuer: signer.Issuer, Audience: signer.Audience, AppID: signer.AppID, InstanceID: signer.InstanceID, BootID: signer.BootID, Keys: map[string][]byte{signer.KeyID: signer.Key}})
	if _, err := verifier.Verify(strings.TrimPrefix(token, "mch."), "GET", "/v1/machine/bars?from=a", nil, now); err == nil {
		t.Fatal("a machine token verified as a viewer assertion")
	}
	if _, err := VerifyPublicAssertion(signer, "pub."+strings.TrimPrefix(token, "mch."), "/v1/machine/bars?from=a", now); err == nil {
		t.Fatal("a machine token verified as public")
	}
	public, _ := SignPublicAssertion(signer, 1, "history", "/v1/public/history", "", now)
	if _, err := VerifyMachineAssertion(signer, "mch."+strings.TrimPrefix(public, "pub."), "/v1/public/history", now); err == nil {
		t.Fatal("a public token verified as machine")
	}
	viewer, _ := signer.Sign("GET", "/v1/machine/bars?from=a", nil, protocol.Viewer{Subject: "s", Role: "user", OUs: []string{"o"}}, now)
	if _, err := VerifyMachineAssertion(signer, "mch."+viewer, "/v1/machine/bars?from=a", now); err == nil {
		t.Fatal("a viewer token verified as machine")
	}
	for _, c := range []struct {
		gen                   int64
		route, path, id, name string
	}{{0, "bars", "/v1/machine/bars", "abcdefghijkl", "x"}, {1, "Bars", "/v1/machine/bars", "abcdefghijkl", "x"}, {1, "bars", "/v1/public/bars", "abcdefghijkl", "x"},
		{1, "bars", "/v1/machine/bars", "short", "x"}, {1, "bars", "/v1/machine/bars", "abcdefghijkl", ""}, {1, "bars", "/v1/machine/bars", "abcdefghijkl", "a\nb"},
		{1, "bars", "/v1/machine/bars", "abcdefghijkl", strings.Repeat("x", 65)}} {
		if _, err := SignMachineAssertion(signer, c.gen, c.route, c.path, "", c.id, c.name, now); err == nil {
			t.Fatalf("signed %+v", c)
		}
	}
}

// An older runner has only the two-part pattern ("viewer" or "pub." plus two parts): a machine token
// has three parts after its prefix, so the upgrade fails closed (doc 183 section 4.7).
func TestOlderRunnerPatternRefusesMachineTokens(t *testing.T) {
	older := regexp.MustCompile(`^(pub\.)?[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+$`)
	token, err := SignMachineAssertion(testPublicSigner(), 1, "bars", "/v1/machine/bars", "", "abcdefghijkl", "Dify", time.Unix(1_800_000_000, 0))
	if err != nil || older.MatchString(token) || !backendRequestAssertion.MatchString(token) {
		t.Fatal(err)
	}
}

func TestBackendRequestSpacesAreExclusiveOnTheDecodedPath(t *testing.T) {
	signer, now := testPublicSigner(), time.Unix(1_800_000_000, 0)
	machine, _ := SignMachineAssertion(signer, 1, "bars", "/v1/machine/bars", "", "abcdefghijkl", "Dify", now)
	public, _ := SignPublicAssertion(signer, 1, "status", "/v1/public/status", "", now)
	viewer, _ := signer.Sign("GET", "/v1/app/x", nil, protocol.Viewer{Subject: "s", Role: "user", OUs: []string{"o"}}, now)
	if !ValidBackendRequestData("GET", "/v1/machine/bars", nil, machine) || !ValidBackendRequestData("GET", "/v1/machine/bars?from=a", nil, machine) || !ValidBackendRequestData("GET", "/v1/app/x", nil, viewer) {
		t.Fatal("a token of the right kind was refused")
	}
	type tc struct {
		name, method, path string
		body               []byte
		token              string
	}
	cases := []tc{
		{"machine with a body", "GET", "/v1/machine/bars", []byte(`{}`), machine},
		{"machine POST", "POST", "/v1/machine/bars", nil, machine},
		{"machine outside its space", "GET", "/v1/app/x", nil, machine},
		{"machine in the public space", "GET", "/v1/public/status", nil, machine},
		{"public in the machine space", "GET", "/v1/machine/bars", nil, public},
		{"viewer in the machine space", "GET", "/v1/machine/bars", nil, viewer},
		{"viewer in the public space", "GET", "/v1/public/status", nil, viewer},
		{"viewer POST in the machine space", "POST", "/v1/machine/bars", []byte(`{}`), viewer},
		{"viewer, upper case space", "GET", "/v1/MACHINE/bars", nil, viewer},
		{"viewer, mixed case public", "GET", "/v1/Public/status", nil, viewer},
		{"viewer, encoded space", "GET", "/v1/%6dachine/bars", nil, viewer},
		{"viewer, double encoded space", "GET", "/v1/%256dachine/bars", nil, viewer},
		{"viewer, encoded slash", "GET", "/v1/machine%2fbars", nil, viewer},
		{"viewer, dot segment into the space", "GET", "/v1/app/../machine/bars", nil, viewer},
		{"viewer, encoded dot segment", "GET", "/v1/app/%2e%2e/machine/bars", nil, viewer},
		{"viewer, empty segment", "GET", "/v1//machine/bars", nil, viewer},
		{"viewer, semicolon", "GET", "/v1/machine;x/bars", nil, viewer},
		{"viewer, encoded semicolon", "GET", "/v1/machine%3bx/bars", nil, viewer},
		{"viewer, the bare space", "GET", "/v1/machine", nil, viewer},
		{"machine, upper case space", "GET", "/v1/MACHINE/bars", nil, machine},
		{"machine, encoded space", "GET", "/v1/machine/%62ars", nil, machine},
		{"machine, dot segment", "GET", "/v1/machine/./bars", nil, machine},
		{"machine, semicolon", "GET", "/v1/machine/bars;x=1", nil, machine},
		{"machine, handshake", "GET", "/v1/machine/../handshake", nil, machine},
	}
	for _, c := range cases {
		if ValidBackendRequestData(c.method, c.path, c.body, c.token) {
			t.Errorf("accepted: %s", c.name)
		}
	}
}

// The strict spellings are refused where they could reach a reserved space, not on every path: an
// ordinary viewer request keeps working.
func TestOrdinaryViewerPathsKeepTheirSpellings(t *testing.T) {
	signer, now := testPublicSigner(), time.Unix(1_800_000_000, 0)
	viewer, _ := signer.Sign("GET", "/v1/app/x", nil, protocol.Viewer{Subject: "s", Role: "user", OUs: []string{"o"}}, now)
	for _, path := range []string{"/v1/app/x;y=1", "/v1/app/%3bx", "/v1/app/a%252fb"} {
		if !ValidBackendRequestData("GET", path, nil, viewer) {
			t.Errorf("refused an ordinary path: %s", path)
		}
	}
	for _, path := range []string{"/v1/%6Dachine/x", "/v1/machine;x/", "/v1/app/..;/machine/x", "/v1/app/x;/../../public/y", "/v1/app/../handshake", "/v1/./machine/x"} {
		if ValidBackendRequestData("GET", path, nil, viewer) {
			t.Errorf("accepted: %s", path)
		}
	}
	if _, clean := backendRequestSpace("/V1/Machine;x/"); clean {
		t.Error("an upper-case spelling with a path parameter was judged clean")
	}
}

// The reference verifier decodes strictly and checks the installation, route and request ID: a token
// with a valid MAC but claims no Portal would sign is refused.
func TestVerifyMachineAssertionIsStrictAboutClaims(t *testing.T) {
	signer, now := testPublicSigner(), time.Unix(1_800_000_000, 0)
	good, err := SignMachineAssertion(signer, 1, "bars", "/v1/machine/bars", "", "abcdefghijkl", "Dify", now)
	if err != nil {
		t.Fatal(err)
	}
	claims, err := ParseMachineClaims(good)
	if err != nil {
		t.Fatal(err)
	}
	remac := func(raw []byte) string {
		encoded := base64.RawURLEncoding.EncodeToString(raw)
		return MachineAssertionPrefix + encoded + "." + base64.RawURLEncoding.EncodeToString(machineMAC(signer.Key, encoded))
	}
	marshal := func(c MachineClaims) []byte { raw, _ := json.Marshal(c); return raw }
	if _, err := VerifyMachineAssertion(signer, remac(marshal(claims)), "/v1/machine/bars", now); err != nil {
		t.Fatal("a re-signed canonical token was refused", err)
	}
	other, badRoute, noRID := claims, claims, claims
	other.Installation = "someone-else"
	badRoute.Route = "Bars"
	noRID.RequestID = ""
	spaced := append([]byte(" "), marshal(claims)...)
	for name, token := range map[string]string{"other installation": remac(marshal(other)), "bad route": remac(marshal(badRoute)), "no request id": remac(marshal(noRID)), "non canonical": remac(spaced)} {
		if _, err := VerifyMachineAssertion(signer, token, "/v1/machine/bars", now); err == nil {
			t.Errorf("accepted: %s", name)
		}
	}
}
