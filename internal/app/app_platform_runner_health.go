package app

import (
	"context"
	"errors"
	"log"
	"math/rand/v2"
	"net"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/transport"
)

// A runner that cannot be reached is not a verdict on any operation. The operation engine
// therefore never counts such a failure against an operation's attempt budget: it backs the
// whole tick off, keeps the operation's lease alive, and resumes when the runner answers. Only
// definite runner verdicts (and errors that happened after the runner was reached) count.

const (
	platformRunnerBackoffMax = 30 * time.Second
	// A reminder that the runner is still down is logged at most this often.
	platformRunnerReminderEvery = 5 * time.Minute
)

// platformRunnerUnreachable reports a failure that happened before anything reached the runner.
func platformRunnerUnreachable(err error) bool {
	return err != nil && errors.Is(err, transport.ErrRunnerUnreachable)
}

// platformRunnerDown is platformRunnerUnreachable for a caller that holds the attempt's context: it
// also consults the context's witness, which survives an intermediate layer wrapping the error
// without %w. A finished context is never an outage.
func platformRunnerDown(ctx context.Context, err error) bool {
	if err == nil || ctx.Err() != nil {
		return false
	}
	return platformRunnerUnreachable(err) || transport.UnreachableSeen(ctx)
}

// platformRunnerHealth is the engine's view of its runner: down since when, how many ticks in a
// row it stayed down, and when it last said so in the log. It is used from one goroutine (the
// scheduler's tick).
type platformRunnerHealth struct {
	down         bool
	since        time.Time
	lastNotice   time.Time
	consecutive  int
	now          func() time.Time
	reminderEach time.Duration
}

func (h *platformRunnerHealth) clock() time.Time {
	if h.now != nil {
		return h.now()
	}
	return time.Now()
}

func (h *platformRunnerHealth) reminder() time.Duration {
	if h.reminderEach > 0 {
		return h.reminderEach
	}
	return platformRunnerReminderEvery
}

// unreachable records one more unreachable observation and logs only state changes: one line when
// the runner first becomes unreachable and a reminder at most every few minutes.
func (h *platformRunnerHealth) unreachable(who string) {
	now := h.clock()
	h.consecutive++
	if !h.down {
		h.down, h.since, h.lastNotice = true, now, now
		log.Printf("%s: application runner is unreachable; backing off until it answers", who)
		return
	}
	if now.Sub(h.lastNotice) >= h.reminder() {
		h.lastNotice = now
		log.Printf("%s: application runner is still unreachable after %s", who, now.Sub(h.since).Round(time.Second))
	}
}

// reachable records that the runner answered; it logs once if it had been down.
func (h *platformRunnerHealth) reachable(who string) {
	if !h.down {
		return
	}
	log.Printf("%s: application runner is reachable again after %s", who, h.clock().Sub(h.since).Round(time.Second))
	*h = platformRunnerHealth{now: h.now, reminderEach: h.reminderEach}
}

// delay is how long the next tick waits: the scheduler's interval while the runner answers, and
// a bounded exponential backoff (the interval doubling up to 30 s, with +-20% jitter) while it
// is down.
func (h *platformRunnerHealth) delay(interval time.Duration) time.Duration {
	if !h.down {
		return interval
	}
	d := interval
	for i := 1; i < h.consecutive && d < platformRunnerBackoffMax; i++ {
		d *= 2
	}
	if d > platformRunnerBackoffMax {
		d = platformRunnerBackoffMax
	}
	jitter := 0.8 + 0.4*rand.Float64()
	d = time.Duration(float64(d) * jitter)
	if d > platformRunnerBackoffMax {
		d = platformRunnerBackoffMax
	}
	return d
}

// probePlatformRunner opens and closes one connection to the control socket. It sends nothing.
func probePlatformRunner(ctx context.Context, endpoint platformRuntimeEndpoint) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	conn, err := (&net.Dialer{}).DialContext(ctx, "unix", endpoint.path)
	if err != nil {
		return errors.Join(transport.ErrRunnerUnreachable, err)
	}
	return conn.Close()
}
