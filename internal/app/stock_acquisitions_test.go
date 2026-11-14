package app

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"testing"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/config"
)

// stockSpyServer is a server over a spy-driver store with a few readers and a symbol that has
// several dates and report types, so the handler walks every branch of its reads.
func stockSpyServer(t *testing.T) (*Server, *spyDriver, http.Handler) {
	t.Helper()
	st, d := newSpyTestStore(t)
	s := &Server{st: st, cfg: &config.Config{SecretKey: "0123456789abcdef0123456789abcdef"}}
	st.SetSetting("public_url", "https://portal.example")
	s.names = LoadNames(t.TempDir(), st)
	st.UpsertUser(User{Username: "alice", PasswordHash: "h", Role: "user"})
	st.UpsertUser(User{Username: "boss", PasswordHash: "h", Role: "admin"})
	for d := 0; d < 6; d++ {
		for k := 0; k < 3; k++ {
			st.UpsertReport(Rep{Symbol: "600519", Date: fmt.Sprintf("2026-06-%02d", d+1), RType: fmt.Sprintf("类型%d", k),
				Title: fmt.Sprintf("600519 类型%d", k), MD: "# 正文\n\n内容 " + fmt.Sprint(d, k)})
		}
	}
	mux := http.NewServeMux()
	s.wireRoutes(mux)
	return s, d, mux
}

// apiStockReference is the handler as it was before its reads were grouped, kept verbatim as the
// reference the grouped one must match byte for byte. It reads through the same store helpers.
func (s *Server) apiStockReference(w http.ResponseWriter, r *http.Request, user string) {
	symbol := r.PathValue("symbol")
	all, _ := s.st.NewBySymbol(symbol, s.viewerScope(user))
	all = dropInternal(all) // cache/plumbing entries are not reports; see internalTypes
	if len(all) == 0 {
		jsonErrorCode(w, http.StatusNotFound, "no_reports_for_symbol", "该标的暂无报告")
		return
	}
	// Newest date first; keep a stable order within a date by ingest/report time.
	sort.SliceStable(all, func(i, j int) bool {
		if all[i].Date != all[j].Date {
			return all[i].Date > all[j].Date
		}
		return all[i].Time < all[j].Time
	})
	q := r.URL.Query()
	// Resolve the deep link only within the already-scoped symbol list. A readable report ID
	// selects its own date and category, even when the URL carries stale navigation parameters.
	selID, _ := strconv.ParseInt(q.Get("r"), 10, 64)
	linkedDate, linkedKind := "", ""
	for _, m := range all {
		if m.ID == selID {
			linkedDate, linkedKind = m.Date, repKind(m)
			break
		}
	}
	var order []string // rdate DESC → newest to oldest
	byDate := map[string][]Rep{}
	for _, m := range all {
		if _, ok := byDate[m.Date]; !ok {
			order = append(order, m.Date)
		}
		byDate[m.Date] = append(byDate[m.Date], m)
	}
	selDate := q.Get("date")
	if linkedDate != "" {
		selDate = linkedDate
	}
	if _, ok := byDate[selDate]; !ok {
		selDate = order[0]
	}
	dateReps := byDate[selDate]
	kindSet := map[string]bool{}
	for _, m := range dateReps {
		kindSet[repKind(m)] = true
	}
	var kinds []string
	for _, k := range kindOrder {
		if kindSet[k] {
			kinds = append(kinds, k)
			delete(kindSet, k)
		}
	}
	for k := range kindSet {
		kinds = append(kinds, k)
	}
	selKind := q.Get("kind")
	if linkedKind != "" {
		selKind = linkedKind
	}
	if !containsStr(kinds, selKind) {
		selKind = kinds[0]
	}
	var kindReps []Rep
	for _, m := range dateReps {
		if repKind(m) == selKind {
			kindReps = append(kindReps, m)
		}
	}
	for i := range kindReps {
		kindReps[i].Label = tabLabel(kindReps[i])
	}
	kindReps, defID := s.orderAndDefault(kindReps)
	if !repInList(kindReps, selID) {
		selID = defID
	}
	rep := s.loadRep(r, user, selID)
	predictedDates := s.forecastTimelineDates(user, symbol, all)
	timeline := make([]map[string]any, 0, len(order)) // newest to oldest
	for _, d := range order {
		node := map[string]any{"date": d, "n": len(byDate[d])}
		if predictedDates != nil {
			node["predicted"] = predictedDates[d]
		}
		timeline = append(timeline, node)
	}
	subtabs := make([]map[string]any, 0, len(kindReps))
	for _, m := range kindReps {
		subtabs = append(subtabs, map[string]any{"id": m.ID, "label": m.Label, "rtype": m.RType,
			// A tab names the type and generator version, so the report it opens is named here
			// instead: the strip shows this on hover. Same string as the reader heading, so
			// pointing at a tab and opening it agree.
			"title": s.repDisplayTitle(&m)})
	}
	out := map[string]any{
		"symbol": symbol, "market": marketPrefix(symbol), "name": s.names.Get(symbol),
		"selDate": selDate, "selKind": selKind, "selId": selID,
		"timeline": timeline, "kinds": kinds, "subtabs": subtabs,
		"rep": repJSON(rep, s.names.Get),
	}
	if prediction := s.forecastReadingSummary(r, user, rep); prediction != nil {
		out["prediction"] = prediction
	}
	writeJSON(w, out)
}

