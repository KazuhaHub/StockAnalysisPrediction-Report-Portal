//go:build linux || darwin

package journal

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/driver"
)

// The daemon can finish a container create after the client gave up on it, so one absence observation proves
// nothing. Tests do not wait out the real delay.
func init() { absenceSettleDelay = 0 }

// sequenceInspector answers each inspection from a script, the last answer repeating.
type sequenceInspector struct {
	answers []driver.TargetInspection
	calls   int
}

func (s *sequenceInspector) InspectTarget(context.Context, string) (driver.TargetInspection, error) {
	i := s.calls
	if i >= len(s.answers) {
		i = len(s.answers) - 1
	}
	s.calls++
	return s.answers[i], nil
}

func (s *sequenceInspector) ListNamespaceContainers(context.Context, string) ([]driver.NamespaceContainer, error) {
	return nil, nil
}

func (s *sequenceInspector) RemoveStrayContainer(context.Context, string) error { return nil }

func lateContainer() driver.TargetInspection {
	return driver.TargetInspection{Found: true, NativeID: strings.Repeat("d", 64), State: "created"}
}

// Absent twice settles; present on the second look keeps the start unknown and its reservation held.
func TestCandidateOrphanNeedsTwoAbsentObservations(t *testing.T) {
	for _, k := range orphanKinds() {
		t.Run(k.name+"/absent_twice", func(t *testing.T) {
			f := k.fixture(t, true)
			inspector := &sequenceInspector{answers: []driver.TargetInspection{{}, {}}}
			err := k.dispatch(f, context.Background(), inspector)
			if !errors.Is(err, ErrCandidateOrphanSettled) || inspector.calls != 2 {
				t.Fatalf("err = %v calls=%d, want a settlement after two looks", err, inspector.calls)
			}
			if st := f.state(t); st.command != "reverted" || st.reservation != "released" {
				t.Fatalf("state = %+v", st)
			}
		})
		t.Run(k.name+"/present_on_second_look", func(t *testing.T) {
			f := k.fixture(t, true)
			before := f.state(t)
			inspector := &sequenceInspector{answers: []driver.TargetInspection{{}, lateContainer()}}
			err := k.dispatch(f, context.Background(), inspector)
			if !errors.Is(err, ErrCandidateOrphanPresent) || errors.Is(err, ErrCandidateOrphanSettled) || inspector.calls != 2 {
				t.Fatalf("err = %v calls=%d", err, inspector.calls)
			}
			if after := f.state(t); after != before || after.command != "unknown" || after.reservation != "held" {
				t.Fatalf("state changed from %+v to %+v", before, after)
			}
		})
	}
}

// The second look is a settle delay after the first, not back to back.
func TestAbsenceSecondLookWaitsTheSettleDelay(t *testing.T) {
	old := absenceSettleDelay
	absenceSettleDelay = 40 * time.Millisecond
	defer func() { absenceSettleDelay = old }()
	f := orphanKinds()[0].fixture(t, true)
	var looks []time.Time
	inspector := &fakeInspector{onInspect: func() { looks = append(looks, time.Now()) }}
	_ = orphanKinds()[0].dispatch(f, context.Background(), inspector)
	if len(looks) != 2 || looks[1].Sub(looks[0]) < 40*time.Millisecond {
		t.Fatalf("looks = %v", looks)
	}
}

func TestSettleOrphanNeverRanNeedsTwoAbsentObservations(t *testing.T) {
	t.Run("absent_twice", func(t *testing.T) {
		r := reservedStart(t)
		r.orphaned(t, false)
		inspector := &sequenceInspector{answers: []driver.TargetInspection{{}, {}}}
		raw, sig := r.settleCommand(t, 1)
		receipt, err := r.f.store.SettleUpdateStart(context.Background(), raw, sig, inspector)
		if err != nil || receipt.Outcome != "never_ran" || inspector.calls != 2 {
			t.Fatalf("receipt %+v err %v calls %d", receipt, err, inspector.calls)
		}
	})
	t.Run("present_on_second_look", func(t *testing.T) {
		r := reservedStart(t)
		r.orphaned(t, false)
		inspector := &sequenceInspector{answers: []driver.TargetInspection{{}, lateContainer()}}
		raw, sig := r.settleCommand(t, 1)
		_, err := r.f.store.SettleUpdateStart(context.Background(), raw, sig, inspector)
		if err == nil || inspector.calls != 2 {
			t.Fatalf("err %v calls %d, want a refusal after two looks", err, inspector.calls)
		}
		if r.startState(t) != "unknown" {
			t.Fatalf("the start is %s", r.startState(t))
		}
		if desired, observed, revision := r.resource(t); desired != "present" || observed != "unknown" || revision != 1 {
			t.Fatalf("resource: %s %s %d", desired, observed, revision)
		}
		if scalar(t, r.f.store.db, `SELECT COUNT(*) FROM runner_commands WHERE kind='verify'`) != 0 {
			t.Fatal("a settlement row was written")
		}
	})
}

