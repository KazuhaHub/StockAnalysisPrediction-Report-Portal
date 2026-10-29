package app

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"
)

const setForecastDeliveryFailures = "forecast_delivery_failures"

// Delivery diagnostics live in bounded operational metadata, never in scored ledger fields.
// Only the admin settings route publishes them; reader publication gates are unchanged.
type forecastDeliveryFailure struct {
	SetID  int64  `json:"set_id"`
	Symbol string `json:"symbol"`
	Stage  string `json:"stage"`
	Source string `json:"source"`
	From   string `json:"from"`
	To     string `json:"to"`
	At     string `json:"at"`
	Detail string `json:"detail"`
}

var forecastDiagnosticURL = regexp.MustCompile(`https?://[^\s"<>]+`)

func forecastDeliveryDetail(err error) string {
	// Queries and credentials are irrelevant to diagnosing delivery; keep only host/path.
	var status *vendorStatusError
	if errors.As(err, &status) {
		return fmt.Sprintf("HTTP %d from %s %s", status.StatusCode, status.Host, status.RequestPath)
	}
	text := forecastDiagnosticURL.ReplaceAllStringFunc(err.Error(), func(raw string) string {
		u, e := url.Parse(raw)
		if e != nil {
			return "[URL]"
		}
		return u.Scheme + "://" + u.Hostname() + u.EscapedPath()
	})
	text = strings.Join(strings.Fields(text), " ")
	r := []rune(text)
	if len(r) > 400 {
		text = string(r[:400]) + "…"
	}
	return text
}

func (s *Store) forecastDeliveryFailures() []forecastDeliveryFailure {
	out := []forecastDeliveryFailure{}
	if err := json.Unmarshal([]byte(s.GetSetting(setForecastDeliveryFailures, "[]")), &out); err != nil {
		return nil
	}
	return out
}

func (e *forecastEvaluator) recordDeliveryFailure(s *Server, set forecastWorkSet, stage, source, from, to string, err error) {
	if err == nil || isVendorDeferred(err) {
		return
	}
	if source == "" {
		primary, _ := ledgerPrimary(s.quotes.configuredLedgerSources(s.quoteConfigLoad(), quoteIntervalSettled))
		source = primary.name
	}
	rows := s.st.forecastDeliveryFailures()
	out := []forecastDeliveryFailure{{SetID: set.SetID, Symbol: set.Symbol, Stage: stage, Source: source, From: from, To: to,
		At: e.clock().UTC().Format(time.RFC3339), Detail: forecastDeliveryDetail(err)}}
	for _, row := range rows {
		if row.SetID != set.SetID && len(out) < 50 {
			out = append(out, row)
		}
	}
	s.st.saveForecastDeliveryFailures(out)
}

func (s *Store) saveForecastDeliveryFailures(rows []forecastDeliveryFailure) {
	body, err := json.Marshal(rows)
	if err == nil {
		err = s.SetSetting(setForecastDeliveryFailures, string(body))
	}
	if err != nil {
		log.Printf("forecast delivery diagnostics: %v", err)
	}
}

func (s *Store) clearForecastDeliveryFailure(setID int64) {
	rows := s.forecastDeliveryFailures()
	out := make([]forecastDeliveryFailure, 0, len(rows))
	for _, row := range rows {
		if row.SetID != setID {
			out = append(out, row)
		}
	}
	if len(out) != len(rows) {
		s.saveForecastDeliveryFailures(out)
	}
}

type forecastBacklogItem struct {
	SetID       int64                    `json:"set_id"`
	Symbol      string                   `json:"symbol"`
	Checkpoint  string                   `json:"checkpoint"`
	Due         string                   `json:"due"`
	Minutes     int                      `json:"minutes"`
	Refusal     string                   `json:"refusal,omitempty"`
	NextAttempt string                   `json:"next_attempt_at,omitempty"`
	Failure     *forecastDeliveryFailure `json:"failure,omitempty"`
}

