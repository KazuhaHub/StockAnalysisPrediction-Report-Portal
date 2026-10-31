//go:build linux && (amd64 || arm64)

package journal

import (
	"context"
	"encoding/json"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/data"
	"strconv"
	"strings"
	"testing"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
)

// Called only by the explicitly gated disposable native writer fixture, once
// with its actual execution receipt and once with actual recovered evidence.
func exerciseNativeWritableCandidateIntent(t *testing.T, ctx context.Context, s *Store, writerRaw, writerSig []byte, effectID string, root *data.Root, pin data.RootIdentity) {
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

	project, _ := strconv.ParseUint(receipt.Native.ProjectID, 10, 32)
	physical, err := root.InspectWriteAccess(ctx, pin, view.DataNamespaceID, uint32(project))
	if err != nil {
		t.Fatal(err)
	}
	reservation, err := s.ReserveWritableCandidate(ctx, raw, sig)
	if err != nil || reservation.Replayed || reservation.ResourceID != view.ResourceID || reservation.WriterReceiptSHA256 != view.WriterReceiptSHA256 {
		t.Fatal("actual-source reservation", err)
	}
	expected := reservation
	expected.Replayed = true
	if replay, err := s.ReadWritableCandidateReservation(ctx, raw, sig); err != nil || replay != expected {
		t.Fatal("actual-source pending resource read", err)
	}
	claim, err := s.ClaimWritableCandidate(ctx, raw, sig)
	if err != nil {
		t.Fatal("actual-source one-shot claim", err)
	}
	attempt, err := s.RunWritableCandidateOnce(ctx, claim, func(ctx context.Context, p WritableCandidatePlan) error {
		if p.Reservation != expected || p.Writer != receipt || p.Candidate.Payload.ResourceID != view.ResourceID {
			t.Fatal("actual-source plan rebound")
		}
		current, err := root.InspectWriteAccess(ctx, pin, view.DataNamespaceID, uint32(project))
		if err != nil || current != physical {
			t.Fatal("actual allocation changed during readonly callback", err)
		}
		return nil
	})
	if err != nil || !attempt.Invoked {
		t.Fatal("actual-source guarded invocation", err)
	}
	if again, err := s.RunWritableCandidateOnce(ctx, claim, func(context.Context, WritableCandidatePlan) error {
		t.Fatal("actual-source repeated invocation")
		return nil
	}); err == nil || again.Invoked {
		t.Fatal("actual-source one-shot replay")
	}
	if _, err := s.ClaimWritableCandidate(ctx, raw, sig); err == nil {
		t.Fatal("actual-source unknown work reclaimed")
	}
	if scalar(t, s.db, `SELECT COUNT(*) FROM runner_commands WHERE effect_id='`+effectID+`' AND state='unknown' AND ack_attempted_ms IS NULL`) != 1 || scalar(t, s.db, `SELECT COUNT(*) FROM runner_resources WHERE id='`+view.ResourceID+`' AND native_id='`+writableCandidatePendingPrefix+view.ResourceID+`' AND observed_state='unknown' AND revision=1 AND observed_ms IS NULL`) != 1 {
		t.Fatal("readonly callback invented process success")
	}
	after, err := root.InspectWriteAccess(ctx, pin, view.DataNamespaceID, uint32(project))
	if err != nil || after != physical {
		t.Fatal("reservation/claim changed physical data", err)
	}
}
