//go:build linux

package app

import (
	"context"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/transport"
)

// platformServiceEgressRunner is the controller side of the runner's endpoint:
// the capability query (an older runner answers it with an end of stream, which
// is "no egress") and the session open.
type platformServiceEgressRunner struct{}

func (platformServiceEgressRunner) Query(ctx context.Context, e platformRuntimeEndpoint) (transport.ServiceEgressProfile, error) {
	return transport.QueryControllerServiceEgress(ctx, e.path, e.controllerUID, e.controllerGID)
}

func (platformServiceEgressRunner) QueryProxy(ctx context.Context, e platformRuntimeEndpoint) (transport.ProxyProfile, error) {
	return transport.QueryControllerProxy(ctx, e.path, e.controllerUID, e.controllerGID)
}

func (platformServiceEgressRunner) Open(ctx context.Context, e platformRuntimeEndpoint, raw, signature []byte, a control.Authority, descriptor []byte) (*transport.EgressMux, error) {
	return transport.OpenControllerServiceEgress(ctx, e.path, e.controllerUID, e.controllerGID, raw, signature, a, descriptor)
}

// QueryPostgres asks the runner whether it serves the PostgreSQL probe (doc 178).
func (platformServiceEgressRunner) QueryPostgres(ctx context.Context, e platformRuntimeEndpoint) (transport.PostgresProbeProfile, error) {
	return transport.QueryControllerPostgresProbe(ctx, e.path, e.controllerUID, e.controllerGID)
}

func newPlatformServiceEgressRunner() platformServiceEgressTransport {
	return platformServiceEgressRunner{}
}
