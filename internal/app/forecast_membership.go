package app

import (
	"sort"
	"time"
)

const (
	membershipMember          = "member"
	membershipDuplicate       = "duplicate"
	membershipDegraded        = "degraded"
	membershipMismatch        = "mismatch"
	membershipPendingBaseline = "pending_baseline"
	membershipPreLive         = "pre_live"
	membershipOffBenchmark    = "off_benchmark"
	membershipUnknown         = "unknown"
)

type verdict struct {
	Set    *groupSet
	Status string
}

// classifyMembership is shared by statistics and read annotations. Its canonical order matches
// the statistics loader (symbol, made_at, id), including the permutation input order. It sorts a
// private slice; neither the input slice nor any ledger row is changed.
//
// A missing/empty cohort benchmark preserves the existing unconstrained statistics mode. Callers
// that have a cohort intent must supply it; a later on-benchmark rerun cannot replace an earlier
// off-benchmark decision. Pre-live and non-accepted rows never reserve a symbol.
func classifyMembership(sets []*groupSet, from time.Time, cohortBench map[string]string) []verdict {
	ordered := append([]*groupSet(nil), sets...)
	sort.SliceStable(ordered, func(i, j int) bool {
		a, b := ordered[i], ordered[j]
		if a.Symbol != b.Symbol {
			return a.Symbol < b.Symbol
		}
		if a.MadeAt != b.MadeAt {
			return a.MadeAt < b.MadeAt
		}
		return a.ID < b.ID
	})
	first := map[string]*groupSet{}
	out := make([]verdict, 0, len(ordered))
	for _, s := range ordered {
		status := membershipUnknown
		switch {
		case !from.IsZero() && madeAtBefore(s.MadeAt, from):
			status = membershipPreLive
		case s.State == predStateAbsent || s.State == predStateRejected || s.State == predStateBacktest || s.State == predStateTest:
			status = s.State
		case s.State == predStateAccepted:
			anchor := first[s.Symbol]
			if anchor == nil {
				anchor = s
				first[s.Symbol] = s
			}
			bench := cohortBench[s.Cohort]
			switch {
			case bench != "" && (s.BenchCode != bench || anchor.BenchCode != bench):
				status = membershipOffBenchmark
			case anchor != s:
				status = membershipDuplicate
			case s.BaseCheck == baseCheckPending || s.BaseCheck == baseRefused:
				status = membershipPendingBaseline
			case s.DecisionQuality != predQualityNormal:
				status = membershipDegraded
			case s.BaseCheck == baseMismatch:
				status = membershipMismatch
			default:
				status = membershipMember
			}
		}
		out = append(out, verdict{Set: s, Status: status})
	}
	return out
}
