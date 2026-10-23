package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/config"
)

type forecastMatrixSubject struct {
	name, scope string
	ou          int64
	external    bool
	admin       bool
	walled      bool
	bridge      bool // an app-bridge Bearer token: never admitted to a forecast read
	// Explicit fixture expectations, not a call back into the resolver under test.
	population  []int64
	user, token string
}

type forecastMatrixSet struct {
	id, ou, report int64
	month          string
	readable       bool
}

// TestForecastAccessMatrix pins the P26 columns of plan section 10.2 through the real mux.
// P18 adds the stage/chip/overlay columns.
func TestForecastAccessMatrix(t *testing.T) {
	for _, driver := range []string{"sqlite", "postgres"} {
		t.Run(driver, func(t *testing.T) {
			for _, defaultScope := range []string{scopeNone, scopeOwn} {
				t.Run("default-"+defaultScope, func(t *testing.T) {
					var st *Store
					if driver == "postgres" {
						st = pgStore(t, "prediction_marks", "prediction_claims", "prediction_sets", "report_viewers", "reports", "version_grants", "report_versions", "users", "user_groups")
					} else {
						st = newTestStore(t)
					}
					runForecastAccessMatrix(t, st, defaultScope)
				})
			}
		})
	}
}

func runForecastAccessMatrix(t *testing.T, st *Store, defaultScope string) {
	t.Helper()
	s := &Server{st: st, cfg: &config.Config{SecretKey: "0123456789abcdef0123456789abcdef"},
		loginThr: newLoginThrottle(), v1Rate: newRateLimiter(), appTok: newAppTokens(time.Hour),
		forecastEval: forecastEvaluator{now: func() time.Time { return time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC) }}}
	// The shared Postgres database can contain settings from earlier tests.
	for key, value := range map[string]string{setRequire2FAForStaff: "0", setForecastCustomPerMin: "0", setForecastMembersPerMin: "0", setForecastPopulationFrom: "", setForecastUnrecordedFloorID: ""} {
		prior := st.GetSetting(key, "")
		t.Cleanup(func() {
			if err := st.SetSetting(key, prior); err != nil {
				t.Error(err)
			}
		})
		if err := st.SetSetting(key, value); err != nil {
			t.Fatal(err)
		}
	}
	def := st.EnsureDefaultGroup()
	t.Cleanup(func() {
		if _, err := st.exec("UPDATE user_groups SET forecast_scope=? WHERE id=?", scopeNone, def); err != nil {
			t.Error(err)
		}
	})
	if _, err := st.exec("UPDATE user_groups SET forecast_scope=? WHERE id=?", defaultScope, def); err != nil {
		t.Fatal(err)
	}
	internal := seedOU(t, st, 0, scopeAll)
	team := seedOU(t, st, internal, nil)
	own := seedOU(t, st, internal, scopeOwn)
	sub := seedOU(t, st, own, nil)
	none := seedOU(t, st, 0, scopeNone)
	extAll := seedOU(t, st, 0, scopeAll)
	extChild := seedOU(t, st, extAll, nil)
	extOwn := seedOU(t, st, 0, scopeOwn)
	extNone := seedOU(t, st, 0, scopeNone)
	weird := seedOU(t, st, 0, "everyone")
	defChild := seedOU(t, st, def, nil)
	missing := seedOU(t, st, 0, nil)
	dangling := seedOU(t, st, missing, scopeAll)
	if _, err := st.exec("DELETE FROM user_groups WHERE id=?", missing); err != nil {
		t.Fatal(err)
	}
	for _, id := range []int64{extAll, extOwn, extNone} {
		if err := st.SetGroupRestricted(id, true); err != nil {
			t.Fatal(err)
		}
	}
	walledOU := seedOU(t, st, internal, nil)
	if err := st.SetGroupRequire2FA(walledOU, true); err != nil {
		t.Fatal(err)
	}
	subjects := []forecastMatrixSubject{
		{name: "admin", scope: scopeAll, ou: none, admin: true},
		{name: "Default", scope: defaultScope, ou: def, population: []int64{def, defChild}},
		{name: "Internal", scope: scopeAll, ou: internal},
		{name: "InternalTeam", scope: scopeAll, ou: team},
		{name: "InternalOwn", scope: scopeOwn, ou: own, population: []int64{own, sub}},
		{name: "InternalOwnSub", scope: scopeOwn, ou: sub, population: []int64{sub}},
		{name: "InternalNone", scope: scopeNone, ou: none},
		{name: "ExtAll", scope: scopeAll, ou: extAll, external: true},
		{name: "ExtAllChild", scope: scopeAll, ou: extChild, external: true},
		{name: "ExtOwn", scope: scopeOwn, ou: extOwn, external: true, population: []int64{extOwn}},
		{name: "ExtNone", scope: scopeNone, ou: extNone, external: true},
		{name: "Weird", scope: scopeNone, ou: weird},
		{name: "DefChild", scope: defaultScope, ou: defChild, population: []int64{defChild}},
		{name: "Dangling", scope: scopeNone, ou: dangling},
		{name: "unassigned", scope: defaultScope, population: []int64{def, defChild}},
		{name: "restricted-account", scope: scopeAll, ou: internal, external: true},
		{name: "enrolment", scope: scopeAll, ou: walledOU, walled: true},
	}
	var grants []string
	for i := range subjects {
		who := &subjects[i]
		who.user = "matrix-" + who.name
		mustAddUser(t, s, who.user, who.admin)
		if _, err := st.exec("UPDATE users SET group_id=?,restricted=? WHERE username=?", nullableID(who.ou), boolInt(who.name == "restricted-account"), who.user); err != nil {
			t.Fatal(err)
		}
		if who.external {
			grants = append(grants, userPrincipal(who.user))
		}
	}
	if err := st.SaveVersion(ReportVersion{Name: "default", Visibility: VisibilityOwner}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetVersionGrants("default", grants); err != nil {
		t.Fatal(err)
	}
	var ledger []forecastMatrixSet
	var readableReport, hiddenReport int64
	for _, month := range []string{"2025-06", "2025-07"} {
		for i := 0; i < 40; i++ {
			// Each own population is nonempty; all has >=30 resolved members in both months.
			ou := []int64{own, sub, extOwn, def, defChild, 0}[i%6]
			report, _, err := st.UpsertReport(Rep{Symbol: forecastMatrixSymbol(i), Date: month + "-10", RType: predTestRtype, Version: "default", MD: "# matrix"})
			if err != nil {
				t.Fatal(err)
			}
			set := seedForecastMatrixSet(t, st, report, ou, forecastMatrixSymbol(i), month+"-10", predStateAccepted)
			if i == 31 { // A genuinely purged report leaves a member for internal readers.
				if _, err := st.DeleteReport(report); err != nil {
					t.Fatal(err)
				}
				report = 0
			}
			readable := i%2 == 0 && report != 0
			ledger = append(ledger, forecastMatrixSet{set, ou, report, month, readable})
			if readable {
				for _, who := range subjects {
					if who.external {
						if err := st.AddReportViewer(report, month+"-10", who.user, 0); err != nil {
							t.Fatal(err)
						}
					}
				}
			}
			if month == "2025-06" && i == 0 {
				readableReport = report
			}
			if month == "2025-06" && i == 1 {
				hiddenReport = report
			}
		}
	}
	// Both diagnostic states share the queried symbol. Neither may displace an accepted row.
	seedForecastMatrixSet(t, st, readableReport, own, forecastMatrixSymbol(0), "2025-06-11", predStateRejected)
	seedForecastMatrixSet(t, st, 0, 0, forecastMatrixSymbol(0), "2025-07-11", predStateAccepted)
	machineToken := randToken()
	if err := st.CreateToken(machineToken, "matrix", "query", ""); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := st.exec("DELETE FROM api_tokens WHERE token_hash=?", tokenDigest(machineToken)); err != nil {
			t.Error(err)
		}
	})
	subjects = append(subjects, forecastMatrixSubject{name: "machine", token: machineToken},
		// App-bridge tokens get no forecast reads whichever account minted them (fail closed until the
		// shared token binding exists), so the minting account's scope changes nothing below.
		forecastMatrixSubject{name: "bridge-all", scope: scopeAll, bridge: true, token: mintForecastBridge(t, s, "matrix-Internal")},
		forecastMatrixSubject{name: "bridge-none", scope: scopeNone, bridge: true, token: mintForecastBridge(t, s, "matrix-InternalNone")},
		forecastMatrixSubject{name: "bridge-admin", scope: scopeAll, bridge: true, token: mintForecastBridge(t, s, "matrix-admin")},
		forecastMatrixSubject{name: "anonymous"})
	mux := http.NewServeMux()
	s.wireRoutes(mux)
	type probe struct{ name, path, kind string }
	probes := []probe{
		{"record", "/api/forecast/groups", "groups"},
		{"explore", "/api/forecast/groups?mode=custom&from=2025-06-01&to=2025-07-31&group=month", "custom"},
		{"members", "/api/forecast/groups/members?label=2025-06&checkpoint=d5&limit=200", "members"},
		{"members-explore", "/api/forecast/groups/members?mode=custom&from=2025-06-01&to=2025-07-31&group=month&label=2025-06&checkpoint=d5", "members-custom"},
		{"admin-record", "/api/admin/forecast/groups", "admin-groups"},
		{"admin-members", "/api/admin/forecast/groups/members?label=2025-06&checkpoint=d5", "admin-members"},
		{"admin-access", "/api/admin/forecast/access", "admin-access"},
		{"card", "/api/report/" + itoa64(readableReport) + "/predictions?include=payload", "card"},
		{"card-hidden", "/api/report/" + itoa64(hiddenReport) + "/predictions?include=payload", "hidden"},
		{"history", "/api/predictions?symbol=" + forecastMatrixSymbol(0) + "&include=payload", "list"},
		{"v1-card", "/api/v1/reports/" + itoa64(readableReport) + "/predictions?include=payload", "card"},
		{"v1-hidden", "/api/v1/reports/" + itoa64(hiddenReport) + "/predictions?include=payload", "hidden"},
		{"v1-history", "/api/v1/predictions?symbol=" + forecastMatrixSymbol(0) + "&include=payload", "list"},
		{"me", "/api/me", "me"},
	}
	for _, who := range subjects {
		t.Run(who.name, func(t *testing.T) {
			for _, p := range probes {
				t.Run(p.name, func(t *testing.T) {
					r := httptest.NewRequest("GET", p.path, nil)
					if who.user != "" {
						r = sessionForPath(t, s, who.user, "", p.path)
					} else if who.token != "" {
						r.Header.Set("Authorization", "Bearer "+who.token)
					}
					w := httptest.NewRecorder()
					mux.ServeHTTP(w, r)
					want, code := 200, ""
					adminRoute := strings.HasPrefix(p.kind, "admin-")
					v1 := strings.HasPrefix(p.path, "/api/v1/")
					switch {
					case who.bridge && v1:
						want, code = 403, "forecast_unavailable"
					case who.user == "" && !(who.name == "machine" && v1):
						want = 401
					case adminRoute && !who.admin:
						// The existing permission wrapper refuses before the enrolment wall.
						want = 403
					case who.name == "Dangling" && p.kind != "me":
						// A broken tree already fails closed in the independent enrolment policy.
						want, code = 503, "enrolment_policy_unavailable"
					case who.walled && p.kind != "me":
						want, code = 403, "enrolment_required"
					case who.user != "" && who.scope == scopeNone && p.kind != "me":
						want, code = 403, "forecast_unavailable"
					case who.external && strings.Contains(p.kind, "custom"):
						want, code = 403, "forecast_custom_not_allowed"
					case who.external && p.kind == "hidden":
						want, code = 404, "not_found"
					}
					if w.Code != want || code != "" && forecastMatrixErrorCode(t, w.Body.Bytes()) != code {
						t.Fatalf("got %d %s; want %d %s", w.Code, w.Body.String(), want, code)
					}
					if want != 200 {
						return
					}
					var out map[string]any
					if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
						t.Fatal(err)
					}
					if p.kind == "me" {
						if who.user != "" {
							hint := out["forecast"].(map[string]any)
							if hint["scope"] != who.scope || hint["external"] != who.external {
								t.Fatalf("incorrect navigation hint: %v", hint)
							}
						}
						return
					}
					assertForecastMatrixShape(t, out, who, p.kind, ledger)
				})
			}
		})
	}
}

