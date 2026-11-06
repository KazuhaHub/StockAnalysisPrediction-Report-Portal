//go:build linux

package app

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/driver"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/journal"
)

// Only the runner's typed absence reply becomes the definite no-effect answer for a start read. A closed
// connection or any other failure stays an ordinary error, which the Portal treats as unknown.
func TestStartReceiptEndpointMapsAbsentReplyToDefiniteAbsence(t *testing.T) {
	endpoint := platformRuntimeEndpoint{path: "/nonexistent/start.sock", controllerUID: 1000, controllerGID: 1001}
	intent := platformUpdateStartIntent{raw: []byte("{}"), signature: make([]byte, 64), targetDescriptor: []byte("{}")}
	stubLifecycleRunner(t, fmt.Errorf("runner reply: %w", journal.ErrNoSuchEffect))
	if _, err := platformReadUpdateStartReceipt(context.Background(), endpoint, intent); !errors.Is(err, errPlatformStartAbsent) {
		t.Fatalf("absent reply was not mapped: %v", err)
	}
	for name, failure := range map[string]error{
		"connection closed": fmt.Errorf("%w: lifecycle header", driver.ErrContainerUncertain),
		"unreachable":       errors.New("dial unix: no such file"),
	} {
		stubLifecycleRunner(t, failure)
		if _, err := platformReadUpdateStartReceipt(context.Background(), endpoint, intent); err == nil || errors.Is(err, errPlatformStartAbsent) {
			t.Fatalf("%s must stay unknown: %v", name, err)
		}
	}
}
