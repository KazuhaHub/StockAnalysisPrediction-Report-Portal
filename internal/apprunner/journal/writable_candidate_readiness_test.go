//go:build linux || darwin

package journal

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/driver"
)

func TestWritableCandidateReadinessMetadata(t *testing.T) {
	r := WritableCandidateReadiness{Format: writableCandidateReadinessFormat, PayloadSHA256: strings.Repeat("a", 64), RunningReceiptSHA256: strings.Repeat("b", 64), NativeID: strings.Repeat("c", 64), Protocol: "portal.backend/1", DataFormat: "1", ObservedMS: "1000"}
	raw, _ := json.Marshal(r)
	if got, err := ParseWritableCandidateReadiness(raw); err != nil || got != r {
		t.Fatal("canonical readiness", err)
	}
	for _, bad := range [][]byte{nil, append(raw, ' '), []byte(strings.Replace(string(raw), `"data_format":"1"`, `"data_format":"0"`, 1)), []byte(strings.Replace(string(raw), `"protocol":"portal.backend/1"`, `"protocol":"other"`, 1)), []byte(strings.Replace(string(raw), `"observed_ms":"1000"`, `"observed_ms":"-1"`, 1)), []byte(strings.TrimSuffix(string(raw), "}") + `,"extra":true}`), []byte(strings.Replace(string(raw), writableCandidateReadinessFormat, "portal.runner.candidate-readiness/1-dev.1", 1)), []byte(strings.TrimSuffix(string(raw), "}") + `,"native_id":"` + r.NativeID + `"}`)} {
		if got, err := ParseWritableCandidateReadiness(bad); err == nil || got.Format != "" {
			t.Fatal("ambiguous readiness accepted")
		}
	}
	if _, err := ParseCandidateReadiness(raw); err == nil {
		t.Fatal("writable metadata crossed readonly protocol")
	}
	if r, err := new(Store).ObserveWritableCandidateReadiness(context.Background(), nil, nil, nil, nil); err == nil || r.Format != "" {
		t.Fatal("missing runtime supplied readiness")
	}
}
func TestWritableCandidateReadinessRequiresCurrentAppliedAndOpaqueProof(t *testing.T) {
	s, raw, sig, r := syntheticWritableRunningFixture(t)
	defer s.Close()
	before, err := s.prepareWritableCandidateReadiness(context.Background(), raw, sig, nil)
	if err != nil || before.running.NativeID != r.NativeID || before.expected.Binding.ClaimID != r.Claim.ClaimID {
		t.Fatal("current readiness source", err)
	}
	if got, err := s.finishWritableCandidateReadiness(context.Background(), before, driver.WritableCandidateReadinessObservation{}); !errors.Is(err, driver.ErrCandidateNotReady) || got.Format != "" {
		t.Fatal("zero native proof supplied readiness", err)
	}
	if after, err := s.ReadWritableCandidateRunningReceipt(context.Background(), raw, sig); err != nil || !sameWritableReceipt(after, r) {
		t.Fatal("readiness changed history", err)
	}
	unknown, uraw, usig, _ := unknownWritableRecoveryFixture(t)
	defer unknown.Close()
	if _, err := unknown.prepareWritableCandidateReadiness(context.Background(), uraw, usig, nil); err == nil {
		t.Fatal("unknown start treated as ready")
	}
}
func TestWritableCandidateReadinessRechecksCurrentFactsAfterIO(t *testing.T) {
	for _, mode := range []string{"authority", "ack", "writer_ack", "fence", "source", "native", "resource_revision", "receipt", "time", "cancelled"} {
		t.Run(mode, func(t *testing.T) {
			s, raw, sig, _ := syntheticWritableRunningFixture(t)
			defer s.Close()
			before, err := s.prepareWritableCandidateReadiness(context.Background(), raw, sig, nil)
			if err != nil {
				t.Fatal(err)
			}
			queries := map[string]string{
				"authority":         `UPDATE runner_deployment SET control_revision=control_revision+1`,
				"ack":               `UPDATE runner_commands SET ack_attempted_ms=updated_ms WHERE stage='start'`,
				"writer_ack":        `UPDATE runner_commands SET ack_attempted_ms=updated_ms WHERE kind='activate'`,
				"fence":             `UPDATE runner_resources SET writer_fence=writer_fence+1 WHERE kind='container'`,
				"source":            `UPDATE runner_commands SET receipt_json='{}' WHERE kind='activate'`,
				"native":            `UPDATE runner_resources SET native_id='` + strings.Repeat("d", 64) + `' WHERE kind='container'`,
				"resource_revision": `UPDATE runner_resources SET revision=3 WHERE kind='container'`,
				"receipt":           `UPDATE runner_commands SET receipt_json='{}' WHERE stage='start'`,
				"time":              `UPDATE runner_commands SET updated_ms=updated_ms+1 WHERE stage='start'`,
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if mode == "cancelled" {
				cancel()
			} else {
				if _, err := s.db.Exec(queries[mode]); err != nil {
					t.Fatal(err)
				}
			}
			// Invalid current facts must be rejected before the opaque-proof check. A
			// zero proof cannot conceal a guard which skipped its post-IO reread.
			if got, err := s.finishWritableCandidateReadiness(ctx, before, driver.WritableCandidateReadinessObservation{}); err == nil || errors.Is(err, driver.ErrCandidateNotReady) || got.Format != "" {
				t.Fatal("postflight did not reject current drift first", mode, err)
			}
		})
	}
}
