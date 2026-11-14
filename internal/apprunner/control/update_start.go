package control

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"strconv"
	"unicode/utf8"
)

const UpdateStartFormat = "portal.runner.data-update-start-command/1-dev.1"
const updateStartSignaturePurpose = "portal.app/runner-data-update-start-command/v1"
const updateStartReadPurpose = "portal.app/runner-data-update-start-read/v1"
const updateStartEffectPurpose = "portal.app/runner-data-update-start-effect/v1"
const maxUpdateStartArchiveBytes int64 = 512 << 20

// CommandClockSkewMS is extra margin the controller adds when it concludes that no command it signed can still be
// accepted. The runner enforces no skew tolerance: it compares a command only with its own clock, refuses one issued
// after that clock (zero tolerance), and accepts one until that clock reaches the expiry. A runner whose clock lags the
// controller's therefore sees a fresh command's issued time in its own future and refuses it, so late acceptance because
// of a lagging clock cannot occur. The margin is a conservative allowance on the controller's side only; it is not a
// tolerance the runner applies.
const CommandClockSkewMS = 5000

type UpdateStartPayload struct {
	ResourceID                    string `json:"resource_id"`
	NamespaceID                   string `json:"namespace_id"`
	SourceOperationID             string `json:"source_operation_id"`
	SourceStartEffectID           string `json:"source_start_effect_id"`
	SourceStartReceiptSHA256      string `json:"source_start_receipt_sha256"`
	SourceDataEffectID            string `json:"source_data_effect_id"`
	SourceDataReceiptSHA256       string `json:"source_data_receipt_sha256"`
	SourceCheckpointOperationID   string `json:"source_checkpoint_operation_id"`
	SourceCheckpointEffectID      string `json:"source_checkpoint_effect_id"`
	SourceCheckpointReceiptSHA256 string `json:"source_checkpoint_receipt_sha256"`
	CheckpointID                  string `json:"checkpoint_id"`
	CheckpointManifestSHA256      string `json:"checkpoint_manifest_sha256"`
	CheckpointArchiveBytes        string `json:"checkpoint_archive_bytes"`
	SourceReleaseSHA256           string `json:"source_release_sha256"`
	TargetReleaseSHA256           string `json:"target_release_sha256"`
	SourceGeneration              string `json:"source_generation"`
	TargetPolicyRevision          string `json:"target_policy_revision"`
	ProjectID                     string `json:"project_id"`
	NamespaceInode                string `json:"namespace_inode"`
	Device                        string `json:"device"`
	QuotaBytes                    string `json:"quota_bytes"`
	QuotaInodes                   string `json:"quota_inodes"`
	RootFilesystemUUID            string `json:"root_filesystem_uuid"`
	RootInode                     string `json:"root_inode"`
	DataFormat                    string `json:"data_format"`
	SourceWriterFence             string `json:"source_writer_fence"`
	TargetWriterFence             string `json:"target_writer_fence"`
	CheckpointWriterFence         string `json:"checkpoint_writer_fence,omitempty"`
	MigrateEffectID               string `json:"migrate_effect_id,omitempty"`
	MigrateReceiptSHA256          string `json:"migrate_receipt_sha256,omitempty"`
	Platform                      string `json:"platform"`
	SandboxProfile                string `json:"sandbox_profile"`
}

type UpdateStartCommand struct {
	Format            string             `json:"format"`
	DeploymentID      string             `json:"deployment_id"`
	ControllerEpoch   string             `json:"controller_epoch"`
	ControlRevision   string             `json:"control_revision"`
	InstallationID    string             `json:"installation_id"`
	OperationID       string             `json:"operation_id"`
	TargetGeneration  string             `json:"target_generation"`
	Stage             string             `json:"stage"`
	EffectNumber      string             `json:"effect_number"`
	EffectID          string             `json:"effect_id"`
	Kind              string             `json:"kind"`
	InstallationFence string             `json:"installation_fence"`
	PayloadSHA256     string             `json:"payload_sha256"`
	IssuedMS          string             `json:"issued_ms"`
	ExpiresMS         string             `json:"expires_ms"`
	Payload           UpdateStartPayload `json:"payload"`
}

type VerifiedUpdateStartCommand struct{ command UpdateStartCommand }

func (v VerifiedUpdateStartCommand) Command() UpdateStartCommand { return v.command }

