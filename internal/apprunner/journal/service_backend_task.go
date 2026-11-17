package journal

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"time"
	"unicode/utf8"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/driver"
)

const ServiceBackendTaskResultFormat = "portal.runner.service-backend-task-result/1-dev.1"

type ServiceBackendTaskResult struct {
	Format                string                 `json:"format"`
	RequestID             string                 `json:"request_id"`
	TaskID                string                 `json:"task_id"`
	LeaseID               string                 `json:"lease_id"`
	RequestSHA256         string                 `json:"request_sha256"`
	DeliveryReceiptSHA256 string                 `json:"delivery_receipt_sha256"`
	NativeID              string                 `json:"native_id"`
	BootID                string                 `json:"boot_id"`
	Response              driver.BackendResponse `json:"response"`
	ObservedMS            string                 `json:"observed_ms"`
}

func ParseServiceBackendTaskResult(raw []byte) (ServiceBackendTaskResult, error) {
	var result ServiceBackendTaskResult
	if len(raw) == 0 || len(raw) > MaxBackendRequestResultBytes ||
		!canonicalDataJSON(string(raw), &result) ||
		result.Format != ServiceBackendTaskResultFormat ||
		!enrollmentUUID.MatchString(result.RequestID) ||
		!enrollmentUUID.MatchString(result.TaskID) ||
		!enrollmentUUID.MatchString(result.LeaseID) ||
		!dataSHA(result.RequestSHA256) ||
		!dataSHA(result.DeliveryReceiptSHA256) ||
		!dataSHA(result.NativeID) ||
		len(result.BootID) < 1 || len(result.BootID) > 128 ||
		!utf8.ValidString(result.BootID) ||
		!appplatform.ValidBackendResponse(result.Response.Status, result.Response.Body) ||
		!dataNumber(result.ObservedMS, false) {
		return ServiceBackendTaskResult{}, ErrFenceReconciliation
	}
	return result, nil
}

func (s *Store) ForwardServiceBackendTask(
	ctx context.Context,
	raw, signature, descriptor, body []byte,
	native *driver.CandidateRuntime,
) (ServiceBackendTaskResult, error) {
	var zero ServiceBackendTaskResult
	if native == nil || len(raw) == 0 || len(raw) > control.MaxCommandBytes ||
		len(signature) != 64 || len(descriptor) == 0 ||
		len(descriptor) > appplatform.MaxDescriptorBytes ||
		len(body) == 0 || len(body) > appplatform.MaxTaskBackendBody {
		return zero, ErrFenceReconciliation
	}
	raw, signature, descriptor, body = bytes.Clone(raw), bytes.Clone(signature), bytes.Clone(descriptor), bytes.Clone(body)
	defer clear(body)
	enrollment, err := s.ControllerDeployment(ctx)
	if err != nil {
		return zero, err
	}
	authority := control.Authority{
		DeploymentID: enrollment.DeploymentID, ControllerEpoch: enrollment.ControllerEpoch,
		ControlRevision: enrollment.ControlRevision, PublicKey: enrollment.PublicKey,
		Fingerprint: enrollment.Fingerprint,
	}
	verified, err := control.VerifyServiceBackendTask(raw, signature, authority, time.Now().UnixMilli())
	if err != nil {
		return zero, ErrFenceReconciliation
	}
	if err := control.ValidateServiceBackendTaskData(verified, body); err != nil {
		return zero, ErrFenceReconciliation
	}
	identity := verified.RegistrationIdentity()
	if identity.Format == control.UpdateStartRegistrationFormat {
		return s.forwardUpdateStartServiceBackendTask(
			ctx, verified, raw, signature, descriptor, body, native)
	}
	if identity.Format == control.WritableBackendRegistrationFormat {
		return s.forwardWritableServiceBackendTask(
			ctx, verified, raw, signature, descriptor, body, native)
	}
	initial, ok := verified.InitialRegistration()
	if !ok || identity.Format != control.BackendRegistrationFormat {
		return zero, ErrFenceReconciliation
	}
	command := verified.Command()
	prepared, err := s.prepareServiceBindingInitial(
		ctx, initial, identity.ExpiresMS,
		command.RegistrationRaw, command.RegistrationSignature,
		descriptor, serviceTaskAppID(command.ServiceIdentity), native,
		func(a control.Authority) error {
			_, err := control.VerifyServiceBackendTask(
				raw, signature, a, time.Now().UnixMilli())
			return err
		})
	if err != nil {
		return zero, err
	}
	defer prepared.close()
	ctx = prepared.ctx
	facts, before, stored, launch, binding :=
		prepared.facts, prepared.before, prepared.stored, prepared.launch, prepared.binding
	observation, err := native.ForwardServiceTask(
		ctx, launch, binding, body, command.ServiceIdentity, command.Handler)
	if err != nil {
		return zero, err
	}
	physical, err := observation.Snapshot()
	bodySHA := sha256.Sum256(body)
	identitySHA := sha256.Sum256(command.ServiceIdentity)
	if err != nil || physical.Binding != binding ||
		physical.BodySHA256 != hex.EncodeToString(bodySHA[:]) ||
		physical.ServiceIdentitySHA256 != hex.EncodeToString(identitySHA[:]) ||
		physical.ObservedMS < facts.source.now {
		return zero, ErrFenceReconciliation
	}
	after, err := s.ReadBackendRegistrationReceipt(ctx, command.RegistrationRaw, command.RegistrationSignature)
	if err != nil || after != before {
		return zero, ErrFenceReconciliation
	}
	if _, err := control.VerifyServiceBackendTask(raw, signature, facts.source.authority, time.Now().UnixMilli()); err != nil {
		return zero, err
	}
	result := ServiceBackendTaskResult{
		Format:    ServiceBackendTaskResultFormat,
		RequestID: command.RequestID, TaskID: command.TaskID,
		LeaseID:               command.LeaseID,
		RequestSHA256:         candidateHash(raw),
		DeliveryReceiptSHA256: candidateHash(stored),
		NativeID:              physical.Binding.NativeID, BootID: command.BootID,
		Response:   physical.Response,
		ObservedMS: strconv.FormatInt(physical.ObservedMS, 10),
	}
	encoded, _ := json.Marshal(result)
	return ParseServiceBackendTaskResult(encoded)
}

