package transport

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/journal"
)

func observationFixture() journal.ControllerEnrollment {
	public := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{30}, 32)).Public().(ed25519.PublicKey)
	pin := sha256.Sum256(public)
	return journal.ControllerEnrollment{DeploymentID: "00000000-0000-0000-0000-000000000400", ControllerEpoch: "00000000-0000-0000-0000-000000000401", ControlRevision: 3, PublicKey: public, Fingerprint: hex.EncodeToString(pin[:])}
}
func TestEnrollmentWireStrictness(t *testing.T) {
	expected := observationFixture()
	raw, err := encodeEnrollment(expected)
	if err != nil {
		t.Fatal(err)
	}
	got, err := decodeEnrollment(raw)
	if err != nil || !bytes.Equal(got.PublicKey, expected.PublicKey) || got.Fingerprint != expected.Fingerprint {
		t.Fatal("public observation changed identity")
	}
	cases := [][]byte{append([]byte(" "), raw...), append(append([]byte{}, raw...), raw...), bytes.Replace(raw, []byte(`"control_revision":"3"`), []byte(`"control_revision":3`), 1), bytes.Replace(raw, []byte(`"control_revision":"3"`), []byte(`"control_revision":"03"`), 1), bytes.Replace(raw, []byte(`"format":`), []byte(`"unknown":"x","format":`), 1), bytes.Replace(raw, []byte(`"format":`), []byte(`"format":"duplicate","format":`), 1), bytes.Replace(raw, []byte(expected.Fingerprint), []byte(strings.Repeat("a", 64)), 1), []byte(`{}`)}
	for i, bad := range cases {
		if _, err := decodeEnrollment(bad); err == nil {
			t.Fatalf("accepted noncanonical/invalid observation %d", i)
		}
	}
	if _, err := encodeEnrollment(journal.ControllerEnrollment{}); err == nil {
		t.Fatal("encoded unenrolled identity")
	}
}
