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
	Handler               string `json:"handler"`
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

type ServiceBackendTaskRegistrationIdentity struct {
	Format          string
	InstallationID  string
	Generation      string
	PolicyRevision  string
	ControllerEpoch string
	ReleaseSHA256   string
	ExpiresMS       string
}

type VerifiedServiceBackendTask struct {
	command             ServiceBackendTaskCommand
	registration        ServiceBackendTaskRegistrationIdentity
	initialRegistration *VerifiedBackendRegistrationCommand
	updateRegistration  *VerifiedUpdateStartRegistrationCommand
}

func (v VerifiedServiceBackendTask) Command() ServiceBackendTaskCommand {
	c := v.command
	c.RegistrationRaw = bytes.Clone(c.RegistrationRaw)
	c.RegistrationSignature = bytes.Clone(c.RegistrationSignature)
	c.ServiceIdentity = bytes.Clone(c.ServiceIdentity)
	return c
}

func (v VerifiedServiceBackendTask) RegistrationIdentity() ServiceBackendTaskRegistrationIdentity {
	return v.registration
}

func (v VerifiedServiceBackendTask) InitialRegistration() (VerifiedBackendRegistrationCommand, bool) {
	if v.initialRegistration == nil {
		return VerifiedBackendRegistrationCommand{}, false
	}
	return *v.initialRegistration, true
}

func (v VerifiedServiceBackendTask) UpdateStartRegistration() (VerifiedUpdateStartRegistrationCommand, bool) {
	if v.updateRegistration == nil {
		return VerifiedUpdateStartRegistrationCommand{}, false
	}
	return *v.updateRegistration, true
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
	var format struct {
		Format string `json:"format"`
	}
	if json.Unmarshal(c.RegistrationRaw, &format) != nil {
		return zero, ErrInvalidCommand
	}
	var registration ServiceBackendTaskRegistrationIdentity
	var initial *VerifiedBackendRegistrationCommand
	var update *VerifiedUpdateStartRegistrationCommand
	switch format.Format {
	case BackendRegistrationFormat:
		verified, err := VerifyBackendRegistration(
			c.RegistrationRaw, c.RegistrationSignature, a, nowMS)
		if err != nil {
			return zero, ErrInvalidCommand
		}
		candidate := verified.Candidate()
		initial = &verified
		registration = ServiceBackendTaskRegistrationIdentity{
			Format:         BackendRegistrationFormat,
			InstallationID: candidate.InstallationID,
			Generation:     candidate.TargetGeneration, PolicyRevision: "1",
			ControllerEpoch: candidate.ControllerEpoch,
			ReleaseSHA256:   candidate.Payload.ReleaseSHA256,
			ExpiresMS:       candidate.ExpiresMS,
		}
	case UpdateStartRegistrationFormat:
		verified, err := VerifyUpdateStartRegistration(
			c.RegistrationRaw, c.RegistrationSignature, a, nowMS)
		if err != nil {
			return zero, ErrInvalidCommand
		}
		start := verified.Start().Command()
		update = &verified
		registration = ServiceBackendTaskRegistrationIdentity{
			Format:          UpdateStartRegistrationFormat,
			InstallationID:  start.InstallationID,
			Generation:      start.TargetGeneration,
			PolicyRevision:  start.Payload.TargetPolicyRevision,
			ControllerEpoch: start.ControllerEpoch,
			ReleaseSHA256:   start.Payload.TargetReleaseSHA256,
			ExpiresMS:       start.ExpiresMS,
		}
	default:
		return zero, ErrInvalidCommand
	}
	if c.Format != ServiceBackendTaskFormat ||
		!candidateUUID(c.RequestID) || !candidateUUID(c.TaskID) ||
		!candidateUUID(c.LeaseID) ||
		(c.Handler != appplatform.TaskHandlerBackend &&
			c.Handler != appplatform.TaskHandlerDigest &&
			c.Handler != appplatform.TaskHandlerEvent) ||
		c.InstallationID != registration.InstallationID ||
		c.Generation != registration.Generation ||
		c.PolicyRevision != registration.PolicyRevision ||
		c.ControllerEpoch != registration.ControllerEpoch ||
		c.ReleaseSHA256 != registration.ReleaseSHA256 ||
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
		!serviceTaskResource(grant.Resources, "tasks.schedule") ||
		c.Handler == appplatform.TaskHandlerDigest &&
			!serviceTaskResource(grant.Resources, "reports.read") ||
		c.Handler == appplatform.TaskHandlerEvent &&
			!serviceTaskResource(grant.Resources, "events.subscribe") {
		return zero, ErrInvalidCommand
	}
	return VerifiedServiceBackendTask{
		command: c, registration: registration,
		initialRegistration: initial, updateRegistration: update,
	}, nil
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
	switch v.command.Handler {
	case appplatform.TaskHandlerBackend:
		request, err := appplatform.DecodeTaskBackendRequest(body)
		if err != nil || request.TaskID != v.command.TaskID ||
			request.OccurrenceID == "" {
			return ErrInvalidCommand
		}
	case appplatform.TaskHandlerDigest:
		request, err := appplatform.DecodeTaskBackendRequest(body)
		if err != nil || request.TaskID != v.command.TaskID ||
			request.OccurrenceID == "" {
			return ErrInvalidCommand
		}
	case appplatform.TaskHandlerEvent:
		if _, err := appplatform.ParseEventDelivery(body); err != nil {
			return ErrInvalidCommand
		}
	default:
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
