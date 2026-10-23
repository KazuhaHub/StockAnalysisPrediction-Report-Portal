package app

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/config"
)

var forecastReadingBenchmark = flag.Bool("forecast-reading-benchmark", false, "measure the local stock reading path on a fixed fixture")
var forecastReadingBaseline = flag.Bool("forecast-reading-baseline", false, "expect the pre-P18 handler shape when measuring its archived source")
var forecastReadingDriver = flag.String("forecast-reading-driver", "sqlite", "sqlite or postgres for the local reading measurement")

// This opt-in measurement deliberately uses only pre-P18 fixture helpers so the identical file
// can measure the archived parent source. Default tests do not turn local timing into a CI gate.
func TestForecastReadingStockBenchmark(t *testing.T) {
	if !*forecastReadingBenchmark {
		t.Skip("opt-in local performance measurement")
	}
	s := newV1Server(t)
	switch *forecastReadingDriver {
	case "sqlite":
	case "postgres":
		st := pgStore(t, "prediction_marks", "prediction_claims", "prediction_sets", "report_viewers", "reports", "version_grants", "report_versions", "users", "user_groups")
		s = &Server{st: st, names: LoadNames(t.TempDir(), st)}
		s.names.fetch = func(string) string { return "" }
	default:
		t.Fatalf("unknown measurement driver: %s", *forecastReadingDriver)
	}
	if err := s.st.DeleteSetting(setForecastPopulationFrom); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.st.DeleteSetting("forecast_reader"); err != nil {
			t.Error(err)
		}
	})
	s.cfg = &config.Config{SecretKey: "0123456789abcdef0123456789abcdef"}
	s.forecastEval.now = func() time.Time { return dateAtClose("2026-10-22") }
	for _, subject := range []struct {
		user, scope string
		external    bool
	}{
		{"bench-all", scopeAll, false}, {"bench-own", scopeOwn, false}, {"bench-external", scopeAll, true},
	} {
		seedForecastReader(t, s, subject.user, subject.scope, subject.external)
	}
	if err := s.st.SaveVersion(ReportVersion{Name: "bench-client", Visibility: VisibilityAll}); err != nil {
		t.Fatal(err)
	}
	if err := s.st.SetVersionGrants("bench-client", []string{userPrincipal("bench-external")}); err != nil {
		t.Fatal(err)
	}
	body := strings.Repeat("Research fixture: operations, valuation, scenario assumptions and risk boundaries.\n", 500)
	start := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	var selected, selectedSet int64
	for day := 0; day < 60; day++ {
		date := start.AddDate(0, 0, day).Format("2006-01-02")
		for tab := 0; tab < 4; tab++ {
			rtype, version := predTestRtype, "bench-internal"
			if tab == 1 {
				version = "bench-client"
			}
			if tab >= 2 {
				rtype = fmt.Sprintf("uncovered-benchmark-%d", tab)
			}
			report, _, err := s.st.UpsertReport(Rep{Symbol: "600001", Date: date, Kind: kindOrder[1], RType: rtype, Version: version, Title: fmt.Sprintf("Fixture %d %d", day, tab), MD: body})
			if err != nil {
				t.Fatal(err)
			}
			if tab >= 2 {
				continue
			}
			set := seedGroupSet(t, s, gsSeed{Symbol: "600001", MadeOn: date, MadeAt: fmt.Sprintf("%sT%02d:00:00+08:00", date, 10+tab), Bench: defaultForecastBench,
				S0Fen: ip(10000), TerminalFen: ip(12000), HorizonStd: true, Marks: map[string]gsMark{"d5": {Status: markResolved}, "d20": {Status: markPending}}})
			if _, err := s.st.exec(`UPDATE prediction_sets SET report_id=?,version=?,body_sha256=? WHERE id=?`, report, version, sha256Hex([]byte(body+"\x00")), set); err != nil {
				t.Fatal(err)
			}
			if day == 0 && tab == 1 {
				selected, selectedSet = report, set
			}
		}
	}
	for _, month := range []string{"2026-08", "2026-09"} {
		for i := 0; i < 500; i++ {
			seedGroupSet(t, s, gsSeed{Symbol: fmt.Sprintf("%06d", 650000+i), MadeOn: month + "-01", MadeAt: month + "-01T12:00:00+08:00", Bench: defaultForecastBench, S0Fen: ip(10000), TerminalFen: ip(12000)})
		}
	}
	mux := http.NewServeMux()
	s.wireRoutes(mux)
	handler := gzipMiddleware(securityHeadersMiddleware(mux))
	q := url.Values{"date": {"2026-08-01"}, "kind": {kindOrder[1]}, "r": {itoa64(selected)}}
	path := "/api/stock/600001?" + q.Encode()
	stages := []string{"card", "full"}
	if *forecastReadingBaseline {
		stages = []string{"card"}
	}
	t.Logf("fixture: %s, 240 reports, 60 dates, 4 tabs/date, body=%d B, 1120 accepted sets, 2 editions, 100 measured real + 100 prefetch per profile, gzip + security + real route/session wrappers; baseline=%v", *forecastReadingDriver, len(body), *forecastReadingBaseline)
	for _, stage := range stages {
		if err := s.st.SetSetting("forecast_reader", stage); err != nil {
			t.Fatal(err)
		}
		for _, user := range []string{"bench-all", "bench-own", "bench-external"} {
			request := sessionForPath(t, s, user, "", path)
			request.Header.Set("Accept-Encoding", "gzip")
			var samples [2][]time.Duration
			var sizes [2]int
			for sample := -10; sample < 200; sample++ {
				prefetch := (sample + 10) % 2
				r := request.Clone(request.Context())
				if prefetch == 1 {
					r.Header.Set("X-Prefetch", "1")
				}
				w := httptest.NewRecorder()
				began := time.Now()
				handler.ServeHTTP(w, r)
				elapsed := time.Since(began)
				validateForecastReadingMeasurement(t, w, user, stage, prefetch == 1, selected, selectedSet)
				if sample >= 0 {
					samples[prefetch] = append(samples[prefetch], elapsed)
					sizes[prefetch] += w.Body.Len()
				}
			}
			combined := append(append([]time.Duration(nil), samples[0]...), samples[1]...)
			for mode, series := range samples {
				p50, p95 := forecastReadingPercentile(series, 50), forecastReadingPercentile(series, 95)
				t.Logf("stage=%s user=%s prefetch=%v n=%d p50_ms=%.3f p95_ms=%.3f gzip_B=%d", stage, user, mode == 1, len(series), float64(p50)/float64(time.Millisecond), float64(p95)/float64(time.Millisecond), sizes[mode]/len(series))
				if !*forecastReadingBaseline && stage == "full" && p95 > 800*time.Millisecond {
					t.Errorf("%s mode=%d exceeds the 800 ms p95 budget: %v", user, mode, p95)
				}
			}
			p95 := forecastReadingPercentile(combined, 95)
			t.Logf("stage=%s user=%s mixed_n=%d mixed_p95_ms=%.3f", stage, user, len(combined), float64(p95)/float64(time.Millisecond))
			if !*forecastReadingBaseline && stage == "full" && p95 > 800*time.Millisecond {
				t.Errorf("%s mixed requests exceed the 800 ms p95 budget: %v", user, p95)
			}
		}
	}
}

