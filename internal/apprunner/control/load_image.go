package control

import (
	"crypto/ed25519"
	"encoding/json"
	"strconv"
	"strings"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

const LoadImageFormat = "portal.runner.load-image-command/1-dev.1"
const loadImageSignaturePurpose = "portal.app/runner-load-image-command/v1"
const loadImageReadPurpose = "portal.app/runner-load-image-read/v1"
const loadImageEffectPurpose = "portal.app/runner-load-image-effect/v1"

// LoadImageStage and LoadImageKind name the effect in the journal and Portal stage vocabularies. The effect
// number is not fixed: Portal takes the next number free in the stage for the operation (doc 170 resolution 9),
// and the runner enforces only its uniqueness per operation and stage.
const LoadImageStage = "stage"
const LoadImageKind = "load-image"

// LoadImagePayload names the one image a runner loads: the release whose descriptor binds it, the platform and the
// descriptor's image entry. The layers are a string because the command parser accepts objects and strings only:
// diff_id:uncompressed_bytes pairs in image order, joined by commas.
type LoadImagePayload struct {
	ReleaseSHA256  string `json:"release_sha256"`
	Platform       string `json:"platform"`
	ManifestDigest string `json:"manifest_digest"`
	ConfigDigest   string `json:"config_digest"`
	Layers         string `json:"layers"`
}

type LoadImageCommand struct {
	CommandHeader
	Payload LoadImagePayload `json:"payload"`
}

type VerifiedLoadImageCommand struct{ command LoadImageCommand }

func (v VerifiedLoadImageCommand) Command() LoadImageCommand { return v.command }

// FormatLoadImageLayers is the payload encoding of a descriptor's layer list.
func FormatLoadImageLayers(layers []appplatform.ImageLayer) string {
	parts := make([]string, len(layers))
	for i, layer := range layers {
		parts[i] = layer.DiffID + ":" + strconv.Itoa(layer.UncompressedBytes)
	}
	return strings.Join(parts, ",")
}

// ParseLoadImageLayers decodes the payload layer list. It accepts only the canonical encoding, so the signed
// string and the list the runner binds to the descriptor cannot differ.
func ParseLoadImageLayers(value string) ([]appplatform.ImageLayer, bool) {
	parts := strings.Split(value, ",")
	if value == "" || len(parts) > appplatform.MaxImageLayers {
		return nil, false
	}
	layers := make([]appplatform.ImageLayer, 0, len(parts))
	for _, part := range parts {
		diff, size, ok := strings.Cut(part, ":")
		n, valid := counter(size, true)
		if !ok || !shaPattern.MatchString(diff) || !valid || n > appplatform.MaxImageLayerUncompressedBytes {
			return nil, false
		}
		layers = append(layers, appplatform.ImageLayer{DiffID: diff, UncompressedBytes: int(n)})
	}
	return layers, true
}

func validLoadImageLogical(c LoadImageCommand) bool {
	if !validHeaderAnySlot(c.CommandHeader, LoadImageFormat, LoadImageStage, LoadImageKind) {
		return false
	}
	p := c.Payload
	if !shaPattern.MatchString(p.ReleaseSHA256) || !shaPattern.MatchString(p.ManifestDigest) || !shaPattern.MatchString(p.ConfigDigest) ||
		p.ManifestDigest == p.ConfigDigest || (p.Platform != "linux/amd64" && p.Platform != "linux/arm64") {
		return false
	}
	_, ok := ParseLoadImageLayers(p.Layers)
	return ok
}

// validHeaderAnySlot is validHeader for an effect whose number Portal allocates, within the journal's bound.
func validHeaderAnySlot(h CommandHeader, format, stage, kind string) bool {
	slot, ok := counter(h.EffectNumber, true)
	return ok && slot <= 32 && validHeader(h, format, stage, kind, h.EffectNumber)
}

func LoadImageLogicalDigest(c LoadImageCommand) (string, error) {
	if !validLoadImageLogical(c) {
		return "", ErrInvalidCommand
	}
	return logicalDigest(loadImageEffectPurpose, "portal.runner.load-image-effect/1-dev.1", c.CommandHeader, c.Payload)
}

func verifyLoadImage(raw, signature []byte, authority Authority, nowMS int64, settling bool, purpose string) (VerifiedLoadImageCommand, error) {
	c, err := parseCanonicalCommand[LoadImageCommand](raw)
	if err != nil || !verifyEnvelope(c.CommandHeader, raw, signature, authority, nowMS, settling, purpose, func() (string, error) { return LoadImageLogicalDigest(c) }) {
		return VerifiedLoadImageCommand{}, ErrInvalidCommand
	}
	return VerifiedLoadImageCommand{command: c}, nil
}

func VerifyLoadImage(raw, signature []byte, authority Authority, nowMS int64) (VerifiedLoadImageCommand, error) {
	return verifyLoadImage(raw, signature, authority, nowMS, false, loadImageSignaturePurpose)
}

// VerifyLoadImageSettlement checks the exact command already admitted for an in-flight load. Expiry governs
// dispatch; a load that finishes after the signed window must still be settled.
func VerifyLoadImageSettlement(raw, signature []byte, authority Authority, nowMS int64) (VerifiedLoadImageCommand, error) {
	return verifyLoadImage(raw, signature, authority, nowMS, true, loadImageSignaturePurpose)
}

// VerifyLoadImageRead checks a read-back command, signed under its own purpose and not bound by the window.
func VerifyLoadImageRead(raw, signature []byte, authority Authority, nowMS int64) (VerifiedLoadImageCommand, error) {
	return verifyLoadImage(raw, signature, authority, nowMS, true, loadImageReadPurpose)
}

func SignLoadImage(c LoadImageCommand, key ed25519.PrivateKey, authority Authority, nowMS int64) ([]byte, []byte, error) {
	if len(key) != ed25519.PrivateKeySize {
		return nil, nil, ErrInvalidCommand
	}
	raw, err := json.Marshal(c)
	if err != nil {
		return nil, nil, ErrInvalidCommand
	}
	signature := ed25519.Sign(key, frame(loadImageSignaturePurpose, raw))
	if _, err := VerifyLoadImage(raw, signature, authority, nowMS); err != nil {
		return nil, nil, err
	}
	return raw, signature, nil
}

func SignLoadImageRead(raw []byte, key ed25519.PrivateKey, authority Authority, nowMS int64) ([]byte, error) {
	if len(key) != ed25519.PrivateKeySize {
		return nil, ErrInvalidCommand
	}
	signature := ed25519.Sign(key, frame(loadImageReadPurpose, raw))
	if _, err := VerifyLoadImageRead(raw, signature, authority, nowMS); err != nil {
		return nil, err
	}
	return signature, nil
}
