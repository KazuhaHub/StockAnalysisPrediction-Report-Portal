package journal

import (
	"bytes"
	"context"
	"encoding/json"
	"strconv"
	"time"
	"unicode/utf8"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/driver"
)

const updateStartBackendRequestResultFormat = "portal.runner.update-start-backend-request-result/1-dev.1"

type UpdateStartBackendRequestResult struct {
	Format                string                 `json:"format"`
	RequestID             string                 `json:"request_id"`
	RequestSHA256         string                 `json:"request_sha256"`
	DeliveryReceiptSHA256 string                 `json:"delivery_receipt_sha256"`
	NativeID              string                 `json:"native_id"`
	BootID                string                 `json:"boot_id"`
	Response              driver.BackendResponse `json:"response"`
	ObservedMS            string                 `json:"observed_ms"`
}

func ParseUpdateStartBackendRequestResult(raw []byte) (UpdateStartBackendRequestResult, error) {
	var result UpdateStartBackendRequestResult
	if len(raw) == 0 || len(raw) > MaxBackendRequestResultBytes ||
		!canonicalDataJSON(string(raw), &result) ||
		result.Format != updateStartBackendRequestResultFormat ||
		!enrollmentUUID.MatchString(result.RequestID) ||
		result.RequestID == "00000000-0000-0000-0000-000000000000" ||
		!dataSHA(result.RequestSHA256) ||
		!dataSHA(result.DeliveryReceiptSHA256) ||
		!dataSHA(result.NativeID) || len(result.BootID) == 0 ||
		len(result.BootID) > 128 || !utf8.ValidString(result.BootID) ||
		!appplatform.ValidBackendResponse(
			result.Response.Status, result.Response.Body) ||
		!dataNumber(result.ObservedMS, false) {
		return UpdateStartBackendRequestResult{}, ErrFenceReconciliation
	}
	return result, nil
}

func (s *Store) ForwardUpdateStartBackendRequest(ctx context.Context, raw, signature, descriptor, body []byte, assertion string, native *driver.CandidateRuntime) (UpdateStartBackendRequestResult, error) {
	var zero UpdateStartBackendRequestResult
	if native == nil || s.closing.Load() || len(raw) == 0 ||
		len(raw) > control.MaxCommandBytes || len(signature) != 64 ||
		len(descriptor) == 0 ||
		len(descriptor) > appplatform.MaxDescriptorBytes ||
		len(body) > appplatform.MaxBackendRequestBody ||
		len(assertion) > appplatform.MaxBackendAssertion {
		return zero, ErrFenceReconciliation
	}
	raw, signature, descriptor, body =
		bytes.Clone(raw), bytes.Clone(signature),
		bytes.Clone(descriptor), bytes.Clone(body)
	defer clear(body)
	if err := s.acquireDataExecution(ctx); err != nil {
		return zero, err
	}
	defer s.releaseDataExecution()
	if s.closing.Load() {
		return zero, ErrFenceReconciliation
	}
	deployment, err := s.ControllerDeployment(ctx)
	if err != nil {
		return zero, err
	}
	authority := control.Authority{
		DeploymentID:    deployment.DeploymentID,
		ControllerEpoch: deployment.ControllerEpoch,
		ControlRevision: deployment.ControlRevision,
		PublicKey:       deployment.PublicKey, Fingerprint: deployment.Fingerprint,
	}
	verified, err := control.VerifyUpdateStartBackendRequest(
		raw, signature, authority, time.Now().UnixMilli())
	if err != nil {
		return zero, err
	}
	if err := control.ValidateUpdateStartBackendRequestData(
		verified, body, assertion); err != nil {
		return zero, err
	}
	command := verified.Command()
	expires, _ := strconv.ParseInt(
		verified.Registration().Start().Command().ExpiresMS, 10, 64)
	ctx, cancel := context.WithDeadline(ctx, time.UnixMilli(expires))
	defer cancel()
	parsed, err := appplatform.ParseDescriptor(descriptor)
	if err != nil || parsed.Manifest.Backend == nil {
		return zero, ErrFenceReconciliation
	}
	before, err := s.prepareUpdateStartBackendExchange(
		ctx, command.RegistrationRaw, command.RegistrationSignature)
	if err != nil {
		return zero, err
	}
	if parsed.SHA256 !=
		before.facts.verified.Start().Command().
			Payload.TargetReleaseSHA256 {
		return zero, ErrFenceReconciliation
	}
	request, expected, err := updateStartExpectedSnapshot(
		before.facts.source.start,
		before.facts.source.running.Claim, descriptor)
	if err != nil {
		return zero, err
	}
	before.request = request
	before.binding.Launch = expected
	if _, err := control.VerifyUpdateStartBackendRequest(
		raw, signature, before.facts.source.start.authority,
		time.Now().UnixMilli()); err != nil {
		return zero, err
	}
	if s.closing.Load() {
		return zero, ErrFenceReconciliation
	}
	observed, err := native.ForwardUpdateStartCandidateRequest(
		ctx, before.request, before.binding.Launch, before.binding,
		descriptor,
		command.Method, command.Path, body, assertion)
	if err != nil {
		return zero, err
	}
	return s.finishUpdateStartBackendRequest(
		ctx, before, raw, signature, body, assertion, observed)
}

