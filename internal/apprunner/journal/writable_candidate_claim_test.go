//go:build linux || darwin

package journal

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
)

func writableReservationFixture(t *testing.T) (*Store, []byte, []byte, WritableCandidateReservation) {
	t.Helper()
	s, wraw, wsig, receipt := syntheticAppliedWriterFixture(t)
	raw, sig := signWritableIntent(t, writableIntentFixture(t, wraw, wsig, receipt))
	if _, err := s.AdmitWritableCandidateCommand(context.Background(), raw, sig); err != nil {
		t.Fatal(err)
	}
	r, err := s.ReserveWritableCandidate(context.Background(), raw, sig)
	if err != nil {
		t.Fatal(err)
	}
	return s, raw, sig, r
}
func writableClaimFixture(t *testing.T) (*Store, []byte, []byte, WritableCandidateClaim) {
	t.Helper()
	s, raw, sig, _ := writableReservationFixture(t)
	claim, err := s.ClaimWritableCandidate(context.Background(), raw, sig)
	if err != nil {
		t.Fatal(err)
	}
	return s, raw, sig, claim
}

func TestWritableCandidateReservationAndOneShotAreNotNativeSuccess(t *testing.T) {
	s, raw, sig, r := writableReservationFixture(t)
	defer s.Close()
	if r.Replayed || !dataSHA(r.OwnershipSHA256) || r.ResourceID == "" {
		t.Fatal("initial reservation", r)
	}
	replay, err := s.ReserveWritableCandidate(context.Background(), raw, sig)
	expected := r
	expected.Replayed = true
	if err != nil || replay != expected {
		t.Fatal("reservation not idempotent", err)
	}
	if view, err := s.ReadWritableCandidateReservation(context.Background(), raw, sig); err != nil || view != expected {
		t.Fatal("current reservation", err)
	}
	if _, err := s.ReadWritableCandidateIntent(context.Background(), raw, sig); err == nil {
		t.Fatal("no-resource reader adopted later reservation")
	}
	if _, err := s.ReserveCandidate(context.Background(), raw, sig); err == nil {
		t.Fatal("readonly reserve accepted writable authority")
	}
	claim, err := s.ClaimWritableCandidate(context.Background(), raw, sig)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := json.Marshal(claim); err == nil {
		t.Fatal("claim serialized")
	}
	decoded := claim
	if err := json.Unmarshal([]byte(`{}`), &decoded); err == nil || decoded.state != nil {
		t.Fatal("decoded executable claim")
	}
	attempt, err := s.RunWritableCandidateOnce(context.Background(), claim, func(ctx context.Context, p WritableCandidatePlan) error {
		if ctx.Err() != nil || p.Reservation != expected || p.Candidate.Payload.ResourceID != r.ResourceID || p.Writer.Claim.ResourceID != r.ResourceID || p.Candidate.Payload.SandboxProfile != control.WritableCandidateSandboxProfile {
			t.Fatal("bound plan changed")
		}
		encoded, _ := json.Marshal(p.Writer)
		if candidateHash(encoded) != r.WriterReceiptSHA256 {
			t.Fatal("plan lost exact writer receipt")
		}
		p.Command.WriterRaw[0] ^= 1
		p.Command.WriterSignature[0] ^= 1
		p.Candidate.Payload.ResourceID = "changed local copy"
		return nil
	})
	if err != nil || !attempt.Invoked {
		t.Fatal("one-shot invocation", attempt, err)
	}
	if scalar(t, s.db, `SELECT COUNT(*) FROM runner_commands WHERE stage='start' AND state='unknown' AND ack_attempted_ms IS NULL`) != 1 || scalar(t, s.db, `SELECT COUNT(*) FROM runner_resources WHERE kind='container' AND revision=1 AND observed_state='unknown' AND observed_ms IS NULL`) != 1 {
		t.Fatal("callback invented process/ready receipt")
	}
	if _, err := s.ClaimWritableCandidate(context.Background(), raw, sig); err == nil {
		t.Fatal("unknown work reclaimed")
	}
	if _, err := s.ReadWritableCandidateReservation(context.Background(), raw, sig); err == nil {
		t.Fatal("unknown work reported prepared")
	}
	if again, err := s.RunWritableCandidateOnce(context.Background(), claim, func(context.Context, WritableCandidatePlan) error { return nil }); err == nil || again.Invoked {
		t.Fatal("one-shot callback repeated")
	}
}

