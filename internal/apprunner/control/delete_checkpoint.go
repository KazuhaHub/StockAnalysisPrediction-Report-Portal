package control

import (
	"crypto/ed25519"
	"encoding/json"
)

const DeleteCheckpointFormat = "portal.runner.delete-checkpoint-command/1-dev.1"
const deleteCheckpointSignaturePurpose = "portal.app/runner-delete-checkpoint-command/v1"
const deleteCheckpointReadPurpose = "portal.app/runner-delete-checkpoint-read/v1"
const deleteCheckpointEffectPurpose = "portal.app/runner-delete-checkpoint-effect/v1"

// DeleteCheckpointStage and DeleteCheckpointKind name the effect in the journal and Portal stage vocabularies. The
// command shares the journal stage "remove_data" with the delete-data command (the closed stage list of
// runner_commands has no stage of its own, and none may be added), so effect number 1 stays the delete-data
// command's and a checkpoint deletion takes a number from 2 to 32.
const DeleteCheckpointStage = "remove_data"
const DeleteCheckpointKind = "delete-checkpoint"

// MinDeleteCheckpointEffectNumber is the first effect number a checkpoint deletion may take.
const MinDeleteCheckpointEffectNumber = 2

// MaxDeleteCheckpointArchiveBytes bounds the archive size a deletion may name: the checkpoint size limit.
const MaxDeleteCheckpointArchiveBytes = MaxCheckpointArchiveBytes

// DeleteCheckpointPayload names the one retained checkpoint a runner retires. Every field but RecoverySetID is a
// fact the runner compares with its own checkpoint row, so a deletion signed for one checkpoint cannot retire
// another. RecoverySetID is carried into the receipt only: the runner has no recovery set table.
type DeleteCheckpointPayload struct {
	RecoverySetID     string `json:"recovery_set_id"`
	CheckpointID      string `json:"checkpoint_id"`
	NamespaceID       string `json:"namespace_id"`
	SourceOperationID string `json:"source_operation_id"`
	ReleaseSHA256     string `json:"release_sha256"`
	ManifestSHA256    string `json:"manifest_sha256"`
	ArchiveBytes      string `json:"archive_bytes"`
	DataFormat        string `json:"data_format"`
	WriterFence       string `json:"writer_fence"`
}

type DeleteCheckpointCommand struct {
	CommandHeader
	Payload DeleteCheckpointPayload `json:"payload"`
}

type VerifiedDeleteCheckpointCommand struct{ command DeleteCheckpointCommand }

func (v VerifiedDeleteCheckpointCommand) Command() DeleteCheckpointCommand { return v.command }

func validDeleteCheckpointLogical(c DeleteCheckpointCommand) bool {
	slot, ok := counter(c.EffectNumber, true)
	if !ok || slot < MinDeleteCheckpointEffectNumber || !validHeaderAnySlot(c.CommandHeader, DeleteCheckpointFormat, DeleteCheckpointStage, DeleteCheckpointKind) {
		return false
	}
	p := c.Payload
	for _, id := range []string{p.RecoverySetID, p.CheckpointID, p.NamespaceID, p.SourceOperationID} {
		if !candidateUUID(id) {
			return false
		}
	}
	if !shaPattern.MatchString(p.ReleaseSHA256) || !shaPattern.MatchString(p.ManifestSHA256) {
		return false
	}
	bytes, ok := counter(p.ArchiveBytes, true)
	if !ok || bytes > MaxDeleteCheckpointArchiveBytes {
		return false
	}
	for _, number := range []string{p.DataFormat, p.WriterFence} {
		if _, ok := counter(number, true); !ok {
			return false
		}
	}
	return true
}

func DeleteCheckpointLogicalDigest(c DeleteCheckpointCommand) (string, error) {
	if !validDeleteCheckpointLogical(c) {
		return "", ErrInvalidCommand
	}
	return logicalDigest(deleteCheckpointEffectPurpose, "portal.runner.delete-checkpoint-effect/1-dev.1", c.CommandHeader, c.Payload)
}

func verifyDeleteCheckpoint(raw, signature []byte, authority Authority, nowMS int64, settling bool, purpose string) (VerifiedDeleteCheckpointCommand, error) {
	c, err := parseCanonicalCommand[DeleteCheckpointCommand](raw)
	if err != nil || !verifyEnvelope(c.CommandHeader, raw, signature, authority, nowMS, settling, purpose, func() (string, error) { return DeleteCheckpointLogicalDigest(c) }) {
		return VerifiedDeleteCheckpointCommand{}, ErrInvalidCommand
	}
	return VerifiedDeleteCheckpointCommand{command: c}, nil
}

func VerifyDeleteCheckpoint(raw, signature []byte, authority Authority, nowMS int64) (VerifiedDeleteCheckpointCommand, error) {
	return verifyDeleteCheckpoint(raw, signature, authority, nowMS, false, deleteCheckpointSignaturePurpose)
}

// VerifyDeleteCheckpointSettlement checks the exact command already admitted for an in-flight deletion. Expiry
// governs dispatch; a deletion that finishes after the signed window must still be settled.
func VerifyDeleteCheckpointSettlement(raw, signature []byte, authority Authority, nowMS int64) (VerifiedDeleteCheckpointCommand, error) {
	return verifyDeleteCheckpoint(raw, signature, authority, nowMS, true, deleteCheckpointSignaturePurpose)
}

// VerifyDeleteCheckpointRead checks a read-back command, signed under its own purpose and not bound by the window.
func VerifyDeleteCheckpointRead(raw, signature []byte, authority Authority, nowMS int64) (VerifiedDeleteCheckpointCommand, error) {
	return verifyDeleteCheckpoint(raw, signature, authority, nowMS, true, deleteCheckpointReadPurpose)
}

func SignDeleteCheckpoint(c DeleteCheckpointCommand, key ed25519.PrivateKey, authority Authority, nowMS int64) ([]byte, []byte, error) {
	if len(key) != ed25519.PrivateKeySize {
		return nil, nil, ErrInvalidCommand
	}
	raw, err := json.Marshal(c)
	if err != nil {
		return nil, nil, ErrInvalidCommand
	}
	signature := ed25519.Sign(key, frame(deleteCheckpointSignaturePurpose, raw))
	if _, err := VerifyDeleteCheckpoint(raw, signature, authority, nowMS); err != nil {
		return nil, nil, err
	}
	return raw, signature, nil
}

func SignDeleteCheckpointRead(raw []byte, key ed25519.PrivateKey, authority Authority, nowMS int64) ([]byte, error) {
	if len(key) != ed25519.PrivateKeySize {
		return nil, ErrInvalidCommand
	}
	signature := ed25519.Sign(key, frame(deleteCheckpointReadPurpose, raw))
	if _, err := VerifyDeleteCheckpointRead(raw, signature, authority, nowMS); err != nil {
		return nil, err
	}
	return signature, nil
}