// candidatePreparer is the one native call the preparation of an initial
// registration makes; *driver.CandidateRuntime implements it.
type candidatePreparer interface {
	PrepareCandidate(context.Context, driver.CandidateLaunchRequest) (driver.CandidateLaunch, error)
}

// initialServiceBinding is an initial-registration installation, prepared for a
// native exchange: its registration row was read and rechecked against the
// journal, and its launch snapshot matches the one the container was started
// with. It is shared by service tasks and service egress sessions, which differ
// only in what they then ask the native runtime to do. close releases the launch
// and the registration deadline.
type initialServiceBinding struct {
	ctx    context.Context
	cancel context.CancelFunc
	launch driver.CandidateLaunch
	binding driver.BackendRegistrationSnapshot
	facts  backendRegistrationFacts
	before BackendRegistrationReceipt
	stored []byte
}

func (b *initialServiceBinding) close() {
	b.launch.Close()
	b.cancel()
}

// prepareServiceBindingInitial verifies an initial registration's journal state
// and prepares its launch. verifyCommand re-verifies the caller's own signed
// command against the journal's current authority, in the transaction that reads
// the registration and again after the launch is prepared. appID, when not
// empty, must be the application of the descriptor.
func (s *Store) prepareServiceBindingInitial(
	ctx context.Context,
	initial control.VerifiedBackendRegistrationCommand,
	expiresMS string,
	registrationRaw, registrationSignature, descriptor []byte,
	appID string,
	native candidatePreparer,
	verifyCommand func(control.Authority) error,
) (*initialServiceBinding, error) {
	reg := initial.Command()
	candidate := initial.Candidate()
	parsed, err := appplatform.ParseDescriptor(descriptor)
	if err != nil || parsed.SHA256 != candidate.Payload.ReleaseSHA256 ||
		parsed.Manifest.Backend == nil ||
		(appID != "" && parsed.Manifest.ID != appID) {
		return nil, ErrFenceReconciliation
	}
	expires, _ := strconv.ParseInt(expiresMS, 10, 64)
	bounded, cancel := context.WithDeadline(ctx, time.UnixMilli(expires))
	ok := false
	defer func() {
		if !ok {
			cancel()
		}
	}()
	ctx = bounded
	before, err := s.ReadBackendRegistrationReceipt(ctx, registrationRaw, registrationSignature)
	if err != nil {
		return nil, err
	}
	tx, facts, err := s.beginBackendRegistration(ctx, registrationRaw, registrationSignature, "applied")
	if err != nil {
		return nil, err
	}
	stored, _ := json.Marshal(before)
	if facts.receipt != string(stored) || !before.MatchesCommand(reg) {
		tx.Rollback()
		return nil, ErrFenceReconciliation
	}
	if err := verifyCommand(facts.source.authority); err != nil {
		tx.Rollback()
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	request, err := candidateNativeRequest(facts.verified.Candidate(), facts.source.data, descriptor)
	if err != nil {
		return nil, err
	}
	launch, err := native.PrepareCandidate(ctx, request)
	if err != nil {
		return nil, err
	}
	snapshot, err := launch.Snapshot()
	if err != nil || candidatePreflight(snapshot) != *facts.running.Claim.Preflight {
		launch.Close()
		return nil, ErrFenceReconciliation
	}
	if _, err := s.ReadBackendRegistrationReceipt(ctx, registrationRaw, registrationSignature); err != nil {
		launch.Close()
		return nil, err
	}
	if err := verifyCommand(facts.source.authority); err != nil {
		launch.Close()
		return nil, err
	}
	device, _ := strconv.ParseUint(before.Device, 10, 64)
	inode, _ := strconv.ParseUint(before.Inode, 10, 64)
	ok = true
	return &initialServiceBinding{
		ctx: ctx, cancel: cancel, launch: launch, facts: facts, before: before, stored: stored,
		binding: driver.BackendRegistrationSnapshot{
			Launch: snapshot, NativeID: before.Claim.NativeID,
			BindingSHA256:  before.BindingSHA256,
			DocumentSHA256: before.Claim.DocumentSHA256,
			Device:         device, Inode: inode,
		},
	}, nil
}

func (s *Store) forwardUpdateStartServiceBackendTask(
	ctx context.Context,
	verified control.VerifiedServiceBackendTask,
	raw, signature, descriptor, body []byte,
	native *driver.CandidateRuntime,
) (ServiceBackendTaskResult, error) {
	var zero ServiceBackendTaskResult
	if native == nil || s.closing.Load() ||
		len(raw) == 0 || len(raw) > control.MaxCommandBytes ||
		len(signature) != 64 || len(descriptor) == 0 ||
		len(descriptor) > appplatform.MaxDescriptorBytes ||
		len(body) == 0 || len(body) > appplatform.MaxTaskBackendBody {
		return zero, ErrFenceReconciliation
	}
	update, ok := verified.UpdateStartRegistration()
	if !ok {
		return zero, ErrFenceReconciliation
	}
	command := verified.Command()
	registrationRaw := bytes.Clone(command.RegistrationRaw)
	registrationSignature := bytes.Clone(command.RegistrationSignature)
	parsed, err := appplatform.ParseDescriptor(descriptor)
	if err != nil || parsed.Manifest.Backend == nil ||
		parsed.SHA256 != update.Start().Command().Payload.TargetReleaseSHA256 ||
		parsed.Manifest.ID != serviceTaskAppID(command.ServiceIdentity) {
		return zero, ErrFenceReconciliation
	}
	expires, err := strconv.ParseInt(
		verified.RegistrationIdentity().ExpiresMS, 10, 64)
	if err != nil || expires <= time.Now().UnixMilli() {
		return zero, ErrFenceReconciliation
	}
	ctx, cancel := context.WithDeadline(ctx, time.UnixMilli(expires))
	defer cancel()
	if err := s.acquireDataExecution(ctx); err != nil {
		return zero, err
	}
	defer s.releaseDataExecution()
	before, err := s.prepareUpdateStartBackendExchange(
		ctx, registrationRaw, registrationSignature)
	if err != nil {
		return zero, fmt.Errorf(
			"update service registration exchange: %w", err)
	}
	request, expected, err := updateStartExpectedSnapshot(
		before.facts.source.start,
		before.facts.source.running.Claim, descriptor)
	if err != nil {
		return zero, fmt.Errorf(
			"update service descriptor snapshot: %w", err)
	}
	before.request = request
	before.binding.Launch = expected
	if _, err := control.VerifyServiceBackendTask(
		raw, signature, before.facts.source.start.authority,
		time.Now().UnixMilli()); err != nil {
		return zero, err
	}
	observation, err := native.ForwardUpdateStartServiceTask(
		ctx, before.request, before.binding.Launch, before.binding, body,
		command.ServiceIdentity, command.Handler)
	if err != nil {
		return zero, fmt.Errorf(
			"update service native exchange: %w", err)
	}
	physical, err := observation.Snapshot()
	bodySHA := sha256.Sum256(body)
	identitySHA := sha256.Sum256(command.ServiceIdentity)
	expectedBinding := driver.BackendRegistrationSnapshot{
		Launch:         before.binding.Launch.Launch,
		NativeID:       before.binding.NativeID,
		BindingSHA256:  before.binding.BindingSHA256,
		DocumentSHA256: before.binding.DocumentSHA256,
		Device:         before.binding.Device,
		Inode:          before.binding.Inode,
	}
	if err != nil || physical.Binding != expectedBinding ||
		physical.BodySHA256 != hex.EncodeToString(bodySHA[:]) ||
		physical.ServiceIdentitySHA256 != hex.EncodeToString(identitySHA[:]) ||
		physical.ObservedMS < before.facts.source.start.now {
		return zero, ErrFenceReconciliation
	}
	tx, after, err := beginUpdateStartRegistration(
		ctx, s, registrationRaw, registrationSignature, "applied")
	if err != nil {
		return zero, fmt.Errorf(
			"update service registration recheck: %w", err)
	}
	defer tx.Rollback()
	if !sameUpdateStartRegistrationSource(before.facts, after) ||
		checkUpdateStartRegistrationCurrent(ctx, after) != nil {
		return zero, ErrFenceReconciliation
	}
	if err := tx.Commit(); err != nil {
		return zero, fmt.Errorf(
			"update service registration commit: %w", err)
	}
	if _, err := control.VerifyServiceBackendTask(
		raw, signature, after.source.start.authority,
		time.Now().UnixMilli()); err != nil {
		return zero, err
	}
	stored := after.row.receipt
	result := ServiceBackendTaskResult{
		Format:    ServiceBackendTaskResultFormat,
		RequestID: command.RequestID, TaskID: command.TaskID,
		LeaseID:               command.LeaseID,
		RequestSHA256:         candidateHash(raw),
		DeliveryReceiptSHA256: candidateHash([]byte(stored)),
		NativeID:              physical.Binding.NativeID, BootID: command.BootID,
		Response:   physical.Response,
		ObservedMS: strconv.FormatInt(physical.ObservedMS, 10),
	}
	encoded, _ := json.Marshal(result)
	return ParseServiceBackendTaskResult(encoded)
}

func serviceTaskAppID(raw []byte) string {
	var grant appplatform.ServiceIdentityGrant
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&grant) != nil {
		return ""
	}
	return grant.AppID
}

