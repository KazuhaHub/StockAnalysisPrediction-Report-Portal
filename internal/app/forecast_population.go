package app

// The forecast ledger start (ADR 0029, decisions V3).
//
// Sets 1–6 in production are integration runs made before the ledger was declared live. They are NOT
// deleted — a ledger that rewrites its own history is worth nothing — and instead a single write-once,
// bounded, audited instant is recorded: every set made before it is `pre_live`. A pre-live set is
// still selected, settled, counted and purged like any other; it simply never enters the record, and
// in particular it can never make a later real set a duplicate (plan B4).
//
// The value lives in `meta` under forecast_population_from as an RFC3339 UTC instant. The key is
// WRITE-ONCE by construction: once present, a second save is refused, and a restore may carry it from
// the live database but may never silently move it.

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"time"
)

const setForecastPopulationFrom = "forecast_population_from"

// populationBound is the latest instant a ledger start may name: the first 15:00 Asia/Shanghai on a
// weekday strictly after the earliest made_at in the ledger. It needs no exchange calendar; a holiday
// only moves the true first S0 close later, so the bound is conservative. A start later than this
// would leave a set that has already had sessions to settle inside the record.
func populationBound(earliestMadeAt time.Time) time.Time {
	t := earliestMadeAt.In(chinaZone)
	// Start from the receipt's own date when the receipt was before the close, so a report received
	// on a weekday before 15:00 can be cut at that day's close; otherwise from the next day.
	day := time.Date(t.Year(), t.Month(), t.Day(), 15, 0, 0, 0, chinaZone)
	if !t.Before(day) {
		day = day.AddDate(0, 0, 1)
	}
	for day.Weekday() == time.Saturday || day.Weekday() == time.Sunday {
		day = day.AddDate(0, 0, 1)
	}
	return day
}

// populationCandidate is one set a ledger start might exclude. It never carries the payload.
type populationCandidate struct {
	ID     int64  `json:"id"`
	Symbol string `json:"symbol"`
	Rtype  string `json:"rtype"`
	MadeAt string `json:"made_at"`
	State  string `json:"state"`
	Orphan bool   `json:"orphan"` // report_id is NULL: its report was purged
}

