package app

import (
	"testing"
	"time"
)

// One request projects the same receipt days again and again: every set in a cohort shares a few
// receipt dates and every mark of a set shares its set's. The memo must change nothing but the work
// done, so a single memo reused across every case has to agree with a fresh projection each time,
// including after earlier calls have handed out the same session slices.
func TestPredSessionMemoMatchesFreshProjection(t *testing.T) {
	memo := newPredSessionMemo()
	start := time.Date(2025, 12, 20, 0, 0, 0, 0, chinaZone)
	for day := 0; day < 760; day += 3 {
		for _, clock := range []int{9, 15, 16} { // before, at and after the close
			madeAt := start.AddDate(0, 0, day).Add(time.Duration(clock) * time.Hour)
			madeOn := madeAt.Format("2006-01-02")
			if got, want := memo.weekdays(shiftDays(madeOn, -1)), predProjectSessions(shiftDays(madeOn, -1), 400); !equalStrings(got, want) {
				t.Fatalf("weekdays from %s differ", madeOn)
			}
			for _, cp := range predCheckpoints {
				for _, std := range []bool{true, false} {
					for _, frozen := range []string{"", madeOn, shiftDays(madeOn, 1), shiftDays(madeOn, -3)} {
						gotSch, gotExp := predReadProjectionWith(memo, cp, madeAt, std, frozen)
						wantSch, wantExp := predReadProjection(cp, madeAt, std, frozen)
						if gotSch != wantSch || gotExp != wantExp {
							t.Fatalf("%s %s std=%v frozen=%q: memo %+v/%v, fresh %+v/%v", madeAt, cp, std, frozen, gotSch, gotExp, wantSch, wantExp)
						}
					}
				}
			}
		}
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
