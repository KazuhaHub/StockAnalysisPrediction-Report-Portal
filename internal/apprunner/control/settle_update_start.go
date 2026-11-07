package control

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"unicode/utf8"
)

// SettleUpdateStartCommand asks the runner to settle an update-start it holds claimed with nothing executing it. It
// can only settle the exact start named by a signed start reference, and it never creates or starts anything: under
// the data execution gate the runner looks at what exists by name and label and records it. The start reference is
// signed for a read, so no execute-capable start command is made to settle one.
type SettleUpdateStartCommand struct {
	Format             string `json:"format"`
	EffectID           string `json:"effect_id"`
	OperationID        string `json:"operation_id"`
	InstallationFence  string `json:"installation_fence"`
	IssuedMS           string `json:"issued_ms"`
	ExpiresMS          string `json:"expires_ms"`
	PayloadSHA256      string `json:"payload_sha256"`
	StartRaw           []byte `json:"start_raw"`
	StartReadSignature []byte `json:"start_read_signature"`
}

const SettleUpdateStartFormat = "portal.runner.settle-update-start-command/1-dev.1"
const settleUpdateStartSignaturePurpose = "portal.app/runner-settle-update-start/v1"
const settleUpdateStartReadPurpose = "portal.app/runner-settle-update-start-read/v1"

type VerifiedSettleUpdateStartCommand struct {
	command SettleUpdateStartCommand
	start   VerifiedUpdateStartCommand
}

func (v VerifiedSettleUpdateStartCommand) Command() SettleUpdateStartCommand {
	c := v.command
	c.StartRaw, c.StartReadSignature = bytes.Clone(c.StartRaw), bytes.Clone(c.StartReadSignature)
	return c
}

func (v VerifiedSettleUpdateStartCommand) Start() VerifiedUpdateStartCommand { return v.start }

func SettleUpdateStartLogicalDigest(c SettleUpdateStartCommand) (string, error) {
	start, err := parseUpdateStart(c.StartRaw)
	if err != nil || c.Format != SettleUpdateStartFormat || start.Kind != "update-start" ||
		!candidateUUID(c.EffectID) || !candidateUUID(c.OperationID) || len(c.StartReadSignature) != ed25519.SignatureSize {
		return "", ErrInvalidCommand
	}
	digest, err := UpdateStartLogicalDigest(start)
	_, fenceOK := counter(c.InstallationFence, true)
	issued, issuedOK := counter(c.IssuedMS, false)
	expires, expiresOK := counter(c.ExpiresMS, false)
	if err != nil || digest != start.PayloadSHA256 || !fenceOK || !issuedOK || !expiresOK || expires <= issued || expires-issued > 30000 ||
		c.EffectID == start.EffectID || c.OperationID == start.OperationID {
		return "", ErrInvalidCommand
	}
	logical := struct {
		Format             string `json:"format"`
		OperationID        string `json:"operation_id"`
		StartOperationID   string `json:"start_operation_id"`
		StartEffectID      string `json:"start_effect_id"`
		StartPayloadSHA256 string `json:"start_payload_sha256"`
		InstallationFence  string `json:"installation_fence"`
		ResourceID         string `json:"resource_id"`
		NamespaceID        string `json:"namespace_id"`
		ReleaseSHA256      string `json:"release_sha256"`
		Generation         string `json:"generation"`
		ControllerEpoch    string `json:"controller_epoch"`
	}{
		"portal.runner.settle-update-start-effect/1-dev.1", c.OperationID, start.OperationID, start.EffectID, start.PayloadSHA256,
		c.InstallationFence, start.Payload.ResourceID, start.Payload.NamespaceID, start.Payload.TargetReleaseSHA256,
		start.TargetGeneration, start.ControllerEpoch,
	}
	raw, err := json.Marshal(logical)
	if err != nil {
		return "", ErrInvalidCommand
	}
	sum := sha256.Sum256(frame("portal.app/runner-settle-update-start-effect/v1", raw))
	return hex.EncodeToString(sum[:]), nil
}

