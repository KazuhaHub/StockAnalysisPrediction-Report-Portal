package control

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

func goldenV1Command() NetworkHTTPRequestCommand {
	policy := []byte(`{"format":"portal.http-policy/1-dev.1","origin":"https://api.example.com","paths":["/v1"],"methods":["POST"],"request_bytes":64,"response_bytes":64,"deadline_ms":1000,"concurrency":1}`)
	sum := sha256.Sum256(policy)
	body := sha256.Sum256([]byte(`{}`))
	return NetworkHTTPRequestCommand{Format: NetworkHTTPRequestFormat, DeploymentID: "10000000-0000-4000-8000-000000000001", ControllerEpoch: "20000000-0000-4000-8000-000000000001", ControlRevision: "1", RequestID: "30000000-0000-4000-8000-000000000001", InstallationID: "40000000-0000-4000-8000-000000000001", ReviewPlanID: "50000000-0000-4000-8000-000000000001", Generation: "3", PolicyRevision: "4", ReleaseSHA256: strings.Repeat("a", 64), ActorScope: strings.Repeat("b", 64), PolicyRaw: policy, PolicySHA256: hex.EncodeToString(sum[:]), Method: "POST", URL: "https://api.example.com/v1/check", Accept: "application/json", ContentType: "application/json", BodyBytes: "2", BodySHA256: hex.EncodeToString(body[:]), IssuedMS: "1700000000000", ExpiresMS: "1700000030000"}
}

// The v1 command bytes are what existing runners and signed requests carry; the
// new optional field must leave them untouched.
func TestNetworkHTTPV1CommandBytesAreUnchanged(t *testing.T) {
	raw, err := json.Marshal(goldenV1Command())
	if err != nil {
		t.Fatal(err)
	}
	const want = `{"format":"portal.runner.viewer-http-request/1-dev.1","deployment_id":"10000000-0000-4000-8000-000000000001","controller_epoch":"20000000-0000-4000-8000-000000000001","control_revision":"1","request_id":"30000000-0000-4000-8000-000000000001","installation_id":"40000000-0000-4000-8000-000000000001","review_plan_id":"50000000-0000-4000-8000-000000000001","generation":"3","policy_revision":"4","release_sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","actor_scope":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","policy_raw":"eyJmb3JtYXQiOiJwb3J0YWwuaHR0cC1wb2xpY3kvMS1kZXYuMSIsIm9yaWdpbiI6Imh0dHBzOi8vYXBpLmV4YW1wbGUuY29tIiwicGF0aHMiOlsiL3YxIl0sIm1ldGhvZHMiOlsiUE9TVCJdLCJyZXF1ZXN0X2J5dGVzIjo2NCwicmVzcG9uc2VfYnl0ZXMiOjY0LCJkZWFkbGluZV9tcyI6MTAwMCwiY29uY3VycmVuY3kiOjF9","policy_sha256":"cbac65e5663f48d40b8ec5cc6444d80a21b650dcb1f41181d7c3b62860b4f3ea","method":"POST","url":"https://api.example.com/v1/check","accept":"application/json","content_type":"application/json","body_bytes":"2","body_sha256":"44136fa355b3678a1146ad16f7e8649e94fb4fc21fe77e8310c060f61caaff8a","issued_ms":"1700000000000","expires_ms":"1700000030000"}`
	if string(raw) != want {
		t.Fatalf("v1 command bytes changed:\n%s", raw)
	}
	if strings.Contains(string(raw), "private_networks_sha256") {
		t.Fatal("empty field serialised")
	}
}

func v2Fixture(t *testing.T, targets ...appplatform.HTTPTarget) (NetworkHTTPRequestCommand, func(NetworkHTTPRequestCommand) error) {
	t.Helper()
	c, _, a, now, _ := networkFixture(t)
	raw, err := appplatform.CanonicalHTTPPolicyV2(appplatform.HTTPPolicyV2{Targets: targets, RequestBytes: 64, ResponseBytes: 64, Deadline: time.Second, Concurrency: 1})
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := appplatform.ParseHTTPPolicyAny(raw)
	if err != nil {
		t.Fatal(err)
	}
	c.PolicyRaw, c.PolicySHA256 = raw, parsed.SHA256
	return c, func(c NetworkHTTPRequestCommand) error { return ValidateNetworkHTTPRequestMetadata(c, a, now) }
}