// The tests run with no delay between the two looks; this pins the property the production value exists for. A
// create the runner gave up on may be completed by the daemon after the client disconnected. The bound assumes the
// daemon finishes a request within DockerRequestTimeout of its issue; dockerd does not promise that, so this is a
// stated assumption, not an observation. Under it: the first look is taken with the data execution gate held, so no
// request of the runner's can be issued after it, and a second look more than the timeout later sees every container
// an earlier request was going to produce. A delay that is not longer than the timeout proves nothing: the first
// version of this value was two seconds against a five-second timeout.
func TestTheProductionSecondLookWaitsLongerThanADaemonCreateCanTake(t *testing.T) {
	if defaultAbsenceSettleDelay <= driver.DockerRequestTimeout {
		t.Fatalf("the production settle delay is %s, which does not exceed the daemon request timeout %s", defaultAbsenceSettleDelay, driver.DockerRequestTimeout)
	}
}

// connectionBound is candidateExecutionTimeout in the transport package, the bound of the connection that carries a
// settlement. A settlement may inspect for up to DockerRequestTimeout each time.
const connectionBound = 30 * time.Second

// countingWaits replaces the settle wait with a counter for the length of a test.
func countingWaits(t *testing.T) *int {
	t.Helper()
	old := absenceWaiter
	n := 0
	absenceWaiter = func(context.Context) error { n++; return nil }
	t.Cleanup(func() { absenceWaiter = old })
	return &n
}

// The whole of an orphaned enable start's settlement (the target's two looks and the source's two looks) must fit
// the connection bound at the worst case: every inspection taking its full timeout, and the delay taken once. The
// delay is taken once because the source's looks share the target's wait instead of adding their own.
func TestSettlingAnOrphanedEnableStartFitsTheConnectionBound(t *testing.T) {
	waits := countingWaits(t)
	r, source := enableStartAsReserved(t)
	r.claimOrphan(t, false)
	inspector := &keyedInspector{}
	raw, sig := r.settleCommand(t, 1)
	if receipt, err := r.f.store.SettleUpdateStart(context.Background(), raw, sig, inspector); err != nil || receipt.Outcome != "never_ran" {
		t.Fatalf("receipt: %+v %v", receipt, err)
	}
	inspections := inspector.seen[source] + inspector.seen[r.command.Payload.ResourceID]
	if inspector.seen[source] != 2 || inspector.seen[r.command.Payload.ResourceID] != 2 {
		t.Fatalf("each of source and target is looked at twice: %v", inspector.seen)
	}
	worst := time.Duration(inspections)*driver.DockerRequestTimeout + time.Duration(*waits)*defaultAbsenceSettleDelay
	if *waits != 1 || worst+2*time.Second > connectionBound {
		t.Fatalf("%d inspections and %d waits take up to %s, which does not fit the %s connection bound", inspections, *waits, worst, connectionBound)
	}
}

// The namespace sweep is the evidence that nothing was left behind, so an empty listing is confirmed by a second
// listing after the delay, like the target's absence: a container a late create produces between the two is removed.
func TestSweepConfirmsAnEmptyNamespaceWithASecondListingAfterTheDelay(t *testing.T) {
	waits := countingWaits(t)
	r := reservedStart(t)
	prover := &fakeTargetProver{}
	for i, stage := range []string{"stop", "remove_code"} {
		raw, sig := r.removal(t, stage, i+1)
		if _, err := r.f.store.RemoveReservedUpdateStart(context.Background(), raw, sig, prover); err != nil {
			t.Fatal(err)
		}
	}
	late := strings.Repeat("ee", 32)
	inspector := &scriptedLister{answers: [][]driver.NamespaceContainer{nil, {{NativeID: late, State: "created"}}, nil, nil}}
	raw, sig := r.modeCommand(t, 3, control.SettleModeSweep)
	receipt, err := r.f.store.SettleUpdateStart(context.Background(), raw, sig, inspector)
	if err != nil || receipt.Outcome != "swept" || receipt.Removed != "1" {
		t.Fatalf("sweep: %+v %v", receipt, err)
	}
	if inspector.lists != 4 || len(inspector.removed) != 1 || inspector.removed[0] != late {
		t.Fatalf("the late container was not found by the confirming listing and removed: lists=%d removed=%v", inspector.lists, inspector.removed)
	}
	if *waits != 2 {
		t.Fatalf("each empty listing is confirmed after a wait: %d waits", *waits)
	}
}

// scriptedLister answers each namespace listing from a script, the last answer repeating.
type scriptedLister struct {
	fakeInspector
	answers [][]driver.NamespaceContainer
	lists   int
}

func (s *scriptedLister) ListNamespaceContainers(context.Context, string) ([]driver.NamespaceContainer, error) {
	i := min(s.lists, len(s.answers)-1)
	s.lists++
	return append([]driver.NamespaceContainer(nil), s.answers[i]...), nil
}
