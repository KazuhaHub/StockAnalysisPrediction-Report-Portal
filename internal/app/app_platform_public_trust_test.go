package app

import (
	"context"
	"crypto/ed25519"
	"fmt"
	"testing"
	"time"
)

// Publisher trust is read with every request, so a disabled publisher or a revoked key stops the
// anonymous projection on the very next request, from the cache too (doc 03 lifecycle).
func TestPublicTrustChangesAreHonouredOnTheNextRequest(t *testing.T) {
	t.Parallel()
	eachPlatformDriver(t, func(t *testing.T, pg bool) {
		cases := map[string]struct{ revoke, restore string }{
			"publisher disabled":              {`UPDATE app_platform_publishers SET enabled=0,revision=revision+1`, `UPDATE app_platform_publishers SET enabled=1,revision=revision+1`},
			"publisher disabled, no revision": {`UPDATE app_platform_publishers SET enabled=0`, `UPDATE app_platform_publishers SET enabled=1`},
			"key revoked, no revision":        {`UPDATE app_platform_publisher_keys SET revoked_ms=1`, `UPDATE app_platform_publisher_keys SET revoked_ms=NULL`},
			"key revoked":                     {`UPDATE app_platform_publisher_keys SET revoked_ms=1,revision=revision+1`, `UPDATE app_platform_publisher_keys SET revoked_ms=NULL,revision=revision+1`},
			"key expired":                     {`UPDATE app_platform_publisher_keys SET not_after_ms=not_before_ms+1,revision=revision+1`, `UPDATE app_platform_publisher_keys SET not_after_ms=NULL,revision=revision+1`},
		}
		for name, c := range cases {
			e := platformPublicFixture(t, pg)
			e.enable(t)
			base := "/public/apps/" + e.id
			if e.data("status", "").Code != 200 || e.get(base+"/app.js", "", nil).Code != 200 {
				t.Fatal(name, "before")
			}
			calls := e.calls.Load()
			mustExec(t, e.s.st, c.revoke)
			for _, path := range []string{"/data/status", "/app.js", "/"} {
				w := e.get(base+path, "", nil)
				if w.Code != 503 || w.Body.String() != `{"code":"unavailable"}` || w.Header().Get("Cache-Control") != "no-store" {
					t.Fatal(name, path, "cached content outlived the trust change", w.Code, w.Body.String())
				}
			}
			if e.calls.Load() != calls {
				t.Fatal(name, "the backend was reached")
			}
			mustExec(t, e.s.st, c.restore)
			if w := e.data("status", ""); w.Code != 200 {
				t.Fatal(name, "did not recover", w.Code)
			}
		}
	})
}

func TestPublicVerifiedReleaseIsNotEvictedByResponses(t *testing.T) {
	t.Parallel()
	var p platformPublicState
	now := time.Now()
	p.useTuple("a", "t")
	verified := &platformPublicEntry{key: "v", installation: "a", tuple: "t", verified: &platformPublicVerified{}, cacheSeconds: 300, expires: now.Add(time.Hour)}
	if !p.store(verified, now) {
		t.Fatal("store")
	}
	for i := 0; i < 3*platformPublicEntriesPerInstallation; i++ {
		if !p.store(&platformPublicEntry{key: fmt.Sprint("k", i), installation: "a", tuple: "t", body: []byte("x"), cacheSeconds: 10, expires: now.Add(time.Hour)}, now) {
			t.Fatal("store", i)
		}
	}
	if p.lookup("v", now) == nil {
		t.Fatal("distinct responses pushed the verified release out")
	}
	if n := p.countLocked("a"); n != platformPublicEntriesPerInstallation {
		t.Fatal("responses", n)
	}
	// Under byte pressure responses go first and the release last.
	big := &platformPublicEntry{key: "big", installation: "a", tuple: "t", body: make([]byte, 7<<20), cacheSeconds: 10, expires: now.Add(time.Hour)}
	if !p.store(big, now) || p.lookup("v", now) == nil {
		t.Fatal("a large response evicted the verified release before other responses")
	}
}

