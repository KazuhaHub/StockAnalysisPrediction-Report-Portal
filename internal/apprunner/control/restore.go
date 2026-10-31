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

const RestoreFormat = "portal.runner.data-restore-command/1-dev.1"
const restoreSignaturePurpose = "portal.app/runner-data-restore-command/v1"
const restoreEffectPurpose = "portal.app/runner-data-restore-effect/v1"
const maxRestoreArchiveBytes int64 = 512 << 20

type RestorePayload struct {
	RecoverySetID               string `json:"recovery_set_id"`
	CheckpointID                string `json:"checkpoint_id"`
	NamespaceID                 string `json:"namespace_id"`
	ReleaseSHA256               string `json:"release_sha256"`
	ManifestSHA256              string `json:"manifest_sha256"`
	ArchiveBytes                string `json:"archive_bytes"`
	ProjectID                   string `json:"project_id"`
	NamespaceInode              string `json:"namespace_inode"`
	Device                      string `json:"device"`
	DataFormat                  string `json:"data_format"`
	WriterFence                 string `json:"writer_fence"`
	SourceCheckpointOperationID string `json:"source_checkpoint_operation_id"`
	SourceCheckpointEffectID    string `json:"source_checkpoint_effect_id"`
	SourceCheckpointReceiptSHA  string `json:"source_checkpoint_receipt_sha256"`
}

type RestoreCommand struct {
	Format            string         `json:"format"`
	DeploymentID      string         `json:"deployment_id"`
	ControllerEpoch   string         `json:"controller_epoch"`
	ControlRevision   string         `json:"control_revision"`
	InstallationID    string         `json:"installation_id"`
	OperationID       string         `json:"operation_id"`
	TargetGeneration  string         `json:"target_generation"`
	Stage             string         `json:"stage"`
	EffectNumber      string         `json:"effect_number"`
	EffectID          string         `json:"effect_id"`
	Kind              string         `json:"kind"`
	InstallationFence string         `json:"installation_fence"`
	PayloadSHA256     string         `json:"payload_sha256"`
	IssuedMS          string         `json:"issued_ms"`
	ExpiresMS         string         `json:"expires_ms"`
	Payload           RestorePayload `json:"payload"`
}

type VerifiedRestoreCommand struct{ command RestoreCommand }

func (v VerifiedRestoreCommand) Command() RestoreCommand { return v.command }

func validRestoreLogical(c RestoreCommand) bool {
	if c.Format != RestoreFormat || c.Stage != "restore_data" || c.Kind != "restore" ||
		!shaPattern.MatchString(c.Payload.ReleaseSHA256) ||
		!shaPattern.MatchString(c.Payload.ManifestSHA256) ||
		!shaPattern.MatchString(c.Payload.SourceCheckpointReceiptSHA) {
		return false
	}
	for _, id := range []string{
		c.DeploymentID, c.ControllerEpoch, c.InstallationID, c.OperationID, c.EffectID,
		c.Payload.RecoverySetID, c.Payload.CheckpointID, c.Payload.NamespaceID,
		c.Payload.SourceCheckpointOperationID, c.Payload.SourceCheckpointEffectID,
	} {
		if !candidateUUID(id) {
			return false
		}
	}
	for _, value := range []string{
		c.TargetGeneration, c.Payload.ArchiveBytes, c.Payload.ProjectID,
		c.Payload.DataFormat, c.Payload.WriterFence,
	} {
		if _, ok := counter(value, true); !ok {
			return false
		}
	}
	project, _ := strconv.ParseInt(c.Payload.ProjectID, 10, 64)
	archive, _ := strconv.ParseInt(c.Payload.ArchiveBytes, 10, 64)
	if project > math.MaxUint32 || archive > maxRestoreArchiveBytes ||
		!checkpointUint(c.Payload.NamespaceInode) || !checkpointUint(c.Payload.Device) {
		return false
	}
	for _, value := range []string{c.EffectNumber, c.InstallationFence} {
		if _, ok := counter(value, true); !ok {
			return false
		}
	}
	slot, _ := counter(c.EffectNumber, true)
	return slot <= 32
}

func RestoreLogicalDigest(c RestoreCommand) (string, error) {
	if !validRestoreLogical(c) {
		return "", ErrInvalidCommand
	}
	logical := struct {
		Format           string         `json:"format"`
		DeploymentID     string         `json:"deployment_id"`
		ControllerEpoch  string         `json:"controller_epoch"`
		InstallationID   string         `json:"installation_id"`
		OperationID      string         `json:"operation_id"`
		TargetGeneration string         `json:"target_generation"`
		Stage            string         `json:"stage"`
		EffectNumber     string         `json:"effect_number"`
		Kind             string         `json:"kind"`
		Payload          RestorePayload `json:"payload"`
	}{"portal.runner.data-restore-effect/1-dev.1", c.DeploymentID, c.ControllerEpoch,
		c.InstallationID, c.OperationID, c.TargetGeneration, c.Stage, c.EffectNumber, c.Kind, c.Payload}
	raw, err := json.Marshal(logical)
	if err != nil {
		return "", ErrInvalidCommand
	}
	sum := sha256.Sum256(frame(restoreEffectPurpose, raw))
	return hex.EncodeToString(sum[:]), nil
}

func parseRestore(raw []byte) (RestoreCommand, error) {
	var c RestoreCommand
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
		return RestoreCommand{}, ErrInvalidCommand
	}
	return c, nil
}

func VerifyRestore(raw, signature []byte, authority Authority, nowMS int64) (VerifiedRestoreCommand, error) {
	fail := func() (VerifiedRestoreCommand, error) { return VerifiedRestoreCommand{}, ErrInvalidCommand }
	if len(authority.PublicKey) != ed25519.PublicKeySize || authority.ControlRevision < 1 || nowMS < 0 || len(signature) != ed25519.SignatureSize {
		return fail()
	}
	keySHA := sha256.Sum256(authority.PublicKey)
	if authority.Fingerprint != hex.EncodeToString(keySHA[:]) {
		return fail()
	}
	c, err := parseRestore(raw)
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
	digest, err := RestoreLogicalDigest(c)
	if err != nil || digest != c.PayloadSHA256 {
		return fail()
	}
	if !ed25519.Verify(ed25519.PublicKey(authority.PublicKey), frame(restoreSignaturePurpose, raw), signature) {
		return fail()
	}
	return VerifiedRestoreCommand{command: c}, nil
}

func SignRestore(c RestoreCommand, key ed25519.PrivateKey, authority Authority, nowMS int64) ([]byte, []byte, error) {
	if len(key) != ed25519.PrivateKeySize {
		return nil, nil, ErrInvalidCommand
	}
	raw, err := json.Marshal(c)
	if err != nil {
		return nil, nil, ErrInvalidCommand
	}
	signature := ed25519.Sign(key, frame(restoreSignaturePurpose, raw))
	if _, err := VerifyRestore(raw, signature, authority, nowMS); err != nil {
		return nil, nil, err
	}
	return raw, signature, nil
}
