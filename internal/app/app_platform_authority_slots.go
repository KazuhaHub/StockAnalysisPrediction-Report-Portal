package app

import "sync"

// platformAuthorityConcurrency bounds how many session-less authority reads (the public projection's
// and the ingress one) may run at once. They are reachable by anyone who can send a request, so under a
// flood the database is protected by refusing the overflow with a 503 instead of queueing it.
const platformAuthorityConcurrency = 32

// platformAuthoritySlots is a non-blocking counting semaphore. The zero value is ready to use.
type platformAuthoritySlots struct {
	once sync.Once
	ch   chan struct{}
}

func (s *platformAuthoritySlots) init() {
	s.once.Do(func() { s.ch = make(chan struct{}, platformAuthorityConcurrency) })
}

// acquire takes a slot and reports whether there was one; it never waits.
func (s *platformAuthoritySlots) acquire() bool {
	s.init()
	select {
	case s.ch <- struct{}{}:
		return true
	default:
		return false
	}
}

func (s *platformAuthoritySlots) release() {
	s.init()
	select {
	case <-s.ch:
	default:
	}
}
