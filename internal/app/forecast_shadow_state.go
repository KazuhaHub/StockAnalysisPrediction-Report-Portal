package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

const (
	setForecastStateShadow  = "forecast_state_shadow"
	setForecastStateCalls   = "forecast_state_calls"
	forecastDiagnosticLimit = 2 << 20
)

type forecastShadowResult struct {
	At              string                 `json:"at"`
	Target          quoteProbeTarget       `json:"target"`
	Primary         string                 `json:"primary"`
	Candidate       string                 `json:"candidate"`
	Anchors         [2]string              `json:"anchors"`
	Status          string                 `json:"status"`
	Reason          string                 `json:"reason,omitempty"`
	Comparison      *quoteShadowComparison `json:"comparison,omitempty"`
	DifferenceCount int                    `json:"difference_count"`
	Truncated       bool                   `json:"truncated"`
}

type forecastShadowRun struct {
	At           string
	Reads        int
	HTTPAttempts int
	Results      []forecastShadowResult
}

// Calls counts source observations, not HTTP requests. ShadowHTTPAttempts is deliberately named:
// primary transport traffic is not measured by the candidate's private client override.
type forecastCallObservation struct {
	Source             string
	Calls              int64
	Failure            bool
	Refusal            string
	ShadowHTTPAttempts int64
}

type forecastSourceCounts struct {
	Calls              int64            `json:"calls"`
	Failures           int64            `json:"failures"`
	Refusals           map[string]int64 `json:"refusals"`
	ShadowHTTPAttempts int64            `json:"shadow_http_attempts"`
}

type forecastCallDay struct {
	Date    string                          `json:"date"`
	Sources map[string]forecastSourceCounts `json:"sources"`
}

type forecastCallState struct {
	Version   int               `json:"version"`
	UpdatedAt string            `json:"updated_at"`
	Days      []forecastCallDay `json:"days"`
}

type forecastShadowDay struct {
	Date         string `json:"date"`
	Selected     int64  `json:"selected"`
	Reads        int64  `json:"reads"`
	HTTPAttempts int64  `json:"http_attempts"`
	Matches      int64  `json:"matches"`
	Mismatches   int64  `json:"mismatches"`
	Unavailable  int64  `json:"unavailable"`
}

type forecastShadowState struct {
	Version   int                    `json:"version"`
	UpdatedAt string                 `json:"updated_at"`
	Days      []forecastShadowDay    `json:"days"`
	Recent    []forecastShadowResult `json:"recent"`
}

func decodeForecastDiagnostics(raw map[string]string) (forecastShadowState, forecastCallState, error) {
	shadow := forecastShadowState{Version: 1, Days: []forecastShadowDay{}, Recent: []forecastShadowResult{}}
	calls := forecastCallState{Version: 1, Days: []forecastCallDay{}}
	_, hasShadow := raw[setForecastStateShadow]
	_, hasCalls := raw[setForecastStateCalls]
	if hasShadow != hasCalls || (hasShadow && (raw[setForecastStateShadow] == "" || raw[setForecastStateCalls] == "")) {
		return shadow, calls, fmt.Errorf("uninitialized or incomplete forecast diagnostic pair")
	}
	if raw[setForecastStateShadow] != "" {
		shadow = forecastShadowState{}
		calls = forecastCallState{}
	}
	for _, item := range []struct {
		key   string
		value any
	}{{setForecastStateShadow, &shadow}, {setForecastStateCalls, &calls}} {
		body := raw[item.key]
		if len(body) > forecastDiagnosticLimit {
			return shadow, calls, fmt.Errorf("forecast diagnostics exceed limit")
		}
		if body != "" {
			if !strings.HasPrefix(strings.TrimSpace(body), "{") {
				return shadow, calls, fmt.Errorf("forecast diagnostics must be objects")
			}
			if err := decodeQuoteProbeJSON([]byte(body), item.value); err != nil {
				return shadow, calls, fmt.Errorf("decode %s: %w", item.key, err)
			}
		}
	}
	if shadow.Version != 1 || calls.Version != 1 {
		return shadow, calls, fmt.Errorf("unsupported forecast diagnostic version")
	}
	if raw[setForecastStateShadow] != "" {
		if err := validateForecastDiagnostics(shadow, calls); err != nil {
			return shadow, calls, err
		}
	}
	return shadow, calls, nil
}

