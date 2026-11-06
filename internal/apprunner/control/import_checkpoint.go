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

const ImportCheckpointFormat = "portal.runner.data-import-command/1-dev.1"
const importCheckpointSignaturePurpose = "portal.app/runner-data-import-command/v1"
const importCheckpointEffectPurpose = "portal.app/runner-data-import-effect/v1"

// ImportCheckpointPayload carries one single-use import grant and the values the
// archive must prove. The Source* and Checkpoint* fields are Portal's recorded
// provenance of the old host; they are compared with the archive manifest and
// never with the host that receives the import. ProjectID and the quota are the
// new host's reservation for the namespace being created.
type ImportCheckpointPayload struct {
	GrantID                  string `json:"grant_id"`
	RecoverySetID            string `json:"recovery_set_id"`
	CheckpointID             string `json:"checkpoint_id"`
	NamespaceID              string `json:"namespace_id"`
	ReleaseSHA256            string `json:"release_sha256"`
	ManifestSHA256           string `json:"manifest_sha256"`
	ArchiveBytes             string `json:"archive_bytes"`
	DataFormat               string `json:"data_format"`
	WriterFence              string `json:"writer_fence"`
	ProjectID                string `json:"project_id"`
	QuotaBytes               string `json:"quota_bytes"`
	QuotaInodes              string `json:"quota_inodes"`
	SourceProjectID          string `json:"source_project_id"`
	SourceNamespaceInode     string `json:"source_namespace_inode"`
	SourceDevice             string `json:"source_device"`
	SourceFilesystemUUID     string `json:"source_filesystem_uuid"`
	SourceRootInode          string `json:"source_root_inode"`
	CheckpointFilesystemUUID string `json:"checkpoint_filesystem_uuid"`
	CheckpointRootInode      string `json:"checkpoint_root_inode"`
}

type ImportCheckpointCommand struct {
	Format            string                  `json:"format"`
	DeploymentID      string                  `json:"deployment_id"`
	ControllerEpoch   string                  `json:"controller_epoch"`
	ControlRevision   string                  `json:"control_revision"`
	InstallationID    string                  `json:"installation_id"`
	OperationID       string                  `json:"operation_id"`
	TargetGeneration  string                  `json:"target_generation"`
	Stage             string                  `json:"stage"`
	EffectNumber      string                  `json:"effect_number"`
	EffectID          string                  `json:"effect_id"`
	Kind              string                  `json:"kind"`
	InstallationFence string                  `json:"installation_fence"`
	PayloadSHA256     string                  `json:"payload_sha256"`
	IssuedMS          string                  `json:"issued_ms"`
	ExpiresMS         string                  `json:"expires_ms"`
	Payload           ImportCheckpointPayload `json:"payload"`
}

type VerifiedImportCheckpointCommand struct{ command ImportCheckpointCommand }

func (v VerifiedImportCheckpointCommand) Command() ImportCheckpointCommand { return v.command }

func validImportCheckpointLogical(c ImportCheckpointCommand) bool {
	p := c.Payload
	if c.Format != ImportCheckpointFormat || c.Stage != "restore_data" || c.Kind != "import-checkpoint" ||
		!shaPattern.MatchString(p.ReleaseSHA256) || !shaPattern.MatchString(p.ManifestSHA256) {
		return false
	}
	for _, id := range []string{
		c.DeploymentID, c.ControllerEpoch, c.InstallationID, c.OperationID, c.EffectID,
		p.GrantID, p.RecoverySetID, p.CheckpointID, p.NamespaceID,
		p.SourceFilesystemUUID, p.CheckpointFilesystemUUID,
	} {
		if !candidateUUID(id) {
			return false
		}
	}
	for _, value := range []string{
		c.TargetGeneration, c.EffectNumber, c.InstallationFence, p.ArchiveBytes, p.DataFormat,
		p.WriterFence, p.ProjectID, p.QuotaBytes, p.QuotaInodes,
	} {
		if _, ok := counter(value, true); !ok {
			return false
		}
	}
	for _, value := range []string{
		p.SourceProjectID, p.SourceNamespaceInode, p.SourceDevice, p.SourceRootInode, p.CheckpointRootInode,
	} {
		if !checkpointUint(value) {
			return false
		}
	}
	project, _ := strconv.ParseInt(p.ProjectID, 10, 64)
	sourceProject, _ := strconv.ParseUint(p.SourceProjectID, 10, 64)
	archive, _ := strconv.ParseInt(p.ArchiveBytes, 10, 64)
	quota, _ := strconv.ParseInt(p.QuotaBytes, 10, 64)
	slot, _ := counter(c.EffectNumber, true)
	// The old and new roots are different physical roots by construction; a
	// payload naming one root for both is a malformed provenance record.
	return project > 0 && project <= math.MaxUint32 && sourceProject <= math.MaxUint32 &&
		archive <= maxRestoreArchiveBytes && quota%512 == 0 && slot <= 32 &&
		(p.SourceFilesystemUUID != p.CheckpointFilesystemUUID || p.SourceRootInode != p.CheckpointRootInode)
}

