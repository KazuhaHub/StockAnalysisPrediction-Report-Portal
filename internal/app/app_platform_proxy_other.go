//go:build !linux

package app

import (
	"context"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/transport"
)

func platformQueryProxy(context.Context, platformRuntimeEndpoint) (transport.ProxyProfile, error) {
	return transport.ProxyProfile{}, transport.ErrProxyUnsupported
}
