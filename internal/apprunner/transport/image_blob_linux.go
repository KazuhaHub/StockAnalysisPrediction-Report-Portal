//go:build linux

package transport

import (
	"context"
	"io"
	"os"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
)

// SendControllerImageBlob opens a blob session on the protected root endpoint, after the same peer and socket
// ownership checks as every other controller exchange, and sends one blob. Any error may follow stored bytes: the
// sender resumes with the offset the runner reports and never needs a new identity.
func SendControllerImageBlob(ctx context.Context, path string, uid, gid uint32, raw, signature []byte, a control.Authority, prove ImageBlobProver, body io.Reader) (ImageBlobResult, error) {
	if ctx == nil || ctx.Err() != nil || !validController(uid, gid) || uint32(os.Geteuid()) != uid {
		return ImageBlobResult{}, ErrUnsafeEndpoint
	}
	conn, err := connectEndpoint(ctx, path, controllerProfile(gid))
	if err != nil {
		return ImageBlobResult{}, err
	}
	defer conn.Close()
	return SendImageBlob(ctx, conn, raw, signature, a, prove, body)
}
