package app

import (
	"errors"
	"testing"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
)

// platformServiceHistoryFor reads one installation's active tuple exactly as the
// service task preflight does and asks for the history a task would run against.
func platformServiceHistoryFor(t *testing.T, s *Server, installation string) (platformServiceBackendHistory, error) {
	t.Helper()
	tx, err := s.st.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	e := migExec{s: s.st, ex: tx}
	var b platformBackendHistoryBinding
	b.installation = installation
	var generation int64
	if err := e.queryRow(`SELECT i.generation,i.controller_epoch,i.control_fence,
		a.id,a.release_sha256,a.data_namespace_id,a.data_format,a.observed_ms,a.readiness_evidence
		FROM app_platform_installations i
		JOIN app_platform_activations a ON a.installation_id=i.id AND a.state='active'
		WHERE i.id=?`, installation).Scan(&generation, &b.epoch, &b.fence,
		&b.activation, &b.release, &b.namespace, &b.format, &b.observed,
		&b.readiness); err != nil {
		t.Fatal(err)
	}
	b.generation = generation
	dek, err := s.dek()
	if err != nil {
		t.Fatal(err)
	}
	defer clear(dek)
	runtime, seed, err := s.platformDataRuntime(e, b.epoch, dek)
	if err != nil {
		t.Fatal(err)
	}
	defer clear(seed)
	now, err := platformDatabaseNow(e)
	if err != nil {
		t.Fatal(err)
	}
	return s.loadPlatformServiceBackendHistory(e, b, runtime, seed, dek, now)
}

func requireServiceHistory(t *testing.T, h platformServiceBackendHistory, format string, initial, update bool) {
	t.Helper()
	defer h.Close()
	if h.format != format || (h.initial != nil) != initial || (h.update != nil) != update ||
		len(h.raw) == 0 || len(h.signature) == 0 || len(h.descriptor) == 0 ||
		h.runtime.id == "" || h.authority.DeploymentID == "" {
		t.Fatalf("service history: format=%q initial=%v update=%v", h.format, h.initial != nil, h.update != nil)
	}
	if _, err := h.registration.Metadata(); err != nil {
		t.Fatal("service history registration:", err)
	}
}

// The managed generation-1 path keeps the initial registration the runner verifies.
func testPlatformServiceHistoryManagedGenerationOne(t *testing.T, pg bool) {
	s, _, lease := platformManagedAccessFixture(t, pg)
	h, err := platformServiceHistoryFor(t, s, lease.installationID)
	if err != nil {
		t.Fatal(err)
	}
	requireServiceHistory(t, h, control.BackendRegistrationFormat, true, false)
}

// A writable generation-1 install is not mis-read as a managed one: its history is
// refused by the format check and not by the managed reader's effect count.
func testPlatformServiceHistoryWritableGenerationOneIsRefusedByFormat(t *testing.T, pg bool) {
	s, _, lease := platformWritableAccessFixture(t, pg)
	h, err := platformServiceHistoryFor(t, s, lease.installationID)
	if err == nil {
		h.Close()
		t.Fatal("a writable generation-1 install resolved a service-task history")
	}
	if !errors.Is(err, errAppPlatformTaskUnavailable) {
		t.Fatalf("unexpected refusal: %v", err)
	}
}

// Generation 2 after an update selects the update reader.
func testPlatformServiceHistoryGenerationTwoAfterUpdate(t *testing.T, pg bool) {
	s, lease, _ := platformUpdateStartActivatedFixture(t, pg)
	h, err := platformServiceHistoryFor(t, s, lease.installationID)
	if err != nil {
		t.Fatal(err)
	}
	requireServiceHistory(t, h, control.UpdateStartRegistrationFormat, false, true)
}

