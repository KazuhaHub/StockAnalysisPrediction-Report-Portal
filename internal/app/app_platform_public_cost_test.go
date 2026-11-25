package app

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
)

// The work an anonymous request costs is bounded: the release is verified and its archive read once
// per tuple, not per request (doc 179 section 2, "anonymous traffic overloads the backend").
func TestPublicRequestsReadTheArchiveOncePerRelease(t *testing.T) {
	t.Parallel()
	eachPlatformDriver(t, func(t *testing.T, pg bool) {
		e := platformPublicFixture(t, pg)
		e.enable(t)
		base := "/public/apps/" + e.id
		for i := 0; i < 60; i++ {
			if w := e.get(base+"/data/status", fmt.Sprintf("10.1.%d.1:1", i), nil); w.Code != 200 {
				t.Fatal(i, w.Code)
			}
			if w := e.get(base+"/app.js", fmt.Sprintf("10.2.%d.1:1", i), nil); w.Code != 200 {
				t.Fatal(i, w.Code)
			}
			if w := e.get(base+"/", fmt.Sprintf("10.3.%d.1:1", i), nil); w.Code != 200 {
				t.Fatal(i, w.Code)
			}
		}
		if reads := e.s.platformPublicArchiveReads.Load(); reads != 1 {
			t.Fatal("archive reads for 180 requests:", reads)
		}
		if e.calls.Load() != 1 {
			t.Fatal("backend fetches", e.calls.Load())
		}
		// A request for a file that does not exist costs a lookup, not another archive read.
		for i := 0; i < 20; i++ {
			if w := e.get(fmt.Sprintf("%s/missing-%d.js", base, i), "", nil); w.Code != 404 {
				t.Fatal(w.Code)
			}
		}
		if reads := e.s.platformPublicArchiveReads.Load(); reads != 1 {
			t.Fatal("misses read the archive", reads)
		}
	})
}

func TestPublicChangesAreHonouredOnTheNextRequest(t *testing.T) {
	t.Parallel()
	eachPlatformDriver(t, func(t *testing.T, pg bool) {
		e := platformPublicFixture(t, pg)
		e.enable(t)
		status := func() int { return e.data("status", "").Code }
		if status() != 200 {
			t.Fatal("first")
		}
		reads, calls := e.s.platformPublicArchiveReads.Load(), e.calls.Load()
		// A new generation of the same release changes the tuple: verified again, fetched again.
		mustExec(t, e.s.st, `UPDATE app_platform_installations SET generation=generation+1 WHERE id=?`, e.id)
		mustExec(t, e.s.st, `UPDATE app_platform_activations SET generation=generation+1 WHERE installation_id=? AND state='active'`, e.id)
		if status() != 200 {
			t.Fatal("after a new generation")
		}
		if e.s.platformPublicArchiveReads.Load() != reads+1 || e.calls.Load() != calls+1 {
			t.Fatal("a changed tuple was served from the old verification or cache", e.s.platformPublicArchiveReads.Load()-reads, e.calls.Load()-calls)
		}
		// The verified release has a bounded life: a trust change is looked at again.
		reads = e.s.platformPublicArchiveReads.Load()
		e.s.platformPublic.mu.Lock()
		for _, entry := range e.s.platformPublic.entries {
			if entry.verified != nil {
				entry.expires = time.Now().Add(-time.Second)
			}
		}
		e.s.platformPublic.mu.Unlock()
		time.Sleep(1100 * time.Millisecond) // the build ceiling is one a second
		if status() != 200 || e.s.platformPublicArchiveReads.Load() != reads+1 {
			t.Fatal("an expired verification was trusted", e.s.platformPublicArchiveReads.Load()-reads)
		}
	})
}

