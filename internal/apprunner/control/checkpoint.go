package control

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"math"
	"strconv"
	"unicode/utf8"
)

const CheckpointFormat = "portal.runner.data-checkpoint-command/1-dev.1"
const checkpointSignaturePurpose = "portal.app/runner-data-checkpoint-command/v1"
const checkpointEffectPurpose = "portal.app/runner-data-checkpoint-effect/v1"

type CheckpointPayload struct {
	CheckpointID            string `json:"checkpoint_id"`
	NamespaceID             string `json:"namespace_id"`
	ResourceID              string `json:"resource_id"`
	ReleaseSHA256           string `json:"release_sha256"`
	SourceOperationID       string `json:"source_operation_id"`
	SourceStartEffectID     string `json:"source_start_effect_id"`
	SourceReceiptSHA256     string `json:"source_receipt_sha256"`
	SourceDataEffectID      string `json:"source_data_effect_id"`
	SourceDataReceiptSHA256 string `json:"source_data_receipt_sha256"`
	ProjectID               string `json:"project_id"`
	NamespaceInode          string `json:"namespace_inode"`
	Device                  string `json:"device"`
	DataFormat              string `json:"data_format"`
	WriterFence             string `json:"writer_fence"`
}

type CheckpointCommand struct {
	Format            string            `json:"format"`
	DeploymentID      string            `json:"deployment_id"`
	ControllerEpoch   string            `json:"controller_epoch"`
	ControlRevision   string            `json:"control_revision"`
	InstallationID    string            `json:"installation_id"`
	OperationID       string            `json:"operation_id"`
	TargetGeneration  string            `json:"target_generation"`
	Stage             string            `json:"stage"`
	EffectNumber      string            `json:"effect_number"`
	EffectID          string            `json:"effect_id"`
	Kind              string            `json:"kind"`
	InstallationFence string            `json:"installation_fence"`
	PayloadSHA256     string            `json:"payload_sha256"`
	IssuedMS          string            `json:"issued_ms"`
	ExpiresMS         string            `json:"expires_ms"`
	Payload           CheckpointPayload `json:"payload"`
}

type VerifiedCheckpointCommand struct{ command CheckpointCommand }

func (v VerifiedCheckpointCommand) Command() CheckpointCommand { return v.command }

func validCheckpointLogical(c CheckpointCommand) bool {
	if c.Format != CheckpointFormat || c.Stage != "snapshot" || c.Kind != "snapshot" ||
		!shaPattern.MatchString(c.Payload.ReleaseSHA256) || !shaPattern.MatchString(c.Payload.SourceReceiptSHA256) ||
		!shaPattern.MatchString(c.Payload.SourceDataReceiptSHA256) {
		return false
	}
	for _, id := range []string{c.DeploymentID, c.ControllerEpoch, c.InstallationID, c.OperationID, c.EffectID,
		c.Payload.CheckpointID, c.Payload.NamespaceID, c.Payload.ResourceID, c.Payload.SourceOperationID, c.Payload.SourceStartEffectID, c.Payload.SourceDataEffectID} {
		if !candidateUUID(id) {
			return false
		}
	}
	for _, number := range []string{c.TargetGeneration, c.Payload.ProjectID, c.Payload.DataFormat, c.Payload.WriterFence} {
		if _, ok := counter(number, true); !ok {
			return false
		}
	}
	project, _ := strconv.ParseInt(c.Payload.ProjectID, 10, 64)
	if project > math.MaxUint32 || !checkpointUint(c.Payload.NamespaceInode) || !checkpointUint(c.Payload.Device) {
		return false
	}
	for _, number := range []string{c.EffectNumber, c.InstallationFence} {
		if _, ok := counter(number, true); !ok {
			return false
		}
	}
	slot, _ := counter(c.EffectNumber, true)
	return slot <= 32
}

func checkpointUint(value string) bool {
	n, err := strconv.ParseUint(value, 10, 64)
	return err == nil && n > 0 && strconv.FormatUint(n, 10) == value
}

