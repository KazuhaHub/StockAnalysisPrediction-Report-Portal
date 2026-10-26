//go:build linux || darwin

package journal

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
)

func reservedClaimFixture(t *testing.T) (*Store, string, []byte, []byte) {
	t.Helper()
	s, directory := enrolledCommandStore(t)
	raw, sig := signRunnerCommand(t, runnerCommandFixture(t))
	if _, err := s.AdmitControlCommand(context.Background(), raw, sig); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReservePrepareData(context.Background(), raw, sig); err != nil {
		t.Fatal(err)
	}
	return s, directory, raw, sig
}
func TestDataClaimDurableUnknownBeforeRun(t *testing.T) {
	s, _, raw, sig := reservedClaimFixture(t)
	claim, err := s.ClaimPrepareData(context.Background(), raw, sig)
	if err != nil || claim == (DataClaim{}) {
		t.Fatalf("claim unavailable: %+v %v", claim, err)
	}
	var state string
	if err := s.db.QueryRow(`SELECT state FROM runner_commands`).Scan(&state); err != nil || state != "unknown" {
		t.Fatal("claim failed to persist uncertainty before return", state, err)
	}
	if again, err := s.ClaimPrepareData(context.Background(), raw, sig); err == nil || again != (DataClaim{}) {
		t.Fatal("claimed twice")
	}
	count := 0
	attempt, err := s.RunPrepareDataOnce(context.Background(), claim, func(ctx context.Context, r DataReservation, p control.PrepareDataPayload) error {
		count++
		if r.ProjectID != 1 || p.DataNamespaceID != r.NamespaceID {
			t.Fatal("work identity changed")
		}
		return nil
	})
	if err != nil || !attempt.Invoked || count != 1 {
		t.Fatalf("single invocation failed: %+v %v", attempt, err)
	}
	copied := claim
	if attempt, err := s.RunPrepareDataOnce(context.Background(), copied, func(context.Context, DataReservation, control.PrepareDataPayload) error { count++; return nil }); err == nil || attempt.Invoked || count != 1 {
		t.Fatal("copied handle replayed execution")
	}
	if scalar(t, s.db, `SELECT COUNT(*) FROM runner_commands WHERE state='unknown'`) != 1 || scalar(t, s.db, `SELECT COUNT(*) FROM runner_resources WHERE observed_state='unknown' AND observed_ms IS NULL`) != 1 {
		t.Fatal("callback return fabricated physical completion")
	}
}

func TestDataClaimRequiresExactReservationAndRollsBack(t *testing.T) {
	s, _ := enrolledCommandStore(t)
	raw, sig := signRunnerCommand(t, runnerCommandFixture(t))
	if _, err := s.AdmitControlCommand(context.Background(), raw, sig); err != nil {
		t.Fatal(err)
	}
	if claim, err := s.ClaimPrepareData(context.Background(), raw, sig); err == nil || claim != (DataClaim{}) {
		t.Fatal("missing reservation claimed")
	}
	if _, err := s.ReservePrepareData(context.Background(), raw, sig); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`CREATE TRIGGER fixture_claim_failure BEFORE UPDATE ON runner_commands WHEN NEW.state='unknown' BEGIN SELECT RAISE(ABORT,'fixture claim failure');END`); err != nil {
		t.Fatal(err)
	}
	if claim, err := s.ClaimPrepareData(context.Background(), raw, sig); err == nil || claim != (DataClaim{}) {
		t.Fatal("claim insert failure ignored")
	}
	if scalar(t, s.db, `SELECT COUNT(*) FROM runner_commands WHERE state='prepared' AND receipt_json='{}'`) != 1 {
		t.Fatal("failed claim consumed command")
	}
	if _, err := s.db.Exec(`DROP TRIGGER fixture_claim_failure`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ClaimPrepareData(context.Background(), raw, sig); err != nil {
		t.Fatal("claim after rollback", err)
	}
}

