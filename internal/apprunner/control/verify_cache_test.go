package control

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"reflect"
	"testing"
)

// The cache is process-wide, so every test starts from an empty one with a known bound.
func freshVerifyCache(t *testing.T, capacity int) {
	t.Helper()
	old := verifiedCommands
	verifiedCommands = newVerifyCache(capacity)
	t.Cleanup(func() { verifiedCommands = old })
}

func signedRegistration(t *testing.T) (raw, sig []byte, a Authority, now int64) {
	t.Helper()
	c, key, a, now := writableRegistrationFixture(t)
	raw, sig, err := SignWritableBackendRegistration(c, key, a, now)
	if err != nil {
		t.Fatal(err)
	}
	return raw, sig, a, now
}

func TestVerifyCacheHitReturnsTheIdenticalVerifiedValue(t *testing.T) {
	freshVerifyCache(t, 16)
	raw, sig, a, now := signedRegistration(t)
	freshVerifyCache(t, 16) // the fixture's own self-checks must not count
	first, err := VerifyWritableBackendRegistration(raw, sig, a, now)
	if err != nil {
		t.Fatal(err)
	}
	if h, m, n := VerifyCacheStats(); h != 0 || m == 0 || n == 0 {
		t.Fatalf("first verification must miss and store: hits=%d misses=%d size=%d", h, m, n)
	}
	_, missesBefore, _ := VerifyCacheStats()
	second, err := VerifyWritableBackendRegistration(raw, sig, a, now+1)
	if err != nil {
		t.Fatal(err)
	}
	if h, m, _ := VerifyCacheStats(); h != 1 || m != missesBefore {
		t.Fatalf("second verification must hit without re-verifying: hits=%d misses=%d", h, m)
	}
	if !reflect.DeepEqual(first.Command(), second.Command()) ||
		!reflect.DeepEqual(first.Candidate().Index(), second.Candidate().Index()) ||
		!reflect.DeepEqual(first.Candidate().Command(), second.Candidate().Command()) {
		t.Fatal("cache hit differs from the verified value")
	}
	// A caller that scribbles on a getter's copy must not poison later hits.
	c := second.Command()
	c.CandidateRaw[0] ^= 1
	third, err := VerifyWritableBackendRegistration(raw, sig, a, now+2)
	if err != nil || !bytes.Equal(third.Command().CandidateRaw, first.Command().CandidateRaw) {
		t.Fatal("a hit aliased caller-visible bytes", err)
	}
}

func TestVerifyCacheNeverOutlivesTheDocumentWindow(t *testing.T) {
	freshVerifyCache(t, 16)
	raw, sig, a, now := signedRegistration(t)
	// The fixture is issued at 10000 and expires at 40000.
	if _, err := VerifyWritableBackendRegistration(raw, sig, a, now); err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyWritableBackendRegistration(raw, sig, a, 39999); err != nil {
		t.Fatal("still inside the window", err)
	}
	for _, at := range []int64{40000, 40001, 1 << 40} {
		if _, err := VerifyWritableBackendRegistration(raw, sig, a, at); err == nil {
			t.Fatalf("cached verification outlived the window at %d", at)
		}
	}
	// A clock sample earlier than the one that verified is not served from the cache either: the
	// uncached check refuses a document issued after the sample.
	if _, err := VerifyWritableBackendRegistration(raw, sig, a, 9999); err == nil {
		t.Fatal("cache served a verification to a clock before the document was issued")
	}
	if _, err := VerifyWritableBackendRegistration(raw, sig, a, -1); err == nil {
		t.Fatal("negative clock accepted")
	}
}

func TestVerifyCacheRekeyedAuthorityIsReverifiedAndRefused(t *testing.T) {
	freshVerifyCache(t, 16)
	raw, sig, a, now := signedRegistration(t)
	if _, err := VerifyWritableBackendRegistration(raw, sig, a, now); err != nil {
		t.Fatal(err)
	}
	other := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{77}, 32)).Public().(ed25519.PublicKey)
	sum := sha256.Sum256(other)
	// Each field of the authority on its own: every one is part of the cache key, so a cached
	// verification can never answer for an authority that differs in just that field.
	changes := map[string]func(*Authority){
		"deployment":       func(x *Authority) { x.DeploymentID = "9" + x.DeploymentID[1:] },
		"epoch":            func(x *Authority) { x.ControllerEpoch = "9" + x.ControllerEpoch[1:] },
		"control revision": func(x *Authority) { x.ControlRevision++ },
		"fingerprint":      func(x *Authority) { x.Fingerprint = hex.EncodeToString(sum[:]) },
		"public key":       func(x *Authority) { x.PublicKey = other },
		"key and fingerprint": func(x *Authority) {
			x.PublicKey, x.Fingerprint = other, hex.EncodeToString(sum[:])
		},
	}
	hitsBefore, _, _ := VerifyCacheStats()
	for name, change := range changes {
		changed := a
		change(&changed)
		if _, err := VerifyWritableBackendRegistration(raw, sig, changed, now); err == nil {
			t.Fatalf("a changed %s accepted a cached verification", name)
		}
	}
	if h, _, _ := VerifyCacheStats(); h != hitsBefore {
		t.Fatalf("a changed authority was answered from the cache: hits %d -> %d", hitsBefore, h)
	}
	// The original authority is still served: the entry was never replaced or evicted by refusals.
	if _, err := VerifyWritableBackendRegistration(raw, sig, a, now); err != nil {
		t.Fatal(err)
	}
	if h, _, _ := VerifyCacheStats(); h != hitsBefore+1 {
		t.Fatal("the original authority no longer hits")
	}
}

