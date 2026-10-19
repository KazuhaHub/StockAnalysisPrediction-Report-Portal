package app

// Golden snapshots of the group statistics (plan §4.1).
//
// This is a CHARACTERIZATION test: it passes on main and pins today's statistics output byte for
// byte, so every later release proves it changed nothing it did not mean to. The fixture is built
// through the real write path (InsertPredictionSet for the set/claims/marks, FinishForecastBaseline
// and SettleForecastMark for the evaluator's own writes), never through hand-written rows, so the
// golden output is the output the running system produces.
//
// Regenerate only in a commit whose message explains every changed byte:
//
//	go test ./internal/app -run TestForecastGolden -update-forecast-golden
//
// EXPECTED REGENERATION IN P7. P7 changes the baseline-check semantics (design §9.4: the reference
// session comes from made_at, the emitted baseline is compared against what the market printed) and
// settles the marks a baseline mismatch currently leaves pending. Every set here sets
// EmittedBaseFen = its S0Fen so that the fixture is one a regeneration under P7 can re-derive; P7
// will refresh these three files wholesale, in its own commit, and that is the one whole-file
// regeneration this suite anticipates.
//
// The fixture deliberately holds NO pre_live, test or off_benchmark set: those are introduced by
// P5, P36 and P4, which bring their own tests and must leave these snapshots untouched.

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"
)

var updateForecastGolden = flag.Bool("update-forecast-golden", false,
	"rewrite testdata/forecast/golden_*.json from the current statistics output")

// goldenFrom / goldenTo bound the fixture's three months.
const (
	goldenFrom = "2025-04-01"
	goldenTo   = "2025-06-30"
	// goldenToday is FIXED so the snapshots never depend on the wall clock. It is well past the
	// fixture's months, so every fixture group is closed.
	goldenToday = "2030-01-01"
)

// ---------- fixture specification ----------

// goldenFixtureSpec is the fixture as data: the exact inputs handed to the writers. It is written to
// golden_fixture.json beside the two outputs so a reviewer can read what produced the snapshots
// without reading this Go file.
type goldenFixtureSpec struct {
	Rtype  string            `json:"rtype"`
	Bench  string            `json:"bench_code"`
	Months []goldenMonthSpec `json:"months"`
}

type goldenMonthSpec struct {
	Label string          `json:"label"`
	Sets  []goldenSetSpec `json:"sets"`
}

type goldenSetSpec struct {
	Symbol               string                    `json:"symbol"`
	Title                string                    `json:"report_title"`
	Kind                 string                    `json:"report_kind,omitempty"`
	Name                 string                    `json:"report_name,omitempty"`
	MadeOn               string                    `json:"made_on"`
	MadeAt               string                    `json:"made_at"`
	ShareReport          bool                      `json:"share_report_with_previous,omitempty"`
	PolicyVersion        string                    `json:"policy_version"`
	ProbabilityMethod    string                    `json:"probability_method"`
	ProbabilityTransform string                    `json:"probability_transform,omitempty"`
	DecisionQuality      string                    `json:"decision_quality"`
	DecisionStatus       string                    `json:"decision_status"`
	HorizonStd           bool                      `json:"horizon_std"`
	Claims               goldenClaimsSpec          `json:"claims"`
	Baseline             goldenBaselineSpec        `json:"baseline"`
	Marks                map[string]goldenMarkSpec `json:"marks"`
}

type goldenClaimsSpec struct {
	Stance      string `json:"stance"`
	TerminalFen int64  `json:"terminal_fen"`
	PProfitBp   *int32 `json:"p_profit_bp,omitempty"`
	EntryFen    *int64 `json:"entry_fen,omitempty"`
	BearFen     *int64 `json:"bear_fen,omitempty"`
	BaseFen     *int64 `json:"base_fen,omitempty"`
	BullFen     *int64 `json:"bull_fen,omitempty"`
	WeightBp    *int32 `json:"weight_bp,omitempty"`
}

