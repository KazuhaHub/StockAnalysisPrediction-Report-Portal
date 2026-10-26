//go:build linux && (amd64 || arm64)

package journal

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/data"
	"golang.org/x/sys/unix"
)

func nativeJournalCommand(t *testing.T, slot int) control.Command {
	c := runnerCommandFixture(t)
	c.InstallationID, c.OperationID, c.EffectID, c.Payload.DataNamespaceID = fixtureID(6000+slot*10), fixtureID(6001+slot*10), fixtureID(6002+slot*10), fixtureID(6003+slot*10)
	c.Payload.QuotaBytes, c.Payload.QuotaInodes = "8388608", "128"
	return c
}
func nativeJournalClaim(t *testing.T, s *Store, slot int) DataClaim {
	t.Helper()
	raw, sig := signRunnerCommand(t, nativeJournalCommand(t, slot))
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
	return claim
}
func TestNativeJournalDataExecution(t *testing.T) {
	rootPath, journalPath := os.Getenv("APP_DATA_JOURNAL_ROOT"), os.Getenv("APP_DATA_JOURNAL_CONTROL")
	if rootPath == "" {
		t.Skip("explicit disposable native journal fixture only")
	}
	if os.Getenv("GITHUB_ACTIONS") != "true" || os.Getenv("RUNNER_ENVIRONMENT") != "github-hosted" || os.Geteuid() != 0 || !filepath.IsAbs(rootPath) || !filepath.IsAbs(journalPath) {
		t.Fatal("requires disposable root and explicit private paths")
	}
	inode, err := strconv.ParseUint(os.Getenv("APP_DATA_JOURNAL_INODE"), 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	expected := data.RootIdentity{FilesystemUUID: os.Getenv("APP_DATA_NATIVE_UUID"), Inode: inode}
	input, err := os.Open(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	root, err := data.Open(input)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	s, err := Open(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	switch os.Getenv("APP_DATA_NATIVE_PHASE") {
	case "replaced":
		claim := nativeJournalClaim(t, s, 5)
		if got, err := s.ExecutePrepareData(ctx, claim, root, expected); !errors.Is(err, data.ErrUnsafe) || got != (DataPreparationReceipt{}) {
			t.Fatal("changed filesystem executed", got, err)
		}
		if _, err := os.Lstat(filepath.Join(rootPath, nativeJournalCommand(t, 5).Payload.DataNamespaceID)); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("changed filesystem created namespace")
		}
		return
	case "inspect":
		for _, slot := range []int{1, 3, 4} {
			c := nativeJournalCommand(t, slot)
			observed, err := root.Inspect(ctx, expected, c.Payload.DataNamespaceID, uint32(slot))
			if err != nil || observed.ByteLimit != 8<<20 || observed.InodeLimit != 128 {
				t.Fatal("remount native state", observed, err)
			}
			raw, sig := signRunnerCommand(t, c)
			if _, err := s.ClaimPrepareData(ctx, raw, sig); err == nil {
				t.Fatal("restart re-claimed completed/uncertain native work")
			}
			var stored string
			if err := s.db.QueryRow(`SELECT receipt_json FROM runner_commands WHERE effect_id=?`, c.EffectID).Scan(&stored); err != nil {
				t.Fatal(err)
			}
			if slot != 4 {
				var receipt DataPreparationReceipt
				if !canonicalDataJSON(stored, &receipt) || !validDataReceipt(receipt) || receipt.NamespaceInode != strconv.FormatUint(observed.Inode, 10) || markerRoot(receipt.Claim) != expected {
					t.Fatal("receipt lost native identity after remount")
				}
			}
		}
		if scalar(t, s.db, `SELECT COUNT(*) FROM runner_commands WHERE state='applied'`) != 2 || scalar(t, s.db, `SELECT COUNT(*) FROM runner_commands WHERE state='unknown'`) != 2 {
			t.Fatal("reopen changed settlement states")
		}
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		if err := requireDataRootHistory(ctx, tx, expected); err != nil {
			t.Fatal(err)
		}
		return
	}
	if _, _, err := s.EnrollController(ctx, enrollmentFixture(30)); err != nil {
		t.Fatal(err)
	}
	first := nativeJournalClaim(t, s, 1)
	receipt, err := s.ExecutePrepareData(ctx, first, root, expected)
	if err != nil || !validDataReceipt(receipt) || receipt.ProjectID != "1" || markerRoot(receipt.Claim) != expected {
		t.Fatal("real native settlement", receipt, err)
	}
	var stored string
	if err := s.db.QueryRow(`SELECT receipt_json FROM runner_commands WHERE effect_id=?`, receipt.Claim.EffectID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	var durable DataPreparationReceipt
	if json.Unmarshal([]byte(stored), &durable) != nil || durable != receipt {
		t.Fatal("returned receipt not durable")
	}
	if got, err := s.ExecutePrepareData(ctx, first, root, expected); err == nil || got != (DataPreparationReceipt{}) {
		t.Fatal("native one-shot replay")
	}
	// A matching pin for a different root still cannot replace journal history.
	alternativePath := rootPath + "-alternate"
	if err := os.Mkdir(alternativePath, 0700); err != nil {
		t.Fatal(err)
	}
	alternativeInput, err := os.Open(alternativePath)
	if err != nil {
		t.Fatal(err)
	}
	defer alternativeInput.Close()
	alternative, err := data.Open(alternativeInput)
	if err != nil {
		t.Fatal(err)
	}
	defer alternative.Close()
	var stat unix.Stat_t
	if err := unix.Stat(alternativePath, &stat); err != nil {
		t.Fatal(err)
	}
	alternatePin := data.RootIdentity{FilesystemUUID: expected.FilesystemUUID, Inode: stat.Ino}
	second := nativeJournalClaim(t, s, 2)
	if got, err := s.ExecutePrepareData(ctx, second, alternative, alternatePin); !errors.Is(err, ErrFenceReconciliation) || got != (DataPreparationReceipt{}) {
		t.Fatal("deployment root rebound", got, err)
	}
	if _, err := os.Lstat(filepath.Join(alternativePath, nativeJournalCommand(t, 2).Payload.DataNamespaceID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("root binding refusal mutated alternate namespace")
	}
	third := nativeJournalClaim(t, s, 3)
	if got, err := s.ExecutePrepareData(ctx, third, root, expected); err != nil || got.ProjectID != "3" {
		t.Fatal("same root additional installation", got, err)
	}
	fourth := nativeJournalClaim(t, s, 4)
	if _, err := s.db.Exec(`CREATE TRIGGER fixture_native_settlement_failure BEFORE UPDATE ON runner_commands WHEN NEW.state='applied' BEGIN SELECT RAISE(ABORT,'fixture receipt commit failure');END`); err != nil {
		t.Fatal(err)
	}
	if got, err := s.ExecutePrepareData(ctx, fourth, root, expected); err == nil || got != (DataPreparationReceipt{}) {
		t.Fatal("persistence failure returned success")
	}
	if _, err := s.db.Exec(`DROP TRIGGER fixture_native_settlement_failure`); err != nil {
		t.Fatal(err)
	}
	if _, err := root.Inspect(ctx, expected, nativeJournalCommand(t, 4).Payload.DataNamespaceID, 4); err != nil {
		t.Fatal("fixture did not perform native work before persistence failure", err)
	}
	if got, err := s.ExecutePrepareData(ctx, fourth, root, expected); err == nil || got != (DataPreparationReceipt{}) {
		t.Fatal("uncertain physical effect replayed")
	}
	if scalar(t, s.db, `SELECT COUNT(*) FROM runner_commands WHERE state='applied' AND ack_attempted_ms IS NULL`) != 2 || scalar(t, s.db, `SELECT COUNT(*) FROM runner_resources WHERE observed_state='unknown' AND observed_ms IS NULL AND revision=1`) != 2 {
		t.Fatal("native failure committed partial receipt/resource")
	}
}
