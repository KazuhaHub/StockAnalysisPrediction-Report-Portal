package control

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"math"
	"unicode/utf8"
)

const MigrateDataFormat = "portal.runner.data-migrate-command/1-dev.1"
const migrateDataSignaturePurpose = "portal.app/runner-data-migrate-command/v1"
const migrateDataReadPurpose = "portal.app/runner-data-migrate-read/v1"
const migrateDataEffectPurpose = "portal.app/runner-data-migrate-effect/v1"

const DiscardMigrationFormat = "portal.runner.data-discard-migration-command/1-dev.1"
const discardMigrationSignaturePurpose = "portal.app/runner-data-discard-migration-command/v1"
const discardMigrationReadPurpose = "portal.app/runner-data-discard-migration-read/v1"
const discardMigrationEffectPurpose = "portal.app/runner-data-discard-migration-effect/v1"

// MigrateDataEffectNumber and DiscardMigrationEffectNumber are the fixed slots of the migrate stage:
// slot 1 is the source removal that stage already carries.
const MigrateDataEffectNumber = "2"
const DiscardMigrationEffectNumber = "3"

// MaxMigrationWallClockSeconds bounds a migration job; it equals the descriptor limit.
const MaxMigrationWallClockSeconds = 600

// CommandHeader is the envelope every data-lifecycle command carries.
type CommandHeader struct {
	Format            string `json:"format"`
	DeploymentID      string `json:"deployment_id"`
	ControllerEpoch   string `json:"controller_epoch"`
	ControlRevision   string `json:"control_revision"`
	InstallationID    string `json:"installation_id"`
	OperationID       string `json:"operation_id"`
	TargetGeneration  string `json:"target_generation"`
	Stage             string `json:"stage"`
	EffectNumber      string `json:"effect_number"`
	EffectID          string `json:"effect_id"`
	Kind              string `json:"kind"`
	InstallationFence string `json:"installation_fence"`
	PayloadSHA256     string `json:"payload_sha256"`
	IssuedMS          string `json:"issued_ms"`
	ExpiresMS         string `json:"expires_ms"`
}

// MigrateDataPayload binds one migration job: the verified pre-update checkpoint it can be undone
// to, the applied source removal it follows, the schemas it converts between and the writer fences
// (checkpoint S < WriterFence < StartWriterFence) that order it between the source and the target
// start. The entrypoint itself comes from the signed descriptor; only its digest is signed here.
type MigrateDataPayload struct {
	NamespaceID             string `json:"namespace_id"`
	ResourceID              string `json:"resource_id"`
	CheckpointID            string `json:"checkpoint_id"`
	CheckpointEffectID      string `json:"checkpoint_effect_id"`
	CheckpointReceiptSHA256 string `json:"checkpoint_receipt_sha256"`
	RemoveCodeEffectID      string `json:"remove_code_effect_id"`
	RemoveCodeReceiptSHA256 string `json:"remove_code_receipt_sha256"`
	SourceReleaseSHA256     string `json:"source_release_sha256"`
	TargetReleaseSHA256     string `json:"target_release_sha256"`
	SourceSchema            string `json:"source_schema"`
	TargetSchema            string `json:"target_schema"`
	CheckpointWriterFence   string `json:"checkpoint_writer_fence"`
	WriterFence             string `json:"writer_fence"`
	StartWriterFence        string `json:"start_writer_fence"`
	ProjectID               string `json:"project_id"`
	NamespaceInode          string `json:"namespace_inode"`
	Device                  string `json:"device"`
	QuotaBytes              string `json:"quota_bytes"`
	QuotaInodes             string `json:"quota_inodes"`
	WallClockSeconds        string `json:"wall_clock_seconds"`
	EntrypointSHA256        string `json:"entrypoint_sha256"`
}

type MigrateDataCommand struct {
	CommandHeader
	Payload MigrateDataPayload `json:"payload"`
}

// DiscardMigrationPayload names the migrate-data effect to settle. It carries no native identifier:
// the runner finds the job by the name and labels it derived for that effect.
type DiscardMigrationPayload struct {
	NamespaceID          string `json:"namespace_id"`
	ResourceID           string `json:"resource_id"`
	MigrateEffectID      string `json:"migrate_effect_id"`
	MigratePayloadSHA256 string `json:"migrate_payload_sha256"`
	TargetReleaseSHA256  string `json:"target_release_sha256"`
}