// goldenBaselineSpec drives FinishForecastBaseline. A set with Settled=false is left pending, which
// is the "pending baseline" case; IndexHasS0=false settles it as mismatch.
type goldenBaselineSpec struct {
	Settled    bool   `json:"settled"`
	IndexHasS0 bool   `json:"index_has_s0"`
	S0Date     string `json:"s0_date"`
	BaseDate   string `json:"base_date"`
	S0Fen      int64  `json:"s0_fen"`
}

type goldenMarkSpec struct {
	Status     string `json:"status"`
	Session    string `json:"session,omitempty"`
	CloseFen   *int64 `json:"close_fen,omitempty"`
	LowFen     *int64 `json:"low_fen,omitempty"`
	HighFen    *int64 `json:"high_fen,omitempty"`
	RetBp      *int32 `json:"ret_bp,omitempty"`
	BenchRetBp *int32 `json:"bench_ret_bp,omitempty"`
	Halted     bool   `json:"halted,omitempty"`
	CorpAction bool   `json:"corp_action,omitempty"`
}

func i64p(v int64) *int64 { return &v }
func i32p(v int32) *int32 { return &v }

// goldenFixtureSpecFor builds the fixture. Everything the plan §4.1 calls for is here as a named,
// deliberate case, and the bulk of the members exist only to reach the n>=30 gate with real variety.
func goldenFixtureSpecFor() goldenFixtureSpec {
	spec := goldenFixtureSpec{Rtype: predTestRtype, Bench: defaultForecastBench}

	// 2025-04: the membership edge cases. No cell here is mature, so they cost no permutations.
	apr := goldenMonthSpec{Label: "2025-04"}
	apr.Sets = append(apr.Sets,
		goldenSetSpec{
			Symbol: "600201", Title: "600201 2025-04 report", Kind: "投资决策", Name: "公司 600201",
			MadeOn: "2025-04-10", MadeAt: "2025-04-10T09:00:00+08:00",
			PolicyVersion: "v3config-1", ProbabilityMethod: "subjective_committee",
			DecisionQuality: "degraded", DecisionStatus: "eligible", HorizonStd: true,
			Claims:   goldenClaims(11000, i32p(6000)),
			Baseline: goldenBaselineSpec{Settled: true, IndexHasS0: true, S0Date: "2025-04-11", BaseDate: "2025-04-11", S0Fen: 10000},
			Marks:    map[string]goldenMarkSpec{"d5": goldenResolved(200, 20)},
		},
		goldenSetSpec{
			// A revision: the same report, a later receipt, a normal verdict. It is a duplicate of the
			// first set for its symbol, which is what D4 keeps out of the cell.
			Symbol: "600201", Title: "600201 2025-04 report", Kind: "投资决策", Name: "公司 600201",
			MadeOn: "2025-04-10", MadeAt: "2025-04-10T11:00:00+08:00", ShareReport: true,
			PolicyVersion: "v3config-1", ProbabilityMethod: "subjective_committee",
			DecisionQuality: "normal", DecisionStatus: "eligible", HorizonStd: true,
			Claims:   goldenClaims(11500, i32p(6500)),
			Baseline: goldenBaselineSpec{Settled: true, IndexHasS0: true, S0Date: "2025-04-11", BaseDate: "2025-04-11", S0Fen: 10000},
			Marks:    map[string]goldenMarkSpec{"d5": goldenResolved(300, 40)},
		},
		goldenSetSpec{
			// A first set whose baseline check came back mismatch: the symbol is out for the group.
			Symbol: "600202", Title: "600202 2025-04 report", Kind: "投资决策", Name: "公司 600202",
			MadeOn: "2025-04-10", MadeAt: "2025-04-10T10:00:00+08:00",
			PolicyVersion: "v3config-1", ProbabilityMethod: "subjective_committee",
			DecisionQuality: "normal", DecisionStatus: "eligible", HorizonStd: true,
			Claims:   goldenClaims(12000, i32p(7000)),
			Baseline: goldenBaselineSpec{Settled: true, IndexHasS0: false, S0Date: "2025-04-11", BaseDate: "2025-04-11", S0Fen: 10000},
			Marks:    map[string]goldenMarkSpec{"d5": goldenResolved(400, 60)},
		},
		goldenSetSpec{
			// A pending baseline: it blocks the whole group's cells rather than being scored.
			Symbol: "600203", Title: "600203 2025-04 report", Kind: "投资决策", Name: "公司 600203",
			MadeOn: "2025-04-10", MadeAt: "2025-04-10T10:00:00+08:00",
			PolicyVersion: "v3config-1", ProbabilityMethod: "subjective_committee",
			DecisionQuality: "normal", DecisionStatus: "eligible", HorizonStd: true,
			Claims:   goldenClaims(12500, i32p(7500)),
			Baseline: goldenBaselineSpec{Settled: false, S0Date: "2025-04-11", BaseDate: "2025-04-11", S0Fen: 10000},
			Marks:    map[string]goldenMarkSpec{"d5": {Status: "pending"}},
		},
	)

	// 2025-05: the mature month. 36 members, all checkpoints resolved.
	may := goldenMonthSpec{Label: "2025-05"}
	may.Sets = append(may.Sets, goldenMayMembers()...)

	// 2025-06: a single fully-resolved set, which is a small (n<30) cell.
	jun := goldenMonthSpec{Label: "2025-06"}
	jun.Sets = append(jun.Sets, goldenSetSpec{
		Symbol: "600300", Title: "600300 2025-06 report", Kind: "投资决策", Name: "公司 600300",
		MadeOn: "2025-06-10", MadeAt: "2025-06-10T10:00:00+08:00",
		PolicyVersion: "v3config-1", ProbabilityMethod: "subjective_committee",
		DecisionQuality: "normal", DecisionStatus: "eligible", HorizonStd: true,
		Claims:   goldenClaims(10800, i32p(5500)),
		Baseline: goldenBaselineSpec{Settled: true, IndexHasS0: true, S0Date: "2025-06-11", BaseDate: "2025-06-11", S0Fen: 10000},
		Marks:    goldenAllResolved(),
	})

	spec.Months = []goldenMonthSpec{apr, may, jun}
	return spec
}

