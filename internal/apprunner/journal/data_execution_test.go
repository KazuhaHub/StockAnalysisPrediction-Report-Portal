//go:build linux || darwin

package journal

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/data"
)

func TestNativeDataExecutionCannotInventCompletion(t *testing.T) {
	s, _, raw, sig := reservedClaimFixture(t)
	claim, err := s.ClaimPrepareData(context.Background(), raw, sig)
	if err != nil {
		t.Fatal(err)
	}
	pin := data.RootIdentity{FilesystemUUID: fixtureID(900), Inode: 10}
	for _, root := range []*data.Root{nil, {}} {
		got, err := s.ExecutePrepareData(context.Background(), claim, root, pin)
		if err == nil || got != (DataPreparationReceipt{}) {
			t.Fatal("invalid native provider fabricated receipt", got, err)
		}
	}
	if scalar(t, s.db, `SELECT COUNT(*) FROM runner_commands WHERE state='unknown'`) != 1 || scalar(t, s.db, `SELECT COUNT(*) FROM runner_resources WHERE observed_state='unknown' AND observed_ms IS NULL`) != 1 {
		t.Fatal("invalid provider changed physical state")
	}
}
func TestDataRootBindingDurableAndImmutable(t *testing.T) {
	s, directory, raw, sig := reservedClaimFixture(t)
	claim, err := s.ClaimPrepareData(context.Background(), raw, sig)
	if err != nil {
		t.Fatal(err)
	}
	pin := data.RootIdentity{FilesystemUUID: fixtureID(900), Inode: 10}
	if err := s.bindDataRoot(context.Background(), claim.state, pin); err != nil {
		t.Fatal(err)
	}
	var marker dataClaimMarker
	if json.Unmarshal([]byte(claim.state.marker), &marker) != nil || marker.RootFilesystemUUID != pin.FilesystemUUID || marker.RootInode != "10" {
		t.Fatal("physical root not recorded before mutation")
	}
	if err := s.bindDataRoot(context.Background(), claim.state, data.RootIdentity{FilesystemUUID: pin.FilesystemUUID, Inode: 11}); !errors.Is(err, ErrFenceReconciliation) {
		t.Fatal("rebound data root", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	tx, err := reopened.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := requireDataRootHistory(context.Background(), tx, pin); err != nil {
		t.Fatal("lost durable root binding", err)
	}
	if err := requireDataRootHistory(context.Background(), tx, data.RootIdentity{FilesystemUUID: fixtureID(901), Inode: 10}); !errors.Is(err, ErrFenceReconciliation) {
		t.Fatal("changed filesystem allowed after reopen", err)
	}
}

func syntheticDataObservation(r DataReservation) data.Observation {
	return data.Observation{NamespaceID: r.NamespaceID, ProjectID: r.ProjectID, Device: 7, Inode: 12, ByteLimit: 268435456, InodeLimit: 65536, UsedBytes: 0, UsedInodes: 1, Inherited: true}
}
func TestDataSettlementAtomicHistoricalReceipt(t *testing.T) {
	s, directory, raw, sig := reservedClaimFixture(t)
	claim, err := s.ClaimPrepareData(context.Background(), raw, sig)
	if err != nil {
		t.Fatal(err)
	}
	pin := data.RootIdentity{FilesystemUUID: fixtureID(900), Inode: 10}
	var receipt DataPreparationReceipt
	_, err = s.RunPrepareDataOnce(context.Background(), claim, func(ctx context.Context, r DataReservation, _ control.PrepareDataPayload) error {
		if err := s.bindDataRoot(ctx, claim.state, pin); err != nil {
			return err
		}
		var err error
		receipt, err = s.settlePreparedData(ctx, claim.state, pin, syntheticDataObservation(r))
		return err
	})
	if err != nil || !validDataReceipt(receipt) || receipt.ProjectID != "1" || receipt.NamespaceInode != "12" {
		t.Fatal("typed settlement", receipt, err)
	}
	if scalar(t, s.db, `SELECT COUNT(*) FROM runner_resources WHERE observed_state='present' AND revision=2 AND observed_ms IS NOT NULL AND writer_fence=1 AND control_fence=1`) != 1 || scalar(t, s.db, `SELECT COUNT(*) FROM runner_commands WHERE state='applied' AND ack_attempted_ms IS NULL`) != 1 {
		t.Fatal("settlement not atomic or granted writer/ack")
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
	var got DataPreparationReceipt
	if !canonicalDataJSON(stored, &got) || got != receipt {
		t.Fatal("restart lost receipt")
	}
	if result, err := reopened.ClaimPrepareData(context.Background(), raw, sig); err == nil || result != (DataClaim{}) {
		t.Fatal("applied native work re-claimed")
	}
	tx, err := reopened.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := requireDataRootHistory(context.Background(), tx, pin); err != nil {
		t.Fatal(err)
	}
	if err := requireDataRootHistory(context.Background(), tx, data.RootIdentity{FilesystemUUID: fixtureID(901), Inode: 10}); err == nil {
		t.Fatal("receipt lost root binding")
	}
}
func TestDataBindingAndSettlementRollback(t *testing.T) {
	for _, phase := range []string{"bind", "settle_command", "settle_resource"} {
		t.Run(phase, func(t *testing.T) {
			s, _, raw, sig := reservedClaimFixture(t)
			claim, err := s.ClaimPrepareData(context.Background(), raw, sig)
			if err != nil {
				t.Fatal(err)
			}
			initial := claim.state.marker
			pin := data.RootIdentity{FilesystemUUID: fixtureID(900), Inode: 10}
			statement := `CREATE TRIGGER fixture_native_failure BEFORE UPDATE ON runner_commands WHEN NEW.state='applied' BEGIN SELECT RAISE(ABORT,'fixture settle failure');END`
			if phase == "bind" {
				statement = `CREATE TRIGGER fixture_native_failure BEFORE UPDATE ON runner_commands BEGIN SELECT RAISE(ABORT,'fixture bind failure');END`
			}
			if phase == "settle_resource" {
				statement = `CREATE TRIGGER fixture_native_failure BEFORE UPDATE ON runner_resources BEGIN SELECT RAISE(ABORT,'fixture resource failure');END`
			}
			if _, err := s.db.Exec(statement); err != nil {
				t.Fatal(err)
			}
			_, err = s.RunPrepareDataOnce(context.Background(), claim, func(ctx context.Context, r DataReservation, _ control.PrepareDataPayload) error {
				if err := s.bindDataRoot(ctx, claim.state, pin); err != nil {
					return err
				}
				_, err := s.settlePreparedData(ctx, claim.state, pin, syntheticDataObservation(r))
				return err
			})
			if err == nil {
				t.Fatal("failed persistence accepted")
			}
			if phase == "bind" && claim.state.marker != initial {
				t.Fatal("failed binding changed live marker")
			}
			if scalar(t, s.db, `SELECT COUNT(*) FROM runner_commands WHERE state='unknown' AND ack_attempted_ms IS NULL`) != 1 || scalar(t, s.db, `SELECT COUNT(*) FROM runner_resources WHERE observed_state='unknown' AND revision=1 AND observed_ms IS NULL`) != 1 {
				t.Fatal("failed settlement partly committed")
			}
			if _, err := s.db.Exec(`DROP TRIGGER fixture_native_failure`); err != nil {
				t.Fatal(err)
			}
			if attempt, err := s.RunPrepareDataOnce(context.Background(), claim, func(context.Context, DataReservation, control.PrepareDataPayload) error {
				t.Fatal("failed native attempt replayed")
				return nil
			}); err == nil || attempt.Invoked {
				t.Fatal("failure reset live claim")
			}
		})
	}
}
func TestDataSettlementRejectsAlteredObservation(t *testing.T) {
	for _, field := range []string{"namespace", "project", "quota", "inode", "inherit", "usage", "root"} {
		t.Run(field, func(t *testing.T) {
			s, _, raw, sig := reservedClaimFixture(t)
			claim, err := s.ClaimPrepareData(context.Background(), raw, sig)
			if err != nil {
				t.Fatal(err)
			}
			pin := data.RootIdentity{FilesystemUUID: fixtureID(900), Inode: 10}
			_, err = s.RunPrepareDataOnce(context.Background(), claim, func(ctx context.Context, r DataReservation, _ control.PrepareDataPayload) error {
				if err := s.bindDataRoot(ctx, claim.state, pin); err != nil {
					return err
				}
				observation := syntheticDataObservation(r)
				switch field {
				case "namespace":
					observation.NamespaceID = fixtureID(902)
				case "project":
					observation.ProjectID++
				case "quota":
					observation.ByteLimit += 512
				case "inode":
					observation.Inode = 0
				case "inherit":
					observation.Inherited = false
				case "usage":
					observation.UsedInodes = -1
				case "root":
					pin.Inode++
				}
				result, err := s.settlePreparedData(ctx, claim.state, pin, observation)
				if result != (DataPreparationReceipt{}) {
					t.Fatal("refusal returned receipt")
				}
				return err
			})
			if err == nil || scalar(t, s.db, `SELECT COUNT(*) FROM runner_commands WHERE state='unknown'`) != 1 || scalar(t, s.db, `SELECT COUNT(*) FROM runner_resources WHERE observed_ms IS NULL`) != 1 {
				t.Fatal("altered observation accepted", err)
			}
		})
	}
}

func TestDataRootHistoryRefusesMalformedOrMissingBinding(t *testing.T) {
	for _, mutation := range []string{"missing", "mixed_root", "unknown_format", "duplicate", "wrong_effect", "wrong_fence", "unknown_field", "applied_without_receipt"} {
		t.Run(mutation, func(t *testing.T) {
			s, _, raw, sig := reservedClaimFixture(t)
			claim, err := s.ClaimPrepareData(context.Background(), raw, sig)
			if err != nil {
				t.Fatal(err)
			}
			pin := data.RootIdentity{FilesystemUUID: fixtureID(900), Inode: 10}
			if err := s.bindDataRoot(context.Background(), claim.state, pin); err != nil {
				t.Fatal(err)
			}
			var marker dataClaimMarker
			if json.Unmarshal([]byte(claim.state.marker), &marker) != nil {
				t.Fatal("fixture marker")
			}
			encoded := claim.state.marker
			switch mutation {
			case "missing":
				encoded = "{}"
			case "mixed_root":
				marker.RootInode = ""
				raw, _ := json.Marshal(marker)
				encoded = string(raw)
			case "unknown_format":
				marker.Format = "future"
				raw, _ := json.Marshal(marker)
				encoded = string(raw)
			case "duplicate":
				encoded = strings.Replace(encoded, `"root_inode":"10"`, `"root_inode":"11","root_inode":"10"`, 1)
			case "wrong_effect":
				marker.EffectID = fixtureID(903)
				raw, _ := json.Marshal(marker)
				encoded = string(raw)
			case "wrong_fence":
				marker.AdmittedFence = "2"
				raw, _ := json.Marshal(marker)
				encoded = string(raw)
			case "unknown_field":
				encoded = strings.TrimSuffix(encoded, "}") + `,"unrecognized":true}`
			case "applied_without_receipt":
				if _, err := s.db.Exec(`UPDATE runner_commands SET state='applied'`); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := s.db.Exec(`UPDATE runner_commands SET receipt_json=?`, encoded); err != nil {
				t.Fatal(err)
			}
			tx, err := s.db.BeginTx(context.Background(), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			if err := requireDataRootHistory(context.Background(), tx, pin); !errors.Is(err, ErrFenceReconciliation) {
				t.Fatal("malformed history ignored", err)
			}
		})
	}
}
func TestDataSettlementRechecksCurrentDurableAuthority(t *testing.T) {
	for _, mutation := range []string{"controller", "fence", "payload", "writer", "resource_state", "ack", "receipt"} {
		t.Run(mutation, func(t *testing.T) {
			s, _, raw, sig := reservedClaimFixture(t)
			claim, err := s.ClaimPrepareData(context.Background(), raw, sig)
			if err != nil {
				t.Fatal(err)
			}
			pin := data.RootIdentity{FilesystemUUID: fixtureID(900), Inode: 10}
			_, err = s.RunPrepareDataOnce(context.Background(), claim, func(ctx context.Context, r DataReservation, _ control.PrepareDataPayload) error {
				if err := s.bindDataRoot(ctx, claim.state, pin); err != nil {
					return err
				}
				statement := map[string]string{
					"controller":     `UPDATE runner_deployment SET control_revision=control_revision+1`,
					"fence":          `UPDATE runner_resources SET control_fence=control_fence+1`,
					"payload":        `UPDATE runner_commands SET payload_sha256='bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb'`,
					"writer":         `UPDATE runner_resources SET writer_fence=writer_fence+1`,
					"resource_state": `UPDATE runner_resources SET desired_state='absent'`,
					"ack":            `UPDATE runner_commands SET ack_attempted_ms=updated_ms`,
					"receipt":        `UPDATE runner_commands SET receipt_json='{}'`,
				}[mutation]
				if _, err := s.db.Exec(statement); err != nil {
					t.Fatal(err)
				}
				got, err := s.settlePreparedData(ctx, claim.state, pin, syntheticDataObservation(r))
				if got != (DataPreparationReceipt{}) {
					t.Fatal("changed authority returned receipt")
				}
				return err
			})
			if err == nil || scalar(t, s.db, `SELECT COUNT(*) FROM runner_commands WHERE state='applied'`) != 0 || scalar(t, s.db, `SELECT COUNT(*) FROM runner_resources WHERE observed_state='present' OR observed_ms IS NOT NULL`) != 0 {
				t.Fatal("stale authority settled", err)
			}
		})
	}
}
func TestDataSettlementDatabaseWaitCancellation(t *testing.T) {
	s, _, raw, sig := reservedClaimFixture(t)
	claim, err := s.ClaimPrepareData(context.Background(), raw, sig)
	if err != nil {
		t.Fatal(err)
	}
	pin := data.RootIdentity{FilesystemUUID: fixtureID(900), Inode: 10}
	_, err = s.RunPrepareDataOnce(context.Background(), claim, func(ctx context.Context, r DataReservation, _ control.PrepareDataPayload) error {
		if err := s.bindDataRoot(ctx, claim.state, pin); err != nil {
			return err
		}
		held, err := s.db.Conn(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer held.Close()
		bounded, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
		defer cancel()
		result, err := s.settlePreparedData(bounded, claim.state, pin, syntheticDataObservation(r))
		if !errors.Is(err, context.DeadlineExceeded) || result != (DataPreparationReceipt{}) {
			t.Fatal("database wait ignored cancellation", err)
		}
		return err
	})
	if err == nil || scalar(t, s.db, `SELECT COUNT(*) FROM runner_commands WHERE state='unknown'`) != 1 {
		t.Fatal("canceled settlement changed state")
	}
}
