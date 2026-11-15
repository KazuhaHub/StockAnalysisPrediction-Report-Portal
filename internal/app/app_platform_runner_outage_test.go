package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/transport"
)

var errRunnerVerdict = errors.New("runner verdict: refused")

func unreachableErr() error {
	return fmt.Errorf("stage failed: %w", errors.Join(transport.ErrRunnerUnreachable, errors.New("dial unix: connection refused")))
}

type outageClock struct{ t time.Time }

func (c *outageClock) now() time.Time { return c.t }

// outageScheduler returns a scheduler on a queued operation whose runner is simulated: while
// down() is true the stage fails as unreachable and the probe fails; otherwise the stage
// reports its outcome through verdict.
func outageScheduler(t *testing.T, down func() bool, verdict func(platformOperationLease) (platformInstallRun, error)) (*platformInstallScheduler, *Server, string, *outageClock) {
	t.Helper()
	s, _, id := platformInstallQueueFixture(t, false)
	w := platformSchedulerFixture(t, s)
	clock := &outageClock{t: time.Unix(1_800_000_000, 0)}
	w.health.now = clock.now
	w.probe = func(context.Context) error {
		if down() {
			return unreachableErr()
		}
		return nil
	}
	w.runOverride = func(_ context.Context, lease platformOperationLease) (platformInstallRun, error) {
		if down() {
			return platformInstallRun{}, unreachableErr()
		}
		return verdict(lease)
	}
	return w, s, id, clock
}

func quiet(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	return &buf
}

// A runner that is down for 60 seconds, then back: the operation never finishes as review
// and completes once the runner answers.
func TestPlatformSchedulerRunnerOutageDoesNotSpendAttempts(t *testing.T) {
	quiet(t)
	var down = true
	w, s, id, clock := outageScheduler(t, func() bool { return down }, func(lease platformOperationLease) (platformInstallRun, error) {
		done := lease
		done.snapshot.Stage = appplatform.StageComplete
		return platformInstallRun{lease: done}, nil
	})
	start := clock.t
	ticks := 0
	for clock.t.Sub(start) < 60*time.Second {
		if err := w.tick(context.Background()); err == nil {
			t.Fatal("a tick against a down runner reported success")
		}
		ticks++
		if w.attempts != 0 || w.active.id == "" {
			t.Fatal("an unreachable runner spent an attempt or dropped the operation", w.attempts)
		}
		if stage := scalar[string](t, s.st, `SELECT stage FROM app_platform_operations WHERE id=?`, id); stage == "recovery_required" {
			t.Fatal("the operation was sent to review during the outage")
		}
		clock.t = clock.t.Add(w.health.delay(w.interval))
	}
	if ticks < 3 || ticks > 40 {
		t.Fatal("backoff produced", ticks, "ticks in 60 s")
	}
	if code := w.currentStatus().code; code != "runner_unreachable" {
		t.Fatal("status during the outage:", code)
	}
	down = false
	if err := w.tick(context.Background()); err != nil {
		t.Fatal("the runner is back and the tick failed:", err)
	}
	if w.currentStatus().code != "complete" || w.active.id != "" || w.health.down {
		t.Fatal("the operation did not complete after the outage", w.currentStatus(), w.health.down)
	}
}

// Only definite verdicts count: unreachable ticks between verdicts neither add to nor reset the budget.
func TestPlatformSchedulerDefiniteVerdictsStillCountAgainstTheBudget(t *testing.T) {
	quiet(t)
	down := false
	w, s, id, clock := outageScheduler(t, func() bool { return down }, func(platformOperationLease) (platformInstallRun, error) {
		return platformInstallRun{}, errRunnerVerdict
	})
	for i := 0; i < platformInstallAttemptLimit; i++ {
		if i == 1 {
			down = true
			for j := 0; j < 12; j++ {
				if w.tick(context.Background()) == nil {
					t.Fatal("down runner reported success")
				}
				clock.t = clock.t.Add(w.health.delay(w.interval))
			}
			if w.attempts != 1 {
				t.Fatal("the outage changed the attempt count:", w.attempts)
			}
			down = false
		}
		if w.tick(context.Background()) == nil && i < platformInstallAttemptLimit-1 {
			t.Fatal("a verdict failure reported success")
		}
	}
	if w.active.id != "" || w.currentStatus().code != "review_required" {
		t.Fatal("three definite verdicts did not end in review", w.currentStatus())
	}
	if stage := scalar[string](t, s.st, `SELECT stage FROM app_platform_operations WHERE id=?`, id); stage != "failed" && stage != "recovery_required" {
		t.Fatal("operation stage", stage)
	}
}

