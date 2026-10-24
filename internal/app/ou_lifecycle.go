package app

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"slices"
)

func validateOUDeletionReferences(e migExec, id int64) error {
	var members, providers int
	if err := e.queryRow(`SELECT COUNT(*) FROM users WHERE group_id=?`, id).Scan(&members); err != nil {
		return err
	}
	if members > 0 {
		return invalidOU("ou_has_members", "move this OU's members explicitly before deleting it")
	}
	if err := e.queryRow(`SELECT COUNT(*) FROM sso_providers WHERE default_group=?`, id).Scan(&providers); err != nil {
		return err
	}
	if providers > 0 {
		return invalidOU("ou_has_sso_references", "update the identity provider's default OU before deleting this unit")
	}
	var raw string
	err := e.queryRow(`SELECT COALESCE(v,'') FROM meta WHERE k=?`, setSSORules).Scan(&raw)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if raw != "" {
		var rules []storedRule
		if err := json.Unmarshal([]byte(raw), &rules); err != nil {
			return err
		}
		for _, r := range rules {
			if r.TargetGroup == id {
				return invalidOU("ou_has_sso_references", "update the SSO assignment rules before deleting this unit")
			}
		}
	}
	return nil
}

type ouTopologyIssue struct {
	OU   int64  `json:"ou"`
	Code string `json:"code"`
}

func (g ouGraph) issues() []ouTopologyIssue {
	out := make([]ouTopologyIssue, 0)
	if g.defaultErr != nil {
		out = append(out, ouTopologyIssue{Code: "invalid_default"})
	}
	ids := make([]int64, 0, len(g.nodes))
	for id := range g.nodes {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	for _, id := range ids {
		n := g.nodes[id]
		if !n.isDefault && n.parent == 0 {
			out = append(out, ouTopologyIssue{OU: id, Code: "legacy_root"})
		}
		if _, err := g.actualChain(id); err != nil && !errors.Is(err, errDefaultOU) {
			code := "dangling_parent"
			if errors.Is(err, errCyclicOU) {
				code = "cycle_or_depth"
			}
			out = append(out, ouTopologyIssue{OU: id, Code: code})
		}
	}
	return out
}

func (s *Server) apiOUTopology(w http.ResponseWriter, r *http.Request, _ string) {
	snapshot, err := s.st.loadOUPolicySnapshotFrom(s.st.db)
	if err != nil {
		ouWriteHTTPError(w, err)
		return
	}
	writeJSON(w, map[string]any{"default_ou": snapshot.graph.defaultID, "issues": snapshot.graph.issues()})
}

func ouPolicyChangeCount(before, after ouPolicySnapshot, ids []int64) int {
	changed := 0
	for _, id := range ids {
		a, ae := before.resolve(id)
		b, be := after.resolve(id)
		if (ae == nil) != (be == nil) || !reflect.DeepEqual(a, b) {
			changed++
		}
	}
	return changed
}
