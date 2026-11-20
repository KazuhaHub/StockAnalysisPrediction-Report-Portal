//go:build !linux

package app

import (
	"context"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/transport"
)

func platformQueryMachineSupport(context.Context, platformRuntimeEndpoint) (transport.MachineSupport, error) {
	return transport.MachineSupport{}, transport.ErrMachineSupportUnsupported
}