// forwardWritableServiceBackendTask forwards a generation-1 task to a writable
// install. It reads the existing applied writable registration row and writes
// nothing durable. It holds the data execution gate because the target is a
// writer's container. Any error after native IO can follow an app write; the
// caller never retries here.
func (s *Store) forwardWritableServiceBackendTask(
	ctx context.Context,
	verified control.VerifiedServiceBackendTask,
	raw, signature, descriptor, body []byte,
	native *driver.CandidateRuntime,
) (ServiceBackendTaskResult, error) {
	var zero ServiceBackendTaskResult
	if native == nil || s.closing.Load() ||
		len(raw) == 0 || len(raw) > control.MaxCommandBytes ||
		len(signature) != 64 || len(descriptor) == 0 ||
		len(descriptor) > appplatform.MaxDescriptorBytes ||
		len(body) == 0 || len(body) > appplatform.MaxTaskBackendBody {
		return zero, ErrFenceReconciliation
	}
	registration, ok := verified.WritableRegistration()
	if !ok {
		return zero, ErrFenceReconciliation
	}
	command := verified.Command()
	parsed, err := appplatform.ParseDescriptor(descriptor)
	if err != nil || parsed.Manifest.Backend == nil ||
		parsed.SHA256 != registration.Candidate().Index().Payload.ReleaseSHA256 ||
		parsed.Manifest.ID != serviceTaskAppID(command.ServiceIdentity) {
		return zero, ErrFenceReconciliation
	}
	expires, err := strconv.ParseInt(
		verified.RegistrationIdentity().ExpiresMS, 10, 64)
	if err != nil || expires <= time.Now().UnixMilli() {
		return zero, ErrFenceReconciliation
	}
	ctx, cancel := context.WithDeadline(ctx, time.UnixMilli(expires))
	defer cancel()
	if err := s.acquireDataExecution(ctx); err != nil {
		return zero, err
	}
	defer s.releaseDataExecution()
	if s.closing.Load() {
		return zero, ErrFenceReconciliation
	}
	before, err := s.prepareWritableBackendExchange(
		ctx, command.RegistrationRaw, command.RegistrationSignature, descriptor)
	if err != nil {
		return zero, fmt.Errorf("writable service registration exchange: %w", err)
	}
	if _, err := control.VerifyServiceBackendTask(
		raw, signature, before.facts.source.authority,
		time.Now().UnixMilli()); err != nil {
		return zero, err
	}
	if s.closing.Load() {
		return zero, ErrFenceReconciliation
	}
	observed, err := native.ForwardWritableCandidateServiceTask(
		ctx, before.request, before.binding, body,
		command.ServiceIdentity, command.Handler)
	if err != nil {
		return zero, fmt.Errorf("writable service native exchange: %w", err)
	}
	actual, err := observed.Snapshot()
	if err != nil {
		return zero, ErrFenceReconciliation
	}
	return s.finishWritableServiceBackendTask(
		ctx, before, raw, signature, body, actual)
}

