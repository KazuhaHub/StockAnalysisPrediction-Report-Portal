package appplatform

import (
	"strings"
	"testing"
	"time"

	protocol "github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal-Apps/packages/extensionprotocol"
)

func ingressBlock() map[string]any {
	return map[string]any{"routes": []any{
		map[string]any{"id": "heartbeat", "path": "/v1/ingress/heartbeat", "body_bytes": 1024, "per_endpoint_per_minute": 10},
		map[string]any{"id": "webhook", "path": "/v1/ingress/hooks/in", "body_bytes": 4096, "per_endpoint_per_minute": 60},
	}}
}

func declareIngress(mutate func(map[string]any)) func(map[string]any) {
	return func(d map[string]any) {
		d["capabilities"] = map[string]any{"optional": []string{"ingress.webhook"}}
		block := ingressBlock()
		if mutate != nil {
			mutate(block)
		}
		d["ingress"] = block
	}
}

func TestDescriptorAcceptsIngress(t *testing.T) {
	parsed, err := ParseDescriptor(mutateDescriptor(t, declareIngress(nil)))
	if err != nil {
		t.Fatal(err)
	}
	in := parsed.Manifest.Ingress
	if in == nil || len(in.Routes) != 2 || in.Routes[0].ID != "heartbeat" || in.Routes[0].BodyBytes != 1024 || in.Routes[0].PerEndpointPerMinute != 10 {
		t.Fatalf("ingress block was not decoded: %+v", in)
	}
}

func TestDescriptorRefusesMalformedIngress(t *testing.T) {
	cases := map[string]func(map[string]any){
		"unsorted ids":    func(b map[string]any) { b["routes"] = []any{routeAt(b, 1), routeAt(b, 0)} },
		"duplicate id":    func(b map[string]any) { routeAt(b, 1)["id"] = "heartbeat" },
		"duplicate path":  func(b map[string]any) { routeAt(b, 1)["path"] = "/v1/ingress/heartbeat" },
		"outside ingress": func(b map[string]any) { routeAt(b, 0)["path"] = "/v1/public/heartbeat" },
		"handshake":       func(b map[string]any) { routeAt(b, 0)["path"] = "/v1/ingress/../handshake" },
		"query in path":   func(b map[string]any) { routeAt(b, 0)["path"] = "/v1/ingress/heartbeat?x=1" },
		"body zero":       func(b map[string]any) { routeAt(b, 0)["body_bytes"] = 0 },
		"body too large":  func(b map[string]any) { routeAt(b, 0)["body_bytes"] = 4097 },
		"rate zero":       func(b map[string]any) { routeAt(b, 0)["per_endpoint_per_minute"] = 0 },
		"rate too high":   func(b map[string]any) { routeAt(b, 0)["per_endpoint_per_minute"] = 61 },
		"no routes":       func(b map[string]any) { b["routes"] = []any{} },
		"five routes": func(b map[string]any) {
			var routes []any
			for _, id := range []string{"a", "b", "c", "d", "e"} {
				routes = append(routes, map[string]any{"id": id, "path": "/v1/ingress/" + id, "body_bytes": 10, "per_endpoint_per_minute": 1})
			}
			b["routes"] = routes
		},
		"unknown field":     func(b map[string]any) { b["auth"] = "bearer" },
		"unknown route key": func(b map[string]any) { routeAt(b, 0)["token"] = "x" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseDescriptor(mutateDescriptor(t, declareIngress(mutate))); err == nil {
				t.Fatal("accepted")
			}
		})
	}
	// The block and the capability stand or fall together, and the capability is never required.
	if _, err := ParseDescriptor(mutateDescriptor(t, func(d map[string]any) { d["ingress"] = ingressBlock() })); err == nil {
		t.Fatal("a block without the capability")
	}
	if _, err := ParseDescriptor(mutateDescriptor(t, func(d map[string]any) {
		d["capabilities"] = map[string]any{"optional": []string{"ingress.webhook"}}
	})); err == nil {
		t.Fatal("the capability without a block")
	}
	if _, err := ParseDescriptor(mutateDescriptor(t, func(d map[string]any) {
		d["capabilities"] = map[string]any{"required": []string{"ingress.webhook"}}
		d["ingress"] = ingressBlock()
	})); err == nil {
		t.Fatal("a required ingress capability")
	}
}

