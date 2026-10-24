package app

// The forecast adapter retains its typed scope data while delegating topology to the common graph.

// The closed set of forecast_scope values. An unknown stored value resolves as none.
const (
	scopeAll  = "all"
	scopeOwn  = "own"
	scopeNone = "none"
)

// ouIndex is the whole OU table read in ONE statement, with every failure surfaced.
type ouIndex struct {
	defaultID int64
	ous       map[int64]ouRow
	topology  *ouGraph
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
	snapshot, err := s.loadOUPolicySnapshotFrom(ex)
	if err != nil {
		return ouIndex{}, err
	}
	ix := ouIndex{defaultID: snapshot.graph.defaultID, topology: &snapshot.graph, ous: map[int64]ouRow{}}
	for id, node := range snapshot.graph.nodes {
		v := snapshot.values[id]
		ix.ous[id] = ouRow{parent: node.parent, restricted: v.restricted != nil && *v.restricted, scope: v.scope}
	}
	return ix, nil
}

func (ix ouIndex) graph() ouGraph {
	if ix.topology != nil {
		return *ix.topology
	}
	// Pure forecast fixtures may construct the adapter directly without a database snapshot.
	nodes := make(map[int64]ouNode, len(ix.ous))
	for id, row := range ix.ous {
		nodes[id] = ouNode{parent: row.parent, isDefault: id == ix.defaultID}
	}
	return newOUGraph(nodes)
}

// chain returns leaf → root, with the Default OU appended when the walk reaches a NULL parent
// without passing it. A non-NULL parent that is missing from the index is errDanglingOU; a cycle or
// a chain deeper than 64 is errCyclicOU. A truncated chain is never returned.
func (ix ouIndex) chain(leaf int64) ([]int64, error) {
	return ix.graph().policyChain(leaf)
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
	out, err := ix.graph().subtree(root)
	if err != nil {
		return nil
	}
	return out
}