// Generation 2 after a completed restore selects the restore reader; the update
// reader only reads completed update operations.
func testPlatformServiceHistoryGenerationTwoAfterRestore(t *testing.T, pg bool) {
	s, lease, _ := platformRestoreStartActivatedFixture(t, pg)
	if scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_operations WHERE installation_id=? AND action='restore' AND stage='complete'`, lease.installationID) != 1 {
		t.Fatal("fixture: no completed restore")
	}
	h, err := platformServiceHistoryFor(t, s, lease.installationID)
	if err != nil {
		t.Fatal(err)
	}
	requireServiceHistory(t, h, control.UpdateStartRegistrationFormat, false, true)
}

// Generation 2 after a completed resume selects the resume reader.
func testPlatformServiceHistoryGenerationTwoAfterResume(t *testing.T, pg bool) {
	f := platformResumeFixture(t, pg)
	commit := f.reviewAndConsumeResume(t)
	startLease := f.driveResume(t, commit.OperationID, appplatform.StageStart)
	platformResumeStartToReady(t, f.s, startLease, f.endpoint)
	h, err := platformServiceHistoryFor(t, f.s, f.installation)
	if err != nil {
		t.Fatal(err)
	}
	requireServiceHistory(t, h, control.UpdateStartRegistrationFormat, false, true)
}

// Nothing completed to read: no format is guessed.
func testPlatformServiceHistoryUnknownPlanFormatIsRefused(t *testing.T, pg bool) {
	s, _, lease := platformManagedAccessFixture(t, pg)
	mustExec(t, s.st, `UPDATE app_platform_review_plans SET plan_json='{"format":"portal.unknown/1"}' WHERE id=(SELECT review_plan_id FROM app_platform_operations WHERE id=?)`, lease.id)
	h, err := platformServiceHistoryFor(t, s, lease.installationID)
	if err == nil {
		h.Close()
		t.Fatal("an unknown plan format resolved a service-task history")
	}
}

func TestPlatformServiceHistoryManagedGenerationOneSQLite(t *testing.T) {
	testPlatformServiceHistoryManagedGenerationOne(t, false)
}
func TestPlatformServiceHistoryManagedGenerationOnePostgres(t *testing.T) {
	testPlatformServiceHistoryManagedGenerationOne(t, true)
}

func TestPlatformServiceHistoryWritableGenerationOneIsRefusedByFormatSQLite(t *testing.T) {
	testPlatformServiceHistoryWritableGenerationOneIsRefusedByFormat(t, false)
}
func TestPlatformServiceHistoryWritableGenerationOneIsRefusedByFormatPostgres(t *testing.T) {
	testPlatformServiceHistoryWritableGenerationOneIsRefusedByFormat(t, true)
}

func TestPlatformServiceHistoryGenerationTwoAfterUpdateSQLite(t *testing.T) {
	testPlatformServiceHistoryGenerationTwoAfterUpdate(t, false)
}
func TestPlatformServiceHistoryGenerationTwoAfterUpdatePostgres(t *testing.T) {
	testPlatformServiceHistoryGenerationTwoAfterUpdate(t, true)
}

func TestPlatformServiceHistoryGenerationTwoAfterRestoreSQLite(t *testing.T) {
	testPlatformServiceHistoryGenerationTwoAfterRestore(t, false)
}
func TestPlatformServiceHistoryGenerationTwoAfterRestorePostgres(t *testing.T) {
	testPlatformServiceHistoryGenerationTwoAfterRestore(t, true)
}

func TestPlatformServiceHistoryGenerationTwoAfterResumeSQLite(t *testing.T) {
	testPlatformServiceHistoryGenerationTwoAfterResume(t, false)
}
func TestPlatformServiceHistoryGenerationTwoAfterResumePostgres(t *testing.T) {
	testPlatformServiceHistoryGenerationTwoAfterResume(t, true)
}

func TestPlatformServiceHistoryUnknownPlanFormatIsRefusedSQLite(t *testing.T) {
	testPlatformServiceHistoryUnknownPlanFormatIsRefused(t, false)
}
func TestPlatformServiceHistoryUnknownPlanFormatIsRefusedPostgres(t *testing.T) {
	testPlatformServiceHistoryUnknownPlanFormatIsRefused(t, true)
}
