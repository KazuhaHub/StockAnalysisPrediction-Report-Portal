package service

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"testing"
)

func enrollmentFixture() (Controller, []byte) {
	public := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{7}, ed25519.SeedSize)).Public().(ed25519.PublicKey)
	sum := sha256.Sum256(public)
	return Controller{DeploymentID: "11111111-2222-4333-8444-555555555555", ControllerEpoch: "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee", ControlRevision: 1, Fingerprint: hex.EncodeToString(sum[:])}, public
}

// The document Portal hands an administrator is exactly the one host-setup reads.
func TestMarshalEnrollmentRoundTripsThroughParseEnrollment(t *testing.T) {
	controller, public := enrollmentFixture()
	raw, err := MarshalEnrollment(controller, public)
	if err != nil {
		t.Fatal(err)
	}
	doc, err := ParseEnrollment(raw)
	if err != nil {
		t.Fatalf("host-setup refuses Portal's document: %v\n%s", err, raw)
	}
	if doc.Controller != controller || doc.Format != BootstrapFormat || doc.JournalDir != "" {
		t.Fatalf("round trip changed the identity: %+v", doc)
	}
	key, err := EnrollmentPublicKey(doc)
	if err != nil || !bytes.Equal(key, public) {
		t.Fatalf("public key: %v", err)
	}
	if bytes.Contains(raw, []byte("journal_dir")) {
		t.Fatalf("the document names a host path: %s", raw)
	}
	again, _ := MarshalEnrollment(controller, public)
	if !bytes.Equal(raw, again) {
		t.Fatal("the document is not deterministic")
	}
}

func TestMarshalEnrollmentRefusesAnInconsistentIdentity(t *testing.T) {
	controller, public := enrollmentFixture()
	wrongPin := controller
	wrongPin.Fingerprint = hex.EncodeToString(make([]byte, 32))
	badUUID := controller
	badUUID.DeploymentID = "NOT-A-UUID"
	zeroRevision := controller
	zeroRevision.ControlRevision = 0
	for name, c := range map[string]struct {
		controller Controller
		key        []byte
	}{
		"pin": {wrongPin, public}, "uuid": {badUUID, public}, "revision": {zeroRevision, public}, "short key": {controller, public[:31]},
	} {
		if _, err := MarshalEnrollment(c.controller, c.key); !errors.Is(err, ErrConfig) {
			t.Errorf("%s: %v", name, err)
		}
	}
}
