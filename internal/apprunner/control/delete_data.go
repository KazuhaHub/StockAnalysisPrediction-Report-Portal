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

const DeleteDataFormat = "portal.runner.delete-data-command/1-dev.1"
const deleteDataSignaturePurpose = "portal.app/runner-delete-data-command/v1"
const deleteDataEffectPurpose = "portal.app/runner-delete-data-effect/v1"

type DeleteDataPayload struct {
	NamespaceID            string `json:"namespace_id"`
	ReleaseSHA256          string `json:"release_sha256"`
	ProjectID              string `json:"project_id"`
	DataFormat             string `json:"data_format"`
	QuotaBytes             string `json:"quota_bytes"`
	QuotaInodes            string `json:"quota_inodes"`
	ExpectedDevice         string `json:"expected_device"`
	ExpectedNamespaceInode string `json:"expected_namespace_inode"`
	WriterFence            string `json:"writer_fence"`
}

type DeleteDataCommand struct {
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
	Payload           DeleteDataPayload `json:"payload"`
}

type VerifiedDeleteDataCommand struct{ command DeleteDataCommand }

func (v VerifiedDeleteDataCommand) Command() DeleteDataCommand { return v.command }

func validDeleteDataLogical(c DeleteDataCommand) bool {
	p := c.Payload
	if c.Format != DeleteDataFormat || c.Stage != "remove_data" ||
		c.Kind != "delete-data" || len(c.EffectNumber) == 0 ||
		!shaPattern.MatchString(p.ReleaseSHA256) {
		return false
	}
	for _, id := range []string{
		c.DeploymentID, c.ControllerEpoch, c.InstallationID,
		c.OperationID, c.EffectID, p.NamespaceID,
	} {
		if !candidateUUID(id) {
			return false
		}
	}
	project, projectErr := strconv.ParseUint(p.ProjectID, 10, 32)
	format, formatErr := strconv.ParseInt(p.DataFormat, 10, 64)
	quota, quotaErr := strconv.ParseInt(p.QuotaBytes, 10, 64)
	inodes, inodesErr := strconv.ParseInt(p.QuotaInodes, 10, 64)
	device, deviceErr := strconv.ParseUint(p.ExpectedDevice, 10, 64)
	inode, inodeErr := strconv.ParseUint(p.ExpectedNamespaceInode, 10, 64)
	writer, writerErr := strconv.ParseInt(p.WriterFence, 10, 64)
	generation, generationErr := strconv.ParseInt(c.TargetGeneration, 10, 64)
	fence, fenceErr := strconv.ParseInt(c.InstallationFence, 10, 64)
	effect, effectErr := strconv.ParseInt(c.EffectNumber, 10, 64)
	return projectErr == nil && project > 0 &&
		formatErr == nil && format > 0 &&
		quotaErr == nil && quota > 0 && quota%512 == 0 &&
		inodesErr == nil && inodes > 0 &&
		deviceErr == nil && device > 0 &&
		inodeErr == nil && inode > 0 &&
		writerErr == nil && writer > 0 &&
		generationErr == nil && generation > 0 &&
		fenceErr == nil && fence > 0 &&
		effectErr == nil && effect > 0 && effect <= 32
}

func DeleteDataLogicalDigest(c DeleteDataCommand) (string, error) {
	if !validDeleteDataLogical(c) {
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
		Payload          DeleteDataPayload `json:"payload"`
	}{"portal.runner.delete-data-effect/1-dev.1", c.DeploymentID,
		c.ControllerEpoch, c.InstallationID, c.OperationID,
		c.TargetGeneration, c.Stage, c.EffectNumber, c.Kind, c.Payload}
	raw, err := json.Marshal(logical)
	if err != nil {
		return "", ErrInvalidCommand
	}
	sum := sha256.Sum256(frame(deleteDataEffectPurpose, raw))
	return hex.EncodeToString(sum[:]), nil
}

func parseDeleteData(raw []byte) (DeleteDataCommand, error) {
	var c DeleteDataCommand
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
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&c) != nil {
		return DeleteDataCommand{}, ErrInvalidCommand
	}
	return c, nil
}

func VerifyDeleteData(raw, signature []byte, a Authority, nowMS int64) (VerifiedDeleteDataCommand, error) {
	var zero VerifiedDeleteDataCommand
	if len(a.PublicKey) != ed25519.PublicKeySize ||
		len(signature) != ed25519.SignatureSize || a.ControlRevision < 1 ||
		nowMS < 0 {
		return zero, ErrInvalidCommand
	}
	keySHA := sha256.Sum256(a.PublicKey)
	if a.Fingerprint != hex.EncodeToString(keySHA[:]) {
		return zero, ErrInvalidCommand
	}
	c, err := parseDeleteData(raw)
	if err != nil {
		return zero, ErrInvalidCommand
	}
	revision, ok := counter(c.ControlRevision, true)
	if !ok || revision != a.ControlRevision ||
		c.DeploymentID != a.DeploymentID ||
		c.ControllerEpoch != a.ControllerEpoch {
		return zero, ErrInvalidCommand
	}
	issued, ok := counter(c.IssuedMS, false)
	if !ok {
		return zero, ErrInvalidCommand
	}
	expires, ok := counter(c.ExpiresMS, false)
	if !ok || issued > nowMS || nowMS >= expires ||
		expires <= issued || expires-issued > 30000 ||
		issued > math.MaxInt64-30000 {
		return zero, ErrInvalidCommand
	}
	digest, err := DeleteDataLogicalDigest(c)
	if err != nil || digest != c.PayloadSHA256 ||
		!ed25519.Verify(
			ed25519.PublicKey(a.PublicKey),
			frame(deleteDataSignaturePurpose, raw), signature) {
		return zero, ErrInvalidCommand
	}
	return VerifiedDeleteDataCommand{command: c}, nil
}

func SignDeleteData(c DeleteDataCommand, key ed25519.PrivateKey, a Authority, nowMS int64) ([]byte, []byte, error) {
	if len(key) != ed25519.PrivateKeySize {
		return nil, nil, ErrInvalidCommand
	}
	raw, err := json.Marshal(c)
	if err != nil {
		return nil, nil, ErrInvalidCommand
	}
	signature := ed25519.Sign(
		key, frame(deleteDataSignaturePurpose, raw))
	if _, err := VerifyDeleteData(
		raw, signature, a, nowMS); err != nil {
		return nil, nil, err
	}
	return raw, signature, nil
}
