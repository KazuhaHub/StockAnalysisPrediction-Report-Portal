package control

import (
	"crypto/ed25519"
	"encoding/json"
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

// RemoveImagePayload names the one image a runner removes. It has the shape of a load payload on purpose: the
// release whose descriptor binds the image, the platform and the descriptor's image entry. The runner derives the
// daemon's image ID from that entry exactly as the load does, so the removal acts on the ID the load produced.
type RemoveImagePayload LoadImagePayload

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
	return validLoadImageLogical(LoadImageCommand{CommandHeader: loadShapedHeader(c.CommandHeader), Payload: LoadImagePayload(c.Payload)})
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
