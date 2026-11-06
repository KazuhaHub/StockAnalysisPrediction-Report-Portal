package control

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"unicode/utf8"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

const UpdateStartRegistrationFormat = "portal.runner.update-start-registration-command/1-dev.1"
const updateStartRegistrationSignaturePurpose = "portal.app/runner-update-start-registration/v1"

// UpdateStartRegistrationCommand carries digests only. StartRaw is a freshly
// signed reference to the existing update-start effect; it cannot authorize
// another start. Private registration bytes never enter this command or the
// runner journal.
type UpdateStartRegistrationCommand struct {
	Format               string `json:"format"`
	EffectID             string `json:"effect_id"`
	PayloadSHA256        string `json:"payload_sha256"`
	StartRaw             []byte `json:"start_raw"`
	StartSignature       []byte `json:"start_signature"`
	RunningReceiptSHA256 string `json:"running_receipt_sha256"`
	DocumentSHA256       string `json:"document_sha256"`
}

type VerifiedUpdateStartRegistrationCommand struct {
	command UpdateStartRegistrationCommand
	start   VerifiedUpdateStartCommand
}

func (v VerifiedUpdateStartRegistrationCommand) Command() UpdateStartRegistrationCommand {
	c := v.command
	c.StartRaw, c.StartSignature = bytes.Clone(c.StartRaw), bytes.Clone(c.StartSignature)
	return c
}

func (v VerifiedUpdateStartRegistrationCommand) Start() VerifiedUpdateStartCommand {
	return v.start
}

func UpdateStartRegistrationLogicalDigest(c UpdateStartRegistrationCommand) (string, error) {
	start, err := parseUpdateStart(c.StartRaw)
	if err != nil || c.Format != UpdateStartRegistrationFormat ||
		!candidateUUID(c.EffectID) || len(c.StartSignature) != ed25519.SignatureSize ||
		!shaPattern.MatchString(c.RunningReceiptSHA256) ||
		!shaPattern.MatchString(c.DocumentSHA256) {
		return "", ErrInvalidCommand
	}
	digest, err := UpdateStartLogicalDigest(start)
	generation, generationErr := strconv.ParseInt(start.TargetGeneration, 10, 64)
	policy, policyErr := strconv.ParseInt(start.Payload.TargetPolicyRevision, 10, 64)
	if err != nil || digest != start.PayloadSHA256 || generationErr != nil || generation < 2 ||
		policyErr != nil || policy < 1 {
		return "", ErrInvalidCommand
	}
	for _, id := range []string{
		start.EffectID, start.OperationID, start.InstallationID, start.Payload.ResourceID,
		start.Payload.NamespaceID, start.Payload.SourceOperationID,
		start.Payload.SourceStartEffectID, start.Payload.SourceDataEffectID,
		start.Payload.SourceCheckpointEffectID, start.Payload.CheckpointID,
	} {
		if c.EffectID == id {
			return "", ErrInvalidCommand
		}
	}
	logical := struct {
		Format                  string `json:"format"`
		StartEffectID           string `json:"start_effect_id"`
		StartPayloadSHA256      string `json:"start_payload_sha256"`
		RunningReceiptSHA256    string `json:"running_receipt_sha256"`
		DocumentSHA256          string `json:"document_sha256"`
		TargetGeneration        string `json:"target_generation"`
		TargetPolicyRevision    string `json:"target_policy_revision"`
		TargetReleaseSHA256     string `json:"target_release_sha256"`
		InstallationFence       string `json:"installation_fence"`
		ControllerEpoch         string `json:"controller_epoch"`
		InstallationID          string `json:"installation_id"`
		OperationID             string `json:"operation_id"`
		ResourceID              string `json:"resource_id"`
		DataNamespaceID         string `json:"data_namespace_id"`
		CheckpointReceiptSHA256 string `json:"checkpoint_receipt_sha256"`
	}{
		"portal.runner.update-start-registration-effect/1-dev.1",
		start.EffectID, start.PayloadSHA256, c.RunningReceiptSHA256,
		c.DocumentSHA256, start.TargetGeneration, start.Payload.TargetPolicyRevision,
		start.Payload.TargetReleaseSHA256, start.InstallationFence,
		start.ControllerEpoch, start.InstallationID, start.OperationID,
		start.Payload.ResourceID, start.Payload.NamespaceID,
		start.Payload.SourceCheckpointReceiptSHA256,
	}
	raw, err := json.Marshal(logical)
	if err != nil {
		return "", ErrInvalidCommand
	}
	sum := sha256.Sum256(frame("portal.app/runner-update-start-registration-effect/v1", raw))
	return hex.EncodeToString(sum[:]), nil
}

