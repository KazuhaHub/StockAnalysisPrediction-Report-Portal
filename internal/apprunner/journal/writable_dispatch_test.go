//go:build linux || darwin

package journal

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/driver"
)

func TestWriterDispatchRejectsInvalidInputBeforeAdmission(t *testing.T) {
	s, raw, sig, descriptor := syntheticWriterExecutionFixture(t)
	ctx := context.Background()
	native := &driver.CandidateRuntime{}
	before := scalar(t, s.db, `SELECT COUNT(*) FROM runner_commands`)
	for _, mode := range []string{"nil_native", "descriptor", "signature", "cancel"} {
		d, signature, runtime := descriptor, append([]byte(nil), sig...), native
		requestCtx := ctx
		switch mode {
		case "nil_native":
			runtime = nil
		case "descriptor":
			d = []byte(`{}`)
		case "signature":
			signature[0] ^= 1
		case "cancel":
			var cancel context.CancelFunc
			requestCtx, cancel = context.WithCancel(ctx)
			cancel()
		}
		if _, err := s.DispatchDataWriter(requestCtx, raw, signature, d, runtime); err == nil {
			t.Fatal(mode)
		}
		if n := scalar(t, s.db, `SELECT COUNT(*) FROM runner_commands`); n != before {
			t.Fatal("invalid input admitted", mode, n)
		}
	}
}
func TestWriterDispatchCannotRepeatUnknownAttempt(t *testing.T) {
	s, raw, sig, descriptor := syntheticWriterExecutionFixture(t)
	if _, err := s.AdmitDataWriterCommand(context.Background(), raw, sig); err != nil {
		t.Fatal(err)
	}
	claim, err := s.ClaimDataWriter(context.Background(), raw, sig)
	if err != nil {
		t.Fatal(err)
	}
	before := scalar(t, s.db, `SELECT COUNT(*) FROM runner_commands WHERE state='unknown'`)
	if _, err := s.DispatchDataWriter(context.Background(), raw, sig, descriptor, &driver.CandidateRuntime{}); err == nil {
		t.Fatal("unknown writer redispatched")
	}
	if scalar(t, s.db, `SELECT COUNT(*) FROM runner_commands WHERE state='unknown'`) != before {
		t.Fatal("unknown state changed")
	}
	var command control.DataWriterCommand
	if err := json.Unmarshal(raw, &command); err != nil {
		t.Fatal(err)
	}
	var marker string
	if err := s.db.QueryRow(`SELECT receipt_json FROM runner_commands WHERE effect_id=?`, command.EffectID).Scan(&marker); err != nil {
		t.Fatal(err)
	}
	if marker != claim.state.marker {
		t.Fatal("unknown claim changed")
	}
}

func TestWriterDispatchReplaysCompletedHistoryWithoutFirstAdmission(t *testing.T) {
	s, raw, sig, descriptor := syntheticWriterExecutionFixture(t)
	if _, err := s.AdmitDataWriterCommand(context.Background(), raw, sig); err != nil {
		t.Fatal(err)
	}
	receipt := syntheticSettleWriterReceipt(t, s, raw, sig)
	// Admission deliberately keeps the sealed/pre-start source contract.
	if _, err := s.AdmitDataWriterCommand(context.Background(), raw, sig); err == nil {
		t.Fatal("first admission accepted written source")
	}
	replay, err := s.DispatchDataWriter(context.Background(), raw, sig, descriptor, &driver.CandidateRuntime{})
	if err != nil || replay != receipt {
		t.Fatal("completed writer replay", err)
	}
	if scalar(t, s.db, `SELECT revision FROM runner_resources WHERE kind='volume'`) != 3 {
		t.Fatal("replay changed writer metadata")
	}
}
