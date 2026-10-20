package app

// Forecast verification: the pure settlement rules (P7, ADR 0029 §9.4, §9.5).
//
// Nothing in this file touches the store or the network. It answers three questions the evaluator
// asks about one mark: may the baseline be believed, may the bar be believed, and what does the
// answer mean when the stock did not trade. Keeping them pure is what lets the design's tables be
// tested case by case instead of through a vendor fixture.

import (
	"fmt"
	"strings"
	"time"
)

// errNoBaseInWindow means neither S0 nor any earlier bar inside the reference window carries a
// price for this stock. The caller may widen the window once before treating the baseline as
// unknowable.
var errNoBaseInWindow = fmt.Errorf("no traded bar in the reference window")

// settleInput is everything settleMark needs. It is a struct because the function has more inputs
// than an argument list can keep readable, and because a test can name the one it is varying.
type settleInput struct {
	Set              forecastWorkSet
	Mark             forecastMark
	Stock            []QuoteBar
	Index            []QuoteBar
	ExDates          []string
	AsOf             string
	UnverifiableDate string
	Source           string
	Sessions         []string
	S0               string
	BaseDate         string
	S0Fen            int64
	Session          string
	// SeriesFrom is the fetch window's floor: a series that begins there is a listing's first bars,
	// whose moves have no price limit.
	SeriesFrom string
	Now        time.Time
	// Valve is the pass-wide stale-feed signal: when tripped, a missing bar means the vendor is
	// behind, not that this stock halted. Evidence still wins, so a current body resolves.
	Valve bool
}

// settleMark computes a checkpoint's outcome from the two series.
//
// The stock's bar is the last one at or before the checkpoint's session, and it is taken at or after
// base_date: a bar printed before the report existed is not this report's outcome, and the fill
// window below uses the same boundary for the same reason.
func settleMark(in settleInput) markResult {
	res := markResult{
		Status: markResolved, Session: in.Session, Attempts: in.Mark.Attempts, Source: in.Source,
	}
	// A bar the vendor stamped today but did not close cannot settle today's checkpoint.
	if in.UnverifiableDate != "" && in.Session == in.UnverifiableDate {
		res.Status, res.Refusal = markRefused, "unverifiable"
		return res
	}
	// The benchmark leg runs from S0, never from base_date: a stock halted on S0 must not add the
	// index's pre-report move to its excess. A missing benchmark bar is a refusal, never a nil
	// bench_ret_bp.
	s0Bar := barOn(in.Index, in.S0)
	sessBar := barOn(in.Index, in.Session)
	if s0Bar == nil || sessBar == nil || s0Bar.Close <= 0 {
		res.Status, res.Refusal = markRefused, "unverifiable"
		return res
	}

	bar, ok := lastBarAtOrBefore(in.Stock, in.Session)
	if !ok || bar.Date < in.BaseDate {
		res.Status, res.Refusal = markRefused, "no_bar"
		return res
	}
	// The stored start price must still be what this body prints on base_date. A revised or
	// adjusted body that moved the anchor cannot be scored against the old start.
	if b := barOn(in.Stock, in.BaseDate); b != nil && b.Close != in.S0Fen {
		res.Status, res.Refusal = markRefused, "anchor_mismatch"
		return res
	}
	// Every stock bar in the window must be on the index's calendar. This is the one calendar
	// refusal about a single stock's series, so it counts toward finality.
	if stockDatesNotIndexed(in.Stock, in.Index, in.BaseDate, in.Session) {
		res.Status, res.Refusal = markRefused, "calendar_mismatch"
		return res
	}

	res.BarDate = bar.Date
	if bar.Date < in.Session {
		// The stock did not trade on the due session. Evidence (a body stamped on or after the
		// session) resolves a halt at once. Without it, three completed sessions of silence are
		// confirmation. Before that, the mark is retryable: a vendor lag is not a halt. A tripped
		// valve says the whole feed is behind, so nothing may be written at all.
		evidence := stampProvesCurrent(in.AsOf, in.Session)
		confirmed := sessionsPastDue(in.Sessions, in.Session, in.Now) >= 3
		switch {
		case evidence:
			res.Halted = true
		case confirmed && !in.Valve:
			res.Halted = true
		case in.Valve:
			res.Status, res.Refusal = markRefused, "calendar_lag"
			return res
		default:
			res.Status, res.Refusal = markRefused, "stale_bar"
			return res
		}
	}

	closeFen := bar.Close
	res.CloseFen = &closeFen

	// The fill window is (base_date, session]. An empty one means nothing traded since the base, so
	// there is no low or high to report rather than a zero.
	if lo, hi, ok := lowHighBetween(in.Stock, in.BaseDate, in.Session); ok {
		res.LowFen, res.HighFen = &lo, &hi
	}
	if in.S0Fen > 0 {
		ret := bpFromRatio(closeFen, in.S0Fen)
		res.RetBp = &ret
	}
	bench := bpFromRatio(sessBar.Close, s0Bar.Close)
	res.BenchRetBp = &bench
	res.CorpAction = corporateActionBetween(in.Set.Symbol, in.Stock, in.Index, in.ExDates,
		in.BaseDate, in.Session, in.SeriesFrom)

	// A resumed or delisted stock keeps the halt flag; it never stays refused, because a refusal that
	// can never resolve is exactly what hollows out a cohort without showing it.
	return res
}

