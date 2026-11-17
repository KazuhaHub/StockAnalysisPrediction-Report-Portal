package app

import (
	"context"
	"path/filepath"
	"testing"
)

func testPlatformDataDispatchUnavailable(t *testing.T, pg bool) {
	s, _, lease := platformDataIntentFixture(t, pg)
	endpoint := platformRuntimeEndpoint{path: filepath.Join(t.TempDir(), "absent.sock"), controllerUID: 41001, controllerGID: 41001}
	for range 2 {
		if _, err := s.dispatchPlatformInstallData(context.Background(), lease, endpoint); err == nil {
			t.Fatal("missing runner completed data dispatch")
		}
		if scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_data_namespaces WHERE state='review_required' AND revision=1 AND writer_fence=0`) != 1 || scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_operation_effects WHERE state='prepared' AND receipt_json='{}'`) != 1 || scalar[string](t, s.st, `SELECT stage FROM app_platform_operations`) != "prepare_data" {
			t.Fatal("uncertain dispatch replaced or completed durable intent")
		}
	}
	if scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_activations`)+scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_access_grants`) != 0 {
		t.Fatal("failed dispatch exposed app")
	}
}
func TestPlatformDataDispatchUnavailableSQLite(t *testing.T) {
	testPlatformDataDispatchUnavailable(t, false)
}
func TestPlatformDataDispatchUnavailablePostgres(t *testing.T) {
	testPlatformDataDispatchUnavailable(t, true)
}
