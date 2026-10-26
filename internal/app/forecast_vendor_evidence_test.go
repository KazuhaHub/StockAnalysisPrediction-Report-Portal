package app

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"
)

type forecastVendorTransport func(*http.Request) (*http.Response, error)

func (f forecastVendorTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// Replay the actual fetch entry point, not only a parser with a hand-selected row key.
// No test in this package runs in parallel; restore the pooled client before the next test.
func replayForecastAdjustment(t *testing.T, market, code, from, to, fixture string) map[string]int64 {
	t.Helper()
	body := readQuoteFixture(t, fixture)
	got, err := replayForecastAdjustmentBody(t, market, code, from, to, body)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func replayForecastAdjustmentBody(t *testing.T, market, code, from, to string, body []byte) (map[string]int64, error) {
	t.Helper()
	previous := vendorClient
	defer func() { vendorClient = previous }()
	calls := 0
	vendorClient = &http.Client{Transport: forecastVendorTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		end, err := time.Parse(quoteWindowLayout, to)
		if err != nil {
			t.Fatal(err)
		}
		want := market + code + ",day," + from + "," + end.AddDate(0, 0, 1).Format(quoteWindowLayout) + "," + strconv.Itoa(quoteMaxBars) + ",qfq"
		if r.Method != http.MethodGet || r.URL.Host != "web.ifzq.gtimg.cn" ||
			r.URL.Path != "/appstock/app/fqkline/get" || r.URL.Query().Get("param") != want {
			t.Errorf("unexpected adjustment request: %s %s", r.Method, r.URL)
		}
		if r.Header.Get("Referer") != quoteTencentReferer {
			t.Error("adjustment request lost the vendor referer")
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header),
			Body: io.NopCloser(strings.NewReader(string(body))), Request: r}, nil
	})}
	got, err := fetchTencentAdjustRatio(context.Background(), market, code, from, to)
	if calls != 1 {
		t.Fatalf("adjustment requests = %d, want 1", calls)
	}
	return got, err
}

