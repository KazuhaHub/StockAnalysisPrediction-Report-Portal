//go:build linux

package app

import (
	"context"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/transport"
)

// platformSendImageBlob opens one blob session on the protected runner endpoint.
func platformSendImageBlob(ctx context.Context, endpoint platformRuntimeEndpoint, call platformImageBlobCall) (transport.ImageBlobResult, error) {
	return transport.SendControllerImageBlob(ctx, endpoint.path, endpoint.controllerUID, endpoint.controllerGID, call.raw, call.signature, call.authority, call.prove, call.body)
}
