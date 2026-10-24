package app

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"
)

func TestShippedLedgerEvidenceGrants(t *testing.T) {
	for _, src := range defaultQuoteSources() {
		want := src.name == quoteSourceTencent
		if src.cashDividends != want || src.reportsKind != want {
			t.Fatalf("source %s evidence flags disagree with measured capabilities", src.name)
		}
		for _, market := range []string{"sh", "sz", "bj", "hk", "us"} {
			if got := src.servesExplicit(quoteAsk(t, market), quoteIntervalSettled); got != (want && (market == "sh" || market == "sz")) {
				t.Errorf("source=%s market=%s writer grant=%v", src.name, market, got)
			}
		}
	}
}

func TestEvaluatorLedgerSourceOrderCannotBeBypassed(t *testing.T) {
	previous := vendorClient
	t.Cleanup(func() { vendorClient = previous })
	network := 0
	vendorClient = &http.Client{Transport: forecastVendorTransport(func(*http.Request) (*http.Response, error) {
		network++
		return nil, errors.New("unexpected direct vendor call")
	})}
	s := newV1Server(t)
	if err := s.st.SetSetting(setQuoteSourceOrder, quoteSourceSina); err != nil {
		t.Fatal(err)
	}
	e := &forecastEvaluator{}
	_, err := e.rangeBars(context.Background(), s, "sh", "601899", "2026-10-01", shanghai(2026, time.October, 22, 16, 0))
	var refusal *ledgerRefusal
	if !errors.As(err, &refusal) || refusal.Code != "no_source" || network != 0 {
		t.Fatalf("disabled primary bypassed: error=%v network=%d", err, network)
	}
}

func TestEvaluatorUsesUncachedLedgerWalkAndCompletionGate(t *testing.T) {
	s := newV1Server(t)
	calls := 0
	s.quotes.sources = []quoteSource{ledgerWalkSource(quoteSourceTencent, func(context.Context, string, string, string, string) (*QuoteResp, error) {
		calls++
		return &QuoteResp{Source: quoteSourceTencent, BarsSource: quoteSourceTencent,
			Snapshot: QuoteSnapshot{AsOf: "2026-10-22T14:57:00+08:00"},
			Bars:     []QuoteBar{bar("2026-10-21", 1000, 1000, 1000, 1000), bar("2026-10-22", 1000, 1000, 1000, 1000)}}, nil
	})}
	e := &forecastEvaluator{}
	for i := 0; i < 2; i++ {
		series, err := e.rangeBars(context.Background(), s, "sh", "601899", "2026-10-01", shanghai(2026, time.October, 22, 16, 0))
		if err != nil || len(series.Bars) != 1 || series.Bars[0].Date != "2026-10-21" || series.UnverifiableDate != "" {
			t.Fatalf("completion gate changed: series=%+v error=%v", series, err)
		}
	}
	if calls != 2 || s.quotes.stats().Entries != 0 || len(s.quotes.snapshotLedgerHealth()) != 1 || len(s.quotes.snapshotHealth()) != 0 {
		t.Fatalf("evaluator bypassed uncached isolated walk: calls=%d", calls)
	}
}

func TestEvaluatorNoSourceRemainsASourceFault(t *testing.T) {
	assertEvaluatorNoSourceFault(t, newV1Server(t))
}

func TestPostgresEvaluatorNoSourceRemainsASourceFault(t *testing.T) {
	st := pgStore(t, "prediction_marks", "prediction_claims", "prediction_sets", "reports", "api_tokens")
	if err := st.CreateToken("tok-all", "test", "all", ""); err != nil {
		t.Fatal(err)
	}
	s := &Server{st: st, names: LoadNames(t.TempDir(), st)}
	s.names.fetch = func(string) string { return "" }
	t.Cleanup(func() {
		if err := st.DeleteSetting(setQuoteSourceOrder); err != nil {
			t.Error(err)
		}
	})
	assertEvaluatorNoSourceFault(t, s)
}

func assertEvaluatorNoSourceFault(t *testing.T, s *Server) {
	t.Helper()
	setID, e := opsFixture(t, s)
	e.fetch = nil
	if err := s.st.SetSetting(setQuoteSourceOrder, quoteSourceSina); err != nil {
		t.Fatal(err)
	}
	if err := e.tick(s); err != nil {
		t.Fatal(err)
	}
	var code string
	var attempts int
	if err := s.st.queryRow(`SELECT refusal, attempts FROM prediction_marks WHERE set_id=? AND checkpoint='d5'`, setID).Scan(&code, &attempts); err != nil {
		t.Fatal(err)
	}
	if code != "no_source" || attempts != 0 {
		t.Fatalf("no-source fault obscured or charged to mark: code=%s attempts=%d", code, attempts)
	}
	if e.ledgerPass != nil {
		t.Fatal("completed tick retained its breaker state")
	}
}

