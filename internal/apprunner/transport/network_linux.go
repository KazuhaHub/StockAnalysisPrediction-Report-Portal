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
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/journal"
)

// ListenNetworkController explicitly enables only the experimental signed HTTPS
// path on an existing root-owned endpoint. It creates no users, directories,
// enrollment, grant, container or production startup hook. The owner retains
// one server/pool across invocations; applications cannot select its limits.
func ListenNetworkController(ctx context.Context, path string, uid, gid uint32, store *journal.Store, active, entries int) (*Server, error) {
	if ctx == nil || ctx.Err() != nil || os.Geteuid() != 0 || store == nil {
		return nil, ErrUnsafeEndpoint
	}
	if _, err := store.ControllerDeployment(ctx); err != nil {
		return nil, ErrUnsafeEndpoint
	}
	provider, err := newNetworkProvider(store, active, entries)
	if err != nil {
		return nil, ErrUnsafeEndpoint
	}
	server, err := ListenController(path, uid, gid, store)
	if err != nil {
		return nil, err
	}
	server.network = provider
	return server, nil
}

// RequestControllerNetwork authenticates root socket ownership/peer before any
// packet is sent. approve must resolve current Portal SQL/session/OU/grant facts
// on every challenge before signing; neither a browser nor an app supplies it.
// The returned observation still requires current Portal post-IO validation.
func RequestControllerNetwork(ctx context.Context, path string, uid, gid uint32, raw, signature, body []byte, a control.Authority, approve NetworkApproval) (NetworkObservation, error) {
	var zero NetworkObservation
	if ctx == nil || ctx.Err() != nil || approve == nil || !validController(uid, gid) || uint32(os.Geteuid()) != uid {
		return zero, ErrUnsafeEndpoint
	}
	if len(raw) == 0 || len(raw) > control.MaxCommandBytes || len(signature) != 64 || len(body) > 65536 || len(a.PublicKey) != 32 {
		return zero, broker.ErrDenied
	}
	raw, signature, body = bytes.Clone(raw), bytes.Clone(signature), bytes.Clone(body)
	a.PublicKey = bytes.Clone(a.PublicKey)
	defer clear(body)
	if _, err := control.VerifyNetworkHTTPRequest(raw, signature, a, time.Now().UnixMilli()); err != nil {
		return zero, broker.ErrDenied
	}
	ctx, cancel := context.WithTimeout(ctx, networkSessionTimeout)
	defer cancel()
	conn, err := connectEndpoint(ctx, path, controllerProfile(gid))
	if err != nil {
		return zero, err
	}
	defer conn.Close()
	response, err := exchangeNetworkSession(ctx, conn, raw, signature, body, a, approve)
	if err != nil {
		return zero, err
	}
	digest := sha256.Sum256(raw)
	return NetworkObservation{response: response, authority: a, endpoint: CandidateReceiptEndpoint{Path: path, RunnerUID: 0, ControllerUID: uid, ControllerGID: gid, RootController: true}, requestSHA: hex.EncodeToString(digest[:]), receivedAt: time.Now()}, nil
}
