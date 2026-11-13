package app

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// ---------- market phase ----------
//
// A card on the home grid prints a price, and before the A-share open that price is the previous
// close with a change of exactly zero — correct, and indistinguishable from a broken feed. The phase
// is the finer statement that tells a reader what the number IS: before the open, trading, on the
// lunch break, after the close, or on a day the exchange is shut.
//
// It is deliberately a SEPARATE field from Session. Session is a two-valued fact the cache TTL code
// reads, and widening it would change how long every cached quote lives. The phase is derived at
// answer time from the vendor's session line, never stored in a cache entry, so it cannot outlive
// the transition it describes.
//
// The batch quote endpoint (qt.gtimg.cn) carries no market-state line — measured 2026-11-03,
// `q=sh603289,market` answers one line for 603289 and nothing else. The state travels on fqkline's
// qt.market, which is market-wide rather than per symbol, so ONE anchor request serves every card on
// the page, and it is cached for a few seconds below.

const (
	quotePhasePreOpen    = "pre_open"
	quotePhaseTrading    = "trading"
	quotePhaseLunchBreak = "lunch_break"
	quotePhaseClosed     = "closed"
	quotePhaseHoliday    = "holiday"
	quotePhaseUnknown    = "unknown"

	// quoteMarketLineAnchor is the symbol the market-wide state line is read through. Any listed
	// code answers it; an index is used because it never delists and names no company.
	quoteMarketLineAnchor = "sh000001"

	// quotePhaseMaxAge is how old the vendor's own timestamp on the line may be, against this
	// server's clock, before the line is refused. A vendor feed that has frozen still says
	// "trading" in its last words; a phase must not outlive the evidence for it.
	quotePhaseMaxAge = 10 * time.Minute

	// quoteMarketLineTTL / quoteMarketLineFailTTL bound how often the anchor request is made: one
	// per window however many pages ask, and a failed one is remembered so a dead vendor is not
	// asked once per page view.
	quoteMarketLineTTL     = 20 * time.Second
	quoteMarketLineFailTTL = 30 * time.Second

	// quoteMarketLineStamp is the layout of the line's leading timestamp, and quoteMarketLineZone its
	// zone. Measured 2026-11-03: the same line, requested through a Hong Kong, a US and a Shanghai
	// symbol, carried the same China-clock stamp (09:15:19 at 01:15 UTC).
	quoteMarketLineStamp = "2006-01-02 15:04:05"
	quoteMarketLineZone  = "Asia/Shanghai"
)

// quotePhaseWord maps the vendor's own state word - the part after the second underscore in a
// segment such as SH_close_<word> - to a phase. Words measured or captured so far:
//
//	"trading"                                   -> trading
//	"not yet open", call auction, HK pre-market
//	trading, "pre-open auction"                 -> before the open
//	"midday break" (exact word only)            -> lunch break
//	"closed" (after the bell)                   -> after the close
//	"market closed", and a holiday's own name
//	ending in "market closed"                   -> the exchange is shut for the day
//
// The exact literals are in the switch below (they are vendor data). Anything else is unknown, and
// unknown renders as no label. The pre-open words are further gated on the clock in
// quoteMarketPhase, because a call auction also happens at the close.
func quotePhaseWord(word string) string {
	switch {
	case word == "交易中":
		return quotePhaseTrading
	case word == "未开盘", word == "集合竞价", word == "盘前交易", word == "盘前竞价":
		return quotePhasePreOpen
	case word == "已收盘":
		return quotePhaseClosed
	case word == "午间休市":
		// The EXACT word only. A holiday's own name also ends in the generic market-closed word
		// (the Dragon Boat and Mid-Autumn festivals are examples), and a substring rule read them as
		// a lunch break for the whole day.
		return quotePhaseLunchBreak
	case strings.HasSuffix(word, "休市"):
		return quotePhaseHoliday
	}
	return quotePhaseUnknown
}

