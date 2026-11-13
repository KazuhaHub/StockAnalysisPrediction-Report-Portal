package app

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

// Recorded vendor lines. The first three were measured against the live endpoint on 2026-11-03
// (China clock) through fqkline's qt.market; the holiday lines come from the committed Labor Day
// fixture and the earlier pre-open capture. The leading timestamp is the China clock in every line.
const (
	// 09:15, A-share call auction, Hong Kong pre-opening, New York closed for the day.
	phaseLineAuction = "2026-11-03 09:15:16|HK_close_盘前交易|SH_close_集合竞价|SZ_close_集合竞价|US_close_已收盘|SQ_open_交易中|DS_open_交易中|ZS_open_交易中|NEWSH_open_盘前竞价|NEWSZ_open_盘前竞价|NEWHK_open_盘前竞价|NEWUS_close_已收盘|REPO_open_盘前竞价|UK_close_未开盘|KCB_open_盘前竞价|HSZB_open_盘前竞价"
	// Labor Day: the mainland and Hong Kong trading, New York shut under the holiday's own name.
	phaseLineLaborDay = "2026-09-07 14:51:13|HK_open_交易中|SH_open_交易中|SZ_open_交易中|US_close_劳动节休市|NEWSH_open_交易中|NEWSZ_open_交易中|NEWHK_open_交易中|NEWUS_close_劳动节休市|USA_close_已收盘"
	// A New York weekend as the vendor words it, with every other board before its open.
	phaseLineWeekend = "2026-09-07 06:01:25|HK_close_未开盘|SH_close_未开盘|SZ_close_未开盘|US_close_已休市|SQ_close_未开盘|NEWSH_close_未开盘|NEWSZ_close_未开盘|NEWUS_close_已休市"
	// 12:00, the mainland and Hong Kong lunch break.
	phaseLineLunch = "2026-11-03 12:00:40|HK_close_午间休市|SH_close_午间休市|SZ_close_午间休市|US_close_已收盘|SQ_close_午间休市|DS_close_午间休市|ZS_close_午间休市"
	// 15:05, the mainland closed for the day while Hong Kong still trades.
	phaseLineAfterClose = "2026-11-03 15:05:49|HK_open_交易中|SH_close_已收盘|SZ_close_已收盘|US_close_未开盘|SQ_close_已休市|DS_close_已休市|ZS_close_已休市"
	// 16:30, Hong Kong closed too; the stamp keeps following the clock after the close.
	phaseLineEvening = "2026-11-03 16:30:52|HK_close_已收盘|SH_close_已收盘|SZ_close_已收盘|US_close_未开盘|SQ_close_已休市|DS_close_已休市|ZS_close_已休市"
)

// The inline lines in the table below are SYNTHETIC: hand-built from the recorded shapes above to
// isolate one rule each (the festival names were not captured live). The phaseLine constants
// above are the recorded ones.

func phaseAtStamp(t *testing.T, line string) time.Time {
	t.Helper()
	stamp, _, _ := strings.Cut(line, "|")
	loc, err := quoteZone(quoteMarketLineZone)
	if err != nil {
		t.Fatal(err)
	}
	at, err := time.ParseInLocation(quoteMarketLineStamp, stamp, loc)
	if err != nil {
		t.Fatal(err)
	}
	return at
}

