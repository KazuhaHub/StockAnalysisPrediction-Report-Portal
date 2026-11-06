package app

import (
	"context"
	"log"
	"sync"
	"time"
)

// One owner per Portal process. Durable authority, pause and pending work remain
// in SQL; this owner never initializes control or enrolls a native runtime.
type platformFrontendInstallRuntime struct {
	once   sync.Once
	cancel context.CancelFunc
	done   chan struct{}
	worker *platformInstallScheduler
}

func (s *Server) startPlatformFrontendInstallService() func(context.Context) {
	r := &s.platformInstaller
	r.once.Do(func() {
		ctx, cancel := context.WithCancel(context.Background())
		r.cancel, r.done = cancel, make(chan struct{})
		id, err := platformNewUUID()
		if err == nil {
			r.worker, err = newPlatformInstallScheduler(s, platformRuntimeEndpoint{}, id, 500*time.Millisecond)
		}
		if err != nil {
			cancel()
			close(r.done)
			log.Print("application installation: service unavailable")
			return
		}
		go r.run(ctx)
	})
	return func(deadline context.Context) {
		r.cancel()
		select {
		case <-r.done:
		case <-deadline.Done():
			log.Print("application installation: worker shutdown deadline exceeded")
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
