package app

// Group statistics (plan §7.2–7.6).
//
// One engine, two viewing modes: monthly is the record, a custom range is exploratory. The only
// difference between them is how the groups are cut; membership, maturity and every metric are the
// same code, and the permutation seed deliberately does not include the mode, so a custom range that
// happens to equal whole months returns exactly the monthly numbers.

import (
	"fmt"
	"math"
	"sort"
	"time"
)

type forecastGroupsResponse struct {
	*ForecastGroupsMetadata
	OK             bool                  `json:"ok"`
	Mode           string                `json:"mode"`
	Official       bool                  `json:"official"`
	Range          forecastRange         `json:"range"`
	Checkpoints    []string              `json:"checkpoints"`
	Unrecorded     int                   `json:"unrecorded"`
	Groups         []forecastGroup       `json:"groups"`
	ExcludedMonths []forecastPurgedMonth `json:"excluded_months,omitempty"`
	// The population these numbers describe, taken from the decision that selected the cohort. A
	// page that labels a result from its session-start navigation hint can name a population the
	// numbers no longer cover; this is the label that cannot drift from them.
	Scope   string `json:"scope"`
	ScopeOU string `json:"scope_ou,omitempty"`
}

type forecastPurgedMonth struct {
	Cohort   string `json:"cohort"`
	PurgedAt string `json:"purged_at"`
}

type forecastRange struct {
	From  string `json:"from"`
	To    string `json:"to"`
	Group string `json:"group,omitempty"`
}

type forecastGroup struct {
	Label    string `json:"label"`
	Start    string `json:"start"`
	End      string `json:"end"`
	Official bool   `json:"official"`
	PurgedAt string `json:"purged_at,omitempty"`
	// Bench is the index every member of this group was frozen with (P4). A range that spans cohorts
	// with different indexes is split into one group per index, so a cell never pools two baselines.
	Bench    string                   `json:"bench"`
	Coverage forecastCoverage         `json:"coverage"`
	Cells    map[string]*forecastCell `json:"cells"`
}

// forecastCoverage accounts for every accepted set in the group. The reasons are exhaustive so the
// numbers sum to the accepted count; a set that fits none of them would be a bug, not a silence.
type forecastCoverage struct {
	Reports         int `json:"reports"`
	Member          int `json:"member"`
	Duplicate       int `json:"duplicate"`
	Absent          int `json:"absent"`
	Rejected        int `json:"rejected"`
	Backtest        int `json:"backtest"`
	Degraded        int `json:"degraded"`
	Mismatch        int `json:"mismatch"`
	PendingBaseline int `json:"pending_baseline"`
	// PreLive counts sets made before the ledger start (P5). They are accounted for here so Reports
	// still equals the number of rows loaded; they simply never reach the record.
	PreLive int `json:"pre_live"`
	// Test counts sets a producer declared as test runs (P36). Like pre-live, they are accounted for
	// and never reach the record.
	Test int `json:"test"`
	// OffBenchmark counts accepted sets frozen with an index other than their cohort's benchmark
	// (P4). They are counted and kept out of the cell, so a cohort never pools two indexes.
	OffBenchmark int `json:"off_benchmark"`
	// Unknown accounts for future or corrupt ledger states without silently losing coverage.
	Unknown int `json:"unknown,omitempty"`
}

// madeAtBefore reports whether a set's stored made_at instant is strictly before the boundary. A
// stored value that cannot be parsed is treated as NOT before the boundary: an unreadable timestamp
// must not silently pull a live set into pre-live, which is the direction that would hide data.
func madeAtBefore(madeAt string, boundary time.Time) bool {
	t, err := time.Parse(time.RFC3339, madeAt)
	if err != nil {
		return false
	}
	return t.Before(boundary)
}