func TestIngressSigningStringAndSignature(t *testing.T) {
	body := []byte(`{"timestamp":1763400000,"nonce":"3f9c0000000000000000"}`)
	const nonce = "3f9c0000000000000000"
	got := IngressSigningString("11111111-1111-4111-8111-111111111111", "heartbeat", "api", "1763400000", nonce, body)
	want := "portal-ingress/v1\n11111111-1111-4111-8111-111111111111\nheartbeat\napi\n1763400000\n" + nonce + "\n" + IngressDigest(body)
	if got != want || strings.Count(got, "\n") != 6 {
		t.Fatal("signing string", got)
	}
	secret := []byte(strings.Repeat("s", 32))
	header := IngressSign(secret, "11111111-1111-4111-8111-111111111111", "heartbeat", "api", "1763400000", nonce, body)
	if !ValidIngressSignatureHeader(header) || !IngressVerify(secret, "11111111-1111-4111-8111-111111111111", "heartbeat", "api", "1763400000", nonce, body, header) {
		t.Fatal("round trip", header)
	}
	// Every member of the string is bound.
	for name, call := range map[string]func() bool{
		"installation": func() bool {
			return IngressVerify(secret, "22222222-2222-4222-8222-222222222222", "heartbeat", "api", "1763400000", nonce, body, header)
		},
		"route": func() bool {
			return IngressVerify(secret, "11111111-1111-4111-8111-111111111111", "other", "api", "1763400000", nonce, body, header)
		},
		"endpoint": func() bool {
			return IngressVerify(secret, "11111111-1111-4111-8111-111111111111", "heartbeat", "web", "1763400000", nonce, body, header)
		},
		"timestamp": func() bool {
			return IngressVerify(secret, "11111111-1111-4111-8111-111111111111", "heartbeat", "api", "1763400001", nonce, body, header)
		},
		"nonce": func() bool {
			return IngressVerify(secret, "11111111-1111-4111-8111-111111111111", "heartbeat", "api", "1763400000", nonce+"x", body, header)
		},
		"body": func() bool {
			return IngressVerify(secret, "11111111-1111-4111-8111-111111111111", "heartbeat", "api", "1763400000", nonce, append(body, ' '), header)
		},
		"secret": func() bool {
			return IngressVerify([]byte(strings.Repeat("t", 32)), "11111111-1111-4111-8111-111111111111", "heartbeat", "api", "1763400000", nonce, body, header)
		},
	} {
		if call() {
			t.Fatalf("%s is not bound by the signature", name)
		}
	}
}

func TestIngressHeaderShapesAndWindow(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	for header, ok := range map[string]bool{
		"1800000000": true, "1799999970": true, "1799999969": false, "1800000005": true, "1800000006": false,
		"": false, "-1": false, "1.5": false, "18000000000000": false, "0x10": false, " 1800000000": false,
	} {
		if _, got := IngressTimestampWithin(header, now); got != ok {
			t.Fatalf("timestamp %q: %v", header, got)
		}
	}
	for nonce, ok := range map[string]bool{
		strings.Repeat("a", 16): true, strings.Repeat("A", 128): true, "a-b_c-d_e-f_g-h_": true,
		strings.Repeat("a", 15): false, strings.Repeat("a", 129): false, strings.Repeat("a", 15) + "!": false, strings.Repeat("a", 15) + " ": false,
	} {
		if ValidIngressNonce(nonce) != ok {
			t.Fatalf("nonce %q", nonce)
		}
	}
	for header, ok := range map[string]bool{"v1=" + strings.Repeat("a", 64): true, "v1=" + strings.Repeat("A", 64): false, "v2=" + strings.Repeat("a", 64): false, strings.Repeat("a", 64): false, "v1=" + strings.Repeat("a", 63): false} {
		if ValidIngressSignatureHeader(header) != ok {
			t.Fatalf("signature %q", header)
		}
	}
}

