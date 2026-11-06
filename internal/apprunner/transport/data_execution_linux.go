//go:build linux

package transport

import (
	"context"
	"net"
	"os"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/data"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/journal"
)

const dataExecutionQueryMagic = "PRCE0001"
const dataExecutionReplyMagic = "PRCF0001"

type dataExecutionProvider struct {
	store *journal.Store
	root  *data.Root
	pin   data.RootIdentity
}

// ListenDataController explicitly enables signed data preparation on the
// provisioned root endpoint. It creates no OS identity, root, trust or journal.
// The caller owns the concrete root/store until Serve has stopped all sessions.
func ListenDataController(ctx context.Context, path string, controllerUID, controllerGID uint32, store *journal.Store, root *data.Root, pin data.RootIdentity) (*Server, error) {
	if os.Geteuid() != 0 || store == nil || root == nil || ctx.Err() != nil {
		return nil, ErrUnsafeEndpoint
	}
	actual, err := root.Identity(ctx)
	if err != nil || actual != pin {
		return nil, ErrUnsafeEndpoint
	}
	server, err := ListenController(path, controllerUID, controllerGID, store)
	if err != nil {
		return nil, err
	}
	server.dataExecution = &dataExecutionProvider{store: store, root: root, pin: pin}
	return server, nil
}
func (s *Server) handleDataExecution(ctx context.Context, conn *net.UnixConn) {
	if s.dataExecution == nil {
		return
	}
	s.handleDataResponse(ctx, conn, true)
}

// PrepareControllerData explicitly requests execution/reconciliation, returning
// only authenticated matching receipt evidence. A timeout/lost reply is an
// uncertain result: retain the exact effect and use fresh authority to inspect.
// Ordinary admission and read-only receipt queries never invoke this operation.
func PrepareControllerData(ctx context.Context, path string, controllerUID, controllerGID uint32, raw, signature []byte, authority control.Authority) (DataReceiptObservation, error) {
	if !validController(controllerUID, controllerGID) || uint32(os.Geteuid()) != controllerUID {
		return DataReceiptObservation{}, ErrUnsafeEndpoint
	}
	return readDataResponse(ctx, path, controllerProfile(controllerGID), raw, signature, authority, true)
}
