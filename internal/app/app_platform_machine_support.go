package app

import (
	"context"
	"errors"
	"time"
)

// Whether the runner forwards machine assertions (doc 183 section 4.7) is asked with a separate runner
// query and remembered briefly, so a request does not cost a connection of its own. A runner that does
// not answer is one the capability is not offered against: enabling is refused, and a request is
// answered 503 before anything is relayed. (An older runner also refuses the token itself: a machine
// assertion does not match its two-part pattern, so the upgrade fails closed either way.)

const (
	platformMachineSupportTTL     = time.Minute
	platformMachineSupportFailTTL = 10 * time.Second
)

var errPlatformMachineRunner = errors.New("the runner does not forward machine assertions")

type platformMachineSupportState struct {
	path      string
	until     time.Time
	supported bool
}

// machineRunnerSupport reports whether the runner at endpoint serves machine assertions. A failure to
// ask is "no" for a short time, not a conclusion about the runner.
func (s *Server) machineRunnerSupport(ctx context.Context, endpoint platformRuntimeEndpoint, now time.Time) bool {
	s.platformMachine.mu.Lock()
	cached := s.platformMachine.support
	s.platformMachine.mu.Unlock()
	if cached.path == endpoint.path && now.Before(cached.until) {
		return cached.supported
	}
	query := platformQueryMachineSupport
	if s.platformMachineSupportQuery != nil {
		query = s.platformMachineSupportQuery
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	got, err := query(ctx, endpoint)
	supported := err == nil && got.Supports()
	ttl := platformMachineSupportTTL
	if !supported {
		ttl = platformMachineSupportFailTTL
	}
	s.platformMachine.mu.Lock()
	s.platformMachine.support = platformMachineSupportState{path: endpoint.path, until: now.Add(ttl), supported: supported}
	s.platformMachine.mu.Unlock()
	return supported
}

// platformMachineRunnerState is "supported", "unsupported" or "unknown" (there is no runner endpoint to
// ask), for the administrator's preview.
func (s *Server) platformMachineRunnerState(ctx context.Context) string {
	endpoint := s.platformBackendEndpoint.Load()
	if endpoint == nil {
		return "unknown"
	}
	if s.machineRunnerSupport(ctx, *endpoint, time.Now()) {
		return "supported"
	}
	return "unsupported"
}