// One line when the runner becomes unreachable, a reminder at most every few minutes, one line
// when it answers again; never a line per tick or per item.
func TestPlatformSchedulerOutageLogsStateChangesOnly(t *testing.T) {
	buf := quiet(t)
	down := true
	w, _, _, clock := outageScheduler(t, func() bool { return down }, func(lease platformOperationLease) (platformInstallRun, error) {
		done := lease
		done.snapshot.Stage = appplatform.StageComplete
		return platformInstallRun{lease: done}, nil
	})
	start := clock.t
	for clock.t.Sub(start) < 22*time.Minute {
		_ = w.tick(context.Background())
		clock.t = clock.t.Add(w.health.delay(w.interval))
	}
	down = false
	if err := w.tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	var unreachable, still, again int
	for _, line := range lines {
		switch {
		case strings.Contains(line, "is unreachable"):
			unreachable++
		case strings.Contains(line, "still unreachable"):
			still++
		case strings.Contains(line, "reachable again"):
			again++
		default:
			t.Fatal("unexpected log line during an outage:", line)
		}
	}
	if unreachable != 1 || again != 1 || still < 3 || still > 5 {
		t.Fatalf("log lines: %d unreachable, %d reminders, %d recovered\n%s", unreachable, still, again, buf.String())
	}
}

// The idle steps (settlements, image reclaim) stay quiet and back the tick off too.
func TestPlatformSchedulerIdleStepsTreatAnUnreachableRunnerAsBackoff(t *testing.T) {
	buf := quiet(t)
	w, _, _, _ := outageScheduler(t, func() bool { return true }, nil)
	if !w.idleUnreachable(unreachableErr()) || !w.health.down {
		t.Fatal("an unreachable error was not recorded")
	}
	if w.idleUnreachable(errRunnerVerdict) {
		t.Fatal("a definite failure was treated as an outage")
	}
	if strings.Count(buf.String(), "\n") != 1 {
		t.Fatal("idle outage logged per call:", buf.String())
	}
}

func TestPlatformRunnerBackoffIsBoundedExponentialWithJitter(t *testing.T) {
	interval := 500 * time.Millisecond
	h := platformRunnerHealth{down: true}
	if (&platformRunnerHealth{}).delay(interval) != interval {
		t.Fatal("a reachable runner changed the tick interval")
	}
	var previousBase time.Duration
	for n := 1; n <= 20; n++ {
		h.consecutive = n
		base := interval
		for i := 1; i < n && base < platformRunnerBackoffMax; i++ {
			base *= 2
		}
		if base > platformRunnerBackoffMax {
			base = platformRunnerBackoffMax
		}
		if base < previousBase {
			t.Fatal("backoff shrank")
		}
		previousBase = base
		for i := 0; i < 50; i++ {
			d := h.delay(interval)
			if d < time.Duration(0.79*float64(base)) || d > platformRunnerBackoffMax || d > time.Duration(1.21*float64(base)) {
				t.Fatalf("delay %s outside the jittered bounds of %s (attempt %d)", d, base, n)
			}
		}
	}
	if previousBase != platformRunnerBackoffMax {
		t.Fatal("backoff never reached its cap", previousBase)
	}
}

func TestPlatformRunnerUnreachableClassification(t *testing.T) {
	if !platformRunnerUnreachable(unreachableErr()) || platformRunnerUnreachable(errRunnerVerdict) || platformRunnerUnreachable(nil) {
		t.Fatal("classification")
	}
}
