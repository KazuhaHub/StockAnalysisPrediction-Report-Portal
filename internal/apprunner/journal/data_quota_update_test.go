//go:build linux || darwin

package journal

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/data"
)

// An approved update raises the data quota (doc 185 section 3.2) with the same set-data-quota command a reviewed
// raise uses, under the update's own operation: slot prepare_data/1, which an update never uses for anything else.
// These tests state what the runner does with such a raise, on the fixtures of an update whose checkpoint is applied.

// resignUpdateStart signs an update-start again after the test changed it.
func resignUpdateStart(t *testing.T, command control.UpdateStartCommand) ([]byte, []byte) {
	t.Helper()
	digest, err := control.UpdateStartLogicalDigest(command)
	if err != nil {
		t.Fatal(err)
	}
	command.PayloadSHA256 = digest
	now := time.Now().UTC().UnixMilli()
	command.IssuedMS, command.ExpiresMS = strconv.FormatInt(now, 10), strconv.FormatInt(now+25000, 10)
	authority, _ := dataCommandAuthority()
	raw, signature, err := control.SignUpdateStart(command, ed25519.NewKeyFromSeed(bytes.Repeat([]byte{30}, 32)), authority, now)
	if err != nil {
		t.Fatal(err)
	}
	return raw, signature
}

// J1: a raise under the update's operation, after its checkpoint is applied, is claimed (no unsettled work refuses
// it) and extends the quota chain.
func TestSetDataQuotaUnderAnUpdateOperationExtendsTheChain(t *testing.T) {
	ctx := context.Background()
	f, checkpoint := appliedCheckpointFixture(t)
	fixtureVolumePresent(t, f)
	oldBytes, oldInodes := mustDataInt(f.data.QuotaBytes), mustDataInt(f.data.QuotaInodes)
	command, raw, sig := quotaRaiseUnder(t, f, checkpoint.OperationID, fixtureID(7201), oldBytes, oldInodes, oldBytes*4, oldInodes*2)
	calls := 0
	receipt, err := f.store.DispatchSetDataQuota(ctx, raw, sig, raiseNative(command, &calls))
	if err != nil || calls != 1 || receipt.OperationID != checkpoint.OperationID || receipt.State != "raised" {
		t.Fatalf("a raise under the update's operation: %+v calls=%d %v", receipt, calls, err)
	}
	tx, err := f.store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	bytes, inodes, err := currentQuota(ctx, tx, f.command.Payload.NamespaceID, f.data.QuotaBytes, f.data.QuotaInodes)
	if err != nil || bytes != strconv.FormatInt(oldBytes*4, 10) || inodes != strconv.FormatInt(oldInodes*2, 10) {
		t.Fatalf("the chain after the raise: %s %s %v", bytes, inodes, err)
	}
}

// startRequestAfterRaise raises the quota of an update fixture, then reserves, claims and builds the native request of
// a start whose command carries the quota the test names (the raised one, or the install's). The runner holds the
// command's quota against the namespace's current limits when it builds that request.
func startRequestAfterRaise(t *testing.T, kind string, carryRaised bool) error {
	t.Helper()
	ctx := context.Background()
	f, _, start, _, _ := updateStartJournalFixture(t)
	fixtureVolumePresent(t, f)
	oldBytes, oldInodes := mustDataInt(f.data.QuotaBytes), mustDataInt(f.data.QuotaInodes)
	command, raw, sig := quotaRaiseUnder(t, f, start.OperationID, fixtureID(7211), oldBytes, oldInodes, oldBytes*2, oldInodes*2)
	if _, err := f.store.DispatchSetDataQuota(ctx, raw, sig, raiseNative(command, new(int))); err != nil {
		t.Fatal(err)
	}
	if kind != "update-start" {
		start.Kind, start.OperationID = kind, fixtureID(7212)
		start.Payload.TargetReleaseSHA256 = start.Payload.SourceReleaseSHA256
	}
	if carryRaised {
		start.Payload.QuotaBytes, start.Payload.QuotaInodes = strconv.FormatInt(oldBytes*2, 10), strconv.FormatInt(oldInodes*2, 10)
	}
	startRaw, startSig := resignUpdateStart(t, start)
	if _, err := f.store.ReserveUpdateStart(ctx, startRaw, startSig); err != nil {
		return err
	}
	if _, err := f.store.ClaimUpdateStart(ctx, startRaw, startSig); err != nil {
		return err
	}
	tx, facts, err := f.store.beginUpdateStartExecution(ctx, startRaw, startSig, "unknown")
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var marker UpdateStartClaimMarker
	var markerRaw string
	if err := tx.QueryRowContext(ctx, `SELECT receipt_json FROM runner_commands WHERE effect_id=?`, start.EffectID).Scan(&markerRaw); err != nil || !canonicalDataJSON(markerRaw, &marker) {
		t.Fatal("claim marker", err)
	}
	_, err = updateStartNativeRequest(facts, marker, nil)
	return err
}

