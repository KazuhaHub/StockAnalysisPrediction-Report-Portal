package service

import (
	"context"
	"errors"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

// ErrProxyPinsNetns is why the runner refuses to start when proxy_pins is set and the container daemon is not
// identified as being in the runner's network namespace (doc 186 section 3). A pin is an address of the runner's
// own namespace, so dialling it from any other would reach nothing or the wrong thing.
var ErrProxyPinsNetns = errors.New("proxy_pins_netns")

// CheckProxyPinsNamespace asks check whether the daemon shares the runner's network namespace, but only when
// the runner holds pins. check is the namespace comparison itself; a nil check, and any error from it
// (including a namespace file that cannot be read), refuses. An empty allowlist starts as it always has.
func CheckProxyPinsNamespace(ctx context.Context, pins []appplatform.ProxyPin, check func(context.Context) error) error {
	if len(pins) == 0 {
		return nil
	}
	if check == nil || check(ctx) != nil {
		return ErrProxyPinsNetns
	}
	return nil
}