func goldenClaims(terminal int64, pProfit *int32) goldenClaimsSpec {
	return goldenClaimsSpec{
		Stance: "long", TerminalFen: terminal, PProfitBp: pProfit,
		EntryFen: i64p(9850), BearFen: i64p(8000), BaseFen: i64p(10000), BullFen: i64p(14000),
		WeightBp: i32p(1000),
	}
}

func goldenResolved(retBp, benchRetBp int32) goldenMarkSpec {
	return goldenMarkSpec{
		Status: "resolved", CloseFen: i64p(int64(10000) + int64(retBp)), LowFen: i64p(9800),
		HighFen: i64p(10200), RetBp: i32p(retBp), BenchRetBp: i32p(benchRetBp),
	}
}

func goldenAllResolved() map[string]goldenMarkSpec {
	return map[string]goldenMarkSpec{
		"d5": goldenResolved(180, 40), "d20": goldenResolved(190, 45),
		"d60": goldenResolved(200, 50), "d120": goldenResolved(210, 55), "h12": goldenResolved(220, 60),
	}
}

// goldenMayMembers builds 36 members of one month, the only mature fixture month. The design gives
// every metric real variety rather than a degenerate constant:
//
//   - terminal (hence upside) alternates sign with the index and is at least 6% in magnitude, so the
//     hit-rate subset has ~35 members;
//   - excess is positive for the first half and negative for the second, so "agree" and "disagree"
//     are both well represented (the hit rate lands near a half, not at 1);
//   - stance cycles long / long / no_position, so the spread has both arms;
//   - the boundary upsides 4.99 % / 5.00 % / 5.01 % pin the exact 5 % hit threshold; the remaining
//     named members pin zero excess, a nil benchmark return, a corporate action and a halt;
//   - the last two are non-standard horizons and carry no h12 mark.
func goldenMayMembers() []goldenSetSpec {
	type spec struct {
		terminal   int64
		quality    string
		policy     string
		horizonStd bool
		corp, halt bool
		nilBench   bool
		zeroExcess bool
		stance     string
	}
	makeSpec := func(i int) spec {
		// Upside sign: even index positive, odd negative; magnitude 6 %..14 %.
		upSign := int64(1)
		if i%2 == 1 {
			upSign = -1
		}
		upsideBp := int64(600 + (i%5)*200)
		s := spec{
			terminal: 10000 + upSign*upsideBp, quality: predQualityNormal,
			// A 32/4 policy split: the first slice is mature (its own metrics and correction), the
			// second is small (n only). Both slice branches are pinned by one fixture.
			policy: func() string {
				if i < 32 {
					return "v3config-1"
				}
				return "v3config-2"
			}(),
			horizonStd: i < 34,
			stance:     []string{"long", "long", "no_position"}[i%3],
		}
		switch i {
		case 0:
			s.terminal = 10499 // +4.99%: below the 5% floor, untested
		case 1:
			s.terminal = 10500 // exactly +5.00%: tested
		case 2:
			s.terminal = 10501 // +5.01%: tested
		case 3:
			s.zeroExcess = true
		case 4:
			s.nilBench = true
		case 5:
			s.corp = true
		case 6:
			s.halt = true
		}
		return s
	}

	var out []goldenSetSpec
	for i := 0; i < 36; i++ {
		m := makeSpec(i)
		// Excess sign: positive for the first half, negative for the second.
		exSign := int32(1)
		if i >= 18 {
			exSign = -1
		}
		marks := map[string]goldenMarkSpec{}
		for _, cp := range []string{"d5", "d20", "d60", "d120"} {
			idx := int32(checkpointIndex(cp))
			bench := int32(100) + idx*10
			ret := bench + exSign*(150+(int32(i%7))*40) + idx*5
			if m.zeroExcess {
				ret = bench // an exact tie with the benchmark
			}
			mk := goldenMarkSpec{
				Status: "resolved", Session: "2025-05-16",
				CloseFen: i64p(int64(10000) + int64(ret)), LowFen: i64p(9800), HighFen: i64p(10200),
				RetBp: i32p(ret), BenchRetBp: i32p(bench),
				CorpAction: m.corp, Halted: m.halt,
			}
			if m.nilBench {
				mk.BenchRetBp = nil
			}
			marks[cp] = mk
		}
		if m.horizonStd {
			ret := int32(240) + exSign*(int32(i%5)*30)
			marks["h12"] = goldenMarkSpec{
				Status: "resolved", Session: "2025-05-16",
				CloseFen: i64p(int64(10000) + int64(ret)), LowFen: i64p(9800), HighFen: i64p(10200),
				RetBp: i32p(ret), BenchRetBp: i32p(70),
				CorpAction: m.corp, Halted: m.halt,
			}
		}
		out = append(out, goldenSetSpec{
			Symbol: fmt.Sprintf("6001%02d", i), Title: fmt.Sprintf("6001%02d 2025-05 report", i),
			Kind: "投资决策", Name: fmt.Sprintf("公司 6001%02d", i),
			MadeOn: "2025-05-10", MadeAt: fmt.Sprintf("2025-05-10T10:%02d:00+08:00", i),
			PolicyVersion: m.policy, ProbabilityMethod: "subjective_committee",
			DecisionQuality: m.quality, DecisionStatus: "eligible", HorizonStd: m.horizonStd,
			Claims: goldenClaimsSpec{
				Stance: m.stance, TerminalFen: m.terminal, PProfitBp: i32p(int32(4000 + (i%6)*1000)),
				EntryFen: i64p(9850), BearFen: i64p(8000), BaseFen: i64p(10000), BullFen: i64p(14000),
				WeightBp: i32p(1000),
			},
			Baseline: goldenBaselineSpec{Settled: true, IndexHasS0: true, S0Date: "2025-05-12", BaseDate: "2025-05-12", S0Fen: 10000},
			Marks:    marks,
		})
	}
	return out
}

