package transport

import (
	"errors"
	"sync/atomic"
)

// ErrRunnerUnreachable marks a failure that happened before any byte reached the runner:
// the control socket is absent, refuses connections or did not accept in time. Nothing was
// sent, so the same call can be repeated safely, and it says nothing about the runner's
// verdict on any operation. It always travels together with the error it refines
// (ErrRefused or ErrUnsafeEndpoint), so existing checks keep working.
var ErrRunnerUnreachable = errors.New("runner control endpoint unreachable")

var unreachableDials atomic.Uint64

// UnreachableDials counts the connection attempts that failed with ErrRunnerUnreachable in
// this process. A caller whose error chain lost the sentinel can compare the count before
// and after an attempt.
func UnreachableDials() uint64 { return unreachableDials.Load() }

func noteUnreachable() { unreachableDials.Add(1) }
