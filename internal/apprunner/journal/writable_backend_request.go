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

const writableBackendRequestResultFormat = "portal.runner.writable-backend-request-result/1-dev.1"

// Bounded one-shot reply, not a durable effect or a viewer/activation grant.
type WritableBackendRequestResult struct {
	Format                string                 `json:"format"`
	RequestID             string                 `json:"request_id"`
	RequestSHA256         string                 `json:"request_sha256"`
	DeliveryReceiptSHA256 string                 `json:"delivery_receipt_sha256"`
	NativeID              string                 `json:"native_id"`
	BootID                string                 `json:"boot_id"`
	Response              driver.BackendResponse `json:"response"`
	ObservedMS            string                 `json:"observed_ms"`
}

func ParseWritableBackendRequestResult(raw []byte) (WritableBackendRequestResult, error) {
	var r WritableBackendRequestResult
	if len(raw) == 0 || len(raw) > MaxBackendRequestResultBytes || !canonicalDataJSON(string(raw), &r) || r.Format != writableBackendRequestResultFormat || !enrollmentUUID.MatchString(r.RequestID) || r.RequestID == "00000000-0000-0000-0000-000000000000" || !dataSHA(r.RequestSHA256) || !dataSHA(r.DeliveryReceiptSHA256) || !dataSHA(r.NativeID) || len(r.BootID) == 0 || len(r.BootID) > 128 || !utf8.ValidString(r.BootID) || !appplatform.ValidBackendResponse(r.Response.Status, r.Response.Body) || !dataNumber(r.ObservedMS, false) {
		return WritableBackendRequestResult{}, ErrFenceReconciliation
	}
	return r, nil
}

// Portal must perform current viewer/OU/grant checks before issuing this distinct
// authorization and before releasing its reply. No browser route is added here.
// Any error after native IO can follow an app write; this never retries.
func (s *Store) ForwardWritableBackendRequest(ctx context.Context, raw, signature, descriptor, body []byte, assertion string, native *driver.CandidateRuntime) (WritableBackendRequestResult, error) {
	var zero WritableBackendRequestResult
	if native == nil || s.closing.Load() || len(raw) == 0 || len(raw) > control.MaxCommandBytes || len(signature) != 64 || len(descriptor) == 0 || len(descriptor) > appplatform.MaxDescriptorBytes || len(body) > appplatform.MaxBackendRequestBody || len(assertion) > appplatform.MaxBackendAssertion {
		return zero, ErrFenceReconciliation
	}
	raw, signature, descriptor, body = bytes.Clone(raw), bytes.Clone(signature), bytes.Clone(descriptor), bytes.Clone(body)
	defer clear(body)
	if err := s.acquireDataExecution(ctx); err != nil {
		return zero, err
	}
	defer s.releaseDataExecution()
	if s.closing.Load() {
		return zero, ErrFenceReconciliation
	}
	d, err := s.ControllerDeployment(ctx)
	if err != nil {
		return zero, err
	}
	a := control.Authority{DeploymentID: d.DeploymentID, ControllerEpoch: d.ControllerEpoch, ControlRevision: d.ControlRevision, PublicKey: d.PublicKey, Fingerprint: d.Fingerprint}
	v, err := control.VerifyWritableBackendRequest(raw, signature, a, time.Now().UnixMilli())
	if err != nil {
		return zero, err
	}
	if err := control.ValidateWritableBackendRequestData(v, body, assertion); err != nil {
		return zero, err
	}
	c := v.Command()
	expires, _ := strconv.ParseInt(v.Registration().Candidate().Index().ExpiresMS, 10, 64)
	ctx, cancel := context.WithDeadline(ctx, time.UnixMilli(expires))
	defer cancel()
	before, err := s.prepareWritableBackendExchange(ctx, c.RegistrationRaw, c.RegistrationSignature, descriptor)
	if err != nil {
		return zero, err
	}
	if _, err := control.VerifyWritableBackendRequest(raw, signature, before.facts.source.authority, time.Now().UnixMilli()); err != nil {
		return zero, err
	}
	if s.closing.Load() {
		return zero, ErrFenceReconciliation
	}
	observed, err := native.ForwardWritableCandidateRequest(ctx, before.request, before.binding, c.Method, c.Path, body, assertion)
	if err != nil {
		return zero, err
	}
	return s.finishWritableBackendRequest(ctx, before, raw, signature, body, assertion, observed)
}
func (s *Store) finishWritableBackendRequest(ctx context.Context, before writableBackendExchangePreflight, raw, signature, body []byte, assertion string, observed driver.WritableBackendRequestObservation) (WritableBackendRequestResult, error) {
	var zero WritableBackendRequestResult
	tx, f, err := s.beginWritableRegistration(ctx, before.facts.raw, before.facts.signature, "applied")
	if err != nil {
		return zero, err
	}
	defer tx.Rollback()
	if s.closing.Load() || !sameWritableRegistrationSource(before.facts, f) {
		return zero, ErrFenceReconciliation
	}
	v, err := control.VerifyWritableBackendRequest(raw, signature, f.source.authority, time.Now().UnixMilli())
	if err != nil {
		return zero, err
	}
	c := v.Command()
	if !bytes.Equal(c.RegistrationRaw, before.facts.raw) || !bytes.Equal(c.RegistrationSignature, before.facts.signature) || control.ValidateWritableBackendRequestData(v, body, assertion) != nil {
		return zero, ErrFenceReconciliation
	}
	actual, err := observed.Snapshot()
	if err != nil || actual.Registration != before.binding || !writableSnapshotMatches(actual.Registration.Launch, f.source, f.running.Claim) || actual.Method != c.Method || actual.Path != c.Path || actual.BodySHA256 != candidateHash(body) || actual.AssertionSHA256 != candidateHash([]byte(assertion)) || actual.ObservedMS < before.facts.source.now || actual.ObservedMS > f.source.now {
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
	if _, err := control.VerifyWritableBackendRequest(raw, signature, f.source.authority, time.Now().UnixMilli()); err != nil {
		return zero, err
	}
	r := WritableBackendRequestResult{Format: writableBackendRequestResultFormat, RequestID: c.RequestID, RequestSHA256: candidateHash(raw), DeliveryReceiptSHA256: candidateHash([]byte(f.row.receipt)), NativeID: actual.Registration.NativeID, BootID: c.BootID, Response: actual.Response, ObservedMS: strconv.FormatInt(actual.ObservedMS, 10)}
	encoded, _ := json.Marshal(r)
	return ParseWritableBackendRequestResult(encoded)
}
