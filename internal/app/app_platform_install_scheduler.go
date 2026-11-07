package app

import (
	"context"
	"errors"
	"log"
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

// An import-checkpoint is a bulk data action. The connection that carries it is bounded at two
// minutes by the runner, but a claimed import runs on, detached, for up to journal.ImportCheckpointBound.
// An attempt therefore waits a little longer than the connection, ends inside the 180 second operation
// lease, and the Portal polls: an attempt that ends because the runner is still working is not a
// failure, and the next one reads the receipt of the same effect (platformBusyWaitLimit of them cover
// the longest bound).
// platformImportAttemptContext is the context a bulk attempt runs under: the parent bounded by the attempt timeout. Tests
// replace it to end an attempt at a moment of their choosing instead of by the clock.
var platformImportAttemptContext = func(parent context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(parent, platformImportAttemptTimeout)
}

var platformImportAttemptTimeout = 2*time.Minute + 20*time.Second

// platformBusyWaitLimit bounds how many attempts in a row may end because the runner was still busy
// with the same effect before they count as failures.
const platformBusyWaitLimit = 20

func platformAttemptTimeout(lease platformOperationLease) time.Duration {
	if lease.snapshot.Action == appplatform.ActionRestoreImport && lease.snapshot.Stage == appplatform.StageRestoreData {
		return platformImportAttemptTimeout
	}
	// A migration job runs on, detached, for up to its wall clock; the attempt waits like an import's.
	if lease.snapshot.Action == appplatform.ActionUpdate && lease.snapshot.Managed && lease.snapshot.Stage == appplatform.StageMigrate {
		return platformImportAttemptTimeout
	}
	return platformInstallAttemptTimeout
}

type platformInstallWorkerStatus struct {
	code, operationID string
	stage             appplatform.Stage
	attempts          int
}

// One explicitly owned service instance. Construction does not start it;
// endpoint/worker identity come only from trusted bootstrap.
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
	waits    int
	writable bool
	// reconciledAt is when recovery-mode reconciliation last ran.
	reconciledAt time.Time
}

func newPlatformInstallScheduler(s *Server, endpoint platformRuntimeEndpoint, worker string, interval time.Duration) (*platformInstallScheduler, error) {
	frontendOnly := endpoint == (platformRuntimeEndpoint{})
	if s == nil || s.st == nil || !platformValidUUID(worker) || interval < 50*time.Millisecond || interval > 30*time.Second || (!frontendOnly && (!filepath.IsAbs(endpoint.path) || filepath.Clean(endpoint.path) != endpoint.path || len(endpoint.path) > 107 || strings.ContainsAny(endpoint.path, "\x00\r\n") || endpoint.controllerUID == 0 || endpoint.controllerGID == 0 || endpoint.controllerUID == ^uint32(0) || endpoint.controllerGID == ^uint32(0))) {
		return nil, errAppPlatformRuntimeConflict
	}
	return &platformInstallScheduler{server: s, endpoint: endpoint, worker: worker, interval: interval, gate: make(chan struct{}, 1)}, nil
}

