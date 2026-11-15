package transport

import (
	"context"
	"errors"
	"sync/atomic"
)

// ErrRunnerUnreachable marks a failure that happened before any byte reached the runner:
// the control socket is absent, refuses connections or did not accept in time. Nothing was
// sent, so the same call can be repeated safely, and it says nothing about the runner's
// verdict on any operation. It always travels together with the error it refines
// (ErrRefused or ErrUnsafeEndpoint), so existing checks keep working. A dial that fails only
// because the caller's context is already finished is not tagged: that is the caller's
// budget running out, not a statement about the runner.
var ErrRunnerUnreachable = errors.New("runner control endpoint unreachable")

// UnreachableWitness records, for the calls made under one context, whether the most recent
// connection attempt failed before reaching the runner. It lets a caller classify a failure
// whose error was wrapped without %w by an intermediate layer. It is scoped to the context it
// was attached to: dials made under other contexts (other goroutines, other requests) never
// touch it.
type UnreachableWitness struct{ last atomic.Bool }

// Seen reports whether the latest connection attempt under the witness's context failed as
// unreachable.
func (w *UnreachableWitness) Seen() bool { return w != nil && w.last.Load() }

type witnessKey struct{}

// Note records the outcome of one connection attempt: true when it failed before reaching the runner.
func (w *UnreachableWitness) Note(unreachable bool) {
	if w != nil {
		w.last.Store(unreachable)
	}
}

// WithUnreachableWitness returns a context whose connection attempts report to the witness.
func WithUnreachableWitness(ctx context.Context) (context.Context, *UnreachableWitness) {
	w := &UnreachableWitness{}
	return context.WithValue(ctx, witnessKey{}, w), w
}

func witnessOf(ctx context.Context) *UnreachableWitness {
	w, _ := ctx.Value(witnessKey{}).(*UnreachableWitness)
	return w
}

func noteDial(ctx context.Context, unreachable bool) {
	witnessOf(ctx).Note(unreachable)
}

// UnreachableSeen reports whether the latest connection attempt under ctx's witness failed as
// unreachable. It is false for a context without a witness.
func UnreachableSeen(ctx context.Context) bool { return witnessOf(ctx).Seen() }
