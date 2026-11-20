package app

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/journal"
)

func failedStatusFor(h control.CommandHeader, code string) *journal.MigrateDataStatus {
	return &journal.MigrateDataStatus{
		Format: "portal.runner.migration-status/1-dev.1", Kind: h.Kind, DeploymentID: h.DeploymentID, ControllerEpoch: h.ControllerEpoch,
		InstallationID: h.InstallationID, OperationID: h.OperationID, TargetGeneration: h.TargetGeneration, Stage: h.Stage,
		EffectNumber: h.EffectNumber, EffectID: h.EffectID, PayloadSHA256: h.PayloadSHA256, State: "failed", Code: code,
		ObservedMS: strconv.FormatInt(time.Now().UTC().UnixMilli(), 10),
	}
}

// The runner's failed status is a definite end of the data command, for exactly that command. Any other answer is
// not one, and a failed status that does not bind to the command is stage evidence that does not hold.
func TestPlatformDataEffectFailureRecognisesOnlyTheRunnersFailedStatusForThisCommand(t *testing.T) {
	_, _, _, _, runtime, _ := platformImportDisabledFixture(t, false)
	endpoint := platformCheckpointEndpointFixture()
	intent := platformImportDataIntent{authority: platformRuntimeAuthority(runtime)}
	intent.command.CommandHeader = control.CommandHeader{
		Kind: control.ImportDataKind, Stage: control.ImportDataStage, EffectNumber: "1", PayloadSHA256: platformTestArchiveSHA,
		DeploymentID: intent.authority.DeploymentID, ControllerEpoch: intent.authority.ControllerEpoch,
		InstallationID: platformFixtureID(1), OperationID: platformFixtureID(2), EffectID: platformFixtureID(3), TargetGeneration: "2",
	}
	intent.raw = []byte("signed command")
	observation := platformDataImportObservation(intent, endpoint)
	observation.status = failedStatusFor(intent.command.CommandHeader, "import_incomplete")
	err := platformDataEffectFailure(observation, intent.command.CommandHeader, intent.raw, intent.authority, endpoint)
	if !errors.Is(err, errPlatformDataEffectFailed) {
		t.Fatalf("the failed status was not recognised: %v", err)
	}
	other := intent.command.CommandHeader
	other.EffectID = platformFixtureID(4)
	if err := platformDataEffectFailure(observation, other, intent.raw, intent.authority, endpoint); !errors.Is(err, errAppPlatformStageEvidence) || errors.Is(err, errPlatformDataEffectFailed) {
		t.Fatalf("a failed status for another effect: %v", err)
	}
	pending := *observation.status
	pending.State, pending.Code = "pending", ""
	observation.status = &pending
	if err := platformDataEffectFailure(observation, intent.command.CommandHeader, intent.raw, intent.authority, endpoint); err != nil {
		t.Fatalf("a pending status is not a failure: %v", err)
	}
	observation.status = nil
	if err := platformDataEffectFailure(observation, intent.command.CommandHeader, intent.raw, intent.authority, endpoint); err != nil {
		t.Fatalf("a receipt is not a failure: %v", err)
	}
}

// A tick whose run ends in the runner's failed answer finishes the operation at once through its failure path: the
// command is not retried, because it answers the same failure every time.
func TestSchedulerTickFinishesAtOnceWhenTheRunnerReportsTheDataCommandFailed(t *testing.T) {
	s, _, id := platformInstallQueueFixture(t, false)
	w := platformSchedulerFixture(t, s)
	calls := 0
	w.runOverride = func(context.Context, platformOperationLease) (platformInstallRun, error) {
		calls++
		return platformInstallRun{}, fmt.Errorf("import: %w: import_archive_missing", errPlatformDataEffectFailed)
	}
	if err := w.tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if calls != 1 || w.active.id != "" || w.currentStatus().code != "review_required" {
		t.Fatalf("calls=%d status=%+v", calls, w.currentStatus())
	}
	if scalar[string](t, s.st, `SELECT stage FROM app_platform_operations WHERE id=?`, id) == "queued" {
		t.Fatal("the operation was not finished")
	}
}
