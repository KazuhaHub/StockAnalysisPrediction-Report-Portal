package app

// Group statistics tests (plan §7.8, design §11 #13 and #16–18, #23–26).
//
// The fixtures are inserted directly rather than ingested: the ingest path has its own tests, and
// these are about the arithmetic and the gates, which need exact control over n, refusals and marks.

import (
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

type gsMark struct {
	Status     string
	CorpAction bool
	Close      *int64
	Low        *int64
	RetBp      *int32
	BenchRetBp *int32
}

type gsSeed struct {
	Symbol      string
	MadeOn      string
	MadeAt      string
	State       string
	Quality     string
	BaseCheck   string
	Policy      string
	Method      string
	Transform   string
	HorizonStd  bool
	S0Fen       *int64
	Stance      string
	TerminalFen *int64
	EntryFen    *int64
	BearFen     *int64
	BullFen     *int64
	PProfitBp   *int32
	Marks       map[string]gsMark
}

func ip(v int64) *int64 { return &v }
func bp(v int32) *int32 { return &v }

// seedGroupSet inserts one set with claims and marks, defaulting the harmless fields.
func seedGroupSet(t *testing.T, s *Server, g gsSeed) int64 {
	t.Helper()
	if g.State == "" {
		g.State = predStateAccepted
	}
	if g.Quality == "" {
		g.Quality = predQualityNormal
	}
	if g.BaseCheck == "" {
		g.BaseCheck = baseMatch
	}
	if g.MadeAt == "" {
		g.MadeAt = g.MadeOn + "T10:00:00+08:00"
	}
	if g.Policy == "" {
		g.Policy = "v3config-1"
	}
	if g.Method == "" {
		g.Method = "approved_policy_default"
	}
	if g.Stance == "" {
		g.Stance = "long"
	}
	res, err := s.st.exec(s.st.bind(`INSERT INTO prediction_sets(
		report_id, symbol, rtype, version, title, report_date, made_at, made_on, cohort, zone, state,
		policy_version, probability_method, probability_transform, decision_quality, horizon_std,
		s0_fen, base_check)
		VALUES(NULL,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`),
		g.Symbol, predTestRtype, "", "", g.MadeOn, g.MadeAt, g.MadeOn, g.MadeOn[:7], predZone,
		g.State, g.Policy, g.Method, g.Transform, g.Quality, boolInt(g.HorizonStd), g.S0Fen, g.BaseCheck)
	if err != nil {
		t.Fatalf("insert set: %v", err)
	}
	id, _ := res.LastInsertId()

	insClaim := func(key, kind, stance, label string, price *int64, prob *int32) {
		if _, err := s.st.exec(s.st.bind(`INSERT INTO prediction_claims(set_id, claim_key, kind, stance, label, price_fen, prob_bp)
			VALUES(?,?,?,?,?,?,?)`), id, key, kind, stance, label, price, prob); err != nil {
			t.Fatalf("insert claim: %v", err)
		}
	}
	insClaim("stance", kindStance, g.Stance, "", nil, nil)
	insClaim("expected_terminal", kindTerminal, "", "", g.TerminalFen, g.PProfitBp)
	insClaim("scenario.bear", kindScenario, "", "bear", g.BearFen, nil)
	insClaim("scenario.bull", kindScenario, "", "bull", g.BullFen, nil)
	if g.EntryFen != nil {
		insClaim("entry_price", kindEntry, "", "", g.EntryFen, nil)
	}
	for cp, m := range g.Marks {
		if _, err := s.st.exec(s.st.bind(`INSERT INTO prediction_marks(
			set_id, checkpoint, status, corp_action, close_fen, low_fen, ret_bp, bench_ret_bp)
			VALUES(?,?,?,?,?,?,?,?)`), id, cp, m.Status, boolInt(m.CorpAction), m.Close, m.Low, m.RetBp, m.BenchRetBp); err != nil {
			t.Fatalf("insert mark: %v", err)
		}
	}
	return id
}

// sampleMembers seeds n members in one month, all resolved at d5. The default has a positive excess
// that rises with the downside, so the metrics are not degenerate.
func sampleMembers(t *testing.T, s *Server, n int, mutate func(i int, g *gsSeed)) []*groupSet {
	t.Helper()
	for i := 0; i < n; i++ {
		sym := symbolFor(i)
		g := gsSeed{
			Symbol: sym, MadeOn: "2025-06-10",
			S0Fen: ip(10000), TerminalFen: ip(int64(11000 + i*10)), EntryFen: ip(10500),
			BearFen: ip(8000), BullFen: ip(14000), PProfitBp: bp(int32(6000 + i%10)),
			Marks: map[string]gsMark{
				"d5": {Status: markResolved, Close: ip(int64(10600 + i*10)), Low: ip(10300),
					RetBp: bp(int32(200 + i*5)), BenchRetBp: bp(100)},
			},
		}
		if mutate != nil {
			mutate(i, &g)
		}
		seedGroupSet(t, s, g)
	}
	sets, err := s.st.loadForecastGroupSets(predTestRtype, "2025-06-01", "2025-06-30")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	return sets
}

func symbolFor(i int) string {
	return "6000" + string(rune('0'+i/100%10)) + string(rune('0'+i/10%10)) + string(rune('0'+i%10))
}

// TestStatisticsAreArchitectureDeterministic pins the last-bit result of the statistics' floating-point
// multiply-adds to their base-amd64 value.
//
// Go's spec permits an implementation to contract `a*b + c` into a single fused multiply-add. The arm64
// backend and the GOAMD64>=v3 amd64 backend do; the base (SSE2) amd64 backend does not. The two forms
// round differently, so without an explicit float64() barrier a p-value would differ by one ulp between
// the multi-arch release image's builds — and a "mature cell can never change" promise would be false
// the moment two builds disagreed.
//
// Each case is a fixed input whose contracted form differs from its rounded form; the expected values
// are the base-amd64 results. On an architecture that contracts, the test fails the moment a barrier is
// removed, and on base amd64 it pins that the barrier changed nothing.
func TestStatisticsAreArchitectureDeterministic(t *testing.T) {
	// pearson (forecast_stats.go) — the IC path.
	x := []float64{-467453.34799834504, 851814.5740686853, -354709.66366818256, 521846.8054312628,
		-274953.3148886243, -124110.07143413178, -157333.47489722128, 1.8503700466309118e+06, -892838.6317734944}
	y := []float64{1.758743180429219e+06, -259610.62353169444, 97133.37088882424, 686934.3356067712,
		-41771.97866842364, -797130.1890835358, -196153.73329400615, 114533.50850602132, 404752.0167660134}
	if got, want := pearson(x, y), -0.19012956571531586247; got != want {
		t.Errorf("pearson = %.20g, want %.20g (the amd64 value; a difference is FMA contraction)", got, want)
	}

	// spreadStat (forecast_groups.go) — the spread permutation statistic.
	spreadVals := []float64{-14297.669077771037, 15574.538981985952, 17506.99973298668, 11124.922162094244,
		-4276.642431456285, -6118.545681733372, 12191.189609114055}
	var s float64
	for _, v := range spreadVals {
		s += v
	}
	k := 1/8.0 + 1/5.0
	invc := 12345.67 / 5.0
	if got, want := spreadStat(s, k, invc), 7834.9238209465747786; got != want {
		t.Errorf("spreadStat = %.20g, want %.20g (the amd64 value)", got, want)
	}
}

func TestSpearmanAveragesTies(t *testing.T) {
	x := []float64{1, 2, 2, 3}
	y := []float64{4, 3, 2, 1}
	got := spearman(x, y)
	want := -4.5 / math.Sqrt(22.5)
	if math.Abs(got-want) > 1e-12 {
		t.Fatalf("spearman = %v, want %v", got, want)
	}
	if r := averageRanks([]float64{10, 10, 20}); math.Abs(r[0]-1.5) > 1e-12 || r[2] != 3 {
		t.Fatalf("average ranks = %v", r)
	}
	if !math.IsNaN(spearman([]float64{1, 1, 1}, []float64{1, 2, 3})) {
		t.Fatal("constant series must be NaN, not 0")
	}
}

func TestPermutationPIsReproducibleAndBounded(t *testing.T) {
	vals := []float64{1, 2, 3, 4, 5, 6}
	stat := func(perm []int) float64 {
		var s float64
		for i := range perm {
			s += vals[perm[i]] * float64(i)
		}
		return s - 0
	}
	a := permP(42, stat(nil2identity(len(vals))), len(vals), stat)
	b := permP(42, stat(nil2identity(len(vals))), len(vals), stat)
	if a != b {
		t.Fatalf("same seed gave %v and %v", a, b)
	}
	if !(a > 0 && a <= 1) {
		t.Fatalf("p = %v, want (0, 1]", a)
	}
	// A wildly extreme statistic is still not impossible.
	if a >= 0.05 {
		t.Fatalf("p = %v, expected a small value", a)
	}
}

func nil2identity(n int) []int {
	out := make([]int, n)
	for i := range out {
		out[i] = i
	}
	return out
}

func TestBHAdjustTextbook(t *testing.T) {
	got := bhAdjust([]float64{0.01, 0.04, 0.03})
	want := []float64{0.03, 0.04, 0.04}
	for i := range want {
		if math.Abs(got[i]-want[i]) > 1e-12 {
			t.Fatalf("q[%d] = %v, want %v (all %v)", i, got[i], want[i], got)
		}
	}
	q := bhAdjust([]float64{0.01, math.NaN(), 0.04})
	if math.Abs(q[0]-0.02) > 1e-12 || !math.IsNaN(q[1]) || math.Abs(q[2]-0.04) > 1e-12 {
		t.Fatalf("NaN must be skipped: %v", q)
	}
}

func TestCellMaturityGates(t *testing.T) {
	s := newV1Server(t)
	sets := sampleMembers(t, s, 30, nil)
	members, pending, cov := forecastMembership(sets)
	if cov.Member != 30 || len(members) != 30 {
		t.Fatalf("membership = %+v", cov)
	}
	cell := computeForecastCell(members, pending, predTestRtype, "2025-06-01", "2025-06-30", "d5", "2026-10-18")
	if cell.State != "mature" {
		t.Fatalf("30 members: state = %q, want mature (%+v)", cell.State, cell)
	}
	if cell.N == nil || *cell.N != 30 {
		t.Fatalf("n = %v", cell.N)
	}

	// n = 29 is small.
	s2 := newV1Server(t)
	m29, p29, _ := forecastMembership(sampleMembers(t, s2, predMatureMinN-1, nil))
	if c := computeForecastCell(m29, p29, predTestRtype, "2025-06-01", "2025-06-30", "d5", "2026-10-18"); c.State != "small" || c.N == nil || *c.N != 29 {
		t.Fatalf("29 members: %+v", c)
	}

	// A group that has not ended is open, whatever its marks say.
	if c := computeForecastCell(members, pending, predTestRtype, "2025-06-01", "2025-06-30", "d5", "2025-06-15"); c.State != "open" {
		t.Fatalf("unfinished group: %+v", c)
	}

	// One pending mark holds the whole cell.
	s3 := newV1Server(t)
	m3, p3, _ := forecastMembership(sampleMembers(t, s3, 30, func(i int, g *gsSeed) {
		if i == 0 {
			g.Marks["d5"] = gsMark{Status: markPending}
		}
	}))
	if c := computeForecastCell(m3, p3, predTestRtype, "2025-06-01", "2025-06-30", "d5", "2026-10-18"); c.State != "pending" || c.Resolved == nil || *c.Resolved != 29 {
		t.Fatalf("pending mark: %+v", c)
	}

	// Refusals: 5 % of 40 members is allowed, 6 % of 50 is not.
	s4 := newV1Server(t)
	m4, p4, _ := forecastMembership(sampleMembers(t, s4, 40, func(i int, g *gsSeed) {
		if i < 2 {
			g.Marks["d5"] = gsMark{Status: markRefusedFinal}
		}
	}))
	if c := computeForecastCell(m4, p4, predTestRtype, "2025-06-01", "2025-06-30", "d5", "2026-10-18"); c.State != "mature" {
		t.Fatalf("5%% refusals: %+v", c)
	}
	s5 := newV1Server(t)
	m5, p5, _ := forecastMembership(sampleMembers(t, s5, 50, func(i int, g *gsSeed) {
		if i < 3 {
			g.Marks["d5"] = gsMark{Status: markRefusedFinal}
		}
	}))
	if c := computeForecastCell(m5, p5, predTestRtype, "2025-06-01", "2025-06-30", "d5", "2026-10-18"); c.State != "pending" {
		t.Fatalf("6%% refusals: %+v", c)
	}
}

func TestNonMatureCellsOmitMetricKeys(t *testing.T) {
	s := newV1Server(t)
	m, p, _ := forecastMembership(sampleMembers(t, s, predMatureMinN-1, nil))
	cell := computeForecastCell(m, p, predTestRtype, "2025-06-01", "2025-06-30", "d5", "2026-10-18")
	b, _ := json.Marshal(cell)
	var m2 map[string]any
	_ = json.Unmarshal(b, &m2)
	for _, k := range []string{"spread_bp", "ic", "hit", "fill", "band", "calib"} {
		if _, ok := m2[k]; ok {
			t.Fatalf("%q present on a %s cell: %s", k, cell.State, b)
		}
	}
}

func TestMembershipCountsDuplicatesAndBaseline(t *testing.T) {
	s := newV1Server(t)
	base := gsSeed{MadeOn: "2025-06-10", S0Fen: ip(10000), TerminalFen: ip(11000),
		Marks: map[string]gsMark{"d5": {Status: markResolved, RetBp: bp(100), BenchRetBp: bp(50)}}}
	early := base
	early.Symbol = "600001"
	early.MadeAt = "2025-06-10T09:00:00+08:00"
	seedGroupSet(t, s, early)
	late := base
	late.Symbol = "600001"
	late.MadeAt = "2025-06-10T11:00:00+08:00"
	seedGroupSet(t, s, late)

	pendingBase := base
	pendingBase.Symbol = "600002"
	pendingBase.BaseCheck = baseCheckPending
	pendingBase.Marks = map[string]gsMark{"d5": {Status: markPending}}
	seedGroupSet(t, s, pendingBase)

	sets, _ := s.st.loadForecastGroupSets(predTestRtype, "2025-06-01", "2025-06-30")
	members, pb, cov := forecastMembership(sets)
	if cov.Member != 1 || cov.Duplicate != 1 || cov.PendingBaseline != 1 || len(members) != 1 || len(pb) != 1 {
		t.Fatalf("coverage = %+v, members=%d pending=%d", cov, len(members), len(pb))
	}
	m, p, _ := forecastMembership(sets)
	if c := computeForecastCell(m, p, predTestRtype, "2025-06-01", "2025-06-30", "d5", "2026-10-18"); c.State != "pending" {
		t.Fatalf("a pending baseline must block: %+v", c)
	}
}

func TestMonthlyAndWholeMonthCustomAgree(t *testing.T) {
	s := newV1Server(t)
	sampleMembers(t, s, predMatureMinN+2, nil)

	monthly := doGroups(t, s, "/api/admin/forecast/groups?mode=month")
	custom := doGroups(t, s, "/api/admin/forecast/groups?mode=custom&from=2025-06-01&to=2025-06-30&group=month")

	mg := findGroup(t, monthly, "2025-06")
	cg := findGroup(t, custom, "2025-06")
	if mg.Official != true || cg.Official != false {
		t.Fatalf("official flags: monthly=%v custom=%v", mg.Official, cg.Official)
	}
	for _, cp := range predCheckpoints {
		a, _ := json.Marshal(mg.Cells[cp])
		b, _ := json.Marshal(cg.Cells[cp])
		if string(a) != string(b) {
			t.Fatalf("%s differs:\n monthly=%s\n custom =%s", cp, a, b)
		}
	}
}

func TestMatureCellsDoNotChange(t *testing.T) {
	s := newV1Server(t)
	members, pending, _ := forecastMembership(sampleMembers(t, s, predMatureMinN+2, nil))
	a := computeForecastCell(members, pending, predTestRtype, "2025-06-01", "2025-06-30", "d5", "2026-10-18")
	b := computeForecastCell(members, pending, predTestRtype, "2025-06-01", "2025-06-30", "d5", "2027-01-01")
	ja, _ := json.Marshal(a)
	jb, _ := json.Marshal(b)
	if a.State != "mature" || string(ja) != string(jb) {
		t.Fatalf("a mature cell moved:\n %s\n %s", ja, jb)
	}
}

func TestForecastRangeRefusals(t *testing.T) {
	cases := []struct {
		from, to, group, want string
		ok                    bool
	}{
		{"2026-01-01", "2026-02-01", "month", "", true},
		{"2026-02-01", "2026-01-01", "month", "range_inverted", false},
		{"2020-01-01", "2026-10-01", "month", "range_too_long", false},
		{"2026-01-01", "2026-08-01", "week", "week_range_too_long", false},
		{"2026-01-01", "2026-02-01", "decade", "bad_group", false},
		{"2026-13-01", "2026-02-01", "month", "bad_date", false},
	}
	for _, c := range cases {
		code, ok := validateForecastRange(c.from, c.to, c.group)
		if ok != c.ok || code != c.want {
			t.Errorf("%s..%s/%s: got (%q,%v), want (%q,%v)", c.from, c.to, c.group, code, ok, c.want, c.ok)
		}
	}
}

func TestPartialWeekIsLabelledWithItsDates(t *testing.T) {
	ps := forecastPeriods("week", "2026-10-07", "2026-10-12")
	if len(ps) != 2 {
		t.Fatalf("periods = %+v", ps)
	}
	if ps[0].Label != "2026-10-07 – 2026-10-11" || ps[1].Label != "2026-10-12 – 2026-10-12" {
		t.Fatalf("labels = %q, %q", ps[0].Label, ps[1].Label)
	}
}

func TestMonthModeRefusesRange(t *testing.T) {
	s := newV1Server(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/admin/forecast/groups?mode=month&from=2026-01-01", nil)
	s.apiForecastGroups(rec, req, "admin")
	if rec.Code != http.StatusBadRequest || !hasCode(rec.Body.Bytes(), "month_takes_no_range") {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestGroupsRouteRequiresManage(t *testing.T) {
	s := tenancyServer(t)
	s.st.UpsertUser(User{Username: "alice", PasswordHash: "h", Role: "user"})
	s.st.UpsertUser(User{Username: "root", PasswordHash: "h", Role: "admin"})
	mux := http.NewServeMux()
	s.wireRoutes(mux)

	if w := sessionTestRequest(mux, "GET", "/api/admin/forecast/groups", managedTestCookie(t, s, "alice", "Chrome/130.0", time.Hour), ""); w.Code != http.StatusForbidden {
		t.Fatalf("non-manager: %d %s", w.Code, w.Body.String())
	}
	if w := sessionTestRequest(mux, "GET", "/api/admin/forecast/groups", managedTestCookie(t, s, "root", "Chrome/130.0", time.Hour), ""); w.Code != http.StatusOK {
		t.Fatalf("admin: %d %s", w.Code, w.Body.String())
	}
}

// doGroups calls the handler directly, which is how the validation tests avoid constructing a session.
func doGroups(t *testing.T, s *Server, target string) forecastGroupsResponse {
	t.Helper()
	rec := httptest.NewRecorder()
	s.apiForecastGroups(rec, httptest.NewRequest("GET", target, nil), "admin")
	if rec.Code != http.StatusOK {
		t.Fatalf("groups %s: %d %s", target, rec.Code, rec.Body.String())
	}
	var out forecastGroupsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return out
}

func findGroup(t *testing.T, resp forecastGroupsResponse, label string) forecastGroup {
	t.Helper()
	for _, g := range resp.Groups {
		if g.Label == label {
			return g
		}
	}
	t.Fatalf("no group %q in %d groups", label, len(resp.Groups))
	return forecastGroup{}
}

func hasCode(body []byte, code string) bool {
	var m map[string]any
	return json.Unmarshal(body, &m) == nil && m["code"] == code
}

// BenchmarkForecastGroupsMaxBounds is the plan's cost gate: 36 monthly groups x 5 checkpoints = 180
// cells at 100 members, with the members split across two policies so the slices are computed too.
// The budget is under 3 s for one pass of all 180 cells.
func BenchmarkForecastGroupsMaxBounds(b *testing.B) {
	members := make([]*groupSet, 100)
	for i := range members {
		gs := &groupSet{
			Symbol: symbolFor(i), MadeOn: "2025-01-15", State: predStateAccepted, DecisionQuality: predQualityNormal,
			BaseCheck: baseMatch, HorizonStd: true, Stance: "long",
			PolicyVersion: []string{"policy-a", "policy-b"}[i%2],
			S0Fen:         ip(10000), TerminalFen: ip(int64(11000 + i*10)), EntryFen: ip(10500),
			BearFen: ip(8000), BullFen: ip(14000), PProfitBp: bp(int32(5500 + i%20)),
			Marks: map[string]PredictionMark{},
		}
		if i%3 == 0 {
			gs.Stance = "no_position"
		}
		for _, cp := range predCheckpoints {
			gs.Marks[cp] = PredictionMark{Checkpoint: cp, Status: markResolved,
				RetBp: bp(int32(200 + i*5)), BenchRetBp: bp(100),
				CloseFen: ip(int64(10600 + i*10)), LowFen: ip(10300)}
		}
		members[i] = gs
	}
	periods := make([]forecastPeriod, 36)
	for i := range periods {
		periods[i] = forecastPeriod{Start: "2025-01-01", End: "2025-01-31", Label: "2025-01"}
	}
	srv := &Server{} // no cache: this measures the cold computation the first request pays
	b.ResetTimer()
	for n := 0; n < b.N; n++ {
		srv.buildForecastGroups(periods, members, predTestRtype, "2026-10-18", false)
	}
}

// TestOnlyOfficialMatureCellsAreCached pins the one optimization whose correctness rests on the
// immutability argument: if a pending cell were cached it would never finish, and if an exploratory
// range wrote to the memo it could poison the record.
func TestOnlyOfficialMatureCellsAreCached(t *testing.T) {
	s := newV1Server(t)
	sampleMembers(t, s, predMatureMinN+2, nil)

	first := doGroups(t, s, "/api/admin/forecast/groups?mode=month")
	if len(s.forecastGroupsCache) == 0 {
		t.Fatal("a mature monthly cell was not cached")
	}
	if c, ok := s.forecastGroupsCache[forecastGroupCellKey(predTestRtype, "2025-06-01", "2025-06-30", "d5")]; !ok || c.State != "mature" {
		t.Fatalf("d5 entry = %+v, present=%v", c, ok)
	}
	if c, ok := s.forecastGroupsCache[forecastGroupCellKey(predTestRtype, "2025-06-01", "2025-06-30", "h12")]; ok {
		t.Fatalf("a %s cell was cached: %+v", c.State, c)
	}

	customServer := newV1Server(t)
	sampleMembers(t, customServer, predMatureMinN+2, nil)
	doGroups(t, customServer, "/api/admin/forecast/groups?mode=custom&from=2025-06-01&to=2025-06-30&group=month")
	if len(customServer.forecastGroupsCache) != 0 {
		t.Fatalf("exploratory mode wrote %d entries", len(customServer.forecastGroupsCache))
	}
	_ = first
}

func doMembers(t *testing.T, s *Server, target string) forecastMembersResponse {
	t.Helper()
	rec := httptest.NewRecorder()
	s.apiForecastGroupMembers(rec, httptest.NewRequest("GET", target, nil), "admin")
	if rec.Code != http.StatusOK {
		t.Fatalf("members %s: %d %s", target, rec.Code, rec.Body.String())
	}
	var out forecastMembersResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return out
}

func TestGroupMembersMirrorTheCell(t *testing.T) {
	s := newV1Server(t)
	sampleMembers(t, s, predMatureMinN+3, nil)
	resp := doMembers(t, s, "/api/admin/forecast/groups/members?mode=custom&from=2025-06-01&to=2025-06-30&group=month&label=2025-06&checkpoint=d5")
	if resp.State != "mature" || len(resp.Members) != predMatureMinN+3 {
		t.Fatalf("state=%q members=%d", resp.State, len(resp.Members))
	}
	m := resp.Members[0]
	if m.Stance != "long" || m.ExcessBp == nil || m.Upside == nil || m.Agree == nil {
		t.Fatalf("member = %+v", m)
	}
	// The fixtures insert no reports, so every set is orphaned and must not offer a link.
	if m.ReportID != nil {
		t.Fatalf("orphaned set has report id %v", *m.ReportID)
	}

	rec := httptest.NewRecorder()
	s.apiForecastGroupMembers(rec, httptest.NewRequest("GET",
		"/api/admin/forecast/groups/members?mode=custom&from=2025-06-01&to=2025-06-30&group=month&label=2025-06&checkpoint=d7", nil), "admin")
	if rec.Code != http.StatusBadRequest || !hasCode(rec.Body.Bytes(), "bad_checkpoint") {
		t.Fatalf("bad checkpoint: %d %s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	s.apiForecastGroupMembers(rec, httptest.NewRequest("GET",
		"/api/admin/forecast/groups/members?mode=custom&from=2025-06-01&to=2025-06-30&group=month&label=2024-01&checkpoint=d5", nil), "admin")
	if rec.Code != http.StatusNotFound || !hasCode(rec.Body.Bytes(), "label_not_found") {
		t.Fatalf("unknown label: %d %s", rec.Code, rec.Body.String())
	}
}