// One statement gives readers one snapshot of the pair rather than two independently timed gets.
func (s *Store) forecastDiagnostics() (forecastShadowState, forecastCallState, error) {
	return s.forecastDiagnosticsContext(context.Background())
}

func (s *Store) forecastDiagnosticsContext(ctx context.Context) (forecastShadowState, forecastCallState, error) {
	for attempt := 0; ; attempt++ {
		shadow, calls, err := s.forecastDiagnosticsOnce(ctx)
		if err == nil || !isContentionErr(err) || attempt == 5 {
			return shadow, calls, err
		}
		timer := time.NewTimer(time.Duration(10<<attempt) * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return shadow, calls, ctx.Err()
		case <-timer.C:
		}
	}
}

func (s *Store) forecastDiagnosticsOnce(ctx context.Context) (forecastShadowState, forecastCallState, error) {
	rows, err := s.db.QueryContext(ctx, s.bind(`SELECT k,COALESCE(v,'') FROM meta WHERE k IN (?,?)`), setForecastStateCalls, setForecastStateShadow)
	if err != nil {
		return forecastShadowState{}, forecastCallState{}, err
	}
	defer rows.Close()
	raw := map[string]string{}
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			return forecastShadowState{}, forecastCallState{}, err
		}
		raw[key] = value
	}
	if err := rows.Err(); err != nil {
		return forecastShadowState{}, forecastCallState{}, err
	}
	return decodeForecastDiagnostics(raw)
}

