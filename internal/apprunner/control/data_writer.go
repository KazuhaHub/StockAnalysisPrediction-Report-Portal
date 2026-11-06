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

const DataWriterFormat = "portal.runner.data-writer-command/1-dev.1"
const DataWriterSandboxProfile = "linux-docker-rootful-projectquota-writer/v1-dev"
const dataWriterSignaturePurpose = "portal.app/runner-data-writer/v1"

// DataWriterCommand separately authorizes an initial writer attempt against one
// exact prepared-data receipt and future container identity. It carries no path,
// Unix identity, permission mode, Docker option or secret. Verification alone
// proves neither journal admission nor exclusion of actual old writers.
type DataWriterCommand struct {
	Format            string `json:"format"`
	EffectID          string `json:"effect_id"`
	PayloadSHA256     string `json:"payload_sha256"`
	DataRaw           []byte `json:"data_raw"`
	DataSignature     []byte `json:"data_signature"`
	DataReceiptSHA256 string `json:"data_receipt_sha256"`
	ResourceID        string `json:"resource_id"`
	Platform          string `json:"platform"`
	SandboxProfile    string `json:"sandbox_profile"`
}

type VerifiedDataWriterCommand struct {
	command DataWriterCommand
	data    Command
}

func (v VerifiedDataWriterCommand) Command() DataWriterCommand {
	c := v.command
	c.DataRaw, c.DataSignature = bytes.Clone(c.DataRaw), bytes.Clone(c.DataSignature)
	return c
}
func (v VerifiedDataWriterCommand) Data() Command { return v.data }

func DataWriterLogicalDigest(c DataWriterCommand) (string, error) {
	ref, err := parse(c.DataRaw)
	if err != nil || c.Format != DataWriterFormat || !candidateUUID(c.EffectID) || !candidateUUID(c.ResourceID) || c.EffectID == ref.EffectID || c.ResourceID == ref.Payload.DataNamespaceID || c.EffectID == c.ResourceID || len(c.DataSignature) != ed25519.SignatureSize || !shaPattern.MatchString(c.DataReceiptSHA256) || c.SandboxProfile != DataWriterSandboxProfile || (c.Platform != "linux/amd64" && c.Platform != "linux/arm64") {
		return "", ErrInvalidCommand
	}
	// This first contract covers a fresh initial allocation only. Updating or
	// taking over a writer requires an explicit later fence/revocation protocol.
	if ref.TargetGeneration != "1" || ref.Payload.WriterFence != "1" || ref.EffectNumber != "1" || !candidateUUID(ref.Payload.DataNamespaceID) {
		return "", ErrInvalidCommand
	}
	for _, id := range []string{ref.DeploymentID, ref.ControllerEpoch, ref.InstallationID, ref.OperationID, ref.EffectID} {
		if !candidateUUID(id) {
			return "", ErrInvalidCommand
		}
	}
	digest, err := LogicalDigest(ref)
	if err != nil || digest != ref.PayloadSHA256 {
		return "", ErrInvalidCommand
	}
	logical := struct {
		Format            string `json:"format"`
		DeploymentID      string `json:"deployment_id"`
		ControllerEpoch   string `json:"controller_epoch"`
		InstallationID    string `json:"installation_id"`
		OperationID       string `json:"operation_id"`
		Generation        string `json:"generation"`
		DataEffectID      string `json:"data_effect_id"`
		DataPayloadSHA256 string `json:"data_payload_sha256"`
		DataReceiptSHA256 string `json:"data_receipt_sha256"`
		ResourceID        string `json:"resource_id"`
		Platform          string `json:"platform"`
		SandboxProfile    string `json:"sandbox_profile"`
	}{"portal.runner.data-writer-effect/1-dev.1", ref.DeploymentID, ref.ControllerEpoch, ref.InstallationID, ref.OperationID, ref.TargetGeneration, ref.EffectID, ref.PayloadSHA256, c.DataReceiptSHA256, c.ResourceID, c.Platform, c.SandboxProfile}
	raw, _ := json.Marshal(logical)
	sum := sha256.Sum256(frame("portal.app/runner-data-writer-effect/v1", raw))
	return hex.EncodeToString(sum[:]), nil
}

func parseDataWriter(raw []byte) (DataWriterCommand, error) {
	var c DataWriterCommand
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
		return DataWriterCommand{}, ErrInvalidCommand
	}
	return c, nil
}

func VerifyDataWriter(raw, signature []byte, a Authority, nowMS int64) (VerifiedDataWriterCommand, error) {
	var zero VerifiedDataWriterCommand
	if len(signature) != ed25519.SignatureSize {
		return zero, ErrInvalidCommand
	}
	c, err := parseDataWriter(raw)
	if err != nil {
		return zero, ErrInvalidCommand
	}
	ref, err := Verify(c.DataRaw, c.DataSignature, a, nowMS)
	if err != nil {
		return zero, ErrInvalidCommand
	}
	digest, err := DataWriterLogicalDigest(c)
	if err != nil || digest != c.PayloadSHA256 || !ed25519.Verify(ed25519.PublicKey(a.PublicKey), frame(dataWriterSignaturePurpose, raw), signature) {
		return zero, ErrInvalidCommand
	}
	return VerifiedDataWriterCommand{command: c, data: ref.Command()}, nil
}

func SignDataWriter(c DataWriterCommand, key ed25519.PrivateKey, a Authority, nowMS int64) ([]byte, []byte, error) {
	if len(key) != ed25519.PrivateKeySize {
		return nil, nil, ErrInvalidCommand
	}
	raw, err := json.Marshal(c)
	if err != nil {
		return nil, nil, ErrInvalidCommand
	}
	signature := ed25519.Sign(key, frame(dataWriterSignaturePurpose, raw))
	if _, err := VerifyDataWriter(raw, signature, a, nowMS); err != nil {
		return nil, nil, err
	}
	return raw, signature, nil
}