// forecastCell is one group × checkpoint. Every metric is a pointer so a non-mature cell can OMIT
// the key entirely: a zero would read as a measured spread of zero, which is a different claim.
type forecastCell struct {
	State string `json:"state"`
	// External all readers receive the maturity assurance without the ledger's member count.
	MinNMet           bool           `json:"min_n_met,omitempty"`
	PendingReasons    map[string]int `json:"pending_reasons,omitempty"`
	FailedCapExceeded *bool          `json:"failed_cap_exceeded,omitempty"`
	NotBefore         string         `json:"not_before,omitempty"`
	Expected          bool           `json:"expected,omitempty"`

	N          *int `json:"n,omitempty"`
	Resolved   *int `json:"resolved,omitempty"`
	Total      *int `json:"total,omitempty"`
	Refused    *int `json:"refused,omitempty"`
	CorpAction *int `json:"corp_action,omitempty"`

	SpreadBp     *float64 `json:"spread_bp,omitempty"`
	SpreadOneArm bool     `json:"spread_one_arm,omitempty"`
	SpreadP      *float64 `json:"spread_p,omitempty"`
	SpreadQ      *float64 `json:"spread_q,omitempty"`

	IC  *float64 `json:"ic,omitempty"`
	ICN *int     `json:"ic_n,omitempty"`
	ICP *float64 `json:"ic_p,omitempty"`
	ICQ *float64 `json:"ic_q,omitempty"`

	Hit     *float64 `json:"hit,omitempty"`
	HitN    *int     `json:"hit_n,omitempty"`
	HitNull *float64 `json:"hit_null,omitempty"`
	HitP    *float64 `json:"hit_p,omitempty"`
	HitQ    *float64 `json:"hit_q,omitempty"`

	Fill        *float64 `json:"fill,omitempty"`
	FillRetBp   *float64 `json:"fill_ret_bp,omitempty"`
	FillDerived bool     `json:"fill_derived,omitempty"`

	Band   *float64 `json:"band,omitempty"`
	BandN  *int     `json:"band_n,omitempty"`
	Calib  *float64 `json:"calib,omitempty"`
	CalibN *int     `json:"calib_n,omitempty"`

	Slices []forecastSlice `json:"slices,omitempty"`
}

// forecastSlice is one policy_version inside a cell. It has its own maturity and joins the cell's
// BH family, so a reader never compares two policies on a cell-level correction that does not
// include the split they introduced.
type forecastSlice struct {
	PolicyVersion string `json:"policy_version"`
	forecastCell
}

type forecastPeriod struct {
	Start string
	End   string
	Label string
}

// forecastMembership decides, per symbol, which accepted set is the group's member (D4).
//
// The earliest accepted set is the decision, and nothing promotes a later one: if that set is
// degraded, mismatched, or has no settled baseline, the symbol is out for the whole group. Later
// accepted sets are duplicates — visible, counted, never scored.
//
// `populationFrom` is the ledger start (P5, decisions V3). A zero time means "no boundary set": every
// set is live. A set made strictly before the boundary is pre-live and never enters bySymbol, so it
// can neither be a member nor make a later real set a duplicate (plan B4).
func forecastMembership(sets []*groupSet, populationFrom time.Time, bench string) (members, pendingBaseline []*groupSet, cov forecastCoverage) {
	cov.Reports = len(sets)
	cohortBench := make(map[string]string)
	for _, gs := range sets {
		cohortBench[gs.Cohort] = bench
	}
	for _, v := range classifyMembership(sets, populationFrom, cohortBench) {
		switch v.Status {
		case membershipPreLive:
			cov.PreLive++
		case predStateAbsent:
			cov.Absent++
		case predStateRejected:
			cov.Rejected++
		case predStateBacktest:
			cov.Backtest++
		case predStateTest:
			cov.Test++
		case membershipOffBenchmark:
			cov.OffBenchmark++
		case membershipDuplicate:
			cov.Duplicate++
		case membershipPendingBaseline:
			cov.PendingBaseline++
			pendingBaseline = append(pendingBaseline, v.Set)
		case membershipDegraded:
			cov.Degraded++
		case membershipMismatch:
			cov.Mismatch++
		case membershipMember:
			cov.Member++
			members = append(members, v.Set)
		default:
			cov.Unknown++
		}
	}
	return members, pendingBaseline, cov
}