func parseSettleUpdateStart(raw []byte) (SettleUpdateStartCommand, error) {
	var command SettleUpdateStartCommand
	if len(raw) == 0 || len(raw) > MaxCommandBytes || !utf8.Valid(raw) {
		return command, ErrInvalidCommand
	}
	scan := json.NewDecoder(bytes.NewReader(raw))
	scan.UseNumber()
	first, err := scan.Token()
	if err != nil || first != json.Delim('{') || !strictObject(scan, 1) {
		return command, ErrInvalidCommand
	}
	if _, err := scan.Token(); err != io.EOF {
		return command, ErrInvalidCommand
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&command) != nil {
		return SettleUpdateStartCommand{}, ErrInvalidCommand
	}
	return command, nil
}

func verifySettleUpdateStart(raw, signature []byte, authority Authority, nowMS int64, purpose string, bounded bool) (VerifiedSettleUpdateStartCommand, error) {
	var zero VerifiedSettleUpdateStartCommand
	if len(signature) != ed25519.SignatureSize {
		return zero, ErrInvalidCommand
	}
	command, err := parseSettleUpdateStart(raw)
	if err != nil {
		return zero, err
	}
	start, err := VerifyUpdateStartRead(command.StartRaw, command.StartReadSignature, authority, nowMS)
	if err != nil {
		return zero, err
	}
	digest, err := SettleUpdateStartLogicalDigest(command)
	if err != nil || digest != command.PayloadSHA256 || !ed25519.Verify(ed25519.PublicKey(authority.PublicKey), frame(purpose, raw), signature) {
		return zero, ErrInvalidCommand
	}
	if bounded {
		issued, _ := counter(command.IssuedMS, false)
		expires, _ := counter(command.ExpiresMS, false)
		if issued > nowMS || nowMS >= expires {
			return zero, ErrInvalidCommand
		}
	}
	return VerifiedSettleUpdateStartCommand{command: command, start: start}, nil
}

// VerifySettleUpdateStart checks a command to settle, inside its window.
func VerifySettleUpdateStart(raw, signature []byte, authority Authority, nowMS int64) (VerifiedSettleUpdateStartCommand, error) {
	return verifySettleUpdateStart(raw, signature, authority, nowMS, settleUpdateStartSignaturePurpose, true)
}

// VerifySettleUpdateStartRead checks the same command signed for a read-back, which the window does not bind.
func VerifySettleUpdateStartRead(raw, signature []byte, authority Authority, nowMS int64) (VerifiedSettleUpdateStartCommand, error) {
	return verifySettleUpdateStart(raw, signature, authority, nowMS, settleUpdateStartReadPurpose, false)
}

// VerifySettleUpdateStartSettlement checks the exact command already admitted, whose job may finish after its window.
func VerifySettleUpdateStartSettlement(raw, signature []byte, authority Authority, nowMS int64) (VerifiedSettleUpdateStartCommand, error) {
	return verifySettleUpdateStart(raw, signature, authority, nowMS, settleUpdateStartSignaturePurpose, false)
}

func SignSettleUpdateStart(command SettleUpdateStartCommand, key ed25519.PrivateKey, authority Authority, nowMS int64) ([]byte, []byte, error) {
	if len(key) != ed25519.PrivateKeySize {
		return nil, nil, ErrInvalidCommand
	}
	raw, err := json.Marshal(command)
	if err != nil {
		return nil, nil, ErrInvalidCommand
	}
	signature := ed25519.Sign(key, frame(settleUpdateStartSignaturePurpose, raw))
	if _, err := VerifySettleUpdateStart(raw, signature, authority, nowMS); err != nil {
		return nil, nil, err
	}
	return raw, signature, nil
}

func SignSettleUpdateStartRead(raw []byte, key ed25519.PrivateKey, authority Authority, nowMS int64) ([]byte, error) {
	if len(key) != ed25519.PrivateKeySize {
		return nil, ErrInvalidCommand
	}
	signature := ed25519.Sign(key, frame(settleUpdateStartReadPurpose, raw))
	if _, err := VerifySettleUpdateStartRead(raw, signature, authority, nowMS); err != nil {
		return nil, err
	}
	return signature, nil
}
