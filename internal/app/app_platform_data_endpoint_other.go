//go:build !linux

package app

import (
	"context"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/transport"
)

func platformReadDataReceipt(context.Context, platformRuntimeEndpoint, platformDataIntent) (transport.DataReceiptObservation, error) {
	return transport.DataReceiptObservation{}, errAppPlatformRuntimeConflict
}
