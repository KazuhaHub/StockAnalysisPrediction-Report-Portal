package control

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

const ImageBlobFormat = "portal.runner.image-blob-command/1-dev.1"
const imageBlobSignaturePurpose = "portal.app/runner-image-blob-command/v1"

// ImageBlobProofPurpose frames the controller's answer to the runner's fresh nonce challenge on the blob channel.
const ImageBlobProofPurpose = "portal.app/runner-image-blob-proof/v1"

// ImageBlobPayload declares one blob a session may upload: its digest, role, size and, for a layer, the diff_id and
// the uncompressed bound the runner enforces while it decompresses. Uploading is staging only. The command names
// no effect and authorizes no native call.
type ImageBlobPayload struct {
	Digest            string `json:"digest"`
	Kind              string `json:"kind"`
	MediaType         string `json:"media_type"`
	Size              string `json:"size"`
	ResumeOffset      string `json:"resume_offset"`
	DiffID            string `json:"diff_id,omitempty"`
	UncompressedBytes string `json:"uncompressed_bytes,omitempty"`
}

type ImageBlobCommand struct {
	Format          string           `json:"format"`
	DeploymentID    string           `json:"deployment_id"`
	ControllerEpoch string           `json:"controller_epoch"`
	ControlRevision string           `json:"control_revision"`
	InstallationID  string           `json:"installation_id"`
	OperationID     string           `json:"operation_id"`
	IssuedMS        string           `json:"issued_ms"`
	ExpiresMS       string           `json:"expires_ms"`
	Payload         ImageBlobPayload `json:"payload"`
}

type VerifiedImageBlobCommand struct{ command ImageBlobCommand }

func (v VerifiedImageBlobCommand) Command() ImageBlobCommand { return v.command }

// ImageBlobDeclaration is the checked numeric form of a payload.
type ImageBlobDeclaration struct {
	Blob         appplatform.ImageBlob
	ResumeOffset int64
}

// Declaration returns the typed blob a valid command declares.
func (c ImageBlobCommand) Declaration() (ImageBlobDeclaration, bool) {
	if !validImageBlobLogical(c) {
		return ImageBlobDeclaration{}, false
	}
	p := c.Payload
	size, _ := counter(p.Size, true)
	resume, _ := counter(p.ResumeOffset, false)
	blob := appplatform.ImageBlob{Digest: p.Digest, Kind: appplatform.ImageBlobKind(p.Kind), MediaType: p.MediaType, Size: size, DiffID: p.DiffID}
	if p.UncompressedBytes != "" {
		blob.UncompressedBytes, _ = counter(p.UncompressedBytes, true)
	}
	return ImageBlobDeclaration{Blob: blob, ResumeOffset: resume}, true
}

func validImageBlobLogical(c ImageBlobCommand) bool {
	if c.Format != ImageBlobFormat {
		return false
	}
	for _, id := range []string{c.DeploymentID, c.ControllerEpoch, c.InstallationID, c.OperationID} {
		if !candidateUUID(id) {
			return false
		}
	}
	if _, ok := counter(c.ControlRevision, true); !ok {
		return false
	}
	p := c.Payload
	size, ok := counter(p.Size, true)
	resume, resumeOK := counter(p.ResumeOffset, false)
	if !shaPattern.MatchString(p.Digest) || !ok || !resumeOK || resume > size {
		return false
	}
	switch appplatform.ImageBlobKind(p.Kind) {
	case appplatform.ImageBlobManifest:
		return (p.MediaType == appplatform.MediaTypeOCIManifest || p.MediaType == appplatform.MediaTypeDockerManifest) &&
			size <= appplatform.MaxImageMetadataBytes && p.DiffID == "" && p.UncompressedBytes == ""
	case appplatform.ImageBlobConfig:
		return (p.MediaType == appplatform.MediaTypeOCIConfig || p.MediaType == appplatform.MediaTypeDockerConfig) &&
			size <= appplatform.MaxImageMetadataBytes && p.DiffID == "" && p.UncompressedBytes == ""
	case appplatform.ImageBlobLayer:
		_, known := appplatform.ImageBlobCompressed(p.MediaType)
		uncompressed, uncompressedOK := counter(p.UncompressedBytes, true)
		return known && size <= appplatform.MaxImageBlobBytes && shaPattern.MatchString(p.DiffID) &&
			uncompressedOK && uncompressed <= appplatform.MaxImageLayerUncompressedBytes
	}
	return false
}

func verifyImageBlob(raw, signature []byte, authority Authority, nowMS int64) (VerifiedImageBlobCommand, error) {
	c, err := parseCanonicalCommand[ImageBlobCommand](raw)
	if err != nil || !validImageBlobLogical(c) {
		return VerifiedImageBlobCommand{}, ErrInvalidCommand
	}
	// The envelope check needs only the authority, window and signature; the blob command has no effect header.
	header := CommandHeader{DeploymentID: c.DeploymentID, ControllerEpoch: c.ControllerEpoch, ControlRevision: c.ControlRevision, IssuedMS: c.IssuedMS, ExpiresMS: c.ExpiresMS}
	if !verifyEnvelope(header, raw, signature, authority, nowMS, false, imageBlobSignaturePurpose, func() (string, error) { return "", nil }) {
		// verifyEnvelope compares the logical digest to the header's payload digest; the blob command has none, so
		// the comparison below is a deliberate no-op: both sides are empty.
		return VerifiedImageBlobCommand{}, ErrInvalidCommand
	}
	return VerifiedImageBlobCommand{command: c}, nil
}

func VerifyImageBlob(raw, signature []byte, authority Authority, nowMS int64) (VerifiedImageBlobCommand, error) {
	return verifyImageBlob(raw, signature, authority, nowMS)
}

func SignImageBlob(c ImageBlobCommand, key ed25519.PrivateKey, authority Authority, nowMS int64) ([]byte, []byte, error) {
	if len(key) != ed25519.PrivateKeySize {
		return nil, nil, ErrInvalidCommand
	}
	raw, err := json.Marshal(c)
	if err != nil {
		return nil, nil, ErrInvalidCommand
	}
	signature := ed25519.Sign(key, frame(imageBlobSignaturePurpose, raw))
	if _, err := VerifyImageBlob(raw, signature, authority, nowMS); err != nil {
		return nil, nil, err
	}
	return raw, signature, nil
}

func imageBlobProofMessage(raw []byte, nonceHex string) []byte {
	sum := sha256.Sum256(raw)
	return append(sum[:], nonceHex...)
}

// SignImageBlobProof answers the runner's nonce challenge: it binds the exact signed command bytes to a nonce the
// runner just made, so a captured command cannot open a session on another connection.
func SignImageBlobProof(raw []byte, nonceHex string, key ed25519.PrivateKey) []byte {
	return ed25519.Sign(key, frame(ImageBlobProofPurpose, imageBlobProofMessage(raw, nonceHex)))
}

func VerifyImageBlobProof(raw []byte, nonceHex string, proof []byte, authority Authority) bool {
	if len(authority.PublicKey) != ed25519.PublicKeySize || len(proof) != ed25519.SignatureSize {
		return false
	}
	if _, err := hex.DecodeString(nonceHex); err != nil || len(nonceHex) != 64 {
		return false
	}
	return ed25519.Verify(ed25519.PublicKey(authority.PublicKey), frame(ImageBlobProofPurpose, imageBlobProofMessage(raw, nonceHex)), proof)
}
