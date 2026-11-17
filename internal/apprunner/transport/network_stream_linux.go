//go:build linux

package transport

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/broker"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
)

// RequestControllerNetworkStream authenticates root socket ownership and peer
// identity before sending a stream command. callbacks receive data only after
// each fresh Portal authority challenge succeeds.
func RequestControllerNetworkStream(
	ctx context.Context,
	path string,
	uid, gid uint32,
	raw, signature, body []byte,
	a control.Authority,
	approve NetworkApproval,
	callbacks broker.HTTPStreamCallbacks,
) (NetworkStreamObservation, error) {
	var zero NetworkStreamObservation
	if ctx == nil || ctx.Err() != nil || approve == nil || callbacks.Start == nil || !validController(uid, gid) || uint32(os.Geteuid()) != uid {
		return zero, ErrUnsafeEndpoint
	}
	if len(raw) == 0 || len(raw) > control.MaxCommandBytes || len(signature) != 64 || len(body) > 65536 || len(a.PublicKey) != 32 {
		return zero, broker.ErrDenied
	}
	raw, signature, body = bytes.Clone(raw), bytes.Clone(signature), bytes.Clone(body)
	a.PublicKey = bytes.Clone(a.PublicKey)
	defer clear(body)
	if _, err := control.VerifyNetworkHTTPStreamRequest(raw, signature, a, time.Now().UnixMilli()); err != nil {
		return zero, broker.ErrDenied
	}
	ctx, cancel := context.WithTimeout(ctx, networkStreamSessionTimeout)
	defer cancel()
	conn, err := connectEndpoint(ctx, path, controllerProfile(gid))
	if err != nil {
		return zero, err
	}
	defer conn.Close()
	result, err := exchangeNetworkStreamSession(ctx, conn, raw, signature, body, a, approve, callbacks)
	if err != nil {
		return zero, err
	}
	digest := sha256.Sum256(raw)
	return NetworkStreamObservation{
		result: result, authority: a,
		endpoint:   CandidateReceiptEndpoint{Path: path, RunnerUID: 0, ControllerUID: uid, ControllerGID: gid, RootController: true},
		requestSHA: hex.EncodeToString(digest[:]), receivedAt: time.Now(),
	}, nil
}
