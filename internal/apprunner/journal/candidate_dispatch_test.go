//go:build linux || darwin

package journal

import (
	"context"
	"os"
	"testing"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/driver"
)

func candidateDescriptorFixture(t *testing.T) []byte {
	t.Helper()
	raw, err := os.ReadFile("../../appplatform/testdata/descriptors/managed.json")
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
func TestCandidateDispatchRejectsInvalidRequestBeforeAdmission(t *testing.T) {
	for _, kind := range []string{"descriptor", "static", "release", "signature", "cancelled", "missing_raw"} {
		t.Run(kind, func(t *testing.T) {
			s, _ := enrolledCommandStore(t)
			descriptor := candidateDescriptorFixture(t)
			c := journalCandidateFixture(t)
			c.Payload.ReleaseSHA256 = candidateHash(descriptor)
			raw, sig := signJournalCandidate(t, c)
			ctx := context.Background()
			switch kind {
			case "descriptor":
				descriptor = []byte(`{}`)
			case "static":
				var err error
				descriptor, err = os.ReadFile("../../appplatform/testdata/descriptors/static.json")
				if err != nil {
					t.Fatal(err)
				}
			case "release":
				descriptor = append(descriptor, ' ')
			case "signature":
				sig[0] ^= 1
			case "cancelled":
				cancelled, cancel := context.WithCancel(ctx)
				cancel()
				ctx = cancelled
			case "missing_raw":
				raw = nil
			}
			if got, err := s.DispatchCandidate(ctx, raw, sig, nil, descriptor); err == nil || got.Format != "" {
				t.Fatal("invalid dispatch accepted", kind, err)
			}
			if scalar(t, s.db, `SELECT COUNT(*) FROM runner_commands`)+scalar(t, s.db, `SELECT COUNT(*) FROM runner_resources`) != 0 {
				t.Fatal("invalid dispatch admitted or executed work")
			}
		})
	}
}
func TestCandidateDispatchWithoutNativeRuntimeDoesNotClaimOrInventSuccess(t *testing.T) {
	s, _ := enrolledCommandStore(t)
	descriptor := candidateDescriptorFixture(t)
	c := journalCandidateFixture(t)
	c.Payload.ReleaseSHA256 = candidateHash(descriptor)
	raw, sig := signJournalCandidate(t, c)
	for _, native := range []*driver.CandidateRuntime{nil, {}} {
		if got, err := s.DispatchCandidate(context.Background(), raw, sig, native, descriptor); err == nil || got.Format != "" {
			t.Fatal("missing native/data evidence produced success")
		}
		if scalar(t, s.db, `SELECT COUNT(*) FROM runner_commands WHERE state='prepared' AND receipt_json='{}'`) != 1 || scalar(t, s.db, `SELECT COUNT(*) FROM runner_resources`) != 0 {
			t.Fatal("dispatch without physical evidence claimed a candidate")
		}
	}
}
func TestCandidateHistoricalReadRefusesNewPendingOperation(t *testing.T) {
	s, raw, sig, _ := appliedCandidateReceiptFixture(t)
	next := journalCandidateFixture(t)
	next.EffectID, next.OperationID, next.Payload.ResourceID, next.TargetGeneration = fixtureID(950), fixtureID(951), fixtureID(952), "2"
	nextRaw, nextSig := signJournalCandidate(t, next)
	if _, err := s.AdmitCandidateCommand(context.Background(), nextRaw, nextSig); err != nil {
		t.Fatal(err)
	}
	if got, err := s.ReadCandidateRunningReceipt(context.Background(), raw, sig); err == nil || got.Format != "" {
		t.Fatal("old receipt crossed pending operation barrier")
	}
}
