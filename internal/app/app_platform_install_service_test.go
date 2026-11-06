package app

import (
	"bytes"
	"context"
	"sync"
	"testing"
	"time"
)

func platformStartFrontendService(t *testing.T, s *Server) {
	t.Helper()
	stop := s.startPlatformFrontendInstallService()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		stop(ctx)
		select {
		case <-s.platformInstaller.done:
		default:
			t.Error("installation service did not join before store cleanup")
		}
	})
}

func platformAwaitFrontendService(t *testing.T, s *Server, code string) {
	t.Helper()
	timer := time.NewTimer(4 * time.Second)
	defer timer.Stop()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		if s.platformInstaller.worker.currentStatus().code == code {
			return
		}
		select {
		case <-timer.C:
			t.Fatalf("installation service never reached %s: %+v", code, s.platformInstaller.worker.currentStatus())
		case <-ticker.C:
		}
	}
}

func testPlatformFrontendServiceOwnership(t *testing.T, pg bool) {
	s, actor, id := platformFrontendQueuedFixture(t, pg)
	platformStartFrontendService(t, s)
	platformAwaitFrontendService(t, s, "complete")
	installation := scalar[string](t, s.st, `SELECT installation_id FROM app_platform_operations WHERE id=?`, id)
	if _, err := s.inspectPlatformLaunchAccess(actor, installation); err != nil {
		t.Fatal("autonomous service did not produce launchable installation", err)
	}
	if scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_runtime_bindings`)+scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_data_namespaces`)+scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_operation_effects`) != 0 {
		t.Fatal("portable service provisioned native resources")
	}
	worker := s.platformInstaller.worker
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			stop := s.startPlatformFrontendInstallService()
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			stop(ctx)
		})
	}
	wg.Wait()
	if s.platformInstaller.worker != worker || worker.currentStatus().code != "stopped" {
		t.Fatal("repeated start/stop replaced the owner or failed to join")
	}
	select {
	case <-s.platformInstaller.done:
	default:
		t.Fatal("stop returned before worker exit")
	}
}

func TestPlatformFrontendServiceOwnershipSQLite(t *testing.T) {
	testPlatformFrontendServiceOwnership(t, false)
}
func TestPlatformFrontendServiceOwnershipPostgres(t *testing.T) {
	testPlatformFrontendServiceOwnership(t, true)
}

func TestPlatformFrontendServiceNewAdmissionSQLite(t *testing.T) {
	s, actor, release := platformInstallFixture(t, false)
	platformStartFrontendService(t, s)
	platformAwaitFrontendService(t, s, "idle")
	review, err := s.createPlatformInstallAudienceReview(actor, release.sha256, 1, []platformAudienceTarget{{kind: "user", username: actor.username}})
	if err != nil {
		t.Fatal(err)
	}
	admitted, err := s.consumePlatformInstallReview(actor, review.ID, review.SHA256, platformFixtureID(961))
	if err != nil {
		t.Fatal(err)
	}
	platformAwaitFrontendService(t, s, "complete")
	if s.platformInstaller.worker.currentStatus().operationID != admitted.OperationID {
		t.Fatal("idle service did not pick up newly admitted operation")
	}
	if _, err = s.inspectPlatformLaunchAccess(actor, admitted.InstallationID); err != nil {
		t.Fatal("newly admitted operation is not launchable", err)
	}
}

func TestPlatformFrontendServiceBoundariesSQLite(t *testing.T) {
	for _, kind := range []string{"pristine", "restore_paused", "native"} {
		t.Run(kind, func(t *testing.T) {
			var s *Server
			switch kind {
			case "pristine":
				s = &Server{st: newTestStore(t)}
			case "restore_paused":
				s, _, _ = platformFrontendQueuedFixture(t, false)
				mustExec(t, s.st, `UPDATE meta SET v='1' WHERE k='app_platform_restore_paused'`)
			case "native":
				s, _, _ = platformInstallQueueFixture(t, false)
			}
			before := dumpOf(t, s.st)
			platformStartFrontendService(t, s)
			code := "worker_unavailable"
			if kind == "native" {
				code = "idle"
			}
			platformAwaitFrontendService(t, s, code)
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			s.startPlatformFrontendInstallService()(ctx)
			if !bytes.Equal(stripHeader(before), stripHeader(dumpOf(t, s.st))) {
				t.Fatal("service initialized fresh control, unpaused restore, or claimed native work")
			}
		})
	}
}

func TestPlatformFrontendServiceContainsPanic(t *testing.T) {
	s := &Server{st: newTestStore(t)}
	w, err := newPlatformInstallScheduler(s, platformRuntimeEndpoint{}, platformFixtureID(960), 50*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	// A broken worker must terminate its owner without taking the process down.
	// Do not introduce a production callback or expose panic details to callers.
	w.server = nil
	r := platformFrontendInstallRuntime{worker: w, done: make(chan struct{})}
	r.run(context.Background())
	select {
	case <-r.done:
	default:
		t.Fatal("panicked worker did not signal exit")
	}
	if w.currentStatus().code != "stopped" {
		t.Fatal("panicked worker retained running status")
	}
}