func validUpdateStartLogical(c UpdateStartCommand) bool {
	p := c.Payload
	if c.Format != UpdateStartFormat || c.Stage != "start" ||
		(c.Kind != "update-start" && c.Kind != "restore-start" &&
			c.Kind != "enable-start") ||
		!shaPattern.MatchString(p.SourceStartReceiptSHA256) ||
		!shaPattern.MatchString(p.SourceDataReceiptSHA256) ||
		!shaPattern.MatchString(p.SourceCheckpointReceiptSHA256) ||
		!shaPattern.MatchString(p.CheckpointManifestSHA256) ||
		!shaPattern.MatchString(p.SourceReleaseSHA256) ||
		!shaPattern.MatchString(p.TargetReleaseSHA256) {
		return false
	}
	for _, id := range []string{
		c.DeploymentID, c.ControllerEpoch, c.InstallationID, c.OperationID, c.EffectID,
		p.ResourceID, p.NamespaceID, p.SourceOperationID, p.SourceStartEffectID,
		p.SourceDataEffectID, p.SourceCheckpointOperationID, p.SourceCheckpointEffectID,
		p.CheckpointID, p.RootFilesystemUUID,
	} {
		if !candidateUUID(id) {
			return false
		}
	}
	for _, value := range []string{
		c.TargetGeneration, p.CheckpointArchiveBytes, p.SourceGeneration,
		p.TargetPolicyRevision, p.ProjectID, p.DataFormat, p.SourceWriterFence,
		p.TargetWriterFence, p.QuotaBytes, p.QuotaInodes,
	} {
		if _, ok := counter(value, true); !ok {
			return false
		}
	}
	for _, value := range []string{c.EffectNumber, c.InstallationFence} {
		if _, ok := counter(value, true); !ok {
			return false
		}
	}
	source, _ := strconv.ParseInt(p.SourceGeneration, 10, 64)
	target, _ := strconv.ParseInt(c.TargetGeneration, 10, 64)
	sourceWriter, _ := strconv.ParseInt(p.SourceWriterFence, 10, 64)
	targetWriter, _ := strconv.ParseInt(p.TargetWriterFence, 10, 64)
	checkpointWriter := sourceWriter
	if p.CheckpointWriterFence != "" {
		parsed, ok := counter(p.CheckpointWriterFence, true)
		if !ok || parsed > sourceWriter {
			return false
		}
		checkpointWriter = parsed
	}
	// A migration binds the update to its applied migrate-data receipt, and only an update-start
	// can carry one: the checkpoint writer fence S, the migration's S+1 as the source fence and the
	// target above it.
	migrating := p.MigrateEffectID != "" || p.MigrateReceiptSHA256 != ""
	if migrating && (c.Kind != "update-start" || p.MigrateEffectID == "" || p.MigrateReceiptSHA256 == "" ||
		!candidateUUID(p.MigrateEffectID) || !shaPattern.MatchString(p.MigrateReceiptSHA256) ||
		p.CheckpointWriterFence == "" || checkpointWriter >= sourceWriter) {
		return false
	}
	project, _ := strconv.ParseInt(p.ProjectID, 10, 64)
	archive, _ := strconv.ParseInt(p.CheckpointArchiveBytes, 10, 64)
	quota, _ := strconv.ParseInt(p.QuotaBytes, 10, 64)
	slot, _ := counter(c.EffectNumber, true)
	operationRelation := p.SourceCheckpointOperationID == c.OperationID
	if c.Kind == "restore-start" {
		operationRelation = p.SourceCheckpointOperationID != c.OperationID &&
			p.TargetReleaseSHA256 == p.SourceReleaseSHA256
	} else if c.Kind == "enable-start" &&
		p.TargetReleaseSHA256 != p.SourceReleaseSHA256 {
		operationRelation = false
	}
	return operationRelation &&
		p.SourceCheckpointReceiptSHA256 != p.SourceStartReceiptSHA256 &&
		p.SourceStartReceiptSHA256 != p.SourceDataReceiptSHA256 &&
		source >= 1 && target == source+1 && sourceWriter >= 1 &&
		checkpointWriter >= 1 &&
		targetWriter > sourceWriter && project > 0 && project <= math.MaxUint32 &&
		archive > 0 && archive <= maxUpdateStartArchiveBytes && quota > 0 &&
		quota%512 == 0 && checkpointUint(p.NamespaceInode) &&
		checkpointUint(p.Device) && checkpointUint(p.RootInode) &&
		(p.Platform == "linux/amd64" || p.Platform == "linux/arm64") &&
		(p.SandboxProfile == CandidateSandboxProfile || p.SandboxProfile == WritableCandidateSandboxProfile) &&
		slot <= 32
}

func UpdateStartLogicalDigest(c UpdateStartCommand) (string, error) {
	if !validUpdateStartLogical(c) {
		return "", ErrInvalidCommand
	}
	logical := struct {
		Format           string             `json:"format"`
		DeploymentID     string             `json:"deployment_id"`
		ControllerEpoch  string             `json:"controller_epoch"`
		InstallationID   string             `json:"installation_id"`
		OperationID      string             `json:"operation_id"`
		TargetGeneration string             `json:"target_generation"`
		Stage            string             `json:"stage"`
		EffectNumber     string             `json:"effect_number"`
		Kind             string             `json:"kind"`
		Payload          UpdateStartPayload `json:"payload"`
	}{"portal.runner.data-update-start-effect/1-dev.1", c.DeploymentID, c.ControllerEpoch,
		c.InstallationID, c.OperationID, c.TargetGeneration, c.Stage, c.EffectNumber, c.Kind, c.Payload}
	raw, err := json.Marshal(logical)
	if err != nil {
		return "", ErrInvalidCommand
	}
	sum := sha256.Sum256(frame(updateStartEffectPurpose, raw))
	return hex.EncodeToString(sum[:]), nil
}

