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

func TestNetworkPoolSharesExecutorAcrossViewersAndRequests(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var remote, created atomic.Int32
	h, _, _ := fixture(t, testPolicy(), func(context.Context) error { return nil }, func(w http.ResponseWriter, r *http.Request) {
		remote.Add(1)
		close(entered)
		<-release
		_, _ = w.Write([]byte("ok"))
	})
	raw, sig, a := signedNetworkFixture(t, h, []byte(`{}`))
	pool, err := NewNetworkPool(2, 2, func(context.Context, control.NetworkHTTPRequestCommand) (control.Authority, error) { return a, nil })
	if err != nil {
		t.Fatal(err)
	}
	pool.create = func(HTTPPolicy) (*HTTP, error) { created.Add(1); return h, nil }
	done := make(chan error, 1)
	go func() { _, err := pool.Do(context.Background(), raw, sig, []byte(`{}`), a); done <- err }()
	<-entered
	if _, err := pool.Do(context.Background(), raw, sig, []byte(`{}`), a); !errors.Is(err, ErrUnavailable) {
		t.Fatal("per-grant budget bypassed", err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if remote.Load() != 1 || created.Load() != 1 {
		t.Fatalf("remote=%d created=%d", remote.Load(), created.Load())
	}
}

func TestNetworkPoolGlobalLimitAndBoundedIdleEviction(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var created atomic.Int32
	h, _, _ := fixture(t, testPolicy(), func(context.Context) error { return nil }, func(w http.ResponseWriter, r *http.Request) { close(entered); <-release; _, _ = w.Write([]byte("ok")) })
	raw, sig, a := signedNetworkFixture(t, h, []byte(`{}`))
	pool, _ := NewNetworkPool(1, 1, func(context.Context, control.NetworkHTTPRequestCommand) (control.Authority, error) { return a, nil })
	pool.create = func(HTTPPolicy) (*HTTP, error) { created.Add(1); return h, nil }
	done := make(chan error, 1)
	go func() { _, err := pool.Do(context.Background(), raw, sig, []byte(`{}`), a); done <- err }()
	<-entered
	if _, err := pool.Do(context.Background(), raw, sig, []byte(`{}`), a); !errors.Is(err, ErrUnavailable) {
		t.Fatal("global budget bypassed", err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	// Acquire a distinct verified identity directly to exercise idle eviction without
	// manufacturing another enrolled controller or admitting a network request.
	v, err := control.VerifyNetworkHTTPRequest(raw, sig, a, pool.now().UnixMilli())
	if err != nil {
		t.Fatal(err)
	}
	c := v.Command()
	lease, err := pool.acquire(c)
	if err != nil {
		t.Fatal(err)
	}
	c.Generation = "2"
	if _, err := pool.acquire(c); !errors.Is(err, ErrUnavailable) {
		t.Fatal("active entry evicted", err)
	}
	lease.release()
	lease2, err := pool.acquire(c)
	if err != nil {
		t.Fatal(err)
	}
	lease2.release()
	if len(pool.entries) != 1 || created.Load() != 2 {
		t.Fatalf("unbounded or missing eviction: entries=%d created=%d", len(pool.entries), created.Load())
	}
}

func TestNetworkPoolRejectsBeforeAllocatingOrDispatch(t *testing.T) {
	h, _ := NewHTTP(testPolicy(), func(context.Context) error { return nil })
	raw, sig, a := signedNetworkFixture(t, h, []byte(`{}`))
	var created atomic.Int32
	pool, _ := NewNetworkPool(2, 2, func(context.Context, control.NetworkHTTPRequestCommand) (control.Authority, error) { return a, nil })
	pool.create = func(HTTPPolicy) (*HTTP, error) { created.Add(1); return h, nil }
	if _, err := pool.Do(context.Background(), raw, sig, []byte(`[]`), a); !errors.Is(err, ErrDenied) {
		t.Fatal("body mismatch")
	}
	bad := append([]byte{}, sig...)
	bad[0] ^= 1
	if _, err := pool.Do(context.Background(), raw, bad, []byte(`{}`), a); !errors.Is(err, ErrDenied) {
		t.Fatal("bad signature")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := pool.Do(ctx, raw, sig, []byte(`{}`), a); !errors.Is(err, ErrUnavailable) {
		t.Fatal(err)
	}
	if len(pool.entries) != 0 || created.Load() != 0 {
		t.Fatal("rejected request allocated executor")
	}
	if _, err := (&NetworkPool{}).Do(context.Background(), raw, sig, []byte(`{}`), a); !errors.Is(err, ErrDenied) {
		t.Fatal("zero pool")
	}
	for _, bounds := range [][2]int{{0, 1}, {9, 1}, {1, 0}, {1, 33}} {
		if _, err := NewNetworkPool(bounds[0], bounds[1], pool.current); !errors.Is(err, ErrDenied) {
			t.Fatal("invalid bounds")
		}
	}
	if _, err := NewNetworkPool(1, 1, nil); !errors.Is(err, ErrDenied) {
		t.Fatal("nil authority")
	}
}

func TestNetworkPoolInstallationBudgetSurvivesPolicyKeyChange(t *testing.T) {
	h, _ := NewHTTP(testPolicy(), func(context.Context) error { return nil })
	raw, sig, a := signedNetworkFixture(t, h, []byte(`{}`))
	pool, _ := NewNetworkPool(4, 4, func(context.Context, control.NetworkHTTPRequestCommand) (control.Authority, error) { return a, nil })
	v, err := control.VerifyNetworkHTTPRequest(raw, sig, a, pool.now().UnixMilli())
	if err != nil {
		t.Fatal(err)
	}
	c := v.Command()
	first, err := pool.acquire(c)
	if err != nil {
		t.Fatal(err)
	}
	changed := c
	changed.PolicyRevision = "2"
	if _, err := pool.acquire(changed); !errors.Is(err, ErrUnavailable) {
		t.Fatal("revision multiplied installation budget", err)
	}
	changed.ControllerEpoch = changed.RequestID
	if _, err := pool.acquire(changed); !errors.Is(err, ErrUnavailable) {
		t.Fatal("epoch multiplied installation budget", err)
	}
	first.release()
	first.release()
	next, err := pool.acquire(changed)
	if err != nil {
		t.Fatal(err)
	}
	next.release()
	if len(pool.entries) != 2 {
		t.Fatal("unexpected executor ownership")
	}
}

func TestNetworkPoolRevocationNeverCachesAnExecutor(t *testing.T) {
	h, _ := NewHTTP(testPolicy(), func(context.Context) error { return nil })
	raw, sig, a := signedNetworkFixture(t, h, []byte(`{}`))
	pool, _ := NewNetworkPool(1, 1, func(context.Context, control.NetworkHTTPRequestCommand) (control.Authority, error) {
		return control.Authority{}, errors.New("revoked private details")
	})
	pool.create = func(HTTPPolicy) (*HTTP, error) { t.Fatal("revoked authority allocated executor"); return nil, nil }
	got, err := pool.Do(context.Background(), raw, sig, []byte(`{}`), a)
	if !errors.Is(err, ErrDenied) || got.Status != 0 || len(got.Body) != 0 || len(pool.entries) != 0 || len(pool.slots) != 0 {
		t.Fatalf("%+v %v", got, err)
	}
}

func TestNetworkPoolBoundsInitialAuthorityLookup(t *testing.T) {
	p := testPolicy()
	p.Deadline = 25 * time.Millisecond
	h, _ := NewHTTP(p, func(context.Context) error { return nil })
	raw, sig, a := signedNetworkFixture(t, h, []byte(`{}`))
	pool, _ := NewNetworkPool(1, 1, func(ctx context.Context, _ control.NetworkHTTPRequestCommand) (control.Authority, error) {
		if _, ok := ctx.Deadline(); !ok {
			t.Error("authority lookup has no deadline")
		}
		<-ctx.Done()
		return control.Authority{}, ctx.Err()
	})
	start := time.Now()
	if _, err := pool.Do(context.Background(), raw, sig, []byte(`{}`), a); !errors.Is(err, ErrUnavailable) {
		t.Fatal(err)
	}
	if time.Since(start) > time.Second || len(pool.slots) != 0 || len(pool.entries) != 0 {
		t.Fatal("lookup retained resources")
	}
}