func TestVerifyCacheDoesNotStoreFailures(t *testing.T) {
	freshVerifyCache(t, 16)
	raw, sig, a, now := signedRegistration(t)
	freshVerifyCache(t, 16)
	bad := bytes.Clone(sig)
	bad[0] ^= 1
	if _, err := VerifyWritableBackendRegistration(raw, bad, a, now); err == nil {
		t.Fatal("forged signature accepted")
	}
	// The three nested layers genuinely verified and are remembered; the forged outer one is not.
	_, _, nested := VerifyCacheStats()
	if nested != 3 {
		t.Fatalf("expected only the verified nested layers to be stored, got %d entries", nested)
	}
	for i := 0; i < 3; i++ {
		if _, err := VerifyWritableBackendRegistration(raw, bad, a, now); err == nil {
			t.Fatal("forged signature accepted from the cache")
		}
	}
	if _, _, n := VerifyCacheStats(); n != nested {
		t.Fatalf("a failed verification was cached: %d entries", n)
	}
	freshVerifyCache(t, 16)
	if _, err := VerifyWritableBackendRegistration(raw, sig, a, 40000); err == nil {
		t.Fatal("expired document accepted")
	}
	if _, _, n := VerifyCacheStats(); n != 0 {
		t.Fatal("an expired verification was cached")
	}
	if _, err := VerifyWritableBackendRegistration(raw, sig, a, now); err != nil {
		t.Fatal("the genuine document must still verify after failures", err)
	}
	if _, _, n := VerifyCacheStats(); n != 4 {
		t.Fatalf("expected four layers stored for the genuine document, got %d", n)
	}
}

func TestVerifyCacheIsBounded(t *testing.T) {
	// The three nested layers of every document are shared, so with a bound of five only two outer
	// registrations can be resident at once.
	const bound = 5
	freshVerifyCache(t, bound)
	c, key, a, now := writableRegistrationFixture(t)
	type signed struct{ raw, sig []byte }
	var docs []signed
	for i := 0; i < 8; i++ {
		c.EffectID = fmt.Sprintf("b000000%d-0000-4000-8000-000000000000", i)
		var err error
		c.PayloadSHA256, err = WritableBackendRegistrationLogicalDigest(c)
		if err != nil {
			t.Fatal(err)
		}
		raw, sig, err := SignWritableBackendRegistration(c, key, a, now)
		if err != nil {
			t.Fatal(err)
		}
		docs = append(docs, signed{raw, sig})
	}
	freshVerifyCache(t, bound)
	for _, d := range docs {
		if _, err := VerifyWritableBackendRegistration(d.raw, d.sig, a, now); err != nil {
			t.Fatal(err)
		}
		if _, _, n := VerifyCacheStats(); n > bound {
			t.Fatalf("cache grew past its bound: %d", n)
		}
	}
	if _, _, n := VerifyCacheStats(); n != bound {
		t.Fatalf("cache did not fill to its bound: %d", n)
	}
	// The most recent document is resident: no new verification happens.
	_, misses, _ := VerifyCacheStats()
	if _, err := VerifyWritableBackendRegistration(docs[7].raw, docs[7].sig, a, now); err != nil {
		t.Fatal(err)
	}
	if _, m, _ := VerifyCacheStats(); m != misses {
		t.Fatal("a recent entry was evicted")
	}
	// The oldest was evicted long ago: it must be verified again, and still verifies.
	if _, err := VerifyWritableBackendRegistration(docs[0].raw, docs[0].sig, a, now); err != nil {
		t.Fatal(err)
	}
	if _, m, _ := VerifyCacheStats(); m != misses+1 {
		t.Fatal("the least recently used entry survived eviction")
	}
	if _, _, n := VerifyCacheStats(); n > bound {
		t.Fatalf("cache grew past its bound: %d", n)
	}
}

func TestVerifyCacheEveryLayerOfAChainIsServed(t *testing.T) {
	freshVerifyCache(t, 64)
	raw, sig, a, now := signedRegistration(t)
	freshVerifyCache(t, 64)
	// Verifying the outer registration populates every nested layer, so the inner checks a signer
	// repeats on the same bytes cost a lookup, not a signature check.
	v, err := VerifyWritableBackendRegistration(raw, sig, a, now)
	if err != nil {
		t.Fatal(err)
	}
	cand := v.Command()
	hits, _, _ := VerifyCacheStats()
	if _, err := VerifyWritableCandidate(cand.CandidateRaw, cand.CandidateSignature, a, now); err != nil {
		t.Fatal(err)
	}
	if h, _, _ := VerifyCacheStats(); h != hits+1 {
		t.Fatal("nested candidate verification was not served from the cache")
	}
}
