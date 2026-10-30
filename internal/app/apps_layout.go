package app

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"sync/atomic"
)

// App ordering and per-OU app access (ADR 0048).
//
// An app is addressed by its key: "builtin:<slug>" for a compiled-in app, the installed app's id
// otherwise. That is the same convention the entry-button pin uses, and it cannot collide because an
// installed id never contains a colon (appIDRe).

const builtinAppPrefix = "builtin:"

// builtinAppKeys is the compiled-in apps, in the order the hub has always listed them. The SPA owns
// their names, routes and permissions (web/src/lib/builtinApps.ts); the server only needs the keys,
// to validate an ordering or an access change and to report which are open.
var builtinAppKeys = []string{"batch", "recurring", "quotes"}

// appLayoutKeys is every key an ordering or an access row may name: the built-in apps, then the
// installed ones in the order ListApps returns them. That sequence is "today's order", which is where
// an app with no position row goes.
func appLayoutKeys(installed []App) []string {
	keys := make([]string, 0, len(builtinAppKeys)+len(installed))
	for _, k := range builtinAppKeys {
		keys = append(keys, builtinAppPrefix+k)
	}
	for _, a := range installed {
		keys = append(keys, a.ID)
	}
	return keys
}

// orderAppKeys sorts keys by their stored position, ascending. Keys without a row follow, in the order
// they were given. A position tie (never written by the API, possible in a hand-edited database) falls
// back to that same given order, so the result is deterministic.
func orderAppKeys(keys []string, order map[string]int) []string {
	ranked, rest := []string{}, []string{}
	for _, k := range keys {
		if _, ok := order[k]; ok {
			ranked = append(ranked, k)
		} else {
			rest = append(rest, k)
		}
	}
	slices.SortStableFunc(ranked, func(a, b string) int { return order[a] - order[b] })
	return append(ranked, rest...)
}

// ---------- store ----------

// appOrderMap reads app_order as key -> position.
func (s *Store) appOrderMap() (map[string]int, error) {
	rows, err := s.query(`SELECT app_key,position FROM app_order`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var k string
		var p int
		if err := rows.Scan(&k, &p); err != nil {
			return nil, err
		}
		out[k] = p
	}
	return out, rows.Err()
}

