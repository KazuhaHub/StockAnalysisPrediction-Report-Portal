package control

import (
	"reflect"
	"testing"
)

// One relayed request verifies the same signed request command at the transport, at the journal entry,
// after the preflight and twice more in the postflight. Only the first of those may do the parse.
func TestWritableBackendRequestVerificationIsRememberedForItsWindow(t *testing.T) {
	c, key, a, now, _, _ := writableBackendRequestFixture(t)
	raw, sig, err := SignWritableBackendRequest(c, key, a, now)
	if err != nil {
		t.Fatal(err)
	}
	freshVerifyCache(t, 16)
	first, err := VerifyWritableBackendRequest(raw, sig, a, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := verifiedCommands.get(verifyCacheKey("writable-backend-request", raw, sig, a), now+1); !ok {
		t.Fatal("a successful request verification was not remembered")
	}
	_, missesAfterFirst, _ := VerifyCacheStats()
	for i := int64(1); i <= 4; i++ {
		again, err := VerifyWritableBackendRequest(raw, sig, a, now+i)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(first.Command(), again.Command()) || !reflect.DeepEqual(first.Registration().Command(), again.Registration().Command()) {
			t.Fatal("a remembered verification differs from the verified value")
		}
	}
	if hits, misses, _ := VerifyCacheStats(); hits < 4 || misses != missesAfterFirst {
		t.Fatalf("repeat verifications must be served from memory: hits=%d misses=%d (after first %d)", hits, misses, missesAfterFirst)
	}
	// A caller that scribbles on a getter's copy must not poison later hits.
	cmd := first.Command()
	cmd.RegistrationRaw[0] ^= 1
	if again, err := VerifyWritableBackendRequest(raw, sig, a, now+5); err != nil || again.Command().RegistrationRaw[0] != c.RegistrationRaw[0] {
		t.Fatal("a hit aliased caller-visible bytes", err)
	}
}

// A remembered success never outlives the registration window, never answers for another authority or
// another signature, and a failure is always recomputed.
func TestWritableBackendRequestVerificationMemoryFailsClosed(t *testing.T) {
	c, key, a, now, _, _ := writableBackendRequestFixture(t)
	raw, sig, err := SignWritableBackendRequest(c, key, a, now)
	if err != nil {
		t.Fatal(err)
	}
	reg, err := VerifyWritableBackendRegistration(c.RegistrationRaw, c.RegistrationSignature, a, now)
	if err != nil {
		t.Fatal(err)
	}
	expires := expiryMS(reg.Candidate().Index().ExpiresMS)
	freshVerifyCache(t, 16)
	if _, err := VerifyWritableBackendRequest(raw, sig, a, now); err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyWritableBackendRequest(raw, sig, a, expires); err == nil {
		t.Fatal("a remembered request outlived its registration window")
	}
	other := a
	other.ControlRevision++
	if _, err := VerifyWritableBackendRequest(raw, sig, other, now+1); err == nil {
		t.Fatal("a remembered request answered for another control revision")
	}
	bad := append([]byte(nil), sig...)
	bad[0] ^= 1
	for i := 0; i < 2; i++ {
		if _, err := VerifyWritableBackendRequest(raw, bad, a, now+1); err == nil {
			t.Fatal("a changed signature was accepted")
		}
	}
}

func BenchmarkVerifyWritableBackendRequestFiveTimes(b *testing.B) {
	t := &testing.T{}
	c, key, a, now, _, _ := writableBackendRequestFixture(t)
	raw, sig, err := SignWritableBackendRequest(c, key, a, now)
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		// A relayed request is a new signature every time: start from an empty memory.
		verifiedCommands = newVerifyCache(VerifyCacheCapacity)
		for j := int64(0); j < 5; j++ {
			if _, err := VerifyWritableBackendRequest(raw, sig, a, now+j); err != nil {
				b.Fatal(err)
			}
		}
	}
}