func TestWritableCandidateReservationConcurrentCopiesAndReopen(t *testing.T) {
	s, raw, sig, r := writableReservationFixture(t)
	directory := writerStoreDirectory(t, s)
	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, err := s.ReserveWritableCandidate(context.Background(), raw, sig)
			if err != nil || got.ResourceID != r.ResourceID || !got.Replayed {
				t.Error("concurrent reservation", err)
			}
		}()
	}
	wg.Wait()
	claim, err := s.ClaimWritableCandidate(context.Background(), raw, sig)
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int64
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.RunWritableCandidateOnce(context.Background(), claim, func(context.Context, WritableCandidatePlan) error { calls.Add(1); return nil })
		}()
	}
	wg.Wait()
	if calls.Load() != 1 || scalar(t, s.db, `SELECT COUNT(*) FROM runner_resources WHERE kind='container'`) != 1 {
		t.Fatal("duplicate resources or invocation", calls.Load())
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if result, err := reopened.RunWritableCandidateOnce(context.Background(), claim, func(context.Context, WritableCandidatePlan) error { calls.Add(1); return nil }); err == nil || result.Invoked {
		t.Fatal("owner reopen resurrected handle")
	}
	if _, err := reopened.ClaimWritableCandidate(context.Background(), raw, sig); err == nil {
		t.Fatal("restart reclaimed unknown")
	}
	if calls.Load() != 1 {
		t.Fatal("restart ran callback")
	}
}

func TestWritableCandidateClaimRefusesChangedEvidenceBeforeAndAfterIO(t *testing.T) {
	for _, phase := range []string{"before", "during"} {
		for _, mode := range []string{"authority", "ack", "marker", "ownership", "native", "resource_time", "writer_ack", "data_time", "writer_fence", "other_resource"} {
			t.Run(phase+"/"+mode, func(t *testing.T) {
				s, _, _, claim := writableClaimFixture(t)
				defer s.Close()
				queries := map[string]string{
					"authority":      `UPDATE runner_deployment SET control_revision=control_revision+1`,
					"ack":            `UPDATE runner_commands SET ack_attempted_ms=updated_ms WHERE stage='start'`,
					"marker":         `UPDATE runner_commands SET receipt_json='{}' WHERE stage='start'`,
					"ownership":      `UPDATE runner_resources SET ownership_sha256='` + strings.Repeat("f", 64) + `' WHERE kind='container'`,
					"native":         `UPDATE runner_resources SET native_id='candidate-pending/v1/wrong' WHERE kind='container'`,
					"resource_time":  `UPDATE runner_resources SET created_ms=0,updated_ms=0 WHERE kind='container'`,
					"writer_ack":     `UPDATE runner_commands SET ack_attempted_ms=updated_ms WHERE kind='activate'`,
					"data_time":      `UPDATE runner_resources SET observed_ms=observed_ms+1 WHERE kind='volume'`,
					"writer_fence":   `UPDATE runner_resources SET writer_fence=writer_fence+1 WHERE kind='volume'`,
					"other_resource": `INSERT INTO runner_resources SELECT '` + fixtureID(1701) + `',installation_id,data_namespace_id,'socket','extra-socket',release_sha256,generation,controller_epoch,control_fence,writer_fence,'present','unknown',ownership_sha256,1,NULL,created_ms,created_ms FROM runner_resources WHERE kind='container'`,
				}
				mutate := func() {
					if _, err := s.db.Exec(queries[mode]); err != nil {
						t.Fatal(err)
					}
				}
				if phase == "before" {
					mutate()
				}
				called := false
				attempt, err := s.RunWritableCandidateOnce(context.Background(), claim, func(context.Context, WritableCandidatePlan) error { called = true; mutate(); return nil })
				if err == nil || attempt.Invoked != (phase == "during") || called != (phase == "during") {
					t.Fatal("evidence drift accepted", attempt, err)
				}
				if phase == "during" && !errors.Is(err, ErrWritableCandidateUncertain) {
					t.Fatal("invoked drift lost uncertainty", err)
				}
			})
		}
	}
}

