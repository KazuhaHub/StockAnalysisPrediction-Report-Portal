package app

import "testing"

// The session-less database reads are capped in concurrency: with every slot taken, a request that
// would need one is a 503 and reads nothing; requests that fail before any read are still 401; once a
// slot is free a valid request succeeds.
func TestMachineAuthorityReadsAreCapped(t *testing.T) {
	t.Parallel()
	eachPlatformDriver(t, func(t *testing.T, pg bool) {
		e := platformMachineFixture(t, pg)
		e.enable(t)
		made := e.simple(t)
		for i := 0; i < platformAuthorityConcurrency; i++ {
			if !e.s.platformMachine.slots.acquire() {
				t.Fatal("the cap is lower than", platformAuthorityConcurrency)
			}
		}
		reads, calls := e.s.platformMachineReads.Load(), e.calls.Load()
		w := e.data(made.Token, "bars", "", "10.95.0.1:1")
		if w.Code != 503 || w.Header().Get("Retry-After") == "" {
			t.Fatal("with the slots full:", w.Code, w.Body.String())
		}
		if e.s.platformMachineReads.Load() != reads || e.calls.Load() != calls {
			t.Fatal("a refused request read the authority or reached the backend")
		}
		// A request that fails before any read needs no slot.
		if w := e.get("/machine/apps/"+e.id+"/data/bars", "10.95.0.2:1", nil); w.Code != 401 {
			t.Fatal("static failure with the slots full", w.Code)
		}
		for i := 0; i < platformAuthorityConcurrency; i++ {
			e.s.platformMachine.slots.release()
		}
		if w := e.data(made.Token, "bars", "", "10.95.0.3:1"); w.Code != 200 {
			t.Fatal("after the slots were released:", w.Code)
		}
		if e.s.platformMachineReads.Load() != reads+1 {
			t.Fatal("reads", e.s.platformMachineReads.Load()-reads)
		}
	})
}
