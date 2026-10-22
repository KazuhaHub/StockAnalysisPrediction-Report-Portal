package app

// The group statistics route (plan §7.1).
//
// It is under /api/admin and gated by PermManage, not on /api/v1. v1 authorizes by token scope
// alone, and the app bridge mints query tokens for every unrestricted user, so a v1 route would
// hand the whole ledger's aggregate to any bridge app. This is a management view.

import (
	"log"
	"net/http"
	"runtime"
	"sort"
	"sync"
	"time"
)

func (s *Server) apiForecastGroups(w http.ResponseWriter, r *http.Request, user string) {
	q := r.URL.Query()
	mode := q.Get("mode")
	if mode == "" {
		mode = "month"
	}
	rtype := q.Get("rtype")
	if rtype == "" {
		rtype = predTestRtype
	}
	if !predictionCovered(rtype) {
		jsonErrorCode(w, http.StatusBadRequest, "bad_rtype", "rtype is not covered by the ledger")
		return
	}

	now := s.forecastNow()
	resp := forecastGroupsResponse{
		OK:          true,
		Mode:        mode,
		Checkpoints: append([]string{}, predCheckpoints...),
	}
	// The admin alias keeps its bounded unrecorded integer until P29; it is 0 while the ledger start
	// is unset (the admin GET's `unrecorded` list is where the "set the ledger start" state is shown).
	unrecorded, _, _, err := s.st.countForecastUnrecorded(rtype)
	if err != nil {
		jsonErrorCode(w, http.StatusInternalServerError, "db_error", "database error")
		return
	}
	resp.Unrecorded = unrecorded

	var periods []forecastPeriod
	loadFrom, loadTo := "", ""

	switch mode {
	case "month":
		// Monthly is the record: its range is the calendar's, so a range parameter is a mistake the
		// caller must see, not something to ignore.
		if q.Get("from") != "" || q.Get("to") != "" || q.Get("group") != "" {
			jsonErrorCode(w, http.StatusBadRequest, "month_takes_no_range", "month mode takes no from, to or group")
			return
		}
		earliest, err := s.st.forecastEarliestMadeOn(rtype)
		if err != nil {
			jsonErrorCode(w, http.StatusInternalServerError, "db_error", "database error")
			return
		}
		periods = monthlyPeriods(earliest, now)
		if len(periods) > 0 {
			loadFrom, loadTo = periods[0].Start, periods[len(periods)-1].End
			resp.Range.From, resp.Range.To = loadFrom, loadTo
		}
	case "custom":
		from, to := q.Get("from"), q.Get("to")
		group := q.Get("group")
		if group == "" {
			group = "month"
		}
		if code, ok := validateForecastRange(from, to, group); !ok {
			jsonErrorCode(w, http.StatusBadRequest, code, forecastRangeMessage(code))
			return
		}
		periods = forecastPeriods(group, from, to)
		loadFrom, loadTo = from, to
		resp.Range = forecastRange{From: from, To: to, Group: group}
		resp.Official = false
		resp.Groups = []forecastGroup{}
	default:
		jsonErrorCode(w, http.StatusBadRequest, "bad_mode", "mode must be month or custom")
		return
	}

	resp.Official = mode == "month"
	purges, err := s.st.loadPredictionPurgeMarkers()
	if err != nil {
		jsonErrorCode(w, http.StatusInternalServerError, "db_error", "database error")
		return
	}
	if resp.Official {
		earliest := forecastEarliestWithPurges(loadFrom, rtype, purges)
		periods = monthlyPeriods(earliest, now)
		if len(periods) > 0 {
			loadFrom, loadTo = periods[0].Start, periods[len(periods)-1].End
			resp.Range.From, resp.Range.To = loadFrom, loadTo
		}
	} else {
		resp.ExcludedMonths = forecastExcludedMonths(rtype, loadFrom, loadTo, purges)
	}
	today := now.In(chinaZone).Format("2006-01-02")

	var sets []*groupSet
	if loadFrom != "" && loadTo != "" {
		sets, err = s.st.loadForecastGroupSets(rtype, loadFrom, loadTo)
		if err != nil {
			jsonErrorCode(w, http.StatusInternalServerError, "db_error", "database error")
			return
		}
	}

	populationFrom, _ := s.st.ForecastPopulationFrom()
	sets = forecastWithoutPurgedSets(sets, rtype, purges)
	active := make([]forecastPeriod, 0, len(periods))
	for _, p := range periods {
		if _, purged := purges[[2]string{rtype, p.Start[:7]}]; !resp.Official || !purged {
			active = append(active, p)
		}
	}
	resp.Groups = s.buildForecastGroupsWithCache(active, sets, rtype, today, resp.Official, populationFrom, len(resp.ExcludedMonths) == 0)
	if resp.Official {
		for _, p := range periods {
			if at, purged := purges[[2]string{rtype, p.Start[:7]}]; purged {
				g := forecastGroup{Label: p.Label, Start: p.Start, End: p.End, Official: true, PurgedAt: at,
					Cells: make(map[string]*forecastCell, len(predCheckpoints))}
				for _, cp := range predCheckpoints {
					g.Cells[cp] = &forecastCell{State: "purged"}
				}
				resp.Groups = append(resp.Groups, g)
			}
		}
		sort.SliceStable(resp.Groups, func(i, j int) bool { return resp.Groups[i].Start < resp.Groups[j].Start })
	}
	writeJSON(w, resp)
}

