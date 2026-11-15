package app

import (
	"bytes"
	"context"
	"errors"
	"log"
	"os"
	"strings"
	"testing"
	"time"
)

func writableServiceTestEndpoint() platformRuntimeEndpoint {
	return platformRuntimeEndpoint{path: "/run/portal-app-runner/control/runner.sock", controllerUID: 41001, controllerGID: 41001}
}

// Production startup starts the writable install scheduler when a trusted runner
// endpoint is bound, next to the frontend scheduler, and joins both on shutdown.
func TestPlatformInstallServiceStartsTheWritableSchedulerWhenAnEndpointIsBound(t *testing.T) {
	s := &Server{st: newTestStore(t)}
	endpoint := writableServiceTestEndpoint()
	s.platformBackendEndpoint.Store(&endpoint)
	stop := s.startPlatformFrontendInstallService()
	r := &s.platformInstaller
	if r.writer == nil || !r.writer.writable || r.writer.endpoint != endpoint {
		t.Fatal("startup did not construct the writable install scheduler for the bound endpoint")
	}
	if r.worker == nil || r.worker.writable || r.worker.endpoint != (platformRuntimeEndpoint{}) || r.writer.worker == r.worker.worker {
		t.Fatal("the frontend scheduler was replaced or shares a worker identity")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	stop(ctx)
	for name, done := range map[string]chan struct{}{"frontend": r.done, "writable": r.writerDone} {
		select {
		case <-done:
		default:
			t.Fatalf("shutdown returned before the %s scheduler exited", name)
		}
	}
	if code := r.writer.currentStatus().code; code != "stopped" {
		t.Fatal("writable scheduler status after shutdown:", code)
	}
}

func TestPlatformInstallServiceStartsNoWritableSchedulerWithoutAnEndpoint(t *testing.T) {
	s := &Server{st: newTestStore(t)}
	stop := s.startPlatformFrontendInstallService()
	if s.platformInstaller.writer != nil {
		t.Fatal("a writable scheduler was built without a runner endpoint")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	stop(ctx)
}

// The endpoint must be bound before the schedulers start, and shutdown must run
// the installer's stop function. Both are properties of RunServer's order.
func TestRunServerBindsTheRunnerEndpointBeforeStartingAndStoppingTheInstaller(t *testing.T) {
	raw, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(raw)
	bind := strings.Index(src, "configurePlatformRuntimeEndpoint(s)")
	start := strings.Index(src, "stopAppInstalls := s.startPlatformFrontendInstallService()")
	stopped := strings.Index(src, "serve(srv, st, stopAppInstalls")
	if bind < 0 || start < 0 || stopped < 0 || !(bind < start && start < stopped) {
		t.Fatal("RunServer must bind the runner endpoint, then start the installer, then hand its stop to serve", bind, start, stopped)
	}
}

// An unavailable frontend scheduler must not strand the managed operations: the operation
// engine still starts when an endpoint is bound (the reverse holds too), and each failure is
// logged. Startup does not abort, because the frontend scheduler is optional for managed work
// and aborting would take the whole Portal down for a worker-identity failure.
func TestPlatformInstallServiceStartsTheOperationEngineWhenTheFrontendSchedulerFails(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	calls := 0
	old := platformInstallerUUID
	platformInstallerUUID = func() (string, error) {
		calls++
		if calls == 1 {
			return "", errors.New("entropy unavailable")
		}
		return old()
	}
	t.Cleanup(func() { platformInstallerUUID = old })
	s := &Server{st: newTestStore(t)}
	endpoint := writableServiceTestEndpoint()
	s.platformBackendEndpoint.Store(&endpoint)
	stop := s.startPlatformFrontendInstallService()
	r := &s.platformInstaller
	if r.worker != nil || r.writer == nil || !r.writer.writable {
		t.Fatal("the operation engine was not started after the frontend scheduler failed")
	}
	if !strings.Contains(buf.String(), "frontend service unavailable") {
		t.Fatal("the frontend failure was not logged:", buf.String())
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	stop(ctx)
	for name, done := range map[string]chan struct{}{"frontend": r.done, "engine": r.writerDone} {
		select {
		case <-done:
		default:
			t.Fatalf("shutdown returned before the %s exited", name)
		}
	}
}

// ...and the engine's own failure leaves the frontend scheduler running.
func TestPlatformInstallServiceKeepsTheFrontendSchedulerWhenTheOperationEngineFails(t *testing.T) {
	calls := 0
	old := platformInstallerUUID
	platformInstallerUUID = func() (string, error) {
		calls++
		if calls == 2 {
			return "", errors.New("entropy unavailable")
		}
		return old()
	}
	t.Cleanup(func() { platformInstallerUUID = old })
	s := &Server{st: newTestStore(t)}
	endpoint := writableServiceTestEndpoint()
	s.platformBackendEndpoint.Store(&endpoint)
	stop := s.startPlatformFrontendInstallService()
	r := &s.platformInstaller
	if r.worker == nil || r.writer != nil {
		t.Fatal("frontend scheduler or engine state is wrong")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	stop(ctx)
}
