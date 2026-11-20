//go:build !linux

package app

import (
	"context"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/transport"
)

// There is no runner to talk to off Linux: every query says so, and the
// coordinator then opens nothing.
type platformServiceEgressRunner struct{}

func (platformServiceEgressRunner) Query(context.Context, platformRuntimeEndpoint) (transport.ServiceEgressProfile, error) {
	return transport.ServiceEgressProfile{}, transport.ErrServiceEgressUnsupported
}

func (platformServiceEgressRunner) QueryProxy(context.Context, platformRuntimeEndpoint) (transport.ProxyProfile, error) {
	return transport.ProxyProfile{}, transport.ErrProxyUnsupported
}

func (platformServiceEgressRunner) Open(context.Context, platformRuntimeEndpoint, []byte, []byte, control.Authority, []byte) (*transport.EgressMux, error) {
	return nil, transport.ErrServiceEgressUnsupported
}

func (platformServiceEgressRunner) QueryPostgres(context.Context, platformRuntimeEndpoint) (transport.PostgresProbeProfile, error) {
	return transport.PostgresProbeProfile{}, transport.ErrPostgresProbeUnsupported
}

func newPlatformServiceEgressRunner() platformServiceEgressTransport {
	return platformServiceEgressRunner{}
}
