package control

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

func networkFixture(t *testing.T) (NetworkHTTPRequestCommand, ed25519.PrivateKey, Authority, int64, []byte) {
	t.Helper()
	public, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(public)
	a := Authority{DeploymentID: "10000000-0000-4000-8000-000000000001", ControllerEpoch: "20000000-0000-4000-8000-000000000001", ControlRevision: 1, PublicKey: public, Fingerprint: hex.EncodeToString(sum[:])}
	p := appplatform.HTTPPolicy{Origin: "https://api.example.com", Paths: []string{"/v1"}, Methods: []string{"POST"}, RequestBytes: 64, ResponseBytes: 64, Deadline: time.Second, Concurrency: 1}
	policy, err := appplatform.CanonicalHTTPPolicy(p)
	if err != nil {
		t.Fatal(err)
	}
	parsed, _ := appplatform.ParseHTTPPolicy(policy)
	now := int64(1700000000000)
	body := []byte(`{}`)
	digest := sha256.Sum256(body)
	c := NetworkHTTPRequestCommand{Format: NetworkHTTPRequestFormat, DeploymentID: a.DeploymentID, ControllerEpoch: a.ControllerEpoch, ControlRevision: "1", RequestID: "30000000-0000-4000-8000-000000000001", InstallationID: "40000000-0000-4000-8000-000000000001", ReviewPlanID: "50000000-0000-4000-8000-000000000001", Generation: "9007199254740993", PolicyRevision: "9007199254740994", ReleaseSHA256: strings.Repeat("a", 64), ActorScope: strings.Repeat("b", 64), PolicyRaw: policy, PolicySHA256: parsed.SHA256, Method: "POST", URL: "https://api.example.com/v1/check", Accept: "application/json", ContentType: "application/json", BodyBytes: "2", BodySHA256: hex.EncodeToString(digest[:]), IssuedMS: strconv.FormatInt(now, 10), ExpiresMS: strconv.FormatInt(now+30000, 10)}
	return c, key, a, now, body
}

func TestNetworkHTTPSignatureAndPayloadBinding(t *testing.T) {
	c, key, a, now, body := networkFixture(t)
	raw, sig, err := SignNetworkHTTPRequest(c, key, a, now)
	if err != nil {
		t.Fatal(err)
	}
	v, err := VerifyNetworkHTTPRequest(raw, sig, a, now)
	if err != nil {
		t.Fatal(err)
	}
	if ValidateNetworkHTTPBody(v, body) != nil || ValidateNetworkHTTPBody(v, []byte(`[]`)) == nil || ValidateNetworkHTTPBody(VerifiedNetworkHTTPRequest{}, body) == nil {
		t.Fatal("body binding")
	}
	got := v.Command()
	if got.Generation != "9007199254740993" || got.PolicyRevision != "9007199254740994" {
		t.Fatal("counter precision lost")
	}
	got.PolicyRaw[0] ^= 1
	if bytes.Equal(got.PolicyRaw, v.Command().PolicyRaw) {
		t.Fatal("aliased policy")
	}
	c.PolicyRaw[0] ^= 1
	if bytes.Equal(c.PolicyRaw, v.Command().PolicyRaw) {
		t.Fatal("aliased input")
	}
	if _, err = VerifyBackendRequest(raw, sig, a, now); err == nil {
		t.Fatal("network signature crossed backend domain")
	}
	sig = ed25519.Sign(key, frame(backendRequestSignaturePurpose, raw))
	if _, err = VerifyNetworkHTTPRequest(raw, sig, a, now); err == nil {
		t.Fatal("backend signature authorized network")
	}
}

func TestNetworkHTTPRefusesSignedInvalidRequests(t *testing.T) {
	for _, mode := range []string{"format", "zero", "installation", "review", "epoch", "deployment", "revision", "generation", "policy_revision", "release", "actor", "method", "path", "header", "policy", "policy_hash", "body_size", "body_hash", "future", "expired", "lifetime", "short_lifetime", "unknown", "duplicate", "space", "oversized", "fingerprint", "signature"} {
		t.Run(mode, func(t *testing.T) {
			c, key, a, now, _ := networkFixture(t)
			switch mode {
			case "format":
				c.Format = BackendRequestFormat
			case "zero":
				c.RequestID = strings.Repeat("0", 8) + "-0000-0000-0000-000000000000"
			case "installation":
				c.InstallationID = "bad"
			case "review":
				c.ReviewPlanID = "bad"
			case "epoch":
				c.ControllerEpoch = c.InstallationID
			case "deployment":
				c.DeploymentID = c.InstallationID
			case "revision":
				c.ControlRevision = "2"
			case "generation":
				c.Generation = "01"
			case "policy_revision":
				c.PolicyRevision = "0"
			case "release":
				c.ReleaseSHA256 = "bad"
			case "actor":
				c.ActorScope = "user"
			case "method":
				c.Method = "DELETE"
			case "path":
				c.URL = "https://api.example.com/v10"
			case "header":
				c.Accept = "x\r\nCookie: a"
			case "policy":
				c.PolicyRaw = append(c.PolicyRaw, ' ')
			case "policy_hash":
				c.PolicySHA256 = strings.Repeat("f", 64)
			case "body_size":
				c.BodyBytes = "65"
			case "body_hash":
				c.BodySHA256 = "bad"
			case "future":
				now--
			case "expired":
				now += 30000
			case "lifetime":
				c.ExpiresMS = strconv.FormatInt(now+30001, 10)
			case "short_lifetime":
				c.ExpiresMS = strconv.FormatInt(now+999, 10)
			case "fingerprint":
				a.Fingerprint = strings.Repeat("f", 64)
			}
			raw, _ := json.Marshal(c)
			switch mode {
			case "unknown":
				raw = append(raw[:len(raw)-1], []byte(`,"extra":true}`)...)
			case "duplicate":
				raw = append(raw[:len(raw)-1], []byte(`,"method":"POST"}`)...)
			case "space":
				raw = append(raw, ' ')
			case "oversized":
				raw = make([]byte, MaxCommandBytes+1)
			}
			sig := ed25519.Sign(key, frame(networkHTTPRequestPurpose, raw))
			if mode == "signature" {
				sig[0] ^= 1
			}
			if _, err := VerifyNetworkHTTPRequest(raw, sig, a, now); err == nil {
				t.Fatal("accepted", mode)
			}
		})
	}
}
