package app

import (
	"bytes"
	"context"
	"os"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

func TestPlatformWritableSchedulerRoutesManagedUninstall(t *testing.T) {
	s, actor, lease := platformWritableAccessFixture(t, false)
	plan, err := s.createPlatformRemovalReview(actor, lease.installationID, appplatform.ActionUninstall)
	if err != nil {
		t.Fatal(err)
	}
	commit, err := s.consumePlatformRemovalReview(actor, plan.ID, plan.SHA256, platformFixtureID(947))
	if err != nil {
		t.Fatal(err)
	}
	endpoint := platformRuntimeEndpoint{path: "/tmp/portal-writable-uninstall.sock", controllerUID: uint32(os.Geteuid()), controllerGID: uint32(os.Getegid())}
	worker, err := newPlatformWritableInstallScheduler(s, endpoint, platformFixtureID(948), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := worker.tick(context.Background()); err == nil {
		t.Fatal("unreachable managed endpoint unexpectedly succeeded")
	}
	if worker.active.id != commit.OperationID || worker.active.snapshot.Action != appplatform.ActionUninstall ||
		!worker.active.snapshot.Managed || worker.active.snapshot.Stage != appplatform.StageStop {
		t.Fatal("writable scheduler did not route managed uninstall", worker.active.snapshot)
	}
}

// Canonical SQL review/runtime metadata only; no native success is produced.
func platformWritableQueueFixture(t *testing.T, pg bool) (*Server, string) {
	t.Helper()
	s, lease := platformWriterIntentFixture(t, pg)
	mustExec(t, s.st, `DELETE FROM app_platform_operation_effects WHERE operation_id=?`, lease.id)
	mustExec(t, s.st, `DELETE FROM app_platform_data_namespaces WHERE owner_installation_id=?`, lease.installationID)
	mustExec(t, s.st, `UPDATE app_platform_installations SET revision=1,control_fence=0 WHERE id=?`, lease.installationID)
	mustExec(t, s.st, `UPDATE app_platform_operations SET stage='queued',state_revision=1,row_revision=1,fence=0,lease_owner=NULL,lease_until_ms=NULL WHERE id=?`, lease.id)
	return s, lease.id
}

func testPlatformWritableQueueClaim(t *testing.T, pg bool) {
	s, id := platformWritableQueueFixture(t, pg)
	for _, selectOther := range []func(context.Context, string) (platformInstallSelection, error){s.claimNextPlatformStaticInstall, s.claimNextPlatformFrontendInstall} {
		selected, err := selectOther(context.Background(), platformFixtureID(941))
		if err != nil || selected.lease.id != "" || selected.reviewed {
			t.Fatal("static queue adopted writable work", err)
		}
	}
	first, err := s.claimNextPlatformWritableInstall(context.Background(), platformFixtureID(941))
	if err != nil || first.lease.id != id || !first.lease.snapshot.Managed || first.lease.snapshot.Stage != appplatform.StageQueued || first.lease.fence != 1 {
		t.Fatal("writable queue did not claim canonical initial work", err)
	}
	before := dumpOf(t, s.st)
	second, err := s.claimNextPlatformWritableInstall(context.Background(), platformFixtureID(942))
	if err != nil || second.lease.id != "" || second.reviewed || !bytes.Equal(stripHeader(before), stripHeader(dumpOf(t, s.st))) {
		t.Fatal("live writable lease stolen", err)
	}
	mustExec(t, s.st, `UPDATE app_platform_operations SET lease_until_ms=0`)
	resumed, err := s.claimNextPlatformWritableInstall(context.Background(), platformFixtureID(942))
	if err != nil || resumed.lease.id != id || resumed.lease.fence != 2 {
		t.Fatal("side-effect-free writable work did not resume", err)
	}
	if _, err := s.st.heartbeatPlatformOperation(first.lease); err == nil {
		t.Fatal("old owner regained lease")
	}
}
func TestPlatformWritableQueueClaimSQLite(t *testing.T)   { testPlatformWritableQueueClaim(t, false) }
func TestPlatformWritableQueueClaimPostgres(t *testing.T) { testPlatformWritableQueueClaim(t, true) }

func testPlatformWritableQueueRecovery(t *testing.T, pg bool) {
	for _, stage := range []string{"writer", "start", "probe", "activate", "revoked_runtime"} {
		t.Run(stage, func(t *testing.T) {
			var s *Server
			var lease platformOperationLease
			switch stage {
			case "writer", "revoked_runtime":
				s, lease = platformWriterIntentFixture(t, pg)
				if stage == "revoked_runtime" {
					mustExec(t, s.st, `UPDATE app_platform_runtime_bindings SET enabled=0`)
				}
			case "start":
				s, lease, _ = platformWritableStartFixture(t, pg)
			case "probe":
				s, lease, _ = platformWritableProbeFixture(t, pg)
			case "activate":
				s, lease = platformWritableRegistrationFixture(t, pg)
			}
			source := scalar[string](t, s.st, `SELECT receipt_json FROM app_platform_operation_effects WHERE operation_id=? AND stage='prepare_data' AND effect_number=1`, lease.id)
			mustExec(t, s.st, `UPDATE app_platform_operations SET lease_until_ms=0`)
			selected, err := s.claimNextPlatformWritableInstall(context.Background(), platformFixtureID(943))
			if err != nil || !selected.reviewed || selected.lease.id != "" || selected.operationID != lease.id {
				t.Fatal("expired effects were reclaimed", err)
			}
			if scalar[string](t, s.st, `SELECT stage FROM app_platform_operations WHERE id=?`, lease.id) != "recovery_required" || scalar[string](t, s.st, `SELECT phase FROM app_platform_installations WHERE id=?`, lease.installationID) != "recovery_required" || scalar[string](t, s.st, `SELECT receipt_json FROM app_platform_operation_effects WHERE operation_id=? AND stage='prepare_data' AND effect_number=1`, lease.id) != source {
				t.Fatal("recovery lost evidence or hid effects")
			}
			if scalar[int64](t, s.st, `SELECT control_fence FROM app_platform_installations WHERE id=?`, lease.installationID) != lease.fence+1 || scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_access_grants`) != 0 {
				t.Fatal("recovery granted authority or failed to fence")
			}
		})
	}
}
func TestPlatformWritableQueueRecoverySQLite(t *testing.T) {
	testPlatformWritableQueueRecovery(t, false)
}
func TestPlatformWritableQueueRecoveryPostgres(t *testing.T) {
	testPlatformWritableQueueRecovery(t, true)
}

func testPlatformWritableSchedulerRetries(t *testing.T, pg bool) {
	s, id := platformWritableQueueFixture(t, pg)
	static := platformSchedulerFixture(t, s)
	w, err := newPlatformWritableInstallScheduler(s, static.endpoint, platformFixtureID(944), 50*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	for attempt := 0; attempt < platformInstallAttemptLimit; attempt++ {
		err := w.tick(context.Background())
		if attempt < platformInstallAttemptLimit-1 && (err == nil || w.active.id != id || w.currentStatus().code != "retry_pending") {
			t.Fatal("writable retry lost active work", err)
		}
		if attempt == platformInstallAttemptLimit-1 && err != nil {
			t.Fatal(err)
		}
	}
	if w.active.id != "" || w.currentStatus().code != "review_required" || scalar[string](t, s.st, `SELECT phase FROM app_platform_installations`) != "recovery_required" || scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_operation_effects`) != 1 {
		t.Fatal("writable retry ceiling discarded evidence or kept ownership")
	}
	if err := w.tick(context.Background()); err != nil || w.currentStatus().code != "idle" {
		t.Fatal("review work reentered queue", err)
	}
}
func TestPlatformWritableSchedulerRetriesSQLite(t *testing.T) {
	testPlatformWritableSchedulerRetries(t, false)
}
func TestPlatformWritableSchedulerRetriesPostgres(t *testing.T) {
	testPlatformWritableSchedulerRetries(t, true)
}

func testPlatformWritableQueueAuthority(t *testing.T, pg bool) {
	for _, mode := range []string{"sponsor", "publisher", "runtime", "review", "runtime_identity", "pause"} {
		t.Run(mode, func(t *testing.T) {
			s, id := platformWritableQueueFixture(t, pg)
			switch mode {
			case "sponsor":
				mustExec(t, s.st, `UPDATE users SET active=0 WHERE username=(SELECT username FROM app_platform_review_plans)`)
			case "publisher":
				mustExec(t, s.st, `UPDATE app_platform_publishers SET enabled=0`)
			case "runtime":
				mustExec(t, s.st, `UPDATE app_platform_runtime_bindings SET enabled=0`)
			case "review":
				mustExec(t, s.st, `UPDATE app_platform_review_plans SET plan_sha256=?`, platformDigest([]byte("other")))
			case "runtime_identity":
				mustExec(t, s.st, `UPDATE app_platform_installations SET runtime_id=NULL`)
			case "pause":
				mustExec(t, s.st, `UPDATE meta SET v='1' WHERE k='app_platform_restore_paused'`)
			}
			selected, err := s.claimNextPlatformWritableInstall(context.Background(), platformFixtureID(945))
			if selected.lease.id != "" || scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_operations WHERE lease_owner IS NOT NULL`) != 0 {
				t.Fatal("invalid authority granted a lease", err)
			}
			if mode == "sponsor" || mode == "publisher" {
				if err != nil || !selected.reviewed || selected.operationID != id || scalar[string](t, s.st, `SELECT phase FROM app_platform_installations`) != "review_required" {
					t.Fatal("revoked no-effect work not denied for review", err)
				}
			}
			if scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_operation_effects`)+scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_access_grants`) != 0 {
				t.Fatal("authority refusal created resources")
			}
		})
	}
}
func TestPlatformWritableQueueAuthoritySQLite(t *testing.T) {
	testPlatformWritableQueueAuthority(t, false)
}
func TestPlatformWritableQueueAuthorityPostgres(t *testing.T) {
	testPlatformWritableQueueAuthority(t, true)
}

func TestPlatformWritableSchedulerCancellationSQLite(t *testing.T) {
	s, _ := platformWritableQueueFixture(t, false)
	if _, err := newPlatformWritableInstallScheduler(s, platformRuntimeEndpoint{}, platformFixtureID(946), time.Second); err == nil {
		t.Fatal("writable scheduler accepted frontend-only bootstrap")
	}
	static := platformSchedulerFixture(t, s)
	w, err := newPlatformWritableInstallScheduler(s, static.endpoint, platformFixtureID(946), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	before := dumpOf(t, s.st)
	if err := w.tick(ctx); err == nil {
		t.Fatal("canceled scheduler claimed work")
	}
	if !bytes.Equal(stripHeader(before), stripHeader(dumpOf(t, s.st))) {
		t.Fatal("canceled scheduler changed storage")
	}
}

func TestPlatformWritableReadinessWaitRevocationSQLite(t *testing.T) {
	s, lease, _ := platformWritableProbeFixture(t, false)
	mustExec(t, s.st, `UPDATE app_platform_runtime_bindings SET enabled=0`)
	before := dumpOf(t, s.st)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := s.waitPlatformWritableCandidate(ctx, lease, platformRuntimeEndpoint{}); err == nil || ctx.Err() != nil {
		t.Fatal("readiness wait ignored current revocation or waited for deadline", err)
	}
	if !bytes.Equal(stripHeader(before), stripHeader(dumpOf(t, s.st))) {
		t.Fatal("readiness refusal mutated storage")
	}
}

func testPlatformWritableFailureBinding(t *testing.T, pg bool) {
	for _, mode := range []string{"readonly", "runtime", "digest", "request"} {
		t.Run(mode, func(t *testing.T) {
			s, lease := platformWriterIntentFixture(t, pg)
			switch mode {
			case "readonly":
				s, lease = platformCandidateIntentFixture(t, pg)
			case "runtime":
				mustExec(t, s.st, `UPDATE app_platform_installations SET runtime_id=NULL`)
			case "digest":
				mustExec(t, s.st, `UPDATE app_platform_review_plans SET plan_sha256=?`, platformDigest([]byte("other")))
			case "request":
				mustExec(t, s.st, `UPDATE app_platform_operations SET request_sha256=?`, platformDigest([]byte("other")))
			}
			before := dumpOf(t, s.st)
			if err := s.finishPlatformInstallForReview(context.Background(), lease); err == nil {
				t.Fatal("failure transition adopted unbound managed source")
			}
			if !bytes.Equal(stripHeader(before), stripHeader(dumpOf(t, s.st))) {
				t.Fatal("refused failure transition changed source")
			}
		})
	}
}
func TestPlatformWritableFailureBindingSQLite(t *testing.T) {
	testPlatformWritableFailureBinding(t, false)
}
func TestPlatformWritableFailureBindingPostgres(t *testing.T) {
	testPlatformWritableFailureBinding(t, true)
}
