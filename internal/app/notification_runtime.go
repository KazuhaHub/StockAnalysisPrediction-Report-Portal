package app

import (
	"context"
	"log"
	"sync"
	"time"
)

// startNotificationRuntime starts a fixed-size pool independent of authentication
// mail. Only delivery metadata is queued in memory; durable work remains in SQL.
// The returned stop function is safe to call repeatedly.
func (s *Server) startNotificationRuntime() func(context.Context) {
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Go(func() {
		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()
		var lastCleanup time.Time
		for {
			if ctx.Err() != nil {
				return
			}
			if err := s.st.RecoverNotificationLeases(time.Now().UTC()); err != nil {
				log.Print("notification: lease recovery failed")
			}
			if err := s.recoverJobNotifications(); err != nil {
				log.Print("notification: job notification recovery failed")
			}
			if time.Since(lastCleanup) >= time.Hour {
				if err := s.st.CleanupNotifications(time.Now().UTC()); err != nil {
					log.Print("notification: retention cleanup failed")
				}
				lastCleanup = time.Now()
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	})
	for range 4 {
		wg.Go(func() {
			ticker := time.NewTicker(250 * time.Millisecond)
			defer ticker.Stop()
			for {
				if ctx.Err() != nil {
					return
				}
				// Recover panics per work unit. A possibly transmitted request is
				// left leased; recovery classifies it uncertain instead of resending.
				func() {
					defer func() {
						if recover() != nil {
							log.Print("notification: delivery worker panicked")
						}
					}()
					if err := s.processNotificationOnce(ctx, time.Now().UTC()); err != nil {
						log.Print("notification: delivery processing failed")
					}
				}()
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
				}
			}
		})
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	return func(deadline context.Context) {
		cancel()
		select {
		case <-done:
		case <-deadline.Done():
			log.Print("notification: worker shutdown deadline exceeded")
		}
	}
}
