package broker

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// memoryBudgetStore is what the journal provides, in memory, with a failure switch.
type memoryBudgetStore struct {
	mu     sync.Mutex
	rows   map[string]map[int64]int64
	adds   int
	pruned []int64
	fail   bool
}

func newMemoryBudgetStore() *memoryBudgetStore {
	return &memoryBudgetStore{rows: map[string]map[int64]int64{}}
}

func (s *memoryBudgetStore) ProxyBudgetUsed(_ context.Context, id string, day int64) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.rows[id][day], nil
}

func (s *memoryBudgetStore) AddProxyBudget(_ context.Context, id string, day, n int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail {
		return errors.New("journal unavailable")
	}
	if s.rows[id] == nil {
		s.rows[id] = map[int64]int64{}
	}
	s.rows[id][day] += n
	s.adds++
	return nil
}

func (s *memoryBudgetStore) PruneProxyBudget(_ context.Context, keepFrom int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruned = append(s.pruned, keepFrom)
	for _, days := range s.rows {
		for day := range days {
			if day < keepFrom {
				delete(days, day)
			}
		}
	}
	return nil
}

func (s *memoryBudgetStore) stored(id string, day int64) int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.rows[id][day]
}

func TestDurableBudgetCountsAndWritesInBatches(t *testing.T) {
	store := newMemoryBudgetStore()
	day := time.Now().UTC().Unix() / 86400
	b := NewDurableProxyBudget(store, time.Now, time.Hour)
	for i := 0; i < 1000; i++ {
		b.Add("a", day, 10)
	}
	if got := b.Used("a", day); got != 10000 {
		t.Fatalf("used %d", got)
	}
	if store.stored("a", day) != 0 {
		t.Fatal("every chunk was written to the journal")
	}
	b.Flush()
	if store.stored("a", day) != 10000 || store.adds != 1 {
		t.Fatalf("one batched write expected: %d bytes in %d writes", store.stored("a", day), store.adds)
	}
	b.Flush()
	if store.adds != 1 {
		t.Fatal("an idle flush wrote again")
	}
	b.Close()
}

// The budget survives a runner restart: a new budget over the same store starts from what was written,
// including what the last close flushed.
func TestDurableBudgetSurvivesARestart(t *testing.T) {
	store := newMemoryBudgetStore()
	day := time.Now().UTC().Unix() / 86400
	first := NewDurableProxyBudget(store, time.Now, time.Hour)
	first.Add("a", day, 1234)
	first.Close() // a clean shutdown flushes
	second := NewDurableProxyBudget(store, time.Now, time.Hour)
	defer second.Close()
	if got := second.Used("a", day); got != 1234 {
		t.Fatalf("after a restart the budget is %d", got)
	}
	if got := second.Add("a", day, 6); got != 1240 {
		t.Fatalf("counting continues from the stored value: %d", got)
	}
	if got := second.Used("a", day+1); got != 0 {
		t.Fatalf("the next day starts empty: %d", got)
	}
}

// A journal that cannot be written loses nothing in memory and retries; the bytes are not counted twice.
func TestDurableBudgetRetriesAFailedWrite(t *testing.T) {
	store := newMemoryBudgetStore()
	day := time.Now().UTC().Unix() / 86400
	b := NewDurableProxyBudget(store, time.Now, time.Hour)
	b.Add("a", day, 100)
	store.mu.Lock()
	store.fail = true
	store.mu.Unlock()
	b.Flush()
	if b.Used("a", day) != 100 {
		t.Fatal("a failed write lost the count")
	}
	store.mu.Lock()
	store.fail = false
	store.mu.Unlock()
	b.Add("a", day, 1)
	b.Flush()
	b.Flush()
	if store.stored("a", day) != 101 {
		t.Fatalf("stored %d, want 101", store.stored("a", day))
	}
	b.Close()
}

// Old days are pruned when the UTC day changes, keeping today and yesterday.
func TestDurableBudgetPrunesOldDays(t *testing.T) {
	store := newMemoryBudgetStore()
	clock := &proxyClock{now: time.Date(2026, 11, 19, 10, 0, 0, 0, time.UTC)}
	today := clock.Now().Unix() / 86400
	for day := today - 5; day <= today; day++ {
		store.rows["a"] = map[int64]int64{}
	}
	for day := today - 5; day <= today; day++ {
		store.rows["a"][day] = 1
	}
	b := NewDurableProxyBudget(store, clock.Now, time.Hour)
	defer b.Close()
	b.Flush()
	if len(store.pruned) != 1 || store.pruned[0] != today-1 {
		t.Fatalf("pruned %v, want keep from %d", store.pruned, today-1)
	}
	if len(store.rows["a"]) != 2 {
		t.Fatalf("%d days kept", len(store.rows["a"]))
	}
	b.Flush()
	if len(store.pruned) != 1 {
		t.Fatal("pruned again within the same day")
	}
	clock.advance(24 * time.Hour)
	b.Flush()
	if len(store.pruned) != 2 || store.pruned[1] != today {
		t.Fatalf("pruned %v after the day changed", store.pruned)
	}
}

func TestDurableBudgetFlushesOnItsOwn(t *testing.T) {
	store := newMemoryBudgetStore()
	day := time.Now().UTC().Unix() / 86400
	b := NewDurableProxyBudget(store, time.Now, 20*time.Millisecond)
	defer b.Close()
	b.Add("a", day, 5)
	deadline := time.Now().Add(3 * time.Second)
	for store.stored("a", day) != 5 {
		if time.Now().After(deadline) {
			t.Fatal("the budget was never flushed")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
