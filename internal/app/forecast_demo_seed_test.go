package app

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/config"
	"golang.org/x/crypto/bcrypt"
)

const forecastDemoInstant = "2027-06-20T07:00:00Z"

var forecastDemoOutput = flag.String("forecast-demo-out", "", "create a new local-only forecast preview database")

type forecastDemoInfo struct {
	Password string
	Groups   map[string]int64
	Reports  map[string]int64
}

type forecastDemoCase struct {
	label, symbol, date, variant, stance, owner string
	slope                                       int64
	version                                     string
	policy                                      string
	entry                                       string
}

func TestWriteForecastDemoDB(t *testing.T) {
	if *forecastDemoOutput == "" {
		t.Skip("set -forecast-demo-out to create the local preview")
	}
	info, err := writeForecastDemoDB(t, *forecastDemoOutput)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("Local synthetic preview only. Accounts: admin, internal, own, external-all, external-own, none")
	t.Logf("One-time preview password for these accounts: %s", info.Password)
	t.Logf("Use RP_CONFIG=%s RP_FORECAST_DEMO_NOW=%s with a dev build; evaluator remains off",
		filepath.Join(filepath.Dir(*forecastDemoOutput), "config.yaml"), forecastDemoInstant)
}

// Reserve both outputs exclusively. In particular, a stale WAL must never be attached to a new DB.
func writeForecastDemoDB(t *testing.T, output string) (info forecastDemoInfo, err error) {
	t.Helper()
	output, err = filepath.Abs(output)
	if err != nil {
		return info, err
	}
	cfgPath := filepath.Join(filepath.Dir(output), "config.yaml")
	if output == cfgPath {
		return info, errors.New("database and config paths must differ")
	}
	if err := os.MkdirAll(filepath.Dir(output), 0o700); err != nil {
		return info, err
	}
	for _, path := range []string{output, cfgPath, output + "-wal", output + "-shm"} {
		if _, err := os.Lstat(path); err == nil {
			return info, fmt.Errorf("refusing existing demo output %s: %w", path, os.ErrExist)
		} else if !os.IsNotExist(err) {
			return info, err
		}
	}
	created := map[string]os.FileInfo{}
	var configFile *os.File
	complete := false
	defer func() {
		if !complete {
			// Only these files were reserved by this call, never preexisting user files or globs.
			for path, original := range created {
				if current, err := os.Lstat(path); err == nil && os.SameFile(original, current) {
					_ = os.Remove(path)
				}
			}
		}
	}()
	for _, path := range []string{output, cfgPath} {
		file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return info, err
		}
		original, err := file.Stat()
		if err != nil {
			_ = file.Close()
			return info, err
		}
		created[path] = original
		if path == cfgPath {
			configFile = file
			defer file.Close()
			continue
		}
		if err := file.Close(); err != nil {
			return info, err
		}
	}
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return info, err
	}
	passwordBytes := make([]byte, 18)
	if _, err := rand.Read(passwordBytes); err != nil {
		return info, err
	}
	info = forecastDemoInfo{Password: hex.EncodeToString(passwordBytes), Groups: map[string]int64{}, Reports: map[string]int64{}}
	st, err := OpenStore("sqlite", output)
	if err != nil {
		return info, err
	}
	defer st.Close()
	// The preview stands for an upgraded database: own-scoped accounts read purge history from the
	// ownership evidence table and are refused without it.
	if err := createPredictionPurgeOwners(st.poolExec()); err != nil {
		return info, err
	}
	s := &Server{st: st, cfg: &config.Config{SecretKey: hex.EncodeToString(secret)}}
	// Every ingest carries a name, so this writer has no name-provider dependency.
	s.names = LoadNames(filepath.Dir(output), st)
	s.names.fetch = func(string) string { t.Fatal("demo seed attempted a network name lookup"); return "" }
	now, _ := time.Parse(time.RFC3339, forecastDemoInstant)
	if _, _, code, err := st.SaveForecastPopulationFrom(time.Date(2026, 1, 1, 0, 0, 0, 0, chinaZone), now); err != nil || code != "" {
		return info, fmt.Errorf("demo ledger start: %s / %v", code, err)
	}
	for key, value := range map[string]string{
		"timezone": "Asia/Shanghai", "forecast_auto": "0", setForecastBench: "sh000852",
		setForecastBenchNext: "sh000906", setForecastBenchNextFrom: "2026-11",
	} {
		if err := st.SetSetting(key, value); err != nil {
			return info, err
		}
	}
	for _, group := range []struct {
		name, parent, scope string
		restricted          bool
	}{
		{"internal", "", "all", false}, {"own", "internal", "own", false},
		{"own-child", "own", "", false}, {"external-all", "", "all", true},
		{"external-own", "external-all", "own", false},
	} {
		id, err := st.CreateUserGroup("Demo "+group.name, "Synthetic preview only", 0)
		if err != nil {
			return info, err
		}
		info.Groups[group.name] = id
		if group.parent != "" {
			if err := st.SetGroupParent(id, info.Groups[group.parent]); err != nil {
				return info, err
			}
		}
		if err := st.SetGroupRestricted(id, group.restricted); err != nil {
			return info, err
		}
		var scope any
		if group.scope != "" {
			scope = group.scope
		}
		if _, err := st.exec(`UPDATE user_groups SET forecast_scope=? WHERE id=?`, scope, id); err != nil {
			return info, err
		}
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(info.Password), bcrypt.DefaultCost)
	if err != nil {
		return info, err
	}
	for _, account := range []struct{ name, group, role string }{
		{"admin", "", "admin"}, {"internal", "internal", "user"}, {"own", "own", "user"},
		{"external-all", "external-all", "user"}, {"external-own", "external-own", "user"}, {"none", "", "user"},
	} {
		if err := st.UpsertUser(User{Username: account.name, Role: account.role, PasswordHash: string(hash)}); err != nil {
			return info, err
		}
		if account.group != "" {
			if err := st.SetPrimaryGroup(account.name, info.Groups[account.group]); err != nil {
				return info, err
			}
		}
	}
	if err := st.CreateToken("tok-all", "forecast-demo-seed", "all", ""); err != nil {
		return info, err
	}
	cases := forecastDemoCases()
	market := map[string]forecastDemoCase{}
	for _, sample := range cases {
		if _, exists := market[sample.symbol]; !exists {
			market[sample.symbol] = sample
		}
		made, err := time.ParseInLocation("2006-01-02", sample.date, chinaZone)
		if err != nil {
			return info, err
		}
		made = made.Add(21 * time.Hour)
		s.forecastReceiptForTest = func() time.Time { return made }
		raw := predWire(func(m map[string]any) {
			m["horizon_end"] = made.AddDate(1, 0, 0).Format("2006-01-02")
			price := "100.00"
			if sample.variant == "mismatch" {
				price = "99.99"
			}
			m["baseline"] = map[string]any{"price": price, "as_of": sample.date, "source": "company_quote"}
			claim(m, "stance", func(c map[string]any) {
				c["stance"] = sample.stance
				if sample.stance == "no_position" {
					c["target_weight"] = "0.0000"
				}
			})
			claim(m, "expected_terminal", func(c map[string]any) { c["price"] = "120.00" })
			for key, price := range map[string]string{"scenario.bull": "150.00", "scenario.base": "120.00", "scenario.bear": "95.00"} {
				claim(m, key, func(c map[string]any) { c["price"] = price })
			}
			claim(m, "entry_price", func(c map[string]any) { c["price"] = firstNonEmpty(sample.entry, "90.00") })
			if sample.policy != "" {
				m["policy_version"] = sample.policy
			}
			switch sample.variant {
			case "test":
				m["run_mode"] = "test"
			case "backtest":
				m["information_cutoff"] = made.Add(-7 * 24 * time.Hour).Format(time.RFC3339)
			case "degraded":
				m["decision_quality"] = "degraded"
			case "rejected":
				m["schema"] = "invalid.schema"
			}
		})
		if sample.variant == "absent" {
			raw = nil
		}
		if sample.variant == "off-benchmark" {
			if err := st.SetSetting(setForecastBench, "sh000906"); err != nil {
				return info, err
			}
		}
		title := "[Demo " + sample.label + "] " + sample.symbol
		if sample.label == "duplicate-edition" {
			title = "[Demo " + cases[0].label + "] " + sample.symbol
		}
		envelope := map[string]any{"symbol": sample.symbol, "name": "Synthetic " + sample.symbol, "date": sample.date,
			"time":    made.Format(time.RFC3339),
			"subtype": predTestRtype, "title": title,
			"body_md": "# Synthetic forecast preview\n\nNot a real research report. For layout and verification tests only.",
			"version": sample.version, "predictions": json.RawMessage(raw)}
		if sample.owner != "" {
			envelope["owner_token"] = s.mintOwnerToken(info.Groups[sample.owner], "admin")
		}
		body, err := json.Marshal(envelope)
		if err != nil {
			return info, err
		}
		code, response := ingestPost(t, s, string(body))
		if code != http.StatusOK {
			return info, fmt.Errorf("ingest %s: %d / %v", sample.label, code, response)
		}
		expected := predStateAccepted
		switch sample.variant {
		case "absent":
			expected = predStateAbsent
		case "rejected":
			expected = predStateRejected
		case "test":
			expected = predStateTest
		case "backtest":
			expected = predStateBacktest
		}
		if response["predictions_status"] != expected {
			return info, fmt.Errorf("ingest %s: expected %s predictions, got %v", sample.label, expected, response)
		}
		id, ok := response["id"].(float64)
		if !ok {
			return info, fmt.Errorf("ingest %s lacks report id", sample.label)
		}
		info.Reports[sample.label] = int64(id)
		if sample.variant == "off-benchmark" {
			if err := st.SetSetting(setForecastBench, "sh000852"); err != nil {
				return info, err
			}
		}
	}
	// A genuine manual edition has no prediction row; it must not inherit another edition's set.
	manualID, _, err := st.UpsertReport(Rep{Symbol: cases[0].symbol, Date: cases[0].date, RType: predTestRtype,
		Version: manualVersionName, Title: "[Demo " + cases[0].label + "] " + cases[0].symbol, MD: "# Synthetic manual edition"})
	if err != nil {
		return info, err
	}
	info.Reports["manual-edition"] = manualID
	// Re-ingest a changed body with the identical prediction payload: the original set's body hash stays frozen.
	var priorPayload, priorReceipt string
	if err := st.queryRow(`SELECT payload,made_at FROM prediction_sets WHERE report_id=? ORDER BY id LIMIT 1`, info.Reports["positive-00"]).Scan(&priorPayload, &priorReceipt); err != nil {
		return info, err
	}
	body, _ := json.Marshal(map[string]any{"symbol": cases[0].symbol, "name": "Synthetic " + cases[0].symbol, "date": cases[0].date,
		"time":    priorReceipt,
		"subtype": predTestRtype, "title": "[Demo " + cases[0].label + "] " + cases[0].symbol,
		"body_md": "# Changed synthetic body", "predictions": json.RawMessage(priorPayload)})
	if code, response := ingestPost(t, s, string(body)); code != http.StatusOK ||
		response["id"] != float64(info.Reports["positive-00"]) || response["created"] != false {
		return info, fmt.Errorf("body-change ingest: %d / %v", code, response)
	}
	if err := settleForecastDemo(t, s, market, now); err != nil {
		return info, err
	}
	// Never leave the seed's machine token in the downloadable preview.
	for _, token := range st.ListTokens() {
		if err := st.DeleteToken(token.ID); err != nil {
			return info, err
		}
	}
	s.forecastReceiptForTest = nil
	content := fmt.Sprintf("# Local synthetic preview only. No feature settings belong here.\nlisten: %q\nsecret_key: %q\ndb_driver: sqlite\ndb_path: %q\n", "127.0.0.1:8791", s.cfg.SecretKey, output)
	if _, err := configFile.WriteString(content); err != nil {
		return info, err
	}
	if err := configFile.Sync(); err != nil {
		return info, err
	}
	if err := st.Close(); err != nil {
		return info, err
	}
	complete = true
	return info, nil
}

