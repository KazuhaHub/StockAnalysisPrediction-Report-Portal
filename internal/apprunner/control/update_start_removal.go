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

const UpdateStartRemovalFormat = "portal.runner.update-start-removal-command/1-dev.1"
const updateStartRemovalSignaturePurpose = "portal.app/runner-update-start-removal/v1"

// UpdateStartRemovalCommand can only stop or remove the exact update-start
// candidate named by a fresh signed start reference and its applied running
// receipt. It never selects a path, container option or generic Docker action.
type UpdateStartRemovalCommand struct {
	Format               string `json:"format"`
	EffectID             string `json:"effect_id"`
	OperationID          string `json:"operation_id"`
	Stage                string `json:"stage"`
	InstallationFence    string `json:"installation_fence"`
	PayloadSHA256        string `json:"payload_sha256"`
	StartRaw             []byte `json:"start_raw"`
	StartSignature       []byte `json:"start_signature"`
	RunningReceiptSHA256 string `json:"running_receipt_sha256"`
}

// ReservedStartReceiptSHA256 stands in for the running receipt in a removal of a start the runner reserved and never
// ran. There is no running receipt to bind, and the sentinel says so in the signed command, so a removal of a running
// target can never be taken for one of a reservation or the other way round.
var ReservedStartReceiptSHA256 = func() string {
	sum := sha256.Sum256([]byte("portal.runner.update-start-removal/reserved-only"))
	return hex.EncodeToString(sum[:])
}()

// ReservedStartNativeID is the identity a receipt names for a target that was reserved and never created. No
// container ever had one, so it is derived from the resource.
func ReservedStartNativeID(resourceID string) string {
	sum := sha256.Sum256([]byte("portal.runner.update-start-reserved-native/" + resourceID))
	return hex.EncodeToString(sum[:])
}

// IsReservedStartRemoval reports whether raw is a removal command of a reserved-only start.
func IsReservedStartRemoval(raw []byte) bool {
	command, err := parseUpdateStartRemoval(raw)
	return err == nil && command.RunningReceiptSHA256 == ReservedStartReceiptSHA256
}

type VerifiedUpdateStartRemovalCommand struct {
	command UpdateStartRemovalCommand
	start   VerifiedUpdateStartCommand
}

func (v VerifiedUpdateStartRemovalCommand) Command() UpdateStartRemovalCommand {
	c := v.command
	c.StartRaw, c.StartSignature = bytes.Clone(c.StartRaw), bytes.Clone(c.StartSignature)
	return c
}

func (v VerifiedUpdateStartRemovalCommand) Start() VerifiedUpdateStartCommand {
	return v.start
}

func UpdateStartRemovalLogicalDigest(c UpdateStartRemovalCommand) (string, error) {
	start, err := parseUpdateStart(c.StartRaw)
	if err != nil || c.Format != UpdateStartRemovalFormat ||
		(c.Stage != "stop" && c.Stage != "remove_code") ||
		!candidateUUID(c.EffectID) || !candidateUUID(c.OperationID) ||
		len(c.StartSignature) != ed25519.SignatureSize ||
		!shaPattern.MatchString(c.RunningReceiptSHA256) {
		return "", ErrInvalidCommand
	}
	digest, err := UpdateStartLogicalDigest(start)
	_, fenceOK := counter(c.InstallationFence, true)
	if err != nil || digest != start.PayloadSHA256 ||
		!fenceOK ||
		c.EffectID == start.EffectID ||
		c.OperationID == start.OperationID {
		return "", ErrInvalidCommand
	}
	logical := struct {
		Format               string `json:"format"`
		OperationID          string `json:"operation_id"`
		Stage                string `json:"stage"`
		StartEffectID        string `json:"start_effect_id"`
		StartPayloadSHA256   string `json:"start_payload_sha256"`
		RunningReceiptSHA256 string `json:"running_receipt_sha256"`
		InstallationFence    string `json:"installation_fence"`
		ResourceID           string `json:"resource_id"`
		NamespaceID          string `json:"namespace_id"`
		ReleaseSHA256        string `json:"release_sha256"`
		Generation           string `json:"generation"`
		ControllerEpoch      string `json:"controller_epoch"`
	}{
		"portal.runner.update-start-removal-effect/1-dev.1",
		c.OperationID, c.Stage, start.EffectID, start.PayloadSHA256,
		c.RunningReceiptSHA256, c.InstallationFence, start.Payload.ResourceID,
		start.Payload.NamespaceID, start.Payload.TargetReleaseSHA256,
		start.TargetGeneration, start.ControllerEpoch,
	}
	raw, err := json.Marshal(logical)
	if err != nil {
		return "", ErrInvalidCommand
	}
	sum := sha256.Sum256(frame(
		"portal.app/runner-update-start-removal-effect/v1", raw))
	return hex.EncodeToString(sum[:]), nil
}

func parseUpdateStartRemoval(raw []byte) (UpdateStartRemovalCommand, error) {
	var command UpdateStartRemovalCommand
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
		return UpdateStartRemovalCommand{}, ErrInvalidCommand
	}
	return command, nil
}

func VerifyUpdateStartRemoval(raw, signature []byte, authority Authority, nowMS int64) (VerifiedUpdateStartRemovalCommand, error) {
	var zero VerifiedUpdateStartRemovalCommand
	if len(signature) != ed25519.SignatureSize {
		return zero, ErrInvalidCommand
	}
	command, err := parseUpdateStartRemoval(raw)
	if err != nil {
		return zero, err
	}
	start, err := VerifyUpdateStart(
		command.StartRaw, command.StartSignature, authority, nowMS)
	if err != nil {
		return zero, err
	}
	digest, err := UpdateStartRemovalLogicalDigest(command)
	if err != nil || digest != command.PayloadSHA256 ||
		!ed25519.Verify(
			ed25519.PublicKey(authority.PublicKey),
			frame(updateStartRemovalSignaturePurpose, raw), signature) {
		return zero, ErrInvalidCommand
	}
	return VerifiedUpdateStartRemovalCommand{
		command: command, start: start,
	}, nil
}

func SignUpdateStartRemoval(command UpdateStartRemovalCommand, key ed25519.PrivateKey, authority Authority, nowMS int64) ([]byte, []byte, error) {
	if len(key) != ed25519.PrivateKeySize {
		return nil, nil, ErrInvalidCommand
	}
	raw, err := json.Marshal(command)
	if err != nil {
		return nil, nil, ErrInvalidCommand
	}
	signature := ed25519.Sign(
		key, frame(updateStartRemovalSignaturePurpose, raw))
	if _, err := VerifyUpdateStartRemoval(
		raw, signature, authority, nowMS); err != nil {
		return nil, nil, err
	}
	return raw, signature, nil
}