func checkpointIndex(cp string) int {
	for i, v := range predCheckpoints {
		if v == cp {
			return i
		}
	}
	return 0
}

// ---------- building the fixture through the real writers ----------

// buildGoldenServer writes the fixture through the real write path and returns the loaded group sets.
func buildGoldenServer(t *testing.T, spec goldenFixtureSpec) (*Server, []*groupSet) {
	t.Helper()
	s := newV1Server(t)
	for _, m := range spec.Months {
		var prevReportID int64
		for _, g := range m.Sets {
			reportID := prevReportID
			if !g.ShareReport {
				id, _, err := s.st.UpsertReport(Rep{
					Title: g.Title, Symbol: g.Symbol, Name: g.Name, RType: spec.Rtype,
					Date: g.MadeOn, Kind: g.Kind, Time: g.MadeAt, MD: "# " + g.Title,
				})
				if err != nil {
					t.Fatalf("UpsertReport %s: %v", g.Symbol, err)
				}
				reportID = id
				prevReportID = id
			}
			setID := insertGoldenSet(t, s, spec, g, reportID)
			if g.Baseline.Settled {
				var index []QuoteBar
				if g.Baseline.IndexHasS0 {
					index = []QuoteBar{{Date: g.Baseline.S0Date, Close: 4000}}
				}
				if err := s.st.ApplyForecastPlan(forecastPlan{setID: setID, baseline: &forecastBaseline{
					S0: g.Baseline.S0Date, BaseDate: g.Baseline.BaseDate, S0Fen: g.Baseline.S0Fen, Index: index,
				}}, time.Now()); err != nil {
					t.Fatalf("FinishForecastBaseline %s: %v", g.Symbol, err)
				}
			}
			settleGoldenMarks(t, s, setID, g)
		}
	}
	sets, err := s.st.loadForecastGroupSets(spec.Rtype, goldenFrom, goldenTo)
	if err != nil {
		t.Fatalf("load fixture sets: %v", err)
	}
	return s, sets
}

