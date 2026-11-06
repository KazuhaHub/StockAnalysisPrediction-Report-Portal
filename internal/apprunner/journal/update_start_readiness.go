package journal

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/driver"
)

const updateStartReadinessFormat = "portal.runner.update-start-readiness/1-dev.1"

type UpdateStartReadiness struct {
	Format               string `json:"format"`
	PayloadSHA256        string `json:"payload_sha256"`
	RunningReceiptSHA256 string `json:"running_receipt_sha256"`
	NativeID             string `json:"native_id"`
	Protocol             string `json:"protocol"`
	DataFormat           string `json:"data_format"`
	ObservedMS           string `json:"observed_ms"`
}

func ParseUpdateStartReadiness(raw []byte) (UpdateStartReadiness, error) {
	var readiness UpdateStartReadiness
	if len(raw) == 0 || len(raw) > 4096 || !canonicalDataJSON(string(raw), &readiness) ||
		readiness.Format != updateStartReadinessFormat ||
		!dataSHA(readiness.PayloadSHA256) || !dataSHA(readiness.RunningReceiptSHA256) ||
		!dataSHA(readiness.NativeID) || readiness.Protocol != "portal.backend/1" ||
		!dataNumber(readiness.DataFormat, true) || !dataNumber(readiness.ObservedMS, false) {
		return UpdateStartReadiness{}, driver.ErrCandidateNotReady
	}
	return readiness, nil
}

type updateStartReadinessPreflight struct {
	facts    updateStartFacts
	running  UpdateStartRunningReceipt
	request  driver.UpdateStartLaunchRequest
	expected driver.UpdateStartLaunchSnapshot
}

func updateStartExpectedSnapshot(f updateStartFacts, marker UpdateStartClaimMarker, descriptor []byte) (driver.UpdateStartLaunchRequest, driver.UpdateStartLaunchSnapshot, error) {
	if marker.Preflight == nil {
		return driver.UpdateStartLaunchRequest{}, driver.UpdateStartLaunchSnapshot{}, ErrFenceReconciliation
	}
	request, err := updateStartNativeRequest(f, marker, descriptor)
	if err != nil {
		return driver.UpdateStartLaunchRequest{}, driver.UpdateStartLaunchSnapshot{}, err
	}
	preflight := marker.Preflight
	expected := driver.UpdateStartLaunchSnapshot{
		Launch: driver.CandidateLaunchSnapshot{
			Identity: request.Identity, Data: request.Data, Platform: request.Platform,
			ImageID: preflight.ImageID, PolicySHA256: preflight.PolicySHA256,
			MountsSHA256: preflight.MountsSHA256,
		},
		Binding: request.Binding, SandboxProfile: request.SandboxProfile,
		SeccompSHA256: preflight.SeccompSHA256,
	}
	if !updateStartSnapshotMatches(expected, f, marker) ||
		!updateStartPreflightMatches(*preflight, f, marker) {
		return driver.UpdateStartLaunchRequest{}, driver.UpdateStartLaunchSnapshot{}, ErrFenceReconciliation
	}
	return request, expected, nil
}

func (s *Store) prepareUpdateStartReadiness(ctx context.Context, raw, signature, descriptor []byte) (updateStartReadinessPreflight, error) {
	var zero updateStartReadinessPreflight
	if len(descriptor) == 0 || len(descriptor) > appplatform.MaxDescriptorBytes {
		return zero, driver.ErrCandidateNotReady
	}
	tx, facts, err := s.beginUpdateStartExecution(ctx, raw, signature, "applied")
	if err != nil {
		return zero, fmt.Errorf(
			"update-start readiness execution: %w", err)
	}
	defer tx.Rollback()
	running, err := ParseUpdateStartRunningReceipt([]byte(facts.receipt))
	if err != nil {
		return zero, fmt.Errorf(
			"update-start readiness running receipt: %w", err)
	}
	request, expected, err := updateStartExpectedSnapshot(facts, running.Claim, descriptor)
	if err != nil {
		return zero, fmt.Errorf(
			"update-start readiness native snapshot: %w", err)
	}
	if err := finalUpdateStartCheck(ctx, facts); err != nil {
		return zero, fmt.Errorf(
			"update-start readiness final check: %w", err)
	}
	if s.closing.Load() {
		return zero, ErrUpdateStartExecutionUnavailable
	}
	if err := tx.Commit(); err != nil {
		return zero, err
	}
	return updateStartReadinessPreflight{facts: facts, running: running, request: request, expected: expected}, nil
}

