//go:build linux || darwin

package journal

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
)

// Direct synthetic rows test journal read validation only. They are never passed
// to native settlement and are not evidence of an actual running process.
func appliedCandidateReceiptFixture(t *testing.T) (*Store, []byte, []byte, CandidateRunningReceipt) {
	t.Helper()
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
		return s.bindCandidatePreflight(ctx, claim.state, candidateSnapshotFixture(t, c, proof))
	})
	if err != nil {
		t.Fatal(err)
	}
	var marker candidateClaimMarker
	if !canonicalDataJSON(claim.state.marker, &marker) {
		t.Fatal("fixture marker")
	}
	now := time.Now().UnixMilli()
	receipt := CandidateRunningReceipt{Format: candidateReceiptFormat, Claim: marker, DeploymentID: c.DeploymentID, ControllerEpoch: c.ControllerEpoch, ControlRevision: c.ControlRevision, InstallationID: c.InstallationID, OperationID: c.OperationID, Generation: c.TargetGeneration, WriterFence: c.Payload.WriterFence, PayloadSHA256: c.PayloadSHA256, ReleaseSHA256: c.Payload.ReleaseSHA256, DataNamespaceID: c.Payload.DataNamespaceID, DataFormat: c.Payload.DataFormat, NativeID: string(bytes.Repeat([]byte{'d'}, 64)), NativeObservedMS: strconv.FormatInt(now, 10), SettledMS: strconv.FormatInt(now, 10)}
	encoded, err := json.Marshal(receipt)
	if err != nil || !validCandidateReceipt(receipt) {
		t.Fatal("fixture receipt", err)
	}
	tx, err := s.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE runner_resources SET native_id=?,observed_state='running',revision=2,observed_ms=?,updated_ms=? WHERE kind='container'`, receipt.NativeID, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`UPDATE runner_commands SET state='applied',receipt_json=?,updated_ms=? WHERE kind='start-candidate'`, string(encoded), now); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return s, raw, sig, receipt
}
func sameCandidateReceipt(a, b CandidateRunningReceipt) bool {
	first, _ := json.Marshal(a)
	second, _ := json.Marshal(b)
	return bytes.Equal(first, second)
}
func TestCandidateReceiptReadAndRenewedEnvelopeKeepOriginalObservation(t *testing.T) {
	s, raw, sig, expected := appliedCandidateReceiptFixture(t)
	got, err := s.ReadCandidateRunningReceipt(context.Background(), raw, sig)
	if err != nil || !sameCandidateReceipt(got, expected) {
		t.Fatal("applied historical read", err)
	}
	var c control.CandidateCommand
	if err := json.Unmarshal(raw, &c); err != nil || !got.MatchesCommand(c) {
		t.Fatal("receipt lost logical binding", err)
	}
	c.IssuedMS = strconv.FormatInt(time.Now().UnixMilli(), 10)
	c.ExpiresMS = strconv.FormatInt(time.Now().UnixMilli()+25000, 10)
	raw, sig = signJournalCandidate(t, c)
	got, err = s.ReadCandidateRunningReceipt(context.Background(), raw, sig)
	if err != nil || !sameCandidateReceipt(got, expected) {
		t.Fatal("renewal replaced original native receipt", err)
	}
	var databaseFile string
	if err := s.db.QueryRow(`SELECT file FROM pragma_database_list WHERE name='main'`).Scan(&databaseFile); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(filepath.Dir(databaseFile))
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	got, err = reopened.ReadCandidateRunningReceipt(context.Background(), raw, sig)
	if err != nil || !sameCandidateReceipt(got, expected) {
		t.Fatal("restart historical read", err)
	}
	if _, err := reopened.ClaimCandidate(context.Background(), raw, sig); err == nil {
		t.Fatal("applied receipt reclaimed")
	}
}
func TestCandidateReceiptReadRejectsChangedHistoryAndAuthority(t *testing.T) {
	queries := map[string]string{
		"state":        `UPDATE runner_resources SET observed_state='stopped' WHERE kind='container'`,
		"desired":      `UPDATE runner_resources SET desired_state='absent' WHERE kind='container'`,
		"native":       `UPDATE runner_resources SET native_id='eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee' WHERE kind='container'`,
		"revision":     `UPDATE runner_resources SET revision=3 WHERE kind='container'`,
		"observed_ms":  `UPDATE runner_resources SET observed_ms=observed_ms+1 WHERE kind='container'`,
		"updated_ms":   `UPDATE runner_resources SET updated_ms=updated_ms+1 WHERE kind='container'`,
		"writer":       `UPDATE runner_resources SET writer_fence=2 WHERE kind='container'`,
		"fence":        `UPDATE runner_resources SET control_fence=2 WHERE kind='container'`,
		"owner":        `UPDATE runner_resources SET ownership_sha256='eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee' WHERE kind='container'`,
		"controller":   `UPDATE runner_deployment SET control_revision=control_revision+1`,
		"ack":          `UPDATE runner_commands SET ack_attempted_ms=updated_ms WHERE kind='start-candidate'`,
		"receipt":      `UPDATE runner_commands SET receipt_json='{}' WHERE kind='start-candidate'`,
		"data_state":   `UPDATE runner_resources SET observed_state='quarantined' WHERE kind='volume'`,
		"data_receipt": `UPDATE runner_commands SET receipt_json='{}' WHERE kind='prepare-data'`,
	}
	for kind, query := range queries {
		t.Run(kind, func(t *testing.T) {
			s, raw, sig, _ := appliedCandidateReceiptFixture(t)
			if _, err := s.db.Exec(query); err != nil {
				t.Fatal(err)
			}
			if got, err := s.ReadCandidateRunningReceipt(context.Background(), raw, sig); err == nil || got.Format != "" {
				t.Fatal("changed evidence exposed", kind, err)
			}
		})
	}
	for _, kind := range []string{"signature", "logical", "expired", "higher_fence", "cancelled"} {
		t.Run(kind, func(t *testing.T) {
			s, raw, sig, _ := appliedCandidateReceiptFixture(t)
			var c control.CandidateCommand
			if err := json.Unmarshal(raw, &c); err != nil {
				t.Fatal(err)
			}
			ctx := context.Background()
			switch kind {
			case "signature":
				sig[0] ^= 1
			case "logical":
				c.Payload.ResourceID = fixtureID(950)
				raw, sig = signJournalCandidate(t, c)
			case "higher_fence":
				c.InstallationFence = "2"
				raw, sig = signJournalCandidate(t, c)
			case "expired":
				c.ExpiresMS = strconv.FormatInt(time.Now().UnixMilli()+50, 10)
				raw, sig = signJournalCandidate(t, c)
				time.Sleep(60 * time.Millisecond)
			case "cancelled":
				cancelled, cancel := context.WithCancel(ctx)
				cancel()
				ctx = cancelled
			}
			if got, err := s.ReadCandidateRunningReceipt(ctx, raw, sig); err == nil || got.Format != "" {
				t.Fatal("invalid request exposed history", kind, err)
			}
		})
	}
}
func TestCandidateReadRejectsUnsettledAndFalsePreflight(t *testing.T) {
	s, raw, sig := readyCandidateFixture(t)
	if got, err := s.ReadCandidateRunningReceipt(context.Background(), raw, sig); err == nil || got.Format != "" {
		t.Fatal("prepared intent read as running")
	}
	if _, err := s.ReserveCandidate(context.Background(), raw, sig); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ClaimCandidate(context.Background(), raw, sig); err != nil {
		t.Fatal(err)
	}
	if got, err := s.ReadCandidateRunningReceipt(context.Background(), raw, sig); err == nil || got.Format != "" {
		t.Fatal("unknown intent read as running")
	}
	for _, kind := range []string{"data_sha", "inode", "project", "device", "quota", "root", "platform", "operation"} {
		t.Run(kind, func(t *testing.T) {
			s, raw, sig, r := appliedCandidateReceiptFixture(t)
			switch kind {
			case "data_sha":
				r.Claim.DataReceiptSHA256 = string(bytes.Repeat([]byte{'e'}, 64))
			case "inode":
				r.Claim.Preflight.NamespaceInode = "123456"
			case "project":
				r.Claim.Preflight.ProjectID = "123456"
			case "device":
				r.Claim.Preflight.Device = "123456"
			case "quota":
				r.Claim.Preflight.QuotaBytes = "512"
			case "root":
				r.Claim.Preflight.RootFilesystemUUID = fixtureID(950)
			case "platform":
				r.Claim.Preflight.Platform = "linux/arm64"
			case "operation":
				r.OperationID = fixtureID(950)
			}
			encoded, _ := json.Marshal(r)
			if _, err := s.db.Exec(`UPDATE runner_commands SET receipt_json=? WHERE kind='start-candidate'`, string(encoded)); err != nil {
				t.Fatal(err)
			}
			if got, err := s.ReadCandidateRunningReceipt(context.Background(), raw, sig); err == nil || got.Format != "" {
				t.Fatal("false preflight exposed", kind, err)
			}
		})
	}
}
