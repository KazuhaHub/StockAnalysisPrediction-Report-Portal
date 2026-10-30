//go:build linux || darwin

package journal

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
)

func readyCandidateFixture(t *testing.T) (*Store, []byte, []byte) {
	t.Helper()
	s, _, _, _ := appliedReceiptFixture(t)
	c := journalCandidateFixture(t)
	c.EffectID = fixtureID(430)
	raw, sig := signJournalCandidate(t, c)
	if _, err := s.AdmitCandidateCommand(context.Background(), raw, sig); err != nil {
		t.Fatal(err)
	}
	return s, raw, sig
}
func TestCandidateReservationRequiresAppliedDataHistory(t *testing.T) {
	s, _ := enrolledCommandStore(t)
	c := journalCandidateFixture(t)
	raw, sig := signJournalCandidate(t, c)
	if _, err := s.AdmitCandidateCommand(context.Background(), raw, sig); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReserveCandidate(context.Background(), raw, sig); err == nil {
		t.Fatal("missing data history accepted")
	}
	if scalar(t, s.db, `SELECT COUNT(*) FROM runner_resources`) != 0 {
		t.Fatal("failed reservation fabricated resource")
	}
}
func TestCandidateReservationAndOneShotClaim(t *testing.T) {
	s, raw, sig := readyCandidateFixture(t)
	reserved, err := s.ReserveCandidate(context.Background(), raw, sig)
	if err != nil || reserved.Replayed {
		t.Fatal("candidate reservation", reserved, err)
	}
	replay, err := s.ReserveCandidate(context.Background(), raw, sig)
	if err != nil || !replay.Replayed || reserved.OwnershipSHA256 != replay.OwnershipSHA256 {
		t.Fatal("reservation replay", replay, err)
	}
	claim, err := s.ClaimCandidate(context.Background(), raw, sig)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := json.Marshal(claim); !errors.Is(err, ErrCandidateClaimUnavailable) {
		t.Fatal("claim serialized into authority", err)
	}
	if _, err := s.ClaimCandidate(context.Background(), raw, sig); err == nil {
		t.Fatal("uncertain work reclaimed")
	}
	copy := claim
	var calls atomic.Int32
	run := func(ctx context.Context, r CandidateReservation, p control.CandidatePayload, receipt DataPreparationReceipt) error {
		calls.Add(1)
		if r.ResourceID != p.ResourceID || r.OwnershipSHA256 != reserved.OwnershipSHA256 || receipt.Claim.ResourceID != p.DataNamespaceID {
			t.Fatal("claim changed its resource/data proof")
		}
		if _, ok := ctx.Deadline(); !ok {
			t.Fatal("candidate callback missing envelope deadline")
		}
		return errors.New("synthetic provider failure")
	}
	attempted, err := s.RunCandidateOnce(context.Background(), claim, run)
	if !attempted.Invoked || err == nil || calls.Load() != 1 {
		t.Fatal("one-shot attempt", attempted, err, calls.Load())
	}
	if attempted, err := s.RunCandidateOnce(context.Background(), copy, run); err == nil || attempted.Invoked || calls.Load() != 1 {
		t.Fatal("copied handle invoked twice", attempted, err)
	}
	if scalar(t, s.db, `SELECT COUNT(*) FROM runner_commands WHERE stage='start' AND state='unknown'`) != 1 || scalar(t, s.db, `SELECT COUNT(*) FROM runner_resources WHERE kind='container' AND observed_state='unknown' AND observed_ms IS NULL`) != 1 {
		t.Fatal("attempt fabricated completion")
	}
}
func TestCandidateClaimRejectsAlteredDataReceipt(t *testing.T) {
	s, raw, sig := readyCandidateFixture(t)
	if _, err := s.ReserveCandidate(context.Background(), raw, sig); err != nil {
		t.Fatal(err)
	}
	claim, err := s.ClaimCandidate(context.Background(), raw, sig)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE runner_resources SET observed_state='quarantined' WHERE kind='volume'`); err != nil {
		t.Fatal(err)
	}
	called := false
	attempted, err := s.RunCandidateOnce(context.Background(), claim, func(context.Context, CandidateReservation, control.CandidatePayload, DataPreparationReceipt) error {
		called = true
		return nil
	})
	if err == nil || called || attempted.Invoked {
		t.Fatal("changed data proof reached provider", attempted, err)
	}
}
func TestCandidateClaimCannotCrossJournalOwner(t *testing.T) {
	s, raw, sig := readyCandidateFixture(t)
	if _, err := s.ReserveCandidate(context.Background(), raw, sig); err != nil {
		t.Fatal(err)
	}
	claim, err := s.ClaimCandidate(context.Background(), raw, sig)
	if err != nil {
		t.Fatal(err)
	}
	other, _ := enrolledCommandStore(t)
	if attempted, err := other.RunCandidateOnce(context.Background(), claim, func(context.Context, CandidateReservation, control.CandidatePayload, DataPreparationReceipt) error {
		t.Fatal("foreign owner callback")
		return nil
	}); err == nil || attempted.Invoked {
		t.Fatal("foreign claim accepted", attempted, err)
	}
}

func TestCandidateClaimConcurrentCopiesInvokeOnce(t *testing.T) {
	s, raw, sig := readyCandidateFixture(t)
	if _, err := s.ReserveCandidate(context.Background(), raw, sig); err != nil {
		t.Fatal(err)
	}
	claim, err := s.ClaimCandidate(context.Background(), raw, sig)
	if err != nil {
		t.Fatal(err)
	}
	var calls, attempts atomic.Int32
	var group sync.WaitGroup
	for range 8 {
		group.Go(func() {
			attempt, _ := s.RunCandidateOnce(context.Background(), claim, func(context.Context, CandidateReservation, control.CandidatePayload, DataPreparationReceipt) error {
				calls.Add(1)
				return nil
			})
			if attempt.Invoked {
				attempts.Add(1)
			}
		})
	}
	group.Wait()
	if calls.Load() != 1 || attempts.Load() != 1 {
		t.Fatal("concurrent claim replay", calls.Load(), attempts.Load())
	}
}
func TestCandidateClaimReopenCannotRecreateHandle(t *testing.T) {
	s, raw, sig := readyCandidateFixture(t)
	var databaseFile string
	if err := s.db.QueryRow(`SELECT file FROM pragma_database_list WHERE name='main'`).Scan(&databaseFile); err != nil {
		t.Fatal(err)
	}
	directory := filepath.Dir(databaseFile)
	if _, err := s.ReserveCandidate(context.Background(), raw, sig); err != nil {
		t.Fatal(err)
	}
	claim, err := s.ClaimCandidate(context.Background(), raw, sig)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if _, err := reopened.ClaimCandidate(context.Background(), raw, sig); err == nil {
		t.Fatal("restart reclaimed uncertain candidate")
	}
	attempted, err := reopened.RunCandidateOnce(context.Background(), claim, func(context.Context, CandidateReservation, control.CandidatePayload, DataPreparationReceipt) error {
		t.Fatal("old owner handle invoked")
		return nil
	})
	if err == nil || attempted.Invoked {
		t.Fatal("restarted owner used old handle")
	}
	if scalar(t, reopened.db, `SELECT COUNT(*) FROM runner_commands WHERE stage='start' AND state='unknown'`) != 1 {
		t.Fatal("restart lost durable uncertainty")
	}
}
func TestCandidateClaimCloseWaitsAndCancellationDoesNotConsume(t *testing.T) {
	s, raw, sig := readyCandidateFixture(t)
	if _, err := s.ReserveCandidate(context.Background(), raw, sig); err != nil {
		t.Fatal(err)
	}
	claim, err := s.ClaimCandidate(context.Background(), raw, sig)
	if err != nil {
		t.Fatal(err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if attempt, err := s.RunCandidateOnce(canceled, claim, func(context.Context, CandidateReservation, control.CandidatePayload, DataPreparationReceipt) error {
		t.Fatal("canceled invocation entered")
		return nil
	}); !errors.Is(err, context.Canceled) || attempt.Invoked {
		t.Fatal("canceled invocation", attempt, err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	done := make(chan error, 1)
	go func() {
		_, err := s.RunCandidateOnce(context.Background(), claim, func(context.Context, CandidateReservation, control.CandidatePayload, DataPreparationReceipt) error {
			close(entered)
			<-release
			return nil
		})
		done <- err
	}()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("candidate never entered")
	}
	closed := make(chan error, 1)
	go func() { closed <- s.Close() }()
	deadline := time.Now().Add(3 * time.Second)
	for !s.closing.Load() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !s.closing.Load() {
		t.Fatal("close never started")
	}
	select {
	case err := <-closed:
		t.Fatal("close passed live provider", err)
	default:
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := <-closed; err != nil {
		t.Fatal(err)
	}
}
func TestCandidateClaimRevalidatesAuthorityAndResource(t *testing.T) {
	cases := map[string]string{
		"controller":   `UPDATE runner_deployment SET control_revision=4`,
		"fence":        `UPDATE runner_resources SET control_fence=2 WHERE kind='container'`,
		"owner":        `UPDATE runner_resources SET installation_id='90000000-0000-4000-8000-000000000000' WHERE kind='container'`,
		"marker":       `UPDATE runner_commands SET receipt_json='{}' WHERE stage='start'`,
		"data receipt": `UPDATE runner_commands SET receipt_json='{}' WHERE stage='prepare_data'`,
	}
	for name, statement := range cases {
		t.Run(name, func(t *testing.T) {
			s, raw, sig := readyCandidateFixture(t)
			if _, err := s.ReserveCandidate(context.Background(), raw, sig); err != nil {
				t.Fatal(err)
			}
			claim, err := s.ClaimCandidate(context.Background(), raw, sig)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.db.Exec(statement); err != nil {
				t.Fatal(err)
			}
			attempt, err := s.RunCandidateOnce(context.Background(), claim, func(context.Context, CandidateReservation, control.CandidatePayload, DataPreparationReceipt) error {
				t.Fatal("stale candidate entered provider")
				return nil
			})
			if err == nil || attempt.Invoked {
				t.Fatal("stale candidate accepted", attempt, err)
			}
		})
	}
}

func TestCandidateReservationRequiresExactDataTuple(t *testing.T) {
	for _, name := range []string{"release", "schema", "namespace", "operation", "generation", "writer"} {
		t.Run(name, func(t *testing.T) {
			s, _, _, _ := appliedReceiptFixture(t)
			c := journalCandidateFixture(t)
			c.EffectID = fixtureID(430)
			switch name {
			case "release":
				c.Payload.ReleaseSHA256 = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
			case "schema":
				c.Payload.DataFormat = "2"
			case "namespace":
				c.Payload.DataNamespaceID = fixtureID(431)
			case "operation":
				c.OperationID = fixtureID(432)
			case "generation":
				c.TargetGeneration = "2"
			case "writer":
				c.Payload.WriterFence = "2"
			}
			raw, sig := signJournalCandidate(t, c)
			if _, err := s.AdmitCandidateCommand(context.Background(), raw, sig); err != nil {
				t.Fatal(err)
			}
			if _, err := s.ReserveCandidate(context.Background(), raw, sig); err == nil {
				t.Fatal("mismatched data tuple reserved")
			}
			if scalar(t, s.db, `SELECT COUNT(*) FROM runner_resources WHERE kind='container'`) != 0 {
				t.Fatal("refused reservation inserted container identity")
			}
		})
	}
}
func TestCandidateReservationRollbackAndDataReaderCoexistence(t *testing.T) {
	s, oldRaw, oldSig, _ := appliedReceiptFixture(t)
	c := journalCandidateFixture(t)
	c.EffectID = fixtureID(430)
	raw, sig := signJournalCandidate(t, c)
	if _, err := s.AdmitCandidateCommand(context.Background(), raw, sig); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`CREATE TRIGGER fixture_candidate_failure BEFORE INSERT ON runner_resources WHEN NEW.kind='container' BEGIN SELECT RAISE(ABORT,'fixture');END`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReserveCandidate(context.Background(), raw, sig); err == nil {
		t.Fatal("failed reservation committed")
	}
	if scalar(t, s.db, `SELECT COUNT(*) FROM runner_resources WHERE kind='container'`) != 0 {
		t.Fatal("partial reservation survived")
	}
	if _, err := s.db.Exec(`DROP TRIGGER fixture_candidate_failure`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReserveCandidate(context.Background(), raw, sig); err != nil {
		t.Fatal(err)
	}
	// The data query must still select exactly the owned volume when a container
	// row shares its namespace; the pending command separately blocks public reads.
	tx, facts, err := s.beginCandidateCommand(context.Background(), raw, sig, "prepared", "{}")
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = candidateDataEvidence(context.Background(), tx, facts.command, facts.now)
	tx.Rollback()
	if err != nil {
		t.Fatal("container polluted data evidence lookup", err)
	}
	if _, err := s.db.Exec(`UPDATE runner_commands SET state='reverted' WHERE stage='start'`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReadDataPreparationReceipt(context.Background(), oldRaw, oldSig); err != nil {
		t.Fatal("container row broke existing data receipt read", err)
	}
}
func TestCandidateClaimExpiredMonotonicDeadline(t *testing.T) {
	s, raw, sig := readyCandidateFixture(t)
	if _, err := s.ReserveCandidate(context.Background(), raw, sig); err != nil {
		t.Fatal(err)
	}
	claim, err := s.ClaimCandidate(context.Background(), raw, sig)
	if err != nil {
		t.Fatal(err)
	}
	claim.state.deadline = time.Now().Add(-time.Millisecond)
	attempt, err := s.RunCandidateOnce(context.Background(), claim, func(context.Context, CandidateReservation, control.CandidatePayload, DataPreparationReceipt) error {
		t.Fatal("expired handle entered")
		return nil
	})
	if err == nil || attempt.Invoked {
		t.Fatal("expired handle accepted")
	}
}
