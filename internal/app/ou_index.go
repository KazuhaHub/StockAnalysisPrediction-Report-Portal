package app

// The OU index (P38, ADR 0039).
//
// The tenancy code already walks the OU tree through groupChain and EffectiveGroupSettings, both of
// which SWALLOW their errors and return a partial answer. That is fine for a default that only
// broadens what an internal administrator can configure, and it is not fine for a visibility rule:
// a truncated chain drops a restricted ancestor and fails OPEN. This index is the strict counterpart:
// the whole table in one read, and an error rather than a shorter chain.

import (
	"database/sql"
	"errors"
	"sort"
)

// The closed set of forecast_scope values. An unknown stored value resolves as none.
const (
	scopeAll  = "all"
	scopeOwn  = "own"
	scopeNone = "none"
)

var (
	errDanglingOU = errors.New("ou chain: parent_id names an OU that is not in the index")
	errCyclicOU   = errors.New("ou chain: cycle or depth over 64")
)

// ouIndex is the whole OU table read in ONE statement, with every failure surfaced.
type ouIndex struct {
	defaultID int64
	ous       map[int64]ouRow
}

type ouRow struct {
	parent     int64
	restricted bool    // the OU's own flag
	scope      *string // forecast_scope; nil = inherit
}

// loadOUIndex reads every OU once. A failed read returns an error, never an empty index: an empty
// index would make every chain dangling and every restriction false, which is the wrong direction.
func (s *Store) loadOUIndex() (ouIndex, error) {
	return s.loadOUIndexFrom(s.db)
}

// loadOUIndexFrom permits an access decision to read the account and tree on the same snapshot.
// Callers holding a transaction must pass it rather than taking another pool connection.
func (s *Store) loadOUIndexFrom(ex execer) (ouIndex, error) {
	ix := ouIndex{ous: map[int64]ouRow{}}
	rows, err := ex.Query(`SELECT id, COALESCE(parent_id,0), COALESCE(restricted,0),
		COALESCE(is_default,0), forecast_scope FROM user_groups`)
	if err != nil {
		return ouIndex{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, parent int64
		var restricted, isDefault int
		var scope sql.NullString
		if err := rows.Scan(&id, &parent, &restricted, &isDefault, &scope); err != nil {
			return ouIndex{}, err
		}
		row := ouRow{parent: parent, restricted: restricted == 1}
		if scope.Valid {
			v := scope.String
			row.scope = &v
		}
		ix.ous[id] = row
		if isDefault == 1 && ix.defaultID == 0 {
			ix.defaultID = id
		}
	}
	if err := rows.Err(); err != nil {
		return ouIndex{}, err
	}
	return ix, nil
}

// chain returns leaf → root, with the Default OU appended when the walk reaches a NULL parent
// without passing it. A non-NULL parent that is missing from the index is errDanglingOU; a cycle or
// a chain deeper than 64 is errCyclicOU. A truncated chain is never returned.
func (ix ouIndex) chain(leaf int64) ([]int64, error) {
	if leaf == 0 {
		return nil, nil
	}
	var out []int64
	seen := map[int64]bool{}
	cur := leaf
	for depth := 0; ; depth++ {
		if depth >= 64 {
			return nil, errCyclicOU
		}
		row, ok := ix.ous[cur]
		if !ok {
			return nil, errDanglingOU
		}
		if seen[cur] {
			return nil, errCyclicOU
		}
		seen[cur] = true
		out = append(out, cur)
		if cur == ix.defaultID {
			return out, nil
		}
		if row.parent == 0 {
			if ix.defaultID != 0 {
				out = append(out, ix.defaultID)
			}
			return out, nil
		}
		cur = row.parent
	}
}

// restrictedEffective is sticky: the OU's own flag or any ancestor's. The error is chain's,
// unchanged: a caller that cannot trust the walk must treat it as "less sharing", never as false.
func (ix ouIndex) restrictedEffective(id int64) (bool, error) {
	ids, err := ix.chain(id)
	if err != nil {
		return false, err
	}
	for _, i := range ids {
		if ix.ous[i].restricted {
			return true, nil
		}
	}
	return false, nil
}

// effectiveScope is the nearest non-NULL forecast_scope walking leaf → root, with a NULL on the
// Default OU read as none. A value outside the closed set also resolves to none: an older or newer
// build can therefore never widen access.
func (ix ouIndex) effectiveScope(id int64) (string, error) {
	ids, err := ix.chain(id)
	if err != nil {
		return scopeNone, err
	}
	for i, ouID := range ids {
		row := ix.ous[ouID]
		if row.scope != nil {
			switch *row.scope {
			case scopeAll, scopeOwn, scopeNone:
				return *row.scope, nil
			default:
				return scopeNone, nil
			}
		}
		if ouID == ix.defaultID && i == len(ids)-1 {
			// Nothing above it, and the Default OU inherits nothing.
			return scopeNone, nil
		}
	}
	return scopeNone, nil
}

// subtree returns root and its descendants by parent_id, sorted. The edges are the same ones
// subtreeIDs uses; a cycle cannot make it loop, and an id missing from the index yields nothing.
func (ix ouIndex) subtree(root int64) []int64 {
	if _, ok := ix.ous[root]; !ok {
		return nil
	}
	children := map[int64][]int64{}
	for id, row := range ix.ous {
		if row.parent != 0 {
			children[row.parent] = append(children[row.parent], id)
		}
	}
	out := make([]int64, 0, 8)
	queue := []int64{root}
	seen := map[int64]bool{}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		if seen[cur] {
			continue
		}
		seen[cur] = true
		out = append(out, cur)
		queue = append(queue, children[cur]...)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}
