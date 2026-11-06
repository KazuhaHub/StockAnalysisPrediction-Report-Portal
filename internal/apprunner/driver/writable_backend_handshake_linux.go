//go:build linux

package driver

import (
	"context"
	"path/filepath"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

// Fixed handshake endpoint over the UID/peer/inode-bound private socket. The
// complete process/private-storage checks surround an exact protected-file read
// both before and after HTTP. No secret bytes leave this native boundary.
func (r *CandidateRuntime) ExchangeWritableBackendHandshake(ctx context.Context, request WritableCandidateLaunchRequest, receipt WritableBackendRegistrationSnapshot, challenge string) (WritableBackendHandshakeObservation, error) {
	var zero WritableBackendHandshakeObservation
	if r == nil || !appplatform.ValidBackendHandshakeToken(challenge) || receipt.Inode == 0 || !containerSHA.MatchString(receipt.BindingSHA256) || !containerSHA.MatchString(receipt.DocumentSHA256) || !containerSHA.MatchString(receipt.NativeID) {
		return zero, ErrContainerUncertain
	}
	var answer string
	actual, err := r.withVerifiedWritableCandidate(ctx, request, receipt.Launch, receipt.NativeID, func(ctx context.Context, pins *candidateMountPins) error {
		check := func() error { return r.checkWritableBackendFile(ctx, pins, receipt) }
		if err := check(); err != nil {
			return err
		}
		bounded, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		path := filepath.Join(r.roots.Sockets, request.Identity.InstallationID, request.Identity.Generation, "http", "app.sock")
		var err error
		answer, err = exchangeBackendHandshake(bounded, path, 65532, 65532, challenge)
		if err != nil {
			return err
		}
		return check()
	})
	if err != nil {
		return zero, err
	}
	result := WritableBackendHandshakeObservation{valid: true, snapshot: WritableBackendHandshakeSnapshot{Registration: receipt, ChallengeSHA256: containerHash([]byte(challenge)), Answer: answer, ObservedMS: actual.ObservedMS}}
	if _, err := result.Snapshot(); err != nil {
		return zero, err
	}
	return result, nil
}
func (r *CandidateRuntime) checkWritableBackendFile(ctx context.Context, pins *candidateMountPins, receipt WritableBackendRegistrationSnapshot) error {
	i := receipt.Launch.Launch.Identity
	path := filepath.Join(r.roots.Sockets, i.InstallationID, i.Generation, "broker")
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
	document, st, err := readBackendRegistrationFile(ctx, parent, receipt.BindingSHA256, 65532, 65532)
	defer clear(document)
	if err != nil || uint64(st.Dev) != receipt.Device || st.Ino != receipt.Inode || containerHash(document) != receipt.DocumentSHA256 {
		return ErrUnsafeSocket
	}
	return ctx.Err()
}
