package app

import (
	"errors"
	"reflect"
	"testing"
)

func TestUnifiedOUGraphSeparatesActualAndPolicyAncestry(t *testing.T) {
	g := newOUGraph(map[int64]ouNode{
		1: {isDefault: true}, 2: {parent: 1}, 3: {parent: 2},
		4: {}, 5: {parent: 4},
	})
	for _, tc := range []struct {
		leaf           int64
		actual, policy []int64
	}{
		{3, []int64{3, 2, 1}, []int64{3, 2, 1}},
		{5, []int64{5, 4}, []int64{5, 4, 1}},
	} {
		actual, err := g.actualChain(tc.leaf)
		if err != nil || !reflect.DeepEqual(actual, tc.actual) {
			t.Fatalf("actual chain %d: %v %v", tc.leaf, actual, err)
		}
		policy, err := g.policyChain(tc.leaf)
		if err != nil || !reflect.DeepEqual(policy, tc.policy) {
			t.Fatalf("policy chain %d: %v %v", tc.leaf, policy, err)
		}
	}
	got, err := g.subtree(1)
	if err != nil || !reflect.DeepEqual(got, []int64{1, 2, 3}) {
		t.Fatalf("legacy roots joined Default population: %v %v", got, err)
	}
}

func TestUnifiedOUGraphRejectsInvalidTopology(t *testing.T) {
	for _, tc := range []struct {
		name  string
		nodes map[int64]ouNode
		leaf  int64
		want  error
	}{
		{"missing-default", map[int64]ouNode{2: {}}, 2, errDefaultOU},
		{"duplicate-default", map[int64]ouNode{1: {isDefault: true}, 2: {isDefault: true}}, 2, errDefaultOU},
		{"parented-default", map[int64]ouNode{1: {isDefault: true, parent: 2}, 2: {}}, 1, errDefaultOU},
		{"dangling", map[int64]ouNode{1: {isDefault: true}, 2: {parent: 99}}, 2, errDanglingOU},
		{"cycle", map[int64]ouNode{1: {isDefault: true}, 2: {parent: 3}, 3: {parent: 2}}, 2, errCyclicOU},
	} {
		t.Run(tc.name, func(t *testing.T) {
			chain, err := newOUGraph(tc.nodes).policyChain(tc.leaf)
			if !errors.Is(err, tc.want) || chain != nil {
				t.Fatalf("partial or permissive chain: %v %v, want %v", chain, err, tc.want)
			}
		})
	}
}

func TestUnifiedOUGraphDoesNotHideCyclesInASubtree(t *testing.T) {
	g := newOUGraph(map[int64]ouNode{1: {isDefault: true}, 2: {parent: 3}, 3: {parent: 2}})
	if _, err := g.subtree(2); !errors.Is(err, errCyclicOU) {
		t.Fatalf("cyclic subtree: %v", err)
	}
	if got, err := g.subtree(1); err != nil || !reflect.DeepEqual(got, []int64{1}) {
		t.Fatalf("unrelated branch broke valid subtree: %v %v", got, err)
	}
}

func TestUnifiedOUResolutionPreservesZeroAndProvenance(t *testing.T) {
	chain := []int64{3, 2, 1}
	values := map[int64]*int{2: quotaPtr(9), 1: quotaPtr(3)}
	read := func(id int64) *int { return values[id] }
	got := nearestOUPolicy(chain, 0, read)
	if got.Effective != 9 || got.SourceOU != 2 || !got.Inherited || got.Own != nil {
		t.Fatalf("nearest source: %+v", got)
	}
	values[3] = quotaPtr(0)
	got = nearestOUPolicy(chain, 99, read)
	if got.Effective != 0 || got.SourceOU != 3 || got.Inherited || got.Own == nil {
		t.Fatalf("explicit zero became inherit: %+v", got)
	}
	flags := map[int64]*bool{3: boolPtr(false), 2: boolPtr(true)}
	sticky := stickyOUPolicy(chain, func(id int64) *bool { return flags[id] })
	if !sticky.Effective || sticky.SourceOU != 2 || !sticky.Inherited {
		t.Fatalf("child relaxed an ancestor floor: %+v", sticky)
	}
}