func forecastDemoCases() []forecastDemoCase {
	var out []forecastDemoCase
	for _, cohort := range []struct {
		label, date string
		n           int
	}{
		{"positive", "2026-01-05", 36}, {"negative", "2026-02-02", 36},
		{"neutral", "2026-03-02", 36}, {"one-arm", "2026-04-06", 36},
		{"policy-slices", "2026-05-04", 72},
		{"csi800", "2026-11-02", 36}, {"pending", "2027-04-05", 36},
		{"small", "2027-05-03", 12}, {"open", "2027-06-18", 36},
	} {
		for i := 0; i < cohort.n; i++ {
			c := forecastDemoCase{label: fmt.Sprintf("%s-%02d", cohort.label, i), symbol: ingestSymbol(len(out) + 1), date: cohort.date, stance: "long", owner: "internal", slope: 30}
			if i%2 == 1 && cohort.label != "one-arm" {
				c.stance, c.slope = "no_position", -10
			}
			if cohort.label == "negative" {
				c.slope = -c.slope
			}
			if i%4 < 2 {
				c.entry = "104.00"
			}
			if cohort.label == "policy-slices" && i >= 36 {
				c.policy = "demo-policy-2"
			}
			if cohort.label == "neutral" {
				c.slope = 0
			}
			if cohort.label == "positive" && i < 10 {
				c.owner = "own-child"
			}
			if cohort.label == "negative" && i < 8 {
				c.owner = "external-own"
			}
			if cohort.label == "pending" && i%4 == 0 {
				c.variant = "source-failure"
			}
			out = append(out, c)
		}
	}
	for _, variant := range []string{"test", "backtest", "absent", "rejected", "degraded", "mismatch", "pending-baseline", "off-benchmark", "corporate-action", "halted", "final-refusal", "pre-live"} {
		c := forecastDemoCase{label: variant, symbol: ingestSymbol(len(out) + 1), date: "2026-01-12", variant: variant, stance: "long", owner: "internal", slope: 10}
		if variant == "pre-live" {
			c.date = "2025-12-30"
		}
		if variant == "pending-baseline" {
			// An unresolved start blocks its entire cohort: keep it away from the mature examples.
			c.date = "2026-06-01"
		}
		out = append(out, c)
	}
	out = append(out, forecastDemoCase{label: "duplicate-edition", symbol: out[0].symbol, date: out[0].date, stance: "long", owner: "internal", version: "demo-second-edition"})
	return out
}

