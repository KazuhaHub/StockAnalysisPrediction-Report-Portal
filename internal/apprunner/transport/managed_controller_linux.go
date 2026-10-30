//go:build linux

package transport

import (
	"context"
	"os"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/data"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/driver"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/journal"
)

// ListenManagedController joins data preparation and candidate execution on one
// provisioned root/controller endpoint. Both channels use one shared journal and
// the exact same concrete data provider/root pin. Existing narrow constructors
// retain their original capability sets. No CLI listener starts automatically.
// The caller owns all concrete providers until Serve has stopped its sessions.
func ListenManagedController(ctx context.Context, path string, controllerUID, controllerGID uint32, store *journal.Store, root *data.Root, pin data.RootIdentity, native *driver.CandidateRuntime) (*Server, error) {
	if os.Geteuid() != 0 || ctx.Err() != nil || store == nil || root == nil || native == nil {
		return nil, ErrUnsafeEndpoint
	}
	if err := native.CheckSharedRootConfiguration(ctx, root, pin); err != nil {
		return nil, ErrUnsafeEndpoint
	}
	server, err := ListenDataController(ctx, path, controllerUID, controllerGID, store, root, pin)
	if err != nil {
		return nil, err
	}
	server.candidateExecution = &candidateExecutionProvider{store: store, native: native}
	return server, nil
}

// ListenNetworkManagedController explicitly combines data, candidate and one
// bounded network pool on the same checked root, journal and protected endpoint.
// The caller owns all providers until Serve stops; enrollment is never created
// here. Existing constructors retain their original narrower capability sets.
func ListenNetworkManagedController(ctx context.Context, path string, uid, gid uint32, store *journal.Store, root *data.Root, pin data.RootIdentity, native *driver.CandidateRuntime, active, entries int) (*Server, error) {
	if ctx == nil || ctx.Err() != nil || os.Geteuid() != 0 || store == nil || root == nil || native == nil {
		return nil, ErrUnsafeEndpoint
	}
	if _, err := store.ControllerDeployment(ctx); err != nil {
		return nil, ErrUnsafeEndpoint
	}
	network, err := newNetworkProvider(store, active, entries)
	if err != nil {
		return nil, ErrUnsafeEndpoint
	}
	server, err := ListenManagedController(ctx, path, uid, gid, store, root, pin, native)
	if err != nil {
		return nil, err
	}
	server.network = network
	return server, nil
}
