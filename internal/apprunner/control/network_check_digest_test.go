package control

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"
)

// A check can be signed from the digest of the request alone, so a caller that
// no longer holds the request (with its credential) can still answer challenges.
func TestSignNetworkHTTPCheckFromDigestMatchesTheFullSigner(t *testing.T) {
	c, key, a, now, _ := networkFixture(t)
	raw, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	challenge := hex.EncodeToString(bytes.Repeat([]byte{9}, 32))
	full, fullSig, err := SignNetworkHTTPCheck(c, challenge, key, a, now)
	if err != nil {
		t.Fatal(err)
	}
	expires, _ := counter(c.ExpiresMS, false)
	byDigest, digestSig, err := SignNetworkHTTPCheckDigest(hex.EncodeToString(sum[:]), challenge, expires, key, now)
	if err != nil || !bytes.Equal(full, byDigest) || !bytes.Equal(fullSig, digestSig) {
		t.Fatal("digest signer differs from the full signer", err)
	}
	if err := VerifyNetworkHTTPCheck(c, challenge, byDigest, digestSig, a, now); err != nil {
		t.Fatal("proof does not verify against the request:", err)
	}
	for _, bad := range []string{"", "abc", challenge[:63], hex.EncodeToString(make([]byte, 32))} {
		if _, _, err := SignNetworkHTTPCheckDigest(hex.EncodeToString(sum[:]), bad, expires, key, now); err == nil {
			t.Fatal("accepted challenge", bad)
		}
	}
	if _, _, err := SignNetworkHTTPCheckDigest("not-a-digest", challenge, expires, key, now); err == nil {
		t.Fatal("accepted a malformed request digest")
	}
}