func forecastDemoBars(c forecastDemoCase, from, to string) []QuoteBar {
	made, _ := time.ParseInLocation("2006-01-02", c.date, chinaZone)
	s0 := projectedS0(made.Add(21 * time.Hour))
	dates := predProjectSessions("2025-12-01", 450)
	step := 0
	var out []QuoteBar
	for _, date := range dates {
		if date > to {
			break
		}
		if date > s0 {
			step++
		}
		n := step
		if n > 60 {
			n = 60
		}
		price := int64(10000) + int64(n)*c.slope
		if date < from {
			continue
		}
		if c.variant == "halted" && step >= 5 && step <= 7 {
			continue
		}
		out = append(out, QuoteBar{Date: date, Open: price, Close: price, High: price + 10, Low: price - 10, Volume: 100000})
	}
	if c.variant == "final-refusal" {
		bad := QuoteBar{Date: "2026-01-17", Open: 10000, Close: 10000, High: 10010, Low: 9990, Volume: 100000}
		if bad.Date >= from && bad.Date <= to {
			out = append(out, bad)
			sort.Slice(out, func(i, j int) bool { return out[i].Date < out[j].Date })
		}
	}
	return out
}

func settleForecastDemo(t *testing.T, s *Server, market map[string]forecastDemoCase, at time.Time) error {
	t.Helper()
	now := at
	e := &forecastEvaluator{now: func() time.Time { return now }}
	e.fetch = func(_ context.Context, m, code, from, to string) (*QuoteResp, error) {
		if _, ok := forecastBenchAllowlist[m+code]; ok {
			c := forecastDemoCase{date: "2025-12-01"}
			return &QuoteResp{Kind: quoteKindIndex, Source: quoteSourceTencent, BarsSource: quoteSourceTencent,
				Bars: forecastDemoBars(c, from, to), Snapshot: QuoteSnapshot{AsOf: now.Format(time.RFC3339)}}, nil
		}
		c, ok := market[code]
		if !ok {
			return nil, fmt.Errorf("unknown synthetic market target %s%s", m, code)
		}
		if c.variant == "source-failure" || c.variant == "pending-baseline" {
			return nil, errors.New("synthetic source unavailable")
		}
		r := &QuoteResp{Kind: quoteKindStock, Source: quoteSourceTencent, BarsSource: quoteSourceTencent,
			Bars: forecastDemoBars(c, from, to), Snapshot: QuoteSnapshot{AsOf: now.Format(time.RFC3339)}}
		if c.variant == "corporate-action" {
			r.ExDates = []string{"2026-01-15"}
		}
		return r, nil
	}
	e.adjust = func(_ context.Context, _, code, from, to string) (map[string]int64, error) {
		c, ok := market[code]
		if !ok {
			return nil, fmt.Errorf("unknown synthetic adjusted target %s", code)
		}
		out := map[string]int64{}
		for _, b := range forecastDemoBars(c, from, to) {
			out[b.Date] = b.Close
		}
		return out, nil
	}
	// Four separated retry passes allow genuine mark-specific faults to reach finality. Source
	// failures retain attempt zero; future checkpoints retain their real scheduled wakes.
	for day := 4; day >= 0; day-- {
		now = at.AddDate(0, 0, -day)
		for batch := 0; batch < 40; batch++ {
			work, err := s.st.ForecastSetsDue(now, predEvalBatch)
			if err != nil {
				return err
			}
			if len(work) == 0 {
				break
			}
			if err := e.tick(s); err != nil {
				return err
			}
			if batch == 39 {
				return errors.New("demo evaluator did not drain its bounded batches")
			}
		}
	}
	return nil
}

