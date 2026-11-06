//go:build linux

package app

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/driver"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/journal"
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

func stubLifecycleRunner(t *testing.T, err error) {
	t.Helper()
	previous := platformRunLifecycle
	platformRunLifecycle = func(context.Context, string, uint32, uint32, transport.WritableLifecycleAction, []byte, []byte, control.Authority, []byte, []byte) (transport.WritableLifecycleObservation, error) {
		return transport.WritableLifecycleObservation{}, err
	}
	t.Cleanup(func() { platformRunLifecycle = previous })
}

func importEndpointFixture() (platformRuntimeEndpoint, platformImportIntent) {
	endpoint := platformRuntimeEndpoint{path: "/nonexistent/import.sock", controllerUID: 1000, controllerGID: 1001}
	return endpoint, platformImportIntent{raw: []byte("{}"), signature: make([]byte, 64), descriptor: []byte("{}")}
}

// Only the runner's typed absence reply becomes the definite no-effect answer, and only for a read.
// A closed connection or any other failure stays an ordinary error, which the Portal treats as unknown.
func TestImportEndpointMapsAbsentReplyToDefiniteAbsence(t *testing.T) {
	endpoint, intent := importEndpointFixture()
	stubLifecycleRunner(t, fmt.Errorf("runner reply: %w", journal.ErrNoSuchEffect))
	if _, err := platformReadImportCheckpointReceipt(context.Background(), endpoint, intent); !errors.Is(err, errPlatformImportAbsent) {
		t.Fatalf("absent reply was not mapped: %v", err)
	}
	if _, err := platformExecuteImportCheckpoint(context.Background(), endpoint, intent); errors.Is(err, errPlatformImportAbsent) {
		t.Fatal("an execute was reported as definitely absent")
	}
	for name, failure := range map[string]error{
		"connection closed": fmt.Errorf("%w: lifecycle header", driver.ErrContainerUncertain),
		"unreachable":       errors.New("dial unix: no such file"),
	} {
		stubLifecycleRunner(t, failure)
		if _, err := platformReadImportCheckpointReceipt(context.Background(), endpoint, intent); err == nil || errors.Is(err, errPlatformImportAbsent) {
			t.Fatalf("%s must stay unknown: %v", name, err)
		}
	}
}

// importAbsentRunnerExecutor reads through the real transport adapter, whose lifecycle runner reports
// the runner's absent reply, and executes through the shared fake.
type importAbsentRunnerExecutor struct {
	platformImportTransport
	fake *platformImportTestExecutor
}

func (e importAbsentRunnerExecutor) Execute(ctx context.Context, endpoint platformRuntimeEndpoint, intent platformImportIntent) (platformImportObservation, error) {
	return e.fake.Execute(ctx, endpoint, intent)
}

func TestRestoreImportRunnerAbsentReplyRevokesGrantAndAllowsRetry(t *testing.T) {
	f := platformRestoreImportFixture(t, false)
	commit := f.reviewAndConsume(t)
	lease, _ := dispatchLostAndTakeOver(t, f, commit.OperationID)
	f.s.platformImportCheckpoint = importAbsentRunnerExecutor{fake: f.executor}
	stubLifecycleRunner(t, journal.ErrNoSuchEffect)
	endpoint := platformRestoreEndpointFixture()
	if _, err := f.s.runPlatformRestoreImport(context.Background(), lease, endpoint); !errors.Is(err, errPlatformImportEnded) {
		t.Fatalf("a runner absent reply must end the operation: %v", err)
	}
	if scalar[string](t, f.s.st, `SELECT stage FROM app_platform_operations WHERE id=?`, commit.OperationID) != "failed" ||
		scalar[string](t, f.s.st, `SELECT state FROM app_platform_import_grants`) != "revoked" ||
		scalar[string](t, f.s.st, `SELECT state FROM app_platform_operation_effects WHERE operation_id=?`, commit.OperationID) != "reverted" {
		t.Fatal("the absent reply did not fail the operation, revoke the grant and revert the effect")
	}
	review, err := f.s.createPlatformRestoreImportReview(context.Background(), f.actor, f.installation, f.recoverySet)
	if err != nil {
		t.Fatal("retry review refused:", err)
	}
	retry, err := f.s.consumePlatformRestoreImportReview(context.Background(), f.actor, review.ID, review.SHA256, platformFixtureID(5211))
	if err != nil {
		t.Fatal(err)
	}
	f.s.platformImportCheckpoint = f.executor
	if done, err := f.drive(t, retry.OperationID); err != nil || done.snapshot.Stage != appplatform.StageComplete {
		t.Fatalf("the retry did not complete: %v", err)
	}
}
