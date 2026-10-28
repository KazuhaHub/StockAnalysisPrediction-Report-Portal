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
	"time"

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
func nativeRecoveryEnvelope(t *testing.T, s *Store, slot int) ([]byte, []byte, dataClaimMarker) {
	t.Helper()
	c := nativeJournalCommand(t, slot)
	var raw string
	if err := s.db.QueryRow(`SELECT receipt_json FROM runner_commands WHERE effect_id=?`, c.EffectID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var marker dataClaimMarker
	if !canonicalDataJSON(raw, &marker) || !validDataMarker(marker) {
		t.Fatal("missing recovery claim")
	}
	claimed, _ := strconv.ParseInt(marker.ClaimedMS, 10, 64)
	for time.Now().UnixMilli() <= claimed {
		time.Sleep(time.Millisecond)
	}
	now := time.Now().UnixMilli()
	c.IssuedMS, c.ExpiresMS = strconv.FormatInt(now, 10), strconv.FormatInt(now+29000, 10)
	envelope, sig := signRunnerCommand(t, c)
	return envelope, sig, marker
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
		renewed, sig, _ := nativeRecoveryEnvelope(t, s, 6)
		if got, err := s.ReconcilePrepareData(ctx, renewed, sig, root, expected); !errors.Is(err, data.ErrUnsafe) || got != (DataPreparationReceipt{}) {
			t.Fatal("recovery accepted changed filesystem", got, err)
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
		if err := tx.Rollback(); err != nil {
			t.Fatal(err)
		}
		// Cross the original envelope's expiry on real time before recovering.
		_, _, original := nativeRecoveryEnvelope(t, s, 4)
		expires, _ := strconv.ParseInt(original.ExpiresMS, 10, 64)
		if delay := time.Until(time.UnixMilli(expires + 1)); delay > 0 {
			time.Sleep(delay)
		}
		raw, sig, _ := nativeRecoveryEnvelope(t, s, 4)
		before, err := root.Inspect(ctx, expected, original.ResourceID, 4)
		if err != nil {
			t.Fatal(err)
		}
		type outcome struct {
			receipt DataPreparationReceipt
			err     error
		}
		results := make(chan outcome, 2)
		for range 2 {
			go func() {
				receipt, err := s.ReconcilePrepareData(ctx, raw, sig, root, expected)
				results <- outcome{receipt, err}
			}()
		}
		success := 0
		for range 2 {
			result := <-results
			if result.err == nil {
				success++
				if result.receipt.Format != dataReconciledReceiptFormat || !validDataReceipt(result.receipt) || result.receipt.Claim != original || result.receipt.NamespaceInode != strconv.FormatUint(before.Inode, 10) {
					t.Fatal("real recovery lost original/native identity", result.receipt)
				}
				observed, _ := strconv.ParseInt(result.receipt.ObservedMS, 10, 64)
				if observed < expires {
					t.Fatal("native fixture did not cross original expiry")
				}
			} else if result.receipt != (DataPreparationReceipt{}) {
				t.Fatal("refused recovery returned receipt")
			}
		}
		if success != 1 {
			t.Fatal("concurrent recovery did not settle once", success)
		}
		after, err := root.Inspect(ctx, expected, original.ResourceID, 4)
		if err != nil || before != after {
			t.Fatal("read-only recovery changed native directory", before, after, err)
		}
		if _, err := s.ClaimPrepareData(ctx, raw, sig); err == nil {
			t.Fatal("recovered effect re-claimed")
		}
		// Simulate interruption after mkdir and before quota/project assignment.
		partial := nativeJournalClaim(t, s, 6)
		partialPath := filepath.Join(rootPath, nativeJournalCommand(t, 6).Payload.DataNamespaceID)
		_, err = s.RunPrepareDataOnce(ctx, partial, func(ctx context.Context, _ DataReservation, _ control.PrepareDataPayload) error {
			if err := s.bindDataRoot(ctx, partial.state, expected); err != nil {
				return err
			}
			return os.Mkdir(partialPath, 0700)
		})
		if err != nil {
			t.Fatal(err)
		}
		var partialBefore, partialAfter unix.Stat_t
		if err := unix.Stat(partialPath, &partialBefore); err != nil {
			t.Fatal(err)
		}
		partialRaw, partialSig, _ := nativeRecoveryEnvelope(t, s, 6)
		if got, err := s.DispatchPrepareData(ctx, partialRaw, partialSig, root, expected); err == nil || got != (DataPreparationReceipt{}) {
			t.Fatal("partial namespace repaired by recovery")
		}
		if err := unix.Stat(partialPath, &partialAfter); err != nil || partialBefore.Ino != partialAfter.Ino || partialBefore.Mode != partialAfter.Mode {
			t.Fatal("partial namespace replaced", err)
		}
		entries, err := os.ReadDir(partialPath)
		if err != nil || len(entries) != 0 {
			t.Fatal("partial namespace contents changed", err)
		}
		missing := nativeJournalClaim(t, s, 7)
		_, err = s.RunPrepareDataOnce(ctx, missing, func(ctx context.Context, _ DataReservation, _ control.PrepareDataPayload) error {
			return s.bindDataRoot(ctx, missing.state, expected)
		})
		if err != nil {
			t.Fatal(err)
		}
		missingRaw, missingSig, _ := nativeRecoveryEnvelope(t, s, 7)
		if got, err := s.DispatchPrepareData(ctx, missingRaw, missingSig, root, expected); !errors.Is(err, os.ErrNotExist) || got != (DataPreparationReceipt{}) {
			t.Fatal("absent namespace recreated", err)
		}
		if _, err := os.Lstat(filepath.Join(rootPath, nativeJournalCommand(t, 7).Payload.DataNamespaceID)); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("recovery created absent namespace")
		}
		if scalar(t, s.db, `SELECT COUNT(*) FROM runner_commands WHERE state='applied'`) != 3 || scalar(t, s.db, `SELECT COUNT(*) FROM runner_commands WHERE state='unknown'`) != 3 {
			t.Fatal("recovery changed unrelated/partial states")
		}

		// Simulate native completion followed by process loss before durable
		// settlement. The dispatcher must recover by inspection, never Prepare.
		uncertain := nativeJournalClaim(t, s, 8)
		var completed data.Observation
		_, err = s.RunPrepareDataOnce(ctx, uncertain, func(ctx context.Context, reservation DataReservation, _ control.PrepareDataPayload) error {
			if err := s.bindDataRoot(ctx, uncertain.state, expected); err != nil {
				return err
			}
			var err error
			completed, err = root.Prepare(ctx, expected, data.Request{NamespaceID: reservation.NamespaceID, ProjectID: reservation.ProjectID, Bytes: 8 << 20, Inodes: 128})
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		recoveryRaw, recoverySig, originalMarker := nativeRecoveryEnvelope(t, s, 8)
		recovered, err := s.DispatchPrepareData(ctx, recoveryRaw, recoverySig, root, expected)
		if err != nil || recovered.Format != dataReconciledReceiptFormat || recovered.Claim != originalMarker || recovered.NamespaceInode != strconv.FormatUint(completed.Inode, 10) {
			t.Fatal("dispatcher did not inspect/recover completed unknown work", err)
		}
		repeated, err := s.DispatchPrepareData(ctx, recoveryRaw, recoverySig, root, expected)
		if err != nil || repeated != recovered {
			t.Fatal("dispatcher applied replay changed receipt", err)
		}
		observed, err := root.Inspect(ctx, expected, completed.NamespaceID, completed.ProjectID)
		if err != nil || observed != completed {
			t.Fatal("dispatcher recovery changed native identity", err)
		}
		if scalar(t, s.db, `SELECT COUNT(*) FROM runner_commands WHERE state='applied'`) != 4 || scalar(t, s.db, `SELECT COUNT(*) FROM runner_commands WHERE state='unknown'`) != 3 {
			t.Fatal("dispatch changed unrelated state")
		}

		// Simultaneous first requests may observe prepared, unknown or applied, but
		// only one stable resource/receipt is allowed to emerge.
		concurrentCommand := nativeJournalCommand(t, 9)
		concurrentRaw, concurrentSig := signRunnerCommand(t, concurrentCommand)
		concurrentResults := make(chan outcome, 3)
		for range 3 {
			go func() {
				r, err := s.DispatchPrepareData(ctx, concurrentRaw, concurrentSig, root, expected)
				concurrentResults <- outcome{r, err}
			}()
		}
		successes := 0
		var firstReceipt DataPreparationReceipt
		for range 3 {
			result := <-concurrentResults
			if result.err == nil {
				successes++
				if firstReceipt == (DataPreparationReceipt{}) {
					firstReceipt = result.receipt
				} else if firstReceipt != result.receipt {
					t.Fatal("concurrent dispatch returned distinct receipts")
				}
			} else if result.receipt != (DataPreparationReceipt{}) {
				t.Fatal("failed dispatch returned receipt")
			}
		}
		if successes < 1 {
			t.Fatal("no concurrent dispatch completed")
		}
		retried, err := s.DispatchPrepareData(ctx, concurrentRaw, concurrentSig, root, expected)
		if err != nil || retried != firstReceipt {
			t.Fatal("concurrent dispatch retry changed identity", err)
		}
		if scalar(t, s.db, `SELECT COUNT(*) FROM runner_commands WHERE effect_id='`+concurrentCommand.EffectID+`' AND state='applied'`) != 1 || scalar(t, s.db, `SELECT COUNT(*) FROM runner_resources WHERE id='`+concurrentCommand.Payload.DataNamespaceID+`'`) != 1 {
			t.Fatal("concurrent dispatch created multiple resources")
		}
		exerciseNativeDataWriterIntent(t, s, root, expected, concurrentRaw, concurrentSig, firstReceipt)
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
