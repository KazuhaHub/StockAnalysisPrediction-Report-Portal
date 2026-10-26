package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

const platformInstallAttemptLimit = 3
const platformInstallAttemptTimeout = 20 * time.Second

type platformInstallWorkerStatus struct {
	code, operationID string
	stage             appplatform.Stage
	attempts          int
}

// One explicitly owned service instance. No constructor or production startup
// implicitly starts it; endpoint/worker identity come only from trusted bootstrap.
type platformInstallScheduler struct {
	server   *Server
	endpoint platformRuntimeEndpoint
	worker   string
	interval time.Duration
	gate     chan struct{}
	started  atomic.Bool
	status   atomic.Pointer[platformInstallWorkerStatus]
	active   platformOperationLease
	attempts int
}

func newPlatformInstallScheduler(s *Server, endpoint platformRuntimeEndpoint, worker string, interval time.Duration) (*platformInstallScheduler, error) {
	if s == nil || s.st == nil || !platformValidUUID(worker) || interval < 50*time.Millisecond || interval > 30*time.Second || !filepath.IsAbs(endpoint.path) || filepath.Clean(endpoint.path) != endpoint.path || len(endpoint.path) > 107 || strings.ContainsAny(endpoint.path, "\x00\r\n") || endpoint.controllerUID == 0 || endpoint.controllerGID == 0 || endpoint.controllerUID == ^uint32(0) || endpoint.controllerGID == ^uint32(0) {
		return nil, errAppPlatformRuntimeConflict
	}
	return &platformInstallScheduler{server: s, endpoint: endpoint, worker: worker, interval: interval, gate: make(chan struct{}, 1)}, nil
}
func (w *platformInstallScheduler) currentStatus() platformInstallWorkerStatus {
	if status := w.status.Load(); status != nil {
		return *status
	}
	return platformInstallWorkerStatus{code: "stopped"}
}
func (w *platformInstallScheduler) publish(code, operation string, stage appplatform.Stage) {
	w.status.Store(&platformInstallWorkerStatus{code: code, operationID: operation, stage: stage, attempts: w.attempts})
}
func (w *platformInstallScheduler) tick(parent context.Context) error {
	select {
	case w.gate <- struct{}{}:
	case <-parent.Done():
		return parent.Err()
	}
	defer func() { <-w.gate }()
	ctx, cancel := context.WithTimeout(parent, platformInstallAttemptTimeout)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return err
	}
	if w.active.id == "" {
		selected, err := w.server.claimNextPlatformStaticInstall(ctx, w.worker)
		if err != nil {
			w.publish("worker_unavailable", "", "")
			return err
		}
		if selected.reviewed {
			w.publish("review_required", selected.operationID, "")
			return nil
		}
		if selected.lease.id == "" {
			w.publish("idle", "", "")
			return nil
		}
		w.active, w.attempts = selected.lease, 0
	}
	if w.attempts >= platformInstallAttemptLimit {
		return w.finish(ctx)
	}
	// Renewal happens only at a serial stage boundary. A heartbeat cannot race
	// the driver's revision-changing transactions or resurrect an expired lease.
	renewed, err := w.server.st.updatePlatformOperationLeaseContext(ctx, w.active.id, w.worker, &w.active)
	if err != nil {
		operation, stage := w.active.id, w.active.snapshot.Stage
		if errors.Is(err, errAppPlatformLeaseConflict) {
			w.active = platformOperationLease{}
			w.attempts = 0
		}
		w.publish("worker_unavailable", operation, stage)
		return err
	}
	w.active = renewed
	w.publish("running", w.active.id, w.active.snapshot.Stage)
	result, err := w.server.runPlatformStaticInstall(ctx, w.active, w.endpoint)
	if err == nil {
		w.publish("complete", w.active.id, appplatform.StageComplete)
		w.active, w.attempts = platformOperationLease{}, 0
		return nil
	}
	w.active = result.lease
	if parent.Err() != nil {
		w.publish("stopped", w.active.id, w.active.snapshot.Stage)
		return parent.Err()
	}
	w.attempts++
	w.publish("retry_pending", w.active.id, w.active.snapshot.Stage)
	if w.attempts >= platformInstallAttemptLimit {
		return w.finish(ctx)
	}
	return err
}
func (w *platformInstallScheduler) finish(ctx context.Context) error {
	operation, stage := w.active.id, w.active.snapshot.Stage
	if err := w.server.finishPlatformInstallForReview(ctx, w.active); err != nil {
		if errors.Is(err, errAppPlatformLeaseConflict) {
			w.active = platformOperationLease{}
			w.attempts = 0
		}
		w.publish("worker_unavailable", operation, stage)
		return err
	}
	w.publish("review_required", operation, "")
	w.active, w.attempts = platformOperationLease{}, 0
	return nil
}

// serve is opt-in Linux infrastructure, never started by existing Portal startup.
// It does not enroll a runner, create OS identities, release uncertain leases or
// reclaim data effects. Cancellation waits for its current context-bound attempt.
func (w *platformInstallScheduler) serve(ctx context.Context) error {
	if runtime.GOOS != "linux" || uint32(os.Geteuid()) != w.endpoint.controllerUID || !w.started.CompareAndSwap(false, true) {
		return errAppPlatformRuntimeConflict
	}
	timer := time.NewTimer(0)
	defer timer.Stop()
	defer func() { w.status.Store(&platformInstallWorkerStatus{code: "stopped"}) }()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-timer.C:
			_ = w.tick(ctx)
			if ctx.Err() != nil {
				return nil
			}
			timer.Reset(w.interval)
		}
	}
}