func TestIngressAssertionBindsRequestAndNeverVerifiesAsAnotherKind(t *testing.T) {
	signer, now := testPublicSigner(), time.Unix(1_800_000_000, 0)
	const nonce, path = "3f9c0000000000000000", "/v1/ingress/heartbeat"
	body := []byte(`{"timestamp":1,"nonce":"x"}`)
	token, err := SignIngressAssertion(signer, 3, "heartbeat", "api", path, now.Add(-time.Second), nonce, body, now)
	if err != nil || !IsIngressAssertion(token) || IsPublicAssertion(token) {
		t.Fatal(err)
	}
	claims, err := VerifyIngressAssertion(signer, token, path, body, now)
	if err != nil || claims.Generation != 3 || claims.Route != "heartbeat" || claims.Endpoint != "api" || claims.NonceDigest != IngressDigest([]byte(nonce)) || claims.BodyDigest != IngressDigest(body) || claims.ReceivedMS != now.Add(-time.Second).UnixMilli() || claims.Method != "POST" {
		t.Fatalf("%+v %v", claims, err)
	}
	if strings.Contains(token, nonce) {
		t.Fatal("the assertion carries the nonce, not its digest")
	}
	for name, call := range map[string]func() error{
		"another body": func() error {
			_, err := VerifyIngressAssertion(signer, token, path, append(body, ' '), now)
			return err
		},
		"another path": func() error {
			_, err := VerifyIngressAssertion(signer, token, "/v1/ingress/other", body, now)
			return err
		},
		"expired": func() error {
			_, err := VerifyIngressAssertion(signer, token, path, body, now.Add(31*time.Second))
			return err
		},
		"from the future": func() error {
			_, err := VerifyIngressAssertion(signer, token, path, body, now.Add(-10*time.Second))
			return err
		},
		"another boot": func() error {
			other := signer
			other.BootID = "other"
			_, err := VerifyIngressAssertion(other, token, path, body, now)
			return err
		},
		"another key": func() error {
			other := signer
			other.Key = []byte(strings.Repeat("z", 32))
			_, err := VerifyIngressAssertion(other, token, path, body, now)
			return err
		},
		"as a public token": func() error {
			_, err := VerifyPublicAssertion(signer, token, path, now)
			return err
		},
	} {
		if call() == nil {
			t.Fatalf("%s verified", name)
		}
	}
	verifier := protocol.NewVerifier(protocol.VerifyConfig{Issuer: signer.Issuer, Audience: signer.Audience, AppID: signer.AppID, InstanceID: signer.InstanceID, BootID: signer.BootID, Keys: map[string][]byte{signer.KeyID: signer.Key}})
	if _, err := verifier.Verify(strings.TrimPrefix(token, "ing."), "POST", path, body, now); err == nil {
		t.Fatal("an ingress token verified as a viewer assertion")
	}
	// Neither of the other kinds verifies as an ingress token.
	public, err := SignPublicAssertion(signer, 3, "status", "/v1/public/status", "", now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyIngressAssertion(signer, public, path, body, now); err == nil {
		t.Fatal("a public token verified as ingress")
	}
	viewer, err := signer.Sign("POST", path, body, protocol.Viewer{Subject: strings.Repeat("a", 64), Role: "user"}, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyIngressAssertion(signer, viewer, path, body, now); err == nil {
		t.Fatal("a viewer token verified as ingress")
	}
	// Signing refuses what the relay would not carry.
	for name, call := range map[string]func() (string, error){
		"short nonce": func() (string, error) {
			return SignIngressAssertion(signer, 3, "heartbeat", "api", path, now, "short", body, now)
		},
		"bad endpoint": func() (string, error) {
			return SignIngressAssertion(signer, 3, "heartbeat", "A", path, now, nonce, body, now)
		},
		"public path": func() (string, error) {
			return SignIngressAssertion(signer, 3, "heartbeat", "api", "/v1/public/x", now, nonce, body, now)
		},
		"generation": func() (string, error) {
			return SignIngressAssertion(signer, 0, "heartbeat", "api", path, now, nonce, body, now)
		},
		"big body": func() (string, error) {
			return SignIngressAssertion(signer, 3, "heartbeat", "api", path, now, nonce, make([]byte, 4097), now)
		},
	} {
		if _, err := call(); err == nil {
			t.Fatalf("%s signed", name)
		}
	}
}

func TestIngressBackendRequestDataAcceptsOnlyIngressTokensOnTheIngressSpace(t *testing.T) {
	signer, now := testPublicSigner(), time.Unix(1_800_000_000, 0)
	const path = "/v1/ingress/heartbeat"
	body := []byte(`{"a":1}`)
	token, err := SignIngressAssertion(signer, 1, "heartbeat", "api", path, now, "3f9c0000000000000000", body, now)
	if err != nil {
		t.Fatal(err)
	}
	if !ValidBackendRequestData("POST", path, body, token) {
		t.Fatal("a valid ingress request was refused")
	}
	for name, ok := range map[string]bool{
		"GET":                               ValidBackendRequestData("GET", path, nil, token),
		"no body":                           ValidBackendRequestData("POST", path, nil, token),
		"not json":                          ValidBackendRequestData("POST", path, []byte("nope"), token),
		"oversize body":                     ValidBackendRequestData("POST", path, []byte(`"`+strings.Repeat("a", 5000)+`"`), token),
		"viewer path":                       ValidBackendRequestData("POST", "/v1/documents", body, token),
		"public path":                       ValidBackendRequestData("POST", "/v1/public/status", body, token),
		"query":                             ValidBackendRequestData("POST", path+"?x=1", body, token),
		"viewer token on the ingress space": ValidBackendRequestData("POST", path, body, "abc.def"),
	} {
		if ok {
			t.Fatalf("%s was accepted", name)
		}
	}
}