// quoteMarketPhase derives one market's phase from the vendor's session line.
//
// The vendor's own word is the source; the rules below only REFUSE a reading the rest of the line
// contradicts, so a doubt degrades to unknown (no label) and never to a wrong one:
//
//   - the line's own timestamp must parse and be within quotePhaseMaxAge of now, so a frozen feed
//     is not believed;
//   - "trading" requires the coarse state to be open, and "closed" / "lunch break" require it to be
//     close — a segment that disagrees with itself is not read;
//   - a pre-open word is believed only before noon in the market's own zone, because a call auction
//     also runs at the close and "pre-open, number is last close" at 14:58 would be false;
//   - a coarse "close" on a Saturday or Sunday in the market's own zone is a non-trading day,
//     whatever the word: holiday.
//
// No exchange hours are hardcoded: the vendor says what state the market is in, and the calendar is
// consulted only to reject an impossible one.
func quoteMarketPhase(m *quoteMarket, raw string, now time.Time) string {
	state, word, ok := quoteMarketSegment(m, raw)
	if !ok {
		return quotePhaseUnknown
	}
	stamp, _, _ := strings.Cut(raw, "|")
	lineLoc, err := quoteZone(quoteMarketLineZone)
	if err != nil {
		return quotePhaseUnknown
	}
	at, err := time.ParseInLocation(quoteMarketLineStamp, strings.TrimSpace(stamp), lineLoc)
	if err != nil {
		return quotePhaseUnknown
	}
	if d := now.Sub(at); d > quotePhaseMaxAge || d < -quotePhaseMaxAge {
		return quotePhaseUnknown
	}
	marketLoc, err := quoteZone(m.zone)
	if err != nil {
		return quotePhaseUnknown
	}
	local := at.In(marketLoc)
	// A weekend in the market's own zone: the exchange is shut whatever word the vendor chose for
	// it, and the price on screen is the last session's close. Any coarse "close" reading is a
	// holiday - a recognised word that would otherwise say pre-open or closed, an explicit
	// closed-market word, or one this build has never seen. Anything else on a weekend (a coarse
	// "open", which no exchange can be) is a contradiction and is refused.
	if wd := local.Weekday(); wd == time.Saturday || wd == time.Sunday {
		if state == quoteSessionClose {
			return quotePhaseHoliday
		}
		return quotePhaseUnknown
	}
	phase := quotePhaseWord(word)
	if phase == quotePhaseUnknown {
		return quotePhaseUnknown
	}
	switch phase {
	case quotePhaseTrading:
		if state != quoteSessionOpen {
			return quotePhaseUnknown
		}
	case quotePhaseClosed, quotePhaseLunchBreak:
		if state != quoteSessionClose {
			return quotePhaseUnknown
		}
	case quotePhasePreOpen:
		if local.Hour() >= 12 {
			return quotePhaseUnknown
		}
	}
	return phase
}

// fetchTencentMarketLine reads the vendor's market-wide session line through one anchor symbol.
// It is the only request the phase needs, and it carries no symbol the reader asked about.
func fetchTencentMarketLine(ctx context.Context) (string, error) {
	rawURL := fmt.Sprintf(quoteTencentKlineURL, quoteMarketLineAnchor, 1)
	ctx, cancel := context.WithTimeout(ctx, quoteFetchTimeout)
	defer cancel()
	body, err := vendorGet(ctx, rawURL, quoteTencentReferer, quoteBodyLimit)
	if err != nil {
		vendorLogf(rawURL, "quote market line fetch failed: %v", err)
		return "", err
	}
	line, err := parseTencentMarketLine(body)
	if err != nil {
		vendorLogf(rawURL, "quote market line parse failed: %v", err)
		return "", err
	}
	return line, nil
}