// Explicit development bootstrap only; the portable production service keeps
// its frontend-only constructor and cannot select managed installations.
func newPlatformWritableInstallScheduler(s *Server, endpoint platformRuntimeEndpoint, worker string, interval time.Duration) (*platformInstallScheduler, error) {
	if endpoint == (platformRuntimeEndpoint{}) {
		return nil, errAppPlatformRuntimeConflict
	}
	w, err := newPlatformInstallScheduler(s, endpoint, worker, interval)
	if err != nil {
		return nil, err
	}
	w.writable = true
	return w, nil
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
	bulkCtx, bulkCancel := platformImportAttemptContext(parent)
	defer bulkCancel()
	if err := ctx.Err(); err != nil {
		return err
	}
	if w.active.id == "" {
		var selected platformInstallSelection
		var err error
		if w.endpoint.path != "" && w.server.platformRecoveryPaused(ctx) {
			// A Portal in recovery runs restore imports and resumes and nothing else; every other selector
			// would refuse on the pause and mask the import behind its error. The frontend-only
			// service has no runner endpoint, so it keeps refusing on the pause as before.
			selected, err = w.server.claimNextPlatformRestoreImport(ctx, w.worker)
			if err == nil && !selected.reviewed && selected.lease.id == "" {
				selected, err = w.server.claimNextPlatformRestoreResume(ctx, w.worker)
			}
			if err == nil && !selected.reviewed && selected.lease.id == "" && time.Since(w.reconciledAt) >= platformRecoveryReconcileInterval {
				// Idle: read back any import left unsettled by an earlier attempt.
				w.reconciledAt = time.Now()
				if _, reconcileErr := w.server.reconcilePlatformRecoveryImports(ctx, w.endpoint); reconcileErr != nil {
					log.Printf("app-platform recovery reconcile: %v", reconcileErr)
				}
				// A resume whose start is settled as applied comes back as a lease at the probe stage, which
				// the scheduler runs on like any claimed operation.
				if resumed, reconcileErr := w.server.reconcilePlatformRecoveryResumes(ctx, w.endpoint, w.worker); reconcileErr != nil {
					log.Printf("app-platform recovery resume reconcile: %v", reconcileErr)
				} else {
					selected = resumed
				}
			}
		} else if w.writable {
			selected, err = w.server.claimNextPlatformWritableInstall(ctx, w.worker)
			if err == nil && !selected.reviewed && selected.lease.id == "" {
				selected, err = w.server.claimNextPlatformManagedUpdate(ctx, w.worker)
			}
			if err == nil && !selected.reviewed && selected.lease.id == "" {
				selected, err = w.server.claimNextPlatformManagedRepair(ctx, w.worker)
			}
			if err == nil && !selected.reviewed && selected.lease.id == "" {
				selected, err = w.server.claimNextPlatformManagedEnable(ctx, w.worker)
			}
			if err == nil && !selected.reviewed && selected.lease.id == "" {
				selected, err = w.server.claimNextPlatformDeleteData(ctx, w.worker)
			}
			if err == nil && !selected.reviewed && selected.lease.id == "" {
				selected, err = w.server.claimNextPlatformManagedUninstall(ctx, w.worker)
			}
			if err == nil && !selected.reviewed && selected.lease.id == "" {
				selected, err = w.server.claimNextPlatformManagedRestore(ctx, w.worker)
			}
		} else if w.endpoint.path == "" {
			selected, err = w.server.claimNextPlatformFrontendInstall(ctx, w.worker)
			if err == nil && !selected.reviewed && selected.lease.id == "" {
				selected, err = w.server.claimNextPlatformFrontendUpdate(ctx, w.worker)
			}
			if err == nil && !selected.reviewed && selected.lease.id == "" {
				selected, err = w.server.claimNextPlatformFrontendEnable(ctx, w.worker)
			}
			if err == nil && !selected.reviewed && selected.lease.id == "" {
				selected, err = w.server.claimNextPlatformFrontendUninstall(ctx, w.worker)
			}
		} else {
			selected, err = w.server.claimNextPlatformStaticInstall(ctx, w.worker)
			if err == nil && !selected.reviewed && selected.lease.id == "" {
				selected, err = w.server.claimNextPlatformManagedEnable(ctx, w.worker)
			}
			if err == nil && !selected.reviewed && selected.lease.id == "" {
				selected, err = w.server.claimNextPlatformManagedUninstall(ctx, w.worker)
			}
			if err == nil && !selected.reviewed && selected.lease.id == "" {
				selected, err = w.server.claimNextPlatformManagedRestore(ctx, w.worker)
			}
		}
		if err != nil {
			w.publish("worker_unavailable", "", "")
			return err
		}
		if selected.reviewed {
			w.publish("review_required", selected.operationID, "")
			return nil
		}
		if selected.lease.id == "" {
			// Idle, in normal and in paused mode: a failed migration waiting for settlement needs no lease.
			if w.endpoint.path != "" {
				if _, settleErr := w.server.settlePlatformMigrations(bulkCtx, w.endpoint); settleErr != nil {
					log.Printf("app-platform migration settlement: %v", settleErr)
				}
			}
			w.publish("idle", "", "")
			return nil
		}
		w.active, w.attempts, w.waits = selected.lease, 0, 0
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
	if platformAttemptTimeout(w.active) == platformImportAttemptTimeout {
		ctx = bulkCtx
	}
	w.publish("running", w.active.id, w.active.snapshot.Stage)
	var result platformInstallRun
	if w.active.snapshot.Action == appplatform.ActionRestoreImport {
		result.lease, err = w.server.runPlatformRestoreImport(
			ctx, w.active, w.endpoint)
	} else if w.active.snapshot.Action == appplatform.ActionRestore &&
		w.active.snapshot.Managed {
		result.lease, err = w.server.runPlatformManagedRestore(
			ctx, w.active, w.endpoint)
	} else if w.active.snapshot.Action == appplatform.ActionUpdate &&
		w.active.snapshot.Managed {
		result.lease, err = w.server.runPlatformManagedUpdate(
			ctx, w.active, w.endpoint)
	} else if w.active.snapshot.Action == appplatform.ActionRepair &&
		w.active.snapshot.Managed {
		result.lease, err = w.server.runPlatformManagedRepair(
			ctx, w.active, w.endpoint)
	} else if w.active.snapshot.Action == appplatform.ActionEnable &&
		w.active.snapshot.Managed {
		result.lease, err = w.server.runPlatformManagedEnable(
			ctx, w.active, w.endpoint)
	} else if w.active.snapshot.Action == appplatform.ActionDeleteData &&
		w.active.snapshot.Managed {
		result, err = w.server.runPlatformDeleteData(
			ctx, w.active, w.endpoint)
	} else if w.active.snapshot.Action == appplatform.ActionDisable &&
		w.active.snapshot.Managed {
		result, err = w.server.runPlatformManagedUninstall(
			ctx, w.active, w.endpoint)
	} else if w.active.snapshot.Action == appplatform.ActionUninstall && w.active.snapshot.Managed {
		result, err = w.server.runPlatformManagedUninstall(ctx, w.active, w.endpoint)
	} else if w.writable {
		result, err = w.server.runPlatformWritableInstallMode(ctx, w.active, w.endpoint, true)
	} else if w.active.snapshot.Action == appplatform.ActionEnable {
		result.lease, err = w.server.runPlatformFrontendEnable(ctx, w.active)
	} else if w.active.snapshot.Action == appplatform.ActionDisable {
		result.lease, err = w.server.runPlatformFrontendDisable(ctx, w.active)
	} else if w.active.snapshot.Action == appplatform.ActionUpdate {
		result.lease, err = w.server.runPlatformStaticUpdate(ctx, w.active)
	} else if w.active.snapshot.Action == appplatform.ActionUninstall {
		result.lease, err = w.server.runPlatformFrontendUninstall(ctx, w.active)
	} else {
		result, err = w.server.runPlatformStaticInstall(ctx, w.active, w.endpoint)
	}
	if errors.Is(err, errPlatformImportEnded) || errors.Is(err, errPlatformMigrationEnded) {
		w.publish("review_required", w.active.id, "")
		w.active, w.attempts = platformOperationLease{}, 0
		return nil
	}
	if err == nil {
		if result.lease.id != "" &&
			result.lease.snapshot.Stage != appplatform.StageComplete {
			w.active, w.attempts = result.lease, 0
			w.publish("running", w.active.id,
				w.active.snapshot.Stage)
			return nil
		}
		w.publish("complete", w.active.id, appplatform.StageComplete)
		w.active, w.attempts = platformOperationLease{}, 0
		return nil
	}
	if result.lease.id == "" {
		result.lease = w.active
	}
	w.active = result.lease
	if parent.Err() != nil {
		w.publish("stopped", w.active.id, w.active.snapshot.Stage)
		return parent.Err()
	}
	if ((w.active.snapshot.Action == appplatform.ActionRestoreImport &&
		errors.Is(err, context.DeadlineExceeded) && ctx.Err() != nil) ||
		(w.active.snapshot.Action == appplatform.ActionUpdate && w.active.snapshot.Stage == appplatform.StageMigrate &&
			(errors.Is(err, errPlatformMigrationBusy) ||
				(errors.Is(err, context.DeadlineExceeded) && ctx.Err() != nil)))) &&
		w.waits < platformBusyWaitLimit {
		// The wait for the runner ran out while it was still working on the import: it has not failed.
		// The same effect is read again on the next attempt; nothing is sent under a new identity.
		w.waits++
		w.publish("retry_pending", w.active.id, w.active.snapshot.Stage)
		return err
	}
	w.waits = 0
	w.attempts++
	w.publish("retry_pending", w.active.id, w.active.snapshot.Stage)
	if w.attempts >= platformInstallAttemptLimit {
		return w.finish(ctx)
	}
	return err
}
func (w *platformInstallScheduler) finish(ctx context.Context) error {
	operation, stage := w.active.id, w.active.snapshot.Stage
	finish := w.server.finishPlatformInstallForReview
	if w.active.snapshot.Action == appplatform.ActionRestoreImport {
		finish = w.server.finishPlatformRestoreImportForReview
	} else if w.active.snapshot.Action == appplatform.ActionUpdate {
		finish = w.server.finishPlatformUpdateForReview
	} else if w.active.snapshot.Action == appplatform.ActionRepair &&
		w.active.snapshot.Managed {
		finish = w.server.finishPlatformManagedRepairForReview
	} else if w.active.snapshot.Action == appplatform.ActionEnable &&
		w.active.snapshot.Managed {
		finish = w.server.finishPlatformManagedEnableForReview
	} else if w.active.snapshot.Action == appplatform.ActionEnable &&
		!w.active.snapshot.Managed {
		finish = w.server.finishPlatformFrontendEnableForReview
	} else if w.active.snapshot.Action == appplatform.ActionRestore &&
		w.active.snapshot.Managed {
		finish = w.server.finishPlatformManagedRestoreForReview
	} else if w.active.snapshot.Action == appplatform.ActionDeleteData &&
		w.active.snapshot.Managed {
		finish = w.server.finishPlatformDeleteDataForReview
	} else if (w.active.snapshot.Action == appplatform.ActionDisable ||
		w.active.snapshot.Action == appplatform.ActionUninstall) &&
		w.active.snapshot.Managed {
		finish = w.server.finishPlatformManagedUninstallForReview
	} else if w.active.snapshot.Action == appplatform.ActionDisable ||
		w.active.snapshot.Action == appplatform.ActionUninstall {
		finish = w.server.finishPlatformFrontendUninstallForReview
	}
	if err := finish(ctx, w.active); err != nil {
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

// Portal startup owns the portable no-data service separately. Native execution
// still requires explicit bootstrap and the exact Linux UID.
// It does not enroll a runner, create OS identities, release uncertain leases or
// reclaim data effects. Cancellation waits for its current context-bound attempt.
func (w *platformInstallScheduler) serve(ctx context.Context) error {
	if (w.endpoint.path != "" && (runtime.GOOS != "linux" || uint32(os.Geteuid()) != w.endpoint.controllerUID)) || !w.started.CompareAndSwap(false, true) {
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
			if err := w.tick(ctx); err != nil && ctx.Err() == nil {
				log.Printf("app-platform scheduler tick: %v", err)
			}
			if ctx.Err() != nil {
				return nil
			}
			timer.Reset(w.interval)
		}
	}
}
