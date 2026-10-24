package app

import (
	"context"
	"database/sql"
	"maps"
	"net/http"
	"slices"
	"strings"
)

func ouValue[T any](own *T, baseline T) T {
	if own != nil {
		return *own
	}
	return baseline
}

func (snapshot ouPolicySnapshot) inheritedPolicies(id int64) (ouResolvedPolicies, error) {
	copy := snapshot
	copy.values = maps.Clone(snapshot.values)
	copy.values[id] = ouPolicyValues{}
	return copy.resolve(id)
}

func (snapshot ouPolicySnapshot) groupRows(members map[int64]int) []UserGroup {
	out := make([]UserGroup, 0, len(snapshot.graph.nodes))
	for id, node := range snapshot.graph.nodes {
		v := snapshot.values[id]
		g := UserGroup{ID: id, Name: node.name, Description: node.description, Created: node.created,
			ParentID: node.parent, IsDefault: node.isDefault, Members: members[id],
			Weight: ouValue(v.weight, 0), WeightInherit: v.weight == nil && !node.isDefault,
			UrgentFree: ouValue(v.urgent, false), UrgentInherit: v.urgent == nil && !node.isDefault,
			AllowUrgent: ouValue(v.allowUrgent, true), AllowUrgentInherit: v.allowUrgent == nil && !node.isDefault,
			MaxQueued: ouValue(v.maxQueued, 0), MaxQueuedInherit: v.maxQueued == nil && !node.isDefault,
			RunWindow: ouValue(v.runWindow, ""), RunWindowInherit: v.runWindow == nil && !node.isDefault,
			Priority: ouValue(v.priority, ""), Restricted: ouValue(v.restricted, false), QuotaPeriod: QuotaDay,
			DailyQuotaInherit: v.quota == nil && !node.isDefault,
			TOTPEnroll:        ouValue(v.totp, true), TOTPEnrollInherit: v.totp == nil && !node.isDefault,
			PasskeyEnroll: ouValue(v.passkey, true), PasskeyEnrollInherit: v.passkey == nil && !node.isDefault,
			Require2FA: ouValue(v.require2FA, false), Require2FAInherit: v.require2FA == nil && !node.isDefault}
		if v.quota != nil {
			g.DailyRunQuota = v.quota.Limit
			g.QuotaPeriod = v.quota.Period
		}
		if p, err := snapshot.resolve(id); err == nil {
			g.RestrictedEffective = p.Restricted.Effective
		}
		out = append(out, g)
	}
	slices.SortFunc(out, func(a, b UserGroup) int {
		if a.IsDefault != b.IsDefault {
			if a.IsDefault {
				return -1
			}
			return 1
		}
		return strings.Compare(a.Name, b.Name)
	})
	return out
}

func (s *Store) loadOUGroupListing() (ouPolicySnapshot, map[int64]int, error) {
	opts := &sql.TxOptions{ReadOnly: true}
	if s.driver == "postgres" {
		opts.Isolation = sql.LevelRepeatableRead
	}
	tx, err := s.db.BeginTx(context.Background(), opts)
	if err != nil {
		return ouPolicySnapshot{}, nil, err
	}
	defer tx.Rollback()
	snapshot, err := s.loadOUPolicySnapshotFrom(tx)
	if err != nil {
		return ouPolicySnapshot{}, nil, err
	}
	rows, err := tx.Query(`SELECT COALESCE(group_id,0),COUNT(*) FROM users GROUP BY group_id`)
	if err != nil {
		return ouPolicySnapshot{}, nil, err
	}
	members := map[int64]int{}
	for rows.Next() {
		var id int64
		var count int
		if err := rows.Scan(&id, &count); err != nil {
			rows.Close()
			return ouPolicySnapshot{}, nil, err
		}
		members[id] = count
	}
	readErr := rows.Err()
	rows.Close()
	if readErr != nil {
		return ouPolicySnapshot{}, nil, readErr
	}
	if err := tx.Commit(); err != nil {
		return ouPolicySnapshot{}, nil, err
	}
	return snapshot, members, nil
}

