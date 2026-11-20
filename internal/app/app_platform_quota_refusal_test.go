package app

import (
	"testing"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

// Migration 0024 lets the schema hold set_data_quota and import_data. Nothing executes them yet, so nothing
// may accept a plan that carries one: not the management request, and not the operation model.
func TestActionsOnlyTheSchemaKnowsAreAcceptedNowhere(t *testing.T) {
	for _, action := range []string{"set_data_quota", "import_data"} {
		if platformRecoveryAction(action) {
			t.Fatalf("%s is a recovery action", action)
		}
		if _, err := appplatform.NewOperation(appplatform.Action(action), true, 1); err == nil {
			t.Fatalf("an operation of action %s was started", action)
		}
	}
}
