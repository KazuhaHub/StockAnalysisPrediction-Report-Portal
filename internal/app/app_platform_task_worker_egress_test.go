package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

// The background egress coordinator belongs to the long-running worker only: a
// --once run executes the due tasks and exits without holding any session open.
func TestPlatformTaskWorkerOnceDoesNotStartTheEgressCoordinator(t *testing.T) {
	var started atomic.Int32
	previous := newPlatformServiceEgressRunnerForWorker
	newPlatformServiceEgressRunnerForWorker = func() platformServiceEgressTransport {
		started.Add(1)
		return newFakeEgressRunner()
	}
	t.Cleanup(func() { newPlatformServiceEgressRunnerForWorker = previous })
	run := func(once bool) {
		t.Helper()
		dir := t.TempDir()
		if err := os.MkdirAll(filepath.Join(dir, "data"), 0o755); err != nil {
			t.Fatal(err)
		}
		t.Chdir(dir)
		ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
		defer cancel()
		err := RunPlatformTaskWorker(ctx, filepath.Join(dir, "config.yaml"), PlatformTaskWorkerOptions{Interval: 100 * time.Millisecond, Once: once})
		if err != nil && !errors.Is(err, context.DeadlineExceeded) {
			t.Fatal("worker failed:", err)
		}
	}
	run(true)
	if started.Load() != 0 {
		t.Fatal("--once started the egress coordinator")
	}
	// The same harness does start it for a long-running worker.
	run(false)
	if started.Load() != 1 {
		t.Fatal("the long-running worker did not start the egress coordinator", started.Load())
	}
}