// cohortBenchFromSets derives a cohort's benchmark from a loaded population: the frozen code of the
// earliest accepted set. The admin matrix uses the ledger-wide query instead, so a partial range
// cannot pick a different earliest set; the paths that already hold the whole cohort use this.
func cohortBenchFromSets(sets []*groupSet) string {
	var best *groupSet
	for _, gs := range sets {
		if gs.State != predStateAccepted {
			continue
		}
		if best == nil || gs.MadeAt < best.MadeAt || (gs.MadeAt == best.MadeAt && gs.ID < best.ID) {
			best = gs
		}
	}
	if best == nil {
		return ""
	}
	return best.BenchCode
}

const predQualityNormal = "normal"

// computeForecastCell applies the maturity gates and, only if all pass, the metrics.
func computeForecastCell(members, pendingBaseline []*groupSet, rtype, start, end, cp, today string) *forecastCell {
	return computeForecastCellForPopulation(members, pendingBaseline, rtype, start, end, cp, today, forecastPopulation{Key: "all"})
}

func computeForecastCellForPopulation(members, pendingBaseline []*groupSet, rtype, start, end, cp, today string, pop forecastPopulation) *forecastCell {
	if cp == "h12" {
		members = filterHorizonStd(members)
		pendingBaseline = filterHorizonStd(pendingBaseline)
	}
	state, n, refused, corp, resolved, total := forecastCellGate(members, pendingBaseline, cp, end, today)
	if state == "pending" {
		r, t := resolved, total
		return &forecastCell{State: state, Resolved: &r, Total: &t}
	}
	if state != "mature" {
		if state == "small" {
			return &forecastCell{State: state, N: &n}
		}
		return &forecastCell{State: state}
	}
	seedParts := pop.seedParts(rtype, start, end)
	cell := &forecastCell{State: "mature", N: &n, Refused: &refused, CorpAction: &corp}
	scored := make([]*groupSet, 0, n)
	for _, gs := range members {
		m := gs.Marks[cp]
		if m.Status == markResolved && !m.CorpAction {
			scored = append(scored, gs)
		}
	}
	scoreForecastCell(cell, scored, cp, seedParts)
	applyForecastBH(cell, cell.Slices)
	return cell
}

// forecastCellGate is the maturity gate without the scoring. computeForecastCell uses it before
// scoring; the members route uses it instead of the scorer, so opening a drawer never runs 10 000
// permutations to learn a state the matrix already computed.
func forecastCellGate(members, pendingBaseline []*groupSet, cp, end, today string) (state string, n, refused, corp, resolved, total int) {
	if end >= today {
		return "open", 0, 0, 0, 0, 0
	}
	total = len(members)
	blocked := false
	for _, gs := range members {
		m, ok := gs.Marks[cp]
		if !ok || m.Status == markPending || m.Status == markRefused {
			blocked = true
			continue
		}
		if m.Status == markResolved {
			resolved++
			if m.CorpAction {
				corp++
			}
		}
		if m.Status == markRefusedFinal {
			refused++
		}
	}
	if blocked || len(pendingBaseline) > 0 || (total > 0 && float64(refused)*100 > predRefusedMaxPct*float64(total)) {
		return "pending", 0, refused, corp, resolved, total
	}
	n = resolved - corp
	if n < predMatureMinN {
		return "small", n, refused, corp, resolved, total
	}
	return "mature", n, refused, corp, resolved, total
}

// spreadStat is the permutation statistic for the stance spread: the long arm's excess sum scaled
// into an excess mean and centred on the sample mean. It is a named function so the arithmetic (and
// the arch-determinism barrier below) has one home and one regression test.
//
// The explicit float64() around the product stops the compiler contracting `s*k - invc` into a fused
// multiply-add. Go permits that contraction on arm64 (and ppc64/s390x) but not on amd64, and the two
// round differently at the last bit, which would make a p-value differ between the multi-arch release
// image's amd64 build and its arm64 build. The barrier keeps the amd64 result and rounds arm64 to it.
func spreadStat(s, k, invc float64) float64 {
	return float64(s*k) - invc
}