// J2: after the raise, an update-start over the old quota is refused and one over the new quota is accepted.
func TestUpdateStartAfterAnUpdateRaiseRefusesTheOldQuotaAndAcceptsTheNew(t *testing.T) {
	if err := startRequestAfterRaise(t, "update-start", false); !errors.Is(err, ErrFenceReconciliation) {
		t.Fatalf("an update-start over the old quota was accepted after the raise: %v", err)
	}
	if err := startRequestAfterRaise(t, "update-start", true); err != nil {
		t.Fatalf("an update-start over the raised quota was refused: %v", err)
	}
}

// migrateAfterRaise raises the quota of an update fixture and sends its migrate-data with the quota the test names.
func migrateAfterRaise(t *testing.T, carryRaised bool) (MigrateDataReceipt, string, error) {
	t.Helper()
	ctx := context.Background()
	m := migrateDataJournalFixture(t)
	fixtureVolumePresent(t, m.f)
	oldBytes, oldInodes := mustDataInt(m.f.data.QuotaBytes), mustDataInt(m.f.data.QuotaInodes)
	command, raw, sig := quotaRaiseUnder(t, m.f, m.checkpoint.OperationID, fixtureID(7221), oldBytes, oldInodes, oldBytes*2, oldInodes)
	if _, err := m.f.store.DispatchSetDataQuota(ctx, raw, sig, raiseNative(command, new(int))); err != nil {
		t.Fatal(err)
	}
	// The job's own check reads the live limit, which is the raised one.
	m.native.marker.ByteLimit, m.native.marker.InodeLimit = oldBytes*2, oldInodes
	migrateRaw, migrateSig := m.resign(t, func(c *control.MigrateDataCommand) {
		if carryRaised {
			c.Payload.QuotaBytes = strconv.FormatInt(oldBytes*2, 10)
		}
	})
	receipt, err := m.f.store.DispatchMigrateData(ctx, migrateRaw, migrateSig, m.descriptor, m.native, importDataPin)
	return receipt, rowState(t, m.f.store, m.command.EffectID), err
}

// J3: migrate-data after the raise carries the new quota. The new one passes the marker check against the raised
// limit; the old one no longer describes the namespace and is refused.
func TestMigrateDataAfterAnUpdateRaiseNeedsTheRaisedQuota(t *testing.T) {
	receipt, state, err := migrateAfterRaise(t, true)
	if err != nil || receipt.State != "migrated" || state != "applied" {
		t.Fatalf("a migration over the raised quota was refused: %+v %s %v", receipt, state, err)
	}
	receipt, state, err = migrateAfterRaise(t, false)
	if (err == nil && receipt.State == "migrated") || state == "applied" {
		t.Fatalf("a migration over the old quota was applied after the raise: %+v %s %v", receipt, state, err)
	}
}

