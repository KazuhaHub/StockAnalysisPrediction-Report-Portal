//go:build linux

package app

import (
	"context"
	"errors"
	"testing"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/transport"
)

// The endpoint refuses an intent that cannot be verified by the runner before it opens a socket.
func TestImportEndpointRefusesIntentWithoutDescriptor(t *testing.T) {
	endpoint := platformRuntimeEndpoint{path: "/nonexistent/import.sock", controllerUID: 1000, controllerGID: 1001}
	intent := platformImportIntent{raw: []byte("{}"), signature: make([]byte, 64)}
	for name, run := range map[string]func(context.Context, platformRuntimeEndpoint, platformImportIntent) (transport.WritableLifecycleObservation, error){
		"execute": platformExecuteImportCheckpoint,
		"read":    platformReadImportCheckpointReceipt,
	} {
		if _, err := run(context.Background(), endpoint, intent); !errors.Is(err, errAppPlatformStageEvidence) {
			t.Fatalf("%s: intent without a release descriptor was sent: %v", name, err)
		}
	}
}
