//go:build linux || darwin

package journal

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
)

func appliedReceiptFixture(t *testing.T) (*Store, []byte, []byte, DataPreparationReceipt) {
	t.Helper()
	s, _, claim, pin := boundRecoveryFixture(t)
	var receipt DataPreparationReceipt
	// Synthetic observation tests journal validation only; native acceptance is separate.
	tx, facts, err := s.beginDataCommand(context.Background(), claim.state.raw, claim.state.signature, "unknown", claim.state.marker)
	if err != nil {
		t.Fatal(err)
	}
	resource, err := existingDataResource(context.Background(), tx, facts.command)
	if err != nil {
		t.Fatal(err)
	}
	reservation, err := reservedDataResource(facts, resource)
	if err != nil {
		t.Fatal(err)
	}
	tx.Rollback()
	receipt, err = s.settlePreparedData(context.Background(), claim.state, pin, syntheticDataObservation(reservation))
	if err != nil {
		t.Fatal(err)
	}
	return s, claim.state.raw, claim.state.signature, receipt
}
func TestReadDataReceiptMatchesAppliedHistory(t *testing.T) {
	s, raw, sig, expected := appliedReceiptFixture(t)
	got, err := s.ReadDataPreparationReceipt(context.Background(), raw, sig)
	if err != nil || got != expected {
		t.Fatal("applied receipt unavailable", got, err)
	}
	var command control.Command
	if json.Unmarshal(raw, &command) != nil || !got.MatchesCommand(command) {
		t.Fatal("receipt not bound to exact command")
	}
	encoded, _ := json.Marshal(got)
	parsed, err := ParseDataPreparationReceipt(encoded)
	if err != nil || parsed != got {
		t.Fatal("canonical receipt codec", err)
	}
	command.Payload.DataNamespaceID = fixtureID(999)
	command.PayloadSHA256, err = control.LogicalDigest(command)
	if err != nil {
		t.Fatal(err)
	}
	if got.MatchesCommand(command) {
		t.Fatal("cross-command receipt matched")
	}
}
func TestReadDataReceiptRefusesUnsettledWork(t *testing.T) {
	s, _, raw, sig := reservedClaimFixture(t)
	if got, err := s.ReadDataPreparationReceipt(context.Background(), raw, sig); err == nil || got != (DataPreparationReceipt{}) {
		t.Fatal("prepared intent exposed as receipt")
	}
	if _, err := s.ClaimPrepareData(context.Background(), raw, sig); err != nil {
		t.Fatal(err)
	}
	if got, err := s.ReadDataPreparationReceipt(context.Background(), raw, sig); err == nil || got != (DataPreparationReceipt{}) {
		t.Fatal("unknown claim exposed as receipt")
	}
}

func TestReadDataReceiptRefusesChangedResourceOrAuthority(t *testing.T) {
	for _, kind := range []string{"state", "desired", "revision", "observed_ms", "updated_ms", "writer", "fence", "owner", "controller", "ack", "receipt", "signature"} {
		t.Run(kind, func(t *testing.T) {
			s, raw, sig, _ := appliedReceiptFixture(t)
			statements := map[string]string{
				"state":       `UPDATE runner_resources SET observed_state='unknown'`,
				"desired":     `UPDATE runner_resources SET desired_state='absent'`,
				"revision":    `UPDATE runner_resources SET revision=3`,
				"observed_ms": `UPDATE runner_resources SET observed_ms=observed_ms+1`,
				"updated_ms":  `UPDATE runner_resources SET updated_ms=updated_ms+1`,
				"writer":      `UPDATE runner_resources SET writer_fence=2`,
				"fence":       `UPDATE runner_resources SET control_fence=2`,
				"owner":       `UPDATE runner_resources SET ownership_sha256='bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb'`,
				"controller":  `UPDATE runner_deployment SET control_revision=4`,
				"ack":         `UPDATE runner_commands SET ack_attempted_ms=updated_ms`,
				"receipt":     `UPDATE runner_commands SET receipt_json='{}'`,
			}
			if statement := statements[kind]; statement != "" {
				if _, err := s.db.Exec(statement); err != nil {
					t.Fatal(err)
				}
			}
			if kind == "signature" {
				sig[0] ^= 1
			}
			if got, err := s.ReadDataPreparationReceipt(context.Background(), raw, sig); err == nil || got != (DataPreparationReceipt{}) {
				t.Fatal("changed receipt authority read", kind, err)
			}
		})
	}
}