func stockCall(h func(http.ResponseWriter, *http.Request, string), user, query string) (int, string) {
	req := httptest.NewRequest("GET", "/api/stock/600519"+query, nil)
	req.SetPathValue("symbol", "600519")
	rec := httptest.NewRecorder()
	h(rec, req, user)
	return rec.Code, rec.Body.String()
}

func TestStockPageGroupsItsReads(t *testing.T) {
	s, d, _ := stockSpyServer(t)
	s.st.UpsertReport(Rep{Symbol: "600519", Date: "2026-06-03", RType: "类型0", Version: "manual", Title: "600519 类型0", MD: "人工版"})
	queries := []string{"", "?date=2026-06-03", "?date=2026-06-02&kind=" + kindOrder[0], "?date=1999-01-01", "?kind=nope", "?r=1", "?r=999999", "?r=" + strconv.Itoa(7) + "&date=2026-06-01"}
	var refReads, newReads int64
	for _, user := range []string{"alice", "boss", ""} {
		for _, q := range queries {
			// Warm both, so neither run is the first to touch lazily loaded state.
			stockCall(s.apiStockReference, user, q)
			stockCall(s.apiStock, user, q)

			before := d.releases.Load()
			wantCode, want := stockCall(s.apiStockReference, user, q)
			refReads += d.releases.Load() - before
			before = d.releases.Load()
			gotCode, got := stockCall(s.apiStock, user, q)
			newReads += d.releases.Load() - before
			if gotCode != wantCode || got != want {
				t.Fatalf("user=%q query=%q: response differs\nwant %d %s\n got %d %s", user, q, wantCode, want, gotCode, got)
			}
		}
	}
	t.Logf("connection acquisitions over %d requests: reference %d, grouped %d", 3*len(queries), refReads, newReads)
	if newReads >= refReads {
		t.Fatalf("grouped handler took %d acquisitions, reference %d", newReads, refReads)
	}
	// A page that finds a report: the reader's scope, one read snapshot, the read audit and the
	// forecast-access check. It was nine.
	stockCall(s.apiStock, "alice", "")
	before := d.releases.Load()
	if code, _ := stockCall(s.apiStock, "alice", "?date=2026-06-03"); code != 200 {
		t.Fatal(code)
	}
	got := d.releases.Load() - before
	before = d.releases.Load()
	stockCall(s.apiStockReference, "alice", "?date=2026-06-03")
	t.Logf("a page that finds a report: reference %d acquisitions, grouped %d", d.releases.Load()-before, got)
	if got > 4 {
		t.Fatalf("a page that finds a report took %d acquisitions, want at most 4", got)
	}
}
