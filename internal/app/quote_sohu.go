package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

const quoteSourceSohu = "sohu"

// Sohu publishes civil dates, without a snapshot clock or a completed-session marker. Its
// history adapter is deliberately excluded from the live and settlement resolvers.
func quoteSohuSource() quoteSource {
	return quoteSource{name: quoteSourceSohu, optIn: true, historyOnly: true,
		fetch: fetchSohuHistory, fetchRange: fetchSohuHistoryRange,
		knows: func(t quoteTarget) bool { _, err := quoteSohuSymbol(t.Market.id, t.Code); return err == nil },
		caps: []quoteCapability{{markets: func(m *quoteMarket) bool { return m.id == "sh" || m.id == "sz" },
			intervals: []quoteInterval{quoteIntervalDaily, quoteIntervalDailyRange}}}}
}

func quoteSohuSymbol(market, code string) (string, error) {
	if _, err := quoteTargetFor(market, code); err != nil {
		return "", err
	}
	if market == "sh" {
		switch code {
		case "000300", "000905", "000852", "000906":
			return "zs_" + code, nil
		}
		if strings.HasPrefix(code, "6") {
			return "cn_" + code, nil
		}
	}
	if market == "sz" {
		for _, prefix := range []string{"000", "001", "002", "003", "200", "300", "301"} {
			if strings.HasPrefix(code, prefix) {
				return "cn_" + code, nil
			}
		}
	}
	return "", errors.New("quote: no measured Sohu symbol for this instrument")
}

type sohuHistoryEnvelope struct {
	Status *int       `json:"status"`
	Code   string     `json:"code"`
	HQ     [][]string `json:"hq"`
}

func parseSohuHistory(body []byte, market, code, from, to string, now time.Time) (*QuoteResp, error) {
	symbol, err := quoteSohuSymbol(market, code)
	if err != nil {
		return nil, err
	}
	var result []sohuHistoryEnvelope
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("quote: Sohu history: %w", err)
	}
	if len(result) != 1 || result[0].Status == nil || *result[0].Status != 0 || len(result[0].HQ) == 0 || len(result[0].HQ) > quoteMaxBars {
		return nil, errors.New("quote: Sohu history is empty, oversized or unsuccessful")
	}
	if err := quoteCheckCodeEcho(symbol, result[0].Code); err != nil {
		return nil, err
	}
	loc, err := quoteZone("Asia/Shanghai")
	if err != nil {
		return nil, err
	}
	today := now.In(loc).Format("2006-01-02")
	previous := ""
	bars := make([]QuoteBar, 0, len(result[0].HQ))
	for _, row := range result[0].HQ {
		if len(row) != 10 && len(row) != 11 {
			return nil, errQuoteFieldCount
		}
		date, err := time.Parse("2006-01-02", row[0])
		if err != nil || date.Format("2006-01-02") != row[0] || row[0] < from || row[0] > to || (previous != "" && row[0] >= previous) {
			return nil, errors.New("quote: Sohu history dates are not a descending bounded series")
		}
		previous = row[0]
		var values [7]int64
		for i, column := range []int{1, 2, 3, 5, 6, 7, 8} {
			scale := 2
			if column == 8 {
				scale = 4
			}
			values[i], err = quoteScaled(row[column], scale)
			if err != nil {
				return nil, fmt.Errorf("quote: Sohu column %d: %w", column, err)
			}
		}
		bar := QuoteBar{Date: row[0], Open: values[0], Close: values[1], Low: values[3], High: values[4], Volume: values[5]}
		if values[5] < 0 || values[6] < 0 {
			return nil, errors.New("quote: Sohu history has negative volume or amount")
		}
		if err := quoteCheckBars([]QuoteBar{bar}); err != nil {
			return nil, err
		}
		snap := QuoteSnapshot{Last: bar.Close, PrevClose: bar.Close - values[2], ChangePct: strings.TrimSuffix(row[4], "%")}
		if snap.PrevClose <= 0 {
			return nil, errQuoteIdentity
		}
		if err := quoteCheckIdentity(&snap); err != nil {
			return nil, err
		}
		// Even after local market close, a date alone cannot prove this day's data is final.
		if bar.Date < today {
			bars = append(bars, bar)
		}
	}
	if len(bars) == 0 {
		return nil, errors.New("quote: Sohu history has no prior-day rows")
	}
	for i, j := 0, len(bars)-1; i < j; i, j = i+1, j-1 {
		bars[i], bars[j] = bars[j], bars[i]
	}
	last := bars[len(bars)-1]
	kind := "stock"
	if strings.HasPrefix(result[0].Code, "zs_") {
		kind = "index"
	}
	return &QuoteResp{Symbol: code, Market: market, Kind: kind, Currency: "CNY", TZ: "Asia/Shanghai",
		Source: quoteSourceSohu, BarsSource: quoteSourceSohu, Bars: bars,
		HistoryOnly: true, HistoryThrough: last.Date,
		Snapshot: QuoteSnapshot{Last: last.Close, Session: quoteSessionUnknown}}, nil
}

func fetchSohuHistory(ctx context.Context, market, code string, count int) (*QuoteResp, error) {
	if count <= 0 || count > quoteMaxBars {
		return nil, errors.New("quote: invalid Sohu bar count")
	}
	now := time.Now()
	loc, err := quoteZone("Asia/Shanghai")
	if err != nil {
		return nil, err
	}
	// Calendar headroom covers holidays without claiming an exchange calendar. A range longer
	// than the bounded parser can accept is refused rather than silently treated as complete.
	to := now.In(loc).AddDate(0, 0, -1)
	from := to.AddDate(0, 0, -(count*2 + 30))
	resp, err := fetchSohuHistoryWindow(ctx, market, code, from.Format("2006-01-02"), to.Format("2006-01-02"), now)
	if err == nil && len(resp.Bars) > count {
		resp.Bars = resp.Bars[len(resp.Bars)-count:]
	}
	return resp, err
}

func fetchSohuHistoryRange(ctx context.Context, market, code, from, to string) (*QuoteResp, error) {
	return fetchSohuHistoryWindow(ctx, market, code, from, to, time.Now())
}

func fetchSohuHistoryWindow(ctx context.Context, market, code, from, to string, now time.Time) (*QuoteResp, error) {
	symbol, err := quoteSohuSymbol(market, code)
	if err != nil {
		return nil, err
	}
	if window, err := quoteParseWindow(from, to); err != nil || !window.bounded() {
		return nil, errors.New("quote: Sohu history requires a bounded valid window")
	}
	ctx, cancel := context.WithTimeout(ctx, quoteFetchTimeout)
	defer cancel()
	url := fmt.Sprintf("https://q.stock.sohu.com/hisHq?code=%s&start=%s&end=%s&stat=1&order=D&period=d", symbol, strings.ReplaceAll(from, "-", ""), strings.ReplaceAll(to, "-", ""))
	body, err := vendorGet(ctx, url, "https://q.stock.sohu.com/", quoteBodyLimit)
	if err != nil {
		return nil, err
	}
	return parseSohuHistory(body, market, code, from, to, now)
}
