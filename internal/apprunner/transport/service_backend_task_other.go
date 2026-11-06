//go:build !linux

package transport

import (
	"context"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/driver"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/journal"
)

type ServiceBackendTaskObservation struct{}

func (ServiceBackendTaskObservation) Result() journal.ServiceBackendTaskResult {
	return journal.ServiceBackendTaskResult{}
}

func RequestControllerServiceTask(
	context.Context,
	string,
	uint32,
	uint32,
	[]byte,
	[]byte,
	control.Authority,
	[]byte,
	[]byte,
) (ServiceBackendTaskObservation, error) {
	return ServiceBackendTaskObservation{}, driver.ErrUnsupported
}