// ReplaceAppOrder makes keys the whole ordering, positions 1..n. Keys the caller leaves out lose their
// row and fall back to the end of the list in today's order.
func (s *Store) ReplaceAppOrder(keys []string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // a no-op once committed
	e := migExec{s: s, ex: tx}
	// Validated here, inside the write, so an uninstall that lands after the handler's check cannot
	// leave a position row behind for an app that is gone.
	for _, k := range appLockOrder(keys) {
		if err := s.requireApp(e, k); err != nil {
			return err
		}
	}
	if _, err := e.exec(`DELETE FROM app_order`); err != nil {
		return err
	}
	for i, k := range keys {
		if _, err := e.exec(`INSERT INTO app_order(app_key,position) VALUES(?,?)`, k, i+1); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// errUnknownApp is a key that names neither a built-in app nor an installed one.
var errUnknownApp = errors.New("unknown app")

// requireApp checks, inside the caller's transaction, that key names a built-in app or an installed
// one. On Postgres the installed row is locked FOR UPDATE, which is the lock DeleteApp takes first, so
// a concurrent uninstall either finishes before this reads (and the key is refused) or waits for this
// transaction (and then deletes the rows it wrote). SQLite has one writer, so it serializes anyway.
func (s *Store) requireApp(e migExec, key string) error {
	if slug, ok := strings.CutPrefix(key, builtinAppPrefix); ok {
		if slices.Contains(builtinAppKeys, slug) {
			return nil
		}
		return errUnknownApp
	}
	q := `SELECT id FROM apps WHERE id=?`
	if s.driver == "postgres" {
		q += ` FOR UPDATE`
	}
	var id string
	switch err := e.queryRow(q, key).Scan(&id); {
	case err == nil:
		return nil
	case errors.Is(err, sql.ErrNoRows):
		return errUnknownApp
	default:
		return err
	}
}

// setAppAccess is setAppAccessRow with the app checked in the same transaction. Clearing a row never
// needs the app to exist: it is cleanup, and refusing it would strand the row.
func (s *Store) setAppAccess(e migExec, key string, ou int64, enabled *bool) error {
	if enabled != nil {
		if err := s.requireApp(e, key); err != nil {
			return err
		}
	}
	return s.setAppAccessRow(e, key, ou, enabled)
}

// appAccessRows reads app_ou_access as key -> OU -> enabled.
func (s *Store) appAccessRows() (map[string]map[int64]bool, error) {
	rows, err := s.query(`SELECT app_key,group_id,enabled FROM app_ou_access`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]map[int64]bool{}
	for rows.Next() {
		var k string
		var ou int64
		var en int
		if err := rows.Scan(&k, &ou, &en); err != nil {
			return nil, err
		}
		if out[k] == nil {
			out[k] = map[int64]bool{}
		}
		out[k][ou] = en != 0
	}
	return out, rows.Err()
}

// setAppAccessRow writes one tri-state override: nil deletes the row (inherit), otherwise the row is
// upserted. The caller holds whatever lock its consistency needs (the handler runs it inside
// withOUMutation).
func (s *Store) setAppAccessRow(e migExec, key string, ou int64, enabled *bool) error {
	if enabled == nil {
		_, err := e.exec(`DELETE FROM app_ou_access WHERE app_key=? AND group_id=?`, key, ou)
		return err
	}
	if _, err := e.exec(`DELETE FROM app_ou_access WHERE app_key=? AND group_id=?`, key, ou); err != nil {
		return err
	}
	_, err := e.exec(`INSERT INTO app_ou_access(app_key,group_id,enabled) VALUES(?,?,?)`, key, ou, boolInt(*enabled))
	return err
}

// ---------- resolution ----------

// appAccessView answers "may this user open this app" from one OU snapshot and one read of the rows.
type appAccessView struct {
	// admin is exempt: an administrator sees and opens every app, which is how they manage them.
	admin bool
	// deny closes everything, for an account that is unknown or deactivated.
	deny  bool
	chain []int64
	rows  map[string]map[int64]bool
}

// resolveAppAccess is the nearest explicit row up the chain, open when there is none. It is the same
// resolver every other OU policy uses, with the baseline "open".
func resolveAppAccess(chain []int64, rows map[int64]bool) ouPolicyResult[bool] {
	return nearestOUPolicy(chain, true, func(id int64) *bool {
		if v, ok := rows[id]; ok {
			return &v
		}
		return nil
	})
}

func (v appAccessView) open(key string) bool {
	if v.deny {
		return false
	}
	if v.admin {
		return true
	}
	return resolveAppAccess(v.chain, v.rows[key]).Effective
}

// appAccessFor builds the view for a signed-in user.
func (s *Store) appAccessFor(user string) (appAccessView, error) {
	sub, snap, err := s.loadOUPolicySubject(user)
	if err != nil {
		return appAccessView{deny: true}, err
	}
	if !sub.Active {
		return appAccessView{deny: true}, nil
	}
	if can(sub.Role, PermManage) {
		return appAccessView{admin: true}, nil
	}
	chain, err := snap.graph.policyChain(sub.EffectiveOU)
	if err != nil {
		return appAccessView{deny: true}, err
	}
	rows, err := s.appAccessRows()
	if err != nil {
		return appAccessView{deny: true}, err
	}
	return appAccessView{chain: chain, rows: rows}, nil
}

// ---------- reader-facing ----------

// appAccessSnapshotLoads counts OU snapshot loads made by the single-app check, so a test can show
// that an app with no access rows costs none.
var appAccessSnapshotLoads atomic.Int64

// appOpenForKey answers "may user open this one app" as cheaply as the answer allows. A user who no
// longer exists or is deactivated is definitely closed (false, nil). An administrator is exempt, known
// from the user row alone. An app with no access rows at all is open for everyone, which one indexed
// lookup on the primary key's prefix settles. Only an app with rows pays for the OU snapshot. A store
// failure is the one error return.
func (s *Store) appOpenForKey(user, key string) (bool, error) {
	var role string
	var active int
	switch err := s.queryRow(`SELECT COALESCE(role,'user'),COALESCE(active,1) FROM users WHERE username=?`, user).Scan(&role, &active); {
	case errors.Is(err, sql.ErrNoRows):
		return false, nil
	case err != nil:
		return false, err
	}
	if active == 0 {
		return false, nil
	}
	if can(role, PermManage) {
		return true, nil
	}
	var one int
	switch err := s.queryRow(`SELECT 1 FROM app_ou_access WHERE app_key=? LIMIT 1`, key).Scan(&one); {
	case errors.Is(err, sql.ErrNoRows):
		return true, nil
	case err != nil:
		return false, err
	}
	appAccessSnapshotLoads.Add(1)
	sub, snap, err := s.loadOUPolicySubject(user)
	if err != nil {
		return false, err
	}
	chain, err := snap.graph.policyChain(sub.EffectiveOU)
	if err != nil {
		return false, err
	}
	rows, err := s.appAccessRowsFor(key)
	if err != nil {
		return false, err
	}
	return resolveAppAccess(chain, rows).Effective, nil
}

// appAccessRowsFor reads one app's rows as OU -> enabled.
func (s *Store) appAccessRowsFor(key string) (map[int64]bool, error) {
	rows, err := s.query(`SELECT group_id,enabled FROM app_ou_access WHERE app_key=?`, key)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]bool{}
	for rows.Next() {
		var ou int64
		var en int
		if err := rows.Scan(&ou, &en); err != nil {
			return nil, err
		}
		out[ou] = en != 0
	}
	return out, rows.Err()
}

// appOpenQuiet is the access check for a caller that has no response to write: it fails closed.
func (s *Store) appOpenQuiet(user, key string) bool {
	open, err := s.appOpenForKey(user, key)
	return err == nil && open
}

// appClosedFor is true only on a definite answer: the app is closed for the user, or the user is
// gone. The bridge credential check uses it so an unreadable policy leaves that check as it was (the
// handler behind it needs the same store and fails on its own), where the extension proxy, which
// forwards to a backend, uses appOpenQuiet and fails closed.
func (s *Store) appClosedFor(user, key string) bool {
	open, err := s.appOpenForKey(user, key)
	return err == nil && !open
}

// txAppRows reads one app's rows inside the caller's transaction.
func txAppRows(e migExec, key string) (map[int64]bool, error) {
	rows, err := e.query(`SELECT group_id,enabled FROM app_ou_access WHERE app_key=?`, key)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]bool{}
	for rows.Next() {
		var ou int64
		var en int
		if err := rows.Scan(&ou, &en); err != nil {
			return nil, err
		}
		out[ou] = en != 0
	}
	return out, rows.Err()
}