func demoPreviewServer(t *testing.T, path string) *Server {
	t.Helper()
	st, err := OpenStore("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return &Server{st: st}
}

func demoGroupsJSON(t *testing.T, s *Server) []byte {
	t.Helper()
	r := httptest.NewRecorder()
	s.apiForecastGroups(r, httptest.NewRequest("GET", "/api/admin/forecast/groups", nil), "admin")
	if r.Code != 200 {
		t.Fatalf("demo groups = %d %s", r.Code, r.Body.String())
	}
	return r.Body.Bytes()
}

func TestForecastDemoSeedIsDeterministic(t *testing.T) {
	t.Setenv("RP_FORECAST_DEMO_NOW", "2027-06-20T07:00:00Z")
	paths := []string{filepath.Join(t.TempDir(), "portal.db"), filepath.Join(t.TempDir(), "portal.db")}
	var first []byte
	for _, path := range paths {
		info, err := writeForecastDemoDB(t, path)
		if err != nil {
			t.Fatal(err)
		}
		cfg, err := config.LoadConfig(filepath.Join(filepath.Dir(path), "config.yaml"))
		if err != nil || cfg.Listen != "127.0.0.1:8791" || cfg.DBSource() != path {
			t.Fatalf("demo config = %+v / %v", cfg, err)
		}
		for _, f := range []string{path, filepath.Join(filepath.Dir(path), "config.yaml")} {
			stat, err := os.Stat(f)
			if err != nil || stat.Mode().Perm() != 0o600 {
				t.Fatalf("demo file permissions: %s / %v", f, err)
			}
		}
		s := demoPreviewServer(t, path)
		got := demoGroupsJSON(t, s)
		if first == nil {
			first = append([]byte(nil), got...)
		} else if !bytes.Equal(first, got) {
			t.Fatal("two real-ingest/evaluator seeds differ at the same read clock")
		}
		var groups forecastGroupsResponse
		if err := json.Unmarshal(got, &groups); err != nil {
			t.Fatal(err)
		}
		states := map[string]bool{}
		positive, negative, neutral, oneArm, policySlices, filled := false, false, false, false, false, false
		memberStates := map[string]bool{}
		coverage := forecastCoverage{}
		for _, group := range groups.Groups {
			if group.Label == "2026-01" && group.Cells["d5"].State != "mature" {
				t.Errorf("positive demo cohort was blocked by a fault fixture: %+v", group.Cells["d5"])
			}
			coverage.PreLive += group.Coverage.PreLive
			coverage.Test += group.Coverage.Test
			coverage.Rejected += group.Coverage.Rejected
			coverage.Absent += group.Coverage.Absent
			coverage.Backtest += group.Coverage.Backtest
			coverage.OffBenchmark += group.Coverage.OffBenchmark
			coverage.Duplicate += group.Coverage.Duplicate
			coverage.Degraded += group.Coverage.Degraded
			coverage.Mismatch += group.Coverage.Mismatch
			coverage.PendingBaseline += group.Coverage.PendingBaseline
			for cp, cell := range group.Cells {
				states[cell.State] = true
				oneArm = oneArm || (cell.State == "mature" && cell.SpreadOneArm)
				policySlices = policySlices || (len(cell.Slices) == 2 && cell.Slices[0].State == "mature" && cell.Slices[1].State == "mature")
				filled = filled || (cell.State == "mature" && cell.Fill != nil && *cell.Fill > 0 && *cell.Fill < 1 && cell.FillRetBp != nil)
				if !memberStates[cell.State] {
					rec := httptest.NewRecorder()
					req := httptest.NewRequest("GET", "/api/admin/forecast/members?label="+group.Label+"&checkpoint="+cp+"&bench="+group.Bench, nil)
					s.apiForecastGroupMembers(rec, req, "admin")
					var members forecastMembersResponse
					if err := json.Unmarshal(rec.Body.Bytes(), &members); err != nil || rec.Code != 200 || members.State != cell.State {
						t.Fatalf("seed matrix/drawer disagree for %s/%s: %d %s / %v", group.Label, cp, rec.Code, rec.Body.String(), err)
					}
					memberStates[cell.State] = true
				}
				if cell.State == "mature" && cell.SpreadBp != nil {
					neutral = neutral || *cell.SpreadBp == 0
					if cell.SpreadQ != nil && *cell.SpreadQ < .05 {
						positive = positive || *cell.SpreadBp > 0
						negative = negative || *cell.SpreadBp < 0
					}
				}
			}
		}
		for _, state := range []string{"open", "pending", "small", "mature"} {
			if !states[state] {
				t.Errorf("seed lacks matrix state %s", state)
			}
		}
		if !positive || !negative || !neutral || !oneArm {
			t.Errorf("mature signal cases missing: positive=%v negative=%v neutral=%v one-arm=%v", positive, negative, neutral, oneArm)
		}
		if !policySlices || !filled {
			t.Errorf("drawer scenarios missing: mature policy slices=%v filled and unfilled=%v", policySlices, filled)
		}
		sets, err := s.st.QueryPredictionSets(PredictionFilter{ReportID: info.Reports["positive-00"], Limit: 10}, nil)
		if err != nil || len(sets) != 1 || !sets[0].BodyChanged {
			t.Fatalf("body-change scenario lost original set: %+v / %v", sets, err)
		}
		report, err := s.st.GetNew(info.Reports["positive-00"], nil)
		if err != nil || report == nil || report.Time != sets[0].MadeAt {
			t.Fatalf("report receipt differs from its ledger receipt: %+v / %v", report, err)
		}
		manualSets, err := s.st.QueryPredictionSets(PredictionFilter{ReportID: info.Reports["manual-edition"], Limit: 10}, nil)
		if err != nil || len(manualSets) != 0 {
			t.Fatalf("manual edition inherited another edition's predictions: %+v / %v", manualSets, err)
		}
		if coverage.PreLive == 0 || coverage.Test == 0 || coverage.Rejected == 0 || coverage.Absent == 0 ||
			coverage.Backtest == 0 || coverage.OffBenchmark == 0 || coverage.Duplicate == 0 || coverage.Degraded == 0 ||
			coverage.Mismatch == 0 || coverage.PendingBaseline == 0 {
			t.Errorf("coverage cases missing: %+v", coverage)
		}
		var own, external, tokens, halted, corp, final, orphanEdition int
		if err := s.st.queryRow(`SELECT COUNT(*) FROM prediction_sets WHERE attributed_ou=?`, info.Groups["own-child"]).Scan(&own); err != nil {
			t.Fatal(err)
		}
		if err := s.st.queryRow(`SELECT COUNT(*) FROM user_groups WHERE id=? AND restricted=1 AND forecast_scope='all'`, info.Groups["external-all"]).Scan(&external); err != nil {
			t.Fatal(err)
		}
		for query, target := range map[string]*int{
			`SELECT COUNT(*) FROM api_tokens`:                                                                          &tokens,
			`SELECT COUNT(*) FROM prediction_marks WHERE halted=1`:                                                     &halted,
			`SELECT COUNT(*) FROM prediction_marks WHERE corp_action=1`:                                                &corp,
			`SELECT COUNT(*) FROM prediction_marks WHERE status='refused_final'`:                                       &final,
			`SELECT COUNT(*) FROM reports r WHERE NOT EXISTS (SELECT 1 FROM prediction_sets p WHERE p.report_id=r.id)`: &orphanEdition,
		} {
			if err := s.st.queryRow(query).Scan(target); err != nil {
				t.Fatal(err)
			}
		}
		if own < 1 || own >= 30 || external != 1 || tokens != 0 || halted == 0 || corp == 0 || final == 0 || orphanEdition == 0 {
			t.Errorf("scenario inventory: own=%d external=%d tokens=%d halted=%d corp=%d final=%d missing-edition=%d",
				own, external, tokens, halted, corp, final, orphanEdition)
		}
	}
}

func TestForecastDemoSeedRefusesExistingOutputs(t *testing.T) {
	for _, name := range []string{"portal.db", "config.yaml", "portal.db-wal", "portal.db-shm"} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			guarded := filepath.Join(dir, name)
			if err := os.WriteFile(guarded, []byte("keep-existing-data"), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := writeForecastDemoDB(t, filepath.Join(dir, "portal.db")); err == nil {
				t.Fatal("existing output was accepted")
			}
			got, err := os.ReadFile(guarded)
			if err != nil || string(got) != "keep-existing-data" {
				t.Fatalf("existing file changed: %q / %v", got, err)
			}
			entries, err := os.ReadDir(dir)
			if err != nil || len(entries) != 1 {
				t.Fatalf("refused seed left new output: %v / %v", entries, err)
			}
		})
	}
}

