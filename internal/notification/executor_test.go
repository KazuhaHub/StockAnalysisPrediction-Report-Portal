package notification

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

func TestExecutorBoundsAdmissionAndCancelsOnClose(t *testing.T) {
	ex := NewExecutor(2, 2)
	var running, peak atomic.Int32
	started := make(chan struct{}, 4)
	for range 4 {
		if !ex.Submit(func(ctx context.Context) {
			n := running.Add(1)
			defer running.Add(-1)
			for old := peak.Load(); n > old && !peak.CompareAndSwap(old, n); old = peak.Load() {
			}
			started <- struct{}{}
			<-ctx.Done()
		}) {
			t.Fatal("bounded task was refused")
		}
	}
	if ex.Submit(func(context.Context) { t.Error("overflow task executed") }) {
		t.Fatal("overflow admitted")
	}
	for range 2 {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("workers did not start")
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := ex.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if running.Load() != 0 || peak.Load() > 2 {
		t.Fatalf("running=%d peak=%d", running.Load(), peak.Load())
	}
	if ex.Submit(func(context.Context) {}) {
		t.Fatal("closed executor accepted work")
	}
}

func TestExecutorPanicDoesNotLoseCapacity(t *testing.T) {
	ex := NewExecutor(1, 0)
	if !ex.Submit(func(context.Context) { panic("test task") }) {
		t.Fatal("first task refused")
	}
	deadline := time.After(time.Second)
	done := make(chan struct{})
	for !ex.Submit(func(context.Context) { close(done) }) {
		select {
		case <-deadline:
			t.Fatal("panic leaked executor capacity")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	<-done
	if err := ex.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}