func TestPublicWithdrawalWorksInAnyState(t *testing.T) {
	t.Parallel()
	eachPlatformDriver(t, func(t *testing.T, pg bool) {
		states := map[string]func(*platformPublicEnv) func(){
			"disabled installation": func(e *platformPublicEnv) func() {
				mustExec(t, e.s.st, `UPDATE app_platform_installations SET desired_state='disabled' WHERE id=?`, e.id)
				return func() {
					mustExec(t, e.s.st, `UPDATE app_platform_installations SET desired_state='enabled' WHERE id=?`, e.id)
				}
			},
			"not ready": func(e *platformPublicEnv) func() {
				mustExec(t, e.s.st, `UPDATE app_platform_installations SET phase='review_required' WHERE id=?`, e.id)
				return func() { mustExec(t, e.s.st, `UPDATE app_platform_installations SET phase='ready' WHERE id=?`, e.id) }
			},
			"operation pending": func(e *platformPublicEnv) func() {
				mustExec(t, e.s.st, `UPDATE app_platform_operations SET stage='validate',completed_ms=NULL,lease_owner=NULL,lease_until_ms=NULL WHERE installation_id=?`, e.id)
				return func() {}
			},
		}
		for name, setup := range states {
			e := platformPublicFixture(t, pg)
			e.enable(t)
			revision := e.preview(t).PolicyRevision
			setup(e)
			// Even from a page that knew an older revision.
			body := `{"enabled":false,"policyRevision":"` + revision + `","routes":null,"document":null,"clientPerMinute":null,"backendPerSecond":null,"disclosureSha256":null}`
			if w := e.admin(t, "POST", "public-projection", body); w.Code != 200 {
				t.Fatal(name, "withdrawal refused", w.Code, w.Body.String())
			}
			if scalar[int](t, e.s.st, `SELECT granted FROM app_platform_capability_grants WHERE installation_id=? AND capability='public.projection'`, e.id) != 0 ||
				scalar[string](t, e.s.st, `SELECT policy_json FROM app_platform_capability_grants WHERE installation_id=? AND capability='public.projection'`, e.id) != "{}" {
				t.Fatal(name, "the grant stayed on")
			}
			if scalar[int](t, e.s.st, `SELECT COUNT(*) FROM audit_log WHERE action='app_platform.public_projection_disabled' AND target_id=?`, e.id) != 1 {
				t.Fatal(name, "not audited")
			}
		}
	})
}

func TestPublicUnknownIdentifiersCostNothingToKeep(t *testing.T) {
	t.Parallel()
	eachPlatformDriver(t, func(t *testing.T, pg bool) {
		e := platformPublicFixture(t, pg)
		e.enable(t)
		for i := 0; i < 300; i++ {
			id := fmt.Sprintf("00000000-0000-4000-8000-%012d", i)
			if w := e.get("/public/apps/"+id+"/data/status", fmt.Sprintf("10.4.%d.1:1", i), nil); w.Code != 404 || w.Body.String() != publicNotFound {
				t.Fatal(w.Code)
			}
		}
		e.s.platformPublic.mu.Lock()
		stats, tuples := len(e.s.platformPublic.stats), len(e.s.platformPublic.tuples)
		e.s.platformPublic.mu.Unlock()
		if stats != 0 || tuples != 0 {
			t.Fatal("unknown identifiers left state behind", stats, tuples)
		}
	})
}

func TestPublicStatsAreBoundedWhateverTheIdentifiers(t *testing.T) {
	t.Parallel()
	var p platformPublicState
	now := time.Now()
	for i := 0; i < 5000; i++ {
		p.record(fmt.Sprintf("id-%d", i), now, "unavailable")
	}
	if len(p.stats) > platformPublicStatsInstallations+1 {
		t.Fatal("stats table grew", len(p.stats))
	}
}