func TestDataClaimConcurrentOneShot(t *testing.T) {
	s, _, raw, sig := reservedClaimFixture(t)
	type outcome struct {
		claim DataClaim
		err   error
	}
	done := make(chan outcome, 8)
	for range 8 {
		go func() { claim, err := s.ClaimPrepareData(context.Background(), raw, sig); done <- outcome{claim, err} }()
	}
	var claim DataClaim
	success := 0
	for range 8 {
		got := <-done
		if got.err == nil {
			claim = got.claim
			success++
		} else if got.claim != (DataClaim{}) {
			t.Fatal("refusal returned handle")
		}
	}
	if success != 1 {
		t.Fatal("multiple claims", success)
	}
	var calls atomic.Int32
	results := make(chan DataAttempt, 8)
	for range 8 {
		copied := claim
		go func() {
			attempt, _ := s.RunPrepareDataOnce(context.Background(), copied, func(context.Context, DataReservation, control.PrepareDataPayload) error { calls.Add(1); return nil })
			results <- attempt
		}()
	}
	invoked := 0
	for range 8 {
		if (<-results).Invoked {
			invoked++
		}
	}
	if invoked != 1 || calls.Load() != 1 {
		t.Fatal("duplicate physical invocation", invoked, calls.Load())
	}
}

