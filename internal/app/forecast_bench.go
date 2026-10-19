package app

// Benchmark identification (ADR 0029 §6, plan §3.1 F01/F02).
//
// A set is scored against the index it was frozen with, and that index is named by a string the
// owner typed ("sh000852"). Turning that string into a vendor request is not a job for marketPrefix:
// marketPrefix maps every 0xxxxx code to Shenzhen, so it files CSI 1000 (sh000852, a Shanghai index)
// under sz000852 — a Shenzhen STOCK with the same digits. Fetching that would score a whole cohort
// against an unrelated company's price.
//
// So the mapping is an explicit allowlist: each benchmark a set may be frozen with is a row here,
// with its market and vendor symbol spelled out, and each has a committed Tencent fixture under
// testdata/quote/. An empty or unknown code is refused, never guessed.

import (
	"database/sql"
	"log"
	"sort"
	"strings"
	"time"
)

// benchSpec is one allowed benchmark.
type benchSpec struct {
	Code   string // the stored, prefixed form: "sh000852"
	Market string // "sh" — the exchange that PUBLISHES the index, never inferred from the digits
	Sym    string // "000852" — the vendor's symbol key
}

// forecastBenchAllowlist is the closed list of benchmarks a set may be frozen with (V8). Every entry
// has a committed Tencent daily fixture under testdata/quote/tencent_fqkline_range_<code>.json,
// captured by a read-only probe, so the routing can be tested without a network.
var forecastBenchAllowlist = map[string]benchSpec{
	"sh000300": {Code: "sh000300", Market: "sh", Sym: "000300"}, // CSI 300
	"sh000905": {Code: "sh000905", Market: "sh", Sym: "000905"}, // CSI 500
	"sh000852": {Code: "sh000852", Market: "sh", Sym: "000852"}, // CSI 1000 (the October cohort)
	"sh000906": {Code: "sh000906", Market: "sh", Sym: "000906"}, // CSI 800 (from the 2026-11 cohort)
}

// benchSpecFor resolves a frozen benchmark code to its vendor request. The match is an exact key
// lookup after trimming whitespace: there is no prefix inference and no numeric parsing, so a code
// that is not a row above is refused rather than routed somewhere plausible.
func benchSpecFor(code string) (benchSpec, bool) {
	spec, ok := forecastBenchAllowlist[strings.TrimSpace(code)]
	return spec, ok
}

// forecastBenchCodes returns the allowed codes, sorted, for the admin GET and the settings Select.
func forecastBenchCodes() []string {
	out := make([]string, 0, len(forecastBenchAllowlist))
	for code := range forecastBenchAllowlist {
		out = append(out, code)
	}
	sort.Strings(out)
	return out
}

// The benchmark schedule (P4, decisions V8/B2). A change is effective from the NEXT cohort: the sets
// already written keep the index they were frozen with, and the ingest path starts using the new one
// when the cohort label reaches `next_from`. Promotion only makes the active value read true; the
// effective-dated reader is already correct before it.
const (
	setForecastBenchNext     = "forecast_bench_next"
	setForecastBenchNextFrom = "forecast_bench_next_from" // "YYYY-MM"
)

// ForecastBenchFor is the benchmark a set made in this cohort is frozen with. It is the only reader
// the ingest path uses: the frozen code and the cohort must come from the same instant.
func (s *Store) ForecastBenchFor(cohort string) string {
	if next, from := s.forecastScheduledBench(); next != "" && cohort >= from {
		return next
	}
	return s.ForecastBench()
}

// validForecastCohort accepts exactly "YYYY-MM". A malformed stored value (a restore, an out-of-band
// edit) must not make the lexicographic comparison below apply a change early or never.
func validForecastCohort(s string) bool {
	t, err := time.Parse("2006-01", s)
	return err == nil && t.Format("2006-01") == s
}

// shanghaiCohort is the cohort label of an instant: the Shanghai civil month. Ingest and the schedule
// both derive it this way, so "which cohort is this" has one answer.
func shanghaiCohort(t time.Time) string {
	d, err := shanghaiDate(t)
	if err != nil {
		return ""
	}
	return d[:7]
}

// nextCohort advances "YYYY-MM" by one month, rolling the year over.
func nextCohort(cohort string) string {
	t, err := time.Parse("2006-01", cohort)
	if err != nil {
		return cohort
	}
	return t.AddDate(0, 1, 0).Format("2006-01")
}

// forecastScheduledBench is the pending schedule, if any: the code and its effective cohort. It reads
// the pair in ONE statement, so a concurrent save cannot be observed as a mixed pair (the two keys are
// committed together). A pair that is incomplete or whose cohort is malformed is treated as nothing.
func (s *Store) forecastScheduledBench() (code, from string) {
	rows, err := s.query(`SELECT k, v FROM meta WHERE k IN (?,?)`, setForecastBenchNext, setForecastBenchNextFrom)
	if err != nil {
		return "", ""
	}
	defer rows.Close()
	pair := map[string]string{}
	for rows.Next() {
		var k, v string
		if rows.Scan(&k, &v) == nil {
			pair[k] = strings.TrimSpace(v)
		}
	}
	code, from = pair[setForecastBenchNext], pair[setForecastBenchNextFrom]
	if code == "" || !validForecastCohort(from) {
		return "", ""
	}
	return code, from
}

