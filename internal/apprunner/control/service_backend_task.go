package control

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"unicode/utf8"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

const ServiceBackendTaskFormat = "portal.runner.service-backend-task/1-dev.1"
const serviceBackendTaskSignaturePurpose = "portal.app/runner-service-backend-task/v1"

type ServiceBackendTaskCommand struct {
	Format                string `json:"format"`
	RequestID             string `json:"request_id"`
	TaskID                string `json:"task_id"`
	LeaseID               string `json:"lease_id"`
	InstallationID        string `json:"installation_id"`
	Generation            string `json:"generation"`
	PolicyRevision        string `json:"policy_revision"`
	ControllerEpoch       string `json:"controller_epoch"`
	ReleaseSHA256         string `json:"release_sha256"`
	BootID                string `json:"boot_id"`
	RegistrationRaw       []byte `json:"registration_raw"`
	RegistrationSignature []byte `json:"registration_signature"`
	ServiceIdentity       []byte `json:"service_identity"`
	BodySHA256            string `json:"body_sha256"`
}

type VerifiedServiceBackendTask struct {
	command      ServiceBackendTaskCommand
	registration VerifiedBackendRegistrationCommand
}

func (v VerifiedServiceBackendTask) Command() ServiceBackendTaskCommand {
	c := v.command
	c.RegistrationRaw = bytes.Clone(c.RegistrationRaw)
	c.RegistrationSignature = bytes.Clone(c.RegistrationSignature)
	c.ServiceIdentity = bytes.Clone(c.ServiceIdentity)
	return c
}

func (v VerifiedServiceBackendTask) Registration() VerifiedBackendRegistrationCommand {
	return v.registration
}

func VerifyServiceBackendTask(raw, signature []byte, a Authority, nowMS int64) (VerifiedServiceBackendTask, error) {
	var zero VerifiedServiceBackendTask
	var c ServiceBackendTaskCommand
	if len(raw) == 0 || len(raw) > MaxCommandBytes || len(signature) != ed25519.SignatureSize || !utf8.Valid(raw) {
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
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&c) != nil {
		return zero, ErrInvalidCommand
	}
	registration, err := VerifyBackendRegistration(c.RegistrationRaw, c.RegistrationSignature, a, nowMS)
	if err != nil {
		return zero, ErrInvalidCommand
	}
	candidate := registration.Candidate()
	if c.Format != ServiceBackendTaskFormat ||
		!candidateUUID(c.RequestID) || !candidateUUID(c.TaskID) ||
		!candidateUUID(c.LeaseID) ||
		c.InstallationID != candidate.InstallationID ||
		c.Generation != candidate.TargetGeneration || c.Generation != "1" ||
		c.PolicyRevision != "1" ||
		c.ControllerEpoch != candidate.ControllerEpoch ||
		c.ReleaseSHA256 != candidate.Payload.ReleaseSHA256 ||
		len(c.BootID) < 1 || len(c.BootID) > 128 || !utf8.ValidString(c.BootID) ||
		len(c.ServiceIdentity) < 1 || len(c.ServiceIdentity) > 8192 ||
		!utf8.Valid(c.ServiceIdentity) || !json.Valid(c.ServiceIdentity) ||
		!shaPattern.MatchString(c.BodySHA256) ||
		!ed25519.Verify(ed25519.PublicKey(a.PublicKey), frame(serviceBackendTaskSignaturePurpose, raw), signature) {
		return zero, ErrInvalidCommand
	}
	var grant appplatform.ServiceIdentityGrant
	identityDecoder := json.NewDecoder(bytes.NewReader(c.ServiceIdentity))
	identityDecoder.DisallowUnknownFields()
	if identityDecoder.Decode(&grant) != nil ||
		grant.Format != appplatform.ServiceIdentityFormat ||
		grant.InstallationID != c.InstallationID ||
		grant.LeaseID != c.LeaseID ||
		grant.Generation != c.Generation ||
		grant.PolicyRevision != c.PolicyRevision ||
		grant.ControllerEpoch != c.ControllerEpoch ||
		grant.ReleaseSHA256 != c.ReleaseSHA256 ||
		len(grant.Mac) != 64 ||
		!serviceTaskResource(grant.Resources, "tasks.schedule") {
		return zero, ErrInvalidCommand
	}
	return VerifiedServiceBackendTask{command: c, registration: registration}, nil
}

func serviceTaskResource(resources []string, required string) bool {
	previous := ""
	found := false
	for _, resource := range resources {
		if resource == "" || previous >= resource {
			return false
		}
		previous = resource
		found = found || resource == required
	}
	return found
}

func SignServiceBackendTask(c ServiceBackendTaskCommand, key ed25519.PrivateKey, a Authority, nowMS int64) ([]byte, []byte, error) {
	if len(key) != ed25519.PrivateKeySize {
		return nil, nil, ErrInvalidCommand
	}
	raw, err := json.Marshal(c)
	if err != nil {
		return nil, nil, ErrInvalidCommand
	}
	signature := ed25519.Sign(key, frame(serviceBackendTaskSignaturePurpose, raw))
	if _, err := VerifyServiceBackendTask(raw, signature, a, nowMS); err != nil {
		return nil, nil, err
	}
	return raw, signature, nil
}

func ValidateServiceBackendTaskData(v VerifiedServiceBackendTask, body []byte) error {
	if v.command.Format != ServiceBackendTaskFormat ||
		len(body) == 0 || len(body) > appplatform.MaxTaskBackendBody ||
		!utf8.Valid(body) || !json.Valid(body) {
		return ErrInvalidCommand
	}
	request, err := appplatform.DecodeTaskBackendRequest(body)
	if err != nil || request.TaskID != v.command.TaskID ||
		request.OccurrenceID == "" {
		return ErrInvalidCommand
	}
	digest := serviceTaskBodyDigest(body)
	if digest != v.command.BodySHA256 {
		return ErrInvalidCommand
	}
	return nil
}

func serviceTaskBodyDigest(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}