func TestDataClaimCannotResumeFromJSONOrReopenedStore(t *testing.T) {
	s, directory, raw, sig := reservedClaimFixture(t)
	claim, err := s.ClaimPrepareData(context.Background(), raw, sig)
	if err != nil {
		t.Fatal(err)
	}
	rawClaim, err := json.Marshal(claim)
	if !errors.Is(err, ErrDataClaimUnavailable) || len(rawClaim) != 0 {
		t.Fatal("claim did not refuse serialization", string(rawClaim), err)
	}
	var decoded DataClaim
	if err := json.Unmarshal([]byte("{}"), &decoded); !errors.Is(err, ErrDataClaimUnavailable) {
		t.Fatal("claim accepted JSON", err)
	}
	callback := func(context.Context, DataReservation, control.PrepareDataPayload) error {
		t.Fatal("invalid handle invoked native work")
		return nil
	}
	if result, err := s.RunPrepareDataOnce(context.Background(), decoded, callback); err == nil || result.Invoked {
		t.Fatal("JSON minted claim")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if result, err := reopened.ClaimPrepareData(context.Background(), raw, sig); err == nil || result != (DataClaim{}) {
		t.Fatal("restart claimed uncertain work again")
	}
	if result, err := reopened.RunPrepareDataOnce(context.Background(), claim, callback); err == nil || result.Invoked {
		t.Fatal("old process handle survived restart")
	}
	if result, err := s.RunPrepareDataOnce(context.Background(), claim, callback); err == nil || result.Invoked {
		t.Fatal("closed owner invoked work")
	}
}

func TestDataClaimCallbackFailureStaysUnknown(t *testing.T) {
	s, _, raw, sig := reservedClaimFixture(t)
	claim, err := s.ClaimPrepareData(context.Background(), raw, sig)
	if err != nil {
		t.Fatal(err)
	}
	failure := errors.New("fixture uncertain native outcome")
	result, err := s.RunPrepareDataOnce(context.Background(), claim, func(context.Context, DataReservation, control.PrepareDataPayload) error { return failure })
	if !result.Invoked || !errors.Is(err, failure) {
		t.Fatal("invocation outcome lost", result, err)
	}
	if scalar(t, s.db, `SELECT COUNT(*) FROM runner_commands WHERE state='unknown'`) != 1 {
		t.Fatal("failed work reset command")
	}
	if _, err := s.RunPrepareDataOnce(context.Background(), claim, func(context.Context, DataReservation, control.PrepareDataPayload) error {
		t.Fatal("uncertain work replayed")
		return nil
	}); err == nil {
		t.Fatal("uncertain replay accepted")
	}
}

func TestDataClaimRevalidatesBeforeInvocation(t *testing.T) {
	for _, kind := range []string{"resource", "fence", "receipt", "state", "controller"} {
		t.Run(kind, func(t *testing.T) {
			s, _, raw, sig := reservedClaimFixture(t)
			claim, err := s.ClaimPrepareData(context.Background(), raw, sig)
			if err != nil {
				t.Fatal(err)
			}
			statement := map[string]string{"resource": `UPDATE runner_resources SET native_id='xfs-project/v1/2'`, "fence": `UPDATE runner_resources SET control_fence=2`, "receipt": `UPDATE runner_commands SET receipt_json='{}'`, "state": `UPDATE runner_commands SET state='applied'`, "controller": `UPDATE runner_deployment SET control_revision=2`}[kind]
			if _, err := s.db.Exec(statement); err != nil {
				t.Fatal(err)
			}
			result, err := s.RunPrepareDataOnce(context.Background(), claim, func(context.Context, DataReservation, control.PrepareDataPayload) error {
				t.Fatal("stale claim invoked work")
				return nil
			})
			if err == nil || result.Invoked {
				t.Fatal("stale claim accepted")
			}
		})
	}
}

func TestDataClaimCloseWaitsForSynchronousInvocation(t *testing.T) {
	s, directory, raw, sig := reservedClaimFixture(t)
	claim, err := s.ClaimPrepareData(context.Background(), raw, sig)
	if err != nil {
		t.Fatal(err)
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
		_, err := s.RunPrepareDataOnce(context.Background(), claim, func(context.Context, DataReservation, control.PrepareDataPayload) error {
			close(entered)
			<-release
			return nil
		})
		done <- err
	}()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("invocation never entered")
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
		t.Fatal("close released a live invocation", err)
	default:
	}
	if other, err := Open(directory); !errors.Is(err, ErrInUse) {
		if other != nil {
			other.Close()
		}
		t.Fatal("journal owner released during native work", err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := <-closed; err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if scalar(t, reopened.db, `SELECT COUNT(*) FROM runner_commands WHERE state='unknown'`) != 1 {
		t.Fatal("close manufactured completion")
	}
}

func TestDataClaimExpiryAfterConnectionWait(t *testing.T) {
	s, _ := enrolledCommandStore(t)
	c := runnerCommandFixture(t)
	c.ExpiresMS = strconv.FormatInt(time.Now().UnixMilli()+600, 10)
	raw, sig := signRunnerCommand(t, c)
	if _, err := s.AdmitControlCommand(context.Background(), raw, sig); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReservePrepareData(context.Background(), raw, sig); err != nil {
		t.Fatal(err)
	}
	claim, err := s.ClaimPrepareData(context.Background(), raw, sig)
	if err != nil {
		t.Fatal(err)
	}
	holder, err := s.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Rollback()
	before := s.db.Stats().WaitCount
	done := make(chan error, 1)
	go func() {
		_, err := s.RunPrepareDataOnce(context.Background(), claim, func(context.Context, DataReservation, control.PrepareDataPayload) error {
			t.Error("expired claim invoked work")
			return nil
		})
		done <- err
	}()
	deadline := time.Now().Add(3 * time.Second)
	for s.db.Stats().WaitCount == before && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if s.db.Stats().WaitCount == before {
		holder.Rollback()
		t.Fatal("invocation did not wait for journal")
	}
	expiry, _ := strconv.ParseInt(c.ExpiresMS, 10, 64)
	for time.Now().UnixMilli() < expiry {
		time.Sleep(time.Millisecond)
	}
	holder.Rollback()
	if err := <-done; !errors.Is(err, control.ErrInvalidCommand) {
		t.Fatal("expired invocation was accepted", err)
	}
	if scalar(t, s.db, `SELECT COUNT(*) FROM runner_commands WHERE state='unknown'`) != 1 {
		t.Fatal("expiry reset claim")
	}
}

func TestDataClaimCancellationAndNilCallbackDoNotConsumeHandle(t *testing.T) {
	s, _, raw, sig := reservedClaimFixture(t)
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if claim, err := s.ClaimPrepareData(canceled, raw, sig); err == nil || claim != (DataClaim{}) {
		t.Fatal("canceled claim persisted")
	}
	if scalar(t, s.db, `SELECT COUNT(*) FROM runner_commands WHERE state='prepared' AND receipt_json='{}'`) != 1 {
		t.Fatal("canceled claim changed state")
	}
	claim, err := s.ClaimPrepareData(context.Background(), raw, sig)
	if err != nil {
		t.Fatal(err)
	}
	callback := func(context.Context, DataReservation, control.PrepareDataPayload) error { return nil }
	if result, err := s.RunPrepareDataOnce(canceled, claim, callback); !errors.Is(err, context.Canceled) || result.Invoked {
		t.Fatal("canceled invocation consumed work", result, err)
	}
	if result, err := s.RunPrepareDataOnce(context.Background(), claim, nil); err == nil || result.Invoked {
		t.Fatal("nil callback invoked work")
	}
	if result, err := s.RunPrepareDataOnce(context.Background(), claim, callback); err != nil || !result.Invoked {
		t.Fatal("unused live handle lost", result, err)
	}
}

func TestDataClaimQueuedInvocationIsCancelableWithoutSQLLocks(t *testing.T) {
	s, _, raw, sig := reservedClaimFixture(t)
	first, err := s.ClaimPrepareData(context.Background(), raw, sig)
	if err != nil {
		t.Fatal(err)
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
		_, err := s.RunPrepareDataOnce(context.Background(), first, func(context.Context, DataReservation, control.PrepareDataPayload) error {
			close(entered)
			<-release
			return nil
		})
		done <- err
	}()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("first invocation did not start")
	}
	// Preparing another installation while the callback is blocked demonstrates
	// that no SQL transaction spans synchronous native work.
	c := runnerCommandFixture(t)
	c.InstallationID = fixtureID(800)
	c.OperationID = fixtureID(801)
	c.EffectID = fixtureID(802)
	c.Payload.DataNamespaceID = fixtureID(803)
	raw, sig = signRunnerCommand(t, c)
	setup, stopSetup := context.WithTimeout(context.Background(), 3*time.Second)
	defer stopSetup()
	if _, err := s.AdmitControlCommand(setup, raw, sig); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReservePrepareData(setup, raw, sig); err != nil {
		t.Fatal(err)
	}
	second, err := s.ClaimPrepareData(setup, raw, sig)
	if err != nil {
		t.Fatal(err)
	}
	queued, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	callback := func(context.Context, DataReservation, control.PrepareDataPayload) error { return nil }
	result, err := s.RunPrepareDataOnce(queued, second, callback)
	if !errors.Is(err, context.DeadlineExceeded) || result.Invoked {
		t.Fatal("queued invocation ignored cancellation", result, err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if result, err := s.RunPrepareDataOnce(context.Background(), second, callback); err != nil || !result.Invoked {
		t.Fatal("queued cancellation consumed unused handle", result, err)
	}
}

func TestDataClaimPanicCannotReplay(t *testing.T) {
	s, _, raw, sig := reservedClaimFixture(t)
	claim, err := s.ClaimPrepareData(context.Background(), raw, sig)
	if err != nil {
		t.Fatal(err)
	}
	caught := false
	func() {
		defer func() { caught = recover() != nil }()
		s.RunPrepareDataOnce(context.Background(), claim, func(context.Context, DataReservation, control.PrepareDataPayload) error {
			panic("fixture native panic")
		})
	}()
	if !caught {
		t.Fatal("fixture panic not observed")
	}
	if result, err := s.RunPrepareDataOnce(context.Background(), claim, func(context.Context, DataReservation, control.PrepareDataPayload) error {
		t.Fatal("panicked invocation replayed")
		return nil
	}); err == nil || result.Invoked {
		t.Fatal("panic reset consumption")
	}
	if scalar(t, s.db, `SELECT COUNT(*) FROM runner_commands WHERE state='unknown'`) != 1 {
		t.Fatal("panic changed durable uncertainty")
	}
}
