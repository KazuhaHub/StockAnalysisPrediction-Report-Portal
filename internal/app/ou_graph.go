package app

import (
	"errors"
	"slices"
)

var (
	errDefaultOU  = errors.New("ou tree: exactly one parentless Default OU is required")
	errDanglingOU = errors.New("ou chain: parent_id names an OU that is not in the graph")
	errCyclicOU   = errors.New("ou chain: cycle or depth over 64")
)

// ouNode contains identity and topology only. Policy values never create graph edges.
type ouNode struct {
	parent      int64
	isDefault   bool
	name        string
	description string
	created     string
}

// ouGraph is immutable within an operation. Legacy policy fallback is a separate view of the
// actual graph: inheriting through Default does not make a historical root its descendant.
type ouGraph struct {
	nodes      map[int64]ouNode
	children   map[int64][]int64
	defaultID  int64
	defaultErr error
}

func newOUGraph(nodes map[int64]ouNode) ouGraph {
	g := ouGraph{nodes: nodes, children: make(map[int64][]int64)}
	defaults := 0
	for id, n := range nodes {
		if n.isDefault {
			defaults++
			g.defaultID = id
		}
		if n.parent != 0 {
			g.children[n.parent] = append(g.children[n.parent], id)
		}
	}
	if defaults != 1 || nodes[g.defaultID].parent != 0 {
		g.defaultID = 0
		g.defaultErr = errDefaultOU
	}
	for _, children := range g.children {
		slices.Sort(children)
	}
	return g
}

func (g ouGraph) actualChain(leaf int64) ([]int64, error) {
	if g.defaultErr != nil {
		return nil, g.defaultErr
	}
	if leaf == 0 {
		return nil, nil
	}
	var out []int64
	seen := make(map[int64]bool)
	for id := leaf; id != 0; {
		if len(out) >= 64 || seen[id] {
			return nil, errCyclicOU
		}
		n, ok := g.nodes[id]
		if !ok || id < 0 {
			return nil, errDanglingOU
		}
		seen[id] = true
		out = append(out, id)
		id = n.parent
	}
	return out, nil
}

func (g ouGraph) policyChain(leaf int64) ([]int64, error) {
	chain, err := g.actualChain(leaf)
	if err != nil || len(chain) == 0 {
		return chain, err
	}
	if chain[len(chain)-1] != g.defaultID {
		chain = append(chain, g.defaultID)
	}
	return chain, nil
}

func (g ouGraph) subtree(root int64) ([]int64, error) {
	if _, err := g.actualChain(root); err != nil {
		return nil, err
	}
	if root == 0 {
		return nil, nil
	}
	out := make([]int64, 0, 8)
	queue := []int64{root}
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		out = append(out, id)
		// Validate before adding a branch; malformed descendants never make a partial population.
		for _, child := range g.children[id] {
			if _, err := g.actualChain(child); err != nil {
				return nil, err
			}
			queue = append(queue, child)
		}
	}
	slices.Sort(out)
	return out, nil
}

// ouPolicyResult is the typed own/effective/source contract. Available is explicit so an error
// cannot be serialized as a successful permissive zero. SourceOU zero denotes a built-in baseline.
type ouPolicyResult[T any] struct {
	Own       *T    `json:"own"`
	Effective T     `json:"effective"`
	SourceOU  int64 `json:"source_ou"`
	Inherited bool  `json:"inherited"`
	Available bool  `json:"available"`
}

func nearestOUPolicy[T any](chain []int64, baseline T, read func(int64) *T) ouPolicyResult[T] {
	out := ouPolicyResult[T]{Effective: baseline, Available: true}
	if len(chain) == 0 {
		return out
	}
	out.Own = read(chain[0])
	for _, id := range chain {
		if value := read(id); value != nil {
			out.Effective = *value
			out.SourceOU = id
			out.Inherited = id != chain[0]
			return out
		}
	}
	out.Inherited = len(chain) > 1
	return out
}

func stickyOUPolicy(chain []int64, read func(int64) *bool) ouPolicyResult[bool] {
	out := ouPolicyResult[bool]{Available: true}
	if len(chain) == 0 {
		return out
	}
	out.Own = read(chain[0])
	for _, id := range chain {
		if v := read(id); v != nil && *v {
			out.Effective = true
			out.SourceOU = id
			out.Inherited = id != chain[0]
			return out
		}
	}
	out.Inherited = out.Own == nil && len(chain) > 1
	return out
}