func parseUpdateStartRegistration(raw []byte) (UpdateStartRegistrationCommand, error) {
	var c UpdateStartRegistrationCommand
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
		return UpdateStartRegistrationCommand{}, ErrInvalidCommand
	}
	return c, nil
}

func VerifyUpdateStartRegistration(raw, signature []byte, a Authority, nowMS int64) (VerifiedUpdateStartRegistrationCommand, error) {
	var zero VerifiedUpdateStartRegistrationCommand
	if len(signature) != ed25519.SignatureSize {
		return zero, ErrInvalidCommand
	}
	c, err := parseUpdateStartRegistration(raw)
	if err != nil {
		return zero, err
	}
	start, err := VerifyUpdateStart(c.StartRaw, c.StartSignature, a, nowMS)
	if err != nil {
		return zero, fmt.Errorf(
			"update-start registration start: %w", err)
	}
	digest, err := UpdateStartRegistrationLogicalDigest(c)
	if err != nil || digest != c.PayloadSHA256 {
		return zero, fmt.Errorf(
			"update-start registration digest: %w", ErrInvalidCommand)
	}
	if !ed25519.Verify(
		ed25519.PublicKey(a.PublicKey),
		frame(updateStartRegistrationSignaturePurpose, raw),
		signature) {
		return zero, fmt.Errorf(
			"update-start registration signature: %w", ErrInvalidCommand)
	}
	return VerifiedUpdateStartRegistrationCommand{command: c, start: start}, nil
}

func SignUpdateStartRegistration(c UpdateStartRegistrationCommand, key ed25519.PrivateKey, a Authority, nowMS int64) ([]byte, []byte, error) {
	if len(key) != ed25519.PrivateKeySize {
		return nil, nil, ErrInvalidCommand
	}
	raw, err := json.Marshal(c)
	if err != nil {
		return nil, nil, ErrInvalidCommand
	}
	signature := ed25519.Sign(key, frame(updateStartRegistrationSignaturePurpose, raw))
	if _, err := VerifyUpdateStartRegistration(raw, signature, a, nowMS); err != nil {
		return nil, nil, err
	}
	return raw, signature, nil
}

// ValidateUpdateStartRegistrationDocument validates private bytes separately
// from journal admission. Parsing proves neither file delivery nor a completed
// application handshake.
func ValidateUpdateStartRegistrationDocument(v VerifiedUpdateStartRegistrationCommand, descriptor, document []byte) (appplatform.BackendRegistration, error) {
	var zero appplatform.BackendRegistration
	if v.command.Format != UpdateStartRegistrationFormat || len(document) == 0 || len(document) > 4096 {
		return zero, ErrInvalidCommand
	}
	sum := sha256.Sum256(document)
	if hex.EncodeToString(sum[:]) != v.command.DocumentSHA256 {
		return zero, ErrInvalidCommand
	}
	parsed, err := appplatform.ParseDescriptor(descriptor)
	if err != nil || parsed.Manifest.Backend == nil || parsed.SHA256 != v.start.command.Payload.TargetReleaseSHA256 {
		return zero, ErrInvalidCommand
	}
	registration, err := appplatform.ParseBackendRegistration(document)
	if err != nil {
		return zero, ErrInvalidCommand
	}
	metadata, err := registration.Metadata()
	identity := metadata.Identity
	if err != nil || identity.AppID != parsed.Manifest.ID ||
		identity.DeploymentID != v.start.command.DeploymentID ||
		identity.ControllerEpoch != v.start.command.ControllerEpoch ||
		identity.InstallationID != v.start.command.InstallationID ||
		identity.Generation != v.start.command.TargetGeneration ||
		identity.Generation == "1" ||
		identity.PolicyRevision != v.start.command.Payload.TargetPolicyRevision ||
		identity.ReleaseSHA256 != v.start.command.Payload.TargetReleaseSHA256 {
		registration.Close()
		return zero, ErrInvalidCommand
	}
	return registration, nil
}