// predBaseCheck compares the producer's emitted baseline with what the market printed (design §9.4).
// The reference session comes from made_at, never from the payload; emitted_base_as_of is only a
// diagnostic. ready is false while the reference bar is not complete yet.
func predBaseCheck(madeAt time.Time, emittedFen *int64, stock []QuoteBar, sessions []string, unverifiableDate string) (check string, ready bool) {
	if emittedFen == nil {
		return baseMismatch, true
	}
	e := *emittedFen
	t := madeAt.In(chinaZone)
	d := t.Format("2006-01-02")
	minute := t.Hour()*60 + t.Minute()
	intraday := minute >= 9*60+30 && minute < predSessionCloseMinute

	// A receipt inside a session compares with that session's range. Once a later session exists and
	// d is not one, d was a holiday: fall through to the outside rule with the last session before d.
	if intraday && !dateKnownNotSession(sessions, d) {
		if !containsDate(sessions, d) {
			// The receipt session has not completed, so its range is not known yet.
			return "", false
		}
		if unverifiableDate == d {
			// The session completed but the stock body could not vouch for its bar. Waiting is
			// correct: falling back to the previous close would decide on a bar that was dropped.
			return "", false
		}
		bar := barOn(stock, d)
		if bar != nil && e >= bar.Low && e <= bar.High {
			return baseMatch, true
		}
		if p := lastSessionBefore(sessions, d); p != "" {
			if b, ok := lastBarAtOrBefore(stock, p); ok && b.Close == e {
				return baseMatch, true
			}
		}
		return baseMismatch, true
	}

	// Received after the close on a date the calendar does not show yet: it may be a session whose
	// bar has not completed, or a holiday. Comparing with the previous close now would call a
	// perfectly good quote a mismatch, so wait until a later session proves it is a holiday.
	if minute >= predSessionCloseMinute && !containsDate(sessions, d) && !dateKnownNotSession(sessions, d) {
		return "", false
	}

	var l string
	if intraday || minute < 9*60+30 {
		// Pre-open on a session day, or intraday on a day known to be a holiday: the last completed
		// session strictly before the receipt date.
		l = lastSessionBefore(sessions, d)
	} else {
		l = lastSessionAtOrBefore(sessions, d)
	}
	if l == "" {
		// The receipt predates every session in the fetched window: the body cannot answer the
		// question, and waiting would never end. Treat it as unverifiable, which is a mismatch.
		return baseMismatch, true
	}
	if unverifiableDate != "" && unverifiableDate == l {
		// The reference bar was dropped because the body could not vouch for it. Falling back to an
		// earlier close would decide on a bar the body itself said was not final.
		return "", false
	}
	b, ok := lastBarAtOrBefore(stock, l)
	if !ok {
		// The stock has no bar in the window at or before the reference session. The emitted price
		// cannot be checked against the market; it is a mismatch rather than an endless wait.
		return baseMismatch, true
	}
	if b.Close == e {
		return baseMatch, true
	}
	if p := lastSessionBefore(sessions, l); p != "" {
		if pb, ok := lastBarAtOrBefore(stock, p); ok && pb.Close == e {
			return baseMatch, true
		}
	}
	return baseMismatch, true
}

// dateKnownNotSession reports whether a date is known not to be a session: a later session is on the
// calendar and the date itself is not. Vendors publish in order, so a date with no bar and no later
// session may simply not have arrived yet.
func dateKnownNotSession(sessions []string, date string) bool {
	for _, s := range sessions {
		if s > date {
			return !containsDate(sessions, date)
		}
	}
	return false
}

func lastSessionBefore(sessions []string, date string) string {
	last := ""
	for _, s := range sessions {
		if s < date {
			last = s
		}
	}
	return last
}