// J4: an unknown raise blocks the checkpoint, update-start and restore of the installation until it is finished, and
// the same command, sent again, finishes it under its own claim.
func TestUnknownRaiseBlocksTheInstallationUntilTheSameCommandFinishesIt(t *testing.T) {
	ctx := context.Background()
	f, _, start, _, _ := updateStartJournalFixture(t)
	fixtureVolumePresent(t, f)
	oldBytes, oldInodes := mustDataInt(f.data.QuotaBytes), mustDataInt(f.data.QuotaInodes)
	command, raw, sig := quotaRaiseUnder(t, f, start.OperationID, fixtureID(7231), oldBytes, oldInodes, oldBytes*2, oldInodes*2)
	incomplete := func(context.Context, data.QuotaRaiseRequest) (data.QuotaRaiseSnapshot, error) {
		return data.QuotaRaiseSnapshot{}, data.ErrQuotaRaiseIncomplete
	}
	if _, err := f.store.DispatchSetDataQuota(ctx, raw, sig, incomplete); err == nil {
		t.Fatal("an incomplete raise settled")
	}
	if got := rowState(t, f.store, command.EffectID); got != "unknown" {
		t.Fatalf("an incomplete raise is %s", got)
	}
	// Update-start of the installation is refused while the raise has no verdict.
	raised := start
	raised.Payload.QuotaBytes, raised.Payload.QuotaInodes = strconv.FormatInt(oldBytes*2, 10), strconv.FormatInt(oldInodes*2, 10)
	startRaw, startSig := resignUpdateStart(t, raised)
	if _, err := f.store.ReserveUpdateStart(ctx, startRaw, startSig); err == nil {
		t.Fatal("an update-start was reserved beside an unknown raise")
	}
	restore := raised
	restore.Kind, restore.OperationID, restore.EffectID = "restore-start", fixtureID(7232), fixtureID(7233)
	restore.Payload.TargetReleaseSHA256 = restore.Payload.SourceReleaseSHA256
	restoreRaw, restoreSig := resignUpdateStart(t, restore)
	if _, err := f.store.ReserveUpdateStart(ctx, restoreRaw, restoreSig); err == nil {
		t.Fatal("a restore-start was reserved beside an unknown raise")
	}
	calls := 0
	if _, err := f.store.DispatchSetDataQuota(ctx, raw, sig, raiseNative(command, &calls)); err != nil || calls != 1 {
		t.Fatalf("the same command did not finish the unknown raise: %v calls=%d", err, calls)
	}
	if scalarJournalInt(t, f.store.db, `SELECT COUNT(*) FROM runner_commands WHERE kind='set-data-quota'`) != 1 {
		t.Fatal("finishing the raise claimed it again")
	}
	if _, err := f.store.ReserveUpdateStart(ctx, startRaw, startSig); err != nil {
		t.Fatalf("the update-start is still refused after the raise finished: %v", err)
	}
}

// J5: a raise the native layer refused ends failed, gives both reservation rows back, and does not block the pre-update
// checkpoint's restore (a reverted row is not unsettled work).
func TestFailedRaiseRestoresTheReservationsAndBlocksNothing(t *testing.T) {
	ctx := context.Background()
	f, _, start, _, _ := updateStartJournalFixture(t)
	fixtureVolumePresent(t, f)
	oldBytes, oldInodes := mustDataInt(f.data.QuotaBytes), mustDataInt(f.data.QuotaInodes)
	reserved := func() int64 {
		return scalarJournalInt(t, f.store.db, `SELECT COALESCE(SUM(disk_bytes),0) FROM runner_reservations WHERE installation_id=?`, f.command.InstallationID)
	}
	before := reserved()
	command, raw, sig := quotaRaiseUnder(t, f, start.OperationID, fixtureID(7241), oldBytes, oldInodes, oldBytes*2, oldInodes)
	unsafe := func(context.Context, data.QuotaRaiseRequest) (data.QuotaRaiseSnapshot, error) {
		return data.QuotaRaiseSnapshot{}, data.ErrQuotaRaiseUnsafe
	}
	_, err := f.store.DispatchSetDataQuota(ctx, raw, sig, unsafe)
	var failed *DataEffectFailedError
	if !errors.As(err, &failed) || failed.Code != "quota_raise_unsafe" {
		t.Fatalf("a refused raise did not end failed: %v", err)
	}
	if rowState(t, f.store, command.EffectID) != "reverted" {
		t.Fatal("a refused raise is not reverted")
	}
	if got := reserved(); got != before {
		t.Fatalf("the reservations were not given back: %d, want %d", got, before)
	}
	if _, err := f.store.DispatchSetDataQuota(ctx, raw, sig, raiseNative(command, new(int))); err == nil {
		t.Fatal("a failed raise ran its native call again")
	}
	startRaw, startSig := resignUpdateStart(t, start)
	if _, err := f.store.ReserveUpdateStart(ctx, startRaw, startSig); err != nil {
		t.Fatalf("a reverted raise blocks the restore path: %v", err)
	}
}

