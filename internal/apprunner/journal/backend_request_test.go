//go:build linux || darwin

package journal

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/driver"
)

func TestBackendRequestParsingAndNoRegistrationConfusion(t *testing.T) {
	r := BackendRequestResult{Format: "portal.runner.backend-request-result/1-dev.1", RequestID: fixtureID(980), RequestSHA256: strings.Repeat("a", 64), DeliveryReceiptSHA256: strings.Repeat("b", 64), NativeID: strings.Repeat("c", 64), BootID: "boot", Response: driver.BackendResponse{Status: 200, Body: []byte(`{}`)}, ObservedMS: "1"}
	raw, _ := json.Marshal(r)
	if _, err := ParseBackendRequestResult(raw); err != nil {
		t.Fatal(err)
	}
	for _, bad := range [][]byte{nil, append(raw, []byte(" {}")...), []byte(strings.Replace(string(raw), `"status":200`, `"status":302`, 1)), []byte(strings.Replace(string(raw), `"boot_id":"boot"`, `"boot_id":""`, 1)), []byte(strings.Replace(string(raw), `"observed_ms":"1"`, `"observed_ms":"01"`, 1))} {
		if _, err := ParseBackendRequestResult(bad); err == nil {
			t.Fatal("invalid result accepted")
		}
	}
	s, ref, sig, _ := appliedCandidateReceiptFixture(t)
	defer s.Close()
	registration, signature := signBackendRegistrationFixture(t, ref, sig, fixtureID(981), strings.Repeat("d", 64))
	if _, err := s.ForwardBackendRequest(context.Background(), registration, signature, []byte(`{}`), nil, "a.b", new(driver.CandidateRuntime)); err == nil {
		t.Fatal("registration authority alone authorized request")
	}
	if scalar(t, s.db, `SELECT COUNT(*) FROM runner_commands WHERE stage='activate'`) != 0 {
		t.Fatal("refused relay created durable work")
	}
}
