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

const MaxBackendRequestResultBytes = 1500000

// BackendRequestResult is a bounded application reply bound to the exact
// independently signed control request. It is not an access or replay grant.
type BackendRequestResult struct {
	Format                string                 `json:"format"`
	RequestID             string                 `json:"request_id"`
	RequestSHA256         string                 `json:"request_sha256"`
	DeliveryReceiptSHA256 string                 `json:"delivery_receipt_sha256"`
	NativeID              string                 `json:"native_id"`
	BootID                string                 `json:"boot_id"`
	Response              driver.BackendResponse `json:"response"`
	ObservedMS            string                 `json:"observed_ms"`
}

func ParseBackendRequestResult(raw []byte) (BackendRequestResult, error) {
	var r BackendRequestResult
	if len(raw) == 0 || len(raw) > MaxBackendRequestResultBytes || !canonicalDataJSON(string(raw), &r) || r.Format != "portal.runner.backend-request-result/1-dev.1" || !enrollmentUUID.MatchString(r.RequestID) || r.RequestID == "00000000-0000-0000-0000-000000000000" || !dataSHA(r.RequestSHA256) || !dataSHA(r.DeliveryReceiptSHA256) || !dataSHA(r.NativeID) || len(r.BootID) == 0 || len(r.BootID) > 128 || !utf8.ValidString(r.BootID) || !appplatform.ValidBackendResponse(r.Response.Status, r.Response.Body) || !dataNumber(r.ObservedMS, false) {
		return BackendRequestResult{}, ErrFenceReconciliation
	}
	return r, nil
}

// ForwardBackendRequest is not connected to a browser route. Current Portal
// user/OU/grant checks are required before creating the separate per-request
// authorization. Errors may follow app writes; this method never auto-retries.
func (s *Store) ForwardBackendRequest(ctx context.Context, raw, signature, descriptor, body []byte, assertion string, native *driver.CandidateRuntime) (BackendRequestResult, error) {
	var zero BackendRequestResult
	if native == nil || len(raw) == 0 || len(raw) > control.MaxCommandBytes || len(signature) != 64 || len(descriptor) == 0 || len(descriptor) > appplatform.MaxDescriptorBytes || len(body) > appplatform.MaxBackendRequestBody || len(assertion) > appplatform.MaxBackendAssertion {
		return zero, ErrFenceReconciliation
	}
	raw, signature, descriptor, body = bytes.Clone(raw), bytes.Clone(signature), bytes.Clone(descriptor), bytes.Clone(body)
	defer clear(body)
	enrollment, err := s.ControllerDeployment(ctx)
	if err != nil {
		return zero, err
	}
	a := control.Authority{DeploymentID: enrollment.DeploymentID, ControllerEpoch: enrollment.ControllerEpoch, ControlRevision: enrollment.ControlRevision, PublicKey: enrollment.PublicKey, Fingerprint: enrollment.Fingerprint}
	v, err := control.VerifyBackendRequest(raw, signature, a, time.Now().UnixMilli())
	if err != nil {
		return zero, err
	}
	if err := control.ValidateBackendRequestData(v, body, assertion); err != nil {
		return zero, err
	}
	c := v.Command()
	reg := v.Registration().Command()
	expires, _ := strconv.ParseInt(v.Registration().Candidate().ExpiresMS, 10, 64)
	bounded, cancel := context.WithDeadline(ctx, time.UnixMilli(expires))
	defer cancel()
	ctx = bounded
	before, err := s.ReadBackendRegistrationReceipt(ctx, c.RegistrationRaw, c.RegistrationSignature)
	if err != nil {
		return zero, err
	}
	tx, f, err := s.beginBackendRegistration(ctx, c.RegistrationRaw, c.RegistrationSignature, "applied")
	if err != nil {
		return zero, err
	}
	stored, _ := json.Marshal(before)
	if f.receipt != string(stored) || !before.MatchesCommand(reg) {
		tx.Rollback()
		return zero, ErrFenceReconciliation
	}
	if _, err := control.VerifyBackendRequest(raw, signature, f.source.authority, time.Now().UnixMilli()); err != nil {
		tx.Rollback()
		return zero, err
	}
	if err := tx.Commit(); err != nil {
		return zero, err
	}
	request, err := candidateNativeRequest(f.verified.Candidate(), f.source.data, descriptor)
	if err != nil {
		return zero, err
	}
	launch, err := native.PrepareCandidate(ctx, request)
	if err != nil {
		return zero, err
	}
	defer launch.Close()
	snapshot, err := launch.Snapshot()
	if err != nil || candidatePreflight(snapshot) != *f.running.Claim.Preflight {
		return zero, ErrFenceReconciliation
	}
	// A long native preflight must not consume authority and then dispatch late.
	if _, err := s.ReadBackendRegistrationReceipt(ctx, c.RegistrationRaw, c.RegistrationSignature); err != nil {
		return zero, err
	}
	if _, err := control.VerifyBackendRequest(raw, signature, f.source.authority, time.Now().UnixMilli()); err != nil {
		return zero, err
	}
	device, _ := strconv.ParseUint(before.Device, 10, 64)
	inode, _ := strconv.ParseUint(before.Inode, 10, 64)
	binding := driver.BackendRegistrationSnapshot{Launch: snapshot, NativeID: before.Claim.NativeID, BindingSHA256: before.BindingSHA256, DocumentSHA256: before.Claim.DocumentSHA256, Device: device, Inode: inode}
	observation, err := native.ForwardCandidateRequest(ctx, launch, binding, c.Method, c.Path, body, assertion)
	if err != nil {
		return zero, err
	}
	physical, err := observation.Snapshot()
	bodySHA, assertionSHA := sha256.Sum256(body), sha256.Sum256([]byte(assertion))
	if err != nil || physical.Binding != binding || physical.Method != c.Method || physical.Path != c.Path || physical.BodySHA256 != hex.EncodeToString(bodySHA[:]) || physical.AssertionSHA256 != hex.EncodeToString(assertionSHA[:]) || physical.ObservedMS < f.source.now {
		return zero, ErrFenceReconciliation
	}
	after, err := s.ReadBackendRegistrationReceipt(ctx, c.RegistrationRaw, c.RegistrationSignature)
	if err != nil || after != before {
		return zero, ErrFenceReconciliation
	}
	if _, err := control.VerifyBackendRequest(raw, signature, f.source.authority, time.Now().UnixMilli()); err != nil {
		return zero, err
	}
	result := BackendRequestResult{Format: "portal.runner.backend-request-result/1-dev.1", RequestID: c.RequestID, RequestSHA256: candidateHash(raw), DeliveryReceiptSHA256: candidateHash(stored), NativeID: physical.Binding.NativeID, BootID: c.BootID, Response: physical.Response, ObservedMS: strconv.FormatInt(physical.ObservedMS, 10)}
	encoded, _ := json.Marshal(result)
	return ParseBackendRequestResult(encoded)
}