// appLockOrder is the order in which a write locks several apps' rows: sorted and de-duplicated, so
// two concurrent writes that name the same apps in different orders cannot deadlock on Postgres.
func appLockOrder(keys []string) []string {
	out := slices.Clone(keys)
	slices.Sort(out)
	return slices.Compact(out)
}

// appClosesSomewhere reports whether a change to one app's rows turns the app from open to closed for
// at least one OU, which is the only kind of change that can invalidate a token already minted.
func appClosesSomewhere(snap ouPolicySnapshot, before, after map[int64]bool) bool {
	for id := range snap.graph.nodes {
		chain, err := snap.graph.policyChain(id)
		if err != nil {
			continue
		}
		if resolveAppAccess(chain, before).Effective && !resolveAppAccess(chain, after).Effective {
			return true
		}
	}
	return false
}

// closeApp answers a closed app. 403 with a stable code: the SPA tells "this app is switched off for
// your organization" from a permission failure by the code, not by the wording.
func closeApp(w http.ResponseWriter) {
	jsonErrorCode(w, http.StatusForbidden, "app_closed", "app_closed")
}

// appOpenFor reports whether the app is open for user. On a lookup failure it has already answered
// the request and returns done=true: access is a gate, so an unreadable policy refuses rather than
// admits.
func (s *Server) appOpenFor(w http.ResponseWriter, user, key string) (open, done bool) {
	open, err := s.st.appOpenForKey(user, key)
	if err != nil {
		jsonError(w, http.StatusServiceUnavailable, "app access unavailable")
		return false, true
	}
	if !open {
		closeApp(w)
		return false, true
	}
	return true, false
}

