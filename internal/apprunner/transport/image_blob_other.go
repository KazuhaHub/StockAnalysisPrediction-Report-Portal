//go:build !linux

package transport

import (
	"context"
	"io"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
)

func SendControllerImageBlob(context.Context, string, uint32, uint32, []byte, []byte, control.Authority, ImageBlobProver, io.Reader) (ImageBlobResult, error) {
	return ImageBlobResult{}, ErrImageBlobSession
}