func nullableID(id int64) any {
	if id == 0 {
		return nil
	}
	return id
}

func forecastMatrixSymbol(i int) string { return strconv.Itoa(600000 + i) }

func seedForecastMatrixSet(t *testing.T, st *Store, report, ou int64, symbol, date, state string) int64 {
	t.Helper()
	id, err := st.insertID(`INSERT INTO prediction_sets(report_id,symbol,rtype,report_date,made_at,made_on,cohort,zone,state,
		decision_quality,horizon_std,s0_fen,base_check,bench_code,attributed_ou,payload,payload_sha256,errors)
		VALUES(?,?,?,?,?,?,?,?,?,'normal',1,10000,?,'sh000852',?,'{"fixture":true}',?,'[{"reason":"fixture"}]')`,
		nullableID(report), symbol, predTestRtype, date, date+"T10:00:00+08:00", date, date[:7], predZone, state, baseMatch, nullableID(ou), date+state)
	if err != nil {
		t.Fatal(err)
	}
	stance, ret := "long", 200
	if symbol[len(symbol)-1]%2 == 1 {
		stance, ret = "no_position", 50
	}
	if _, err := st.exec("INSERT INTO prediction_claims(set_id,claim_key,kind,stance) VALUES(?,?,?,?)", id, "stance", kindStance, stance); err != nil {
		t.Fatal(err)
	}
	if _, err := st.exec(`INSERT INTO prediction_marks(set_id,checkpoint,status,ret_bp,bench_ret_bp,close_fen) VALUES(?,'d5',?,?,100,10200)`, id, markResolved, ret); err != nil {
		t.Fatal(err)
	}
	return id
}