func TestForecastDemoSeedRefusesSymlinkOutputs(t *testing.T) {
	for _, name := range []string{"portal.db", "config.yaml", "portal.db-wal", "portal.db-shm"} {
		for _, dangling := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/dangling=%v", name, dangling), func(t *testing.T) {
				dir := t.TempDir()
				target := filepath.Join(t.TempDir(), "external-data")
				if !dangling {
					if err := os.WriteFile(target, []byte("keep-existing-data"), 0o600); err != nil {
						t.Fatal(err)
					}
				}
				link := filepath.Join(dir, name)
				if err := os.Symlink(target, link); err != nil {
					t.Fatal(err)
				}
				if _, err := writeForecastDemoDB(t, filepath.Join(dir, "portal.db")); err == nil {
					t.Fatal("symlink output was accepted")
				}
				if got, err := os.Readlink(link); err != nil || got != target {
					t.Fatalf("refused seed changed symlink: %s / %v", got, err)
				}
				data, err := os.ReadFile(target)
				if dangling {
					if !os.IsNotExist(err) {
						t.Fatalf("refused seed created target: %q / %v", data, err)
					}
				} else if err != nil || string(data) != "keep-existing-data" {
					t.Fatalf("refused seed changed target: %q / %v", data, err)
				}
				entries, err := os.ReadDir(dir)
				if err != nil || len(entries) != 1 {
					t.Fatalf("refused seed left outputs: %v / %v", entries, err)
				}
			})
		}
	}
}