func (s *Store) adminOUGroups() ([]map[string]any, error) {
	snapshot, members, err := s.loadOUGroupListing()
	if err != nil {
		return nil, err
	}
	groups := userGroupsJSON(snapshot.groupRows(members))
	for _, g := range groups {
		id := g["id"].(int64)
		g["forecast_scope"] = snapshot.values[id].scope
		p, err := snapshot.resolve(id)
		if err != nil {
			g["policy_available"] = false
			g["policy_error"] = "ou_invalid_topology"
			g["forecast_scope_effective"] = scopeNone
			g["forecast_scope_source"] = int64(0)
			continue
		}
		g["policy_available"] = true
		g["policy_results"] = p
		g["forecast_scope_effective"] = p.ForecastScope.Effective
		g["forecast_scope_source"] = p.ForecastScope.SourceOU
		inherited, err := snapshot.inheritedPolicies(id)
		if err != nil {
			return nil, err
		}
		g["inherited_policy_results"] = inherited
	}
	return groups, nil
}

// apiGroupPolicyPreview runs the same proposed-state validation as save, without a mutation or
// audit. Its answer is advisory: the writer revalidates all facts under the shared mutation lock.
func (s *Server) apiGroupPolicyPreview(w http.ResponseWriter, r *http.Request, _ string) {
	var in groupInput
	if err := readJSON(r, &in); err != nil {
		jsonError(w, http.StatusBadRequest, "bad json")
		return
	}
	id := pathID(r, "id")
	in.Priority = s.groupPriorityValid(in.Priority)
	opts := &sql.TxOptions{ReadOnly: true}
	if s.st.driver == "postgres" {
		opts.Isolation = sql.LevelRepeatableRead
	}
	tx, err := s.st.db.BeginTx(r.Context(), opts)
	if err != nil {
		ouWriteHTTPError(w, err)
		return
	}
	defer tx.Rollback()
	snapshot, err := s.st.loadOUPolicySnapshotFrom(tx)
	if err != nil {
		ouWriteHTTPError(w, err)
		return
	}
	proposed, err := prepareOUInput(snapshot, id, in, false)
	if err != nil {
		ouWriteHTTPError(w, err)
		return
	}
	if err := validateOUMandates(migExec{s: s.st, ex: tx}, proposed); err != nil {
		ouWriteHTTPError(w, err)
		return
	}
	p, err := proposed.resolve(id)
	if err != nil {
		ouWriteHTTPError(w, err)
		return
	}
	inherit, err := proposed.inheritedPolicies(id)
	if err != nil {
		ouWriteHTTPError(w, err)
		return
	}
	subtree, err := proposed.graph.subtree(id)
	if err != nil {
		ouWriteHTTPError(w, err)
		return
	}
	accounts := 0
	for _, ou := range subtree {
		var n int
		if err := tx.QueryRow(s.st.bind(`SELECT COUNT(*) FROM users WHERE group_id=?`), ou).Scan(&n); err != nil {
			ouWriteHTTPError(w, err)
			return
		}
		accounts += n
	}
	oldChain, oldErr := snapshot.graph.actualChain(id)
	newChain, newErr := proposed.graph.actualChain(id)
	ancestorsChanged := oldErr != nil || newErr != nil || !slices.Equal(oldChain, newChain)
	if err := tx.Commit(); err != nil {
		ouWriteHTTPError(w, err)
		return
	}
	writeJSON(w, map[string]any{"policy_results": p, "inherited_policy_results": inherit,
		"affected_ous": len(subtree), "affected_accounts": accounts,
		"policy_changes":              ouPolicyChangeCount(snapshot, proposed, subtree),
		"population_ancestry_changed": ancestorsChanged, "external_ancestry_changed": ancestorsChanged})
}
