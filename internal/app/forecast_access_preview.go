package app

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"time"
)

type forecastScopeCounts struct {
	All  int `json:"all"`
	Own  int `json:"own"`
	None int `json:"none"`
}

func (c *forecastScopeCounts) add(scope string) {
	switch scope {
	case scopeAll:
		c.All++
	case scopeOwn:
		c.Own++
	default:
		c.None++
	}
}

type forecastOUAccess struct {
	ID        int64               `json:"id"`
	Scope     *string             `json:"forecast_scope"`
	Effective string              `json:"forecast_scope_effective"`
	Source    int64               `json:"forecast_scope_source"`
	External  bool                `json:"external"`
	Counts    forecastScopeCounts `json:"counts"`
}

type forecastAccessOverview struct {
	OK       bool                `json:"ok"`
	Preview  bool                `json:"preview"`
	Affected int                 `json:"affected"`
	Counts   forecastScopeCounts `json:"counts"`
	Groups   []forecastOUAccess  `json:"groups"`
}

// Account counts and the tree must describe one snapshot, not a mixture during an SSO move.
// Account names exist only as resolver inputs; the admin response contains counts, not identities.
func (s *Store) loadForecastAccessOverview() ([]forecastSubject, ouIndex, error) {
	opts := &sql.TxOptions{ReadOnly: true}
	if s.driver == "postgres" {
		opts.Isolation = sql.LevelRepeatableRead
	}
	tx, err := s.db.BeginTx(context.Background(), opts)
	if err != nil {
		return nil, ouIndex{}, err
	}
	defer tx.Rollback()
	var timezone string
	err = tx.QueryRow("SELECT v FROM meta WHERE k='timezone'").Scan(&timezone)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, ouIndex{}, err
	}
	loc := time.Local
	if configured, err := time.LoadLocation(timezone); timezone != "" && err == nil {
		loc = configured
	}
	// Like accountExpired, a civil-date cutoff remains valid through that entire panel day.
	today := time.Now().In(loc).Format("2006-01-02")
	rows, err := tx.Query(s.bind(`SELECT username,COALESCE(role,'user'),COALESCE(restricted,0),COALESCE(group_id,0) FROM users WHERE COALESCE(active,1)=1 AND (COALESCE(expires_at,'')='' OR expires_at>=?)`), today)
	if err != nil {
		return nil, ouIndex{}, err
	}
	var subjects []forecastSubject
	for rows.Next() {
		var sub forecastSubject
		var role string
		var restricted int
		if err := rows.Scan(&sub.User, &role, &restricted, &sub.GroupID); err != nil {
			rows.Close()
			return nil, ouIndex{}, err
		}
		sub.Admin = can(role, PermManage)
		sub.RestrictedAccount = restricted != 0
		subjects = append(subjects, sub)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, ouIndex{}, err
	}
	ix, err := s.loadOUIndexFrom(tx)
	if err != nil {
		return nil, ouIndex{}, err
	}
	if err := tx.Commit(); err != nil {
		return nil, ouIndex{}, err
	}
	return subjects, ix, nil
}

func forecastOverview(subjects []forecastSubject, ix ouIndex) forecastAccessOverview {
	out := forecastAccessOverview{OK: true, Groups: make([]forecastOUAccess, 0, len(ix.ous))}
	counts := map[int64]forecastScopeCounts{}
	for _, sub := range subjects {
		d := resolveForecastScope(sub, ix)
		out.Counts.add(d.Scope)
		id := sub.GroupID
		if id == 0 {
			id = ix.defaultID
		}
		c := counts[id]
		c.add(d.Scope)
		counts[id] = c
	}
	ids := make([]int64, 0, len(ix.ous))
	for id := range ix.ous {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	for _, id := range ids {
		d := resolveForecastScope(forecastSubject{User: "preview", GroupID: id}, ix)
		out.Groups = append(out.Groups, forecastOUAccess{ID: id, Scope: ix.ous[id].scope, Effective: d.Scope, Source: d.Source, External: d.External, Counts: counts[id]})
	}
	return out
}

func (s *Server) apiForecastAccess(w http.ResponseWriter, r *http.Request, user string) {
	q := r.URL.Query()
	preview := q.Has("ou") || q.Has("scope")
	var id int64
	var raw string
	if preview {
		var err error
		id, err = strconv.ParseInt(q.Get("ou"), 10, 64)
		raw = q.Get("scope")
		if err != nil || id <= 0 || len(q["ou"]) != 1 || len(q["scope"]) != 1 || (raw != scopeAll && raw != scopeOwn && raw != scopeNone && raw != "inherit") {
			jsonErrorCode(w, 400, "forecast_bad_scope", "invalid forecast scope preview")
			return
		}
	}
	subjects, ix, err := s.st.loadForecastAccessOverview()
	if err != nil {
		jsonErrorCode(w, 500, "db_error", "database error")
		return
	}
	if !preview {
		writeJSON(w, forecastOverview(subjects, ix))
		return
	}
	row, ok := ix.ous[id]
	if !ok {
		jsonErrorCode(w, 400, "forecast_bad_scope", "unknown forecast scope OU")
		return
	}
	before := make([]forecastDecision, len(subjects))
	for i, sub := range subjects {
		before[i] = resolveForecastScope(sub, ix)
	}
	row.scope = nil
	if raw != "inherit" {
		row.scope = &raw
	} else if id == ix.defaultID {
		v := scopeNone
		row.scope = &v
	}
	ix.ous[id] = row
	out := forecastOverview(subjects, ix)
	out.Preview = true
	for i, sub := range subjects {
		a, b := before[i], resolveForecastScope(sub, ix)
		// Source-only changes do not alter what an account can read.
		if a.Scope != b.Scope || a.External != b.External {
			out.Affected++
		}
	}
	writeJSON(w, out)
}
