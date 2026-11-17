package app

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"sync"
	"testing"
	"time"
)

// A file-backed SQLite store with its one connection, holding a completed writable installation.
// The fixture is built in memory and its image copied to disk, so the pool and the statement path
// are the production ones.
func platformRelayFileStoreFixture(t testing.TB) (*Server, platformActorSession, string) {
	t.Helper()
	tt, ok := t.(*testing.T)
	if !ok {
		t.Fatal("fixture needs a *testing.T")
	}
	s, actor, lease := platformWritableAccessFixture(tt, false)
	var image []byte
	if err := withSQLiteConn(s.st.db, func(c sqliteImageConn) error {
		var err error
		image, err = c.Serialize()
		return err
	}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "portal.db")
	if err := os.WriteFile(path, image, 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := connectStore("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	s.st = st
	return s, actor, lease.installationID
}

// One relayed backend request holds the database three times: the initial admission, the
// admission that signs the request, and the final admission before the response is released.
func platformRelayAdmissions(ctx context.Context, s *Server, actor platformActorSession, installation string) error {
	initial, err := s.preparePlatformWritableBackendAccess(ctx, actor, installation)
	if err != nil {
		return err
	}
	defer initial.Close()
	current, err := s.withPlatformWritableBackendAccess(ctx, actor, installation, func(a platformWritableBackendAccess, seed []byte) error {
		_, _, _, _, err := signPlatformWritableBackendRequest(a, seed, "POST", "/v1/echo", []byte(`{"value":"x"}`), "bench-boot")
		return err
	})
	if err != nil {
		return err
	}
	defer current.Close()
	final, err := s.preparePlatformWritableBackendAccess(ctx, actor, installation)
	if err != nil {
		return err
	}
	final.Close()
	return nil
}

// platformRelayContention runs workers concurrent relayed requests against the single connection
// and samples a stream of small reads, the shape of a page load, for as long as they run.
func platformRelayContention(tb testing.TB, s *Server, actor platformActorSession, installation string, workers, requests int) (reads []time.Duration, elapsed time.Duration) {
	ctx := context.Background()
	var wg sync.WaitGroup
	start := time.Now()
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < requests; i++ {
				if err := platformRelayAdmissions(ctx, s, actor, installation); err != nil {
					tb.Error(err)
					return
				}
			}
		}()
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	for {
		select {
		case <-done:
			return reads, time.Since(start)
		default:
		}
		t0 := time.Now()
		var n int
		if err := s.st.queryRow(`SELECT COUNT(*) FROM meta`).Scan(&n); err != nil {
			tb.Error(err)
			<-done
			return reads, time.Since(start)
		}
		reads = append(reads, time.Since(t0))
	}
}

func percentile(d []time.Duration, p float64) time.Duration {
	if len(d) == 0 {
		return 0
	}
	sorted := append([]time.Duration(nil), d...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	return sorted[int(float64(len(sorted)-1)*p)]
}

// TestPlatformRelayConnectionContention reports how long a small read waits for the single SQLite
// connection while eight backends are relayed. It asserts only that every request and read
// completed; the figures are the evidence (go test -v -run PlatformRelayConnectionContention).
// RELAY_CONTENTION_REQUESTS scales the load.
func TestPlatformRelayConnectionContention(t *testing.T) {
	s, actor, installation := platformRelayFileStoreFixture(t)
	requests := 6
	if v, err := strconv.Atoi(os.Getenv("RELAY_CONTENTION_REQUESTS")); err == nil && v > 0 {
		requests = v
	}
	// Warm the statement and page caches so the idle figure is the steady state.
	if err := platformRelayAdmissions(context.Background(), s, actor, installation); err != nil {
		t.Fatal(err)
	}
	var idle []time.Duration
	for i := 0; i < 2000; i++ {
		t0 := time.Now()
		var n int
		if err := s.st.queryRow(`SELECT COUNT(*) FROM meta`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		idle = append(idle, time.Since(t0))
	}
	reads, elapsed := platformRelayContention(t, s, actor, installation, 8, requests)
	if len(reads) == 0 {
		t.Fatal("no small read completed while backends were relayed")
	}
	t.Logf("idle:  small read p50=%v p95=%v max=%v (n=%d)", percentile(idle, .5), percentile(idle, .95), percentile(idle, 1), len(idle))
	t.Logf("8 relayed backends x %d requests in %v: small read p50=%v p95=%v max=%v (n=%d)",
		requests, elapsed, percentile(reads, .5), percentile(reads, .95), percentile(reads, 1), len(reads))
}
