package app

import (
	"bytes"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"strconv"
	"testing"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
)

// Test-only cleanup ownership, never an execution command or native proof. This
// is written before the background service starts, so even interrupted execution
// has an independently bounded operation owner without staging a start by hand.
type platformWritableFixtureOwner struct {
	Format, DeploymentID, Epoch, OperationID, InstallationID, ReleaseSHA, Image string
	ControlRevision, IssuedMS, ExpiresMS                                        int64
}
type platformWritableFixtureOwnerEnvelope struct{ Raw, Signature []byte }

func platformWritableFixtureOwnerFrame(raw []byte) []byte {
	return append([]byte("portal.fixture.writable-install-owner/v1\x00"), raw...)
}
func verifyPlatformWritableFixtureOwner(encoded []byte, a control.Authority, image string, now int64) (platformWritableFixtureOwner, error) {
	var zero, p platformWritableFixtureOwner
	var envelope platformWritableFixtureOwnerEnvelope
	if len(encoded) == 0 || len(encoded) > 4096 || json.Unmarshal(encoded, &envelope) != nil || len(a.PublicKey) != ed25519.PublicKeySize || len(envelope.Signature) != ed25519.SignatureSize || !ed25519.Verify(a.PublicKey, platformWritableFixtureOwnerFrame(envelope.Raw), envelope.Signature) || json.Unmarshal(envelope.Raw, &p) != nil {
		return zero, errors.New("invalid fixture owner")
	}
	outer, _ := json.Marshal(envelope)
	inner, _ := json.Marshal(p)
	if !bytes.Equal(outer, encoded) || !bytes.Equal(inner, envelope.Raw) || p.Format != "portal.fixture.writable-install-owner/1-dev.1" || p.DeploymentID != a.DeploymentID || p.Epoch != a.ControllerEpoch || p.ControlRevision != a.ControlRevision || !platformValidUUID(p.OperationID) || !platformValidUUID(p.InstallationID) || !platformValidSHA(p.ReleaseSHA) || p.Image == "" || p.Image != image || p.IssuedMS < 0 || p.ExpiresMS <= p.IssuedMS || p.ExpiresMS-p.IssuedMS > 90000 || now < p.IssuedMS || now > p.ExpiresMS {
		return zero, errors.New("unbound fixture owner")
	}
	return p, nil
}

func TestPlatformWritableFixtureOwnership(t *testing.T) {
	key := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{8}, ed25519.SeedSize))
	a := control.Authority{DeploymentID: platformFixtureID(980), ControllerEpoch: platformFixtureID(981), ControlRevision: 1, PublicKey: key.Public().(ed25519.PublicKey)}
	p := platformWritableFixtureOwner{"portal.fixture.writable-install-owner/1-dev.1", a.DeploymentID, a.ControllerEpoch, platformFixtureID(982), platformFixtureID(983), platformDigest([]byte("release")), "fixture@sha256:" + platformDigest([]byte("image")), 1, 1000, 90000}
	sign := func(p platformWritableFixtureOwner) []byte {
		raw, _ := json.Marshal(p)
		out, _ := json.Marshal(platformWritableFixtureOwnerEnvelope{raw, ed25519.Sign(key, platformWritableFixtureOwnerFrame(raw))})
		return out
	}
	encoded := sign(p)
	if actual, err := verifyPlatformWritableFixtureOwner(encoded, a, p.Image, 2000); err != nil || actual != p {
		t.Fatal("valid fixture ownership refused", err)
	}
	for _, mode := range []string{"signature", "epoch", "deployment", "revision", "operation", "installation", "release", "image", "future", "expired", "lifetime", "canonical"} {
		t.Run(mode, func(t *testing.T) {
			q := p
			now := int64(2000)
			copyBytes := bytes.Clone(encoded)
			switch mode {
			case "signature":
				copyBytes[len(copyBytes)-5] ^= 1
			case "epoch":
				q.Epoch = platformFixtureID(984)
			case "deployment":
				q.DeploymentID = platformFixtureID(984)
			case "revision":
				q.ControlRevision++
			case "operation":
				q.OperationID = "invalid"
			case "installation":
				q.InstallationID = "invalid"
			case "release":
				q.ReleaseSHA = "invalid"
			case "image":
				q.Image = "other"
			case "future":
				now = 999
			case "expired":
				now = 90001
			case "lifetime":
				q.ExpiresMS = 91001
			case "canonical":
				copyBytes = append(copyBytes, ' ')
			}
			if mode != "signature" && mode != "canonical" {
				copyBytes = sign(q)
			}
			if _, err := verifyPlatformWritableFixtureOwner(copyBytes, a, p.Image, now); err == nil {
				t.Fatal("unbound fixture ownership accepted", strconv.Quote(mode))
			}
		})
	}
}