type DiscardMigrationCommand struct {
	CommandHeader
	Payload DiscardMigrationPayload `json:"payload"`
}

type VerifiedMigrateDataCommand struct{ command MigrateDataCommand }

func (v VerifiedMigrateDataCommand) Command() MigrateDataCommand { return v.command }

type VerifiedDiscardMigrationCommand struct{ command DiscardMigrationCommand }

func (v VerifiedDiscardMigrationCommand) Command() DiscardMigrationCommand { return v.command }

func validHeader(h CommandHeader, format, stage, kind, slot string) bool {
	if h.Format != format || h.Stage != stage || h.Kind != kind || h.EffectNumber != slot {
		return false
	}
	for _, id := range []string{h.DeploymentID, h.ControllerEpoch, h.InstallationID, h.OperationID, h.EffectID} {
		if !candidateUUID(id) {
			return false
		}
	}
	for _, value := range []string{h.TargetGeneration, h.EffectNumber, h.InstallationFence} {
		if _, ok := counter(value, true); !ok {
			return false
		}
	}
	return true
}

func validMigrateDataLogical(c MigrateDataCommand) bool {
	p := c.Payload
	if !validHeader(c.CommandHeader, MigrateDataFormat, "migrate", "migrate-data", MigrateDataEffectNumber) {
		return false
	}
	for _, sha := range []string{p.CheckpointReceiptSHA256, p.RemoveCodeReceiptSHA256, p.SourceReleaseSHA256, p.TargetReleaseSHA256, p.EntrypointSHA256} {
		if !shaPattern.MatchString(sha) {
			return false
		}
	}
	for _, id := range []string{p.NamespaceID, p.ResourceID, p.CheckpointID, p.CheckpointEffectID, p.RemoveCodeEffectID} {
		if !candidateUUID(id) {
			return false
		}
	}
	if p.ResourceID == p.NamespaceID || c.EffectID == p.ResourceID || c.EffectID == p.NamespaceID {
		return false
	}
	var numbers [9]int64
	for i, value := range []string{p.SourceSchema, p.TargetSchema, p.CheckpointWriterFence, p.WriterFence, p.StartWriterFence, p.ProjectID, p.QuotaBytes, p.QuotaInodes, p.WallClockSeconds} {
		n, ok := counter(value, true)
		if !ok {
			return false
		}
		numbers[i] = n
	}
	if !checkpointUint(p.NamespaceInode) || !checkpointUint(p.Device) {
		return false
	}
	source, target, checkpoint, writer, start := numbers[0], numbers[1], numbers[2], numbers[3], numbers[4]
	return source < target && checkpoint < writer && writer < start &&
		numbers[5] <= math.MaxUint32 && numbers[6]%512 == 0 &&
		numbers[8] <= MaxMigrationWallClockSeconds
}

func validDiscardMigrationLogical(c DiscardMigrationCommand) bool {
	p := c.Payload
	if !validHeader(c.CommandHeader, DiscardMigrationFormat, "migrate", "discard-migration", DiscardMigrationEffectNumber) ||
		!shaPattern.MatchString(p.MigratePayloadSHA256) || !shaPattern.MatchString(p.TargetReleaseSHA256) {
		return false
	}
	for _, id := range []string{p.NamespaceID, p.ResourceID, p.MigrateEffectID} {
		if !candidateUUID(id) {
			return false
		}
	}
	return c.EffectID != p.MigrateEffectID && p.ResourceID != p.NamespaceID
}

func logicalDigest(effectPurpose, effectFormat string, h CommandHeader, payload any) (string, error) {
	logical := struct {
		Format           string `json:"format"`
		DeploymentID     string `json:"deployment_id"`
		ControllerEpoch  string `json:"controller_epoch"`
		InstallationID   string `json:"installation_id"`
		OperationID      string `json:"operation_id"`
		TargetGeneration string `json:"target_generation"`
		Stage            string `json:"stage"`
		EffectNumber     string `json:"effect_number"`
		Kind             string `json:"kind"`
		Payload          any    `json:"payload"`
	}{effectFormat, h.DeploymentID, h.ControllerEpoch, h.InstallationID, h.OperationID, h.TargetGeneration, h.Stage, h.EffectNumber, h.Kind, payload}
	raw, err := json.Marshal(logical)
	if err != nil {
		return "", ErrInvalidCommand
	}
	sum := sha256.Sum256(frame(effectPurpose, raw))
	return hex.EncodeToString(sum[:]), nil
}