func insertGoldenSet(t *testing.T, s *Server, spec goldenFixtureSpec, g goldenSetSpec, reportID int64) int64 {
	t.Helper()
	set := &predSetRow{
		Symbol: g.Symbol, Rtype: spec.Rtype,
		MadeAt: g.MadeAt, MadeOn: g.MadeOn, Cohort: g.MadeOn[:7], Zone: predZone,
		State: predStateAccepted,
		// A payload hash unique per set: the idempotency key is (report_id, payload_sha256), and the
		// duplicate revision shares its report with the set it revises.
		Payload: g.Title + "@" + g.MadeAt, PayloadSHA: sha256Hex([]byte(g.Title + "@" + g.MadeAt)),
		BenchCode:     spec.Bench,
		PolicyVersion: g.PolicyVersion, ProbabilityMethod: g.ProbabilityMethod,
		ProbabilityTransform: g.ProbabilityTransform, DecisionQuality: g.DecisionQuality,
		DecisionStatus: g.DecisionStatus,
	}
	if g.HorizonStd {
		set.HorizonStd = 1
	}
	// The producer's emitted baseline is the session's close, which is what a real report carries and
	// what P7's baseline check compares against the market. The statistics do not read it (they read
	// s0_fen), so it does not move the snapshots — but a fixture without it is not one a later PR can
	// regenerate faithfully, and P7 changes the baseline semantics enough to refresh these files.
	set.EmittedBaseFen = i64p(g.Baseline.S0Fen)
	c := g.Claims
	claims := []predClaimRow{
		{ClaimKey: "stance", Kind: kindStance, Stance: c.Stance, WeightBp: c.WeightBp},
		{ClaimKey: "expected_terminal", Kind: kindTerminal, PriceFen: i64p(c.TerminalFen), ProbBp: c.PProfitBp},
	}
	if c.EntryFen != nil {
		claims = append(claims, predClaimRow{ClaimKey: "entry_price", Kind: kindEntry, PriceFen: c.EntryFen})
	}
	for _, sc := range []struct {
		key, label string
		p          *int64
	}{{"scenario.bear", "bear", c.BearFen}, {"scenario.base", "base", c.BaseFen}, {"scenario.bull", "bull", c.BullFen}} {
		if sc.p != nil {
			claims = append(claims, predClaimRow{ClaimKey: sc.key, Kind: kindScenario, Label: sc.label, PriceFen: sc.p})
		}
	}
	setID, _, err := s.st.InsertPredictionSet(reportID, set, claims)
	if err != nil {
		t.Fatalf("InsertPredictionSet %s: %v", g.Symbol, err)
	}
	return setID
}

