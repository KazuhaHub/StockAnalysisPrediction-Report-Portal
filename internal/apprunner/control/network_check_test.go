package control

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
)

func TestNetworkHTTPCheckBindsFreshChallengeAndCompleteRequest(t *testing.T) {
	c, key, a, now, _ := networkFixture(t)
	challenge := strings.Repeat("c", 64)
	raw, sig, err := SignNetworkHTTPCheck(c, challenge, key, a, now)
	if err != nil {
		t.Fatal(err)
	}
	if VerifyNetworkHTTPCheck(c, challenge, raw, sig, a, now) != nil {
		t.Fatal("valid confirmation")
	}
	if VerifyNetworkHTTPCheck(c, strings.Repeat("d", 64), raw, sig, a, now) == nil {
		t.Fatal("replayed challenge")
	}
	changed := c
	changed.ActorScope = strings.Repeat("e", 64)
	if VerifyNetworkHTTPCheck(changed, challenge, raw, sig, a, now) == nil {
		t.Fatal("another viewer")
	}
	changed = c
	changed.BodySHA256 = strings.Repeat("f", 64)
	if VerifyNetworkHTTPCheck(changed, challenge, raw, sig, a, now) == nil {
		t.Fatal("another body")
	}
	if VerifyNetworkHTTPCheck(c, challenge, raw, sig, a, now+1000) == nil {
		t.Fatal("expired check")
	}
	wrong := ed25519.Sign(key, frame(networkHTTPRequestPurpose, raw))
	if VerifyNetworkHTTPCheck(c, challenge, raw, wrong, a, now) == nil {
		t.Fatal("request purpose accepted")
	}
	if _, err := VerifyNetworkHTTPRequest(raw, sig, a, now); err == nil {
		t.Fatal("confirmation dispatched request")
	}
	var proof NetworkHTTPCheck
	if json.Unmarshal(raw, &proof) != nil {
		t.Fatal("proof decode")
	}
	if proof.ExpiresMS != "1700000001000" {
		t.Fatal("wrong check lifetime", proof.ExpiresMS)
	}
	c.ExpiresMS = "1700000000050"
	c.PolicyRaw = bytes.Clone(c.PolicyRaw)
	// Use a 1 ms policy so the request itself remains valid with 50 ms remaining.
	c.PolicyRaw = bytes.Replace(c.PolicyRaw, []byte(`"deadline_ms":1000`), []byte(`"deadline_ms":1`), 1)
	sum := sha256.Sum256(c.PolicyRaw)
	c.PolicySHA256 = hex.EncodeToString(sum[:])
	raw, sig, err = SignNetworkHTTPCheck(c, challenge, key, a, now)
	if err != nil {
		t.Fatal(err)
	}
	if json.Unmarshal(raw, &proof) != nil || proof.ExpiresMS != c.ExpiresMS {
		t.Fatal("confirmation outlives request")
	}
}

func TestNetworkHTTPCheckRejectsSignedMalformedProof(t *testing.T) {
	for _, mode := range []string{"unknown", "duplicate", "alias", "space", "version", "request", "nonce", "issued", "expiry", "long", "signature", "authority", "invalid_command", "zero_nonce", "oversize"} {
		t.Run(mode, func(t *testing.T) {
			c, key, a, now, _ := networkFixture(t)
			nonce := strings.Repeat("c", 64)
			raw, _, err := SignNetworkHTTPCheck(c, nonce, key, a, now)
			if err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "unknown":
				raw = append(raw[:len(raw)-1], []byte(`,"extra":true}`)...)
			case "duplicate":
				raw = append(raw[:len(raw)-1], []byte(`,"issued_ms":"1700000000000"}`)...)
			case "alias":
				raw = bytes.Replace(raw, []byte(`"format"`), []byte(`"Format"`), 1)
			case "space":
				raw = append(raw, ' ')
			case "version":
				raw = bytes.Replace(raw, []byte(NetworkHTTPCheckFormat), []byte(NetworkHTTPRequestFormat), 1)
			case "request":
				raw = bytes.Replace(raw, []byte(`"request_sha256":"`), []byte(`"request_sha256":"f`), 1)
			case "nonce":
				raw = bytes.Replace(raw, []byte(nonce), []byte(strings.Repeat("d", 64)), 1)
			case "issued":
				now--
			case "expiry":
				now += 1000
			case "long":
				raw = bytes.Replace(raw, []byte("1700000001000"), []byte("1700000001001"), 1)
			case "authority":
				a.ControlRevision++
			case "invalid_command":
				c.Generation = "01"
			case "zero_nonce":
				nonce = strings.Repeat("0", 64)
			case "oversize":
				raw = make([]byte, 2049)
			}
			sig := ed25519.Sign(key, frame(networkHTTPCheckPurpose, raw))
			if mode == "signature" {
				sig[0] ^= 1
			}
			if VerifyNetworkHTTPCheck(c, nonce, raw, sig, a, now) == nil {
				t.Fatal("accepted", mode)
			}
		})
	}
}
