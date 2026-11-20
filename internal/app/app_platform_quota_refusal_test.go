package app

import (
	"testing"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

// set_data_quota and import_data are managed lifecycle actions with executors of their own. They are never recovery
// actions, and a frontend installation, which has no namespace, cannot hold either.
func TestDataActionsAreManagedLifecycleActionsOnly(t *testing.T) {
	for _, action := range []string{"set_data_quota", "import_data"} {
		if platformRecoveryAction(action) {
			t.Fatalf("%s is a recovery action", action)
		}
		if _, err := appplatform.NewOperation(appplatform.Action(action), true, 1); err != nil {
			t.Fatalf("a managed operation of action %s cannot start: %v", action, err)
		}
		if _, err := appplatform.NewOperation(appplatform.Action(action), false, 1); err == nil {
			t.Fatalf("an unmanaged operation of action %s was started", action)
		}
	}
}
