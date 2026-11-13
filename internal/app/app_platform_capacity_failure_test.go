package app

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/journal"
)

func capacityRefusalFixture(dimension string) journal.CapacityRefusal {
	return journal.CapacityRefusal{
		Format: "portal.runner.capacity-refusal/1-dev.1", DeploymentID: platformFixtureID(1), ControllerEpoch: platformFixtureID(2),
		InstallationID: platformFixtureID(3), OperationID: platformFixtureID(4), TargetGeneration: "1", Stage: "start", EffectNumber: "1",
		EffectID: platformFixtureID(5), Kind: "start-candidate", PayloadSHA256: strings.Repeat("a", 64),
		Outcome: "capacity_exhausted", Dimension: dimension, Requested: "256", Remaining: "100", SettledMS: strconv.FormatInt(time.Now().UnixMilli(), 10),
	}
}

func TestPlatformCapacityErrorCodeIsOnePerDimension(t *testing.T) {
	want := map[string]string{
		"memory": "capacity_exhausted_memory", "cpu": "capacity_exhausted_cpu", "disk": "capacity_exhausted_disk",
		"image_store": "capacity_exhausted_image_store", "running_backends": "capacity_exhausted_running_backends",
	}
	for dimension, code := range want {
		got, ok := platformCapacityErrorCode(dimension)
		if !ok || got != code || !isPlatformCapacityErrorCode(code) {
			t.Fatalf("%s -> %q %v", dimension, got, ok)
		}
		if len(code) > 96 {
			t.Fatalf("code %q does not fit the operation error column", code)
		}
	}
	for _, bad := range []string{"", "swap", "Memory", "memory "} {
		if _, ok := platformCapacityErrorCode(bad); ok {
			t.Fatalf("dimension %q mapped", bad)
		}
	}
	if isPlatformCapacityErrorCode("capacity_exhausted_swap") || isPlatformCapacityErrorCode("install_execution_review_required") {
		t.Fatal("a code outside the closed set was recognised")
	}
}

func TestPlatformCapacityRefusalIsFoundThroughWrapping(t *testing.T) {
	r := capacityRefusalFixture("disk")
	wrapped := fmt.Errorf("image load: %w", &journal.CapacityRefusalError{Receipt: r})
	got, ok := platformCapacityRefusalOf(wrapped)
	if !ok || got != r {
		t.Fatalf("not found: %v", ok)
	}
	if _, ok := platformCapacityRefusalOf(errors.New("other")); ok {
		t.Fatal("found a refusal in an unrelated error")
	}
	if _, ok := platformCapacityRefusalOf(nil); ok {
		t.Fatal("found a refusal in nil")
	}
}

func TestPlatformInstallFailsWithTheCapacityDimension(t *testing.T) {
	for _, dimension := range []string{"memory", "cpu", "disk", "image_store", "running_backends"} {
		t.Run(dimension, func(t *testing.T) {
			s, _, id := platformInstallQueueFixture(t, false)
			w := platformSchedulerFixture(t, s)
			// The first tick claims the operation; the absent endpoint makes the run fail and keeps it active.
			_ = w.tick(context.Background())
			if w.active.id != id {
				t.Fatal("the operation was not claimed")
			}
			refusal := capacityRefusalFixture(dimension)
			if err := w.finishCapacity(context.Background(), refusal); err != nil {
				t.Fatal(err)
			}
			code, _ := platformCapacityErrorCode(dimension)
			if scalar[string](t, s.st, `SELECT error_code FROM app_platform_operations`) != code {
				t.Fatalf("error code = %s", scalar[string](t, s.st, `SELECT error_code FROM app_platform_operations`))
			}
			stage := scalar[string](t, s.st, `SELECT stage FROM app_platform_operations`)
			if stage != "recovery_required" && stage != "failed" {
				t.Fatalf("stage = %s", stage)
			}
			if scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_operations WHERE lease_owner IS NULL AND completed_ms IS NOT NULL`) != 1 {
				t.Fatal("the failed operation still holds its lease")
			}
			var detail string
			if err := s.st.db.QueryRow(`SELECT detail FROM audit_log WHERE action='app_platform.capacity_refused'`).Scan(&detail); err != nil {
				t.Fatalf("no typed detail kept: %v", err)
			}
			for _, want := range []string{`"outcome":"capacity_exhausted"`, `"dimension":"` + dimension + `"`, `"requested":"256"`, `"remaining":"100"`} {
				if !strings.Contains(detail, want) {
					t.Fatalf("detail %s lacks %s", detail, want)
				}
			}
			if w.active.id != "" || w.currentStatus().code != "review_required" {
				t.Fatalf("worker state %+v", w.currentStatus())
			}
		})
	}
}

func TestPlatformCapacityCodesReachTheManagementAndStatusViews(t *testing.T) {
	for _, dimension := range []string{"memory", "cpu", "disk", "image_store", "running_backends"} {
		code, _ := platformCapacityErrorCode(dimension)
		if got := platformPublicOperationErrorCode(code); got != code {
			t.Fatalf("%s is shown as %q", code, got)
		}
	}
	if platformPublicOperationErrorCode("something_internal") != "app_platform_operation_failed" {
		t.Fatal("an internal code leaked")
	}
	if platformPublicOperationErrorCode("install_execution_review_required") != "install_execution_review_required" {
		t.Fatal("an existing public code was lost")
	}
}

// A tick whose run ends in the runner's capacity refusal finishes the operation at once, with the typed code: it
// is not retried like a transient failure. Fails if the tick drops the refusal into the retry path.
func TestSchedulerTickFinishesAnOperationRefusedByCapacity(t *testing.T) {
	s, _, id := platformInstallQueueFixture(t, false)
	w := platformSchedulerFixture(t, s)
	calls := 0
	w.runOverride = func(context.Context, platformOperationLease) (platformInstallRun, error) {
		calls++
		return platformInstallRun{}, fmt.Errorf("start: %w", &journal.CapacityRefusalError{Receipt: capacityRefusalFixture("running_backends")})
	}
	if err := w.tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("the run was attempted %d times", calls)
	}
	if scalar[string](t, s.st, `SELECT error_code FROM app_platform_operations WHERE id=?`, id) != "capacity_exhausted_running_backends" {
		t.Fatalf("error code = %s", scalar[string](t, s.st, `SELECT error_code FROM app_platform_operations WHERE id=?`, id))
	}
	if w.active.id != "" || w.currentStatus().code != "review_required" {
		t.Fatalf("status %+v", w.currentStatus())
	}
	if scalar[int](t, s.st, `SELECT COUNT(*) FROM audit_log WHERE action='app_platform.capacity_refused' AND target_id=?`, id) != 1 {
		t.Fatal("no typed detail kept")
	}
}
