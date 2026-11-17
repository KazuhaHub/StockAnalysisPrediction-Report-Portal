//go:build linux || darwin

package journal

import (
	"context"
	"testing"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/data"
)

func TestDataDispatchNeedsNativeRootBeforeAdmission(t *testing.T) {
	s, _ := enrolledCommandStore(t)
	raw, sig := signRunnerCommand(t, runnerCommandFixture(t))
	for _, root := range []*data.Root{nil, {}} {
		for _, pin := range []data.RootIdentity{{}, {FilesystemUUID: fixtureID(900), Inode: 10}} {
			result, err := s.DispatchPrepareData(context.Background(), raw, sig, root, pin)
			if err == nil || result != (DataPreparationReceipt{}) {
				t.Fatal("invalid configured provider produced receipt", err)
			}
			if scalar(t, s.db, `SELECT COUNT(*) FROM runner_commands`)+scalar(t, s.db, `SELECT COUNT(*) FROM runner_resources`) != 0 {
				t.Fatal("invalid provider admitted work")
			}
		}
	}
}