func TestPublicSummariesAreWrittenOnDisableOnTimerAndOnShutdown(t *testing.T) {
	t.Parallel()
	eachPlatformDriver(t, func(t *testing.T, pg bool) {
		e := platformPublicFixture(t, pg)
		e.enable(t)
		e.data("status", "")
		if w := e.disable(t); w.Code != 200 {
			t.Fatal(w.Code)
		}
		count := func() int {
			return scalar[int](t, e.s.st, `SELECT COUNT(*) FROM audit_log WHERE action='app_platform.public_projection_summary' AND target_id=?`, e.id)
		}
		if count() != 1 {
			t.Fatal("disable did not write the open window", count())
		}
		// A window that has ended is written by the timer; one still open is not.
		e.enable(t)
		e.data("status", "")
		e.s.writePlatformPublicDue(time.Now())
		if count() != 1 {
			t.Fatal("an open window was written early")
		}
		e.s.writePlatformPublicDue(time.Now().Add(platformPublicSummaryWindow + time.Second))
		if count() != 2 {
			t.Fatal("the timer did not write the ended window", count())
		}
		// Shutdown writes what is open, and the window never claims more than it counted.
		e.data("status", "")
		e.s.stopPlatformPublic(context.Background())
		if count() != 3 {
			t.Fatal("shutdown did not write the open window", count())
		}
		var detail string
		if err := e.s.st.queryRow(`SELECT detail FROM audit_log WHERE action='app_platform.public_projection_summary' AND target_id=? ORDER BY id DESC LIMIT 1`, e.id).Scan(&detail); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(detail, `"window_end"`) || strings.Contains(detail, "window_seconds") {
			t.Fatal(detail)
		}
	})
}

func TestPublicStatsDetailNeverSpansMoreThanItsWindow(t *testing.T) {
	t.Parallel()
	st := &platformPublicStats{start: time.Unix(1_800_000_000, 0), refusals: map[string]int{}}
	late := st.detail(st.start.Add(3 * time.Hour))
	if late["window_end"] != st.start.Add(platformPublicSummaryWindow).UTC().Format(time.RFC3339) {
		t.Fatal(late)
	}
	early := st.detail(st.start.Add(time.Minute))
	if early["window_end"] != st.start.Add(time.Minute).UTC().Format(time.RFC3339) {
		t.Fatal(early)
	}
}

// What the Portal signs for a public request, and what the runner will accept of it: the command
// binds the digest of a public token, the token binds the route, and the runner's own validation
// refuses a public token on a write or a private path (generation 1 of the relay).
func TestPublicRequestSignsAPublicAssertionTheRunnerAccepts(t *testing.T) {
	t.Parallel()
	eachPlatformDriver(t, func(t *testing.T, pg bool) {
		e := platformPublicFixture(t, pg)
		e.enable(t)
		auth, err := e.authority()
		if err != nil {
			t.Fatal(err)
		}
		route, _ := auth.route("history")
		target := appplatform.PublicRequestTarget(route.Path, "from=1")
		var raw, sig []byte
		var token string
		var command control.WritableBackendRequestCommand
		a, err := e.s.withPlatformWritableBackendAccess(context.Background(), platformActorSession{anonymousPublic: true}, e.id, func(a platformWritableBackendAccess, seed []byte) error {
			var err error
			raw, sig, token, command, err = signPlatformWritableBackendRequest(a, seed, "GET", target, nil, "fixture-boot", platformPublicAssertion(auth, route, "from=1"))
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		defer a.Close()
		if !appplatform.IsPublicAssertion(token) || command.AssertionSHA256 != platformDigest([]byte(token)) || command.Method != "GET" || command.Path != target || command.BodySHA256 != platformDigest(nil) {
			t.Fatal("command does not bind the public token", command)
		}
		v, err := control.VerifyWritableBackendRequest(raw, sig, a.intent.start.writer.authority, time.Now().UnixMilli())
		if err != nil || control.ValidateWritableBackendRequestData(v, nil, token) != nil {
			t.Fatal("the runner refused the public request", err)
		}
		signer, err := a.intent.registration.Signer("fixture-boot")
		if err != nil {
			t.Fatal(err)
		}
		claims, err := appplatform.VerifyPublicAssertion(signer, token, target, time.Now())
		if err != nil || claims.Route != "history" || claims.Generation != a.intent.generation || claims.Method != "GET" {
			t.Fatal(claims, err)
		}
		// The same token cannot carry a write or another path through the runner's checks.
		if control.ValidateWritableBackendRequestData(v, []byte(`{}`), token) == nil {
			t.Fatal("a body rode a public token")
		}
		// A viewer assertion is signed differently, never public.
		viewerToken, err := signer.Sign("GET", target, nil, a.viewer, time.Now())
		if err == nil && appplatform.IsPublicAssertion(viewerToken) {
			t.Fatal("a viewer token looks public")
		}
	})
}
