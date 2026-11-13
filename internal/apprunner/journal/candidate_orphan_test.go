//go:build linux || darwin

package journal

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/data"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/driver"
)

// orphanFixture is a claimed (unknown) start whose window ended, with the renewed signed envelope a later dispatch
// presents. Nothing about it is native: the claim is real journal history and the failed attempt is simply absent.
type orphanFixture struct {
	s          *Store
	raw, sig   []byte
	descriptor []byte
	resourceID string
	effectID   string
}

// orphanBudgets admits exactly one backend, so a held reservation is visible as a refusal.
func orphanBudgets() CapacityBudgets {
	b := roomyBudgets()
	b.RunningBackends = 1
	return b
}

func orphanDescriptor(t *testing.T) (appplatform.ParsedDescriptor, []byte) {
	t.Helper()
	descriptor, err := os.ReadFile("../../appplatform/testdata/descriptors/managed.json")
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := appplatform.ParseDescriptor(descriptor)
	if err != nil {
		t.Fatal(err)
	}
	return parsed, descriptor
}

// plainOrphanFixture claims a plain candidate start and then ends its window. With expired false the window is
// still open.
func plainOrphanFixture(t *testing.T, expired bool) orphanFixture {
	t.Helper()
	parsed, descriptor := orphanDescriptor(t)
	s, _ := enrolledCommandStore(t)
	t.Cleanup(func() { s.Close() })
	source := runnerCommandFixture(t)
	source.Payload.ReleaseSHA256 = parsed.SHA256
	sraw, ssig := signRunnerCommand(t, source)
	if _, err := s.AdmitControlCommand(context.Background(), sraw, ssig); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReservePrepareData(context.Background(), sraw, ssig); err != nil {
		t.Fatal(err)
	}
	dataClaim, err := s.ClaimPrepareData(context.Background(), sraw, ssig)
	if err != nil {
		t.Fatal(err)
	}
	var reservation DataReservation
	pin := data.RootIdentity{FilesystemUUID: fixtureID(900), Inode: 10}
	if _, err := s.RunPrepareDataOnce(context.Background(), dataClaim, func(ctx context.Context, r DataReservation, _ control.PrepareDataPayload) error {
		reservation = r
		return s.bindDataRoot(ctx, dataClaim.state, pin)
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.settlePreparedData(context.Background(), dataClaim.state, pin, syntheticDataObservation(reservation)); err != nil {
		t.Fatal(err)
	}
	c := journalCandidateFixture(t)
	c.EffectID = fixtureID(430)
	c.Payload.ReleaseSHA256 = parsed.SHA256
	raw, sig := signJournalCandidate(t, c)
	if _, err := s.AdmitCandidateCommand(context.Background(), raw, sig); err != nil {
		t.Fatal(err)
	}
	if err := s.SetCapacityBudgets(orphanBudgets()); err != nil {
		t.Fatal(err)
	}
	ctx := withBackendResources(context.Background(), parsed.Manifest.Backend.Resources)
	if _, err := s.ReserveCandidate(ctx, raw, sig); err != nil {
		t.Fatal(err)
	}
	claim, err := s.ClaimCandidate(ctx, raw, sig)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UnixMilli()
	var marker candidateClaimMarker
	if err := json.Unmarshal([]byte(claim.state.marker), &marker); err != nil {
		t.Fatal(err)
	}
	if expired {
		// The native attempt failed and the window has ended; a later envelope is issued after it.
		marker.ClaimedMS, marker.ExpiresMS = strconv.FormatInt(now-3000, 10), strconv.FormatInt(now-1000, 10)
		encoded, _ := json.Marshal(marker)
		if _, err := s.db.Exec(`UPDATE runner_commands SET receipt_json=? WHERE effect_id=?`, string(encoded), c.EffectID); err != nil {
			t.Fatal(err)
		}
	}
	c.IssuedMS, c.ExpiresMS = strconv.FormatInt(now-500, 10), strconv.FormatInt(now+25000, 10)
	renewed, renewedSig := signJournalCandidate(t, c)
	return orphanFixture{s: s, raw: renewed, sig: renewedSig, descriptor: descriptor, resourceID: c.Payload.ResourceID, effectID: c.EffectID}
}

// writableOrphanFixture claims a writable candidate start that never reached its preflight binding and ends its window.
func writableOrphanFixture(t *testing.T, expired bool) orphanFixture {
	t.Helper()
	s, wraw, wsig, descriptor := syntheticWriterExecutionFixture(t)
	t.Cleanup(func() { s.Close() })
	parsed, err := appplatform.ParseDescriptor(descriptor)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AdmitDataWriterCommand(context.Background(), wraw, wsig); err != nil {
		t.Fatal(err)
	}
	receipt := syntheticSettleWriterReceipt(t, s, wraw, wsig)
	raw, sig := signWritableIntent(t, writableIntentFixture(t, wraw, wsig, receipt))
	if _, err := s.AdmitWritableCandidateCommand(context.Background(), raw, sig); err != nil {
		t.Fatal(err)
	}
	if err := s.SetCapacityBudgets(orphanBudgets()); err != nil {
		t.Fatal(err)
	}
	ctx := withBackendResources(context.Background(), parsed.Manifest.Backend.Resources)
	reserved, err := s.ReserveWritableCandidate(ctx, raw, sig)
	if err != nil {
		t.Fatal(err)
	}
	claim, err := s.ClaimWritableCandidate(ctx, raw, sig)
	if err != nil {
		t.Fatal(err)
	}
	var marker writableCandidateClaimMarker
	if err := json.Unmarshal([]byte(claim.state.marker), &marker); err != nil {
		t.Fatal(err)
	}
	claimed, _ := strconv.ParseInt(marker.ClaimedMS, 10, 64)
	if expired {
		marker.ExpiresMS = strconv.FormatInt(claimed+1, 10)
		encoded, _ := json.Marshal(marker)
		if _, err := s.db.Exec(`UPDATE runner_commands SET receipt_json=? WHERE stage='start'`, string(encoded)); err != nil {
			t.Fatal(err)
		}
		if delay := time.Until(time.UnixMilli(claimed + 3)); delay > 0 {
			time.Sleep(delay)
		}
		raw, sig = renewWritableRecoveryFixture(t, raw)
	}
	return orphanFixture{s: s, raw: raw, sig: sig, descriptor: descriptor, resourceID: reserved.ResourceID, effectID: marker.EffectID}
}

// orphanState reads what a settlement may change.
type orphanState struct {
	command, reservation, desired, observed string
	revision                                int64
	reservationUpdated                      int64
}

func (f orphanFixture) state(t *testing.T) orphanState {
	t.Helper()
	var st orphanState
	if err := f.s.db.QueryRow(`SELECT state FROM runner_commands WHERE effect_id=?`, f.effectID).Scan(&st.command); err != nil {
		t.Fatal(err)
	}
	if err := f.s.db.QueryRow(`SELECT state,updated_ms FROM runner_reservations WHERE resource_id=?`, f.resourceID).Scan(&st.reservation, &st.reservationUpdated); err != nil {
		t.Fatal(err)
	}
	if err := f.s.db.QueryRow(`SELECT desired_state,observed_state,revision FROM runner_resources WHERE id=?`, f.resourceID).Scan(&st.desired, &st.observed, &st.revision); err != nil {
		t.Fatal(err)
	}
	return st
}

func requireUnchangedUnknown(t *testing.T, f orphanFixture, before orphanState, why string) {
	t.Helper()
	if after := f.state(t); after != before || after.command != "unknown" || after.reservation != "held" || after.desired != "present" || after.observed != "unknown" || after.revision != 1 {
		t.Fatalf("%s: state changed from %+v to %+v", why, before, after)
	}
}

// kinds drives the same behaviour through both entry points the transport reaches.
type orphanKind struct {
	name     string
	fixture  func(*testing.T, bool) orphanFixture
	dispatch func(f orphanFixture, ctx context.Context, i CandidateTargetInspector) error
}

func orphanKinds() []orphanKind {
	return []orphanKind{
		{"candidate", plainOrphanFixture, func(f orphanFixture, ctx context.Context, i CandidateTargetInspector) error {
			_, err := f.s.dispatchCandidate(ctx, f.raw, f.sig, nil, i, f.descriptor)
			return err
		}},
		{"writable_candidate", writableOrphanFixture, func(f orphanFixture, ctx context.Context, i CandidateTargetInspector) error {
			_, err := f.s.dispatchWritableCandidate(ctx, f.raw, f.sig, f.descriptor, nil, i)
			return err
		}},
	}
}

func TestCandidateOrphanAbsentIsSettledRevertedAndReleasedOnce(t *testing.T) {
	for _, k := range orphanKinds() {
		t.Run(k.name, func(t *testing.T) {
			f := k.fixture(t, true)
			if refusal := admitPlan(t, f.s, backendPlan(1, 16, 25)); refusal == nil || refusal.dimension != CapacityRunningBackends {
				t.Fatalf("the unknown start does not hold its backend: %+v", refusal)
			}
			if st := f.state(t); st.command != "unknown" || st.reservation != "held" {
				t.Fatalf("fixture state = %+v", st)
			}
			inspector := &fakeInspector{}
			err := k.dispatch(f, context.Background(), inspector)
			var settled *CandidateOrphanSettledError
			if !errors.As(err, &settled) || !errors.Is(err, ErrCandidateOrphanSettled) || errors.Is(err, ErrFenceReconciliation) {
				t.Fatalf("err = %v, want a typed orphan settlement distinct from ErrFenceReconciliation", err)
			}
			if inspector.calls != 2 {
				t.Fatalf("inspected %d times", inspector.calls)
			}
			st := f.state(t)
			if st.command != "reverted" || st.desired != "absent" || st.observed != "absent" || st.revision != 4 || st.reservation != "released" {
				t.Fatalf("state = %+v", st)
			}
			var raw string
			if err := f.s.db.QueryRow(`SELECT receipt_json FROM runner_commands WHERE effect_id=?`, f.effectID).Scan(&raw); err != nil {
				t.Fatal(err)
			}
			receipt, err := ParseCandidateOrphanReceipt([]byte(raw))
			if err != nil || receipt != settled.Receipt || receipt.Outcome != "candidate_never_ran" || receipt.ResourceID != f.resourceID || receipt.EffectID != f.effectID {
				t.Fatalf("stored receipt = %+v, %v; answered %+v", receipt, err, settled.Receipt)
			}
			var observedMS, settledMS int64
			if err := f.s.db.QueryRow(`SELECT observed_ms FROM runner_resources WHERE id=?`, f.resourceID).Scan(&observedMS); err != nil {
				t.Fatal(err)
			}
			settledMS, _ = strconv.ParseInt(receipt.SettledMS, 10, 64)
			if observedMS != settledMS {
				t.Fatalf("observed_ms %d != settled %d", observedMS, settledMS)
			}
			// The reservation is released exactly once: a replay changes no row and looks at nothing.
			again := k.dispatch(f, context.Background(), inspector)
			var replay *CandidateOrphanSettledError
			if !errors.As(again, &replay) || replay.Receipt != settled.Receipt {
				t.Fatalf("replay = %v, want the same receipt", again)
			}
			if inspector.calls != 2 || f.state(t) != st {
				t.Fatalf("the replay inspected again or changed a row: calls=%d", inspector.calls)
			}
			// The freed envelope admits a backend that was refused for lack of budget.
			if refusal := admitPlan(t, f.s, backendPlan(1, 16, 25)); refusal != nil {
				t.Fatalf("a new backend is still refused after the settlement: %+v", refusal)
			}
		})
	}
}

func TestCandidateOrphanReadBackAnswersTheSameReceipt(t *testing.T) {
	f := plainOrphanFixture(t, true)
	err := orphanKinds()[0].dispatch(f, context.Background(), &fakeInspector{})
	var settled *CandidateOrphanSettledError
	if !errors.As(err, &settled) {
		t.Fatalf("err = %v", err)
	}
	_, err = f.s.ReadCandidateRunningReceipt(context.Background(), f.raw, f.sig)
	var read *CandidateOrphanSettledError
	if !errors.As(err, &read) || read.Receipt != settled.Receipt {
		t.Fatalf("read-back = %v", err)
	}
	bad := append([]byte(nil), f.sig...)
	bad[0] ^= 1
	if _, err := f.s.ReadCandidateRunningReceipt(context.Background(), f.raw, bad); errors.As(err, &read) && err == nil {
		t.Fatal("an unverified signature got the receipt")
	}
}

func TestCandidateOrphanPresentContainerKeepsTheEffectUnknown(t *testing.T) {
	for _, k := range orphanKinds() {
		for _, state := range []string{"created", "running", "exited"} {
			t.Run(k.name+"/"+state, func(t *testing.T) {
				f := k.fixture(t, true)
				before := f.state(t)
				inspector := &fakeInspector{inspection: driver.TargetInspection{Found: true, NativeID: strings.Repeat("c", 64), State: state}}
				err := k.dispatch(f, context.Background(), inspector)
				if !errors.Is(err, ErrCandidateOrphanPresent) || !errors.Is(err, ErrFenceReconciliation) || errors.Is(err, ErrCandidateOrphanSettled) {
					t.Fatalf("err = %v", err)
				}
				requireUnchangedUnknown(t, f, before, "present container")
				if refusal := admitPlan(t, f.s, backendPlan(1, 16, 25)); refusal == nil {
					t.Fatal("the reservation of a present container was released")
				}
			})
		}
	}
}

func TestCandidateOrphanFailedInspectionChangesNothing(t *testing.T) {
	for _, k := range orphanKinds() {
		t.Run(k.name, func(t *testing.T) {
			f := k.fixture(t, true)
			before := f.state(t)
			err := k.dispatch(f, context.Background(), &fakeInspector{err: errors.New("daemon unreachable")})
			if err == nil || errors.Is(err, ErrCandidateOrphanSettled) || !errors.Is(err, ErrFenceReconciliation) {
				t.Fatalf("err = %v", err)
			}
			requireUnchangedUnknown(t, f, before, "failed inspection")
		})
	}
}

func TestCandidateOrphanHeldGateChangesNothing(t *testing.T) {
	for _, k := range orphanKinds() {
		t.Run(k.name, func(t *testing.T) {
			f := k.fixture(t, true)
			before := f.state(t)
			if !f.s.tryAcquireDataExecution() {
				t.Fatal("the gate was not free")
			}
			inspector := &fakeInspector{}
			err := k.dispatch(f, context.Background(), inspector)
			f.s.releaseDataExecution()
			if !errors.Is(err, ErrCandidateOrphanBusy) || !errors.Is(err, ErrFenceReconciliation) {
				t.Fatalf("err = %v", err)
			}
			if inspector.calls != 0 {
				t.Fatal("inspected while a start may be executing")
			}
			requireUnchangedUnknown(t, f, before, "held gate")
		})
	}
}

func TestCandidateOrphanOpenWindowChangesNothing(t *testing.T) {
	for _, k := range orphanKinds() {
		t.Run(k.name, func(t *testing.T) {
			f := k.fixture(t, false)
			before := f.state(t)
			inspector := &fakeInspector{}
			err := k.dispatch(f, context.Background(), inspector)
			if !errors.Is(err, ErrFenceReconciliation) || errors.Is(err, ErrCandidateOrphanSettled) {
				t.Fatalf("err = %v", err)
			}
			if inspector.calls != 0 {
				t.Fatal("inspected a start that is still inside its window")
			}
			requireUnchangedUnknown(t, f, before, "open window")
		})
	}
}

func TestCandidateOrphanWithoutInspectorStaysUnknown(t *testing.T) {
	for _, k := range orphanKinds() {
		t.Run(k.name, func(t *testing.T) {
			f := k.fixture(t, true)
			before := f.state(t)
			if err := k.dispatch(f, context.Background(), nil); !errors.Is(err, ErrFenceReconciliation) {
				t.Fatalf("err = %v", err)
			}
			requireUnchangedUnknown(t, f, before, "no inspector")
		})
	}
}

// A resource or command that moved between the inspection and the write is not settled: every update compares the
// exact prior contents.
func TestCandidateOrphanChangedBetweenInspectionAndWriteSettlesNothing(t *testing.T) {
	for _, k := range orphanKinds() {
		t.Run(k.name, func(t *testing.T) {
			f := k.fixture(t, true)
			before := f.state(t)
			inspector := &fakeInspector{onInspect: func() {
				if _, err := f.s.db.Exec(`UPDATE runner_resources SET writer_fence=writer_fence+1 WHERE id=?`, f.resourceID); err != nil {
					t.Error(err)
				}
			}}
			err := k.dispatch(f, context.Background(), inspector)
			if err == nil || errors.Is(err, ErrCandidateOrphanSettled) {
				t.Fatalf("err = %v", err)
			}
			if after := f.state(t); after.command != "unknown" || after.reservation != "held" || after.desired != before.desired || after.revision != 1 {
				t.Fatalf("a moved resource was settled: %+v", after)
			}
		})
	}
}

// A settlement whose last write fails leaves the command and the resource untouched: the writes are one
// transaction.
func TestCandidateOrphanIsOneTransaction(t *testing.T) {
	for _, k := range orphanKinds() {
		t.Run(k.name, func(t *testing.T) {
			f := k.fixture(t, true)
			before := f.state(t)
			if _, err := f.s.db.Exec(`CREATE TRIGGER fail_release BEFORE UPDATE ON runner_reservations BEGIN SELECT RAISE(ABORT, 'release refused'); END`); err != nil {
				t.Fatal(err)
			}
			err := k.dispatch(f, context.Background(), &fakeInspector{})
			if err == nil || errors.Is(err, ErrCandidateOrphanSettled) {
				t.Fatalf("err = %v", err)
			}
			requireUnchangedUnknown(t, f, before, "failed release")
		})
	}
}

func TestCandidateOrphanReceiptCodec(t *testing.T) {
	f := plainOrphanFixture(t, true)
	err := orphanKinds()[0].dispatch(f, context.Background(), &fakeInspector{})
	var settled *CandidateOrphanSettledError
	if !errors.As(err, &settled) {
		t.Fatal(err)
	}
	good, _ := json.Marshal(settled.Receipt)
	if _, err := ParseCandidateOrphanReceipt(good); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*CandidateOrphanReceipt){
		"format":  func(r *CandidateOrphanReceipt) { r.Format = "x" },
		"outcome": func(r *CandidateOrphanReceipt) { r.Outcome = CapacityExhaustedOutcome },
		"kind":    func(r *CandidateOrphanReceipt) { r.Kind = "update-start" },
		"payload": func(r *CandidateOrphanReceipt) { r.PayloadSHA256 = "x" },
		"settled": func(r *CandidateOrphanReceipt) { r.SettledMS = "-1" },
		"effect":  func(r *CandidateOrphanReceipt) { r.EffectID = "x" },
	} {
		r := settled.Receipt
		mutate(&r)
		encoded, _ := json.Marshal(r)
		if _, err := ParseCandidateOrphanReceipt(encoded); err == nil {
			t.Fatalf("%s: a mutated receipt parsed", name)
		}
	}
	if _, err := ParseCandidateOrphanReceipt(append(append([]byte(nil), good...), ' ')); err == nil {
		t.Fatal("a non canonical encoding parsed")
	}
	// It is not a capacity refusal, and a refusal is not an orphan receipt.
	if _, err := ParseCapacityRefusal(good); err == nil {
		t.Fatal("an orphan receipt parsed as a capacity refusal")
	}
	if _, ok := storedCandidateOrphan(string(good), CapacityIdentity{EffectID: fixtureID(1)}); ok {
		t.Fatal("matched another command")
	}
}

// The writable reconcile action reaches the settlement for a start that never reached its preflight binding, so it
// cannot be observed as a running process.
func TestWritableReconcileSettlesAProvenAbsentStart(t *testing.T) {
	f := writableOrphanFixture(t, true)
	reconcile := func(i CandidateTargetInspector) error {
		_, err := f.s.reconcileWritableCandidate(context.Background(), f.raw, f.sig, f.descriptor, &driver.CandidateRuntime{}, i)
		return err
	}
	// A container in any state: the original refusal stands and nothing changes.
	before := f.state(t)
	present := &fakeInspector{inspection: driver.TargetInspection{Found: true, NativeID: strings.Repeat("c", 64), State: "running"}}
	if err := reconcile(present); !errors.Is(err, ErrFenceReconciliation) || errors.Is(err, ErrCandidateOrphanSettled) {
		t.Fatalf("present: err = %v", err)
	}
	requireUnchangedUnknown(t, f, before, "present container")
	if err := reconcile(&fakeInspector{err: errors.New("daemon unreachable")}); !errors.Is(err, ErrFenceReconciliation) || errors.Is(err, ErrCandidateOrphanSettled) {
		t.Fatalf("failed inspection: err = %v", err)
	}
	requireUnchangedUnknown(t, f, before, "failed inspection")
	// Proven absent: settled, and answered with the typed outcome, again on a replay.
	absent := &fakeInspector{}
	err := reconcile(absent)
	var settled *CandidateOrphanSettledError
	if !errors.As(err, &settled) || errors.Is(err, ErrFenceReconciliation) {
		t.Fatalf("absent: err = %v", err)
	}
	if st := f.state(t); st.command != "reverted" || st.desired != "absent" || st.observed != "absent" || st.reservation != "released" {
		t.Fatalf("state = %+v", st)
	}
	// A replay of the reconcile answers with the same receipt and changes nothing.
	stSettled := f.state(t)
	replay := reconcile(absent)
	var again *CandidateOrphanSettledError
	if !errors.As(replay, &again) || again.Receipt != settled.Receipt || f.state(t) != stSettled || absent.calls != 2 {
		t.Fatalf("replay = %v calls=%d", replay, absent.calls)
	}
}

func TestWritableReconcileOpenWindowChangesNothing(t *testing.T) {
	f := writableOrphanFixture(t, false)
	before := f.state(t)
	inspector := &fakeInspector{}
	if _, err := f.s.reconcileWritableCandidate(context.Background(), f.raw, f.sig, f.descriptor, &driver.CandidateRuntime{}, inspector); err == nil {
		t.Fatal("reconciled a start inside its window")
	}
	if inspector.calls != 0 {
		t.Fatal("inspected a start that is still inside its window")
	}
	requireUnchangedUnknown(t, f, before, "open window")
}
