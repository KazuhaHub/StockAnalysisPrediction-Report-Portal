//go:build linux && (amd64 || arm64)

package app

import (
	"context"
	"time"
)

func (s *Server) requestWritableFixtureBoot(ctx context.Context, actor platformActorSession, installation string, endpoint platformRuntimeEndpoint) (string, error) {
	a, err := s.preparePlatformWritableBackendAccess(ctx, actor, installation)
	if err != nil {
		return "", err
	}
	defer a.Close()
	signer, err := a.intent.registration.Signer("")
	if err != nil {
		return "", err
	}
	defer clear(signer.Key)
	challenge, err := signer.NewChallenge(time.Now())
	if err != nil {
		return "", err
	}
	proof, err := platformHandshakeWritableBackend(ctx, endpoint, a.intent, challenge)
	if err != nil {
		return "", err
	}
	if !validPlatformWritableHandshakeObservation(proof, a.intent, endpoint, challenge) {
		return "", errAppPlatformStageEvidence
	}
	return signer.VerifyAnswer(challenge, proof.Evidence().Answer, time.Now())
}
