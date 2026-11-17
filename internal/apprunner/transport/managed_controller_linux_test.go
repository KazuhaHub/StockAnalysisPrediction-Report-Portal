//go:build linux

package transport

import (
	"context"
	"os"
	"testing"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/data"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/driver"
)

func TestManagedControllerRefusesUnconfiguredProviderWithoutSocket(t *testing.T) {
	for _, native := range []*driver.CandidateRuntime{nil, {}} {
		for _, root := range []*data.Root{nil, {}} {
			path := t.TempDir() + "/control.sock"
			server, err := ListenManagedController(context.Background(), path, 41001, 41001, nil, root, data.RootIdentity{}, native)
			if err == nil {
				server.Close()
				t.Fatal("unconfigured unified endpoint opened")
			}
			if _, err := os.Lstat(path); !os.IsNotExist(err) {
				t.Fatal("failed join created socket", err)
			}
			if err := native.CheckSharedRootConfiguration(context.Background(), root, data.RootIdentity{}); err == nil {
				t.Fatal("zero provider accepted as shared root")
			}
		}
	}
}