// J6: a restore-start after a raised and rolled-back update carries the raised quota (the limits stay), and the old
// pair is still a link of the chain, so a start recorded under it stays readable.
func TestRestoreStartAfterAnUpdateRaiseRequiresTheRaisedQuota(t *testing.T) {
	if err := startRequestAfterRaise(t, "restore-start", false); !errors.Is(err, ErrFenceReconciliation) {
		t.Fatalf("a restore-start over the old quota was accepted after the raise: %v", err)
	}
	if err := startRequestAfterRaise(t, "restore-start", true); err != nil {
		t.Fatalf("a restore-start over the raised quota was refused: %v", err)
	}
	ctx := context.Background()
	f, _, start, _, _ := updateStartJournalFixture(t)
	fixtureVolumePresent(t, f)
	oldBytes, oldInodes := mustDataInt(f.data.QuotaBytes), mustDataInt(f.data.QuotaInodes)
	command, raw, sig := quotaRaiseUnder(t, f, start.OperationID, fixtureID(7251), oldBytes, oldInodes, oldBytes*2, oldInodes*2)
	if _, err := f.store.DispatchSetDataQuota(ctx, raw, sig, raiseNative(command, new(int))); err != nil {
		t.Fatal(err)
	}
	tx, err := f.store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	chain, err := loadQuotaChain(ctx, tx, f.command.Payload.NamespaceID, f.data.QuotaBytes, f.data.QuotaInodes)
	if err != nil || !quotaInChain(chain, f.data.QuotaBytes, f.data.QuotaInodes) ||
		!quotaInChain(chain, strconv.FormatInt(oldBytes*2, 10), strconv.FormatInt(oldInodes*2, 10)) {
		t.Fatalf("both the install's and the raised limits are links: %+v %v", chain, err)
	}
}

// J7: a second raise in the same (operation, prepare_data, 1) slot under another effect id is a conflict: the
// runner's journal keeps one row per slot, and an update raises at most once.
func TestSecondRaiseInTheSameSlotIsACommandConflict(t *testing.T) {
	ctx := context.Background()
	f, _, start, _, _ := updateStartJournalFixture(t)
	fixtureVolumePresent(t, f)
	oldBytes, oldInodes := mustDataInt(f.data.QuotaBytes), mustDataInt(f.data.QuotaInodes)
	first, raw, sig := quotaRaiseUnder(t, f, start.OperationID, fixtureID(7261), oldBytes, oldInodes, oldBytes*2, oldInodes)
	if _, err := f.store.DispatchSetDataQuota(ctx, raw, sig, raiseNative(first, new(int))); err != nil {
		t.Fatal(err)
	}
	second, secondRaw, secondSig := quotaRaiseUnder(t, f, start.OperationID, fixtureID(7262), oldBytes*2, oldInodes, oldBytes*3, oldInodes)
	calls := 0
	if _, err := f.store.DispatchSetDataQuota(ctx, secondRaw, secondSig, raiseNative(second, &calls)); !errors.Is(err, ErrCommandConflict) || calls != 0 {
		t.Fatalf("a second raise in the slot under another effect: %v calls=%d", err, calls)
	}
}