// ForecastPopulationFrom returns the ledger start and whether one is set.
//
// "Set" is decided by the PRESENCE of the key, not by whether its value parses: a stored value that
// cannot be parsed is a damaged database, and reporting it as unset would let a second save overwrite
// it (the write-once guard would pass) and quietly move the boundary. A damaged value keeps the key
// set, returns ok=false here so callers do not act on a wrong instant, and leaves the operator to
// inspect it.
func (s *Store) ForecastPopulationFrom() (time.Time, bool) {
	raw := s.populationFromRow()
	if raw == "" {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

// forecastPopulationIsSet reports whether the key exists at all, whatever its value. It counts the
// row rather than reading the value, so a row that is present but empty (” or NULL) is still "set":
// write-once must hold, and the UI must not offer a form whose save would always be refused.
func (s *Store) forecastPopulationIsSet() bool {
	var n int
	if err := s.queryRow(`SELECT COUNT(*) FROM meta WHERE k=?`, setForecastPopulationFrom).Scan(&n); err != nil {
		return false
	}
	return n > 0
}

// ForecastPopulationSetBy returns the actor and instant of the write that set the ledger start, from
// the audit row, as far as the log still holds it. Empty strings when the log has been pruned or the
// row predates this feature. It is display only: the authority is the meta key.
func (s *Store) ForecastPopulationSetBy() (actor, at string) {
	_ = s.queryRow(`SELECT actor, at FROM audit_log WHERE action=? ORDER BY id DESC LIMIT 1`,
		AuditForecastPopulation).Scan(&actor, &at)
	return actor, at
}

// forecastEarliestMadeAt is the receipt instant of the ledger's oldest set, and whether the ledger
// has any set at all.
func (s *Store) forecastEarliestMadeAt() (time.Time, bool, error) {
	var raw sql.NullString
	if err := s.queryRow(`SELECT MIN(made_at) FROM prediction_sets`).Scan(&raw); err != nil {
		return time.Time{}, false, err
	}
	if !raw.Valid || raw.String == "" {
		return time.Time{}, false, nil
	}
	t, err := time.Parse(time.RFC3339, raw.String)
	if err != nil {
		return time.Time{}, false, fmt.Errorf("earliest made_at %q is not RFC3339: %w", raw.String, err)
	}
	return t, true, nil
}

// forecastPopulationCandidates lists every set a ledger start at or before `bound` could exclude,
// oldest first, so an admin can see exactly what would move out of the record. It never selects the
// payload.
func (s *Store) forecastPopulationCandidates(bound time.Time) ([]populationCandidate, error) {
	rows, err := s.query(`SELECT id, symbol, rtype, made_at, state, report_id
		FROM prediction_sets WHERE made_at <= ? ORDER BY made_at, id`, bound.UTC().Format(time.RFC3339))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []populationCandidate
	for rows.Next() {
		var c populationCandidate
		var reportID sql.NullInt64
		if err := rows.Scan(&c.ID, &c.Symbol, &c.Rtype, &c.MadeAt, &c.State, &reportID); err != nil {
			return nil, err
		}
		c.Orphan = !reportID.Valid
		out = append(out, c)
	}
	return out, rows.Err()
}

// SaveForecastPopulationFrom validates and writes the ledger start in one transaction, on the tx
// handle only (§0.1's single-connection rule). It returns the excluded set ids and their counts by
// state for the audit row, or a stable code on refusal with nothing written.
func (s *Store) SaveForecastPopulationFrom(from, now time.Time) (excluded []int64, byState map[string]int, code string, err error) {
	byState = map[string]int{}
	if from.IsZero() {
		return nil, byState, "invalid_param", nil
	}
	// 1. Write-once. The pool read is only a fast path; the authoritative check is the conflict-safe
	// write below, because two admins (or a retried request) can pass this line together. Presence is
	// what matters here, not parseability: a damaged value must not re-open the key.
	if s.forecastPopulationIsSet() {
		return nil, byState, "forecast_population_set", nil
	}
	// 2. Not in the future.
	if from.After(now) {
		return nil, byState, "forecast_population_future", nil
	}
	// 3. Not past the bound, which is derived from the ledger's own oldest set. An empty ledger has no
	// bound and the save is allowed: there is nothing to exclude yet.
	earliest, hasSets, err := s.forecastEarliestMadeAt()
	if err != nil {
		return nil, byState, "", err
	}
	if hasSets && from.After(populationBound(earliest)) {
		return nil, byState, "forecast_population_too_late", nil
	}
	// 4. Not once the first cohort can already publish a result: moving the boundary then would erase
	// settled work from the record.
	mature, err := s.firstCohortMature(now)
	if err != nil {
		return nil, byState, "", err
	}
	if mature {
		return nil, byState, "forecast_population_month_mature", nil
	}

	// The write, plus the excluded-set accounting, on one connection.
	tx, err := s.db.Begin()
	if err != nil {
		return nil, byState, "", err
	}
	defer tx.Rollback() //nolint:errcheck // no-op once committed

	// The write is the write-once gate: DO NOTHING means a row already present (including one whose
	// value is unreadable, so a corrupt key cannot be silently overwritten and re-opened) leaves zero
	// rows affected, and the save is refused. This is what makes write-once hold under a race and not
	// merely under the fast-path read above.
	res, err := tx.Exec(s.bind(`INSERT INTO meta(k,v) VALUES(?,?) ON CONFLICT(k) DO NOTHING`),
		setForecastPopulationFrom, from.UTC().Format(time.RFC3339))
	if err != nil {
		return nil, byState, "", err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, byState, "forecast_population_set", nil
	}

	rows, err := tx.Query(s.bind(`SELECT id, state FROM prediction_sets WHERE made_at < ?`),
		from.UTC().Format(time.RFC3339))
	if err != nil {
		return nil, byState, "", err
	}
	for rows.Next() {
		var id int64
		var state string
		if err := rows.Scan(&id, &state); err != nil {
			rows.Close()
			return nil, byState, "", err
		}
		excluded = append(excluded, id)
		byState[state]++
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, byState, "", err
	}
	rows.Close()
	sort.Slice(excluded, func(i, j int) bool { return excluded[i] < excluded[j] })

	// The floor for "unrecorded" (P2): captured here, inside the same transaction as the boundary, so
	// it is a single snapshot of "which reports existed when the ledger began". Later audit retention
	// cannot move it.
	if err := storeUnrecordedFloor(migExec{s: s, ex: tx}, from); err != nil {
		return nil, byState, "", err
	}

	if err := tx.Commit(); err != nil {
		return nil, byState, "", err
	}
	return excluded, byState, "", nil
}

// The unrecorded floor keys (P2).
const (
	setForecastUnrecordedFloorID     = "forecast_unrecorded_floor_id"
	setForecastUnrecordedFloorAt     = "forecast_unrecorded_floor_at"
	setForecastUnrecordedFloorMethod = "forecast_unrecorded_floor_method"
)

// captureUnrecordedFloor finds the highest report id that existed at the ledger start, so a count of
// "covered reports with no set" can ignore the pre-ledger history. It prefers the ingest audit trail
// (every created covered report leaves a row with detail.created) and falls back to the current max
// id when the audit has been pruned. Runs on the given executor only (tx during the save, pool when
// re-deriving on read).
func captureUnrecordedFloor(e migExec, from time.Time) (int64, string, error) {
	rows, err := e.query(`SELECT target_id, detail FROM audit_log WHERE action=? AND at >= ? ORDER BY id`,
		AuditReportIngest, from.UTC().Format(time.RFC3339))
	if err != nil {
		return 0, "", err
	}
	defer rows.Close()
	var minID int64
	found := false
	for rows.Next() {
		var targetID, detail sql.NullString
		if err := rows.Scan(&targetID, &detail); err != nil {
			return 0, "", err
		}
		if !targetID.Valid || !detail.Valid {
			continue
		}
		var d struct {
			Created bool `json:"created"`
		}
		if json.Unmarshal([]byte(detail.String), &d) != nil || !d.Created {
			continue
		}
		id, err := strconv.ParseInt(targetID.String, 10, 64)
		if err != nil {
			continue
		}
		if !found || id < minID {
			minID, found = id, true
		}
	}
	if err := rows.Err(); err != nil {
		return 0, "", err
	}
	if found {
		if minID > 0 {
			minID--
		}
		return minID, "audit", nil
	}
	var maxID int64
	if err := e.queryRow(`SELECT COALESCE(MAX(id),0) FROM reports`).Scan(&maxID); err != nil {
		return 0, "", err
	}
	return maxID, "max_id", nil
}

// forecastUnrecordedFloor returns the floor captured when the ledger start was set. ok=false when it
// is absent: the count then shows nothing rather than being re-derived on a read path, because
// re-deriving after the audit retention window has moved would read MAX(reports.id) and perpetually
// return 0. The floor is captured at save time and re-derived once by a restore (finishRestore) and at
// boot for a database whose boundary predates this key.
func (s *Store) forecastUnrecordedFloor() (int64, bool) {
	raw := s.GetSetting(setForecastUnrecordedFloorID, "")
	if raw == "" {
		return 0, false
	}
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, false
	}
	return id, true
}

// storeUnrecordedFloor derives the floor from the ledger start and writes the three keys on the given
// executor. Used by the save (tx), by finishRestore (tx) and by the boot ensure (pool).
func storeUnrecordedFloor(e migExec, from time.Time) error {
	floorID, method, err := captureUnrecordedFloor(e, from)
	if err != nil {
		return err
	}
	for k, v := range map[string]string{
		setForecastUnrecordedFloorID:     strconv.FormatInt(floorID, 10),
		setForecastUnrecordedFloorAt:     from.UTC().Format(time.RFC3339),
		setForecastUnrecordedFloorMethod: method,
	} {
		if err := e.setSetting(k, v); err != nil {
			return err
		}
	}
	return nil
}

// EnsureForecastUnrecordedFloor runs at boot: if a ledger start exists but the floor keys do not (a
// database whose boundary was set before P2 shipped, or a restore that could not read them), derive
// and persist them once through a short transaction. Idempotent.
func (s *Store) EnsureForecastUnrecordedFloor() error {
	from, ok := s.ForecastPopulationFrom()
	if !ok {
		return nil
	}
	if _, has := s.forecastUnrecordedFloor(); has {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // no-op once committed
	if err := storeUnrecordedFloor(migExec{s: s, ex: tx}, from); err != nil {
		return err
	}
	return tx.Commit()
}

// firstCohortMature reports whether any official cell of the ledger's first covered cohort is already
// mature. It reuses the real statistics engine over the first month, so "mature" means exactly what it
// means on the record.
func (s *Store) firstCohortMature(now time.Time) (bool, error) {
	earliest, err := s.forecastEarliestMadeOn(predTestRtype)
	if err != nil || earliest == "" {
		return false, err
	}
	first, err := time.ParseInLocation("2006-01-02", earliest, chinaZone)
	if err != nil {
		return false, nil
	}
	start := time.Date(first.Year(), first.Month(), 1, 0, 0, 0, 0, chinaZone)
	end := start.AddDate(0, 1, -1)
	from, to := start.Format("2006-01-02"), end.Format("2006-01-02")
	sets, err := s.loadForecastGroupSets(predTestRtype, from, to, forecastPopulation{Key: "all"})
	if err != nil {
		return false, err
	}
	today := now.In(chinaZone).Format("2006-01-02")
	// Pre-live does not apply to this check: it asks whether the cohort would publish if scored.
	members, pending, _ := forecastMembership(sets, time.Time{}, cohortBenchFromSets(sets))
	for _, cp := range predCheckpoints {
		if computeForecastCell(members, pending, predTestRtype, from, to, cp, today).State == "mature" {
			return true, nil
		}
	}
	return false, nil
}
