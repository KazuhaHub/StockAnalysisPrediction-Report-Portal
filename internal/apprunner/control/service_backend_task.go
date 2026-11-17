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
	command              ServiceBackendTaskCommand
	registration         ServiceBackendTaskRegistrationIdentity
	initialRegistration  *VerifiedBackendRegistrationCommand
	updateRegistration   *VerifiedUpdateStartRegistrationCommand
	writableRegistration *VerifiedWritableBackendRegistrationCommand
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

// WritableRegistration returns the generation-1 writable install registration
// a task was bound to. It is present only for WritableBackendRegistrationFormat.
func (v VerifiedServiceBackendTask) WritableRegistration() (VerifiedWritableBackendRegistrationCommand, bool) {
	if v.writableRegistration == nil {
		return VerifiedWritableBackendRegistrationCommand{}, false
	}
	return *v.writableRegistration, true
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
	bound, err := verifyServiceRegistration(c.RegistrationRaw, c.RegistrationSignature, c.PolicyRevision, a, nowMS)
	if err != nil {
		return zero, err
	}
	registration, initial, update, writable := bound.identity, bound.initial, bound.update, bound.writable
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
		!shaPattern.MatchString(c.BodySHA256) {
		return zero, fmt.Errorf(
			"task tuple: %w", ErrInvalidCommand)
	}
	if !ed25519.Verify(
		ed25519.PublicKey(a.PublicKey),
		frame(serviceBackendTaskSignaturePurpose, raw), signature) {
		return zero, fmt.Errorf(
			"task signature: %w", ErrInvalidCommand)
	}
	// The grant is checked for its shape and for equality with the command's
	// tuple, its resources and a 64-character MAC field. The runner does not
	// hold the identity key, so it does not verify the MAC itself: a grant whose
	// MAC is wrong is rejected by the app and by Portal's callback checks, not
	// here. The runner's own authority is the controller-signed command.
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
		return zero, fmt.Errorf(
			"service identity: %w", ErrInvalidCommand)
	}
	return VerifiedServiceBackendTask{
		command: c, registration: registration,
		initialRegistration: initial, updateRegistration: update,
		writableRegistration: writable,
	}, nil
}

// serviceRegistration is the verified registration a service command is bound
// to: exactly one of initial, update and writable is set.
type serviceRegistration struct {
	identity ServiceBackendTaskRegistrationIdentity
	initial  *VerifiedBackendRegistrationCommand
	update   *VerifiedUpdateStartRegistrationCommand
	writable *VerifiedWritableBackendRegistrationCommand
}

