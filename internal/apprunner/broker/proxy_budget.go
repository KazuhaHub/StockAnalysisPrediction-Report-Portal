package broker

import (
	"context"
	"sync"
	"time"
)

// ProxyBudgetStore is where the runner keeps the daily budget so that a restart does not reset it: the journal.
type ProxyBudgetStore interface {
	ProxyBudgetUsed(ctx context.Context, installation string, utcDay int64) (int64, error)
	AddProxyBudget(ctx context.Context, installation string, utcDay, n int64) error
	PruneProxyBudget(ctx context.Context, keepFrom int64) error
}

type budgetKey struct {
	installation string
	day          int64
}

// DurableProxyBudget is a ProxyBudget over a store. Bytes are counted in memory, which is what Used and Add read,
// and written to the store in batches: a write per 32 KiB chunk would be hundreds a second at the throughput cap.
// A clean close flushes, so a restart loses at most the bytes since the last flush after a crash (one interval),
// and a failed write is retried, never counted twice. Days older than yesterday are pruned from the store when the
// UTC day changes.
type DurableProxyBudget struct {
	store ProxyBudgetStore
	now   func() time.Time

	mu      sync.Mutex
	used    map[budgetKey]int64 // what is counted: the stored value plus what is pending
	pending map[budgetKey]int64
	pruned  int64 // the day the last prune ran for, 0 before the first

	stop chan struct{}
	done chan struct{}
	once sync.Once
}

// NewDurableProxyBudget starts a budget that flushes every interval until Close.
func NewDurableProxyBudget(store ProxyBudgetStore, now func() time.Time, interval time.Duration) *DurableProxyBudget {
	if now == nil {
		now = time.Now
	}
	b := &DurableProxyBudget{store: store, now: now, used: map[budgetKey]int64{}, pending: map[budgetKey]int64{}, stop: make(chan struct{}), done: make(chan struct{})}
	go func() {
		defer close(b.done)
		tick := time.NewTicker(interval)
		defer tick.Stop()
		for {
			select {
			case <-tick.C:
				b.Flush()
			case <-b.stop:
				return
			}
		}
	}()
	return b
}

// counted is the key's counted value, read from the store the first time. The store is read without the lock
// held, and a failed read is returned and cached as nothing, so the next call reads again.
func (b *DurableProxyBudget) counted(k budgetKey) (int64, error) {
	b.mu.Lock()
	if v, ok := b.used[k]; ok {
		b.mu.Unlock()
		return v, nil
	}
	b.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	stored, err := b.store.ProxyBudgetUsed(ctx, k.installation, k.day)
	if err != nil {
		return 0, err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if v, ok := b.used[k]; ok {
		return v, nil // another caller read it meanwhile
	}
	b.used[k] = stored + b.pending[k]
	return b.used[k], nil
}

// Used is the bytes counted for the installation on the day, or an error when the store cannot be read.
func (b *DurableProxyBudget) Used(installation string, day int64) (int64, error) {
	return b.counted(budgetKey{installation, day})
}

// Add counts n more bytes and returns the new total. When the store cannot be read the bytes are still counted
// and will be added to the stored value once it can be, and the total returned is only what is pending.
func (b *DurableProxyBudget) Add(installation string, day int64, n int64) int64 {
	k := budgetKey{installation, day}
	_, err := b.counted(k)
	b.mu.Lock()
	defer b.mu.Unlock()
	b.pending[k] += n
	if err != nil {
		return b.pending[k]
	}
	b.used[k] += n
	return b.used[k]
}

// Flush writes what is pending and, once a day, prunes. A key that cannot be written stays pending.
func (b *DurableProxyBudget) Flush() {
	b.mu.Lock()
	batch := b.pending
	b.pending = map[budgetKey]int64{}
	b.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for k, n := range batch {
		if n == 0 {
			continue
		}
		if err := b.store.AddProxyBudget(ctx, k.installation, k.day, n); err != nil {
			b.mu.Lock()
			b.pending[k] += n
			b.mu.Unlock()
		}
	}
	today := b.now().UTC().Unix() / 86400
	b.mu.Lock()
	due := b.pruned != today
	b.mu.Unlock()
	if due && b.store.PruneProxyBudget(ctx, today-1) == nil {
		b.mu.Lock()
		b.pruned = today
		for k := range b.used {
			if k.day < today-1 && b.pending[k] == 0 {
				delete(b.used, k)
			}
		}
		b.mu.Unlock()
	}
}

// Close stops the flushing and flushes once more.
func (b *DurableProxyBudget) Close() {
	b.once.Do(func() {
		close(b.stop)
		<-b.done
		b.Flush()
	})
}