func TestNetworkHTTPV2CommandPrivateNetworksField(t *testing.T) {
	public := appplatform.HTTPTarget{ID: "pub", Origin: "https://api.example.com", Paths: []string{"/v1"}, Methods: []string{"POST"}, Network: "public"}
	private := appplatform.HTTPTarget{ID: "lan", Origin: "https://10.20.0.5:8443", Paths: []string{"/v1"}, Methods: []string{"POST"}, Network: "private"}
	plain := appplatform.HTTPTarget{ID: "plain", Origin: "http://10.20.0.6:8080", Paths: []string{"/v1"}, Methods: []string{"POST"}, Network: "private"}
	allow := strings.Repeat("c", 64)

	// A v2 policy with only public targets must not carry the field.
	c, validate := v2Fixture(t, public)
	if err := validate(c); err != nil {
		t.Fatalf("public v2: %v", err)
	}
	c.PrivateNetworksSHA256 = allow
	if validate(c) == nil {
		t.Fatal("field accepted for a policy with no private target")
	}

	// A v1 policy must not carry it either.
	v1, validate1 := func() (NetworkHTTPRequestCommand, func(NetworkHTTPRequestCommand) error) {
		c, _, a, now, _ := networkFixture(t)
		return c, func(c NetworkHTTPRequestCommand) error { return ValidateNetworkHTTPRequestMetadata(c, a, now) }
	}()
	if err := validate1(v1); err != nil {
		t.Fatalf("v1: %v", err)
	}
	v1.PrivateNetworksSHA256 = allow
	if validate1(v1) == nil {
		t.Fatal("field accepted for a v1 policy")
	}

	// A policy with a private target must carry a well-formed digest.
	c, validate = v2Fixture(t, public, private)
	c.URL = "https://10.20.0.5:8443/v1/x"
	if validate(c) == nil {
		t.Fatal("private policy accepted without the allowlist digest")
	}
	for name, bad := range map[string]string{"short": "abc", "upper": strings.Repeat("C", 64), "zero": strings.Repeat("0", 64), "non-hex": strings.Repeat("g", 64)} {
		c.PrivateNetworksSHA256 = bad
		if validate(c) == nil {
			t.Fatalf("%s digest accepted", name)
		}
	}
	c.PrivateNetworksSHA256 = allow
	if err := validate(c); err != nil {
		t.Fatalf("private target: %v", err)
	}
	// The requirement follows the policy, not the selected target.
	c.URL = "https://api.example.com/v1/x"
	if err := validate(c); err != nil {
		t.Fatalf("public target of a private-capable policy: %v", err)
	}
	c.PrivateNetworksSHA256 = ""
	if validate(c) == nil {
		t.Fatal("policy with a private target accepted without the digest for a public URL")
	}

	// The key sits right after policy_sha256.
	c.PrivateNetworksSHA256 = allow
	raw, _ := json.Marshal(c)
	if !strings.Contains(string(raw), `"policy_sha256":"`+c.PolicySHA256+`","private_networks_sha256":"`+allow+`","method":`) {
		t.Fatalf("field position: %s", raw)
	}

	// A credential is refused when the selected target is plain http.
	c, validate = v2Fixture(t, plain)
	c.URL = "http://10.20.0.6:8080/v1/x"
	c.PrivateNetworksSHA256 = allow
	if err := validate(c); err != nil {
		t.Fatalf("plain http without credential: %v", err)
	}
	c.CredentialName, c.CredentialHeader, c.CredentialValue = "upstream-api", "Authorization", []byte("Bearer secret")
	if validate(c) == nil {
		t.Fatal("credential accepted for a plain http target")
	}
	// ... but fine for an https private target.
	c, validate = v2Fixture(t, private)
	c.URL = "https://10.20.0.5:8443/v1/x"
	c.PrivateNetworksSHA256 = allow
	c.CredentialName, c.CredentialHeader, c.CredentialValue = "upstream-api", "Authorization", []byte("Bearer secret")
	if err := validate(c); err != nil {
		t.Fatalf("credential over private https: %v", err)
	}
}

func TestNetworkHTTPV2UsesTheSelectedTargetsBounds(t *testing.T) {
	narrow := appplatform.HTTPTarget{ID: "narrow", Origin: "https://api.example.com", Paths: []string{"/v1"}, Methods: []string{"POST"}, Network: "public", RequestBytes: 1, Deadline: 400 * time.Millisecond}
	wide := appplatform.HTTPTarget{ID: "wide", Origin: "https://other.example.com", Paths: []string{"/v1"}, Methods: []string{"POST"}, Network: "public"}
	c, validate := v2Fixture(t, narrow, wide)
	if validate(c) == nil {
		t.Fatal("2-byte body accepted by a target limited to 1 byte")
	}
	c.URL = "https://other.example.com/v1/check"
	if err := validate(c); err != nil {
		t.Fatalf("wide target: %v", err)
	}
	c.URL = "https://unknown.example.com/v1"
	if validate(c) == nil {
		t.Fatal("URL outside every target accepted")
	}
	c.URL = "https://other.example.com/v1/check"
	c.Method = "GET"
	if validate(c) == nil {
		t.Fatal("method outside the target accepted")
	}
	// The signature window must cover the selected target's own deadline.
	c, validate = v2Fixture(t, wide)
	c.URL = "https://other.example.com/v1/check"
	issued, _ := strconv.ParseInt(c.IssuedMS, 10, 64)
	c.ExpiresMS = strconv.FormatInt(issued+999, 10)
	if validate(c) == nil {
		t.Fatal("window shorter than the deadline accepted")
	}
}

func TestNetworkHTTPStreamRefusesV2AndTheField(t *testing.T) {
	c, key, a, now, _ := networkStreamFixture(t)
	if _, _, err := SignNetworkHTTPStreamRequest(c, key, a, now); err != nil {
		t.Fatalf("v1 stream: %v", err)
	}
	withField := c
	withField.PrivateNetworksSHA256 = strings.Repeat("c", 64)
	if _, _, err := SignNetworkHTTPStreamRequest(withField, key, a, now); err == nil {
		t.Fatal("stream command accepted the private-networks field")
	}
	v2, _ := v2Fixture(t, appplatform.HTTPTarget{ID: "pub", Origin: "https://api.example.com", Paths: []string{"/v1"}, Methods: []string{"POST"}, Network: "public"})
	c.PolicyRaw, c.PolicySHA256 = v2.PolicyRaw, v2.PolicySHA256
	if _, _, err := SignNetworkHTTPStreamRequest(c, key, a, now); err == nil {
		t.Fatal("stream command accepted a v2 policy")
	}
}