func forecastVendorBody(t *testing.T, market, code string) *QuoteResp {
	t.Helper()
	r, err := parseTencentQuote(market, code,
		readQuoteFixture(t, "tencent_fqkline_bfq_"+market+code+".json"), 0)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// The real capital-reserve issue is under the daily limit, so that backstop alone cannot detect it.
// The vendor's seventh element names the transfer but has no cash dividend, so ExDates is empty.
func TestCapturedBonusIssueAdjustmentReachesSettlement(t *testing.T) {
	raw := forecastVendorBody(t, "sz", "001323")
	adj := replayForecastAdjustment(t, "sz", "001323", "2025-08-11", "2025-08-25",
		"tencent_fqkline_qfq_sz001323.json")
	if len(raw.Bars) != 11 || len(adj) != 11 {
		t.Fatalf("raw/adjusted bars = %d/%d, want 11/11", len(raw.Bars), len(adj))
	}
	if adj["2025-08-20"] != 2819 || adj["2025-08-21"] != 2871 {
		t.Fatalf("adjusted closes = %v, want 2819 and 2871 fen", adj)
	}
	base, due := barOn(raw.Bars, "2025-08-20"), barOn(raw.Bars, "2025-08-21")
	if base == nil || due == nil || base.Close != 3258 || due.Close != 3015 {
		t.Fatalf("raw closes = %+v / %+v, want 3258 / 3015 fen", base, due)
	}
	if len(raw.ExDates) != 0 {
		t.Fatal("non-cash transfer unexpectedly has a cash-dividend signal")
	}
	if beyondLimitPrice(base.Close, due.Close, predDailyLimitBp("001323")) ||
		corporateActionBetween("001323", raw.Bars, nil, nil, base.Date, due.Date, "") {
		t.Fatal("daily-limit backstop already flags the captured event; ratio probe is not independent")
	}
	index := forecastVendorBody(t, "sh", "000852")
	var sessions []string
	for _, b := range index.Bars {
		sessions = append(sessions, b.Date)
	}
	in := settleInput{
		Set: forecastWorkSet{Symbol: "001323"}, Mark: forecastMark{Checkpoint: "d5"},
		Stock: raw.Bars, Index: index.Bars, Sessions: sessions, ExDates: raw.ExDates,
		AsOf: raw.Snapshot.AsOf, Source: raw.Source,
		S0: base.Date, BaseDate: base.Date, S0Fen: base.Close, Session: due.Date,
		Adj: adj, AdjChecked: true, Now: shanghai(2026, time.October, 22, 17, 0),
	}
	res := settleMark(in)
	if res.Status != markResolved || !res.CorpAction || res.CloseFen == nil || *res.CloseFen != 3015 ||
		res.RetBp == nil || *res.RetBp != -746 || res.Source != quoteSourceTencent {
		t.Fatalf("real bonus settlement = %+v, want flagged with raw close/return 3015/-746", res)
	}
	// A positive control for necessity: with no ratio check or event element, this raw loss is scored.
	in.Adj, in.AdjChecked = nil, false
	if control := settleMark(in); control.Status != markResolved || control.CorpAction {
		t.Fatalf("raw-only control = %+v, want an unflagged resolved loss", control)
	}
}

func TestCapturedAdjustmentRowsStayDetectionOnly(t *testing.T) {
	adjusted := readQuoteFixture(t, "tencent_fqkline_qfq_sz001323.json")
	// Even a qfq-only response cannot supply candles to the ordinary unadjusted reader.
	normal, err := parseTencentQuote("sz", "001323", adjusted, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(normal.Bars) != 0 || normal.BarsSource != "" {
		t.Fatal("ordinary quote reader consumed adjusted candles")
	}
	var rawEnv, adjEnv map[string]json.RawMessage
	if err := json.Unmarshal(readQuoteFixture(t, "tencent_fqkline_bfq_sz001323.json"), &rawEnv); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(adjusted, &adjEnv); err != nil {
		t.Fatal(err)
	}
	var rawData, adjData map[string]map[string]json.RawMessage
	if err := json.Unmarshal(rawEnv["data"], &rawData); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(adjEnv["data"], &adjData); err != nil {
		t.Fatal(err)
	}
	rawData["sz001323"]["qfqday"] = adjData["sz001323"]["qfqday"]
	withRows := func(rows json.RawMessage) []byte {
		t.Helper()
		rawData["sz001323"]["qfqday"] = rows
		var err error
		rawEnv["data"], err = json.Marshal(rawData)
		if err != nil {
			t.Fatal(err)
		}
		body, err := json.Marshal(rawEnv)
		if err != nil {
			t.Fatal(err)
		}
		return body
	}
	both := withRows(adjData["sz001323"]["qfqday"])
	normal, err = parseTencentQuote("sz", "001323", both, 0)
	if err != nil || len(normal.Bars) != 11 || normal.Bars[7].Close != 3258 {
		t.Fatalf("ordinary reader did not retain unadjusted day rows: %+v / %v", normal, err)
	}
	got, err := replayForecastAdjustmentBody(t, "sz", "001323", "2025-08-11", "2025-08-25", both)
	if err != nil || got["2025-08-20"] != 2819 {
		t.Fatalf("detection did not prefer qfqday: %v / %v", got, err)
	}
	for name, rows := range map[string]json.RawMessage{
		"empty": json.RawMessage(`[]`), "null": json.RawMessage(`null`),
		"wrong_type":     json.RawMessage(`"bad"`),
		"short_row":      json.RawMessage(`[["2025-08-20","28.192"]]`),
		"invalid_price":  json.RawMessage(`[["2025-08-20","28.192","oops","29","27","8438"]]`),
		"impossible_bar": json.RawMessage(`[["2025-08-20","28","50","29","27","8438"]]`),
	} {
		t.Run(name, func(t *testing.T) {
			got, err := replayForecastAdjustmentBody(t, "sz", "001323", "2025-08-11", "2025-08-25", withRows(rows))
			if err == nil || len(got) != 0 {
				t.Fatalf("bad adjusted rows fell back to raw candles: %v / %v", got, err)
			}
		})
	}
	// A missing adjusted key is not permission to skip the source check either.
	got, err = replayForecastAdjustmentBody(t, "sz", "001323", "2025-08-11", "2025-08-25",
		readQuoteFixture(t, "tencent_fqkline_bfq_sz001323.json"))
	if err == nil || len(got) != 0 {
		t.Fatalf("missing qfqday accepted unadjusted rows: %v / %v", got, err)
	}
}

func TestCapturedOrdinaryLimitDownIsNotAnAdjustment(t *testing.T) {
	raw := forecastVendorBody(t, "sh", "600221")
	adj := replayForecastAdjustment(t, "sh", "600221", "2024-09-01", "2024-10-31",
		"tencent_fqkline_qfq_sh600221.json")
	if len(raw.Bars) != 37 || len(adj) != 37 {
		t.Fatalf("raw/adjusted bars = %d/%d, want 37/37", len(raw.Bars), len(adj))
	}
	base, due := barOn(raw.Bars, "2024-10-08"), barOn(raw.Bars, "2024-10-09")
	if base == nil || due == nil || base.Close != 132 || due.Close != 119 ||
		adj[base.Date] != 132 || adj[due.Date] != 119 {
		t.Fatalf("captured low-price limit-down pair changed: %+v / %+v / %v", base, due, adj)
	}
	if adjustRatioChanged(adj[base.Date], base.Close, adj[due.Date], due.Close) ||
		corporateActionBetween("600221", raw.Bars, nil, raw.ExDates, base.Date, due.Date, "") {
		t.Fatal("ordinary captured limit-down day was excluded as an adjustment")
	}
}

func TestCapturedBoardMovesStayInsideRoundedLimits(t *testing.T) {
	for _, tc := range []struct {
		market, code, before, after string
		previous, close, limit      int64
	}{
		{"sz", "301308", "2024-09-30", "2024-10-08", 8887, 10664, 2000},
		{"sh", "689009", "2024-09-30", "2024-10-08", 4820, 5680, 2000},
		{"sh", "600221", "2024-10-30", "2024-10-31", 125, 138, 1000},
	} {
		t.Run(tc.market+tc.code, func(t *testing.T) {
			raw := forecastVendorBody(t, tc.market, tc.code)
			before, after := barOn(raw.Bars, tc.before), barOn(raw.Bars, tc.after)
			if before == nil || after == nil || before.Close != tc.previous || after.Close != tc.close {
				t.Fatalf("captured closes = %+v / %+v, want %d / %d", before, after, tc.previous, tc.close)
			}
			if got := predDailyLimitBp(tc.code); got != tc.limit {
				t.Fatalf("board limit = %d, want %d", got, tc.limit)
			}
			if beyondLimitPrice(before.Close, after.Close, tc.limit) ||
				corporateActionBetween(tc.code, raw.Bars, nil, raw.ExDates, tc.before, tc.after, "") {
				t.Fatal("legal captured move was excluded as a corporate action")
			}
		})
	}
}