func (s *Store) ObserveUpdateStartReadiness(ctx context.Context, raw, signature []byte, native *driver.CandidateRuntime, descriptor []byte) (UpdateStartReadiness, error) {
	var zero UpdateStartReadiness
	if native == nil || len(descriptor) == 0 || len(descriptor) > appplatform.MaxDescriptorBytes || s.closing.Load() {
		return zero, driver.ErrCandidateNotReady
	}
	raw, signature, descriptor = bytes.Clone(raw), bytes.Clone(signature), bytes.Clone(descriptor)
	if err := s.acquireDataExecution(ctx); err != nil {
		return zero, err
	}
	defer s.releaseDataExecution()
	if s.closing.Load() {
		return zero, ErrUpdateStartExecutionUnavailable
	}
	before, err := s.prepareUpdateStartReadiness(ctx, raw, signature, descriptor)
	if err != nil {
		return zero, err
	}
	expires, _ := strconv.ParseInt(before.running.Claim.ExpiresMS, 10, 64)
	ctx, cancel := context.WithDeadline(ctx, time.UnixMilli(expires))
	defer cancel()
	observation, err := native.CheckUpdateStartReadiness(ctx, before.request, before.expected, before.running.NativeID, before.running.DataFormat)
	if err != nil {
		return zero, err
	}
	return s.finishUpdateStartReadiness(ctx, before, observation)
}

func (s *Store) finishUpdateStartReadiness(ctx context.Context, before updateStartReadinessPreflight, observation driver.UpdateStartReadinessObservation) (UpdateStartReadiness, error) {
	var zero UpdateStartReadiness
	tx, facts, err := s.beginUpdateStartExecution(ctx, before.facts.raw, before.facts.signature, "applied")
	if err != nil {
		return zero, fmt.Errorf(
			"update-start readiness finish execution: %w", err)
	}
	defer tx.Rollback()
	if s.closing.Load() || facts.now < before.facts.now ||
		!bytes.Equal(facts.source.checkpointRaw, before.facts.source.checkpointRaw) ||
		!bytes.Equal(facts.source.source.raw, before.facts.source.source.raw) ||
		facts.created != before.facts.created || facts.updated != before.facts.updated ||
		facts.receipt != before.facts.receipt {
		return zero, fmt.Errorf(
			"update-start readiness finish source: %w",
			ErrFenceReconciliation)
	}
	actual, err := observation.Snapshot()
	if err != nil || actual.Launch != before.expected || actual.NativeID != before.running.NativeID ||
		actual.DataFormat != before.running.DataFormat ||
		actual.ObservedMS < before.facts.now || actual.ObservedMS > facts.now {
		return zero, driver.ErrCandidateNotReady
	}
	if err := finalUpdateStartCheck(ctx, facts); err != nil {
		return zero, err
	}
	if err := tx.Commit(); err != nil {
		return zero, err
	}
	readiness := UpdateStartReadiness{
		Format: updateStartReadinessFormat, PayloadSHA256: facts.command.PayloadSHA256,
		RunningReceiptSHA256: candidateHash([]byte(facts.receipt)),
		NativeID:             actual.NativeID, Protocol: actual.Protocol,
		DataFormat: actual.DataFormat,
		ObservedMS: strconv.FormatInt(actual.ObservedMS, 10),
	}
	encoded, _ := json.Marshal(readiness)
	return ParseUpdateStartReadiness(encoded)
}
