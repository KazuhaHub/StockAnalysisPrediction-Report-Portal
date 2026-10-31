package app

import (
	"bytes"
	"context"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/transport"
	"time"
)

type platformNetworkRuntimeFacts struct {
	runtime     platformRuntimeRegistration
	observation transport.NetworkRuntimeObservation
}

func (platformNetworkRuntimeFacts) MarshalJSON() ([]byte, error) {
	return nil, errAppPlatformRuntimeConflict
}
func (f *platformNetworkRuntimeFacts) UnmarshalJSON([]byte) error {
	*f = platformNetworkRuntimeFacts{}
	return errAppPlatformRuntimeConflict
}
func (platformNetworkRuntimeFacts) String() string     { return "network runtime observation (redacted)" }
func (f platformNetworkRuntimeFacts) GoString() string { return f.String() }

func validPlatformNetworkRuntimeObservation(o transport.NetworkRuntimeObservation, r platformRuntimeRegistration, e platformRuntimeEndpoint) bool {
	age := time.Since(o.ReceivedAt())
	peer := o.Endpoint()
	a := o.Controller()
	return !o.ReceivedAt().IsZero() && age >= 0 && age <= transport.MaxManagedRuntimeAge && peer.RootController && peer.RunnerUID == 0 && e.controllerUID > 0 && e.controllerGID > 0 && peer.Path == e.path && peer.ControllerUID == e.controllerUID && peer.ControllerGID == e.controllerGID && a.DeploymentID == r.deploymentID && a.ControllerEpoch == r.controllerEpoch && a.ControlRevision == r.controlRevision && a.Fingerprint == r.fingerprint && bytes.Equal(a.PublicKey, r.publicKey) && o.Profile().Format == transport.NetworkRuntimeFormat
}

// OS IO runs between two current administrator/control transactions. An observed
// network family is a prerequisite for review, never a grant or app readiness.
// No runtime is auto-enrolled and no request is executed by this read.
func (s *Server) observePlatformNetworkRuntime(ctx context.Context, session platformActorSession, endpoint platformRuntimeEndpoint) (platformNetworkRuntimeFacts, error) {
	var result platformNetworkRuntimeFacts
	var actorPin string
	capture := func(first bool) error {
		return s.withPlatformTrustActorContext(ctx, session, func(e migExec, actor platformActorFacts, c platformActorControl, _ appplatform.PublisherTrust) error {
			dek, err := s.readPlatformDEK(e)
			if err != nil {
				return err
			}
			defer clear(dek)
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
			if actorPin != actor.authorityFingerprint || !samePlatformManagedRuntime(result.runtime, current) || !validPlatformNetworkRuntimeObservation(result.observation, current, endpoint) {
				return errAppPlatformRuntimeConflict
			}
			_, err = platformRefreshActor(e, session, actor, c)
			return err
		})
	}
	if err := capture(true); err != nil {
		return platformNetworkRuntimeFacts{}, err
	}
	observation, err := platformReadNetworkRuntime(ctx, endpoint)
	if err != nil {
		return platformNetworkRuntimeFacts{}, err
	}
	result.observation = observation
	if err := capture(false); err != nil {
		return platformNetworkRuntimeFacts{}, err
	}
	if ctx.Err() != nil || !validPlatformNetworkRuntimeObservation(observation, result.runtime, endpoint) {
		return platformNetworkRuntimeFacts{}, errAppPlatformRuntimeConflict
	}
	return result, nil
}
