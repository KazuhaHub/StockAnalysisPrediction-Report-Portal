//go:build linux && (amd64 || arm64)

package app

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/transport"
)

func testNativePortalWritableReadiness(t *testing.T, s *Server, lease platformOperationLease, endpoint platformRuntimeEndpoint, start platformWritableStartIntent) {
	t.Helper()
	ctx := context.Background()
	intent, err := s.preparePlatformWritableProbe(ctx, lease)
	if err != nil {
		t.Fatal("current writable probe source", err)
	}
	if intent.writer.resourceID != start.writer.resourceID || intent.running.NativeID != start.running.NativeID {
		t.Fatal("probe replaced original source")
	}
	// The controlled backend completes the full Portal hard-quota probe before
	// opening its readiness socket. No native start is repeated while waiting.
	var proof transport.WritableLifecycleObservation
	deadline := time.Now().Add(20 * time.Second)
	for {
		proof, err = platformRunWritableStart(ctx, endpoint, intent, transport.WritableCandidateProbe)
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("writable readiness unavailable", err)
		}
		time.Sleep(25 * time.Millisecond)
	}
	if !validPlatformWritableReadinessObservation(proof, intent, endpoint) {
		t.Fatal("actual probe provenance/source")
	}
	mustExec(t, s.st, `CREATE TRIGGER portal_writable_readiness_failure BEFORE INSERT ON audit_log WHEN NEW.action='app_platform.writable_readiness_observed' BEGIN SELECT RAISE(ABORT,'writable readiness fixture'); END`)
	if _, err := s.commitPlatformWritableReadiness(ctx, lease, intent, endpoint, proof); err == nil {
		t.Fatal("readiness audit rollback ignored")
	}
	if failed, err := s.runPlatformWritableInstall(ctx, lease, endpoint); err == nil || failed.lease != lease {
		t.Fatal("worker lost readiness rollback progress", err)
	}
	t.Log("PORTAL_WRITABLE_WORKER_PROBE_ROLLBACK")
	if scalar[string](t, s.st, `SELECT stage FROM app_platform_operations WHERE id=?`, lease.id) != "probe" || scalar[string](t, s.st, `SELECT readiness_evidence FROM app_platform_activations WHERE id=?`, intent.writer.resourceID) != "{}" {
		t.Fatal("failed readiness commit partially advanced")
	}
	mustExec(t, s.st, `DROP TRIGGER portal_writable_readiness_failure`)
	activated, err := s.probePlatformWritableCandidate(ctx, lease, endpoint)
	if err != nil {
		t.Fatal("Portal current writable readiness", err)
	}
	if activated.snapshot.Stage != appplatform.StageActivate || activated.rowRevision != lease.rowRevision+1 || activated.fence != lease.fence {
		t.Fatal("probe did not advance exact operation")
	}
	stored, err := s.preparePlatformWritableProbe(ctx, activated)
	if err != nil || !validStoredPlatformWritableReadiness(stored, time.Now().UnixMilli()) || stored.running.NativeID != start.running.NativeID {
		t.Fatal("stored readiness changed running source", err)
	}
	oldReadiness := stored.writer.activationReadiness
	oldObserved := stored.writer.activationObserved
	if _, err := s.probePlatformWritableCandidate(ctx, lease, endpoint); err == nil {
		t.Fatal("stale probe lease accepted")
	}
	t.Log("PORTAL_WRITABLE_READINESS_ADVANCED")
	// Expiration of an observation must not force a new container or an implicit
	// activation. Keep historical binding, then collect a genuinely new probe.
	expiring, err := platformRunWritableStart(ctx, endpoint, stored, transport.WritableCandidateProbe)
	if err != nil {
		t.Fatal("fresh pre-expiry observation", err)
	}
	time.Sleep(3100 * time.Millisecond)
	expiredBefore := dumpOf(t, s.st)
	if _, err := s.commitPlatformWritableReadiness(ctx, activated, stored, endpoint, expiring); err == nil {
		t.Fatal("expired opaque observation accepted")
	}
	if !bytes.Equal(stripHeader(expiredBefore), stripHeader(dumpOf(t, s.st))) {
		t.Fatal("expired proof changed readiness")
	}

	historical, err := s.preparePlatformWritableProbe(ctx, activated)
	if err != nil || historical.writer.activationReadiness != oldReadiness || historical.running.NativeID != start.running.NativeID {
		t.Fatal("historical source expired/replaced", err)
	}
	refreshed, err := s.probePlatformWritableCandidate(ctx, activated, endpoint)
	if err != nil {
		t.Fatal("activate-stage readiness refresh", err)
	}
	if refreshed.rowRevision != activated.rowRevision || refreshed.snapshot.Revision != activated.snapshot.Revision || refreshed.leaseUntilMS != activated.leaseUntilMS || refreshed.fence != activated.fence {
		t.Fatal("readiness refresh changed operation authority")
	}
	current, err := s.preparePlatformWritableProbe(ctx, refreshed)
	if err != nil || !current.writer.activationObserved.Valid || current.writer.activationObserved.Int64 <= oldObserved.Int64 || current.running.NativeID != start.running.NativeID {
		t.Fatal("fresh observation/source", err)
	}
	t.Log("PORTAL_WRITABLE_READINESS_REFRESHED")
	// A real fresh opaque proof still cannot cross changed current Portal trust.
	fresh, err := platformRunWritableStart(ctx, endpoint, current, transport.WritableCandidateProbe)
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, s.st, `UPDATE app_platform_runtime_bindings SET enabled=0 WHERE id=?`, current.writer.runtime.id)
	before := dumpOf(t, s.st)
	if _, err := s.commitPlatformWritableReadiness(ctx, refreshed, current, endpoint, fresh); err == nil {
		t.Fatal("fresh proof adopted disabled runtime")
	}
	if !bytes.Equal(stripHeader(before), stripHeader(dumpOf(t, s.st))) {
		t.Fatal("revoked-source probe mutated state")
	}
	mustExec(t, s.st, `UPDATE app_platform_runtime_bindings SET enabled=1 WHERE id=?`, current.writer.runtime.id)
	if scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_operation_effects WHERE operation_id=?`, lease.id) != 3 || scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_access_grants WHERE installation_id=?`, lease.installationID) != 0 || scalar[string](t, s.st, `SELECT state FROM app_platform_activations WHERE id=?`, start.writer.resourceID) != "candidate" || scalar[string](t, s.st, `SELECT phase FROM app_platform_installations WHERE id=?`, lease.installationID) != "pending" {
		t.Fatal("readiness activated app")
	}
	if scalar[int](t, s.st, `SELECT COUNT(*) FROM audit_log WHERE action='app_platform.writable_readiness_observed' AND target_id=?`, lease.id) != 2 {
		t.Fatal("rollback or refusal persisted readiness audit")
	}
	t.Log("PORTAL_WRITABLE_READINESS_STALE_SOURCE_REFUSED")
	testNativePortalWritableRegistration(t, s, refreshed, endpoint, current)
}
