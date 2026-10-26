package app

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// Replay both observed URL boundaries. The snapshot already describes the completed
// due session in both captures, but only the following-day URL includes its candle.
func adjustmentBoundaryClient(t *testing.T) *int {
	t.Helper()
	previous := vendorClient
	t.Cleanup(func() { vendorClient = previous })
	calls := new(int)
	vendorClient = &http.Client{Transport: forecastVendorTransport(func(r *http.Request) (*http.Response, error) {
		*calls++
		param := strings.Split(r.URL.Query().Get("param"), ",")
		if len(param) != 6 || param[0] != "sh600519" || param[5] != "qfq" {
			t.Fatalf("unexpected adjustment request: %s", r.URL)
		}
		fixture := "tencent_qfq_sh600519_end_20261026.json"
		if param[3] == "2026-10-27" {
			fixture = "tencent_qfq_sh600519_end_20261027.json"
		} else if param[3] != "2026-10-26" {
			t.Fatalf("unexpected vendor end: %s", param[3])
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header),
			Body: io.NopCloser(strings.NewReader(string(readQuoteFixture(t, fixture)))), Request: r}, nil
	})}
	return calls
}

func TestTencentAdjustmentIncludesCompletedDueSession(t *testing.T) {
	calls := adjustmentBoundaryClient(t)
	got, err := fetchTencentAdjustRatio(context.Background(), "sh", "600519", "2026-10-19", "2026-10-26")
	if err != nil {
		t.Fatal(err)
	}
	if got["2026-10-19"] != 132497 || got["2026-10-26"] != 134998 {
		t.Fatalf("adjustment window lost a required close: %v", got)
	}
	if *calls != 1 {
		t.Fatalf("requests = %d, want one", *calls)
	}
}

func TestTencentAdjustmentTrimsExpandedWindow(t *testing.T) {
	var body map[string]any
	if err := json.Unmarshal(readQuoteFixture(t, "tencent_qfq_sh600519_end_20261027.json"), &body); err != nil {
		t.Fatal(err)
	}
	data := body["data"].(map[string]any)["sh600519"].(map[string]any)
	rows := data["qfqday"].([]any)
	// Deliberate mutation: an inclusive vendor could also return the expanded end.
	rows = append(rows, []string{"2026-10-27", "1349.98", "1349.98", "1349.98", "1349.98", "41599"})
	data["qfqday"] = rows
	mutated, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	got, err := replayForecastAdjustmentBody(t, "sh", "600519", "2026-10-20", "2026-10-26", mutated)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 5 || got["2026-10-26"] != 134998 || got["2026-10-19"] != 0 || got["2026-10-27"] != 0 {
		t.Fatalf("expanded request escaped the accepted window: %v", got)
	}
}

func TestTencentAdjustmentRejectsInvalidWindowBeforeHTTP(t *testing.T) {
	calls := adjustmentBoundaryClient(t)
	for _, dates := range [][2]string{{"", ""}, {"2026-10-19", ""}, {"2026-10-26", "2026-10-19"}, {"2026-10-19", "invalid"}, {"9999-12-31", "9999-12-31"}} {
		if _, err := fetchTencentAdjustRatio(context.Background(), "sh", "600519", dates[0], dates[1]); err == nil {
			t.Fatalf("accepted invalid dates %v", dates)
		}
	}
	if *calls != 0 {
		t.Fatalf("invalid windows reached vendor: %d", *calls)
	}
}

func TestCompletedDueSessionAdjustmentReachesDryPlan(t *testing.T) {
	adjustmentBoundaryClient(t)
	s := newV1Server(t)
	setID := seedAcceptedSetAt(t, s, "600519", "2026-10-16")
	if err := s.st.SetSetting(setForecastAuto, forecastModeDry); err != nil {
		t.Fatal(err)
	}
	stock, err := parseTencentQuote("sh", "600519", readQuoteFixture(t, "tencent_bfq_sh600519_end_20261027.json"), 0)
	if err != nil {
		t.Fatal(err)
	}
	// A synthetic index isolates the real stock/adjustment boundary from calendar delivery.
	dates := tradingDays("2026-10-19", 6)
	now := shanghai(2026, time.October, 26, 17, 0)
	e := &forecastEvaluator{now: func() time.Time { return now }}
	e.fetch = func(_ context.Context, _, code, _, _ string) (*QuoteResp, error) {
		if code == "000852" {
			return &QuoteResp{Kind: quoteKindIndex, Source: "tencent", Bars: barsFor(dates, 4000),
				Snapshot: QuoteSnapshot{AsOf: "2026-10-26T16:14:00+08:00"}}, nil
		}
		return stock, nil
	}
	before := forecastChecksum(t, s)
	e.step(s, "manual")
	hb, _ := s.st.ForecastHeartbeat()
	if hb.Pass.Resolved != 1 || hb.Pass.Refused["fetch_failed"] != 0 {
		t.Fatalf("completed d5 did not reach the dry plan: %+v", hb.Pass)
	}
	found := false
	for _, sample := range hb.DrySample {
		if sample.SetID == setID && sample.Kind == "settle" && sample.Mark == "d5" {
			found = true
		}
	}
	if !found || forecastChecksum(t, s) != before {
		t.Fatal("dry evaluation must plan d5 without changing the ledger")
	}
}
