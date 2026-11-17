//go:build linux || darwin

package journal

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/driver"
)

func TestBackendHandshakeRequiresAppliedDelivery(t *testing.T) {
	s, ref, sig, _ := appliedCandidateReceiptFixture(t)
	defer s.Close()
	raw, signature := signBackendRegistrationFixture(t, ref, sig, fixtureID(970), strings.Repeat("c", 64))
	if _, err := s.AdmitBackendRegistrationCommand(context.Background(), raw, signature); err != nil {
		t.Fatal(err)
	}
	for _, state := range []string{"prepared", "unknown"} {
		if state == "unknown" {
			if _, err := s.claimBackendRegistration(context.Background(), raw, signature); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := s.ExchangeBackendHandshake(context.Background(), raw, signature, []byte(`{}`), "a.b", new(driver.CandidateRuntime)); err == nil {
			t.Fatal("unapplied delivery exchanged handshake")
		}
	}
	if scalar(t, s.db, `SELECT COUNT(*) FROM runner_commands WHERE stage='activate' AND state='applied'`) != 0 {
		t.Fatal("handshake fabricated delivery")
	}
}
func TestBackendHandshakeEvidenceParsing(t *testing.T) {
	e := BackendHandshakeEvidence{Format: "portal.runner.backend-handshake/1-dev.1", PayloadSHA256: strings.Repeat("a", 64), DeliveryReceiptSHA256: strings.Repeat("b", 64), NativeID: strings.Repeat("c", 64), ChallengeSHA256: strings.Repeat("d", 64), Answer: "a.b", ObservedMS: "1"}
	raw, _ := json.Marshal(e)
	if _, err := ParseBackendHandshakeEvidence(raw); err != nil {
		t.Fatal(err)
	}
	for _, bad := range [][]byte{nil, append(raw, []byte(" {}")...), []byte(strings.Replace(string(raw), `"answer":"a.b"`, `"answer":null`, 1)), []byte(strings.Replace(string(raw), `"observed_ms":"1"`, `"observed_ms":"01"`, 1)), []byte(strings.Replace(string(raw), `"native_id":"`, `"native_id":"f`, 1))} {
		if _, err := ParseBackendHandshakeEvidence(bad); err == nil {
			t.Fatal("invalid handshake metadata accepted")
		}
	}
}