// buildForecastGroups computes every group x checkpoint. The cells are independent — each one's
// result depends only on its own members and its own seed — so they run on a worker pool. At the
// maximum bounds that is 180 cells of 10 000 permutations each; sequentially it misses the plan's
// three-second budget, and there is nothing to serialise.
func (s *Server) buildForecastGroups(periods []forecastPeriod, sets []*groupSet, rtype, today string, official bool, populationFrom time.Time) []forecastGroup {
	return s.buildForecastGroupsWithCache(periods, sets, rtype, today, official, populationFrom, true)
}

// Exploratory ranges containing removed cohorts cannot reuse an old full-population cell.
func (s *Server) buildForecastGroupsWithCache(periods []forecastPeriod, sets []*groupSet, rtype, today string, official bool, populationFrom time.Time, useCache bool) []forecastGroup {
	// The cohort's benchmark is the frozen code of its earliest in-population accepted set — a
	// ledger-wide answer, not just what this range happened to load. A custom range that starts
	// mid-cohort must not pick a later set and call the cohort something else.
	benchOf := map[string]string{}
	for _, gs := range sets {
		if _, ok := benchOf[gs.Cohort]; ok {
			continue
		}
		b, err := s.st.earliestCohortBench(rtype, gs.Cohort)
		if err != nil {
			log.Printf("forecast groups: cohort %s benchmark: %v", gs.Cohort, err)
			b = ""
		}
		benchOf[gs.Cohort] = b
	}

	// A period that spans cohorts with different benchmarks is split, so no cell pools two indexes.
	type plan struct {
		period forecastPeriod
		bench  string
		in     []*groupSet
	}
	plans := make([]plan, 0, len(periods))
	for _, p := range periods {
		in := make([]*groupSet, 0, 64)
		for _, gs := range sets {
			if gs.MadeOn >= p.Start && gs.MadeOn <= p.End {
				in = append(in, gs)
			}
		}
		byBench := map[string][]*groupSet{}
		for _, gs := range in {
			byBench[benchOf[gs.Cohort]] = append(byBench[benchOf[gs.Cohort]], gs)
		}
		if len(byBench) <= 1 {
			b := ""
			for k := range byBench {
				b = k
			}
			plans = append(plans, plan{p, b, in})
			continue
		}
		codes := make([]string, 0, len(byBench))
		for b := range byBench {
			codes = append(codes, b)
		}
		sort.Strings(codes)
		for _, b := range codes {
			plans = append(plans, plan{
				period: forecastPeriod{Start: p.Start, End: p.End, Label: p.Label + " · " + b},
				bench:  b,
				in:     byBench[b],
			})
		}
	}

	type job struct {
		gi      int
		cp      string
		start   string
		end     string
		bench   string
		members []*groupSet
		pending []*groupSet
	}
	groups := make([]forecastGroup, len(plans))
	jobs := make([]job, 0, len(plans)*len(predCheckpoints))
	for i, pl := range plans {
		members, pending, cov := forecastMembership(pl.in, populationFrom, pl.bench)
		groups[i] = forecastGroup{
			Label:    pl.period.Label,
			Start:    pl.period.Start,
			End:      pl.period.End,
			Official: official,
			Bench:    pl.bench,
			Coverage: cov,
			Cells:    make(map[string]*forecastCell, len(predCheckpoints)),
		}
		for _, cp := range predCheckpoints {
			jobs = append(jobs, job{gi: i, cp: cp, start: pl.period.Start, end: pl.period.End,
				bench: pl.bench, members: members, pending: pending})
		}
	}
	if len(jobs) == 0 {
		return groups
	}

	type result struct {
		gi   int
		cp   string
		cell *forecastCell
	}
	workers := runtime.GOMAXPROCS(0)
	if workers > len(jobs) {
		workers = len(jobs)
	}
	ch := make(chan job)
	out := make(chan result, len(jobs))
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range ch {
				key := forecastGroupCellKey(rtype, j.start, j.end, j.bench, j.cp)
				c, gen := s.cachedForecastCell(key)
				if useCache && c != nil {
					out <- result{j.gi, j.cp, c}
					continue
				}
				c = computeForecastCell(j.members, j.pending, rtype, j.start, j.end, j.cp, today)
				if official && c.State == "mature" {
					s.storeForecastCell(key, c, gen)
				}
				out <- result{j.gi, j.cp, c}
			}
		}()
	}
	go func() {
		for _, j := range jobs {
			ch <- j
		}
		close(ch)
		wg.Wait()
		close(out)
	}()
	for r := range out {
		groups[r.gi].Cells[r.cp] = r.cell
	}
	return groups
}