func MigrateDataLogicalDigest(c MigrateDataCommand) (string, error) {
	if !validMigrateDataLogical(c) {
		return "", ErrInvalidCommand
	}
	return logicalDigest(migrateDataEffectPurpose, "portal.runner.data-migrate-effect/1-dev.1", c.CommandHeader, c.Payload)
}

func DiscardMigrationLogicalDigest(c DiscardMigrationCommand) (string, error) {
	if !validDiscardMigrationLogical(c) {
		return "", ErrInvalidCommand
	}
	return logicalDigest(discardMigrationEffectPurpose, "portal.runner.data-discard-migration-effect/1-dev.1", c.CommandHeader, c.Payload)
}

// parseCanonicalCommand admits only the exact bytes a controller would marshal, with no duplicate or
// unknown keys, so the signed bytes, the journal copy and a replayed copy cannot differ.
func parseCanonicalCommand[T any](raw []byte) (T, error) {
	var c, zero T
	if len(raw) == 0 || len(raw) > MaxCommandBytes || !utf8.Valid(raw) {
		return zero, ErrInvalidCommand
	}
	scan := json.NewDecoder(bytes.NewReader(raw))
	scan.UseNumber()
	first, err := scan.Token()
	if err != nil || first != json.Delim('{') || !strictObject(scan, 1) {
		return zero, ErrInvalidCommand
	}
	if _, err := scan.Token(); err != io.EOF {
		return zero, ErrInvalidCommand
	}
	decode := json.NewDecoder(bytes.NewReader(raw))
	decode.DisallowUnknownFields()
	if decode.Decode(&c) != nil {
		return zero, ErrInvalidCommand
	}
	canonical, err := json.Marshal(c)
	if err != nil || !bytes.Equal(canonical, raw) {
		return zero, ErrInvalidCommand
	}
	return c, nil
}

// verifyEnvelope checks the authority, window and signature common to every such command.
func verifyEnvelope(h CommandHeader, raw, signature []byte, authority Authority, nowMS int64, settling bool, purpose string, digest func() (string, error)) bool {
	if len(authority.PublicKey) != ed25519.PublicKeySize || authority.ControlRevision < 1 || nowMS < 0 || len(signature) != ed25519.SignatureSize {
		return false
	}
	keySHA := sha256.Sum256(authority.PublicKey)
	if authority.Fingerprint != hex.EncodeToString(keySHA[:]) {
		return false
	}
	revision, ok := counter(h.ControlRevision, true)
	if !ok || revision != authority.ControlRevision || h.DeploymentID != authority.DeploymentID || h.ControllerEpoch != authority.ControllerEpoch {
		return false
	}
	issued, ok := counter(h.IssuedMS, false)
	if !ok {
		return false
	}
	expires, ok := counter(h.ExpiresMS, false)
	if !ok || expires <= issued || expires-issued > 30000 || issued > nowMS || (!settling && nowMS >= expires) {
		return false
	}
	computed, err := digest()
	if err != nil || computed != h.PayloadSHA256 {
		return false
	}
	return ed25519.Verify(ed25519.PublicKey(authority.PublicKey), frame(purpose, raw), signature)
}

func verifyMigrateData(raw, signature []byte, authority Authority, nowMS int64, settling bool, purpose string) (VerifiedMigrateDataCommand, error) {
	c, err := parseCanonicalCommand[MigrateDataCommand](raw)
	if err != nil || !verifyEnvelope(c.CommandHeader, raw, signature, authority, nowMS, settling, purpose, func() (string, error) { return MigrateDataLogicalDigest(c) }) {
		return VerifiedMigrateDataCommand{}, ErrInvalidCommand
	}
	return VerifiedMigrateDataCommand{command: c}, nil
}

func VerifyMigrateData(raw, signature []byte, authority Authority, nowMS int64) (VerifiedMigrateDataCommand, error) {
	return verifyMigrateData(raw, signature, authority, nowMS, false, migrateDataSignaturePurpose)
}