func parseUpdateStart(raw []byte) (UpdateStartCommand, error) {
	var c UpdateStartCommand
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
		return UpdateStartCommand{}, ErrInvalidCommand
	}
	return c, nil
}

func VerifyUpdateStart(raw, signature []byte, authority Authority, nowMS int64) (VerifiedUpdateStartCommand, error) {
	return cachedVerify("update-start", raw, signature, authority, nowMS, func() (VerifiedUpdateStartCommand, int64, error) {
		v, err := verifyUpdateStart(raw, signature, authority, nowMS, updateStartSignaturePurpose)
		if err != nil {
			return v, 0, err
		}
		return v, expiryMS(v.command.ExpiresMS), nil
	})
}

// VerifyUpdateStartRead checks a read-receipt command. It is signed under its own purpose and valid for
// the same window as any command, so a captured read cannot be submitted as an execute and an execute
// signature is not a read.
func VerifyUpdateStartRead(raw, signature []byte, authority Authority, nowMS int64) (VerifiedUpdateStartCommand, error) {
	return verifyUpdateStart(raw, signature, authority, nowMS, updateStartReadPurpose)
}

// SignUpdateStartRead signs the exact bytes of an update-start command for a read-receipt.
func SignUpdateStartRead(raw []byte, key ed25519.PrivateKey, authority Authority, nowMS int64) ([]byte, error) {
	if len(key) != ed25519.PrivateKeySize {
		return nil, ErrInvalidCommand
	}
	signature := ed25519.Sign(key, frame(updateStartReadPurpose, raw))
	if _, err := VerifyUpdateStartRead(raw, signature, authority, nowMS); err != nil {
		return nil, err
	}
	return signature, nil
}

func verifyUpdateStart(raw, signature []byte, authority Authority, nowMS int64, purpose string) (VerifiedUpdateStartCommand, error) {
	if len(authority.PublicKey) != ed25519.PublicKeySize || authority.ControlRevision < 1 ||
		nowMS < 0 || len(signature) != ed25519.SignatureSize {
		return VerifiedUpdateStartCommand{}, fmt.Errorf(
			"update-start authority bounds: %w", ErrInvalidCommand)
	}
	keySHA := sha256.Sum256(authority.PublicKey)
	if authority.Fingerprint != hex.EncodeToString(keySHA[:]) {
		return VerifiedUpdateStartCommand{}, fmt.Errorf(
			"update-start authority fingerprint: %w", ErrInvalidCommand)
	}
	c, err := parseUpdateStart(raw)
	if err != nil {
		return VerifiedUpdateStartCommand{}, fmt.Errorf(
			"update-start command parse: %w", ErrInvalidCommand)
	}
	revision, ok := counter(c.ControlRevision, true)
	if !ok || revision != authority.ControlRevision {
		return VerifiedUpdateStartCommand{}, fmt.Errorf(
			"update-start authority tuple revision=%s/%d: %w",
			c.ControlRevision, authority.ControlRevision,
			ErrInvalidCommand)
	}
	if c.DeploymentID != authority.DeploymentID {
		return VerifiedUpdateStartCommand{}, fmt.Errorf(
			"update-start deployment tuple: %w", ErrInvalidCommand)
	}
	if c.ControllerEpoch != authority.ControllerEpoch {
		return VerifiedUpdateStartCommand{}, fmt.Errorf(
			"update-start controller epoch tuple: %w", ErrInvalidCommand)
	}
	issued, ok := counter(c.IssuedMS, false)
	if !ok {
		return VerifiedUpdateStartCommand{}, fmt.Errorf(
			"update-start issued time: %w", ErrInvalidCommand)
	}
	expires, ok := counter(c.ExpiresMS, false)
	if !ok || expires <= issued || expires-issued > 30000 || issued > nowMS || nowMS >= expires {
		return VerifiedUpdateStartCommand{}, fmt.Errorf(
			"update-start time window issued=%d expires=%d now=%d: %w",
			issued, expires, nowMS, ErrInvalidCommand)
	}
	digest, err := UpdateStartLogicalDigest(c)
	if err != nil || digest != c.PayloadSHA256 {
		return VerifiedUpdateStartCommand{}, fmt.Errorf(
			"update-start payload digest: %w", ErrInvalidCommand)
	}
	if !ed25519.Verify(ed25519.PublicKey(authority.PublicKey), frame(purpose, raw), signature) {
		return VerifiedUpdateStartCommand{}, fmt.Errorf(
			"update-start signature: %w", ErrInvalidCommand)
	}
	return VerifiedUpdateStartCommand{command: c}, nil
}

func SignUpdateStart(c UpdateStartCommand, key ed25519.PrivateKey, authority Authority, nowMS int64) ([]byte, []byte, error) {
	if len(key) != ed25519.PrivateKeySize {
		return nil, nil, ErrInvalidCommand
	}
	raw, err := json.Marshal(c)
	if err != nil {
		return nil, nil, ErrInvalidCommand
	}
	signature := ed25519.Sign(key, frame(updateStartSignaturePurpose, raw))
	if _, err := VerifyUpdateStart(raw, signature, authority, nowMS); err != nil {
		return nil, nil, err
	}
	return raw, signature, nil
}