func (s *Store) finishUpdateStartBackendRequest(ctx context.Context, before updateStartBackendExchangePreflight, raw, signature, body []byte, assertion string, observed driver.UpdateStartBackendRequestObservation) (UpdateStartBackendRequestResult, error) {
	var zero UpdateStartBackendRequestResult
	tx, facts, err := beginUpdateStartRegistration(
		ctx, s, before.facts.raw, before.facts.signature, "applied")
	if err != nil {
		return zero, err
	}
	defer tx.Rollback()
	if s.closing.Load() ||
		!sameUpdateStartRegistrationSource(before.facts, facts) {
		return zero, ErrFenceReconciliation
	}
	verified, err := control.VerifyUpdateStartBackendRequest(
		raw, signature, facts.source.start.authority,
		time.Now().UnixMilli())
	if err != nil {
		return zero, err
	}
	command := verified.Command()
	if !bytes.Equal(command.RegistrationRaw, before.facts.raw) ||
		!bytes.Equal(command.RegistrationSignature, before.facts.signature) ||
		control.ValidateUpdateStartBackendRequestData(
			verified, body, assertion) != nil {
		return zero, ErrFenceReconciliation
	}
	actual, err := observed.Snapshot()
	if err != nil || actual.Registration != before.binding ||
		actual.Method != command.Method || actual.Path != command.Path ||
		actual.BodySHA256 != candidateHash(body) ||
		actual.AssertionSHA256 != candidateHash([]byte(assertion)) ||
		actual.ObservedMS < before.facts.source.start.now ||
		actual.ObservedMS > facts.source.start.now {
		return zero, ErrFenceReconciliation
	}
	if err := checkUpdateStartRegistrationCurrent(ctx, facts); err != nil {
		return zero, err
	}
	if err := tx.Commit(); err != nil {
		return zero, err
	}
	if s.closing.Load() {
		return zero, ErrFenceReconciliation
	}
	if err := checkUpdateStartRegistrationCurrent(ctx, facts); err != nil {
		return zero, err
	}
	if _, err := control.VerifyUpdateStartBackendRequest(
		raw, signature, facts.source.start.authority,
		time.Now().UnixMilli()); err != nil {
		return zero, err
	}
	result := UpdateStartBackendRequestResult{
		Format:    updateStartBackendRequestResultFormat,
		RequestID: command.RequestID, RequestSHA256: candidateHash(raw),
		DeliveryReceiptSHA256: candidateHash([]byte(facts.row.receipt)),
		NativeID:              actual.Registration.NativeID,
		BootID:                command.BootID,
		Response:              actual.Response,
		ObservedMS:            strconv.FormatInt(actual.ObservedMS, 10),
	}
	encoded, _ := json.Marshal(result)
	return ParseUpdateStartBackendRequestResult(encoded)
}
