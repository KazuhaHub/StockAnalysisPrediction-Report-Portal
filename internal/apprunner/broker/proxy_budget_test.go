package broker

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
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
	if got, _ := b.Used("a", day); got != 10000 {
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
	if got, _ := second.Used("a", day); got != 1234 {
		t.Fatalf("after a restart the budget is %d", got)
	}
	if got := second.Add("a", day, 6); got != 1240 {
		t.Fatalf("counting continues from the stored value: %d", got)
	}
	if got, _ := second.Used("a", day+1); got != 0 {
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
	if got, _ := b.Used("a", day); got != 100 {
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

// A read that fails is an error, not zero, and is not remembered: the next read tries the store again. The store
// is not called with the budget's lock held, so a slow journal read does not stall counting for other tunnels.
func TestDurableBudgetReadFailureIsAnErrorAndIsRetried(t *testing.T) {
	store := &unreadableStore{memoryBudgetStore: newMemoryBudgetStore()}
	day := time.Now().UTC().Unix() / 86400
	store.rows["a"] = map[int64]int64{day: 700}
	b := NewDurableProxyBudget(store, time.Now, time.Hour)
	defer b.Close()
	store.fail.Store(true)
	if _, err := b.Used("a", day); err == nil {
		t.Fatal("an unreadable store counted as zero")
	}
	// Bytes counted while it is unreadable are kept and added to the stored value once it can be read.
	b.Add("a", day, 5)
	store.fail.Store(false)
	if got, err := b.Used("a", day); err != nil || got != 705 {
		t.Fatalf("after the store can be read: %d %v", got, err)
	}
	// Another key can be read while the first read of one blocks.
	release := make(chan struct{})
	store.block = release
	started := make(chan struct{}, 1)
	store.started = started
	go b.Used("slow", day)
	<-started
	done := make(chan struct{})
	go func() {
		b.Add("a", day, 1)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("a journal read held the budget's lock")
	}
	close(release)
}

type unreadableStore struct {
	*memoryBudgetStore
	fail    atomic.Bool
	block   chan struct{}
	started chan struct{}
}

func (s *unreadableStore) ProxyBudgetUsed(ctx context.Context, id string, day int64) (int64, error) {
	if s.fail.Load() {
		return 0, errors.New("journal unreadable")
	}
	if id == "slow" && s.block != nil {
		s.started <- struct{}{}
		<-s.block
	}
	return s.memoryBudgetStore.ProxyBudgetUsed(ctx, id, day)
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
