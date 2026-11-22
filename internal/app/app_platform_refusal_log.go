package app

import (
	"context"
	"errors"
	"log"
	"net/http"
	"sync"
	"time"
)

// The launch, document and backend routes answer every refusal with the same
// opaque 404, so an operator could not tell a revoked grant from a broken
// identity derivation. The wrapped reason is logged server side only, once per
// distinct reason per interval, and never sent to the client.
const platformRefusalLogInterval = 10 * time.Minute
const platformRefusalLogMax = 64

var platformRefusals = struct {
	sync.Mutex
	last map[string]time.Time
}{last: map[string]time.Time{}}

// logPlatformRefusal records why an app-platform read was refused. Cancellation
// and deadline errors are routine and skipped.
func logPlatformRefusal(r *http.Request, err error) {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return
	}
	text := err.Error()
	now := time.Now()
	platformRefusals.Lock()
	if at, ok := platformRefusals.last[text]; ok && now.Sub(at) < platformRefusalLogInterval {
		platformRefusals.Unlock()
		return
	}
	if len(platformRefusals.last) >= platformRefusalLogMax {
		clear(platformRefusals.last)
	}
	platformRefusals.last[text] = now
	platformRefusals.Unlock()
	log.Printf("app platform: %s %s refused: %s (repeats are not logged for %s)", r.Method, r.PathValue("installation"), text, platformRefusalLogInterval)
}