func lastSessionAtOrBefore(sessions []string, date string) string {
	last := ""
	for _, s := range sessions {
		if s <= date {
			last = s
		}
	}
	return last
}

// stampProvesCurrent reports whether the vendor's own stamp says the session has been reached. A
// same-day stamp BEFORE the close cannot: a cached 14:57 body is not evidence that a stock stopped
// trading, it is a body fetched before the session ended.
func stampProvesCurrent(asOf, session string) bool {
	t, err := time.Parse(time.RFC3339, strings.TrimSpace(asOf))
	if err != nil {
		return false
	}
	lt := t.In(chinaZone)
	d := lt.Format("2006-01-02")
	if d > session {
		return true
	}
	if d < session {
		return false
	}
	close := time.Date(lt.Year(), lt.Month(), lt.Day(),
		predSessionCloseMinute/60, predSessionCloseMinute%60, 0, 0, chinaZone)
	return !lt.Before(close)
}

// asOfDate returns the civil date of a vendor stamp, or "" when it is absent or unreadable.
func asOfDate(asOf string) string {
	t, err := time.Parse(time.RFC3339, strings.TrimSpace(asOf))
	if err != nil {
		return ""
	}
	return t.In(chinaZone).Format("2006-01-02")
}

// newestBarFinal implements the completeness rule for the newest bar of a fetched body (F11).
//
// A body stamped on the bar's date is final only after the vendor's stamp has reached the close and
// the wall clock has passed the 15:30 gate; a body with no stamp cannot vouch for a bar dated today
// and that bar is unverifiable. Every earlier bar is final, which is why a suspended stock stamped
// at 09:00 keeps its last traded bar.
func newestBarFinal(b QuoteBar, asOf string, now time.Time) (final, unverifiable bool) {
	today := now.In(chinaZone).Format("2006-01-02")
	st := asOfDate(asOf)
	if st == "" {
		if b.Date == today {
			return false, true
		}
		return b.Date < today, false
	}
	if b.Date < st {
		return true, false
	}
	if b.Date > st {
		return false, false
	}
	t, err := time.Parse(time.RFC3339, strings.TrimSpace(asOf))
	if err != nil {
		return b.Date < today, false
	}
	lt := t.In(chinaZone)
	closed := !lt.Before(time.Date(lt.Year(), lt.Month(), lt.Day(),
		predSessionCloseMinute/60, predSessionCloseMinute%60, 0, 0, chinaZone))
	return closed && !now.In(chinaZone).Before(dateAtClose(b.Date)), false
}

// stockDatesNotIndexed reports whether any stock bar in the window is missing from the index's
// calendar. It is the per-stock consistency check, not an index-wide one.
func stockDatesNotIndexed(stock, index []QuoteBar, from, to string) bool {
	for _, b := range stock {
		if b.Date < from || b.Date > to {
			continue
		}
		if barOn(index, b.Date) == nil {
			return true
		}
	}
	return false
}

// ---------- daily-limit backstop (F14) ----------

// predDailyLimitBp is the price limit for the board a code belongs to, in basis points. 300/301 are
// ChiNext and 688/689 are STAR; Beijing is 30%; everything else is 10%. ST stocks are deliberately
// not modelled: the main-board risk-warning limit changed in 2025 and guessing it would flag real
// limit days. Their bonus issues are left to P8.
func predDailyLimitBp(symbol string) int64 {
	switch {
	case strings.HasPrefix(symbol, "300"), strings.HasPrefix(symbol, "301"),
		strings.HasPrefix(symbol, "688"), strings.HasPrefix(symbol, "689"):
		return 2000
	case marketPrefix(symbol) == "bj":
		return 3000
	default:
		return 1000
	}
}

// beyondLimitPrice reports whether a close lies outside the exchange's fen-rounded limit prices by
// more than one fen. Comparing rounded prices, rather than a basis-point bound on the ratio, is what
// keeps a cheap stock's legal limit day (¥1.05 → ¥1.16 is +10.48%) from being flagged.
func beyondLimitPrice(prevFen, closeFen, limitBp int64) bool {
	up := roundHalfUp(prevFen*(10000+limitBp), 10000)
	down := roundHalfUp(prevFen*(10000-limitBp), 10000)
	return closeFen > up+1 || closeFen < down-1
}

// roundHalfUp divides num by den, rounding half away from zero. Prices are non-negative here.
func roundHalfUp(num, den int64) int64 {
	if den <= 0 {
		return 0
	}
	if num >= 0 {
		return (num + den/2) / den
	}
	return (num - den/2) / den
}
