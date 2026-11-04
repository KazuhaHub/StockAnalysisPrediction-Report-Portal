package journal

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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
	command := verified.Command()
	reg := verified.Registration().Command()
	candidate := verified.Registration().Candidate()
	parsed, err := appplatform.ParseDescriptor(descriptor)
	if err != nil || parsed.SHA256 != candidate.Payload.ReleaseSHA256 ||
		parsed.Manifest.Backend == nil ||
		parsed.Manifest.ID != serviceTaskAppID(command.ServiceIdentity) {
		return zero, ErrFenceReconciliation
	}
	expires, _ := strconv.ParseInt(verified.Registration().Candidate().ExpiresMS, 10, 64)
	bounded, cancel := context.WithDeadline(ctx, time.UnixMilli(expires))
	defer cancel()
	ctx = bounded
	before, err := s.ReadBackendRegistrationReceipt(ctx, command.RegistrationRaw, command.RegistrationSignature)
	if err != nil {
		return zero, err
	}
	tx, facts, err := s.beginBackendRegistration(ctx, command.RegistrationRaw, command.RegistrationSignature, "applied")
	if err != nil {
		return zero, err
	}
	stored, _ := json.Marshal(before)
	if facts.receipt != string(stored) || !before.MatchesCommand(reg) {
		tx.Rollback()
		return zero, ErrFenceReconciliation
	}
	if _, err := control.VerifyServiceBackendTask(raw, signature, facts.source.authority, time.Now().UnixMilli()); err != nil {
		tx.Rollback()
		return zero, err
	}
	if err := tx.Commit(); err != nil {
		return zero, err
	}
	request, err := candidateNativeRequest(facts.verified.Candidate(), facts.source.data, descriptor)
	if err != nil {
		return zero, err
	}
	launch, err := native.PrepareCandidate(ctx, request)
	if err != nil {
		return zero, err
	}
	defer launch.Close()
	snapshot, err := launch.Snapshot()
	if err != nil || candidatePreflight(snapshot) != *facts.running.Claim.Preflight {
		return zero, ErrFenceReconciliation
	}
	if _, err := s.ReadBackendRegistrationReceipt(ctx, command.RegistrationRaw, command.RegistrationSignature); err != nil {
		return zero, err
	}
	if _, err := control.VerifyServiceBackendTask(raw, signature, facts.source.authority, time.Now().UnixMilli()); err != nil {
		return zero, err
	}
	device, _ := strconv.ParseUint(before.Device, 10, 64)
	inode, _ := strconv.ParseUint(before.Inode, 10, 64)
	binding := driver.BackendRegistrationSnapshot{
		Launch: snapshot, NativeID: before.Claim.NativeID,
		BindingSHA256:  before.BindingSHA256,
		DocumentSHA256: before.Claim.DocumentSHA256,
		Device:         device, Inode: inode,
	}
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

func serviceTaskAppID(raw []byte) string {
	var grant appplatform.ServiceIdentityGrant
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&grant) != nil {
		return ""
	}
	return grant.AppID
}
