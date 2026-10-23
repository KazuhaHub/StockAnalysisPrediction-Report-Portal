package app

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
)

// Forecast access is resolved from one account/tree snapshot, never from lenient tenancy helpers.
// The pure rule is shared by request gates, navigation hints and hypothetical admin previews.
type forecastSubject struct {
	User              string
	Admin             bool
	RestrictedAccount bool
	GroupID           int64
	SessionRev        int64
	CreatedAt         string
	ExpiresAt         string
}

type forecastDecision struct {
	Scope    string
	External bool
	Leaf     int64
	Subtree  []int64
	Source   int64
	Reason   string
}

func (s *Server) loadForecastSubject(user string) (forecastSubject, ouIndex, error) {
	return s.loadForecastSubjectSnapshot(user, nil)
}

// afterAccountRead is a test-only interleaving seam; request paths always pass nil.
func (s *Server) loadForecastSubjectSnapshot(user string, afterAccountRead func()) (forecastSubject, ouIndex, error) {
	opts := &sql.TxOptions{ReadOnly: true}
	if s.st.driver == "postgres" {
		opts.Isolation = sql.LevelRepeatableRead
	}
	tx, err := s.st.db.BeginTx(context.Background(), opts)
	if err != nil {
		return forecastSubject{}, ouIndex{}, err
	}
	defer tx.Rollback()
	var role string
	var restricted, active int
	var group int64
	var revision int64
	var created, expires string
	err = tx.QueryRow(s.st.bind(`SELECT COALESCE(role,'user'), COALESCE(restricted,0),
		COALESCE(group_id,0), COALESCE(active,1), COALESCE(session_rev,0),
		COALESCE(created_at,''), COALESCE(expires_at,'') FROM users WHERE username=?`), user).
		Scan(&role, &restricted, &group, &active, &revision, &created, &expires)
	if err != nil {
		return forecastSubject{}, ouIndex{}, err
	}
	if active == 0 {
		return forecastSubject{}, ouIndex{}, fmt.Errorf("forecast subject is inactive")
	}
	if afterAccountRead != nil {
		afterAccountRead()
	}
	ix, err := s.st.loadOUIndexFrom(tx)
	if err != nil {
		return forecastSubject{}, ouIndex{}, err
	}
	if err := tx.Commit(); err != nil {
		return forecastSubject{}, ouIndex{}, err
	}
	return forecastSubject{User: user, Admin: can(role, PermManage), RestrictedAccount: restricted != 0,
		GroupID: group, SessionRev: revision, CreatedAt: created, ExpiresAt: expires}, ix, nil
}

func resolveForecastScope(sub forecastSubject, ix ouIndex) forecastDecision {
	deny := func(reason string) forecastDecision { return forecastDecision{Scope: scopeNone, Reason: reason} }
	if sub.User == "" {
		return deny("anonymous")
	}
	if sub.Admin {
		return forecastDecision{Scope: scopeAll, Reason: "admin"}
	}
	leaf := sub.GroupID
	if leaf == 0 {
		leaf = ix.defaultID
	}
	if _, ok := ix.ous[leaf]; !ok {
		return deny("dangling_group")
	}
	chain, err := ix.chain(leaf)
	if err != nil {
		if errors.Is(err, errCyclicOU) {
			return deny("cyclic_chain")
		}
		return deny("dangling_group")
	}
	d := forecastDecision{Scope: scopeNone, External: sub.RestrictedAccount, Leaf: leaf, Reason: "default_null"}
	for _, id := range chain {
		row, ok := ix.ous[id]
		if !ok {
			return deny("dangling_group")
		}
		d.External = d.External || row.restricted
	}
	for _, id := range chain {
		v := ix.ous[id].scope
		if v == nil {
			continue
		}
		switch *v {
		case scopeAll, scopeOwn, scopeNone:
			d.Scope, d.Source, d.Reason = *v, id, "ou"
			if d.Scope == scopeOwn {
				d.Subtree = ix.subtree(leaf)
			}
			return d
		default:
			return deny("unknown_value")
		}
	}
	return d
}

func (s *Server) forecastAccess(user string) forecastDecision {
	if user == "" {
		return forecastDecision{Scope: scopeNone, Reason: "anonymous"}
	}
	sub, ix, err := s.loadForecastSubject(user)
	if err != nil {
		return forecastDecision{Scope: scopeNone, Reason: "error"}
	}
	return resolveForecastScope(sub, ix)
}

func (s *Server) requireForecastAccess(w http.ResponseWriter, r *http.Request, user string) (forecastDecision, bool) {
	d := s.forecastAccess(user)
	caps := forecastCapabilitiesFor(d)
	if !caps.Monthly {
		jsonErrorCode(w, http.StatusForbidden, "forecast_unavailable", "forecast access is unavailable")
		return d, false
	}
	if !caps.Custom && r.URL.Query().Get("mode") == "custom" {
		jsonErrorCode(w, http.StatusForbidden, "forecast_custom_not_allowed", "custom ranges are unavailable to external readers")
		return d, false
	}
	return d, true
}

func forecastRateError(w http.ResponseWriter) {
	w.Header().Set("Retry-After", "60")
	jsonErrorCode(w, http.StatusTooManyRequests, "forecast_rate_limited", "forecast request limit exceeded; retry later")
}

// A nil OU slice is the whole ledger; a non-nil empty slice must remain an empty population.
type forecastPopulation struct {
	Key string
	OUs []int64
}

func populationFor(d forecastDecision) forecastPopulation {
	if d.Scope == scopeAll {
		return forecastPopulation{Key: "all"}
	}
	ids := make([]int64, 0, len(d.Subtree))
	if d.Scope == scopeOwn {
		ids = append(ids, d.Subtree...)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = strconv.FormatInt(id, 10)
	}
	sum := sha256.Sum256([]byte(strings.Join(parts, ",")))
	return forecastPopulation{Key: "ou:" + hex.EncodeToString(sum[:]), OUs: ids}
}

// forecastPopulationRoot is the display name of the OU an own reader's population is rooted at: the
// reader's own unit (the leaf), not the ancestor that decided the scope, because the statistics cover
// the leaf's subtree and naming a wider unit would claim a wider population than was scored. It is
// empty for every other scope, and when the name cannot be read: callers then say nothing rather
// than guess.
func (s *Server) forecastPopulationRoot(d forecastDecision) string {
	if d.Scope != scopeOwn || d.Leaf == 0 {
		return ""
	}
	var name string
	if err := s.st.queryRow("SELECT name FROM user_groups WHERE id=?", d.Leaf).Scan(&name); err != nil {
		return ""
	}
	return name
}

func (p forecastPopulation) cellKey(rtype, start, end, bench, cp string) string {
	key := forecastGroupCellKey(rtype, start, end, bench, cp)
	if p.Key == "all" {
		return key
	}
	return key + "\x00" + p.Key
}

func (p forecastPopulation) seedParts(rtype, start, end string) []string {
	parts := []string{rtype, start, end}
	if p.Key != "all" {
		parts = append(parts, p.Key)
	}
	return parts
}
