//go:build !linux

package transport

import (
	"context"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/driver"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/journal"
)

func RequestUpdateStartControllerBackend(context.Context, string, uint32, uint32, []byte, []byte, control.Authority, []byte, journal.UpdateStartRegistrationReceipt, []byte, string) (UpdateStartBackendRequestObservation, error) {
	return UpdateStartBackendRequestObservation{}, driver.ErrContainerUncertain
}