// earliestCohortBench is the frozen code of a cohort's earliest in-population accepted set for one
// rtype. It is the cohort's benchmark by construction: the first set written in a cohort defines the
// index the rest are expected to match, and a later set that differs is off-benchmark.
func (s *Store) earliestCohortBench(rtype, cohort string) (string, error) {
	where := "rtype=? AND state=? AND cohort=?"
	args := []any{rtype, predStateAccepted, cohort}
	if from, ok := s.ForecastPopulationFrom(); ok {
		where += " AND made_at >= ?"
		args = append(args, from.UTC().Format(time.RFC3339))
	}
	var code sql.NullString
	err := s.queryRow("SELECT bench_code FROM prediction_sets WHERE "+where+" ORDER BY made_at, id LIMIT 1", args...).Scan(&code)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return code.String, nil
}

// earliestLedgerBench is earliestCohortBench without the rtype: boot reconciliation is ledger-wide.
func (s *Store) earliestLedgerBench(cohort string) (string, error) {
	where := "state=? AND cohort=?"
	args := []any{predStateAccepted, cohort}
	if from, ok := s.ForecastPopulationFrom(); ok {
		where += " AND made_at >= ?"
		args = append(args, from.UTC().Format(time.RFC3339))
	}
	var code sql.NullString
	err := s.queryRow("SELECT bench_code FROM prediction_sets WHERE "+where+" ORDER BY made_at, id LIMIT 1", args...).Scan(&code)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return code.String, nil
}

// applyForecastBenchState writes the active value and the pending schedule in one transaction. An
// empty next clears the schedule. It does not audit; the caller holds the reason and the actor.
func (s *Store) applyForecastBenchState(active, next, nextFrom string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // a no-op once committed
	if _, err := tx.Exec(s.bind(`INSERT INTO meta(k,v) VALUES(?,?) ON CONFLICT(k) DO UPDATE SET v=excluded.v`),
		setForecastBench, active); err != nil {
		return err
	}
	if _, err := tx.Exec(s.bind(`DELETE FROM meta WHERE k IN (?,?)`), setForecastBenchNext, setForecastBenchNextFrom); err != nil {
		return err
	}
	if next != "" && nextFrom != "" {
		for _, kv := range [][2]string{{setForecastBenchNext, next}, {setForecastBenchNextFrom, nextFrom}} {
			if _, err := tx.Exec(s.bind(`INSERT INTO meta(k,v) VALUES(?,?) ON CONFLICT(k) DO UPDATE SET v=excluded.v`),
				kv[0], kv[1]); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

// promoteForecastBench makes a due schedule active. ForecastBenchFor already returned the new code
// for the new cohort before this ran; promotion only makes the active value read true. Idempotent:
// with nothing scheduled, or before the effective cohort, it does nothing.
func (s *Server) promoteForecastBench(now time.Time) {
	next, from := s.st.forecastScheduledBench()
	if next == "" || shanghaiCohort(now) < from {
		return
	}
	if err := s.st.applyForecastBenchState(next, "", ""); err != nil {
		log.Printf("forecast bench: promote %s: %v", next, err)
		return
	}
	s.recordChange(nil, "system", AuditForecastBench, "settings", setForecastBench,
		map[string]any{"reason": "promote", "to": next, "effective_cohort": from})
}

// reconcileForecastBench aligns the active value with what the current cohort was actually frozen
// with. In production the active value already matches, so this is a no-op; it exists so a restore or
// an out-of-band edit cannot leave the active value describing a different index than the ledger.
func (s *Server) reconcileForecastBench(now time.Time) error {
	cohort := shanghaiCohort(now)
	active := s.st.ForecastBench()
	frozen, err := s.st.earliestLedgerBench(cohort)
	if err != nil {
		return err
	}
	if frozen == "" || frozen == active {
		return nil
	}
	// Keep the old active value reachable: schedule it for the next cohort if nothing else is. If a
	// schedule already exists, it wins — reconciliation must not clobber an admin's decision.
	schedCode, schedFrom := s.st.forecastScheduledBench()
	next, nextFrom := schedCode, schedFrom
	if schedCode == "" {
		next, nextFrom = active, nextCohort(cohort)
	}
	if err := s.st.applyForecastBenchState(frozen, next, nextFrom); err != nil {
		return err
	}
	s.recordChange(nil, "system", AuditForecastBench, "settings", setForecastBench,
		map[string]any{"reason": "reconcile", "from": active, "to": frozen})
	if schedCode == "" {
		s.recordChange(nil, "system", AuditForecastBench, "settings", setForecastBenchNext,
			map[string]any{"reason": "reconcile", "to": active, "effective_cohort": nextFrom})
	}
	return nil
}
