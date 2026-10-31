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

const UpdateStartFormat = "portal.runner.data-update-start-command/1-dev.1"
const updateStartSignaturePurpose = "portal.app/runner-data-update-start-command/v1"
const updateStartEffectPurpose = "portal.app/runner-data-update-start-effect/v1"
const maxUpdateStartArchiveBytes int64 = 512 << 20

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
	if c.Format != UpdateStartFormat || c.Stage != "start" || c.Kind != "update-start" ||
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
	project, _ := strconv.ParseInt(p.ProjectID, 10, 64)
	archive, _ := strconv.ParseInt(p.CheckpointArchiveBytes, 10, 64)
	quota, _ := strconv.ParseInt(p.QuotaBytes, 10, 64)
	slot, _ := counter(c.EffectNumber, true)
	return p.SourceCheckpointOperationID == c.OperationID &&
		p.SourceCheckpointReceiptSHA256 != p.SourceStartReceiptSHA256 &&
		p.SourceStartReceiptSHA256 != p.SourceDataReceiptSHA256 &&
		source >= 1 && target == source+1 && sourceWriter >= 1 &&
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
	fail := func() (VerifiedUpdateStartCommand, error) {
		return VerifiedUpdateStartCommand{}, ErrInvalidCommand
	}
	if len(authority.PublicKey) != ed25519.PublicKeySize || authority.ControlRevision < 1 ||
		nowMS < 0 || len(signature) != ed25519.SignatureSize {
		return fail()
	}
	keySHA := sha256.Sum256(authority.PublicKey)
	if authority.Fingerprint != hex.EncodeToString(keySHA[:]) {
		return fail()
	}
	c, err := parseUpdateStart(raw)
	if err != nil {
		return fail()
	}
	revision, ok := counter(c.ControlRevision, true)
	if !ok || revision != authority.ControlRevision ||
		c.DeploymentID != authority.DeploymentID || c.ControllerEpoch != authority.ControllerEpoch {
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
	digest, err := UpdateStartLogicalDigest(c)
	if err != nil || digest != c.PayloadSHA256 {
		return fail()
	}
	if !ed25519.Verify(ed25519.PublicKey(authority.PublicKey), frame(updateStartSignaturePurpose, raw), signature) {
		return fail()
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
