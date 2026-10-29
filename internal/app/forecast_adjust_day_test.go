package app

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func TestTencentQfqDayOnlyCapturedResponses(t *testing.T) {
	for _, code := range []string{"688469", "688825"} {
		body := readQuoteFixture(t, "tencent_qfq_day_sh"+code+".json")
		got, err := replayForecastAdjustmentBody(t, "sh", code, "2026-10-22", "2026-10-27", body)
		if err != nil || len(got) == 0 || got["2026-10-27"] == 0 {
			t.Fatalf("qfq endpoint day response %s: %v / %v", code, got, err)
		}
		var env map[string]any
		if err := json.Unmarshal(body, &env); err != nil {
			t.Fatal(err)
		}
		data := env["data"].(map[string]any)["sh"+code].(map[string]any)
		for _, bad := range []any{nil, []any{}, "bad"} {
			data["qfqday"] = bad
			b, _ := json.Marshal(env)
			if got, err := replayForecastAdjustmentBody(t, "sh", code, "2026-10-22", "2026-10-27", b); err == nil || len(got) != 0 {
				t.Fatal("corrupt explicit qfqday fell back")
			}
		}
	}
}

func TestCapturedDayOnlyAdjustmentReachesDryPlan(t *testing.T) {
	for _, code := range []string{"688469", "688825"} {
		t.Run(code, func(t *testing.T) {
			s := newV1Server(t)
			id := seedAcceptedSetAt(t, s, code, "2026-10-19")
			body := readQuoteFixture(t, "tencent_qfq_day_sh"+code+".json")
			stock, err := parseTencentQuote("sh", code, body, 0)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.st.exec(`UPDATE prediction_sets SET made_at=?,made_on=?,s0_date=?,base_date=?,base_check=?,s0_fen=? WHERE id=?`, stock.Bars[0].Date+"T02:00:00Z", stock.Bars[0].Date, stock.Bars[0].Date, stock.Bars[0].Date, baseMatch, stock.Bars[0].Close, id); err != nil {
				t.Fatal(err)
			}
			if err := s.st.SetSetting(setForecastAuto, forecastModeDry); err != nil {
				t.Fatal(err)
			}
			now := shanghai(2026, time.October, 29, 17, 0)
			e := &forecastEvaluator{now: func() time.Time { return now }}
			e.fetch = func(_ context.Context, _, c, _, _ string) (*QuoteResp, error) {
				if c == "000852" {
					return &QuoteResp{Kind: quoteKindIndex, Source: "tencent", Bars: barsFor(tradingDays("2026-10-19", 9), 4000), Snapshot: QuoteSnapshot{AsOf: "2026-10-29T16:14:00+08:00"}}, nil
				}
				return stock, nil
			}
			e.adjust = func(_ context.Context, m, c, f, to string) (map[string]int64, error) {
				return replayForecastAdjustmentBody(t, m, c, f, to, body)
			}
			before := forecastChecksum(t, s)
			e.step(s, "manual")
			hb, _ := s.st.ForecastHeartbeat()
			if hb.Pass.Resolved != 1 || hb.Pass.Refused["fetch_failed"] != 0 || hb.Pass.Errors != 0 {
				t.Fatalf("day-only d5 failed: %+v", hb.Pass)
			}
			if forecastChecksum(t, s) != before {
				t.Fatal("dry replay modified ledger")
			}
		})
	}
}
