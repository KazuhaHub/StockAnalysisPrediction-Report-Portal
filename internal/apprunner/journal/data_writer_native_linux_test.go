//go:build linux && (amd64 || arm64)

package journal

import (
	"context"
	"strconv"
	"testing"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/data"
)

// Called only from the explicit disposable native journal phase after its
// existing recovery/count checks. The source is the actual newly applied XFS
// receipt, not a synthetic observation. This proves admission/claim behavior;
// its callback deliberately reads only and grants no physical writer permission.
func exerciseNativeDataWriterIntent(t *testing.T, s *Store, root *data.Root, pin data.RootIdentity, raw, signature []byte, source DataPreparationReceipt) {
	t.Helper()
	ctx := context.Background()
	c := writerCommandFixture(t, raw, signature, source)
	writerRaw, writerSignature := signWriterFixture(t, c)
	project, err := strconv.ParseUint(source.ProjectID, 10, 32)
	if err != nil {
		t.Fatal(err)
	}
	before, err := root.Inspect(ctx, pin, source.Claim.ResourceID, uint32(project))
	if err != nil || source.NamespaceInode != strconv.FormatUint(before.Inode, 10) {
		t.Fatal("native writer source", err)
	}
	admitted, err := s.AdmitDataWriterCommand(ctx, writerRaw, writerSignature)
	if err != nil || admitted.State != "prepared" || admitted.Replayed {
		t.Fatal("native writer intent", admitted, err)
	}
	claim, err := s.ClaimDataWriter(ctx, writerRaw, writerSignature)
	if err != nil {
		t.Fatal(err)
	}
	attempt, err := s.RunDataWriterOnce(ctx, claim, func(ctx context.Context, p DataWriterPlan) error {
		if p.Source != source || p.Command.DataReceiptSHA256 != c.DataReceiptSHA256 {
			t.Fatal("actual source receipt changed")
		}
		observed, err := root.Inspect(ctx, pin, source.Claim.ResourceID, uint32(project))
		if err != nil || observed != before {
			t.Fatal("claim changed sealed XFS data", err)
		}
		return nil
	})
	if err != nil || !attempt.Invoked {
		t.Fatal("native writer claim guard", attempt, err)
	}
	if _, err := s.ClaimDataWriter(ctx, writerRaw, writerSignature); err == nil {
		t.Fatal("native writer claim repeated")
	}
	if attempt, err := s.RunDataWriterOnce(ctx, claim, func(context.Context, DataWriterPlan) error { t.Fatal("native writer attempted twice"); return nil }); err == nil || attempt.Invoked {
		t.Fatal("native writer invocation repeated")
	}
	after, err := root.Inspect(ctx, pin, source.Claim.ResourceID, uint32(project))
	if err != nil || after != before || scalar(t, s.db, `SELECT COUNT(*) FROM runner_commands WHERE effect_id='`+c.EffectID+`' AND state='unknown' AND ack_attempted_ms IS NULL`) != 1 {
		t.Fatal("writer metadata fabricated permission/completion", err)
	}
	t.Log("NATIVE_DATA_WRITER_INTENT_VERIFIED")
}
