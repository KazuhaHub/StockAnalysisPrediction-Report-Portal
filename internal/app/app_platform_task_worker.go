package app

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync/atomic"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/config"
)

const platformTaskExecutionTimeout = 45 * time.Second

type PlatformTaskWorkerOptions struct {
	WorkerID string
	Interval time.Duration
	Once     bool
}

type platformTaskScheduler struct {
	server   *Server
	worker   string
	interval time.Duration
	gate     chan struct{}
	started  atomic.Bool
}

func newPlatformTaskScheduler(server *Server, worker string, interval time.Duration) (*platformTaskScheduler, error) {
	if server == nil || server.st == nil || !platformValidUUID(worker) ||
		interval < 50*time.Millisecond || interval > 30*time.Second {
		return nil, errAppPlatformTaskUnavailable
	}
	return &platformTaskScheduler{
		server: server, worker: worker, interval: interval,
		gate: make(chan struct{}, 1),
	}, nil
}

func (w *platformTaskScheduler) run(ctx context.Context, once bool) error {
	if !w.started.CompareAndSwap(false, true) {
		return errAppPlatformTaskUnavailable
	}
	defer w.started.Store(false)
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()
	for {
		worked, err := w.tick(ctx)
		if err != nil && !errors.Is(err, context.Canceled) {
			log.Printf("application task worker: %v", err)
		}
		if once {
			return err
		}
		if worked {
			continue
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (w *platformTaskScheduler) tick(ctx context.Context) (bool, error) {
	select {
	case w.gate <- struct{}{}:
	case <-ctx.Done():
		return false, ctx.Err()
	}
	defer func() { <-w.gate }()
	task, err := w.server.claimNextPlatformTask(ctx, w.worker)
	if err != nil {
		return false, err
	}
	if task.ID == "" {
		return false, nil
	}
	started, identity, err := w.server.startPlatformTask(ctx, task)
	if err != nil {
		code := "start_conflict"
		if errors.Is(err, errAppPlatformTaskUnavailable) {
			code = "task_authority_lost"
		}
		_ = w.server.markPlatformTaskUncertain(
			task.ID, task.WorkerID, task.LeaseID, task.Fence,
			task.Revision, code)
		return true, err
	}
	defer clear([]byte(identity))
	timeout := platformTaskExecutionTimeout
	remaining := time.Until(time.UnixMilli(started.LeaseUntilMS))
	if remaining <= 0 {
		return true, w.server.markPlatformTaskUncertain(started.ID, w.worker, started.LeaseID, started.Fence, started.Revision, "lease_expired")
	}
	if remaining < timeout {
		timeout = remaining
	}
	execCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	result := w.server.executePlatformTask(execCtx, started, identity)
	if result.Outcome == appplatform.TaskResultRetry {
		result.RetryDelay = platformTaskDuration(started.Attempts)
		if result.RetryDelay == 0 {
			result = appplatform.TaskExecutionResult{Outcome: appplatform.TaskResultFailed, ErrorCode: "retry_exhausted"}
		}
	}
	if execCtx.Err() != nil && result.Outcome == "" {
		result = appplatform.TaskExecutionResult{Outcome: appplatform.TaskResultUncertain, ErrorCode: "execution_interrupted"}
	}
	if err := w.server.completePlatformTask(context.WithoutCancel(ctx), started, identity, result); err != nil {
		if errors.Is(err, errAppPlatformTaskConflict) {
			return true, w.server.markPlatformTaskUncertain(
				started.ID, started.WorkerID, started.LeaseID, started.Fence,
				started.Revision, "completion_conflict")
		}
		return true, err
	}
	return true, nil
}

func (s *Server) executePlatformTask(ctx context.Context, task platformTaskLease, identity string) (result appplatform.TaskExecutionResult) {
	defer func() {
		if recover() != nil {
			result = appplatform.TaskExecutionResult{
				Outcome: appplatform.TaskResultUncertain, ErrorCode: "handler_panic",
			}
		}
	}()
	switch task.Handler {
	case appplatform.TaskHandlerDigest:
		result, err := platformTaskDigestResult(task)
		if err != nil {
			return appplatform.TaskExecutionResult{Outcome: appplatform.TaskResultFailed, ErrorCode: "result_unavailable"}
		}
		return appplatform.TaskExecutionResult{
			Outcome: appplatform.TaskResultSucceeded, ResultJSON: result,
			EvidenceSHA: platformDigest(result),
		}
	case appplatform.TaskHandlerBackend:
		if s.platformTaskBackend == nil {
			return appplatform.TaskExecutionResult{
				Outcome: appplatform.TaskResultFailed, ErrorCode: "backend_handler_unavailable",
			}
		}
		return s.platformTaskBackend(ctx, task, identity)
	default:
		return appplatform.TaskExecutionResult{
			Outcome: appplatform.TaskResultFailed, ErrorCode: "handler_unavailable",
		}
	}
}

func RunPlatformTaskWorker(ctx context.Context, cfgPath string, options PlatformTaskWorkerOptions) error {
	cfg, err := config.EnsureConfig(cfgPath)
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}
	if err := validateSessionSecret(cfg.SecretKey); err != nil {
		return fmt.Errorf("config: %w", err)
	}
	worker := options.WorkerID
	if worker == "" {
		worker, err = platformNewUUID()
		if err != nil {
			return err
		}
	}
	if options.Interval == 0 {
		options.Interval = time.Second
	}
	store, err := OpenStore(cfg.DBDriver, cfg.DBSource())
	if err != nil {
		return fmt.Errorf("db: %w", err)
	}
	defer store.Close()
	server := &Server{cfg: cfg, st: store}
	configurePlatformRuntimeEndpoint(server)
	server.platformTaskBackend = server.runPlatformBackendTask
	scheduler, err := newPlatformTaskScheduler(server, worker, options.Interval)
	if err != nil {
		return err
	}
	log.Printf("application task worker %s started", worker)
	return scheduler.run(ctx, options.Once)
}