func filterHorizonStd(in []*groupSet) []*groupSet {
	out := make([]*groupSet, 0, len(in))
	for _, gs := range in {
		if gs.HorizonStd {
			out = append(out, gs)
		}
	}
	return out
}

// scoreForecastCell fills every metric and, when members span more than one policy, the slices.
//
// The permutation loops are written around precomputed arrays rather than around the set structs:
// at 10 000 draws over 180 cells, a string comparison or an allocation inside the draw is what makes
// the endpoint miss its budget.
func scoreForecastCell(cell *forecastCell, sets []*groupSet, cp string, seedParts []string) {
	n := len(sets)
	excess := make([]float64, n)
	upside := make([]float64, n)
	longFlag := make([]bool, n)
	big := make([]bool, n)
	signUp := make([]int8, n)
	signEx := make([]int8, n)
	var icUp, icEx []float64

	for i, gs := range sets {
		m := gs.Marks[cp]
		if m.RetBp != nil && m.BenchRetBp != nil {
			excess[i] = float64(*m.RetBp - *m.BenchRetBp)
		}
		longFlag[i] = gs.Stance == "long"
		switch {
		case excess[i] > 0:
			signEx[i] = 1
		case excess[i] < 0:
			signEx[i] = -1
		}
		if gs.S0Fen != nil && *gs.S0Fen > 0 && gs.TerminalFen != nil {
			upside[i] = float64(*gs.TerminalFen)/float64(*gs.S0Fen) - 1
			// The hit subset and both signs come from the ONE predicate the drawer also uses, so a
			// row's verdict and the cell's hit rate cannot disagree at the 5% boundary. A member with
			// no benchmark excess is NOT tested: there is nothing to agree or disagree with.
			if m.RetBp != nil && m.BenchRetBp != nil {
				exBp := *m.RetBp - *m.BenchRetBp
				big[i] = predHitClass(*gs.TerminalFen, *gs.S0Fen, exBp) != dirUntested
				signUp[i] = signOfInt64(*gs.TerminalFen - *gs.S0Fen)
				signEx[i] = signOfInt32(exBp)
			}
			icUp = append(icUp, upside[i])
			icEx = append(icEx, excess[i])
		}
	}

	// Stance spread. sum(no_position) = total - sum(long), so one arm's sum is enough per draw.
	lc, nc := 0, 0
	var total float64
	for i := range sets {
		if longFlag[i] {
			lc++
			total += excess[i]
		} else if sets[i].Stance == "no_position" {
			nc++
			total += excess[i]
		}
	}
	if lc == 0 || nc == 0 {
		cell.SpreadOneArm = true
	} else {
		var sl float64
		for i := range sets {
			if longFlag[i] {
				sl += excess[i]
			}
		}
		obs := sl/float64(lc) - (total-sl)/float64(nc)
		cell.SpreadBp = &obs
		k := 1/float64(lc) + 1/float64(nc)
		invc := total / float64(nc)
		cell.SpreadP = float64Ptr(permP(predSeed(append(seedParts, "spread")...), obs, n,
			func(perm []int) float64 {
				var s float64
				for i := range perm {
					if longFlag[perm[i]] {
						s += excess[i]
					}
				}
				return spreadStat(s, k, invc)
			}))
	}

	// Rank IC, on ranks computed once.
	if len(icUp) >= 2 {
		rho, p := correlationPermP(predSeed(append(seedParts, "ic")...), averageRanks(icUp), averageRanks(icEx))
		if !math.IsNaN(rho) {
			cell.IC = &rho
			icN := len(icUp)
			cell.ICN = &icN
			cell.ICP = float64Ptr(p)
		}
	}

	// Directional hit rate on the subset with a real upside.
	hitObs, hitNull, hitN, hitOK := hitStats(big, signUp, signEx, nil)
	if hitOK {
		cell.Hit, cell.HitNull, cell.HitN = &hitObs, &hitNull, &hitN
		centered := hitObs - hitNull
		cell.HitP = float64Ptr(permP(predSeed(append(seedParts, "hit")...), centered, n,
			func(perm []int) float64 {
				h, nu, _, ok := hitStats(big, signUp, signEx, perm)
				if !ok {
					return math.NaN()
				}
				return h - nu
			}))
	}

	// Entry fills. Derived, and labelled so: the entry is a policy number, not a printed price.
	fillCount, entryCount := 0, 0
	var fillRet float64
	for _, gs := range sets {
		m := gs.Marks[cp]
		if gs.EntryFen == nil || *gs.EntryFen <= 0 || m.LowFen == nil || m.CloseFen == nil {
			continue
		}
		entryCount++
		if *m.LowFen <= *gs.EntryFen {
			fillCount++
			fillRet += float64(*m.CloseFen)/float64(*gs.EntryFen) - 1
		}
	}
	if entryCount > 0 {
		share := float64(fillCount) / float64(entryCount)
		cell.Fill = &share
		cell.FillDerived = true
		if fillCount > 0 {
			ret := fillRet / float64(fillCount) * 10000
			cell.FillRetBp = &ret
		}
	}

	if cp == "h12" {
		bandCount, bandTotal := 0, 0
		for _, gs := range sets {
			m := gs.Marks[cp]
			if gs.BearFen == nil || gs.BullFen == nil || m.CloseFen == nil {
				continue
			}
			bandTotal++
			if *gs.BearFen <= *m.CloseFen && *m.CloseFen <= *gs.BullFen {
				bandCount++
			}
		}
		if bandTotal > 0 {
			share := float64(bandCount) / float64(bandTotal)
			bn := bandTotal
			cell.Band, cell.BandN = &share, &bn
		}

		var probSum float64
		pos, calibTotal := 0, 0
		for _, gs := range sets {
			if gs.ProbabilityMethod == "approved_policy_default" || gs.ProbabilityTransform != "" || gs.PProfitBp == nil {
				continue
			}
			m := gs.Marks[cp]
			probSum += float64(*gs.PProfitBp) / 10000
			calibTotal++
			if m.RetBp != nil && *m.RetBp > 0 {
				pos++
			}
		}
		if calibTotal > 0 {
			mean := probSum / float64(calibTotal)
			cn := calibTotal
			cell.Calib, cell.CalibN = &mean, &cn
		}
	}

	// Policy slices, only where the members actually span more than one policy. A slice that cannot
	// publish its own n is shown as small and carries no metrics, exactly like a cell.
	policies := map[string][]*groupSet{}
	for _, gs := range sets {
		if gs.PolicyVersion == "" {
			continue
		}
		policies[gs.PolicyVersion] = append(policies[gs.PolicyVersion], gs)
	}
	if len(policies) > 1 {
		keys := make([]string, 0, len(policies))
		for k := range policies {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			sub := policies[k]
			if len(sub) == 0 {
				continue
			}
			if len(sub) < predMatureMinN {
				sn := len(sub)
				cell.Slices = append(cell.Slices, forecastSlice{
					PolicyVersion: k,
					forecastCell:  forecastCell{State: "small", N: &sn},
				})
				continue
			}
			sc := subscores(sub, cp, seedParts)
			cell.Slices = append(cell.Slices, forecastSlice{PolicyVersion: k, forecastCell: *sc})
		}
	}
}