type forecastBacklog struct {
	AsOf          string                `json:"as_of"`
	Due           int                   `json:"due"`
	Overdue       int                   `json:"overdue"`
	Retrying      int                   `json:"retrying"`
	OldestMinutes int                   `json:"oldest_minutes"`
	Items         []forecastBacklogItem `json:"items"`
	Truncated     bool                  `json:"truncated"`
}

// Count completed due sessions across the ledger, including marks sleeping until a retry.
// A recorded due session or an observed benchmark calendar is required; weekday projections
// must never turn a holiday or an unavailable calendar into a six-hour delivery alarm.
func (s *Server) forecastBacklog(now time.Time) (forecastBacklog, error) {
	out := forecastBacklog{AsOf: now.UTC().Format(time.RFC3339), Items: []forecastBacklogItem{}}
	failures := map[int64]forecastDeliveryFailure{}
	for _, f := range s.st.forecastDeliveryFailures() {
		failures[f.SetID] = f
	}
	rows, err := s.st.query(`SELECT ps.id, ps.symbol, ps.made_on, ps.made_at, ps.horizon_std, ps.s0_date, ps.bench_code,
 pm.checkpoint, pm.status, pm.session_date, pm.refusal, pm.next_attempt_at
 FROM prediction_sets ps JOIN prediction_marks pm ON pm.set_id=ps.id
 WHERE ps.state=? AND pm.status IN (?,?)`, predStateAccepted, markPending, markRefused)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	calendars := s.forecastEval.readCalendarSnapshot()
	for rows.Next() {
		var set forecastWorkSet
		var mark forecastMark
		var standard int
		var s0 sql.NullString
		if err := rows.Scan(&set.SetID, &set.Symbol, &set.MadeOn, &set.MadeAt, &standard, &s0, &set.BenchCode,
			&mark.Checkpoint, &mark.Status, &mark.SessionDate, &mark.Refusal, &mark.NextAttemptAt); err != nil {
			return out, err
		}
		set.HorizonStd = standard == 1
		set.S0Date = s0.String
		due := mark.SessionDate
		// Reproduce the session before trusting a calendar rather than estimating from weekdays.
		if made, ok := setMadeAt(set); ok {
			cal := calendars[set.BenchCode]
			if len(cal) > 0 && cal[0] <= set.MadeOn && storedS0Reproduced(set.S0Date, made, cal) {
				sch := predScheduleFor(mark.Checkpoint, made, set.HorizonStd, set.S0Date, cal)
				if sch.Known && containsDate(cal, sch.Due) {
					due = sch.Due
				}
			}
		}
		if !validReportDate(due) || !readSessionCompleted(due, now) {
			continue
		}
		at := dateAtClose(due)
		if at.IsZero() || now.Before(at) {
			continue
		}
		minutes := int(now.Sub(at) / time.Minute)
		item := forecastBacklogItem{SetID: set.SetID, Symbol: set.Symbol, Checkpoint: mark.Checkpoint, Due: due, Minutes: minutes, Refusal: mark.Refusal, NextAttempt: mark.NextAttemptAt}
		if f, ok := failures[set.SetID]; ok {
			f := f
			item.Failure = &f
		}
		out.Due++
		if minutes > int(forecastLaggingAfter/time.Minute) {
			out.Overdue++
		}
		if mark.Status == markRefused {
			out.Retrying++
		}
		if minutes > out.OldestMinutes {
			out.OldestMinutes = minutes
		}
		out.Items = append(out.Items, item)
	}
	if err := rows.Err(); err != nil {
		return out, err
	}
	sort.Slice(out.Items, func(i, j int) bool {
		a, b := out.Items[i], out.Items[j]
		if a.Due != b.Due {
			return a.Due < b.Due
		}
		if a.SetID != b.SetID {
			return a.SetID < b.SetID
		}
		return a.Checkpoint < b.Checkpoint
	})
	if len(out.Items) > 20 {
		out.Items = out.Items[:20]
		out.Truncated = true
	}
	return out, nil
}