// ---------- admin console ----------

type appAccessOU struct {
	OU        int64 `json:"ou"`
	Own       *bool `json:"own"`
	Effective bool  `json:"effective"`
	SourceOU  int64 `json:"source_ou"`
	Inherited bool  `json:"inherited"`
}

type appLayoutEntry struct {
	Key     string        `json:"key"`
	Name    string        `json:"name"`
	Builtin bool          `json:"builtin"`
	Icon    string        `json:"icon,omitempty"`
	Access  []appAccessOU `json:"access"`
}

// appLayout is the administrator's whole picture: every app in order, and for each, every OU's own
// value, effective value and the OU the effective value comes from.
func (s *Server) appLayout() ([]appLayoutEntry, error) {
	snap, err := s.st.loadOUPolicySnapshotFrom(s.st.db)
	if err != nil {
		return nil, err
	}
	order, err := s.st.appOrderMap()
	if err != nil {
		return nil, err
	}
	rows, err := s.st.appAccessRows()
	if err != nil {
		return nil, err
	}
	installed := s.st.ListApps()
	byID := map[string]App{}
	for _, a := range installed {
		byID[a.ID] = a
	}
	ous := make([]int64, 0, len(snap.graph.nodes))
	for id := range snap.graph.nodes {
		ous = append(ous, id)
	}
	slices.Sort(ous)
	chains := map[int64][]int64{}
	for _, id := range ous {
		if c, err := snap.graph.policyChain(id); err == nil {
			chains[id] = c
		}
	}
	out := []appLayoutEntry{}
	for _, key := range orderAppKeys(appLayoutKeys(installed), order) {
		e := appLayoutEntry{Key: key, Access: []appAccessOU{}}
		if slug, ok := strings.CutPrefix(key, builtinAppPrefix); ok {
			e.Name, e.Builtin = slug, true
		} else {
			e.Name, e.Icon = byID[key].Name, byID[key].Icon
		}
		for _, id := range ous {
			chain, ok := chains[id]
			if !ok {
				continue
			}
			r := resolveAppAccess(chain, rows[key])
			e.Access = append(e.Access, appAccessOU{OU: id, Own: r.Own, Effective: r.Effective, SourceOU: r.SourceOU, Inherited: r.Inherited})
		}
		out = append(out, e)
	}
	return out, nil
}

func (s *Server) apiAdminAppsLayout(w http.ResponseWriter, _ *http.Request, _ string) {
	apps, err := s.appLayout()
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "database error")
		return
	}
	writeJSON(w, map[string]any{"apps": apps})
}