func TestPublicDatabaseFailureCreatesNoPerIdentifierState(t *testing.T) {
	t.Parallel()
	eachPlatformDriver(t, func(t *testing.T, pg bool) {
		e := platformPublicFixture(t, pg)
		e.enable(t)
		mustExec(t, e.s.st, `ALTER TABLE app_platform_capability_grants RENAME TO app_platform_capability_grants_away`)
		restore := func() {
			mustExec(t, e.s.st, `ALTER TABLE app_platform_capability_grants_away RENAME TO app_platform_capability_grants`)
		}
		func() {
			defer restore()
			for i := 0; i < 200; i++ {
				id := fmt.Sprintf("00000000-0000-4000-8000-%012d", i)
				if w := e.get("/public/apps/"+id+"/data/status", fmt.Sprintf("10.5.%d.1:1", i), nil); w.Code != 503 {
					t.Fatal(w.Code)
				}
			}
			if w := e.get("/public/apps/"+e.id+"/data/status", "10.6.0.1:1", nil); w.Code != 503 {
				t.Fatal(w.Code)
			}
			e.s.platformPublic.mu.Lock()
			defer e.s.platformPublic.mu.Unlock()
			for id := range e.s.platformPublic.stats {
				if id != "" {
					t.Fatal("a database failure created statistics for", id)
				}
			}
			if st := e.s.platformPublic.stats[""]; st == nil || st.refusals["store_error"] != 201 {
				t.Fatalf("the failures were not counted globally: %+v", st)
			}
		}()
	})
}

func TestPublicSummaryLoopStopsAtShutdown(t *testing.T) {
	t.Parallel()
	var s Server
	done := make(chan struct{})
	go func() { s.platformPublicSummaryLoop(); close(done) }()
	s.stopPlatformPublic(context.Background())
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the summary loop outlived the shutdown")
	}
	s.stopPlatformPublic(context.Background()) // twice is harmless
}

// A release that cannot be verified is answered 503 and left alone: no archive read per request, and
// never a 429, which would blame the visitor.
func TestPublicUnverifiableReleaseBacksOffAndAnswers503(t *testing.T) {
	t.Parallel()
	eachPlatformDriver(t, func(t *testing.T, pg bool) {
		e := platformPublicFixture(t, pg)
		e.enable(t)
		// The stored key no longer matches the release signature, with no revision to show it: the
		// cheap read passes and only the verification can notice.
		other, _, err := ed25519.GenerateKey(nil)
		if err != nil {
			t.Fatal(err)
		}
		mustExec(t, e.s.st, `UPDATE app_platform_publisher_keys SET public_key=?`, []byte(other))
		for i := 0; i < 40; i++ {
			w := e.get("/public/apps/"+e.id+"/data/status", fmt.Sprintf("10.7.%d.1:1", i), nil)
			if w.Code != 503 || w.Body.String() != `{"code":"unavailable"}` {
				t.Fatal(i, w.Code, w.Body.String())
			}
		}
		if reads := e.s.platformPublicArchiveReads.Load(); reads != 1 {
			t.Fatal("an unverifiable release was retried per request:", reads)
		}
		// After the back-off it is tried again, once.
		e.s.platformPublic.mu.Lock()
		f := e.s.platformPublic.verifyFailed[e.id]
		f.until = time.Now().Add(-time.Second)
		e.s.platformPublic.verifyFailed[e.id] = f
		e.s.platformPublic.mu.Unlock()
		if w := e.data("status", ""); w.Code != 503 {
			t.Fatal(w.Code)
		}
		if reads := e.s.platformPublicArchiveReads.Load(); reads != 2 {
			t.Fatal("no retry after the back-off", reads)
		}
	})
}
