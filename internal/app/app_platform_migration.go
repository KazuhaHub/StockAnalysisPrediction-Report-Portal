package app

import (
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
)

// platformDefaultMigrationWallClockSeconds is the descriptor's default bound on a migration job.
const platformDefaultMigrationWallClockSeconds = 600

// platformMigrationStep is the single data migration an update review binds: the schemas it converts
// between, the wall-clock bound the job runs under and the digest of the descriptor's entrypoint. The
// entrypoint itself stays in the signed descriptor; the runner is handed only its digest.
type platformMigrationStep struct {
	From             int64  `json:"from"`
	To               int64  `json:"to"`
	WallClockSeconds int64  `json:"wall_clock_seconds"`
	EntrypointSHA256 string `json:"entrypoint_sha256"`
}

// platformPlanMigration selects what an update from the installed schema needs. It returns no step when
// the target reads the installed schema as it is (and declares that same schema), the declared step when
// one starts at the installed schema, and an error for any other schema change.
func platformPlanMigration(currentFormat int64, data appplatform.DataCompatibility) (*platformMigrationStep, error) {
	step, err := appplatform.PlanDataUpgrade(int(currentFormat), data)
	if err != nil {
		return nil, errAppPlatformInstallUnavailable
	}
	if step == nil {
		if int64(data.Schema) != currentFormat {
			return nil, errAppPlatformInstallUnavailable
		}
		return nil, nil
	}
	seconds := int64(platformDefaultMigrationWallClockSeconds)
	if step.WallClockSeconds != nil {
		seconds = int64(*step.WallClockSeconds)
	}
	return &platformMigrationStep{
		From: int64(step.From), To: int64(step.To), WallClockSeconds: seconds,
		EntrypointSHA256: control.MigrationEntrypointSHA256(step.Entrypoint),
	}, nil
}
