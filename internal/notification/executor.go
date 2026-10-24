package notification

import (
	"context"
	"log"
	"sync"
)

// Executor reserves separate, bounded capacity for transactional mail. It has no
// idle goroutines. Waiting tasks and active tasks both count against admission.
type Executor struct {
	mu       sync.Mutex
	closed   bool
	ctx      context.Context
	cancel   context.CancelFunc
	admitted chan struct{}
	active   chan struct{}
	wg       sync.WaitGroup
}

func NewExecutor(workers, waiting int) *Executor {
	if workers < 1 || waiting < 0 {
		panic("invalid executor capacity")
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Executor{ctx: ctx, cancel: cancel, admitted: make(chan struct{}, workers+waiting), active: make(chan struct{}, workers)}
}

// Submit never waits for network or queue capacity and returns false on overload.
func (e *Executor) Submit(task func(context.Context)) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return false
	}
	select {
	case e.admitted <- struct{}{}:
	default:
		return false
	}
	e.wg.Add(1)
	go func() {
		defer e.wg.Done()
		defer func() {
			<-e.admitted
			if recover() != nil {
				log.Print("notification: transactional task panicked")
			}
		}()
		select {
		case e.active <- struct{}{}:
		case <-e.ctx.Done():
			return
		}
		defer func() { <-e.active }()
		if e.ctx.Err() == nil {
			task(e.ctx)
		}
	}()
	return true
}

// Close prevents new work and cancels both active and waiting tasks.
func (e *Executor) Close(ctx context.Context) error {
	e.mu.Lock()
	e.closed = true
	e.cancel()
	e.mu.Unlock()
	done := make(chan struct{})
	go func() { e.wg.Wait(); close(done) }()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