// verifyServiceRegistration verifies the registration a service command
// carries as its proof of the installation's registered generation, and derives
// the tuple the command must repeat. policyRevision is the command's, because
// no registration binds it. Service tasks and service egress sessions share it.
func verifyServiceRegistration(registrationRaw, registrationSignature []byte, policyRevision string, a Authority, nowMS int64) (serviceRegistration, error) {
	var format struct {
		Format string `json:"format"`
	}
	if json.Unmarshal(registrationRaw, &format) != nil {
		return serviceRegistration{}, fmt.Errorf("registration format: %w", ErrInvalidCommand)
	}
	var registration ServiceBackendTaskRegistrationIdentity
	var initial *VerifiedBackendRegistrationCommand
	var update *VerifiedUpdateStartRegistrationCommand
	var writable *VerifiedWritableBackendRegistrationCommand
	switch format.Format {
	case BackendRegistrationFormat:
		verified, err := VerifyBackendRegistration(
			registrationRaw, registrationSignature, a, nowMS)
		if err != nil {
			return serviceRegistration{}, fmt.Errorf(
				"initial registration: %w", ErrInvalidCommand)
		}
		candidate := verified.Candidate()
		// The initial registration binds no policy revision. The revision can
		// rise without a new generation (an access change raises it, and the
		// app runs tasks again once its grants are reviewed again, which is a
		// Portal-side requirement), so it is taken from the signed command and
		// must be a canonical number at least 1. The grant's own revision is
		// compared below.
		if !policyRevisionAtLeast(policyRevision, "1") {
			return serviceRegistration{}, fmt.Errorf(
				"initial registration tuple: %w", ErrInvalidCommand)
		}
		initial = &verified
		registration = ServiceBackendTaskRegistrationIdentity{
			Format:         BackendRegistrationFormat,
			InstallationID: candidate.InstallationID,
			Generation:     candidate.TargetGeneration, PolicyRevision: policyRevision,
			ControllerEpoch: candidate.ControllerEpoch,
			ReleaseSHA256:   candidate.Payload.ReleaseSHA256,
			ExpiresMS:       candidate.ExpiresMS,
		}
	case UpdateStartRegistrationFormat:
		verified, err := VerifyUpdateStartRegistration(
			registrationRaw, registrationSignature, a, nowMS)
		if err != nil {
			return serviceRegistration{}, fmt.Errorf(
				"update registration: %w", ErrInvalidCommand)
		}
		start := verified.Start().Command()
		// The update was started at TargetPolicyRevision. The revision can rise
		// later without a new generation, so the command may carry any
		// canonical revision at or above that floor, never below. This is the
		// runner half of a larger change: the Portal's readers still pin the
		// target revision (doc 170, follow-up).
		if !policyRevisionAtLeast(
			policyRevision, start.Payload.TargetPolicyRevision) {
			return serviceRegistration{}, fmt.Errorf(
				"update registration tuple: %w", ErrInvalidCommand)
		}
		update = &verified
		registration = ServiceBackendTaskRegistrationIdentity{
			Format:          UpdateStartRegistrationFormat,
			InstallationID:  start.InstallationID,
			Generation:      start.TargetGeneration,
			PolicyRevision:  policyRevision,
			ControllerEpoch: start.ControllerEpoch,
			ReleaseSHA256:   start.Payload.TargetReleaseSHA256,
			ExpiresMS:       start.ExpiresMS,
		}
	case WritableBackendRegistrationFormat:
		verified, err := VerifyWritableBackendRegistration(
			registrationRaw, registrationSignature, a, nowMS)
		if err != nil {
			return serviceRegistration{}, fmt.Errorf(
				"writable registration: %w", ErrInvalidCommand)
		}
		identity, err := writableServiceTaskIdentity(
			verified.Candidate().Index(), policyRevision)
		if err != nil {
			return serviceRegistration{}, err
		}
		writable = &verified
		registration = identity
	default:
		return serviceRegistration{}, fmt.Errorf(
			"registration kind: %w", ErrInvalidCommand)
	}
	return serviceRegistration{identity: registration, initial: initial, update: update, writable: writable}, nil
}

// policyRevisionAtLeast reports whether v is a canonical decimal revision (no sign,
// no leading zero) that is at least floor. A floor that is not itself a canonical
// revision admits nothing.
func policyRevisionAtLeast(v, floor string) bool {
	n, err := strconv.ParseUint(v, 10, 63)
	if err != nil || strconv.FormatUint(n, 10) != v {
		return false
	}
	least, err := strconv.ParseUint(floor, 10, 63)
	if err != nil || strconv.FormatUint(least, 10) != floor || least < 1 {
		return false
	}
	return n >= least
}

// writableServiceTaskIdentity derives the task identity of a generation-1 writable
// registration. The registration's own generation must be "1" (a writable install is
// registered at generation 1 only), and its policy revision, which it does not bind,
// comes from the command.
func writableServiceTaskIdentity(index CandidateCommand, policy string) (ServiceBackendTaskRegistrationIdentity, error) {
	if index.TargetGeneration != "1" || !policyRevisionAtLeast(policy, "1") {
		return ServiceBackendTaskRegistrationIdentity{}, fmt.Errorf(
			"writable registration tuple: %w", ErrInvalidCommand)
	}
	return ServiceBackendTaskRegistrationIdentity{
		Format:          WritableBackendRegistrationFormat,
		InstallationID:  index.InstallationID,
		Generation:      index.TargetGeneration,
		PolicyRevision:  policy,
		ControllerEpoch: index.ControllerEpoch,
		ReleaseSHA256:   index.Payload.ReleaseSHA256,
		ExpiresMS:       index.ExpiresMS,
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
