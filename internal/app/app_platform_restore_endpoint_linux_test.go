//go:build linux

package app

import (
	"context"
	"errors"
	"testing"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/transport"
)

// A restore read-back is sent with the signature made under the read purpose, never the execute one.
func TestRestoreEndpointReadUsesReadSignature(t *testing.T) {
	endpoint := platformRuntimeEndpoint{path: "/nonexistent/restore.sock", controllerUID: 1000, controllerGID: 1001}
	execute, read := make([]byte, 64), make([]byte, 64)
	execute[0], read[0] = 1, 2
	intent := platformRestoreIntent{raw: []byte("{}"), signature: execute, readSignature: read}
	intent.source.descriptor = []byte("{}")
	previous := platformRunLifecycle
	t.Cleanup(func() { platformRunLifecycle = previous })
	seen := map[transport.WritableLifecycleAction]byte{}
	platformRunLifecycle = func(_ context.Context, _ string, _, _ uint32, action transport.WritableLifecycleAction, _ []byte, signature []byte, _ control.Authority, _ []byte, _ []byte) (transport.WritableLifecycleObservation, error) {
		seen[action] = signature[0]
		return transport.WritableLifecycleObservation{}, errors.New("stop")
	}
	_, _ = platformExecuteRestore(context.Background(), endpoint, intent)
	_, _ = platformReadRestoreReceipt(context.Background(), endpoint, intent)
	if seen[transport.RestoreExecute] != 1 || seen[transport.RestoreRead] != 2 {
		t.Fatalf("signatures by action = %v", seen)
	}
}