func (s *Store) finishWritableServiceBackendTask(
	ctx context.Context,
	before writableBackendExchangePreflight,
	raw, signature, body []byte,
	actual driver.WritableServiceTaskSnapshot,
) (ServiceBackendTaskResult, error) {
	var zero ServiceBackendTaskResult
	tx, f, err := s.beginWritableRegistration(
		ctx, before.facts.raw, before.facts.signature, "applied")
	if err != nil {
		return zero, err
	}
	defer tx.Rollback()
	if s.closing.Load() || !sameWritableRegistrationSource(before.facts, f) {
		return zero, ErrFenceReconciliation
	}
	verified, err := control.VerifyServiceBackendTask(
		raw, signature, f.source.authority, time.Now().UnixMilli())
	if err != nil {
		return zero, err
	}
	command := verified.Command()
	if !bytes.Equal(command.RegistrationRaw, before.facts.raw) ||
		!bytes.Equal(command.RegistrationSignature, before.facts.signature) ||
		control.ValidateServiceBackendTaskData(verified, body) != nil {
		return zero, ErrFenceReconciliation
	}
	identitySHA := candidateHash(command.ServiceIdentity)
	if actual.Registration != before.binding ||
		!writableSnapshotMatches(actual.Registration.Launch, f.source, f.running.Claim) ||
		actual.Handler != command.Handler ||
		actual.BodySHA256 != candidateHash(body) ||
		actual.ServiceIdentitySHA256 != identitySHA ||
		actual.ObservedMS < before.facts.source.now ||
		actual.ObservedMS > f.source.now {
		return zero, ErrFenceReconciliation
	}
	if err := checkWritableRegistrationCurrent(ctx, f); err != nil {
		return zero, err
	}
	if err := tx.Commit(); err != nil {
		return zero, err
	}
	if s.closing.Load() {
		return zero, ErrFenceReconciliation
	}
	if err := checkWritableRegistrationCurrent(ctx, f); err != nil {
		return zero, err
	}
	if _, err := control.VerifyServiceBackendTask(
		raw, signature, f.source.authority, time.Now().UnixMilli()); err != nil {
		return zero, err
	}
	result := ServiceBackendTaskResult{
		Format:    ServiceBackendTaskResultFormat,
		RequestID: command.RequestID, TaskID: command.TaskID,
		LeaseID:               command.LeaseID,
		RequestSHA256:         candidateHash(raw),
		DeliveryReceiptSHA256: candidateHash([]byte(f.row.receipt)),
		NativeID:              actual.Registration.NativeID, BootID: command.BootID,
		Response:   actual.Response,
		ObservedMS: strconv.FormatInt(actual.ObservedMS, 10),
	}
	encoded, _ := json.Marshal(result)
	return ParseServiceBackendTaskResult(encoded)
}