func settleGoldenMarks(t *testing.T, s *Server, setID int64, g goldenSetSpec) {
	t.Helper()
	for cp, mk := range g.Marks {
		if mk.Status == "pending" {
			continue
		}
		var markID int64
		if err := s.st.queryRow("SELECT id FROM prediction_marks WHERE set_id=? AND checkpoint=?", setID, cp).Scan(&markID); err != nil {
			t.Fatalf("find mark %s/%s: %v", g.Symbol, cp, err)
		}
		res := markResult{
			Status:     mk.Status,
			Session:    mk.Session,
			CloseFen:   mk.CloseFen,
			LowFen:     mk.LowFen,
			HighFen:    mk.HighFen,
			RetBp:      mk.RetBp,
			BenchRetBp: mk.BenchRetBp,
			Halted:     mk.Halted,
			CorpAction: mk.CorpAction,
		}
		if mk.Status == "refused" || mk.Status == "refused_final" {
			res.Status = markRefused
			res.Refusal = "no_bar"
		}
		plan := forecastPlan{setID: setID, settles: []forecastSettle{{markID: markID, res: res}}}
		if mk.Status == "refused_final" {
			plan.finals = []forecastFinal{{markID: markID, code: "no_bar"}}
		}
		if err := s.st.ApplyForecastPlan(plan, time.Now()); err != nil {
			t.Fatalf("settle %s/%s: %v", g.Symbol, cp, err)
		}
	}
}

// ---------- the snapshots ----------

type goldenCellsDoc struct {
	Rtype       string                `json:"rtype"`
	Checkpoints []string              `json:"checkpoints"`
	Groups      []goldenGroupSnapshot `json:"groups"`
}

type goldenGroupSnapshot struct {
	Grouping string                   `json:"grouping"`
	Label    string                   `json:"label"`
	Start    string                   `json:"start"`
	End      string                   `json:"end"`
	Official bool                     `json:"official"`
	Coverage forecastCoverage         `json:"coverage"`
	Cells    map[string]*forecastCell `json:"cells"`
}

// goldenGrouping is one way of cutting the fixture's range. "month" is the official record; the rest
// are the exploratory groupings, which use exactly the same engine.
type goldenGrouping struct {
	Name     string
	Group    string
	Official bool
	Periods  []forecastPeriod
}

func goldenGroupings() []goldenGrouping {
	return []goldenGrouping{
		{Name: "month", Group: "month", Official: true, Periods: forecastPeriods("month", goldenFrom, goldenTo)},
		{Name: "quarter", Group: "quarter", Periods: forecastPeriods("quarter", goldenFrom, goldenTo)},
		{Name: "week", Group: "week", Periods: forecastPeriods("week", goldenFrom, goldenTo)},
		{Name: "all", Group: "all", Periods: forecastPeriods("all", goldenFrom, goldenTo)},
	}
}

func buildGoldenCells(spec goldenFixtureSpec, sets []*groupSet) goldenCellsDoc {
	doc := goldenCellsDoc{Rtype: spec.Rtype, Checkpoints: append([]string{}, predCheckpoints...)}
	for _, gr := range goldenGroupings() {
		for _, p := range gr.Periods {
			in := make([]*groupSet, 0, len(sets))
			for _, gs := range sets {
				if gs.MadeOn >= p.Start && gs.MadeOn <= p.End {
					in = append(in, gs)
				}
			}
			members, pending, cov := forecastMembership(in, time.Time{}, "")
			cells := make(map[string]*forecastCell, len(predCheckpoints))
			for _, cp := range predCheckpoints {
				cells[cp] = computeForecastCell(members, pending, spec.Rtype, p.Start, p.End, cp, goldenToday)
			}
			doc.Groups = append(doc.Groups, goldenGroupSnapshot{
				Grouping: gr.Name, Label: p.Label, Start: p.Start, End: p.End,
				Official: gr.Official, Coverage: cov, Cells: cells,
			})
		}
	}
	return doc
}

type goldenMembersDoc struct {
	Rtype string               `json:"rtype"`
	Cells []goldenMembersEntry `json:"cells"`
}