// parseTencentMarketLine pulls qt.market[0] out of an fqkline body for the anchor symbol.
func parseTencentMarketLine(body []byte) (string, error) {
	var env tencentQuoteEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		return "", fmt.Errorf("quote: decode tencent market line: %w", err)
	}
	rawSym, ok := env.Data[quoteMarketLineAnchor]
	if !ok {
		return "", fmt.Errorf("quote: tencent response has no %q entry", quoteMarketLineAnchor)
	}
	var qb tencentQuoteBody
	if err := json.Unmarshal(rawSym, &qb); err != nil {
		return "", fmt.Errorf("quote: decode tencent %s entry: %w", quoteMarketLineAnchor, err)
	}
	var market []string
	if err := json.Unmarshal(qb.Qt["market"], &market); err != nil || len(market) == 0 || market[0] == "" {
		return "", fmt.Errorf("quote: tencent response carries no market line")
	}
	return market[0], nil
}

// marketLine returns the last known vendor session line WITHOUT ever waiting on the network, or ""
// when none is known yet. No request may wait on a vendor for a label: when the cached line is past
// its TTL (or absent) a single background refresh is started - one at a time, on a context detached
// from the request, so a client that navigates away neither cancels it nor is held up by it - and
// this response uses whatever is cached, which for a cold cache is "no label this time".
//
// An old line is still returned: quoteMarketPhase refuses one whose own timestamp is too far from
// now, so staleness is judged by the vendor's clock and not by when we last asked.
func (c *quoteCache) marketLine(cfg quoteConfig) string {
	c.init()
	if cfg.HistoryOnly {
		return ""
	}
	byName := make(map[string]quoteSource, len(c.sources))
	for _, src := range c.sources {
		byName[src.name] = src
	}
	var fetch func(ctx context.Context) (string, error)
	for _, name := range cfg.Order {
		if cand, ok := byName[name]; ok && cand.fetchMarketLine != nil && !cand.historyOnly {
			fetch = cand.fetchMarketLine
			break
		}
	}
	if fetch == nil {
		return ""
	}
	c.phaseMu.Lock()
	defer c.phaseMu.Unlock()
	line := c.phaseLine
	if now := c.now(); (c.phaseAt.IsZero() || !now.Before(c.phaseAt)) && !c.phaseBusy {
		c.phaseBusy = true
		go c.refreshMarketLine(fetch, now)
	}
	return line
}

// refreshMarketLine is the single background refresh. It holds phaseMu only to publish the result,
// never across the network call. A failure keeps the previous line (the vendor-clock age check
// retires it) and is remembered for quoteMarketLineFailTTL so a dead vendor is not asked per page.
//
// Both expiries count from `asked`, the instant the request that started this refresh observed,
// and this goroutine never reads c.now itself: the clock is a seam that tests move between
// requests, and this runs after the starting request has returned, so a read here would race with
// them. Counting from the ask rather than the answer moves an expiry earlier by at most one fetch
// timeout, which only means the next label refresh is asked for a little sooner.
func (c *quoteCache) refreshMarketLine(fetch func(ctx context.Context) (string, error), asked time.Time) {
	ctx, cancel := context.WithTimeout(context.Background(), quoteFetchTimeout)
	defer cancel()
	line, err := fetch(ctx)
	c.phaseMu.Lock()
	defer c.phaseMu.Unlock()
	c.phaseBusy = false
	if err != nil {
		// Not recorded against the source's health: that record is the quote path's own, and a label
		// request failing must not read as the vendor failing to serve prices.
		c.phaseAt = asked.Add(quoteMarketLineFailTTL)
		return
	}
	c.phaseLine, c.phaseAt = line, asked.Add(quoteMarketLineTTL)
}

// phaseFor is the phase for one market against the cached line, "unknown" when there is none.
func (c *quoteCache) phaseFor(m *quoteMarket, line string) string {
	if line == "" {
		return quotePhaseUnknown
	}
	return quoteMarketPhase(m, line, c.clockNow())
}