// forecastGroupCellKey is the memo's identity. It deliberately has no `mode`: a whole-month custom
// group must hit the monthly entry, which is what makes the two answers identical by construction.
func forecastGroupCellKey(rtype, start, end, bench, cp string) string {
	return rtype + "\x00" + start + "\x00" + end + "\x00" + bench + "\x00" + cp
}

// cachedForecastCell returns the cell and the generation it was read at. A miss still returns the
// generation, so a caller can compute and then store only if no purge intervened.
func (s *Server) cachedForecastCell(key string) (*forecastCell, uint64) {
	s.forecastGroupsMu.Lock()
	defer s.forecastGroupsMu.Unlock()
	return s.forecastGroupsCache[key], s.forecastGroupsGen
}

// storeForecastCell drops a result computed across a purge: a pre-delete cell must not repopulate
// the cache after the delete has invalidated it.
func (s *Server) storeForecastCell(key string, c *forecastCell, gen uint64) {
	s.forecastGroupsMu.Lock()
	defer s.forecastGroupsMu.Unlock()
	if gen != s.forecastGroupsGen {
		return
	}
	if s.forecastGroupsCache == nil {
		s.forecastGroupsCache = map[string]*forecastCell{}
	}
	s.forecastGroupsCache[key] = c
}

// purgeForecastGroups drops every memoized cell and invalidates any computation already in flight.
func (s *Server) purgeForecastGroups() {
	s.forecastGroupsMu.Lock()
	defer s.forecastGroupsMu.Unlock()
	s.forecastGroupsGen++
	s.forecastGroupsCache = nil
}
