//go:build linux

package driver

import (
	"context"
	"path/filepath"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

func (r *CandidateRuntime) ExchangeUpdateStartBackendHandshake(ctx context.Context, request UpdateStartLaunchRequest, receipt UpdateStartRegistrationSnapshot, challenge string) (UpdateStartBackendHandshakeObservation, error) {
	var zero UpdateStartBackendHandshakeObservation
	if r == nil || !appplatform.ValidBackendHandshakeToken(challenge) ||
		receipt.Inode == 0 || !containerSHA.MatchString(receipt.BindingSHA256) ||
		!containerSHA.MatchString(receipt.DocumentSHA256) ||
		!containerSHA.MatchString(receipt.NativeID) {
		return zero, ErrContainerUncertain
	}
	var answer string
	actual, err := r.withVerifiedUpdateStartCandidate(
		ctx, request, receipt.Launch, receipt.NativeID,
		func(ctx context.Context, pins *candidateMountPins) error {
			check := func() error {
				return checkUpdateStartBackendFile(ctx, r, pins, receipt)
			}
			if err := check(); err != nil {
				return err
			}
			bounded, cancel := context.WithTimeout(ctx, 2*time.Second)
			defer cancel()
			path := filepath.Join(
				r.roots.Sockets, request.Identity.InstallationID,
				request.Identity.Generation, "http", "app.sock")
			var err error
			answer, err = exchangeBackendHandshake(
				bounded, path, 65532, 65532, challenge)
			if err != nil {
				return err
			}
			return check()
		})
	if err != nil {
		return zero, err
	}
	result := UpdateStartBackendHandshakeObservation{
		valid: true,
		snapshot: UpdateStartBackendHandshakeSnapshot{
			Registration:    receipt,
			ChallengeSHA256: containerHash([]byte(challenge)),
			Answer:          answer, ObservedMS: actual.ObservedMS,
		},
	}
	if _, err := result.Snapshot(); err != nil {
		return zero, err
	}
	return result, nil
}

func checkUpdateStartBackendFile(ctx context.Context, r *CandidateRuntime, pins *candidateMountPins, receipt UpdateStartRegistrationSnapshot) error {
	identity := receipt.Launch.Launch.Identity
	path := filepath.Join(
		r.roots.Sockets, identity.InstallationID,
		identity.Generation, "broker")
	parent := -1
	for _, pin := range pins.dirs {
		if pin.path == path {
			parent = int(pin.file.Fd())
			break
		}
	}
	if parent < 0 {
		return ErrUnsafeSocket
	}
	document, stat, err := readBackendRegistrationFile(
		ctx, parent, receipt.BindingSHA256, 65532, 65532)
	defer clear(document)
	if err != nil || uint64(stat.Dev) != receipt.Device ||
		stat.Ino != receipt.Inode ||
		containerHash(document) != receipt.DocumentSHA256 {
		return ErrUnsafeSocket
	}
	return ctx.Err()
}
