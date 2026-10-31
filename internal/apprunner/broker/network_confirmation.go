package broker

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
)

// NetworkEnrollment reads current trusted controller enrollment locally. Never
// take a public key, epoch or revision from a confirmation response itself.
type NetworkEnrollment func(context.Context) (control.Authority, error)

// NetworkConfirmation must use a private peer-authenticated controller exchange.
// The controller must resolve current SQL/viewer/grant authority before signing.
// This callback is not a public HTTP endpoint and is not implemented by apps.
type NetworkConfirmation func(context.Context, control.NetworkHTTPRequestCommand, string) ([]byte, []byte, error)

// NewConfirmedNetworkAuthority provides the current check consumed by signed
// NetworkCall/NetworkPool. A fresh challenge is required on every invocation;
// neither a successful proof nor an enrollment observation is cached. It does
// not provision the private exchange or a real Portal SQL permission adapter.
func NewConfirmedNetworkAuthority(enrollment NetworkEnrollment, confirm NetworkConfirmation) (CurrentNetworkAuthority, error) {
	if enrollment == nil || confirm == nil {
		return nil, ErrDenied
	}
	return func(parent context.Context, c control.NetworkHTTPRequestCommand) (control.Authority, error) {
		var zero control.Authority
		if parent == nil || parent.Err() != nil {
			return zero, ErrDenied
		}
		ctx, cancel := context.WithTimeout(parent, time.Second)
		defer cancel()
		a, err := enrollment(ctx)
		if err != nil || ctx.Err() != nil || len(a.PublicKey) != ed25519.PublicKeySize {
			return zero, ErrDenied
		}
		a.PublicKey = bytes.Clone(a.PublicKey)
		// Do not allocate/copy an unbounded packet through an authority callback.
		if control.ValidateNetworkHTTPRequestMetadata(c, a, time.Now().UnixMilli()) != nil {
			return zero, ErrDenied
		}
		c.PolicyRaw = bytes.Clone(c.PolicyRaw)
		var nonce [32]byte
		if _, err := rand.Read(nonce[:]); err != nil {
			return zero, ErrDenied
		}
		challenge := hex.EncodeToString(nonce[:])
		sent := c
		sent.PolicyRaw = bytes.Clone(c.PolicyRaw)
		raw, sig, err := confirm(ctx, sent, challenge)
		if err != nil || ctx.Err() != nil {
			return zero, ErrDenied
		}
		// Enrollment may rotate while the controller checks SQL authority. The
		// returned proof is checked against a new trusted local observation.
		current, err := enrollment(ctx)
		if err != nil || ctx.Err() != nil || len(current.PublicKey) != ed25519.PublicKeySize {
			return zero, ErrDenied
		}
		current.PublicKey = bytes.Clone(current.PublicKey)
		if current.DeploymentID != a.DeploymentID || current.ControllerEpoch != a.ControllerEpoch || current.ControlRevision != a.ControlRevision || current.Fingerprint != a.Fingerprint || !bytes.Equal(current.PublicKey, a.PublicKey) {
			return zero, ErrDenied
		}
		if control.VerifyNetworkHTTPCheck(c, challenge, raw, sig, current, time.Now().UnixMilli()) != nil || ctx.Err() != nil {
			return zero, ErrDenied
		}
		return current, nil
	}, nil
}