func (s *Store) saveForecastDiagnostics(ctx context.Context, run forecastShadowRun, observations []forecastCallObservation) error {
	for attempt := 0; ; attempt++ {
		err := s.saveForecastDiagnosticsOnce(ctx, run, observations)
		if err == nil || !isContentionErr(err) || attempt == 5 {
			return err
		}
		timer := time.NewTimer(time.Duration(10<<attempt) * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func (s *Store) saveForecastDiagnosticsOnce(ctx context.Context, run forecastShadowRun, observations []forecastCallObservation) error {
	at, err := time.Parse(time.RFC3339, run.At)
	if err != nil || run.Reads < 0 || run.Reads > forecastShadowMaxComparisons || run.Reads > len(run.Results) || run.HTTPAttempts < 0 || run.HTTPAttempts > forecastShadowMaxHTTPAttempts || run.HTTPAttempts > 4*run.Reads || len(run.Results) > forecastShadowMaxComparisons {
		return fmt.Errorf("invalid forecast shadow run")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after commit
	// Stable row order serializes independent writers and avoids a two-key lock inversion.
	inserted := int64(0)
	for _, key := range []string{setForecastStateCalls, setForecastStateShadow} {
		result, err := tx.ExecContext(ctx, s.bind(`INSERT INTO meta(k,v) VALUES(?,?) ON CONFLICT(k) DO NOTHING`), key, "")
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return err
		}
		inserted += n
	}
	raw, err := s.lockForecastDiagnostics(ctx, tx)
	if err != nil {
		return err
	}
	if inserted == 2 {
		raw = map[string]string{}
	} else if inserted != 0 {
		return fmt.Errorf("incomplete forecast diagnostic pair")
	}
	shadow, calls, err := decodeForecastDiagnostics(raw)
	if err != nil {
		return err
	}
	watermark := at.UTC()
	if previous, err := time.Parse(time.RFC3339, shadow.UpdatedAt); err == nil && previous.After(watermark) {
		watermark = previous
	}
	date := watermark.Format(quoteWindowLayout)
	runDate := at.UTC().Format(quoteWindowLayout)
	cutoff := watermark.AddDate(0, 0, -13).Format(quoteWindowLayout)
	keptShadow := []forecastShadowDay{}
	for _, day := range shadow.Days {
		if day.Date >= cutoff && day.Date <= date {
			keptShadow = append(keptShadow, day)
		}
	}
	shadow.Days = keptShadow
	keptCalls := []forecastCallDay{}
	for _, day := range calls.Days {
		if day.Date >= cutoff && day.Date <= date {
			keptCalls = append(keptCalls, day)
		}
	}
	calls.Days = keptCalls
	recent := []forecastShadowResult{}
	for _, result := range shadow.Recent {
		if len(result.At) >= 10 && result.At[:10] >= cutoff && result.At[:10] <= date {
			recent = append(recent, result)
		}
	}
	// A delayed pass can contribute inside the latest known window, but cannot rewind retention.
	if runDate >= cutoff {
		si, ci := -1, -1
		for i := range shadow.Days {
			if shadow.Days[i].Date == runDate {
				si = i
			}
		}
		for i := range calls.Days {
			if calls.Days[i].Date == runDate {
				ci = i
			}
		}
		if si < 0 {
			shadow.Days = append(shadow.Days, forecastShadowDay{Date: runDate})
			si = len(shadow.Days) - 1
		}
		if ci < 0 {
			calls.Days = append(calls.Days, forecastCallDay{Date: runDate, Sources: map[string]forecastSourceCounts{}})
			ci = len(calls.Days) - 1
		}
		day := &shadow.Days[si]
		day.Selected += int64(len(run.Results))
		day.Reads += int64(run.Reads)
		day.HTTPAttempts += int64(run.HTTPAttempts)
		for _, result := range run.Results {
			switch result.Status {
			case "match":
				day.Matches++
			case "mismatch":
				day.Mismatches++
			case "unavailable":
				day.Unavailable++
			default:
				return fmt.Errorf("invalid comparison status")
			}
			result.At = at.UTC().Format(time.RFC3339)
			if result.Comparison != nil {
				comparison := *result.Comparison
				result.DifferenceCount = len(comparison.Differences)
				limit := min(len(comparison.Differences), 32)
				comparison.Differences = append([]quoteShadowDifference{}, comparison.Differences[:limit]...)
				result.Truncated = limit < result.DifferenceCount
				result.Comparison = &comparison
			}
			recent = append(recent, result)
		}
		if calls.Days[ci].Sources == nil {
			calls.Days[ci].Sources = map[string]forecastSourceCounts{}
		}
		for _, observation := range observations {
			if observation.Source == "" || len(observation.Source) > 64 || observation.Calls < 0 || observation.ShadowHTTPAttempts < 0 || len(observation.Refusal) > 64 {
				return fmt.Errorf("invalid call observation")
			}
			count := calls.Days[ci].Sources[observation.Source]
			count.Calls += observation.Calls
			count.ShadowHTTPAttempts += observation.ShadowHTTPAttempts
			if observation.Failure {
				count.Failures++
			}
			if count.Refusals == nil {
				count.Refusals = map[string]int64{}
			}
			if observation.Refusal != "" {
				count.Refusals[observation.Refusal]++
			}
			calls.Days[ci].Sources[observation.Source] = count
		}
	}
	sort.SliceStable(recent, func(i, j int) bool { return recent[i].At < recent[j].At })
	if len(recent) > 20 {
		recent = recent[len(recent)-20:]
	}
	shadow.Recent = recent
	sort.Slice(shadow.Days, func(i, j int) bool { return shadow.Days[i].Date < shadow.Days[j].Date })
	sort.Slice(calls.Days, func(i, j int) bool { return calls.Days[i].Date < calls.Days[j].Date })
	shadow.UpdatedAt = watermark.Format(time.RFC3339)
	calls.UpdatedAt = shadow.UpdatedAt
	if err := validateForecastDiagnostics(shadow, calls); err != nil {
		return err
	}
	for _, item := range []struct {
		key   string
		value any
	}{{setForecastStateCalls, calls}, {setForecastStateShadow, shadow}} {
		body, err := json.Marshal(item.value)
		if err != nil {
			return err
		}
		if len(body) > forecastDiagnosticLimit {
			return fmt.Errorf("forecast diagnostics exceed limit")
		}
		if _, err := tx.ExecContext(ctx, s.bind(`UPDATE meta SET v=? WHERE k=?`), string(body), item.key); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) lockForecastDiagnostics(ctx context.Context, tx *sql.Tx) (map[string]string, error) {
	query := `SELECT k,COALESCE(v,'') FROM meta WHERE k IN (?,?) ORDER BY k`
	if s.driver == "postgres" {
		query += " FOR UPDATE"
	}
	rows, err := tx.QueryContext(ctx, s.bind(query), setForecastStateCalls, setForecastStateShadow)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	raw := map[string]string{}
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			return nil, err
		}
		raw[key] = value
	}
	return raw, rows.Err()
}
