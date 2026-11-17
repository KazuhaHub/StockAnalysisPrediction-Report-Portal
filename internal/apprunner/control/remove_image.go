package control

import (
	"crypto/ed25519"
	"encoding/json"
	"sort"
	"strings"
)

const RemoveImageFormat = "portal.runner.remove-image-command/1-dev.1"
const removeImageSignaturePurpose = "portal.app/runner-remove-image-command/v1"
const removeImageReadPurpose = "portal.app/runner-remove-image-read/v1"
const removeImageEffectPurpose = "portal.app/runner-remove-image-effect/v1"

// RemoveImageStage and RemoveImageKind name the effect in the journal and Portal stage vocabularies. A removal
// shares the journal stage "stage" with the load (the closed stage list of runner_commands has no stage of its
// own, and none may be added), so effect number 1 stays the load's and a removal takes a number from 2 to 32.
const RemoveImageStage = LoadImageStage
const RemoveImageKind = "remove-image"

// MinRemoveImageEffectNumber is the first effect number a removal may take; number 1 of stage "stage" is the load.
const MinRemoveImageEffectNumber = 2

// MaxKnownLoads bounds the load effects a removal can name.
const MaxKnownLoads = 32

// RemoveImagePayload names the one image a runner removes. It has the fields of a load payload on purpose: the
// release whose descriptor binds the image, the platform and the descriptor's image entry. The runner derives the
// daemon's image ID from that entry exactly as the load does, so the removal acts on the ID the load produced.
//
// KnownLoads is the reference watermark: every load-image effect of this image the Portal saw applied on the
// runtime, as effect ids joined by commas in ascending order. The runner refuses the claim, typed, when its journal
// holds an applied or unknown load of the image that is not in the list: a load the Portal had not seen means the
// image may be referenced again.
type RemoveImagePayload struct {
	ReleaseSHA256  string `json:"release_sha256"`
	Platform       string `json:"platform"`
	ManifestDigest string `json:"manifest_digest"`
	ConfigDigest   string `json:"config_digest"`
	Layers         string `json:"layers"`
	KnownLoads     string `json:"known_loads"`
}

// LoadPayload is the load-shaped part of the payload, for binding to a descriptor.
func (p RemoveImagePayload) LoadPayload() LoadImagePayload {
	return LoadImagePayload{ReleaseSHA256: p.ReleaseSHA256, Platform: p.Platform, ManifestDigest: p.ManifestDigest, ConfigDigest: p.ConfigDigest, Layers: p.Layers}
}

// FormatKnownLoads is the canonical encoding of a set of load effect ids.
func FormatKnownLoads(ids []string) string {
	sorted := append([]string(nil), ids...)
	sort.Strings(sorted)
	return strings.Join(sorted, ",")
}

// ParseKnownLoads decodes the canonical encoding only: one to MaxKnownLoads distinct, ascending effect ids.
func ParseKnownLoads(value string) ([]string, bool) {
	if value == "" {
		return nil, false
	}
	ids := strings.Split(value, ",")
	if len(ids) > MaxKnownLoads {
		return nil, false
	}
	for i, id := range ids {
		if !candidateUUID(id) || (i > 0 && ids[i-1] >= id) {
			return nil, false
		}
	}
	return ids, true
}

type RemoveImageCommand struct {
	CommandHeader
	Payload RemoveImagePayload `json:"payload"`
}

type VerifiedRemoveImageCommand struct{ command RemoveImageCommand }

func (v VerifiedRemoveImageCommand) Command() RemoveImageCommand { return v.command }

func validRemoveImageLogical(c RemoveImageCommand) bool {
	slot, ok := counter(c.EffectNumber, true)
	if !ok || slot < MinRemoveImageEffectNumber || !validHeaderAnySlot(c.CommandHeader, RemoveImageFormat, RemoveImageStage, RemoveImageKind) {
		return false
	}
	if _, ok := ParseKnownLoads(c.Payload.KnownLoads); !ok {
		return false
	}
	return validLoadImageLogical(LoadImageCommand{CommandHeader: loadShapedHeader(c.CommandHeader), Payload: c.Payload.LoadPayload()})
}

// loadShapedHeader presents a remove header to the load payload checks, which validate the payload only.
func loadShapedHeader(h CommandHeader) CommandHeader {
	h.Format, h.Kind = LoadImageFormat, LoadImageKind
	return h
}

func RemoveImageLogicalDigest(c RemoveImageCommand) (string, error) {
	if !validRemoveImageLogical(c) {
		return "", ErrInvalidCommand
	}
	return logicalDigest(removeImageEffectPurpose, "portal.runner.remove-image-effect/1-dev.1", c.CommandHeader, c.Payload)
}

func verifyRemoveImage(raw, signature []byte, authority Authority, nowMS int64, settling bool, purpose string) (VerifiedRemoveImageCommand, error) {
	c, err := parseCanonicalCommand[RemoveImageCommand](raw)
	if err != nil || !verifyEnvelope(c.CommandHeader, raw, signature, authority, nowMS, settling, purpose, func() (string, error) { return RemoveImageLogicalDigest(c) }) {
		return VerifiedRemoveImageCommand{}, ErrInvalidCommand
	}
	return VerifiedRemoveImageCommand{command: c}, nil
}

func VerifyRemoveImage(raw, signature []byte, authority Authority, nowMS int64) (VerifiedRemoveImageCommand, error) {
	return verifyRemoveImage(raw, signature, authority, nowMS, false, removeImageSignaturePurpose)
}

// VerifyRemoveImageSettlement checks the exact command already admitted for an in-flight removal. Expiry governs
// dispatch; a removal that finishes after the signed window must still be settled.
func VerifyRemoveImageSettlement(raw, signature []byte, authority Authority, nowMS int64) (VerifiedRemoveImageCommand, error) {
	return verifyRemoveImage(raw, signature, authority, nowMS, true, removeImageSignaturePurpose)
}

// VerifyRemoveImageRead checks a read-back command, signed under its own purpose and not bound by the window.
func VerifyRemoveImageRead(raw, signature []byte, authority Authority, nowMS int64) (VerifiedRemoveImageCommand, error) {
	return verifyRemoveImage(raw, signature, authority, nowMS, true, removeImageReadPurpose)
}

func SignRemoveImage(c RemoveImageCommand, key ed25519.PrivateKey, authority Authority, nowMS int64) ([]byte, []byte, error) {
	if len(key) != ed25519.PrivateKeySize {
		return nil, nil, ErrInvalidCommand
	}
	raw, err := json.Marshal(c)
	if err != nil {
		return nil, nil, ErrInvalidCommand
	}
	signature := ed25519.Sign(key, frame(removeImageSignaturePurpose, raw))
	if _, err := VerifyRemoveImage(raw, signature, authority, nowMS); err != nil {
		return nil, nil, err
	}
	return raw, signature, nil
}

func SignRemoveImageRead(raw []byte, key ed25519.PrivateKey, authority Authority, nowMS int64) ([]byte, error) {
	if len(key) != ed25519.PrivateKeySize {
		return nil, ErrInvalidCommand
	}
	signature := ed25519.Sign(key, frame(removeImageReadPurpose, raw))
	if _, err := VerifyRemoveImageRead(raw, signature, authority, nowMS); err != nil {
		return nil, err
	}
	return signature, nil
}
