package app

import (
	"context"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/transform"
)

func sinaRangeTestContext(t *testing.T, history []byte, historyStatus int) (context.Context, *int) {
	t.Helper()
	snapshot, _, err := transform.Bytes(simplifiedchinese.GBK.NewEncoder(), readQuoteFixture(t, fixSinaSnap))
	if err != nil {
		t.Fatal(err)
	}
	calls := new(int)
	client := &http.Client{Transport: forecastVendorTransport(func(request *http.Request) (*http.Response, error) {
		*calls++
		body, status := snapshot, http.StatusOK
		if request.URL.Host == "money.finance.sina.com.cn" {
			if request.URL.Query().Get("datalen") != "800" || request.URL.Query().Get("symbol") != "sh601899" {
				t.Fatalf("wrong history request: %s", request.URL)
			}
			body, status = history, historyStatus
		} else if request.URL.Host != "hq.sinajs.cn" {
			t.Fatalf("unexpected vendor: %s", request.URL)
		}
		return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(body))), Request: request}, nil
	})}
	return context.WithValue(context.Background(), quoteProbeClientKey{}, client), calls
}

func TestSinaRangeFiltersByRowDate(t *testing.T) {
	ctx, calls := sinaRangeTestContext(t, readQuoteFixture(t, fixSinaKline), http.StatusOK)
	response, err := fetchSinaRange(ctx, "sh", "601899", "2026-06-15", "2026-06-18")
	if err != nil {
		t.Fatal(err)
	}
	if *calls != 2 || len(response.Bars) != 4 || response.Bars[0].Date != "2026-06-15" || response.Bars[3].Date != "2026-06-18" || response.BarsSource != quoteSourceSina {
		t.Fatalf("not a bounded history: calls=%d %+v", *calls, response)
	}
}

func TestSinaKlineMustAgreeWithItsSnapshot(t *testing.T) {
	original, err := parseSinaSnapshot("sh", "601899", string(readQuoteFixture(t, fixSinaSnap)))
	if err != nil {
		t.Fatal(err)
	}
	original.Bars, err = parseSinaKLine(readQuoteFixture(t, fixSinaKline))
	if err != nil {
		t.Fatal(err)
	}
	original.BarsSource = quoteSourceSina
	window := quoteWindow{from: "2026-06-15", to: "2026-06-18"} // The newest bar is outside the selected window, but must still be checked.
	for _, kind := range []string{"match", "reversed", "one_fen", "date", "stamp", "empty", "unavailable", "duplicate", "bad_date", "adjusted", "source", "top_source", "cached", "over_cap"} {
		t.Run(kind, func(t *testing.T) {
			response := *original
			response.Bars = append([]QuoteBar{}, original.Bars...)
			switch kind {
			case "reversed":
				for i, j := 0, len(response.Bars)-1; i < j; i, j = i+1, j-1 {
					response.Bars[i], response.Bars[j] = response.Bars[j], response.Bars[i]
				}
			case "cached":
				response.Cached = true
			case "over_cap":
				response.Bars = make([]QuoteBar, quoteMaxBars+1)
			case "one_fen":
				response.Bars[len(response.Bars)-1].Close++
			case "date":
				response.Snapshot.AsOf = "2026-09-07T15:34:59+08:00"
			case "stamp":
				response.Snapshot.AsOf = "invalid"
			case "empty":
				response.Bars = nil
			case "unavailable":
				response.BarsUnavailable = quoteBarsSourceFailed
			case "duplicate":
				response.Bars = append(response.Bars, response.Bars[0])
			case "bad_date":
				response.Bars[0].Date = "2026-99-99"
			case "adjusted":
				response.Adjusted = true
			case "source":
				response.BarsSource = quoteSourceTencent
			case "top_source":
				response.Source = quoteSourceTencent
			}
			before := append([]QuoteBar{}, response.Bars...)
			got, err := boundedSinaHistory(&response, window)
			if kind == "match" || kind == "reversed" {
				if err != nil || got == nil || len(got.Bars) != 4 {
					t.Fatalf("coherent response refused: %+v %v", got, err)
				}
			} else if err == nil || got != nil {
				t.Fatalf("incoherent evidence accepted: %+v %v", got, err)
			}
			if !reflect.DeepEqual(response.Bars, before) && !(len(before) == 0 && len(response.Bars) == 0) {
				t.Fatal("range validation mutated input")
			}
		})
	}
	if got, err := boundedSinaHistory(nil, window); err == nil || got != nil {
		t.Fatal("nil response accepted")
	}
}

func TestSinaRangeCancelledOrUnsupportedRequestNeverCallsVendor(t *testing.T) {
	ctx, calls := sinaRangeTestContext(t, nil, http.StatusOK)
	ctx, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := fetchSinaRange(ctx, "sh", "601899", "2026-06-15", "2026-06-18"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation obscured: %v", err)
	}
	ctx = context.WithoutCancel(ctx)
	if _, err := fetchSinaRange(ctx, "us", "AAPL", "2026-06-15", "2026-06-18"); err == nil {
		t.Fatal("unsupported market accepted")
	}
	if *calls != 0 {
		t.Fatal("cancelled or unsupported request reached vendor")
	}
}

func TestSinaRangeRefusesWindowsBeyondCountCap(t *testing.T) {
	start := time.Date(2024, time.June, 1, 0, 0, 0, 0, time.UTC)
	bars := make([]QuoteBar, quoteMaxBars)
	for i := range bars {
		bars[i] = shadowTestBar(start.AddDate(0, 0, i).Format(quoteWindowLayout), 1000, 1000, 1000, 1000)
	}
	last := bars[len(bars)-1].Date
	response := &QuoteResp{Source: quoteSourceSina, BarsSource: quoteSourceSina, Market: "sh", Symbol: "601899", Bars: bars, Snapshot: QuoteSnapshot{Last: 1000, AsOf: last + "T15:34:59+08:00"}}
	_, err := boundedSinaHistory(response, quoteWindow{from: start.AddDate(0, 0, -1).Format(quoteWindowLayout), to: last})
	var refusal *ledgerRefusal
	if !errors.As(err, &refusal) || refusal.Code != "window_not_covered" {
		t.Fatalf("cap narrowed the requested window: %v", err)
	}
	if got, err := boundedSinaHistory(response, quoteWindow{from: bars[0].Date, to: last}); err != nil || len(got.Bars) != quoteMaxBars {
		t.Fatalf("exact cap coverage refused: %v", err)
	}
}

func TestSinaRangeTreatsUnavailableHistoryAsError(t *testing.T) {
	ctx, calls := sinaRangeTestContext(t, []byte("unavailable"), http.StatusServiceUnavailable)
	if response, err := fetchSinaRange(ctx, "sh", "601899", "2026-06-15", "2026-06-18"); err == nil || response != nil || *calls != 2 {
		t.Fatalf("display degradation became shadow evidence: calls=%d %+v %v", *calls, response, err)
	}
}

func TestSinaRangeValidatesWindowBeforeNetwork(t *testing.T) {
	ctx, calls := sinaRangeTestContext(t, nil, http.StatusOK)
	for _, window := range [][2]string{{"", ""}, {"bad", "2026-06-18"}, {"2026-06-18", "2026-06-15"}} {
		if _, err := fetchSinaRange(ctx, "sh", "601899", window[0], window[1]); err == nil {
			t.Fatal("invalid window accepted")
		}
	}
	if *calls != 0 {
		t.Fatal("invalid window reached vendor")
	}
}
