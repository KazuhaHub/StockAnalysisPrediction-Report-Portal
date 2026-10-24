package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"strings"
)

// Per-OU run allow-list — ADR 0022 R3. A restricted OU may run only the targets its allow-list
// grants, on only the surfaces that row permits. Internal (unrestricted) OUs ignore the table
// entirely, so nothing here changes behavior until a restricted OU exists.

// GroupTarget is one allow-list row: this OU may run TargetID, limited to Surfaces (a comma-
// separated subset of run|batch|recurring|chat; "" = every surface the target itself allows).
type GroupTarget struct {
	TargetID int64
	Surfaces string
}

// GroupTargets returns one OU's own allow-list rows (no inheritance — see resolveGroupTargets).
func (s *Store) GroupTargets(groupID int64) []GroupTarget {
	rows, err := s.query(`SELECT target_id, COALESCE(surfaces,'') FROM group_targets WHERE group_id=? ORDER BY target_id`, groupID)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []GroupTarget
	for rows.Next() {
		var g GroupTarget
		if rows.Scan(&g.TargetID, &g.Surfaces) == nil {
			out = append(out, g)
		}
	}
	return out
}

// SetGroupTargets writes an explicit replacement, including an empty deny-all collection.
func (s *Store) SetGroupTargets(groupID int64, rows []GroupTarget) error {
	return s.withOUMutation(func(e migExec) error {
		var present int
		if err := e.queryRow("SELECT 1 FROM user_groups WHERE id=?", groupID).Scan(&present); err != nil {
			return err
		}
		seen := map[int64]bool{}
		for _, row := range rows {
			if seen[row.TargetID] {
				return fmt.Errorf("duplicate target %d", row.TargetID)
			}
			seen[row.TargetID] = true
			if err := e.queryRow("SELECT 1 FROM batch_targets WHERE id=?", row.TargetID).Scan(&present); err != nil {
				return err
			}
			if row.Surfaces != "" {
				for _, surface := range strings.Split(row.Surfaces, ",") {
					switch strings.TrimSpace(surface) {
					case SurfaceRun, SurfaceBatch, SurfaceRecurring, SurfaceChat:
					default:
						return fmt.Errorf("invalid target surface")
					}
				}
			}
		}
		if _, err := e.exec("INSERT INTO group_target_policies(group_id) VALUES(?) ON CONFLICT(group_id) DO NOTHING", groupID); err != nil {
			return err
		}
		if _, err := e.exec("DELETE FROM group_targets WHERE group_id=?", groupID); err != nil {
			return err
		}
		for _, row := range rows {
			if _, err := e.exec("INSERT INTO group_targets(group_id,target_id,surfaces) VALUES(?,?,?)", groupID, row.TargetID, strings.TrimSpace(row.Surfaces)); err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *Store) InheritGroupTargets(groupID int64) error {
	return s.withOUMutation(func(e migExec) error {
		var present int
		if err := e.queryRow("SELECT 1 FROM user_groups WHERE id=?", groupID).Scan(&present); err != nil {
			return err
		}
		if _, err := e.exec("DELETE FROM group_targets WHERE group_id=?", groupID); err != nil {
			return err
		}
		_, err := e.exec("DELETE FROM group_target_policies WHERE group_id=?", groupID)
		return err
	})
}

// loadOUTargetPoliciesFrom decodes both new presence headers and old nonempty collections.
// A query failure never becomes an unset collection that would inherit a broader parent grant.
func loadOUTargetPoliciesFrom(ex execer) (map[int64][]GroupTarget, error) {
	rows, err := ex.Query(`SELECT group_id,NULL AS target_id,NULL AS surfaces FROM group_target_policies
  UNION ALL SELECT group_id,target_id,COALESCE(surfaces,'') FROM group_targets ORDER BY group_id,target_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64][]GroupTarget{}
	for rows.Next() {
		var id int64
		var target sql.NullInt64
		var surface sql.NullString
		if err := rows.Scan(&id, &target, &surface); err != nil {
			return nil, err
		}
		if _, ok := out[id]; !ok {
			out[id] = make([]GroupTarget, 0)
		}
		if target.Valid {
			out[id] = append(out[id], GroupTarget{TargetID: target.Int64, Surfaces: surface.String})
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func resolveOUTargetPolicy(chain []int64, values map[int64][]GroupTarget) ouPolicyResult[[]GroupTarget] {
	return nearestOUPolicy(chain, make([]GroupTarget, 0), func(id int64) *[]GroupTarget {
		rows, configured := values[id]
		if !configured {
			return nil
		}
		return &rows
	})
}

func (s *Store) resolveGroupTargetsErr(username string) ([]GroupTarget, error) {
	opts := &sql.TxOptions{ReadOnly: true}
	if s.driver == "postgres" {
		opts.Isolation = sql.LevelRepeatableRead
	}
	tx, err := s.db.BeginTx(context.Background(), opts)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	sub, snapshot, err := s.loadOUPolicySubjectFrom(tx, username)
	if err != nil {
		return nil, err
	}
	chain, err := snapshot.graph.policyChain(sub.EffectiveOU)
	if err != nil {
		return nil, err
	}
	values, err := loadOUTargetPoliciesFrom(tx)
	if err != nil {
		return nil, err
	}
	result := resolveOUTargetPolicy(chain, values)
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return result.Effective, nil
}

// resolveGroupTargets is a deny-on-error compatibility adapter for boolean run gates.
func (s *Store) resolveGroupTargets(username string) []GroupTarget {
	rows, _ := s.resolveGroupTargetsErr(username)
	return rows
}

// runAllowed reports whether a user may run a target on a surface. Unrestricted users (internal
// staff, admins) always may — the allow-list is a restricted-OU concept. A restricted user is
// default-deny: the target must appear in the resolved allow-list, and the surface must be in both
// that row's subset and the target's own global surfaces.
func (s *Server) runAllowed(user string, targetID int64, surface string) bool {
	if s.viewerScope(user) == nil {
		return true
	}
	for _, g := range s.st.resolveGroupTargets(user) {
		if g.TargetID != targetID {
			continue
		}
		if g.Surfaces != "" && !AllowsSurface(g.Surfaces, surface) {
			return false
		}
		t, ok := s.st.GetTarget(targetID)
		return ok && AllowsSurface(t.Surfaces, surface)
	}
	return false
}

// targetGranted reports whether a target appears in a user's resolved allow-list at all,
// ignoring surfaces. It backs the actions that lead UP TO a run (uploading a file the run will
// reference) and happen before a surface is known; the surface stays enforced by runAllowed on
// submit, which is the authoritative gate.
func (s *Server) targetGranted(user string, targetID int64) bool {
	if s.viewerScope(user) == nil {
		return true
	}
	for _, g := range s.st.resolveGroupTargets(user) {
		if g.TargetID == targetID {
			return true
		}
	}
	return false
}

// targetOutputSubtype reads the report subtype a target produces. It lives in the target's existing
// config JSON rather than a new column (ADR 0022 D6), and feeds both the same-day reuse key and the
// restricted read filter, keeping "what you may run" and "what you may see today" consistent.
func targetOutputSubtype(config string) string {
	var m struct {
		OutputSubtype string `json:"output_subtype"`
	}
	json.Unmarshal([]byte(config), &m)
	return strings.TrimSpace(m.OutputSubtype)
}

// targetOutputVersion names the report version this target produces (ADR 0024). Empty means the
// default version, which is what every pre-version target means.
func targetOutputVersion(config string) string {
	var m struct {
		OutputVersion string `json:"output_version"`
	}
	json.Unmarshal([]byte(config), &m)
	return strings.TrimSpace(m.OutputVersion)
}

// targetSymbolInput names the input key that carries the stock code for this target. Input keys are
// target-defined (code / symbol / …), so the same-day reuse gate cannot guess: it is declared beside
// output_subtype in the target's config JSON. Guessing here would risk handing back a DIFFERENT
// company's report, so an undeclared target simply never reuses (see reuseSameDayReport).
func targetSymbolInput(config string) string {
	var m struct {
		SymbolInput string `json:"symbol_input"`
	}
	json.Unmarshal([]byte(config), &m)
	return strings.TrimSpace(m.SymbolInput)
}

// reuseSameDayReport implements R1's "already generated today → show it directly" rule: it reports
// an existing report to hand back instead of running, for a single-row submit by a scoped user.
//
// The lookup is by CONTENT — this symbol, this subtype, this version, today — and NOT by what the
// caller can already see. Scoping it to the viewer was the original mistake and it made the whole
// feature inert: under per-person visibility a first-time requester is by definition not on any
// report's viewer list, so the lookup found nothing and every request ran for real. Reuse is needed
// precisely for the person who has not asked before.
//
// Handing the report over discloses nothing new, because two conditions already hold: the caller
// passed the run allow-list for this target, so they could have generated the identical content
// themselves, and the version is granted to them, so they are entitled to read that form of it. What
// reuse saves is the second run, not a permission check. On a hit the caller JOINS the report's
// viewer list — which is why strict per-person isolation costs no duplicate generation.
func (s *Server) reuseSameDayReport(user string, targetID int64, rows []map[string]string) (int64, string, bool) {
	sc := s.viewerScope(user)
	// A deny-all scope cannot be proven entitled to anything: reuse would hand back another OU's
	// report and write a viewer row for a caller whose restriction could not even be read (P31).
	if sc == nil || sc.denyAll || len(rows) != 1 {
		return 0, "", false
	}
	t, ok := s.st.GetTarget(targetID)
	if !ok {
		return 0, "", false
	}
	subtype, symbolKey := targetOutputSubtype(t.Config), targetSymbolInput(t.Config)
	if subtype == "" || symbolKey == "" {
		return 0, "", false
	}
	symbol := strings.TrimSpace(rows[0][symbolKey])
	if symbol == "" {
		return 0, "", false
	}
	version := s.st.resolveVersion(targetOutputVersion(t.Config))
	// A version this caller may not read must never be handed back: reuse skips the run, it does not
	// skip the grant. Without this, "reuse" would become a way to receive a form of a report the
	// caller was never entitled to.
	if !containsString(s.st.GrantedVersions(user), version) {
		return 0, "", false
	}
	today := s.panelToday()
	id, found := s.st.FindSameDayReport(symbol, subtype, version, today)
	if !found {
		return 0, "", false
	}
	if err := s.st.AddReportViewer(id, today, user, s.st.OwnerGroupOf(user)); err != nil {
		log.Printf("reuse: could not record %s as a viewer of report %d: %v", user, id, err)
		return 0, "", false // fail closed: handing back a report they cannot then read is worse than running
	}
	// The report was matched on symbol+rdate, so its group key is exactly this pair (see gkey).
	return id, symbol + "|" + today, true
}

func containsString(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}
