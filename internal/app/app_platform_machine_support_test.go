package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/transport"
)

// The capability is offered only against a runner that says it forwards machine assertions: an older
// one is refused at enable time, and a request is answered 503 before anything is relayed to it.
func TestMachineReadNeedsARunnerThatForwardsMachineAssertions(t *testing.T) {
	t.Parallel()
	eachPlatformDriver(t, func(t *testing.T, pg bool) {
		e := platformMachineFixture(t, pg)
		e.s.platformBackendEndpoint.Store(&platformRuntimeEndpoint{path: "/run/fixture/control.sock", controllerUID: 1000, controllerGID: 1000})
		t.Cleanup(func() { e.s.platformBackendEndpoint.Store(nil) })
		var asked int
		answer := func(context.Context, platformRuntimeEndpoint) (transport.MachineSupport, error) {
			asked++
			return transport.MachineSupport{}, transport.ErrMachineSupportUnsupported
		}
		e.s.platformMachineSupportQuery = answer
		if got := e.preview(t).RunnerSupport; got != "unsupported" {
			t.Fatal("preview says", got)
		}
		if w := e.enableWith(t, []string{"bars"}, 5, 4); w.Code != 409 || scalar[int](t, e.s.st, `SELECT COUNT(*) FROM app_platform_capability_grants WHERE capability='machine.read' AND granted=1`) != 0 {
			t.Fatal("enabled against a runner without support", w.Code, w.Body.String())
		}
		// A runner that upgrades is noticed once the short memory of the answer has passed.
		e.s.platformMachine.mu.Lock()
		e.s.platformMachine.support.until = time.Now().Add(-time.Second)
		e.s.platformMachine.mu.Unlock()
		e.s.platformMachineSupportQuery = func(context.Context, platformRuntimeEndpoint) (transport.MachineSupport, error) {
			return transport.NewMachineSupport(), nil
		}
		e.enable(t)
		made := e.simple(t)
		if w := e.data(made.Token, "bars", "", ""); w.Code != 200 || e.calls.Load() != 1 {
			t.Fatal("supported runner", w.Code)
		}
		// The runner is downgraded: requests are 503 and none reaches it.
		e.s.platformMachine.mu.Lock()
		e.s.platformMachine.support.until = time.Now().Add(-time.Second)
		e.s.platformMachine.mu.Unlock()
		e.s.platformMachineSupportQuery = func(context.Context, platformRuntimeEndpoint) (transport.MachineSupport, error) {
			return transport.MachineSupport{}, errors.New("closed")
		}
		if w := e.data(made.Token, "bars", "", "10.90.0.1:1"); w.Code != 503 || e.calls.Load() != 1 {
			t.Fatal("unsupported runner", w.Code, e.calls.Load())
		}
		if asked != 1 {
			t.Fatal("the answer was not remembered", asked)
		}
	})
}