func TestQuoteMarketPhaseFromRecordedLines(t *testing.T) {
	cases := []struct {
		name, market, line, want string
	}{
		// The reported symptom: 09:15 on a trading day, the vendor's last = previous close.
		{"sh call auction is pre-open", "sh", phaseLineAuction, quotePhasePreOpen},
		{"sz call auction is pre-open", "sz", phaseLineAuction, quotePhasePreOpen},
		// Beijing has no segment and follows SZ.
		{"bj follows sz", "bj", phaseLineAuction, quotePhasePreOpen},
		{"hk pre-opening is pre-open", "hk", phaseLineAuction, quotePhasePreOpen},
		{"us after close", "us", phaseLineAuction, quotePhaseClosed},
		{"trading", "sh", phaseLineLaborDay, quotePhaseTrading},
		{"hk trading", "hk", phaseLineLaborDay, quotePhaseTrading},
		// US holiday style: the holiday's own name ends in the generic market-closed word.
		{"us holiday by name", "us", phaseLineLaborDay, quotePhaseHoliday},
		// 06:01 China clock on a Monday is a Sunday evening in New York; the vendor says market-closed.
		{"us weekend word", "us", phaseLineWeekend, quotePhaseHoliday},
		{"sh before the open on a weekday", "sh", phaseLineWeekend, quotePhasePreOpen},
		{"sh recorded lunch break", "sh", phaseLineLunch, quotePhaseLunchBreak},
		{"hk recorded lunch break", "hk", phaseLineLunch, quotePhaseLunchBreak},
		{"sh after close", "sh", phaseLineAfterClose, quotePhaseClosed},
		{"hk still trading after the mainland close", "hk", phaseLineAfterClose, quotePhaseTrading},
		{"sh in the evening", "sh", phaseLineEvening, quotePhaseClosed},
		{"hk in the evening", "hk", phaseLineEvening, quotePhaseClosed},
		{"us before its open", "us", phaseLineEvening, quotePhasePreOpen},
		// NEWSZ_ and NEWUS_ stand BEFORE the market's own segment here, and each contains its
		// prefix. A Contains match reads the wrong board.
		{"NEWSZ prefix trap", "sz", "2026-11-03 10:00:00|NEWSZ_close_已收盘|SZ_open_交易中", quotePhaseTrading},
		{"NEWUS prefix trap", "us", "2026-11-03 22:31:00|NEWUS_close_已收盘|US_open_交易中", quotePhaseTrading},
		{"NEWSH alone is not SH", "sh", "2026-11-03 10:00:00|NEWSH_open_交易中", quotePhaseUnknown},
		// A festival's own name ends in the generic market-closed word and is a holiday, never a lunch
		// break: a substring rule on the midday word's characters read these as lunch all day.
		{"dragon boat festival is a holiday", "sh", "2026-06-19 10:30:00|SH_close_端午节休市|SZ_close_端午节休市", quotePhaseHoliday},
		{"mid-autumn festival is a holiday", "hk", "2026-09-25 14:00:00|HK_close_中秋节休市", quotePhaseHoliday},
		{"lunch break", "sh", "2026-11-03 12:00:00|SH_close_午间休市|SZ_close_午间休市", quotePhaseLunchBreak},
		{"closed", "sh", "2026-11-03 15:30:00|SH_close_已收盘", quotePhaseClosed},
		{"hk lunch break", "hk", "2026-11-03 12:30:00|HK_close_午间休市", quotePhaseLunchBreak},
		{"generic holiday word", "sh", "2026-10-05 10:00:00|SH_close_休市", quotePhaseHoliday},
		// A weekday pre-open word on a Saturday cannot be pre-open.
		{"pre-open word on a Saturday", "sh", "2026-11-07 09:15:00|SH_close_未开盘", quotePhaseHoliday},
		// Weekend robustness. SYNTHETIC lines: the A-share weekend words were never captured, so the
		// rule is that any coarse close on a Saturday or Sunday in the market's zone is a holiday.
		{"weekend closed word", "sh", "2026-11-07 10:00:00|SH_close_已收盘", quotePhaseHoliday},
		{"weekend not-yet-open word", "sh", "2026-11-07 10:00:00|SH_close_未开盘", quotePhaseHoliday},
		{"weekend generic closed word", "sh", "2026-11-08 10:00:00|SH_close_休市", quotePhaseHoliday},
		{"weekend unknown word", "sh", "2026-11-08 10:00:00|SH_close_something-new", quotePhaseHoliday},
		{"weekend hk unknown word", "hk", "2026-11-07 10:00:00|HK_close_something-new", quotePhaseHoliday},
		{"weekend open state is a contradiction", "sh", "2026-11-07 10:00:00|SH_open_交易中", quotePhaseUnknown},
		// A weekday with a fresh stamp and a closed-market word is a holiday (a weekday festival).
		{"weekday closed-market word", "sh", "2026-11-04 10:00:00|SH_close_休市", quotePhaseHoliday},
		// A call auction also runs at the close: "previous close" at 14:58 would be false.
		{"closing auction is not pre-open", "sz", "2026-11-03 14:58:00|SZ_open_集合竞价", quotePhaseUnknown},
		// Refusals: a doubt is no label, never a wrong one.
		{"unrecognised word", "sh", "2026-11-03 09:45:00|SH_open_连续竞价", quotePhaseUnknown},
		{"placeholder word", "sh", "2026-11-03 09:45:00|SH_open_x", quotePhaseUnknown},
		{"trading word with a close state", "sh", "2026-11-03 09:45:00|SH_close_交易中", quotePhaseUnknown},
		{"closed word with an open state", "sh", "2026-11-03 15:30:00|SH_open_已收盘", quotePhaseUnknown},
		{"empty line", "sh", "", quotePhaseUnknown},
		{"unparseable stamp", "sh", "yesterday|SH_open_交易中", quotePhaseUnknown},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := quoteTestMarket(t, c.market)
			now := time.Now()
			if stamp, _, _ := strings.Cut(c.line, "|"); stamp != "" {
				if loc, err := quoteZone(quoteMarketLineZone); err == nil {
					if at, err := time.ParseInLocation(quoteMarketLineStamp, stamp, loc); err == nil {
						now = at.Add(30 * time.Second)
					}
				}
			}
			if got := quoteMarketPhase(m, c.line, now); got != c.want {
				t.Errorf("quoteMarketPhase(%s, %.70q) = %q, want %q", c.market, c.line, got, c.want)
			}
		})
	}
	if got := quoteMarketPhase(nil, phaseLineAuction, phaseAtStamp(t, phaseLineAuction)); got != quotePhaseUnknown {
		t.Errorf("nil market = %q, want unknown", got)
	}
}

