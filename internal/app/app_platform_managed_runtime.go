package app

import (
	"bytes"
	"context"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/transport"
)

type platformManagedRuntimeFacts struct {
	runtime       platformRuntimeRegistration
	observation   transport.ManagedRuntimeObservation
	network       *platformNetworkRuntimeFacts
	networkPolicy string
	streamPolicy  string
}

func platformRuntimeAuthority(r platformRuntimeRegistration) control.Authority {
	return control.Authority{DeploymentID: r.deploymentID, ControllerEpoch: r.controllerEpoch, ControlRevision: r.controlRevision, PublicKey: bytes.Clone(r.publicKey), Fingerprint: r.fingerprint}
}
func samePlatformManagedRuntime(a, b platformRuntimeRegistration) bool {
	return a.id == b.id && a.deploymentID == b.deploymentID && a.controllerEpoch == b.controllerEpoch && a.revision == b.revision && a.controlRevision == b.controlRevision && a.fingerprint == b.fingerprint && bytes.Equal(a.publicKey, b.publicKey)
}
func validPlatformManagedRuntimeObservation(o transport.ManagedRuntimeObservation, r platformRuntimeRegistration, e platformRuntimeEndpoint) bool {
	age := time.Since(o.ReceivedAt())
	peer := o.Endpoint()
	a := o.Controller()
	p := o.Profile()
	return !o.ReceivedAt().IsZero() && age >= 0 && age <= transport.MaxManagedRuntimeAge && peer.RootController && peer.RunnerUID == 0 && e.controllerUID > 0 && e.controllerGID > 0 && peer.Path == e.path && peer.ControllerUID == e.controllerUID && peer.ControllerGID == e.controllerGID && a.DeploymentID == r.deploymentID && a.ControllerEpoch == r.controllerEpoch && a.ControlRevision == r.controlRevision && a.Fingerprint == r.fingerprint && bytes.Equal(a.PublicKey, r.publicKey) && !p.ExecutionReady && p.Protocol == transport.ManagedRunnerProtocol && p.BackendProtocol == transport.ManagedBackendProtocol && p.SandboxProfile == control.CandidateSandboxProfile && (p.Platform == "linux/amd64" || p.Platform == "linux/arm64")
}

// observePlatformManagedRuntime performs OS IO outside SQL transactions. It
// checks the current admin/control binding before and after the fresh profile,
// and issues no install intent, grant, runtime enablement or readiness state.
func (s *Server) observePlatformManagedRuntime(ctx context.Context, session platformActorSession, endpoint platformRuntimeEndpoint) (platformManagedRuntimeFacts, error) {
	return s.observePlatformManagedRuntimeMode(ctx, session, endpoint, false)
}

// observePlatformRecoveryRuntime is the same observation for a paused Portal in recovery, where the
// replacement runtime is the only binding in force and the ordinary transaction would refuse.
func (s *Server) observePlatformRecoveryRuntime(ctx context.Context, session platformActorSession, endpoint platformRuntimeEndpoint) (platformManagedRuntimeFacts, error) {
	return s.observePlatformManagedRuntimeMode(ctx, session, endpoint, true)
}

func (s *Server) observePlatformManagedRuntimeMode(ctx context.Context, session platformActorSession, endpoint platformRuntimeEndpoint, recovery bool) (platformManagedRuntimeFacts, error) {
	var result platformManagedRuntimeFacts
	var actorPin string
	capture := func(first bool) error {
		return s.withPlatformRuntimeActorContext(ctx, session, recovery, func(e migExec, actor platformActorFacts, c platformActorControl, _ appplatform.PublisherTrust) error {
			dek, err := s.dek()
			if err != nil {
				return err
			}
			current, seed, err := s.platformDataRuntime(e, c.epoch, dek)
			clear(seed)
			if err != nil {
				return err
			}
			if first {
				result.runtime = current
				actorPin = actor.authorityFingerprint
				return nil
			}
			if actorPin != actor.authorityFingerprint || !samePlatformManagedRuntime(result.runtime, current) || !validPlatformManagedRuntimeObservation(result.observation, current, endpoint) {
				return errAppPlatformRuntimeConflict
			}
			return nil
		})
	}
	if err := capture(true); err != nil {
		return platformManagedRuntimeFacts{}, err
	}
	observation, err := platformReadManagedRuntime(ctx, endpoint, platformRuntimeAuthority(result.runtime))
	if err != nil {
		return platformManagedRuntimeFacts{}, err
	}
	result.observation = observation
	if err := capture(false); err != nil {
		return platformManagedRuntimeFacts{}, err
	}
	if ctx.Err() != nil || !validPlatformManagedRuntimeObservation(observation, result.runtime, endpoint) {
		return platformManagedRuntimeFacts{}, errAppPlatformRuntimeConflict
	}
	return result, nil
}

// Compatibility is version/platform matching only. Existing required-capability,
// installation review, native execution and readiness gates remain mandatory.
func platformManagedRuntimeCompatibility(m appplatform.Manifest, f platformManagedRuntimeFacts, e platformRuntimeEndpoint) (appplatform.CompatibilityResult, error) {
	if m.Backend == nil || !validPlatformManagedRuntimeObservation(f.observation, f.runtime, e) {
		return appplatform.CompatibilityResult{}, errAppPlatformInstallUnavailable
	}
	p := f.observation.Profile()
	return appplatform.EvaluateCompatibility(m, appplatform.CompatibilityTarget{PlatformAPI: platformDevelopmentAPI, Runner: &appplatform.RunnerCompatibility{Protocol: p.Protocol, Platform: p.Platform, BackendProtocol: p.BackendProtocol}})
}

func validPlatformInstallRuntimeFacts(f platformManagedRuntimeFacts, e platformRuntimeEndpoint) bool {
	if !validPlatformManagedRuntimeObservation(f.observation, f.runtime, e) {
		return false
	}
	if f.networkPolicy == "" && f.streamPolicy == "" {
		return f.network == nil
	}
	return f.network != nil && samePlatformManagedRuntime(f.network.runtime, f.runtime) && validPlatformNetworkRuntimeObservation(f.network.observation, f.runtime, e)
}

// withPlatformRuntimeActorContext is withPlatformTrustActorContext, or its recovery-mode counterpart.
func (s *Server) withPlatformRuntimeActorContext(ctx context.Context, session platformActorSession, recovery bool, fn func(migExec, platformActorFacts, platformActorControl, appplatform.PublisherTrust) error) error {
	if !recovery {
		return s.withPlatformTrustActorContext(ctx, session, fn)
	}
	return s.withPlatformRecoveryTransactionContext(ctx, session, func(e migExec, actor platformActorFacts, control platformActorControl) error {
		if err := lockPlatformPublisherTrust(e); err != nil {
			return err
		}
		trust, err := loadPlatformPublisherTrust(e)
		if err != nil {
			return err
		}
		control, err = platformRefreshActor(e, session, actor, control)
		if err != nil {
			return err
		}
		return fn(e, actor, control, trust)
	})
}

// platformRecoveryResumeTarget reads the target a resume is reviewed against from the replacement
// runner itself. A request never supplies it.
func (s *Server) platformRecoveryResumeTarget(ctx context.Context, session platformActorSession, endpoint platformRuntimeEndpoint) (*platformManagedInstallTarget, error) {
	if s.platformRecoveryTarget != nil {
		return s.platformRecoveryTarget(ctx, session, endpoint)
	}
	facts, err := s.observePlatformRecoveryRuntime(ctx, session, endpoint)
	if err != nil {
		return nil, err
	}
	return managedInstallTarget(facts, endpoint)
}
