//go:build !linux

package app

import (
	"context"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/transport"
)

func platformSendImageBlob(context.Context, platformRuntimeEndpoint, platformImageBlobCall) (transport.ImageBlobResult, error) {
	return transport.ImageBlobResult{}, errAppPlatformRuntimeConflict
}