func forecastReadingPercentile(samples []time.Duration, percentile int) time.Duration {
	sorted := append([]time.Duration(nil), samples...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	return sorted[(len(sorted)*percentile+99)/100-1]
}

func validateForecastReadingMeasurement(t *testing.T, w *httptest.ResponseRecorder, user, stage string, prefetch bool, report, set int64) {
	t.Helper()
	if w.Code != 200 || w.Header().Get("Content-Encoding") != "gzip" {
		t.Fatalf("measurement did not use the authenticated gzip reading path: %d %s", w.Code, w.Body.String())
	}
	reader, err := gzip.NewReader(bytes.NewReader(w.Body.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(reader)
	reader.Close()
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}
	if out["rep"].(map[string]any)["id"] != float64(report) || len(out["timeline"].([]any)) != 60 {
		t.Fatal("measurement did not read the selected fixture")
	}
	prediction, present := out["prediction"]
	full := !*forecastReadingBaseline && stage == "full"
	if present != (full && !prefetch) {
		t.Fatal("measurement silently skipped or unexpectedly performed summary work")
	}
	if present {
		summary := prediction.(map[string]any)
		if summary["display_set_id"] != float64(set) {
			t.Fatal("measurement used the wrong prediction")
		}
		_, counted := summary["counted_edition"]
		if counted != (user == "bench-all") {
			t.Fatal("measurement did not exercise the expected sibling-membership path")
		}
	}
	for _, raw := range out["timeline"].([]any) {
		_, predicted := raw.(map[string]any)["predicted"]
		if predicted != full {
			t.Fatal("measurement did not exercise the expected timeline path")
		}
	}
}