func assertForecastMatrixShape(t *testing.T, out map[string]any, who forecastMatrixSubject, kind string, ledger []forecastMatrixSet) {
	t.Helper()
	noVolume := who.external && who.scope == scopeAll
	forbidden := []string{"attributed_ou"}
	if noVolume && (strings.Contains(kind, "groups") || kind == "custom" || strings.Contains(kind, "members")) {
		forbidden = append(forbidden, "coverage", "unrecorded", "n", "total", "resolved", "refused", "corp_action", "ic_n", "hit_n", "band_n", "calib_n")
	}
	if !who.admin && who.name != "machine" {
		forbidden = append(forbidden, "unrecorded", "errors")
	}
	if who.external || who.scope == scopeOwn {
		forbidden = append(forbidden, "membership", "counted_set_id", "counted_in_group")
	}
	if who.external {
		forbidden = append(forbidden, "payload")
	}
	forecastMatrixForbid(t, out, "$", forbidden)
	switch kind {
	case "groups", "custom", "admin-groups":
		groups, ok := out["groups"].([]any)
		if !ok {
			t.Fatal("groups must be a JSON array")
		}
		seenMature := 0
		months := map[string]int{}
		for _, raw := range groups {
			g := raw.(map[string]any)
			label := g["label"].(string)
			months[label]++
			if _, ok := g["coverage"]; ok == noVolume {
				t.Fatalf("coverage presence mismatch: %v", g)
			}
			cells := g["cells"].(map[string]any)
			c := cells["d5"].(map[string]any)
			if who.scope == scopeOwn && (g["label"] == "2025-06" || g["label"] == "2025-07") {
				want := 0
				for _, set := range ledger {
					if set.month == g["label"] && slices.Contains(who.population, set.ou) {
						want++
					}
				}
				if c["n"] != float64(want) {
					t.Fatalf("own sample size %v want %d", c["n"], want)
				}
			}
			if c["state"] == "mature" {
				seenMature++
				if c["spread_bp"] != float64(150) {
					t.Fatalf("mature spread missing or changed: %v", c)
				}
				if noVolume && c["min_n_met"] != true {
					t.Fatal("mature external cell lost its threshold badge")
				}
				if !noVolume && c["n"] == nil {
					t.Fatal("internal or own cell lost sample size")
				}
			}
			if noVolume {
				for _, rawCell := range cells {
					cell := rawCell.(map[string]any)
					if cell["state"] != "mature" && len(cell) != 1 {
						t.Fatalf("non-mature external cell leaks fields: %v", cell)
					}
				}
			}
		}
		for _, month := range []string{"2025-06", "2025-07"} {
			if months[month] != 1 {
				t.Fatalf("fixture month %s occurs %d times, want exactly once", month, months[month])
			}
		}
		if who.scope == scopeAll && seenMature != 2 {
			t.Fatalf("all record has %d mature months, want two", seenMature)
		}
		_, unrecorded := out["unrecorded"]
		if unrecorded != (kind == "admin-groups") {
			t.Fatal("unrecorded presence mismatch")
		}
	case "members", "members-custom", "admin-members":
		var want, got []int64
		for _, set := range ledger {
			if set.month != "2025-06" || who.external && !set.readable {
				continue
			}
			if who.scope == scopeOwn && !slices.Contains(who.population, set.ou) {
				continue
			}
			want = append(want, set.id)
		}
		for _, raw := range out["members"].([]any) {
			m := raw.(map[string]any)
			got = append(got, int64(m["set_id"].(float64)))
			if who.external && m["report_id"] == nil {
				t.Fatal("external member list contains an orphan")
			}
		}
		sort.Slice(want, func(i, j int) bool { return want[i] < want[j] })
		sort.Slice(got, func(i, j int) bool { return got[i] < got[j] })
		if !reflect.DeepEqual(want, got) {
			t.Fatalf("members mismatch: got %v want %v", got, want)
		}
		if !noVolume && out["total"] == nil {
			t.Fatal("internal/own drawer lost its own count")
		}
	case "card", "hidden", "list":
		sets := out["sets"].([]any)
		full := who.admin || who.name == "machine"
		want := 1
		if kind == "card" && full {
			want = 2
		}
		if kind == "list" {
			want = 3 // two accepted reports and one orphan
			if who.external {
				want = 2
			} else if full {
				want = 4 // rejected diagnostics are machine/admin only
			}
		}
		if len(sets) != want {
			t.Fatalf("prediction set count %d want %d: %v", len(sets), want, sets)
		}
		for _, raw := range sets {
			set := raw.(map[string]any)
			if !full && set["state"] != predStateAccepted {
				t.Fatal("non-admin received rejected diagnostics")
			}
			_, payload := set["payload"]
			_, errors := set["errors"]
			if payload != !who.external || errors != full {
				t.Fatalf("payload/errors mismatch: %v", set)
			}
		}
		if kind == "card" {
			_, membership := out["counted_set_id"]
			if membership != (!who.external && who.scope != scopeOwn) {
				t.Fatal("membership badge presence mismatch")
			}
		}
	}
}

func forecastMatrixErrorCode(t *testing.T, body []byte) string {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatal(err)
	}
	if code, ok := out["code"].(string); ok {
		return code
	}
	if nested, ok := out["error"].(map[string]any); ok {
		code, _ := nested["code"].(string)
		return code
	}
	return ""
}

func forecastMatrixForbid(t *testing.T, value any, path string, forbidden []string) {
	t.Helper()
	switch value := value.(type) {
	case map[string]any:
		for key, child := range value {
			if containsStr(forbidden, key) {
				t.Errorf("forbidden response path %s.%s", path, key)
			}
			forecastMatrixForbid(t, child, path+"."+key, forbidden)
		}
	case []any:
		for _, child := range value {
			forecastMatrixForbid(t, child, path+"[]", forbidden)
		}
	}
}
