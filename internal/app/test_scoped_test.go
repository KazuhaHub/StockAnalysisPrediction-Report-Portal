package app

import (
	"strings"
	"sync"
	"testing"
)

// testScoped is a value a test sets and the shared fixtures it calls read: what used to be a package
// variable assigned at the top of a test and reset in its cleanup. It holds one value per top-level
// test, so two tests that set it can run in parallel, each seeing only its own; within one top-level
// test and its subtests it behaves exactly like the package variable did.
type testScoped[V any] struct{ m sync.Map }

func testTopName(t testing.TB) string {
	top, _, _ := strings.Cut(t.Name(), "/")
	return top
}

// get returns the value t's top-level test set, or the zero value.
func (s *testScoped[V]) get(t testing.TB) V {
	if v, ok := s.m.Load(testTopName(t)); ok {
		return v.(V)
	}
	var zero V
	return zero
}

// set gives t's top-level test the value until t ends, then puts back what was there before.
func (s *testScoped[V]) set(t testing.TB, v V) {
	t.Helper()
	key := testTopName(t)
	prev, had := s.m.Load(key)
	s.m.Store(key, v)
	t.Cleanup(func() {
		if had {
			s.m.Store(key, prev)
		} else {
			s.m.Delete(key)
		}
	})
}
