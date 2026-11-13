//go:build linux || darwin

package journal

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

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