func TestQuoteMarketPhaseRefusesAFrozenFeed(t *testing.T) {
	m := quoteTestMarket(t, "sh")
	line := "2026-11-03 10:00:00|SH_open_交易中"
	at := phaseAtStamp(t, line)
	if got := quoteMarketPhase(m, line, at.Add(time.Minute)); got != quotePhaseTrading {
		t.Fatalf("fresh line = %q, want trading", got)
	}
	if got := quoteMarketPhase(m, line, at.Add(2*time.Hour)); got != quotePhaseUnknown {
		t.Errorf("a two-hour-old line = %q, want unknown", got)
	}
}

func TestQuoteMarketPhaseStalenessEdges(t *testing.T) {
	m := quoteTestMarket(t, "sh")
	line := "2026-11-03 10:00:00|SH_open_交易中" // synthetic, one rule
	at := phaseAtStamp(t, line)
	for _, c := range []struct {
		name string
		now  time.Time
		want string
	}{
		{"exactly ten minutes after", at.Add(quotePhaseMaxAge), quotePhaseTrading},
		{"one second past ten minutes after", at.Add(quotePhaseMaxAge + time.Second), quotePhaseUnknown},
		{"exactly ten minutes before", at.Add(-quotePhaseMaxAge), quotePhaseTrading},
		{"one second past ten minutes before", at.Add(-quotePhaseMaxAge - time.Second), quotePhaseUnknown},
		{"same instant", at, quotePhaseTrading},
	} {
		if got := quoteMarketPhase(m, line, c.now); got != c.want {
			t.Errorf("%s: phase = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestQuoteMarketSessionStaysTwoValued(t *testing.T) {
	// The cache TTL code reads Session and nothing else; the phase work must not widen it.
	m := quoteTestMarket(t, "sh")
	if got := quoteMarketSession(m, phaseLineAuction); got != quoteSessionClose {
		t.Errorf("session during the call auction = %q, want close", got)
	}
	if got := quoteMarketSession(m, phaseLineLaborDay); got != quoteSessionOpen {
		t.Errorf("session while trading = %q, want open", got)
	}
}

func TestParseTencentMarketLine(t *testing.T) {
	body := `{"code":0,"msg":"","data":{"sh000001":{"day":[["2026-11-02","3614.310","3638.030","3648.130","3594.770","565372836.000"]],"qt":{"sh000001":["1"],"market":["` + phaseLineAuction + `"],"zhishu":[]}}}}`
	got, err := parseTencentMarketLine([]byte(body))
	if err != nil || got != phaseLineAuction {
		t.Fatalf("parse = %q, %v", got, err)
	}
	for _, bad := range []string{
		``, `{}`, `{"data":{}}`,
		`{"data":{"sh000001":{"qt":{"sh000001":["1"]}}}}`,
		`{"data":{"sh000001":{"qt":{"market":[]}}}}`,
		`{"data":{"sh000001":{"qt":{"market":[""]}}}}`,
	} {
		if _, err := parseTencentMarketLine([]byte(bad)); err == nil {
			t.Errorf("parse(%q) succeeded, want an error", bad)
		}
	}
}

// primeMarketLine asks for the line, then waits for the background refresh it started to land.
func primeMarketLine(t *testing.T, s *Server) {
	t.Helper()
	cfg := s.quoteConfigLoad()
	s.quotes.marketLine(cfg)
	deadline := time.Now().Add(5 * time.Second)
	for {
		s.quotes.phaseMu.Lock()
		busy, have := s.quotes.phaseBusy, s.quotes.phaseLine != ""
		s.quotes.phaseMu.Unlock()
		if !busy && have {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("market line refresh never landed")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestQuoteBatchCardsCarryThePhase(t *testing.T) {
	s := quoteServer(t)
	tencent := batchStub(t)
	tencent.marketLine = phaseLineLaborDay
	wireQuoteSources(s, tencent, failingStub("sina must not be asked"))
	s.quotes.init()
	s.quotes.now = func() time.Time { return phaseAtStamp(t, phaseLineLaborDay).Add(5 * time.Second) }

	// Cold cache: the first answer carries prices and no label, and does not wait for the anchor.
	cold := quoteBatchBody(t, quoteBatchGET(t, s, "/api/quotes?symbols=601899"))
	quoteEqStr(t, "cold phase", cold.Quotes["sh601899"].Phase, quotePhaseUnknown)
	primeMarketLine(t, s)

	rec := quoteBatchGET(t, s, "/api/quotes?symbols=601899,00700,AAPL")
	if rec.Code != http.StatusOK {
		t.Fatalf("batch → %d (%s)", rec.Code, rec.Body.String())
	}
	view := quoteBatchBody(t, rec)
	quoteEqStr(t, "sh phase", view.Quotes["sh601899"].Phase, quotePhaseTrading)
	quoteEqStr(t, "hk phase", view.Quotes["hk00700"].Phase, quotePhaseTrading)
	quoteEqStr(t, "us phase", view.Quotes["usAAPL"].Phase, quotePhaseHoliday)
	// The anchor request is made once however many pages ask inside its window.
	quoteBatchGET(t, s, "/api/quotes?symbols=601899,00700,AAPL")
	s.quotes.phaseMu.Lock()
	calls := tencent.marketLineCalls
	s.quotes.phaseMu.Unlock()
	if calls != 1 {
		t.Errorf("pages cost %d market-line requests, want 1", calls)
	}
}

// A hanging anchor must not delay a single response, and must not pile up fetches behind it.
func TestQuoteBatchDoesNotWaitOnAHangingMarketLine(t *testing.T) {
	s := quoteServer(t)
	tencent := batchStub(t)
	release := make(chan struct{})
	var started int
	var mu sync.Mutex
	wireQuoteSources(s, tencent, failingStub("sina must not be asked"))
	s.quotes.init()
	for i := range s.quotes.sources {
		if s.quotes.sources[i].fetchMarketLine != nil {
			s.quotes.sources[i].fetchMarketLine = func(ctx context.Context) (string, error) {
				mu.Lock()
				started++
				mu.Unlock()
				select {
				case <-release:
				case <-ctx.Done():
				}
				return "", errors.New("hung")
			}
		}
	}
	t.Cleanup(func() { close(release) })

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 3; i++ {
			rec := quoteBatchGET(t, s, "/api/quotes?symbols=601899")
			card := quoteBatchBody(t, rec).Quotes["sh601899"]
			if card.Last == 0 || card.Phase != quotePhaseUnknown {
				t.Errorf("response %d: last=%d phase=%q", i, card.Last, card.Phase)
			}
		}
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("the handler waited on a hanging market-line fetch")
	}
	mu.Lock()
	defer mu.Unlock()
	if started != 1 {
		t.Errorf("%d concurrent anchor fetches started, want 1 (single flight)", started)
	}
}

func TestQuoteBatchCardsWithoutAMarketLineHaveUnknownPhase(t *testing.T) {
	s := quoteServer(t)
	tencent := batchStub(t) // no market line: the stub's fetcher fails
	wireQuoteSources(s, tencent, failingStub("sina must not be asked"))
	rec := quoteBatchGET(t, s, "/api/quotes?symbols=601899")
	if rec.Code != http.StatusOK {
		t.Fatalf("batch → %d", rec.Code)
	}
	card := quoteBatchBody(t, rec).Quotes["sh601899"]
	quoteEqStr(t, "phase", card.Phase, quotePhaseUnknown)
	if card.Last == 0 {
		t.Error("a failed market line cost the card its price")
	}
}

// The reading page's quote strip asks /api/quote, not the batch endpoint, and it needs the same
// phase: before the open a stock that has not traded yet reports high 0 / low 0, exactly the shape
// a suspension has, and only the phase tells the two apart.
func TestQuoteSingleCarriesThePhase(t *testing.T) {
	s := quoteServer(t)
	tencent := tencentStub(readQuoteFixture(t, fixTencentSH))
	tencent.marketLine = phaseLineAuction
	wireQuoteSources(s, tencent, failingStub("sina must not be asked"))
	s.quotes.init()
	s.quotes.now = func() time.Time { return phaseAtStamp(t, phaseLineAuction).Add(5 * time.Second) }
	primeMarketLine(t, s)

	rec := quoteGET(t, s, "/api/quote/601899")
	if rec.Code != http.StatusOK {
		t.Fatalf("quote → %d (%s)", rec.Code, rec.Body.String())
	}
	quoteEqStr(t, "phase", quoteBody(t, rec).Phase, quotePhasePreOpen)

	// A cached answer is stamped per request too: the phase must never outlive its transition by
	// riding along in a cache entry.
	tencent.mu.Lock()
	tencent.marketLine = phaseLineLunch
	tencent.mu.Unlock()
	s.quotes.phaseMu.Lock()
	s.quotes.phaseAt = time.Time{}
	s.quotes.phaseMu.Unlock()
	s.quotes.now = func() time.Time { return phaseAtStamp(t, phaseLineLunch).Add(5 * time.Second) }
	primeMarketLine(t, s)
	quoteEqStr(t, "phase after the line moved", quoteBody(t, quoteGET(t, s, "/api/quote/601899")).Phase, quotePhaseLunchBreak)
}
