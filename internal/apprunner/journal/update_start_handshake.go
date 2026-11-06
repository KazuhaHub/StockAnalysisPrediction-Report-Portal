package journal

import (
	"bytes"
	"context"
	"encoding/json"
	"strconv"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/driver"
)

const updateStartBackendHandshakeFormat = "portal.runner.update-start-backend-handshake/1-dev.1"

type UpdateStartBackendHandshakeEvidence struct {
	Format                string `json:"format"`
	PayloadSHA256         string `json:"payload_sha256"`
	DeliveryReceiptSHA256 string `json:"delivery_receipt_sha256"`
	NativeID              string `json:"native_id"`
	ChallengeSHA256       string `json:"challenge_sha256"`
	Answer                string `json:"answer"`
	ObservedMS            string `json:"observed_ms"`
}

func ParseUpdateStartBackendHandshakeEvidence(raw []byte) (UpdateStartBackendHandshakeEvidence, error) {
	var evidence UpdateStartBackendHandshakeEvidence
	if len(raw) == 0 || len(raw) > 8192 ||
		!canonicalDataJSON(string(raw), &evidence) ||
		evidence.Format != updateStartBackendHandshakeFormat ||
		!dataSHA(evidence.PayloadSHA256) ||
		!dataSHA(evidence.DeliveryReceiptSHA256) ||
		!dataSHA(evidence.NativeID) ||
		!dataSHA(evidence.ChallengeSHA256) ||
		!appplatform.ValidBackendHandshakeToken(evidence.Answer) ||
		!dataNumber(evidence.ObservedMS, false) {
		return UpdateStartBackendHandshakeEvidence{}, ErrFenceReconciliation
	}
	return evidence, nil
}

type updateStartBackendExchangePreflight struct {
	facts   updateStartRegistrationFacts
	receipt UpdateStartRegistrationReceipt
	request driver.UpdateStartLaunchRequest
	binding driver.UpdateStartRegistrationSnapshot
}

func (s *Store) prepareUpdateStartBackendExchange(ctx context.Context, raw, signature []byte) (updateStartBackendExchangePreflight, error) {
	var zero updateStartBackendExchangePreflight
	tx, facts, err := beginUpdateStartRegistration(
		ctx, s, raw, signature, "applied")
	if err != nil {
		return zero, err
	}
	defer tx.Rollback()
	receipt, err := ParseUpdateStartRegistrationReceipt(
		[]byte(facts.row.receipt))
	if err != nil {
		return zero, err
	}
	device, _ := strconv.ParseUint(receipt.Device, 10, 64)
	inode, _ := strconv.ParseUint(receipt.Inode, 10, 64)
	binding := driver.UpdateStartRegistrationSnapshot{
		Launch:         facts.source.expected,
		NativeID:       receipt.Claim.NativeID,
		BindingSHA256:  receipt.BindingSHA256,
		DocumentSHA256: receipt.Claim.DocumentSHA256,
		Device:         device, Inode: inode,
	}
	if err := checkUpdateStartRegistrationCurrent(ctx, facts); err != nil {
		return zero, err
	}
	if s.closing.Load() {
		return zero, ErrFenceReconciliation
	}
	if err := tx.Commit(); err != nil {
		return zero, err
	}
	if err := checkUpdateStartRegistrationCurrent(ctx, facts); err != nil {
		return zero, err
	}
	return updateStartBackendExchangePreflight{
		facts: facts, receipt: receipt, request: facts.source.request,
		binding: binding,
	}, nil
}

func (s *Store) ExchangeUpdateStartBackendHandshake(ctx context.Context, raw, signature, descriptor []byte, challenge string, native *driver.CandidateRuntime) (UpdateStartBackendHandshakeEvidence, error) {
	var zero UpdateStartBackendHandshakeEvidence
	if native == nil || s.closing.Load() || len(raw) == 0 ||
		len(raw) > control.MaxCommandBytes || len(signature) != 64 ||
		len(descriptor) == 0 || len(descriptor) > appplatform.MaxDescriptorBytes ||
		!appplatform.ValidBackendHandshakeToken(challenge) {
		return zero, ErrFenceReconciliation
	}
	raw, signature, descriptor = bytes.Clone(raw), bytes.Clone(signature), bytes.Clone(descriptor)
	parsed, err := appplatform.ParseDescriptor(descriptor)
	if err != nil || parsed.Manifest.Backend == nil {
		return zero, ErrFenceReconciliation
	}
	if err := s.acquireDataExecution(ctx); err != nil {
		return zero, err
	}
	defer s.releaseDataExecution()
	if s.closing.Load() {
		return zero, ErrFenceReconciliation
	}
	before, err := s.prepareUpdateStartBackendExchange(ctx, raw, signature)
	if err != nil {
		return zero, err
	}
	if parsed.SHA256 != before.facts.verified.Start().Command().Payload.TargetReleaseSHA256 {
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
	expires, _ := strconv.ParseInt(
		before.facts.verified.Start().Command().ExpiresMS, 10, 64)
	ctx, cancel := context.WithDeadline(ctx, time.UnixMilli(expires))
	defer cancel()
	if s.closing.Load() {
		return zero, ErrFenceReconciliation
	}
	observed, err := native.ExchangeUpdateStartBackendHandshake(
		ctx, before.request, before.binding, challenge)
	if err != nil {
		return zero, err
	}
	return s.finishUpdateStartBackendHandshake(
		ctx, before, challenge, observed)
}

func (s *Store) finishUpdateStartBackendHandshake(ctx context.Context, before updateStartBackendExchangePreflight, challenge string, observed driver.UpdateStartBackendHandshakeObservation) (UpdateStartBackendHandshakeEvidence, error) {
	var zero UpdateStartBackendHandshakeEvidence
	tx, facts, err := beginUpdateStartRegistration(
		ctx, s, before.facts.raw, before.facts.signature, "applied")
	if err != nil {
		return zero, err
	}
	defer tx.Rollback()
	if !sameUpdateStartRegistrationSource(before.facts, facts) ||
		s.closing.Load() ||
		!appplatform.ValidBackendHandshakeToken(challenge) {
		return zero, ErrFenceReconciliation
	}
	actual, err := observed.Snapshot()
	if err != nil || actual.Registration != before.binding ||
		actual.ChallengeSHA256 != candidateHash([]byte(challenge)) ||
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
	if err := checkUpdateStartRegistrationCurrent(ctx, facts); err != nil {
		return zero, err
	}
	if s.closing.Load() {
		return zero, ErrFenceReconciliation
	}
	result := UpdateStartBackendHandshakeEvidence{
		Format:                updateStartBackendHandshakeFormat,
		PayloadSHA256:         facts.verified.Command().PayloadSHA256,
		DeliveryReceiptSHA256: candidateHash([]byte(facts.row.receipt)),
		NativeID:              actual.Registration.NativeID,
		ChallengeSHA256:       actual.ChallengeSHA256,
		Answer:                actual.Answer,
		ObservedMS:            strconv.FormatInt(actual.ObservedMS, 10),
	}
	raw, _ := json.Marshal(result)
	return ParseUpdateStartBackendHandshakeEvidence(raw)
}