// subscores computes a slice's metrics without recursing into slices of its own.
func subscores(sets []*groupSet, cp string, seedParts []string) *forecastCell {
	c := &forecastCell{State: "mature"}
	n := len(sets)
	c.N = &n
	scoreForecastCell(c, sets, cp, append(seedParts, "slice"))
	c.Slices = nil
	return c
}

// hitStats returns the hit rate, its base-rate null and the subset size. When perm is non-nil each
// member's upside is read from another member while its own excess stays fixed.
func hitStats(big []bool, signUp, signEx []int8, perm []int) (hit, null float64, n int, ok bool) {
	agree, pos, neg := 0, 0, 0
	for i := range big {
		j := i
		if perm != nil {
			j = perm[i]
		}
		if !big[j] {
			continue
		}
		n++
		switch signEx[i] {
		case 1:
			pos++
		case -1:
			neg++
		}
		if signEx[i] != 0 && signEx[i] == signUp[j] {
			agree++
		}
	}
	if n == 0 {
		return 0, 0, 0, false
	}
	hit = float64(agree) / float64(n)
	null = math.Max(float64(pos)/float64(n), float64(neg)/float64(n))
	return hit, null, n, true
}

// applyForecastBH corrects the cell's metrics and every slice's together, because a slice is part of
// the same family: splitting a cell by policy is a hypothesis the cell's correction must pay for.
func applyForecastBH(cell *forecastCell, slices []forecastSlice) {
	var ps []float64
	collect := func(c *forecastCell) {
		ps = append(ps, derefOrNaN(c.SpreadP), derefOrNaN(c.ICP), derefOrNaN(c.HitP))
	}
	collect(cell)
	for i := range slices {
		collect(&slices[i].forecastCell)
	}
	qs := bhAdjust(ps)
	i := 0
	assign := func(c *forecastCell) {
		if !math.IsNaN(qs[i]) {
			c.SpreadQ = float64Ptr(qs[i])
		}
		i++
		if !math.IsNaN(qs[i]) {
			c.ICQ = float64Ptr(qs[i])
		}
		i++
		if !math.IsNaN(qs[i]) {
			c.HitQ = float64Ptr(qs[i])
		}
		i++
	}
	assign(cell)
	for j := range slices {
		assign(&slices[j].forecastCell)
	}
}

