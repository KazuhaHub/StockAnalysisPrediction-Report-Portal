package app

import (
	"context"
	"log"
	"sync"
	"time"
)

// One owner per Portal process. Durable authority, pause and pending work remain
// in SQL; this owner never initializes control or enrolls a native runtime.
//
// It always runs the frontend scheduler. When a trusted runner endpoint is bound
// (a valid RP_APP_RUNNER_* set, checked at startup), it also runs the platform's
// operation engine against that endpoint (see newPlatformOperationEngine for
// everything it runs). The two claim disjoint queues, hold separate worker
// identities and are stopped together.
type platformFrontendInstallRuntime struct {
	once   sync.Once
	cancel context.CancelFunc
	done   chan struct{}
	worker *platformInstallScheduler
	// writer is nil when no runner endpoint is bound.
	writer     *platformInstallScheduler
	writerDone chan struct{}
}

// platformInstallerUUID names the schedulers' workers. Tests replace it to fail construction.
var platformInstallerUUID = platformNewUUID

// startPlatformFrontendInstallService starts the platform's operation engine and returns its stop
// function. The two schedulers are independent: a failure to build one never prevents the other
// from running, because an unavailable frontend scheduler must not leave the managed operations
// (and the runner reconciliation they carry) stranded, nor the reverse. Each failure is logged.
func (s *Server) startPlatformFrontendInstallService() func(context.Context) {
	r := &s.platformInstaller
	r.once.Do(func() {
		ctx, cancel := context.WithCancel(context.Background())
		r.cancel, r.done = cancel, make(chan struct{})
		id, err := platformInstallerUUID()
		if err == nil {
			r.worker, err = newPlatformInstallScheduler(s, platformRuntimeEndpoint{}, id, 500*time.Millisecond)
		}
		if err != nil {
			close(r.done)
			log.Print("application installation: frontend service unavailable")
		} else {
			go r.run(ctx)
		}
		if endpoint := s.platformBackendEndpoint.Load(); endpoint != nil {
			r.writerDone = make(chan struct{})
			writerID, err := platformInstallerUUID()
			if err == nil {
				r.writer, err = newPlatformOperationEngine(s, *endpoint, writerID, 500*time.Millisecond)
			}
			if err != nil {
				close(r.writerDone)
				log.Print("application installation: operation engine unavailable")
				return
			}
			go r.runWritable(ctx)
		}
	})
	return func(deadline context.Context) {
		r.cancel()
		for _, done := range []chan struct{}{r.done, r.writerDone} {
			if done == nil {
				continue
			}
			select {
			case <-done:
			case <-deadline.Done():
				log.Print("application installation: worker shutdown deadline exceeded")
			}
		}
	}
}

func (r *platformFrontendInstallRuntime) run(ctx context.Context) {
	defer close(r.done)
	defer func() {
		if recover() != nil {
			// Stop rather than retry a panic. Keep any durable lease/evidence for
			// the normal fenced recovery rules; never release it optimistically.
			r.worker.status.Store(&platformInstallWorkerStatus{code: "stopped"})
			log.Print("application installation: worker stopped unexpectedly")
		}
	}()
	if err := r.worker.serve(ctx); err != nil {
		log.Print("application installation: service unavailable")
	}
}

func (r *platformFrontendInstallRuntime) runWritable(ctx context.Context) {
	defer close(r.writerDone)
	defer func() {
		if recover() != nil {
			r.writer.status.Store(&platformInstallWorkerStatus{code: "stopped"})
			log.Print("application installation: writable worker stopped unexpectedly")
		}
	}()
	if err := r.writer.serve(ctx); err != nil {
		log.Print("application installation: writable service unavailable")
	}
}
