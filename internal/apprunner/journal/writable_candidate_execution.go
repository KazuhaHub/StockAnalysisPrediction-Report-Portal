package journal

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strconv"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/driver"
)

const writableRunningFormat = "portal.runner.writable-candidate-running-receipt/1-dev.1"

type writableCandidatePreflightEvidence struct {
	Format        string                     `json:"format"`
	Native        candidatePreflightEvidence `json:"native"`
	SeccompSHA256 string                     `json:"seccomp_sha256"`
}

func writablePreflight(snapshot driver.WritableCandidateLaunchSnapshot) writableCandidatePreflightEvidence {
	return writableCandidatePreflightEvidence{Format: "portal.runner.writable-candidate-preflight/1-dev.1", Native: candidatePreflight(snapshot.Launch), SeccompSHA256: snapshot.SeccompSHA256}
}
func validWritablePreflight(p writableCandidatePreflightEvidence) bool {
	return p.Format == "portal.runner.writable-candidate-preflight/1-dev.1" && validCandidatePreflight(p.Native) && dataSHA(p.SeccompSHA256)
}
func writableMarkerCore(m writableCandidateClaimMarker) writableCandidateClaimMarker {
	m.Preflight = nil
	m.PreflightBoundMS = ""
	return m
}
func writableCandidateBinding(m writableCandidateClaimMarker) driver.WritableCandidateBinding {
	return driver.WritableCandidateBinding{EffectID: m.EffectID, ClaimID: m.ClaimID, PayloadSHA256: m.PayloadSHA256, OwnershipSHA256: m.OwnershipSHA256, DataReceiptSHA256: m.DataReceiptSHA256, WriterReceiptSHA256: m.WriterReceiptSHA256}
}
func writableNativeRequest(f writableCandidateFacts, m writableCandidateClaimMarker, descriptor []byte) (driver.WritableCandidateLaunchRequest, error) {
	var zero driver.WritableCandidateLaunchRequest
	c := f.verified.Index()
	if len(descriptor) > 0 {
		parsed, err := appplatform.ParseDescriptor(descriptor)
		if err != nil || parsed.Manifest.Backend == nil || parsed.SHA256 != c.Payload.ReleaseSHA256 || strconv.Itoa(parsed.Manifest.Data.Schema) != c.Payload.DataFormat {
			return zero, ErrFenceReconciliation
		}
		found := false
		for _, p := range parsed.Manifest.Backend.Platforms {
			if p == c.Payload.Platform {
				found = true
			}
		}
		if !found {
			return zero, ErrFenceReconciliation
		}
	}
	base, err := candidateNativeRequest(c, f.source.data, descriptor)
	if err != nil {
		return zero, err
	}
	return driver.WritableCandidateLaunchRequest{CandidateLaunchRequest: base, Binding: writableCandidateBinding(m)}, nil
}
func writableSnapshotMatches(snapshot driver.WritableCandidateLaunchSnapshot, f writableCandidateFacts, m writableCandidateClaimMarker) bool {
	request, err := writableNativeRequest(f, m, nil)
	return err == nil && snapshot.Binding == request.Binding && snapshot.Launch.Identity == request.Identity && snapshot.Launch.Data == request.Data && snapshot.Launch.Platform == request.Platform && snapshot.Launch.ImageID == f.source.writer.Native.ImageID && snapshot.SandboxProfile == driver.WritableCandidateSandboxProfile && snapshot.SeccompSHA256 == f.source.writer.Native.SeccompSHA256 && snapshot.SeccompSHA256 == driver.DataWriterPolicySHA256()
}
func writablePreflightMatches(p writableCandidatePreflightEvidence, f writableCandidateFacts, m writableCandidateClaimMarker) bool {
	request, err := writableNativeRequest(f, m, nil)
	if err != nil || !validWritablePreflight(p) {
		return false
	}
	expected := candidatePreflight(driver.CandidateLaunchSnapshot{Identity: request.Identity, Data: request.Data, Platform: request.Platform, ImageID: f.source.writer.Native.ImageID, PolicySHA256: p.Native.PolicySHA256, MountsSHA256: p.Native.MountsSHA256})
	return expected == p.Native && p.SeccompSHA256 == f.source.writer.Native.SeccompSHA256 && p.SeccompSHA256 == driver.DataWriterPolicySHA256()
}