type goldenMembersEntry struct {
	Grouping   string           `json:"grouping"`
	Label      string           `json:"label"`
	Start      string           `json:"start"`
	End        string           `json:"end"`
	Checkpoint string           `json:"checkpoint"`
	State      string           `json:"state"`
	Members    []forecastMember `json:"members"`
}

// buildGoldenMembers freezes the members response of every MATURE cell. It calls the real handler, so
// the snapshot is exactly what the drawer returns, then keeps only the mature cells.
func buildGoldenMembers(t *testing.T, s *Server, spec goldenFixtureSpec) goldenMembersDoc {
	t.Helper()
	doc := goldenMembersDoc{Rtype: spec.Rtype}
	for _, gr := range goldenGroupings() {
		for _, p := range gr.Periods {
			for _, cp := range predCheckpoints {
				q := url.Values{}
				q.Set("mode", "custom")
				q.Set("from", goldenFrom)
				q.Set("to", goldenTo)
				q.Set("group", gr.Group)
				q.Set("label", p.Label)
				q.Set("checkpoint", cp)
				rec := httptest.NewRecorder()
				s.apiForecastGroupMembers(rec, httptest.NewRequest("GET",
					"/api/admin/forecast/groups/members?"+q.Encode(), nil), "admin")
				if rec.Code != http.StatusOK {
					t.Fatalf("members %s/%s/%s: %d %s", gr.Name, p.Label, cp, rec.Code, rec.Body.String())
				}
				var out forecastMembersResponse
				if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
					t.Fatalf("decode members: %v", err)
				}
				if out.State != "mature" {
					continue
				}
				doc.Cells = append(doc.Cells, goldenMembersEntry{
					Grouping: gr.Name, Label: out.Label, Start: out.Start, End: out.End,
					Checkpoint: out.Checkpoint, State: out.State, Members: out.Members,
				})
			}
		}
	}
	return doc
}

// ---------- the tests ----------

// TestForecastGoldenCells pins computeForecastCell for every group x checkpoint, with slices and
// coverage, byte for byte. Every later release keeps this file identical.
func TestForecastGoldenCells(t *testing.T) {
	spec := goldenFixtureSpecFor()
	_, sets := buildGoldenServer(t, spec)

	goldenCompare(t, "golden_fixture.json", goldenJSON(t, spec))
	goldenCompare(t, "golden_cells.json", goldenJSON(t, buildGoldenCells(spec, sets)))
}

// TestForecastGoldenMembers pins the members response of every mature cell.
func TestForecastGoldenMembers(t *testing.T) {
	spec := goldenFixtureSpecFor()
	s, _ := buildGoldenServer(t, spec)
	goldenCompare(t, "golden_members.json", goldenJSON(t, buildGoldenMembers(t, s, spec)))
}

// ---------- golden plumbing ----------

func goldenJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatalf("marshal golden: %v", err)
	}
	return append(b, '\n')
}

func goldenCompare(t *testing.T, name string, got []byte) {
	t.Helper()
	path := filepath.Join("testdata", "forecast", name)
	if *updateForecastGolden {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir testdata/forecast: %v", err)
		}
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v (run with -update-forecast-golden to create it)", path, err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("%s differs from the golden snapshot. If this is intended, regenerate it in its own\n"+
			"commit and explain every changed byte:\n%s\n%s", name, firstNameDiff(want, got), firstLineHint(got))
	}
}

// firstNameDiff returns the first line that differs, with a one-line context from each side.
func firstNameDiff(want, got []byte) string {
	wl := bytes.Split(want, []byte("\n"))
	gl := bytes.Split(got, []byte("\n"))
	for i := 0; i < len(wl) || i < len(gl); i++ {
		var w, g []byte
		if i < len(wl) {
			w = wl[i]
		}
		if i < len(gl) {
			g = gl[i]
		}
		if !bytes.Equal(w, g) {
			return fmt.Sprintf("  first difference at line %d\n  golden: %s\n  actual: %s", i+1, w, g)
		}
	}
	return "  byte lengths differ but no line does"
}

func firstLineHint(got []byte) string {
	if n := bytes.IndexByte(got, '\n'); n >= 0 {
		return "  (regenerate with: go test ./internal/app -run TestForecastGolden -update-forecast-golden)"
	}
	return ""
}
