//go:build linux && (amd64 || arm64)

package app

import (
	"bytes"
	"context"
	"testing"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/transport"
)

func testNativePortalWritableRegistration(t *testing.T, s *Server, lease platformOperationLease, endpoint platformRuntimeEndpoint, start platformWritableStartIntent) {
	t.Helper()
	ctx := context.Background()
	intent, err := s.preparePlatformWritableRegistration(ctx, lease)
	if err != nil {
		t.Fatal("writable registration intent", err)
	}
	defer intent.registration.Close()
	if intent.start.writer.resourceID != start.writer.resourceID || intent.start.running.NativeID != start.running.NativeID {
		t.Fatal("registration replaced source")
	}
	if _, err := s.completePlatformWritableRegistration(ctx, lease, endpoint); err == nil {
		t.Fatal("missing delivery completed")
	}
	mustExec(t, s.st, `CREATE TRIGGER portal_writable_registration_failure BEFORE INSERT ON audit_log WHEN NEW.action='app_platform.writable_registration_observed' BEGIN SELECT RAISE(ABORT,'registration settlement fixture'); END`)
	if _, err := s.dispatchPlatformWritableRegistration(ctx, lease, endpoint); err == nil {
		t.Fatal("registration audit rollback ignored")
	}
	if scalar[string](t, s.st, `SELECT state FROM app_platform_operation_effects WHERE operation_id=? AND stage='activate'`, lease.id) != "prepared" || scalar[int64](t, s.st, `SELECT row_revision FROM app_platform_operations WHERE id=?`, lease.id) != lease.rowRevision {
		t.Fatal("failed Portal registration partially committed")
	}
	observed, err := platformRunWritableRegistration(ctx, endpoint, intent, transport.WritableRegistrationRead)
	if err != nil {
		t.Fatal("actual original registration result", err)
	}
	receipt, err := observed.RegistrationReceipt()
	if err != nil || !validPlatformWritableRegistrationObservation(observed, intent, endpoint) {
		t.Fatal("registration provenance/source", err)
	}
	mustExec(t, s.st, `DROP TRIGGER portal_writable_registration_failure`)
	completed, err := s.completePlatformWritableRegistration(ctx, lease, endpoint)
	if err != nil {
		t.Fatal("registration historical completion", err)
	}
	if completed.snapshot.Stage != appplatform.StageActivate || completed.rowRevision != lease.rowRevision+1 || completed.snapshot.Revision != lease.snapshot.Revision || completed.fence != lease.fence {
		t.Fatal("delivery changed activation authority")
	}
	current, err := s.preparePlatformWritableRegistration(ctx, completed)
	if err != nil {
		t.Fatal(err)
	}
	defer current.registration.Close()
	if !current.applied || current.receipt != receipt || current.command.EffectID != intent.command.EffectID || current.command.PayloadSHA256 != intent.command.PayloadSHA256 || !samePlatformWritableStartIntent(current.start, intent.start) {
		t.Fatal("delivery replaced original process/file history")
	}
	t.Log("PORTAL_WRITABLE_REGISTRATION_COMMITTED")
	replay, err := s.dispatchPlatformWritableRegistration(ctx, completed, endpoint)
	if err != nil || replay.rowRevision != completed.rowRevision {
		t.Fatal("applied delivery replay", err)
	}
	after, err := s.preparePlatformWritableRegistration(ctx, replay)
	if err != nil {
		t.Fatal(err)
	}
	defer after.registration.Close()
	if !samePlatformWritableRegistrationIntent(current, after) {
		t.Fatal("delivery replay replaced result")
	}
	if _, err := s.completePlatformWritableRegistration(ctx, lease, endpoint); err == nil {
		t.Fatal("stale registration lease completed")
	}
	t.Log("PORTAL_WRITABLE_REGISTRATION_RETAINED_IDENTITY")
	proof, err := platformRunWritableRegistration(ctx, endpoint, current, transport.WritableRegistrationRead)
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, s.st, `UPDATE app_platform_runtime_bindings SET enabled=0 WHERE id=?`, current.start.writer.runtime.id)
	before := dumpOf(t, s.st)
	if _, err := s.commitPlatformWritableRegistration(ctx, completed, current, endpoint, proof); err == nil {
		t.Fatal("disabled runtime accepted fresh registration result")
	}
	if !bytes.Equal(stripHeader(before), stripHeader(dumpOf(t, s.st))) {
		t.Fatal("revoked source changed registration")
	}
	mustExec(t, s.st, `UPDATE app_platform_runtime_bindings SET enabled=1 WHERE id=?`, current.start.writer.runtime.id)
	if scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_operation_effects WHERE operation_id=?`, lease.id) != 4 || scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_access_grants WHERE installation_id=?`, lease.installationID) != 0 || scalar[string](t, s.st, `SELECT phase FROM app_platform_installations WHERE id=?`, lease.installationID) != "pending" || scalar[string](t, s.st, `SELECT state FROM app_platform_activations WHERE id=?`, start.writer.resourceID) != "candidate" {
		t.Fatal("delivery activated application")
	}
	if scalar[int](t, s.st, `SELECT COUNT(*) FROM audit_log WHERE action='app_platform.writable_registration_observed' AND target_id=?`, lease.id) != 1 {
		t.Fatal("delivery duplicated audit")
	}
	t.Log("PORTAL_WRITABLE_REGISTRATION_STALE_SOURCE_REFUSED")
	testNativePortalWritableBoot(t, s, completed, endpoint, current)
}
