package app

import (
	"sort"
	"time"
)

type forecastCapabilities struct {
	Monthly bool `json:"monthly"`
	Custom  bool `json:"custom"`
	Members bool `json:"members"`
}

// Capabilities describe permission, not instantaneous scorer capacity or remaining rate budget.
func forecastCapabilitiesFor(d forecastDecision) forecastCapabilities {
	allowed := d.Scope == scopeAll || d.Scope == scopeOwn
	return forecastCapabilities{Monthly: allowed, Custom: allowed && !d.External, Members: allowed}
}

// ForecastGroupsMetadata is exported so encoding/json can allocate its anonymous pointer when
// typed API consumers decode the flattened response. A nil pointer omits every explain field.
type ForecastGroupsMetadata struct {
	Summary      forecastGroupsSummary `json:"summary"`
	Rtypes       []string              `json:"rtypes"`
	MinN         int                   `json:"min_n"`
	Limits       forecastExplainLimits `json:"limits"`
	GeneratedAt  string                `json:"generated_at"`
	LedgerStart  string                `json:"ledger_start"`
	Capabilities forecastCapabilities  `json:"capabilities"`
	Evaluator    forecastExplainMode   `json:"evaluator"`
}

type forecastExplainLimits struct {
	MaxRangeMonths int `json:"max_range_months"`
	MaxWeekGroups  int `json:"max_week_groups"`
	OfficialSlots  int `json:"official_slots"`
	ExploreSlots   int `json:"explore_slots"`
	WaitSec        int `json:"wait_sec"`
	CustomPerMin   int `json:"custom_per_min"`
	MembersPerMin  int `json:"members_per_min"`
}

type forecastExplainMode struct {
	Mode string `json:"mode"`
}

// Scalars describe retained scoped ledger rows in the requested range, not statistical eligibility.
// Phase counts describe the classified checkpoint population already used by the returned cells.
type forecastGroupsSummary struct {
	Since               string         `json:"since"`
	Sets                int            `json:"sets"`
	Accepted            int            `json:"accepted"`
	Stocks              int            `json:"stocks"`
	MarksByPhase        map[string]int `json:"marks_by_phase"`
	NextResultNotBefore string         `json:"next_result_not_before,omitempty"`
	Expected            bool           `json:"expected,omitempty"`
}

func forecastGroupsSummaryFor(sets []*groupSet, groups []forecastGroup) forecastGroupsSummary {
	out := forecastGroupsSummary{Sets: len(sets), MarksByPhase: forecastPhaseCounts()}
	symbols := map[string]bool{}
	for _, set := range sets {
		if validReportDate(set.MadeOn) && (out.Since == "" || set.MadeOn < out.Since) {
			out.Since = set.MadeOn
		}
		if set.State == predStateAccepted {
			out.Accepted++
		}
		if set.Symbol != "" {
			symbols[set.Symbol] = true
		}
	}
	out.Stocks = len(symbols)
	for _, group := range groups {
		for _, cell := range group.Cells {
			if cell == nil || cell.State == "purged" {
				continue
			}
			if cell.State != "mature" {
				for phase, n := range cell.PendingReasons {
					out.MarksByPhase[phase] += n
				}
			}
			if cell.State == "mature" {
				resolved := 0
				if cell.Resolved != nil {
					resolved = *cell.Resolved
				} else if cell.N != nil {
					resolved = *cell.N
					if cell.CorpAction != nil {
						resolved += *cell.CorpAction
					}
				}
				out.MarksByPhase[predPhaseResolved] += resolved
				if cell.Refused != nil {
					out.MarksByPhase[predPhaseFinal] += *cell.Refused
				}
				continue
			}
			if cell.NotBefore != "" && (out.NextResultNotBefore == "" || cell.NotBefore < out.NextResultNotBefore) {
				out.NextResultNotBefore, out.Expected = cell.NotBefore, cell.Expected
			} else if cell.NotBefore != "" && cell.NotBefore == out.NextResultNotBefore {
				out.Expected = out.Expected && cell.Expected
			}
		}
	}
	return out
}

func (s *Server) forecastGroupsMetadataFor(sets []*groupSet, groups []forecastGroup, now time.Time, d forecastDecision) *ForecastGroupsMetadata {
	compute := s.forecastComputeLimits()
	out := &ForecastGroupsMetadata{Summary: forecastGroupsSummaryFor(sets, groups), MinN: predMatureMinN,
		GeneratedAt: now.UTC().Format(time.RFC3339Nano), Capabilities: forecastCapabilitiesFor(d),
		Evaluator: forecastExplainMode{Mode: forecastMode(s.st.GetSetting(setForecastAuto, "0"))},
		Limits: forecastExplainLimits{MaxRangeMonths: predMaxRangeMonths, MaxWeekGroups: predMaxWeekGroups,
			OfficialSlots: compute.Official, ExploreSlots: compute.Explore, WaitSec: int(compute.Wait / time.Second),
			CustomPerMin: s.settingInt(setForecastCustomPerMin, 20, 0, 600), MembersPerMin: s.settingInt(setForecastMembersPerMin, 60, 0, 600)}}
	out.Rtypes = []string{}
	for rtype, covered := range predictionRtypes {
		if covered {
			out.Rtypes = append(out.Rtypes, rtype)
		}
	}
	sort.Strings(out.Rtypes)
	if from, ok := s.st.ForecastPopulationFrom(); ok {
		out.LedgerStart = from.UTC().Format(time.RFC3339Nano)
	}
	return out
}