// VerifyMigrateDataSettlement checks the exact command already admitted for an in-flight job. Expiry
// governs dispatch; a job that finishes after the signed window must still be settled.
func VerifyMigrateDataSettlement(raw, signature []byte, authority Authority, nowMS int64) (VerifiedMigrateDataCommand, error) {
	return verifyMigrateData(raw, signature, authority, nowMS, true, migrateDataSignaturePurpose)
}

// VerifyMigrateDataRead checks a read-back command, signed under its own purpose and not bound by the window.
func VerifyMigrateDataRead(raw, signature []byte, authority Authority, nowMS int64) (VerifiedMigrateDataCommand, error) {
	return verifyMigrateData(raw, signature, authority, nowMS, true, migrateDataReadPurpose)
}

func SignMigrateData(c MigrateDataCommand, key ed25519.PrivateKey, authority Authority, nowMS int64) ([]byte, []byte, error) {
	if len(key) != ed25519.PrivateKeySize {
		return nil, nil, ErrInvalidCommand
	}
	raw, err := json.Marshal(c)
	if err != nil {
		return nil, nil, ErrInvalidCommand
	}
	signature := ed25519.Sign(key, frame(migrateDataSignaturePurpose, raw))
	if _, err := VerifyMigrateData(raw, signature, authority, nowMS); err != nil {
		return nil, nil, err
	}
	return raw, signature, nil
}

func SignMigrateDataRead(raw []byte, key ed25519.PrivateKey, authority Authority, nowMS int64) ([]byte, error) {
	if len(key) != ed25519.PrivateKeySize {
		return nil, ErrInvalidCommand
	}
	signature := ed25519.Sign(key, frame(migrateDataReadPurpose, raw))
	if _, err := VerifyMigrateDataRead(raw, signature, authority, nowMS); err != nil {
		return nil, err
	}
	return signature, nil
}

func verifyDiscardMigration(raw, signature []byte, authority Authority, nowMS int64, settling bool, purpose string) (VerifiedDiscardMigrationCommand, error) {
	c, err := parseCanonicalCommand[DiscardMigrationCommand](raw)
	if err != nil || !verifyEnvelope(c.CommandHeader, raw, signature, authority, nowMS, settling, purpose, func() (string, error) { return DiscardMigrationLogicalDigest(c) }) {
		return VerifiedDiscardMigrationCommand{}, ErrInvalidCommand
	}
	return VerifiedDiscardMigrationCommand{command: c}, nil
}

func VerifyDiscardMigration(raw, signature []byte, authority Authority, nowMS int64) (VerifiedDiscardMigrationCommand, error) {
	return verifyDiscardMigration(raw, signature, authority, nowMS, false, discardMigrationSignaturePurpose)
}

func VerifyDiscardMigrationSettlement(raw, signature []byte, authority Authority, nowMS int64) (VerifiedDiscardMigrationCommand, error) {
	return verifyDiscardMigration(raw, signature, authority, nowMS, true, discardMigrationSignaturePurpose)
}

func VerifyDiscardMigrationRead(raw, signature []byte, authority Authority, nowMS int64) (VerifiedDiscardMigrationCommand, error) {
	return verifyDiscardMigration(raw, signature, authority, nowMS, true, discardMigrationReadPurpose)
}

func SignDiscardMigration(c DiscardMigrationCommand, key ed25519.PrivateKey, authority Authority, nowMS int64) ([]byte, []byte, error) {
	if len(key) != ed25519.PrivateKeySize {
		return nil, nil, ErrInvalidCommand
	}
	raw, err := json.Marshal(c)
	if err != nil {
		return nil, nil, ErrInvalidCommand
	}
	signature := ed25519.Sign(key, frame(discardMigrationSignaturePurpose, raw))
	if _, err := VerifyDiscardMigration(raw, signature, authority, nowMS); err != nil {
		return nil, nil, err
	}
	return raw, signature, nil
}

func SignDiscardMigrationRead(raw []byte, key ed25519.PrivateKey, authority Authority, nowMS int64) ([]byte, error) {
	if len(key) != ed25519.PrivateKeySize {
		return nil, ErrInvalidCommand
	}
	signature := ed25519.Sign(key, frame(discardMigrationReadPurpose, raw))
	if _, err := VerifyDiscardMigrationRead(raw, signature, authority, nowMS); err != nil {
		return nil, err
	}
	return signature, nil
}
