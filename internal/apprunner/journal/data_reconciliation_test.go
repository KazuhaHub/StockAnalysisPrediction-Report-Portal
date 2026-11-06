//go:build linux || darwin

package journal

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/data"
)

func boundRecoveryFixture(t *testing.T) (*Store, string, DataClaim, data.RootIdentity) {
	t.Helper()
	s, directory, raw, sig := reservedClaimFixture(t)
	claim, err := s.ClaimPrepareData(context.Background(), raw, sig)
	if err != nil {
		t.Fatal(err)
	}
	pin := data.RootIdentity{FilesystemUUID: fixtureID(900), Inode: 10}
	_, err = s.RunPrepareDataOnce(context.Background(), claim, func(ctx context.Context, _ DataReservation, _ control.PrepareDataPayload) error {
		return s.bindDataRoot(ctx, claim.state, pin)
	})
	if err != nil {
		t.Fatal(err)
	}
	return s, directory, claim, pin
}
func recoveryEnvelope(t *testing.T, claim DataClaim) ([]byte, []byte) {
	t.Helper()
	var marker dataClaimMarker
	var c control.Command
	if json.Unmarshal([]byte(claim.state.marker), &marker) != nil || json.Unmarshal(claim.state.raw, &c) != nil {
		t.Fatal("recovery fixture")
	}
	claimed, _ := strconv.ParseInt(marker.ClaimedMS, 10, 64)
	for time.Now().UnixMilli() <= claimed {
		time.Sleep(time.Millisecond)
	}
	now := time.Now().UnixMilli()
	c.IssuedMS, c.ExpiresMS = strconv.FormatInt(now, 10), strconv.FormatInt(now+29000, 10)
	return signRunnerCommand(t, c)
}
func TestDataRecoveryCannotInventObservation(t *testing.T) {
	s, _, claim, pin := boundRecoveryFixture(t)
	raw, sig := recoveryEnvelope(t, claim)
	for _, root := range []*data.Root{nil, {}} {
		result, err := s.ReconcilePrepareData(context.Background(), raw, sig, root, pin)
		if err == nil || result != (DataPreparationReceipt{}) {
			t.Fatal("invalid native provider reconciled unknown work", err)
		}
	}
	if scalar(t, s.db, `SELECT COUNT(*) FROM runner_commands WHERE state='unknown'`) != 1 {
		t.Fatal("missing observation changed state")
	}
}
func TestDataRecoveryPreflightRequiresFreshExactBoundIntent(t *testing.T) {
	s, _, claim, pin := boundRecoveryFixture(t)
	if _, err := s.prepareDataRecovery(context.Background(), claim.state.raw, claim.state.signature, pin); !errors.Is(err, ErrFenceReconciliation) {
		t.Fatal("old execution envelope authorized recovery", err)
	}
	raw, sig := recoveryEnvelope(t, claim)
	preflight, err := s.prepareDataRecovery(context.Background(), raw, sig, pin)
	if err != nil || preflight.marker != claim.state.marker || preflight.reservation.ProjectID != 1 {
		t.Fatal("fresh recovery unavailable", err)
	}
	if _, err := s.prepareDataRecovery(context.Background(), raw, sig, data.RootIdentity{FilesystemUUID: pin.FilesystemUUID, Inode: 11}); !errors.Is(err, ErrFenceReconciliation) {
		t.Fatal("wrong physical root authorized", err)
	}
}