func derefOrNaN(p *float64) float64 {
	if p == nil {
		return math.NaN()
	}
	return *p
}

func float64Ptr(v float64) *float64 { return &v }

// forecastPeriods cuts [from, to] into groups. The monthly mode is separate because its range is
// decided by the ledger and the calendar, not by the caller.
func forecastPeriods(group, from, to string) []forecastPeriod {
	f, _ := time.ParseInLocation("2006-01-02", from, chinaZone)
	t, _ := time.ParseInLocation("2006-01-02", to, chinaZone)
	day := func(x time.Time) string { return x.Format("2006-01-02") }

	switch group {
	case "all":
		return []forecastPeriod{{Start: from, End: to, Label: from + " – " + to}}
	case "week":
		var out []forecastPeriod
		mon := f
		for mon.Weekday() != time.Monday {
			mon = mon.AddDate(0, 0, -1)
		}
		for !mon.After(t) {
			sun := mon.AddDate(0, 0, 6)
			s, e := mon, sun
			if s.Before(f) {
				s = f
			}
			if e.After(t) {
				e = t
			}
			label := "W" + isoWeekLabel(mon)
			if s != mon || e != sun {
				label = day(s) + " – " + day(e)
			}
			out = append(out, forecastPeriod{Start: day(s), End: day(e), Label: label})
			mon = mon.AddDate(0, 0, 7)
		}
		return out
	case "quarter":
		var out []forecastPeriod
		q := time.Date(f.Year(), time.Month((int(f.Month())-1)/3*3+1), 1, 0, 0, 0, 0, chinaZone)
		for !q.After(t) {
			qe := q.AddDate(0, 3, -1)
			s, e := q, qe
			if s.Before(f) {
				s = f
			}
			if e.After(t) {
				e = t
			}
			label := q.Format("2006") + "-Q" + string(rune('1'+(int(q.Month())-1)/3))
			if s != q || e != qe {
				label = day(s) + " – " + day(e)
			}
			out = append(out, forecastPeriod{Start: day(s), End: day(e), Label: label})
			q = q.AddDate(0, 3, 0)
		}
		return out
	default: // month
		var out []forecastPeriod
		m := time.Date(f.Year(), f.Month(), 1, 0, 0, 0, 0, chinaZone)
		for !m.After(t) {
			me := m.AddDate(0, 1, -1)
			s, e := m, me
			if s.Before(f) {
				s = f
			}
			if e.After(t) {
				e = t
			}
			label := m.Format("2006-01")
			if s != m || e != me {
				label = day(s) + " – " + day(e)
			}
			out = append(out, forecastPeriod{Start: day(s), End: day(e), Label: label})
			m = m.AddDate(0, 1, 0)
		}
		return out
	}
}

