package journal

import (
	"bytes"
	"context"
	"encoding/json"
	"strconv"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/driver"
)

// BackendHandshakeEvidence is a bounded native exchange report. Its answer still
// requires independent host-side MAC/challenge/boot verification. Parsing this
// JSON never proves execution provenance or authorizes activation.
type BackendHandshakeEvidence struct {
	Format                string `json:"format"`
	PayloadSHA256         string `json:"payload_sha256"`
	DeliveryReceiptSHA256 string `json:"delivery_receipt_sha256"`
	NativeID              string `json:"native_id"`
	ChallengeSHA256       string `json:"challenge_sha256"`
	Answer                string `json:"answer"`
	ObservedMS            string `json:"observed_ms"`
}

func ParseBackendHandshakeEvidence(raw []byte) (BackendHandshakeEvidence, error) {
	var e BackendHandshakeEvidence
	if len(raw) == 0 || len(raw) > 8192 || !canonicalDataJSON(string(raw), &e) || e.Format != "portal.runner.backend-handshake/1-dev.1" || !dataSHA(e.PayloadSHA256) || !dataSHA(e.DeliveryReceiptSHA256) || !dataSHA(e.NativeID) || !dataSHA(e.ChallengeSHA256) || !appplatform.ValidBackendHandshakeToken(e.Answer) || !dataNumber(e.ObservedMS, false) {
		return BackendHandshakeEvidence{}, ErrFenceReconciliation
	}
	return e, nil
}

// ExchangeBackendHandshake requires applied delivery under current authority,
// validates actual process/file/HTTP peer evidence outside SQL, then repeats the
// exact history checks. It writes no delivery/activation receipt or grant.
func (s *Store) ExchangeBackendHandshake(ctx context.Context, raw, signature, descriptor []byte, challenge string, native *driver.CandidateRuntime) (BackendHandshakeEvidence, error) {
	var zero BackendHandshakeEvidence
	if native == nil || len(raw) == 0 || len(raw) > control.MaxCommandBytes || len(signature) != 64 || len(descriptor) == 0 || len(descriptor) > appplatform.MaxDescriptorBytes || !appplatform.ValidBackendHandshakeToken(challenge) {
		return zero, ErrFenceReconciliation
	}
	raw, signature, descriptor = bytes.Clone(raw), bytes.Clone(signature), bytes.Clone(descriptor)
	before, err := s.ReadBackendRegistrationReceipt(ctx, raw, signature)
	if err != nil {
		return zero, err
	}
	tx, f, err := s.beginBackendRegistration(ctx, raw, signature, "applied")
	if err != nil {
		return zero, err
	}
	stored, _ := json.Marshal(before)
	if f.receipt != string(stored) {
		tx.Rollback()
		return zero, ErrFenceReconciliation
	}
	if err := checkBackendRegistration(ctx, f, raw, signature); err != nil {
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
	device, _ := strconv.ParseUint(before.Device, 10, 64)
	inode, _ := strconv.ParseUint(before.Inode, 10, 64)
	binding := driver.BackendRegistrationSnapshot{Launch: snapshot, NativeID: before.Claim.NativeID, BindingSHA256: before.BindingSHA256, DocumentSHA256: before.Claim.DocumentSHA256, Device: device, Inode: inode}
	observed, err := native.ExchangeCandidateHandshake(ctx, launch, binding, challenge)
	if err != nil {
		return zero, err
	}
	physical, err := observed.Snapshot()
	if err != nil || physical.Launch != snapshot || physical.NativeID != binding.NativeID || physical.BindingSHA256 != binding.BindingSHA256 || physical.DocumentSHA256 != binding.DocumentSHA256 || physical.Device != device || physical.Inode != inode || physical.ChallengeSHA256 != candidateHash([]byte(challenge)) || physical.ObservedMS < f.source.now {
		return zero, ErrFenceReconciliation
	}
	after, err := s.ReadBackendRegistrationReceipt(ctx, raw, signature)
	if err != nil || after != before {
		return zero, ErrFenceReconciliation
	}
	result := BackendHandshakeEvidence{Format: "portal.runner.backend-handshake/1-dev.1", PayloadSHA256: f.verified.Command().PayloadSHA256, DeliveryReceiptSHA256: candidateHash(stored), NativeID: physical.NativeID, ChallengeSHA256: physical.ChallengeSHA256, Answer: physical.Answer, ObservedMS: strconv.FormatInt(physical.ObservedMS, 10)}
	encoded, _ := json.Marshal(result)
	return ParseBackendHandshakeEvidence(encoded)
}