func TestWritableCandidateClaimCancellationAndOwnerIsolation(t *testing.T) {
	s, raw, sig, claim := writableClaimFixture(t)
	defer s.Close()
	other, _, _, _ := writableReservationFixture(t)
	defer other.Close()
	if attempt, err := other.RunWritableCandidateOnce(context.Background(), claim, func(context.Context, WritableCandidatePlan) error { t.Fatal("wrong owner invoked"); return nil }); err == nil || attempt.Invoked {
		t.Fatal("cross-store handle accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if attempt, err := s.RunWritableCandidateOnce(ctx, claim, func(context.Context, WritableCandidatePlan) error { t.Fatal("cancelled callback"); return nil }); err == nil || attempt.Invoked {
		t.Fatal("pre-cancel invoked")
	}
	ctx, cancel = context.WithCancel(context.Background())
	attempt, err := s.RunWritableCandidateOnce(ctx, claim, func(context.Context, WritableCandidatePlan) error { cancel(); return nil })
	if !attempt.Invoked || !errors.Is(err, context.Canceled) || !errors.Is(err, ErrWritableCandidateUncertain) {
		t.Fatal("invoked cancellation", attempt, err)
	}
	if _, err := s.ClaimWritableCandidate(context.Background(), raw, sig); err == nil {
		t.Fatal("cancelled effect reclaimed")
	}
}

func shortWritableClaimFixture(t *testing.T) (*Store, WritableCandidateClaim) {
	t.Helper()
	s, wraw, _, receipt := syntheticAppliedWriterFixture(t)
	var w control.DataWriterCommand
	json.Unmarshal(wraw, &w)
	var ref control.Command
	json.Unmarshal(w.DataRaw, &ref)
	ref.ExpiresMS = strconv.FormatInt(time.Now().UnixMilli()+1200, 10)
	w.DataRaw, w.DataSignature = signRunnerCommand(t, ref)
	wraw, wsig := signWriterFixture(t, w)
	raw, sig := signWritableIntent(t, writableIntentFixture(t, wraw, wsig, receipt))
	if _, err := s.AdmitWritableCandidateCommand(context.Background(), raw, sig); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReserveWritableCandidate(context.Background(), raw, sig); err != nil {
		t.Fatal(err)
	}
	claim, err := s.ClaimWritableCandidate(context.Background(), raw, sig)
	if err != nil {
		t.Fatal(err)
	}
	return s, claim
}
func TestWritableCandidateClaimSignedDeadline(t *testing.T) {
	t.Run("before", func(t *testing.T) {
		s, claim := shortWritableClaimFixture(t)
		defer s.Close()
		time.Sleep(time.Until(claim.state.deadline.Add(time.Millisecond)))
		if attempt, err := s.RunWritableCandidateOnce(context.Background(), claim, func(context.Context, WritableCandidatePlan) error { t.Fatal("expired callback"); return nil }); err == nil || attempt.Invoked {
			t.Fatal("expired handle invoked")
		}
	})
	t.Run("during", func(t *testing.T) {
		s, claim := shortWritableClaimFixture(t)
		defer s.Close()
		attempt, err := s.RunWritableCandidateOnce(context.Background(), claim, func(ctx context.Context, _ WritableCandidatePlan) error { <-ctx.Done(); return ctx.Err() })
		if !attempt.Invoked || !errors.Is(err, context.DeadlineExceeded) || !errors.Is(err, ErrWritableCandidateUncertain) {
			t.Fatal("deadline not enforced", attempt, err)
		}
	})
}
func TestWritableCandidateCloseWaitsForLiveInvocation(t *testing.T) {
	s, _, _, claim := writableClaimFixture(t)
	directory := writerStoreDirectory(t, s)
	entered, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	finished := make(chan error, 1)
	go func() {
		_, err := s.RunWritableCandidateOnce(context.Background(), claim, func(context.Context, WritableCandidatePlan) error { close(entered); <-release; return nil })
		finished <- err
	}()
	<-entered
	closed := make(chan error, 1)
	go func() { closed <- s.Close() }()
	select {
	case err := <-closed:
		t.Fatal("Close released live owner", err)
	case <-time.After(40 * time.Millisecond):
	}
	if other, err := Open(directory); err == nil {
		other.Close()
		t.Fatal("journal owner released before invocation finished")
	}
	releaseOnce.Do(func() { close(release) })
	if err := <-finished; err == nil {
		t.Fatal("closing invocation reported success")
	}
	if err := <-closed; err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if scalar(t, reopened.db, `SELECT COUNT(*) FROM runner_commands WHERE stage='start' AND state='unknown'`) != 1 {
		t.Fatal("Close erased uncertainty")
	}
}

func TestWritableCandidatePreparedReservationSurvivesOwnerReopen(t *testing.T) {
	s, raw, sig, r := writableReservationFixture(t)
	directory := writerStoreDirectory(t, s)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	r.Replayed = true
	if got, err := reopened.ReadWritableCandidateReservation(context.Background(), raw, sig); err != nil || got != r {
		t.Fatal("reopened prepared reservation changed", err)
	}
	if _, err := reopened.ClaimWritableCandidate(context.Background(), raw, sig); err != nil {
		t.Fatal("unclaimed prepared reservation lost", err)
	}
}

func TestWritableCandidateReservationCurrentKeyKeepsOwnership(t *testing.T) {
	s, raw, sig, reserved := writableReservationFixture(t)
	defer s.Close()
	var c control.WritableCandidateCommand
	json.Unmarshal(raw, &c)
	var w control.DataWriterCommand
	json.Unmarshal(c.WriterRaw, &w)
	var ref control.Command
	json.Unmarshal(w.DataRaw, &ref)
	e := enrollmentFixture(31)
	e.ControlRevision = 4
	if _, err := s.db.Exec(`UPDATE runner_deployment SET control_revision=4,verification_key=?,key_fingerprint=?,revision=revision+1`, e.PublicKey, e.Fingerprint); err != nil {
		t.Fatal(err)
	}
	key := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{31}, 32))
	a := control.Authority{DeploymentID: e.DeploymentID, ControllerEpoch: e.ControllerEpoch, ControlRevision: 4, PublicKey: e.PublicKey, Fingerprint: e.Fingerprint}
	ref.ControlRevision = "4"
	now := time.Now().UnixMilli()
	ref.IssuedMS, ref.ExpiresMS = strconv.FormatInt(now, 10), strconv.FormatInt(now+25000, 10)
	signCurrent := func() ([]byte, []byte) {
		t.Helper()
		var err error
		w.DataRaw, w.DataSignature, err = control.Sign(ref, key, a, time.Now().UnixMilli())
		if err != nil {
			t.Fatal(err)
		}
		c.WriterRaw, c.WriterSignature, err = control.SignDataWriter(w, key, a, time.Now().UnixMilli())
		if err != nil {
			t.Fatal(err)
		}
		return signWritableIntentAuthority(t, c, key, a)
	}
	if _, err := s.ReadWritableCandidateReservation(context.Background(), raw, sig); err == nil {
		t.Fatal("old key remained current")
	}
	raw, sig = signCurrent()
	reserved.Replayed = true
	if got, err := s.ReadWritableCandidateReservation(context.Background(), raw, sig); err != nil || got != reserved {
		t.Fatal("current key rewrote ownership", err)
	}
	if got, err := s.ReserveWritableCandidate(context.Background(), raw, sig); err != nil || got != reserved {
		t.Fatal("current key duplicated reservation", err)
	}
	ref.InstallationFence = "2"
	wrong, wrongSig := signCurrent()
	if _, err := s.ReadWritableCandidateReservation(context.Background(), wrong, wrongSig); err == nil {
		t.Fatal("new fence silently adopted old reservation")
	}
	ref.InstallationFence = "1"
	raw, sig = signCurrent()
	claim, err := s.ClaimWritableCandidate(context.Background(), raw, sig)
	if err != nil {
		t.Fatal(err)
	}
	if attempt, err := s.RunWritableCandidateOnce(context.Background(), claim, func(_ context.Context, p WritableCandidatePlan) error {
		if p.Candidate.ControlRevision != "4" || p.Writer.ControlRevision != "3" || p.Reservation.OwnershipSHA256 != reserved.OwnershipSHA256 {
			t.Fatal("renewed authority changed original evidence")
		}
		return nil
	}); err != nil || !attempt.Invoked {
		t.Fatal("renewed prepared ownership unavailable", err)
	}
}

func TestWritableCandidateConcurrentInitialReservationAndClaim(t *testing.T) {
	s, wraw, wsig, receipt := syntheticAppliedWriterFixture(t)
	defer s.Close()
	raw, sig := signWritableIntent(t, writableIntentFixture(t, wraw, wsig, receipt))
	if _, err := s.AdmitWritableCandidateCommand(context.Background(), raw, sig); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	var creators atomic.Int64
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, err := s.ReserveWritableCandidate(context.Background(), raw, sig)
			if err != nil {
				t.Error(err)
				return
			}
			if !r.Replayed {
				creators.Add(1)
			}
		}()
	}
	wg.Wait()
	if creators.Load() != 1 || scalar(t, s.db, `SELECT COUNT(*) FROM runner_resources WHERE kind='container'`) != 1 {
		t.Fatal("initial resource duplicated", creators.Load())
	}
	var winners atomic.Int64
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := s.ClaimWritableCandidate(context.Background(), raw, sig); err == nil {
				winners.Add(1)
			}
		}()
	}
	wg.Wait()
	if winners.Load() != 1 {
		t.Fatal("multiple live claim handles issued", winners.Load())
	}
}
