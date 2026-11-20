//go:build !linux

package app

import (
	"context"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/transport"
)

func platformQueryProxy(context.Context, platformRuntimeEndpoint) (transport.ProxyRunnerState, error) {
	return transport.ProxyRunnerState{}, transport.ErrProxyUnsupported
}
