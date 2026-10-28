//go:build linux && (amd64 || arm64)

package journal

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
)

// Called only by the explicitly gated disposable native writer fixture, once
// with its actual execution receipt and once with actual recovered evidence.
func exerciseNativeWritableCandidateIntent(t *testing.T, ctx context.Context, s *Store, writerRaw, writerSig []byte, effectID string) {
	t.Helper()
	receipt, err := s.ReadDataWriterReceipt(ctx, writerRaw, writerSig)
	if err != nil {
		t.Fatal("actual writer source for writable intent", err)
	}
	before, _ := json.Marshal(receipt)
	c := writableIntentFixture(t, writerRaw, writerSig, receipt)
	c.EffectID = effectID
	wrong := c
	wrong.WriterReceiptSHA256 = strings.Repeat("f", 64)
	wrongRaw, wrongSig := signWritableIntent(t, wrong)
	if _, err := s.AdmitWritableCandidateCommand(ctx, wrongRaw, wrongSig); err == nil {
		t.Fatal("actual source accepted rebound writer receipt")
	}
	raw, sig := signWritableIntent(t, c)
	if _, err := s.AdmitCandidateCommand(ctx, raw, sig); err == nil {
		t.Fatal("actual writable intent crossed legacy start verifier")
	}
	first, err := s.AdmitWritableCandidateCommand(ctx, raw, sig)
	if err != nil || first.Replayed || first.State != "prepared" {
		t.Fatal("actual source writable admission", err)
	}
	if replay, err := s.AdmitWritableCandidateCommand(ctx, raw, sig); err != nil || !replay.Replayed {
		t.Fatal("actual source replay", err)
	}
	view, err := s.ReadWritableCandidateIntent(ctx, raw, sig)
	if err != nil || view.WriterReceiptSHA256 != candidateHash(before) || view.ResourceID != receipt.Claim.ResourceID {
		t.Fatal("actual completed writer source identity", err)
	}
	var writer control.DataWriterCommand
	json.Unmarshal(writerRaw, &writer)
	var persisted string
	if err := s.db.QueryRow(`SELECT receipt_json FROM runner_commands WHERE effect_id=?`, writer.EffectID).Scan(&persisted); err != nil {
		t.Fatal(err)
	}
	if persisted != string(before) {
		t.Fatal("writable intent changed native writer receipt")
	}
	if scalar(t, s.db, `SELECT COUNT(*) FROM runner_resources WHERE id='`+view.ResourceID+`'`) != 0 {
		t.Fatal("intent invented native resource")
	}
	if _, err := s.ClaimCandidate(ctx, raw, sig); err == nil {
		t.Fatal("readonly claim executed writable intent")
	}
	if _, err := s.ReadDataWriterReceipt(ctx, writerRaw, writerSig); err == nil {
		t.Fatal("old pre-start reader ignored writable start intent")
	}
}