func TestDataRecoverySyntheticSettlementAfterOriginalExpiry(t *testing.T) {
	s, directory := enrolledCommandStore(t)
	c := runnerCommandFixture(t)
	c.ExpiresMS = strconv.FormatInt(time.Now().UnixMilli()+600, 10)
	raw, sig := signRunnerCommand(t, c)
	if _, err := s.AdmitControlCommand(context.Background(), raw, sig); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReservePrepareData(context.Background(), raw, sig); err != nil {
		t.Fatal(err)
	}
	claim, err := s.ClaimPrepareData(context.Background(), raw, sig)
	if err != nil {
		t.Fatal(err)
	}
	pin := data.RootIdentity{FilesystemUUID: fixtureID(900), Inode: 10}
	_, err = s.RunPrepareDataOnce(context.Background(), claim, func(ctx context.Context, _ DataReservation, _ control.PrepareDataPayload) error {
		return s.bindDataRoot(ctx, claim.state, pin)
	})
	if err != nil {
		t.Fatal(err)
	}
	expires, _ := strconv.ParseInt(c.ExpiresMS, 10, 64)
	<-time.After(time.Until(time.UnixMilli(expires + 1)))
	if _, err := s.prepareDataRecovery(context.Background(), raw, sig, pin); err == nil {
		t.Fatal("expired original authority recovered work")
	}
	renewed, renewedSig := recoveryEnvelope(t, claim)
	preflight, err := s.prepareDataRecovery(context.Background(), renewed, renewedSig, pin)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`CREATE TRIGGER fixture_recovery_failure BEFORE UPDATE ON runner_commands WHEN NEW.state='applied' BEGIN SELECT RAISE(ABORT,'fixture recovery receipt failure');END`); err != nil {
		t.Fatal(err)
	}
	if got, err := s.settleDataObservation(context.Background(), renewed, renewedSig, preflight.marker, pin, syntheticDataObservation(preflight.reservation), true); err == nil || got != (DataPreparationReceipt{}) {
		t.Fatal("failed recovery returned receipt")
	}
	if scalar(t, s.db, `SELECT COUNT(*) FROM runner_commands WHERE state='unknown'`) != 1 || scalar(t, s.db, `SELECT COUNT(*) FROM runner_resources WHERE revision=1 AND observed_state='unknown' AND observed_ms IS NULL`) != 1 {
		t.Fatal("failed recovery partly committed")
	}
	if _, err := s.db.Exec(`DROP TRIGGER fixture_recovery_failure`); err != nil {
		t.Fatal(err)
	}
	got, err := s.settleDataObservation(context.Background(), renewed, renewedSig, preflight.marker, pin, syntheticDataObservation(preflight.reservation), true)
	if err != nil || got.Format != dataReconciledReceiptFormat || !validDataReceipt(got) || got.RecoveryEnvelopeSHA256 == "" {
		t.Fatal("fresh recovery could not settle", got, err)
	}
	observed, _ := strconv.ParseInt(got.ObservedMS, 10, 64)
	if observed < expires {
		t.Fatal("fixture did not cross original expiry")
	}
	var marker dataClaimMarker
	if json.Unmarshal([]byte(claim.state.marker), &marker) != nil || got.Claim != marker {
		t.Fatal("recovery rewrote original claim")
	}
	forged := got
	forged.Format = dataReceiptFormat
	if validDataReceipt(forged) {
		t.Fatal("recovery receipt accepted as original execution")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	var stored string
	if err := reopened.db.QueryRow(`SELECT receipt_json FROM runner_commands`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	var durable DataPreparationReceipt
	if !canonicalDataJSON(stored, &durable) || durable != got || !validDataReceipt(durable) {
		t.Fatal("reopen lost recovered receipt")
	}
	tx, err := reopened.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := requireDataRootHistory(context.Background(), tx, pin); err != nil {
		t.Fatal("recovery lost root binding", err)
	}
}
func TestDataRecoveryRefusesChangedIntentOrAuthority(t *testing.T) {
	for _, kind := range []string{"unbound", "marker_owner", "marker_effect", "marker_resource", "marker_fence", "ack", "controller", "fence", "writer", "desired", "revision", "observed", "signature", "different_effect", "different_payload", "renewed_controller", "higher_fence", "closed"} {
		t.Run(kind, func(t *testing.T) {
			s, _, claim, pin := boundRecoveryFixture(t)
			raw, sig := recoveryEnvelope(t, claim)
			var marker dataClaimMarker
			if json.Unmarshal([]byte(claim.state.marker), &marker) != nil {
				t.Fatal("fixture")
			}
			statements := map[string]string{
				"ack":        `UPDATE runner_commands SET ack_attempted_ms=updated_ms`,
				"controller": `UPDATE runner_deployment SET control_revision=control_revision+1`,
				"fence":      `UPDATE runner_resources SET control_fence=control_fence+1`,
				"writer":     `UPDATE runner_resources SET writer_fence=writer_fence+1`,
				"desired":    `UPDATE runner_resources SET desired_state='absent'`,
				"revision":   `UPDATE runner_resources SET revision=2`,
				"observed":   `UPDATE runner_resources SET observed_state='present'`,
			}
			if statement := statements[kind]; statement != "" {
				if _, err := s.db.Exec(statement); err != nil {
					t.Fatal(err)
				}
			}
			switch kind {
			case "unbound":
				marker.RootFilesystemUUID, marker.RootInode = "", ""
			case "marker_owner":
				marker.OwnershipSHA256 = strings.Repeat("b", 64)
			case "marker_effect":
				marker.EffectID = fixtureID(901)
			case "marker_resource":
				marker.ResourceID = fixtureID(902)
			case "marker_fence":
				marker.AdmittedFence = "2"
			case "signature":
				sig[0] ^= 1
			case "different_effect", "different_payload", "renewed_controller", "higher_fence":
				var c control.Command
				if json.Unmarshal(raw, &c) != nil {
					t.Fatal("fixture command")
				}
				switch kind {
				case "different_effect":
					c.EffectID = fixtureID(903)
				case "different_payload":
					c.Payload.QuotaBytes = "536870912"
				case "higher_fence":
					c.InstallationFence = "2"
				case "renewed_controller":
					c.ControlRevision = "2"
					if _, err := s.db.Exec(`UPDATE runner_deployment SET control_revision=2`); err != nil {
						t.Fatal(err)
					}
				}
				raw, sig = signRunnerCommand(t, c)
			case "closed":
				if err := s.Close(); err != nil {
					t.Fatal(err)
				}
			}
			if strings.HasPrefix(kind, "marker_") || kind == "unbound" {
				encoded, _ := json.Marshal(marker)
				if _, err := s.db.Exec(`UPDATE runner_commands SET receipt_json=?`, string(encoded)); err != nil {
					t.Fatal(err)
				}
			}
			if got, err := s.prepareDataRecovery(context.Background(), raw, sig, pin); err == nil || got.marker != "" {
				t.Fatal("changed recovery intent accepted", kind, err)
			}
		})
	}
}
func TestDataRecoveryRechecksAuthorityAtSettlement(t *testing.T) {
	s, _, claim, pin := boundRecoveryFixture(t)
	raw, sig := recoveryEnvelope(t, claim)
	preflight, err := s.prepareDataRecovery(context.Background(), raw, sig, pin)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE runner_resources SET writer_fence=writer_fence+1`); err != nil {
		t.Fatal(err)
	}
	if got, err := s.settleDataObservation(context.Background(), raw, sig, preflight.marker, pin, syntheticDataObservation(preflight.reservation), true); err == nil || got != (DataPreparationReceipt{}) {
		t.Fatal("stale preflight settled")
	}
}
