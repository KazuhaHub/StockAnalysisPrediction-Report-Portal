//go:build !linux

package transport

import (
	"context"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/broker"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
)

func RequestControllerNetworkStream(
	context.Context,
	string,
	uint32,
	uint32,
	[]byte,
	[]byte,
	[]byte,
	control.Authority,
	NetworkApproval,
	broker.HTTPStreamCallbacks,
) (NetworkStreamObservation, error) {
	return NetworkStreamObservation{}, broker.ErrUnavailable
}