func isoWeekLabel(t time.Time) string {
	y, w := t.ISOWeek()
	return fmt.Sprintf("%04d-W%02d", y, w)
}

// monthlyPeriods is the official range: every month from the ledger's first to the current one.
func monthlyPeriods(earliest string, now time.Time) []forecastPeriod {
	if earliest == "" {
		return nil
	}
	first, err := time.ParseInLocation("2006-01-02", earliest, chinaZone)
	if err != nil {
		return nil
	}
	last := now.In(chinaZone)
	m := time.Date(first.Year(), first.Month(), 1, 0, 0, 0, 0, chinaZone)
	var out []forecastPeriod
	for !m.After(last) {
		out = append(out, forecastPeriod{
			Start: m.Format("2006-01-02"),
			End:   m.AddDate(0, 1, -1).Format("2006-01-02"),
			Label: m.Format("2006-01"),
		})
		m = m.AddDate(0, 1, 0)
	}
	return out
}

// validateForecastRange applies the custom-range bounds. Every refusal names its reason: a caller
// that asked for 40 months can see that and narrow the range, rather than reading an empty page.
func validateForecastRange(from, to, group string) (string, bool) {
	switch group {
	case "week", "month", "quarter", "all":
	default:
		return "bad_group", false
	}
	if from == "" || to == "" {
		return "bad_date", false
	}
	f, err1 := time.ParseInLocation("2006-01-02", from, chinaZone)
	t, err2 := time.ParseInLocation("2006-01-02", to, chinaZone)
	if err1 != nil || err2 != nil {
		return "bad_date", false
	}
	if f.After(t) {
		return "range_inverted", false
	}
	if t.After(f.AddDate(0, predMaxRangeMonths, 0)) {
		return "range_too_long", false
	}
	if group == "week" {
		days := int(t.Sub(f)/(24*time.Hour)) + 1
		if days > predMaxWeekGroups*7 {
			return "week_range_too_long", false
		}
	}
	return "", true
}

func forecastRangeMessage(code string) string {
	switch code {
	case "bad_group":
		return "group must be week, month, quarter or all"
	case "bad_date":
		return "from and to must be YYYY-MM-DD"
	case "range_inverted":
		return "from is after to"
	case "range_too_long":
		return "the range spans more than 36 months"
	case "week_range_too_long":
		return "week grouping is limited to 26 weeks"
	}
	return code
}

// correlationPermP is the IC p-value without the per-draw sort: both rank vectors are ranked once,
// and the correlation's means and variances do not change under a permutation, so each draw is one
// O(n) dot product.
func correlationPermP(seed int64, rx, ry []float64) (float64, float64) {
	n := len(rx)
	if n < 2 || n != len(ry) {
		return math.NaN(), math.NaN()
	}
	mx, my := meanOf(rx), meanOf(ry)
	var sxx, syy, sxy float64
	for i := 0; i < n; i++ {
		dx, dy := rx[i]-mx, ry[i]-my
		sxx += float64(dx * dx)
		syy += float64(dy * dy)
		sxy += float64(dx * dy)
	}
	if sxx == 0 || syy == 0 {
		return math.NaN(), math.NaN()
	}
	sd := math.Sqrt(sxx * syy)
	rho := sxy / sd
	if math.IsNaN(rho) {
		return math.NaN(), math.NaN()
	}
	cx := make([]float64, n)
	dy := make([]float64, n)
	for i := 0; i < n; i++ {
		cx[i] = rx[i] - mx
		dy[i] = ry[i] - my
	}
	r := newPredRand(seed)
	perm := make([]int, n)
	for i := range perm {
		perm[i] = i
	}
	absRho := math.Abs(rho)
	extreme := 0
	for b := 0; b < predPermutations; b++ {
		r.shuffle(perm)
		var s float64
		for i := 0; i < n; i++ {
			s += float64(cx[perm[i]] * dy[i])
		}
		if math.Abs(s/sd) >= absRho {
			extreme++
		}
	}
	return rho, float64(1+extreme) / float64(1+predPermutations)
}
