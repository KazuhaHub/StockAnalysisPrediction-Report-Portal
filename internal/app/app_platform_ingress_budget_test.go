package app

import (
	"fmt"
	"testing"
	"time"
)

// The per-installation budget is spent only by requests that passed the signature check, so a flood
// of unauthenticated requests from several sources cannot starve the real senders of an installation.
func TestIngressUnauthenticatedFloodDoesNotSpendTheInstallationBudget(t *testing.T) {
	eachPlatformDriver(t, func(t *testing.T, pg bool) {
		e := platformIngressFixture(t, pg)
		e.enable(t)
		e.bind(t, "api", "heartbeat", ingressSecret("a"))
		// 40 sources, each well inside the per-client ceiling, send 20 junk signatures: 800 requests,
		// more than the installation's 600 a minute.
		for source := 0; source < 40; source++ {
			for i := 0; i < 20; i++ {
				w := e.send(t, withRemote(fmt.Sprintf("198.51.100.%d:1", source+1)), func(m *ingressSend) { m.secret = ingressSecret("z") })
				if w.Code != 401 || w.Body.String() != ingressDenied {
					t.Fatal("junk signature", source, i, w.Code)
				}
			}
		}
		if w := e.send(t, withRemote("203.0.113.77:1")); w.Code != 202 {
			t.Fatal("a correctly signed request was refused after an unauthenticated flood:", w.Code)
		}
		// The budget is still real for what is authenticated: once 600 verified requests are counted, the next
		// verified one is a 429 and does not reach the backend.
		for i := 0; i < platformIngressInstallationPerMinute; i++ {
			e.s.platformIngress.installations.allow("installation:"+e.id, platformIngressInstallationPerMinute, time.Minute, time.Now())
		}
		forwards := e.forwarded()
		if w := e.send(t, withRemote("203.0.113.78:1")); w.Code != 429 || w.Header().Get("Retry-After") == "" || e.forwarded() != forwards {
			t.Fatal("per-installation budget of verified requests", w.Code)
		}
	})
}

// A grant POST, including a no-op re-save, does not forget the nonces already seen.
func TestIngressGrantSaveKeepsTheNonceCache(t *testing.T) {
	eachPlatformDriver(t, func(t *testing.T, pg bool) {
		e := platformIngressFixture(t, pg)
		e.enable(t)
		e.bind(t, "api", "heartbeat", ingressSecret("a"))
		if w := e.send(t, withNonce("kept-nonce-000001")); w.Code != 202 {
			t.Fatal(w.Code)
		}
		e.enable(t) // already granted: a no-op re-save
		if w := e.send(t, withNonce("kept-nonce-000001")); w.Code != 401 {
			t.Fatal("a re-save emptied the nonce cache:", w.Code)
		}
	})
}