func CheckpointLogicalDigest(c CheckpointCommand) (string, error) {
	if !validCheckpointLogical(c) {
		return "", ErrInvalidCommand
	}
	logical := struct {
		Format           string            `json:"format"`
		DeploymentID     string            `json:"deployment_id"`
		ControllerEpoch  string            `json:"controller_epoch"`
		InstallationID   string            `json:"installation_id"`
		OperationID      string            `json:"operation_id"`
		TargetGeneration string            `json:"target_generation"`
		Stage            string            `json:"stage"`
		EffectNumber     string            `json:"effect_number"`
		Kind             string            `json:"kind"`
		Payload          CheckpointPayload `json:"payload"`
	}{"portal.runner.data-checkpoint-effect/1-dev.1", c.DeploymentID, c.ControllerEpoch, c.InstallationID, c.OperationID, c.TargetGeneration, c.Stage, c.EffectNumber, c.Kind, c.Payload}
	raw, err := json.Marshal(logical)
	if err != nil {
		return "", ErrInvalidCommand
	}
	sum := sha256.Sum256(frame(checkpointEffectPurpose, raw))
	return hex.EncodeToString(sum[:]), nil
}

func parseCheckpoint(raw []byte) (CheckpointCommand, error) {
	var c CheckpointCommand
	if len(raw) == 0 || len(raw) > MaxCommandBytes || !utf8.Valid(raw) {
		return c, ErrInvalidCommand
	}
	scan := json.NewDecoder(bytes.NewReader(raw))
	scan.UseNumber()
	first, err := scan.Token()
	if err != nil || first != json.Delim('{') || !strictObject(scan, 1) {
		return c, ErrInvalidCommand
	}
	if _, err := scan.Token(); err != io.EOF {
		return c, ErrInvalidCommand
	}
	decode := json.NewDecoder(bytes.NewReader(raw))
	decode.DisallowUnknownFields()
	if decode.Decode(&c) != nil {
		return CheckpointCommand{}, ErrInvalidCommand
	}
	return c, nil
}

func VerifyCheckpoint(raw, signature []byte, authority Authority, nowMS int64) (VerifiedCheckpointCommand, error) {
	fail := func() (VerifiedCheckpointCommand, error) { return VerifiedCheckpointCommand{}, ErrInvalidCommand }
	if len(authority.PublicKey) != ed25519.PublicKeySize || authority.ControlRevision < 1 || nowMS < 0 || len(signature) != ed25519.SignatureSize {
		return fail()
	}
	keySHA := sha256.Sum256(authority.PublicKey)
	if authority.Fingerprint != hex.EncodeToString(keySHA[:]) {
		return fail()
	}
	c, err := parseCheckpoint(raw)
	if err != nil {
		return fail()
	}
	revision, ok := counter(c.ControlRevision, true)
	if !ok || revision != authority.ControlRevision || c.DeploymentID != authority.DeploymentID || c.ControllerEpoch != authority.ControllerEpoch {
		return fail()
	}
	issued, ok := counter(c.IssuedMS, false)
	if !ok {
		return fail()
	}
	expires, ok := counter(c.ExpiresMS, false)
	if !ok || expires <= issued || expires-issued > 30000 || issued > nowMS || nowMS >= expires {
		return fail()
	}
	digest, err := CheckpointLogicalDigest(c)
	if err != nil || digest != c.PayloadSHA256 {
		return fail()
	}
	if !ed25519.Verify(ed25519.PublicKey(authority.PublicKey), frame(checkpointSignaturePurpose, raw), signature) {
		return fail()
	}
	return VerifiedCheckpointCommand{command: c}, nil
}

func SignCheckpoint(c CheckpointCommand, key ed25519.PrivateKey, authority Authority, nowMS int64) ([]byte, []byte, error) {
	if len(key) != ed25519.PrivateKeySize {
		return nil, nil, ErrInvalidCommand
	}
	raw, err := json.Marshal(c)
	if err != nil {
		return nil, nil, ErrInvalidCommand
	}
	signature := ed25519.Sign(key, frame(checkpointSignaturePurpose, raw))
	if _, err := VerifyCheckpoint(raw, signature, authority, nowMS); err != nil {
		return nil, nil, err
	}
	return raw, signature, nil
}
