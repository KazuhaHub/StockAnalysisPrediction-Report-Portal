package control

import (
	"crypto/ed25519"
	"strings"
	"testing"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

func imageBlobFixture(t *testing.T) (ImageBlobCommand, ed25519.PrivateKey, Authority, int64) {
	t.Helper()
	checkpoint, key, authority, now := checkpointFixture(t)
	return ImageBlobCommand{
		Format: ImageBlobFormat, DeploymentID: authority.DeploymentID, ControllerEpoch: authority.ControllerEpoch,
		ControlRevision: "3", InstallationID: checkpoint.InstallationID, OperationID: checkpoint.OperationID,
		IssuedMS: "29000", ExpiresMS: "54000",
		Payload: ImageBlobPayload{
			Digest: strings.Repeat("5", 64), Kind: "layer", MediaType: "application/vnd.oci.image.layer.v1.tar+gzip",
			Size: "4096", ResumeOffset: "0", DiffID: strings.Repeat("6", 64), UncompressedBytes: "8192",
		},
	}, key, authority, now
}

func TestImageBlobCommandRoundTripsUnderItsOwnPurpose(t *testing.T) {
	command, key, authority, now := imageBlobFixture(t)
	raw, signature, err := SignImageBlob(command, key, authority, now)
	if err != nil {
		t.Fatal(err)
	}
	verified, err := VerifyImageBlob(raw, signature, authority, now)
	if err != nil || verified.Command() != command {
		t.Fatal("round trip", err)
	}
	for name, purpose := range map[string]string{
		"load image purpose":   loadImageSignaturePurpose,
		"migrate purpose":      migrateDataSignaturePurpose,
		"prepare data purpose": "portal.app/runner-command/v1",
		"image blob proof":     ImageBlobProofPurpose,
	} {
		if _, err := VerifyImageBlob(raw, ed25519.Sign(key, frame(purpose, raw)), authority, now); err == nil {
			t.Fatalf("%s signature accepted", name)
		}
	}
	if _, err := VerifyImageBlob(raw, signature, authority, 54000); err == nil {
		t.Fatal("expired blob command accepted")
	}
}

// Every case is a command the runner must refuse before it touches scratch.
func TestImageBlobCommandRefusesMalformedPayloads(t *testing.T) {
	cases := map[string]func(*ImageBlobCommand){
		"unknown kind":               func(c *ImageBlobCommand) { c.Payload.Kind = "index" },
		"layer without diff id":      func(c *ImageBlobCommand) { c.Payload.DiffID = "" },
		"layer without uncompressed": func(c *ImageBlobCommand) { c.Payload.UncompressedBytes = "" },
		"manifest with diff id": func(c *ImageBlobCommand) {
			c.Payload.Kind, c.Payload.MediaType = "manifest", appplatform.MediaTypeOCIManifest
		},
		"layer media type foreign": func(c *ImageBlobCommand) {
			c.Payload.MediaType = "application/vnd.oci.image.layer.nondistributable.v1.tar+gzip"
		},
		"layer media type zstd":   func(c *ImageBlobCommand) { c.Payload.MediaType = "application/vnd.oci.image.layer.v1.tar+zstd" },
		"layer over 256 MiB":      func(c *ImageBlobCommand) { c.Payload.Size = "268435457" },
		"zero size":               func(c *ImageBlobCommand) { c.Payload.Size = "0" },
		"resume beyond size":      func(c *ImageBlobCommand) { c.Payload.ResumeOffset = "4097" },
		"resume leading zero":     func(c *ImageBlobCommand) { c.Payload.ResumeOffset = "00" },
		"uncompressed over 1 GiB": func(c *ImageBlobCommand) { c.Payload.UncompressedBytes = "1073741825" },
		"digest with prefix":      func(c *ImageBlobCommand) { c.Payload.Digest = "sha256:" + strings.Repeat("5", 64) },
		"config over 1 MiB": func(c *ImageBlobCommand) {
			c.Payload.Kind, c.Payload.MediaType, c.Payload.DiffID, c.Payload.UncompressedBytes = "config", "application/vnd.oci.image.config.v1+json", "", ""
			c.Payload.Size = "1048577"
		},
		"config with media type of a layer": func(c *ImageBlobCommand) {
			c.Payload.Kind, c.Payload.DiffID, c.Payload.UncompressedBytes = "config", "", ""
		},
		"foreign format": func(c *ImageBlobCommand) { c.Format = LoadImageFormat },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			command, key, authority, now := imageBlobFixture(t)
			mutate(&command)
			if raw, sig, err := SignImageBlob(command, key, authority, now); err == nil {
				t.Fatalf("malformed blob command signed: %s %x", raw, sig)
			}
		})
	}
}

func TestImageBlobProofBindsCommandAndNonce(t *testing.T) {
	command, key, authority, now := imageBlobFixture(t)
	raw, _, err := SignImageBlob(command, key, authority, now)
	if err != nil {
		t.Fatal(err)
	}
	nonce := strings.Repeat("ab", 32)
	proof := SignImageBlobProof(raw, nonce, key)
	if !VerifyImageBlobProof(raw, nonce, proof, authority) {
		t.Fatal("valid proof refused")
	}
	if VerifyImageBlobProof(raw, strings.Repeat("cd", 32), proof, authority) {
		t.Fatal("proof accepted for a different nonce")
	}
	other := append([]byte(nil), raw...)
	other[len(other)-3] ^= 1
	if VerifyImageBlobProof(other, nonce, proof, authority) {
		t.Fatal("proof accepted for a different command")
	}
	if VerifyImageBlobProof(raw, nonce, ed25519.Sign(key, frame(imageBlobSignaturePurpose, []byte(nonce))), authority) {
		t.Fatal("proof accepted under another purpose")
	}
}