func ImportCheckpointLogicalDigest(c ImportCheckpointCommand) (string, error) {
	if !validImportCheckpointLogical(c) {
		return "", ErrInvalidCommand
	}
	logical := struct {
		Format           string                  `json:"format"`
		DeploymentID     string                  `json:"deployment_id"`
		ControllerEpoch  string                  `json:"controller_epoch"`
		InstallationID   string                  `json:"installation_id"`
		OperationID      string                  `json:"operation_id"`
		TargetGeneration string                  `json:"target_generation"`
		Stage            string                  `json:"stage"`
		EffectNumber     string                  `json:"effect_number"`
		Kind             string                  `json:"kind"`
		Payload          ImportCheckpointPayload `json:"payload"`
	}{"portal.runner.data-import-effect/1-dev.1", c.DeploymentID, c.ControllerEpoch,
		c.InstallationID, c.OperationID, c.TargetGeneration, c.Stage, c.EffectNumber, c.Kind, c.Payload}
	raw, err := json.Marshal(logical)
	if err != nil {
		return "", ErrInvalidCommand
	}
	sum := sha256.Sum256(frame(importCheckpointEffectPurpose, raw))
	return hex.EncodeToString(sum[:]), nil
}

func parseImportCheckpoint(raw []byte) (ImportCheckpointCommand, error) {
	var c ImportCheckpointCommand
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
		return ImportCheckpointCommand{}, ErrInvalidCommand
	}
	// Only the exact bytes a controller would marshal are admitted, so the signed
	// bytes, the journal copy and a replayed copy cannot differ in encoding.
	canonical, err := json.Marshal(c)
	if err != nil || !bytes.Equal(canonical, raw) {
		return ImportCheckpointCommand{}, ErrInvalidCommand
	}
	return c, nil
}

func VerifyImportCheckpoint(raw, signature []byte, authority Authority, nowMS int64) (VerifiedImportCheckpointCommand, error) {
	return verifyImportCheckpoint(raw, signature, authority, nowMS, false)
}

// VerifyImportCheckpointSettlement checks the exact command already admitted for
// an in-flight effect. Expiry governs dispatch; an import that finishes after
// the signed window must still be settled without reissuing the effect.
func VerifyImportCheckpointSettlement(raw, signature []byte, authority Authority, nowMS int64) (VerifiedImportCheckpointCommand, error) {
	return verifyImportCheckpoint(raw, signature, authority, nowMS, true)
}

func verifyImportCheckpoint(raw, signature []byte, authority Authority, nowMS int64, settling bool) (VerifiedImportCheckpointCommand, error) {
	fail := func() (VerifiedImportCheckpointCommand, error) {
		return VerifiedImportCheckpointCommand{}, ErrInvalidCommand
	}
	if len(authority.PublicKey) != ed25519.PublicKeySize || authority.ControlRevision < 1 || nowMS < 0 || len(signature) != ed25519.SignatureSize {
		return fail()
	}
	keySHA := sha256.Sum256(authority.PublicKey)
	if authority.Fingerprint != hex.EncodeToString(keySHA[:]) {
		return fail()
	}
	c, err := parseImportCheckpoint(raw)
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
	if !ok || expires <= issued || expires-issued > 30000 ||
		issued > nowMS || (!settling && nowMS >= expires) {
		return fail()
	}
	digest, err := ImportCheckpointLogicalDigest(c)
	if err != nil || digest != c.PayloadSHA256 {
		return fail()
	}
	if !ed25519.Verify(ed25519.PublicKey(authority.PublicKey), frame(importCheckpointSignaturePurpose, raw), signature) {
		return fail()
	}
	return VerifiedImportCheckpointCommand{command: c}, nil
}

func SignImportCheckpoint(c ImportCheckpointCommand, key ed25519.PrivateKey, authority Authority, nowMS int64) ([]byte, []byte, error) {
	if len(key) != ed25519.PrivateKeySize {
		return nil, nil, ErrInvalidCommand
	}
	raw, err := json.Marshal(c)
	if err != nil {
		return nil, nil, ErrInvalidCommand
	}
	signature := ed25519.Sign(key, frame(importCheckpointSignaturePurpose, raw))
	if _, err := VerifyImportCheckpoint(raw, signature, authority, nowMS); err != nil {
		return nil, nil, err
	}
	return raw, signature, nil
}
