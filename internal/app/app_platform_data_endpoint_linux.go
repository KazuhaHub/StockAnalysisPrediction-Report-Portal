//go:build linux

package app

import (
	"context"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/transport"
)

func platformReadDataReceipt(ctx context.Context, endpoint platformRuntimeEndpoint, intent platformDataIntent) (transport.DataReceiptObservation, error) {
	return transport.ReadControllerDataReceipt(ctx, endpoint.path, endpoint.controllerUID, endpoint.controllerGID, intent.raw, intent.signature, intent.authority)
}
