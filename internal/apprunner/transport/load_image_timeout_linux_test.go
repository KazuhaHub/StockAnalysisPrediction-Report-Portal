//go:build linux

package transport

import (
	"testing"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/driver"
)

// A load that is allowed ImageLoadTimeout must not have its connection cut at the two-minute bulk bound.
func TestLoadImageExecuteTimeoutCoversTheLoadBound(t *testing.T) {
	if writableLifecycleTimeout(LoadImageExecute) <= driver.ImageLoadTimeout || writableLifecycleServerTimeout(LoadImageExecute) <= driver.ImageLoadTimeout {
		t.Fatalf("client %v server %v do not cover %v", writableLifecycleTimeout(LoadImageExecute), writableLifecycleServerTimeout(LoadImageExecute), driver.ImageLoadTimeout)
	}
	if writableLifecycleTimeout(LoadImageRead) > candidateExecutionTimeout || writableLifecycleTimeout(MigrateDataExecute) != restoreExecutionTimeout {
		t.Fatal("another action's bound changed")
	}
}
