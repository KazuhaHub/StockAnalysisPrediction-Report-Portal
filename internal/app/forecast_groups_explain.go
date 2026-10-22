package app

import "time"

// Explanations are request-time metadata. Copy the cell so neither a shared memo nor a later
// reader inherits another request's clock; membership, maturity and scoring remain untouched.
func forecastCellWithPendingReasons(cell *forecastCell, members, pending []*groupSet, cp string, now time.Time, calendars map[string][]string) *forecastCell {
	if cell == nil || cell.State != "open" && cell.State != "pending" && cell.State != "small" {
		return cell
	}
	if cp == "h12" {
		members, pending = filterHorizonStd(members), filterHorizonStd(pending)
	}
	out := *cell
	out.PendingReasons = map[string]int{
		predPhaseWaitingS0: 0, predPhaseWaiting: 0, predPhaseSettling: 0, predPhaseRetrying: 0,
		predPhaseOverdue: 0, predPhaseResolved: 0, predPhaseFinal: 0,
		"unknown": 0,
	}
	out.NotBefore, out.Expected = "", false
	sets := make([]PredictionSet, 0, len(members)+len(pending))
	refused := 0
	appendSet := func(gs *groupSet) {
		mark, ok := gs.Marks[cp]
		if !ok {
			mark = PredictionMark{Checkpoint: cp, Status: markPending}
		}
		sets = append(sets, PredictionSet{ID: gs.ID, MadeAt: gs.MadeAt, MadeOn: gs.MadeOn,
			BenchCode: gs.BenchCode, HorizonStd: gs.HorizonStd, S0Date: gs.S0Date,
			Marks: []PredictionMark{mark}})
	}
	for _, gs := range members {
		if gs.Marks[cp].Status == markRefusedFinal {
			refused++
		}
		appendSet(gs)
	}
	for _, gs := range pending {
		appendSet(gs)
	}
	capExceeded := len(members) > 0 && float64(refused)*100 > predRefusedMaxPct*float64(len(members))
	out.FailedCapExceeded = &capExceeded
	annotateReadPhasesWithCalendars(sets, now, calendars)
	for _, set := range sets {
		mark := set.Marks[0]
		if _, known := out.PendingReasons[mark.Phase]; !known {
			out.PendingReasons["unknown"]++
			continue
		}
		out.PendingReasons[mark.Phase]++
		if mark.Phase == predPhaseResolved || mark.Phase == predPhaseFinal || mark.NotBefore == "" {
			continue
		}
		if out.NotBefore == "" || mark.NotBefore < out.NotBefore {
			out.NotBefore, out.Expected = mark.NotBefore, mark.Expected
		} else if mark.NotBefore == out.NotBefore {
			out.Expected = out.Expected && mark.Expected
		}
	}
	return &out
}
