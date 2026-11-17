//go:build linux || darwin

package journal

import (
	"bytes"
	"context"
	"encoding/json"
	"strconv"
	"testing"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/driver"
)

// Synthetic snapshots exercise persistence/refusal only, never native success.
func candidateSnapshotFixture(t *testing.T, c control.CandidateCommand, proof DataPreparationReceipt) driver.CandidateLaunchSnapshot {
	t.Helper()
	request, err := candidateNativeRequest(c, proof, nil)
	if err != nil {
		t.Fatal(err)
	}
	return driver.CandidateLaunchSnapshot{Identity: request.Identity, Data: request.Data, Platform: request.Platform, ImageID: "sha256:" + string(bytes.Repeat([]byte{'a'}, 64)), PolicySHA256: string(bytes.Repeat([]byte{'b'}, 64)), MountsSHA256: string(bytes.Repeat([]byte{'c'}, 64))}
}
func TestCandidatePreflightBindsBeforeEffectAndRejectsOpaqueForgery(t *testing.T) {
	s, raw, sig := readyCandidateFixture(t)
	if _, err := s.ReserveCandidate(context.Background(), raw, sig); err != nil {
		t.Fatal(err)
	}
	claim, err := s.ClaimCandidate(context.Background(), raw, sig)
	if err != nil {
		t.Fatal(err)
	}
	var c control.CandidateCommand
	if err := json.Unmarshal(raw, &c); err != nil {
		t.Fatal(err)
	}
	_, err = s.RunCandidateOnce(context.Background(), claim, func(ctx context.Context, _ CandidateReservation, _ control.CandidatePayload, proof DataPreparationReceipt) error {
		snapshot := candidateSnapshotFixture(t, c, proof)
		if err := s.bindCandidatePreflight(ctx, claim.state, snapshot); err != nil {
			return err
		}
		if err := s.checkBoundCandidate(ctx, claim.state, snapshot); err != nil {
			return err
		}
		changed := snapshot
		changed.MountsSHA256 = string(bytes.Repeat([]byte{'d'}, 64))
		if err := s.checkBoundCandidate(ctx, claim.state, changed); err == nil {
			t.Fatal("changed mount pins accepted")
		}
		if err := s.bindCandidatePreflight(ctx, claim.state, snapshot); err == nil {
			t.Fatal("preflight binding replaced")
		}
		if r, err := s.settleCandidateRunning(ctx, claim.state, driver.CandidateRunningObservation{}); err == nil || r.Format != "" {
			t.Fatal("zero native observation settled")
		}
		var forged driver.CandidateRunningObservation
		encoded, _ := json.Marshal(driver.CandidateRunningSnapshot{Launch: snapshot, NativeID: string(bytes.Repeat([]byte{'d'}, 64)), ObservedMS: 1})
		if err := json.Unmarshal(encoded, &forged); err == nil {
			t.Fatal("JSON forged opaque observation")
		}
		if r, err := s.settleCandidateRunning(ctx, claim.state, forged); err == nil || r.Format != "" {
			t.Fatal("forged native observation settled")
		}
		var stored string
		if err := s.db.QueryRow(`SELECT receipt_json FROM runner_commands WHERE effect_id=?`, c.EffectID).Scan(&stored); err != nil {
			t.Fatal(err)
		}
		var m candidateClaimMarker
		if !canonicalDataJSON(stored, &m) || !validCandidateMarker(m) || m.Preflight == nil || *m.Preflight != candidatePreflight(snapshot) {
			t.Fatal("durable preflight lost")
		}
		if scalar(t, s.db, `SELECT COUNT(*) FROM runner_resources WHERE kind='container' AND observed_state='unknown' AND revision=1 AND observed_ms IS NULL`) != 1 {
			t.Fatal("preflight claimed native completion")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
func TestCandidatePreflightRefusesChangedBinding(t *testing.T) {
	for _, kind := range []string{"identity", "data", "platform", "image", "mounts", "fence", "data_state", "authority", "ack", "commit"} {
		t.Run(kind, func(t *testing.T) {
			s, raw, sig := readyCandidateFixture(t)
			if _, err := s.ReserveCandidate(context.Background(), raw, sig); err != nil {
				t.Fatal(err)
			}
			claim, err := s.ClaimCandidate(context.Background(), raw, sig)
			if err != nil {
				t.Fatal(err)
			}
			var c control.CandidateCommand
			if err := json.Unmarshal(raw, &c); err != nil {
				t.Fatal(err)
			}
			_, err = s.RunCandidateOnce(context.Background(), claim, func(ctx context.Context, _ CandidateReservation, _ control.CandidatePayload, proof DataPreparationReceipt) error {
				snapshot := candidateSnapshotFixture(t, c, proof)
				switch kind {
				case "identity":
					snapshot.Identity.OperationID = fixtureID(900)
				case "data":
					snapshot.Data.NamespaceInode++
				case "platform":
					snapshot.Platform = "linux/arm64"
				case "image":
					snapshot.ImageID = "floating:latest"
				case "mounts":
					snapshot.MountsSHA256 = ""
				}
				statements := map[string]string{
					"fence":      `UPDATE runner_resources SET control_fence=control_fence+1 WHERE kind='container'`,
					"data_state": `UPDATE runner_resources SET observed_state='quarantined' WHERE kind='volume'`,
					"authority":  `UPDATE runner_deployment SET control_revision=control_revision+1`,
					"ack":        `UPDATE runner_commands SET ack_attempted_ms=updated_ms WHERE kind='start-candidate'`,
					"commit":     `CREATE TRIGGER fixture_preflight_failure BEFORE UPDATE ON runner_commands WHEN NEW.kind='start-candidate' BEGIN SELECT RAISE(ABORT,'fixture binding failure');END`,
				}
				if query := statements[kind]; query != "" {
					if _, err := s.db.Exec(query); err != nil {
						t.Fatal(err)
					}
				}
				if err := s.bindCandidatePreflight(ctx, claim.state, snapshot); err == nil {
					t.Fatal("changed binding persisted", kind)
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			var stored string
			if err := s.db.QueryRow(`SELECT receipt_json FROM runner_commands WHERE effect_id=?`, c.EffectID).Scan(&stored); err != nil {
				t.Fatal(err)
			}
			var marker candidateClaimMarker
			if !canonicalDataJSON(stored, &marker) || marker.Preflight != nil {
				t.Fatal("failed binding partially persisted")
			}
		})
	}
}
func TestCandidateRequestPreservesNativeIntegerPrecisionAndDataBinding(t *testing.T) {
	_, _, _, proof := appliedReceiptFixture(t)
	c := journalCandidateFixture(t)
	proof.NamespaceInode = "18446744073709551615"
	proof.Device = "9007199254740993"
	snapshot := candidateSnapshotFixture(t, c, proof)
	evidence := candidatePreflight(snapshot)
	if evidence.NamespaceInode != proof.NamespaceInode || evidence.Device != proof.Device || !validCandidatePreflight(evidence) {
		t.Fatal("native integers lost precision")
	}
	for _, kind := range []string{"namespace", "writer", "release", "installation", "epoch", "format"} {
		copy := proof
		switch kind {
		case "namespace":
			copy.Claim.ResourceID = fixtureID(900)
		case "writer":
			copy.WriterFence = "2"
		case "release":
			copy.ReleaseSHA256 = string(bytes.Repeat([]byte{'d'}, 64))
		case "installation":
			copy.InstallationID = fixtureID(900)
		case "epoch":
			copy.ControllerEpoch = fixtureID(900)
		case "format":
			copy.DataFormat = "2"
		}
		if _, err := candidateNativeRequest(c, copy, nil); err == nil {
			t.Fatal("foreign data receipt used", kind)
		}
	}
}
func TestCandidateReceiptCodecDoesNotGrantExecution(t *testing.T) {
	s, raw, sig := readyCandidateFixture(t)
	if _, err := s.ReserveCandidate(context.Background(), raw, sig); err != nil {
		t.Fatal(err)
	}
	claim, err := s.ClaimCandidate(context.Background(), raw, sig)
	if err != nil {
		t.Fatal(err)
	}
	var c control.CandidateCommand
	if err := json.Unmarshal(raw, &c); err != nil {
		t.Fatal(err)
	}
	var marker candidateClaimMarker
	if !canonicalDataJSON(claim.state.marker, &marker) {
		t.Fatal("marker")
	}
	_, _, _, proof := appliedReceiptFixture(t)
	preflight := candidatePreflight(candidateSnapshotFixture(t, c, proof))
	marker.Preflight = &preflight
	marker.PreflightBoundMS = marker.ClaimedMS
	r := CandidateRunningReceipt{Format: candidateReceiptFormat, Claim: marker, DeploymentID: c.DeploymentID, ControllerEpoch: c.ControllerEpoch, ControlRevision: c.ControlRevision, InstallationID: c.InstallationID, OperationID: c.OperationID, Generation: c.TargetGeneration, WriterFence: c.Payload.WriterFence, PayloadSHA256: c.PayloadSHA256, ReleaseSHA256: c.Payload.ReleaseSHA256, DataNamespaceID: c.Payload.DataNamespaceID, DataFormat: c.Payload.DataFormat, NativeID: string(bytes.Repeat([]byte{'d'}, 64)), NativeObservedMS: marker.ClaimedMS, SettledMS: marker.ClaimedMS}
	encoded, _ := json.Marshal(r)
	parsed, err := ParseCandidateRunningReceipt(encoded)
	if err != nil || parsed.NativeID != r.NativeID {
		t.Fatal("canonical metadata rejected", err)
	}
	for _, kind := range []string{"unknown", "trailing", "numeric", "missing", "expiry", "before_bound", "native", "overflow"} {
		copy := r
		candidate := encoded
		switch kind {
		case "unknown":
			candidate = append(bytes.Clone(encoded[:len(encoded)-1]), []byte(`,"unknown":true}`)...)
		case "trailing":
			candidate = append(bytes.Clone(encoded), ' ')
		case "numeric":
			candidate = bytes.ReplaceAll(encoded, []byte(`"native_observed_ms":"`+r.NativeObservedMS+`"`), []byte(`"native_observed_ms":`+r.NativeObservedMS))
		case "missing":
			copy.Claim.Preflight = nil
		case "expiry":
			copy.SettledMS = marker.ExpiresMS
		case "before_bound":
			n, _ := strconv.ParseInt(marker.ClaimedMS, 10, 64)
			copy.NativeObservedMS = strconv.FormatInt(n-1, 10)
		case "native":
			copy.NativeID = "candidate-pending/v1/" + marker.ResourceID
		case "overflow":
			copy.SettledMS = "9223372036854775808"
		}
		if kind != "unknown" && kind != "trailing" && kind != "numeric" {
			candidate, _ = json.Marshal(copy)
		}
		if _, err := ParseCandidateRunningReceipt(candidate); err == nil {
			t.Fatal("invalid receipt metadata accepted", kind)
		}
	}
	if scalar(t, s.db, `SELECT COUNT(*) FROM runner_commands WHERE kind='start-candidate' AND state='unknown'`) != 1 {
		t.Fatal("receipt parser changed execution state")
	}
}