// apiAdminAppsOrder replaces the whole ordering. Every key must be a known app; a duplicate is refused
// rather than collapsed, so a client that sends a malformed list learns it.
func (s *Server) apiAdminAppsOrder(w http.ResponseWriter, r *http.Request, user string) {
	var in struct {
		Keys []string `json:"keys"`
	}
	if err := readJSON(r, &in); err != nil || in.Keys == nil {
		jsonError(w, http.StatusBadRequest, "keys must be a list of app keys")
		return
	}
	known := appLayoutKeys(s.st.ListApps())
	seen := map[string]bool{}
	for _, k := range in.Keys {
		if !slices.Contains(known, k) {
			jsonError(w, http.StatusBadRequest, "unknown app key: "+k)
			return
		}
		if seen[k] {
			jsonError(w, http.StatusBadRequest, "duplicate app key: "+k)
			return
		}
		seen[k] = true
	}
	order, err := s.st.appOrderMap()
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "database error")
		return
	}
	if err := s.st.ReplaceAppOrder(in.Keys); err != nil {
		if errors.Is(err, errUnknownApp) {
			jsonError(w, http.StatusBadRequest, "unknown app key")
			return
		}
		jsonError(w, http.StatusInternalServerError, "database error")
		return
	}
	// Audited on a change to the order readers see, not on a change to the stored rows: writing the
	// list that is already in effect is not an event.
	before := orderAppKeys(known, order)
	next := make(map[string]int, len(in.Keys))
	for i, k := range in.Keys {
		next[k] = i + 1
	}
	if after := orderAppKeys(known, next); !slices.Equal(before, after) {
		s.recordChange(r, user, AuditAppOrder, "app", "", map[string]any{"field": "order", "from": before, "to": after})
	}
	apps, err := s.appLayout()
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "database error")
		return
	}
	writeJSON(w, map[string]any{"apps": apps})
}

// apiAdminAppAccess sets one OU's override for one app: true opens it, false closes it, null removes
// the override so the OU inherits again. Removing the Default OU's override is allowed and means open,
// the baseline.
func (s *Server) apiAdminAppAccess(w http.ResponseWriter, r *http.Request, user string) {
	key := r.PathValue("key")
	ou := pathID(r, "ou")
	var in struct {
		Enabled json.RawMessage `json:"enabled"`
	}
	if err := readJSON(r, &in); err != nil || len(in.Enabled) == 0 {
		jsonError(w, http.StatusBadRequest, "enabled must be true, false or null")
		return
	}
	var enabled *bool
	if !isJSONNull(in.Enabled) {
		var b bool
		if json.Unmarshal(in.Enabled, &b) != nil {
			jsonError(w, http.StatusBadRequest, "enabled must be true, false or null")
			return
		}
		enabled = &b
	}
	if !slices.Contains(appLayoutKeys(s.st.ListApps()), key) {
		jsonError(w, http.StatusNotFound, "app not found")
		return
	}
	var from *bool
	notFound, closes := false, false
	err := s.st.withOUMutation(func(e migExec) error {
		from, notFound, closes = nil, false, false
		snap, err := s.st.loadOUPolicySnapshotFrom(e.ex)
		if err != nil {
			return err
		}
		if _, ok := snap.graph.nodes[ou]; !ok {
			notFound = true
			return nil
		}
		var cur int
		switch err := e.queryRow(`SELECT enabled FROM app_ou_access WHERE app_key=? AND group_id=?`, key, ou).Scan(&cur); {
		case err == nil:
			b := cur != 0
			from = &b
		case !errors.Is(err, sql.ErrNoRows):
			return err
		}
		before, err := txAppRows(e, key)
		if err != nil {
			return err
		}
		if err := s.st.setAppAccess(e, key, ou, enabled); err != nil {
			return err
		}
		after, err := txAppRows(e, key)
		if err != nil {
			return err
		}
		closes = appClosesSomewhere(snap, before, after)
		return nil
	})
	switch {
	case errors.Is(err, errUnknownApp):
		jsonError(w, http.StatusNotFound, "app not found")
		return
	case err != nil:
		jsonError(w, http.StatusInternalServerError, "database error")
		return
	case notFound:
		jsonError(w, http.StatusNotFound, "group not found")
		return
	}
	// Tokens minted under the old answer are revoked only when this change closes the app somewhere it
	// was open. An opening or a no-op leaves them alone: revoking would force every open frame to
	// re-mint for nothing, and an extension frame to fail.
	if closes {
		s.appTok.revokeApp(key)
	}
	if (from == nil) != (enabled == nil) || (from != nil && *from != *enabled) {
		s.recordChange(r, user, AuditAppAccess, "app", key, map[string]any{"field": "access", "ou": ou, "from": from, "to": enabled})
	}
	apps, err := s.appLayout()
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "database error")
		return
	}
	for _, a := range apps {
		if a.Key == key {
			writeJSON(w, a)
			return
		}
	}
}