func TestEvaluatorRuntimeLedgerRespectsDryAndBenchmarkGate(t *testing.T) {
	for _, mode := range []string{forecastModeDry, forecastModeOn} {
		t.Run(mode, func(t *testing.T) {
			s := newV1Server(t)
			_, e := opsFixture(t, s)
			fetch := e.fetch
			e.fetch = nil
			calls := 0
			s.quotes.sources = []quoteSource{ledgerWalkSource(quoteSourceTencent, func(ctx context.Context, market, code, from, to string) (*QuoteResp, error) {
				calls++
				return fetch(ctx, market, code, from, to)
			})}
			if err := s.st.SetSetting(setForecastAuto, mode); err != nil {
				t.Fatal(err)
			}
			e.step(s, "manual")
			if n := scalar[int](t, s.st, `SELECT COUNT(*) FROM prediction_marks WHERE status='resolved'`); n != 0 {
				t.Fatalf("runtime source path bypassed %s write gate: resolved=%d", mode, n)
			}
			hb, ok := s.st.ForecastHeartbeat()
			if !ok || calls == 0 || len(hb.DrySample) == 0 || hb.Pass.Errors != 0 {
				t.Fatalf("no successful runtime planning positive control: calls=%d heartbeat=%+v", calls, hb)
			}
			if mode == forecastModeOn && len(hb.AwaitingGate) == 0 {
				t.Fatal("missing benchmark check was not reported")
			}
		})
	}
}

func TestEvaluatorPreservesTypedStockAndLookbackRefusals(t *testing.T) {
	for _, widened := range []bool{false, true} {
		t.Run(map[bool]string{false: "stock", true: "lookback"}[widened], func(t *testing.T) {
			s := newV1Server(t)
			setID, e := opsFixture(t, s)
			fetch := e.fetch
			e.fetch = nil
			stockCalls := 0
			s.quotes.sources = []quoteSource{ledgerWalkSource(quoteSourceTencent, func(ctx context.Context, market, code, from, to string) (*QuoteResp, error) {
				if code == "000852" {
					return fetch(ctx, market, code, from, to)
				}
				stockCalls++
				if !widened || stockCalls > 1 {
					return nil, &ledgerRefusal{Code: "no_source"}
				}
				resp, err := fetch(ctx, market, code, from, to)
				if err != nil {
					return nil, err
				}
				var bars []QuoteBar
				for _, b := range resp.Bars {
					if b.Date > "2026-10-19" {
						bars = append(bars, b)
					}
				}
				resp.Bars = bars
				return resp, nil
			})}
			if err := e.tick(s); err != nil {
				t.Fatal(err)
			}
			var code string
			var attempts int
			if err := s.st.queryRow(`SELECT refusal, attempts FROM prediction_marks WHERE set_id=? AND checkpoint='d5'`, setID).Scan(&code, &attempts); err != nil {
				t.Fatal(err)
			}
			wantCalls := 1
			if widened {
				wantCalls = 2
			}
			if stockCalls != wantCalls || code != "no_source" || attempts != 0 {
				t.Fatalf("typed stock fault lost: calls=%d code=%s attempts=%d", stockCalls, code, attempts)
			}
		})
	}
}

func TestEvaluatorRuntimeBreakerIsSharedAndResetsEachTick(t *testing.T) {
	s := newV1Server(t)
	_, e := opsFixture(t, s)
	for _, symbol := range []string{"600161", "600162", "600163", "600164"} {
		seedAcceptedSet(t, s, symbol, "2026-10-17")
	}
	if _, err := s.st.exec(`UPDATE prediction_sets SET made_on='2026-10-16', made_at='2026-10-16T13:00:00Z'`); err != nil {
		t.Fatal(err)
	}
	fetch := e.fetch
	e.fetch = nil
	calls := 0
	s.quotes.sources = []quoteSource{ledgerWalkSource(quoteSourceTencent, func(ctx context.Context, market, code, from, to string) (*QuoteResp, error) {
		calls++
		if code == "000852" {
			return fetch(ctx, market, code, from, to)
		}
		return nil, errors.New("transport failure")
	})}
	for tick := 1; tick <= 2; tick++ {
		if err := e.tick(s); err != nil {
			t.Fatal(err)
		}
		if calls != tick*4 {
			t.Fatalf("tick=%d calls=%d, want %d", tick, calls, tick*4)
		}
		if e.ledgerPass != nil {
			t.Fatal("tick retained pass state")
		}
		if _, err := s.st.exec(`UPDATE prediction_marks SET next_attempt_at=''`); err != nil {
			t.Fatal(err)
		}
	}
}
