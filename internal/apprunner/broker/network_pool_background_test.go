package broker

import (
	"context"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
)

// backgroundPoolFixture returns a pool whose executor blocks the first `hold`
// requests until release is closed and answers every later request at once.
type backgroundPoolFixture struct {
	pool          *NetworkPool
	raw, sig      []byte
	auth          control.Authority
	entered       chan struct{}
	release       chan struct{}
	remote        atomic.Int32
	closedRelease atomic.Bool
}

func newBackgroundPoolFixture(t *testing.T, active, concurrency, hold int) *backgroundPoolFixture {
	t.Helper()
	f := &backgroundPoolFixture{entered: make(chan struct{}, 16), release: make(chan struct{})}
	policy := testPolicy()
	policy.Concurrency = concurrency
	h, _, _ := fixture(t, policy, func(context.Context) error { return nil }, func(w http.ResponseWriter, r *http.Request) {
		n := int(f.remote.Add(1))
		f.entered <- struct{}{}
		if n <= hold {
			<-f.release
		}
		_, _ = w.Write([]byte("ok"))
	})
	f.raw, f.sig, f.auth = signedNetworkFixture(t, h, []byte(`{}`))
	pool, err := NewNetworkPool(active, 2, func(context.Context, control.NetworkHTTPRequestCommand) (control.Authority, error) {
		return f.auth, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	pool.create = func(HTTPPolicy) (*HTTP, error) { return h, nil }
	f.pool = pool
	t.Cleanup(f.unblock)
	return f
}

func (f *backgroundPoolFixture) unblock() {
	if f.closedRelease.CompareAndSwap(false, true) {
		close(f.release)
	}
}

func (f *backgroundPoolFixture) background() error {
	_, err := f.pool.DoBackground(context.Background(), f.raw, f.sig, []byte(`{}`), f.auth)
	return err
}

func (f *backgroundPoolFixture) foreground() error {
	_, err := f.pool.Do(context.Background(), f.raw, f.sig, []byte(`{}`), f.auth)
	return err
}

func (f *backgroundPoolFixture) waitEntered(t *testing.T, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		select {
		case <-f.entered:
		case <-time.After(5 * time.Second):
			t.Fatal("upstream was not reached")
		}
	}
}

func TestNetworkPoolBackgroundHoldsAtMostHalf(t *testing.T) {
	f := newBackgroundPoolFixture(t, 4, 4, 2)
	done := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() { done <- f.background() }()
	}
	f.waitEntered(t, 2)
	err := f.background()
	if !errors.Is(err, ErrBusy) || !errors.Is(err, ErrUnavailable) {
		t.Fatalf("third background call: %v", err)
	}
	if f.remote.Load() != 2 {
		t.Fatal("a refused background call reached upstream")
	}
	// Half of the pool is still free for foreground work.
	if err := f.foreground(); err != nil {
		t.Fatalf("foreground starved by background: %v", err)
	}
	f.unblock()
	for i := 0; i < 2; i++ {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	if err := f.background(); err != nil {
		t.Fatalf("share not released: %v", err)
	}
}

func TestNetworkPoolBackgroundShareZeroWhenActiveOne(t *testing.T) {
	f := newBackgroundPoolFixture(t, 1, 4, 0)
	err := f.background()
	if !errors.Is(err, ErrUnavailable) || errors.Is(err, ErrBusy) {
		t.Fatalf("disabled background share must be unavailable, not busy: %v", err)
	}
	if f.remote.Load() != 0 {
		t.Fatal("disabled background reached upstream")
	}
	if err := f.foreground(); err != nil {
		t.Fatal(err)
	}
}

func TestNetworkPoolBackgroundSharesInstallationConcurrency(t *testing.T) {
	f := newBackgroundPoolFixture(t, 4, 2, 2)
	done := make(chan error, 2)
	go func() { done <- f.foreground() }()
	f.waitEntered(t, 1)
	go func() { done <- f.background() }()
	f.waitEntered(t, 1)
	// Foreground and background draw on one per-installation budget.
	if err := f.background(); !errors.Is(err, ErrBusy) {
		t.Fatalf("background beyond the installation budget: %v", err)
	}
	if err := f.foreground(); !errors.Is(err, ErrUnavailable) || errors.Is(err, ErrBusy) {
		t.Fatalf("foreground beyond the installation budget must keep its error: %v", err)
	}
	f.unblock()
	for i := 0; i < 2; i++ {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
}
