package app

// The group statistics route (plan §7.1).
//
// It is under /api/admin and gated by PermManage, not on /api/v1. v1 authorizes by token scope
// alone, and the app bridge mints query tokens for every unrestricted user, so a v1 route would
// hand the whole ledger's aggregate to any bridge app. This is a management view.

import (
	"net/http"
	"runtime"
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

	now := time.Now()
	resp := forecastGroupsResponse{
		OK:          true,
		Mode:        mode,
		Checkpoints: append([]string{}, predCheckpoints...),
	}
	unrecorded, err := s.st.countForecastUnrecorded(rtype)
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
	resp.Groups = s.buildForecastGroups(periods, sets, rtype, today, resp.Official, populationFrom)
	writeJSON(w, resp)
}

// buildForecastGroups computes every group x checkpoint. The cells are independent — each one's
// result depends only on its own members and its own seed — so they run on a worker pool. At the
// maximum bounds that is 180 cells of 10 000 permutations each; sequentially it misses the plan's
// three-second budget, and there is nothing to serialise.
func (s *Server) buildForecastGroups(periods []forecastPeriod, sets []*groupSet, rtype, today string, official bool, populationFrom time.Time) []forecastGroup {
	type job struct {
		gi      int
		cp      string
		start   string
		end     string
		members []*groupSet
		pending []*groupSet
	}
	groups := make([]forecastGroup, len(periods))
	jobs := make([]job, 0, len(periods)*len(predCheckpoints))
	for i, p := range periods {
		in := make([]*groupSet, 0, 64)
		for _, gs := range sets {
			if gs.MadeOn >= p.Start && gs.MadeOn <= p.End {
				in = append(in, gs)
			}
		}
		members, pending, cov := forecastMembership(in, populationFrom)
		groups[i] = forecastGroup{
			Label:    p.Label,
			Start:    p.Start,
			End:      p.End,
			Official: official,
			Coverage: cov,
			Cells:    make(map[string]*forecastCell, len(predCheckpoints)),
		}
		for _, cp := range predCheckpoints {
			jobs = append(jobs, job{gi: i, cp: cp, start: p.Start, end: p.End, members: members, pending: pending})
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
				key := forecastGroupCellKey(rtype, j.start, j.end, j.cp)
				if c := s.cachedForecastCell(key); c != nil {
					out <- result{j.gi, j.cp, c}
					continue
				}
				c := computeForecastCell(j.members, j.pending, rtype, j.start, j.end, j.cp, today)
				// Only the official view writes: an exploratory range must not seed the record,
				// and a custom group that equals a whole month already hits the monthly key.
				if official && c.State == "mature" {
					s.storeForecastCell(key, c)
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
func forecastGroupCellKey(rtype, start, end, cp string) string {
	return rtype + "\x00" + start + "\x00" + end + "\x00" + cp
}

func (s *Server) cachedForecastCell(key string) *forecastCell {
	s.forecastGroupsMu.Lock()
	defer s.forecastGroupsMu.Unlock()
	return s.forecastGroupsCache[key]
}

func (s *Server) storeForecastCell(key string, c *forecastCell) {
	s.forecastGroupsMu.Lock()
	defer s.forecastGroupsMu.Unlock()
	if s.forecastGroupsCache == nil {
		s.forecastGroupsCache = map[string]*forecastCell{}
	}
	s.forecastGroupsCache[key] = c
}