// Only a live opaque native launch can bind actual preflight. A public parsed
// snapshot or callback boolean cannot enter this path. Caller holds owner gate.
func (s *Store) bindWritableCandidatePreflight(ctx context.Context, live *writableCandidateClaimState, launch driver.WritableCandidateLaunch) error {
	if live == nil || live.owner != s || !live.used.Load() || live.bound != nil {
		return ErrWritableCandidateClaimUnavailable
	}
	actual, err := launch.Snapshot()
	if err != nil {
		return err
	}
	tx, f, err := s.beginWritableCandidate(ctx, live.raw, live.signature, "unknown", &live.marker)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var m writableCandidateClaimMarker
	if !canonicalDataJSON(live.marker, &m) || m.Preflight != nil || !writableSnapshotMatches(actual, f, m) {
		return ErrFenceReconciliation
	}
	p := writablePreflight(actual)
	if !writablePreflightMatches(p, f, m) {
		return ErrFenceReconciliation
	}
	m.Preflight = &p
	m.PreflightBoundMS = strconv.FormatInt(f.now, 10)
	if !writableCandidateMarkerMatches(m, f) {
		return ErrFenceReconciliation
	}
	encoded, _ := json.Marshal(m)
	changed, err := tx.ExecContext(ctx, `UPDATE runner_commands SET receipt_json=?,updated_ms=? WHERE effect_id=? AND state='unknown' AND receipt_json=? AND ack_attempted_ms IS NULL`, string(encoded), f.now, m.EffectID, live.marker)
	if err != nil {
		return err
	}
	if n, err := changed.RowsAffected(); err != nil || n != 1 {
		return ErrFenceReconciliation
	}
	if err := finalWritableCandidateCheck(ctx, f); err != nil {
		return err
	}
	if s.closing.Load() {
		return ErrWritableCandidateClaimUnavailable
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	live.marker = string(encoded)
	live.bound = &p
	return finalWritableCandidateCheck(ctx, f)
}
func (s *Store) checkBoundWritableCandidate(ctx context.Context, live *writableCandidateClaimState, launch driver.WritableCandidateLaunch) error {
	if live == nil || live.owner != s || !live.used.Load() || live.bound == nil {
		return ErrWritableCandidateClaimUnavailable
	}
	actual, err := launch.Snapshot()
	if err != nil {
		return err
	}
	tx, f, err := s.beginWritableCandidate(ctx, live.raw, live.signature, "unknown", &live.marker)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var m writableCandidateClaimMarker
	if !canonicalDataJSON(live.marker, &m) || m.Preflight == nil || *m.Preflight != *live.bound || *m.Preflight != writablePreflight(actual) || !writableSnapshotMatches(actual, f, m) {
		return ErrFenceReconciliation
	}
	if err := finalWritableCandidateCheck(ctx, f); err != nil {
		return err
	}
	if s.closing.Load() {
		return ErrWritableCandidateClaimUnavailable
	}
	return tx.Commit()
}

func (s *Store) ExecuteWritableCandidate(ctx context.Context, claim WritableCandidateClaim, native *driver.CandidateRuntime, descriptor []byte) (WritableCandidateRunningReceipt, error) {
	var zero WritableCandidateRunningReceipt
	if native == nil || len(descriptor) == 0 || len(descriptor) > appplatform.MaxDescriptorBytes {
		return zero, ErrWritableCandidateClaimUnavailable
	}
	descriptor = bytes.Clone(descriptor)
	var observed driver.WritableCandidateRunningObservation
	_, err := s.RunWritableCandidateOnce(ctx, claim, func(ctx context.Context, _ WritableCandidatePlan) error {
		live := claim.state
		// Derive from the independently current pre-effect transaction again; a
		// caller-controlled plan copy is not used as authorization.
		tx, f, err := s.beginWritableCandidate(ctx, live.raw, live.signature, "unknown", &live.marker)
		if err != nil {
			return err
		}
		var m writableCandidateClaimMarker
		if !canonicalDataJSON(live.marker, &m) {
			tx.Rollback()
			return ErrFenceReconciliation
		}
		request, err := writableNativeRequest(f, m, descriptor)
		tx.Rollback()
		if err != nil {
			return err
		}
		if err := native.ProvisionWritableCandidateSockets(ctx, request); err != nil {
			return err
		}
		launch, err := native.PrepareWritableCandidate(ctx, request)
		if err != nil {
			return err
		}
		defer launch.Close()
		if err := s.bindWritableCandidatePreflight(ctx, live, launch); err != nil {
			return err
		}
		if err := s.checkBoundWritableCandidate(ctx, live, launch); err != nil {
			return err
		}
		observed, err = native.StartWritableCandidate(ctx, launch)
		return err
	})
	if err != nil {
		return zero, err
	}
	receipt, err := s.settleWritableCandidateRunning(ctx, claim, observed)
	if err != nil {
		return zero, errors.Join(ErrWritableCandidateUncertain, err)
	}
	return receipt, nil
}
