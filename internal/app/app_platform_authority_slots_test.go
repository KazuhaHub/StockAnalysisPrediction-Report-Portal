package app

import (
	"testing"
	"time"
)

// Session-less authority reads are capped in concurrency: with every slot taken a new unauthenticated
// request is a 503 and reads nothing; once a slot is free a signed request succeeds.
func TestIngressAuthorityReadsAreCapped(t *testing.T) {
	eachPlatformDriver(t, func(t *testing.T, pg bool) {
		e := platformIngressFixture(t, pg)
		e.enable(t)
		e.bind(t, "api", "heartbeat", ingressSecret("a"))
		for i := 0; i < platformAuthorityConcurrency; i++ {
			if !e.s.platformIngress.slots.acquire() {
				t.Fatal("the cap is lower than", platformAuthorityConcurrency)
			}
		}
		reads, opens := e.s.platformIngressReads.Load(), platformCredentialOpenCount.Load()
		w := e.send(t, func(m *ingressSend) { m.unsigned = false })
		if w.Code != 503 || w.Header().Get("Retry-After") == "" {
			t.Fatal("with the slots full:", w.Code)
		}
		if e.s.platformIngressReads.Load() != reads || platformCredentialOpenCount.Load() != opens || e.forwarded() != 0 {
			t.Fatal("a refused request read the authority, opened a credential or reached the backend")
		}
		// Static failures and the source ceiling never needed a slot.
		if w := e.send(t, withBody("nope")); w.Code != 401 {
			t.Fatal("static failure with the slots full", w.Code)
		}
		for i := 0; i < platformAuthorityConcurrency; i++ {
			e.s.platformIngress.slots.release()
		}
		if w := e.send(t); w.Code != 202 {
			t.Fatal("after the slots were released:", w.Code)
		}
		if e.s.platformIngressReads.Load() != reads+1 {
			t.Fatal("reads", e.s.platformIngressReads.Load()-reads)
		}
	})
}

func TestPublicAuthorityReadsAreCapped(t *testing.T) {
	eachPlatformDriver(t, func(t *testing.T, pg bool) {
		e := platformPublicFixture(t, pg)
		e.enable(t)
		for i := 0; i < platformAuthorityConcurrency; i++ {
			if !e.s.platformPublic.slots.acquire() {
				t.Fatal("the cap is lower than", platformAuthorityConcurrency)
			}
		}
		reads, calls := e.s.platformPublicLightReads.Load(), e.calls.Load()
		w := e.data("status", "")
		if w.Code != 503 || w.Header().Get("Retry-After") == "" {
			t.Fatal("with the slots full:", w.Code, w.Body.String())
		}
		if e.s.platformPublicLightReads.Load() != reads || e.calls.Load() != calls {
			t.Fatal("a refused request read the authority or reached the backend")
		}
		for i := 0; i < platformAuthorityConcurrency; i++ {
			e.s.platformPublic.slots.release()
		}
		if w := e.data("status", ""); w.Code != 200 {
			t.Fatal("after the slots were released:", w.Code)
		}
	})
}

// A request the installation budget refuses must not take a nonce-cache entry.
func TestIngressBudgetRefusalTakesNoNonce(t *testing.T) {
	eachPlatformDriver(t, func(t *testing.T, pg bool) {
		e := platformIngressFixture(t, pg)
		e.enable(t)
		e.bind(t, "api", "heartbeat", ingressSecret("a"))
		for i := 0; i < platformIngressInstallationPerMinute; i++ {
			e.s.platformIngress.installations.allow("installation:"+e.id, platformIngressInstallationPerMinute, time.Minute, time.Now())
		}
		before := e.s.platformIngress.nonceCount(e.id)
		if w := e.send(t, withNonce("budget-nonce-00001")); w.Code != 429 {
			t.Fatal(w.Code)
		}
		if e.s.platformIngress.nonceCount(e.id) != before {
			t.Fatal("a budget-refused request took a nonce-cache entry")
		}
	})
}
