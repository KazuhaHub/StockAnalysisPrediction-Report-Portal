//go:build linux || darwin

package journal

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/data"
)

func dataCommandAuthority() (control.Authority, ed25519.PrivateKey) {
	enrollment := enrollmentFixture(30)
	return control.Authority{
		DeploymentID: enrollment.DeploymentID, ControllerEpoch: enrollment.ControllerEpoch,
		ControlRevision: enrollment.ControlRevision, PublicKey: enrollment.PublicKey, Fingerprint: enrollment.Fingerprint,
	}, ed25519.NewKeyFromSeed(bytes.Repeat([]byte{30}, 32))
}

// quotaRaise builds a signed raise over the fixture's namespace from the given old limits.
func quotaRaise(t *testing.T, f checkpointFixture, n int, oldBytes, oldInodes, newBytes, newInodes int64) (control.SetDataQuotaCommand, []byte, []byte) {
	t.Helper()
	return quotaRaiseUnder(t, f, fixtureID(7000+n), fixtureID(7100+n), oldBytes, oldInodes, newBytes, newInodes)
}

// quotaRaiseUnder is quotaRaise under the given operation and effect, so a raise can belong to an operation that is
// also an update with a checkpoint of its own.
func quotaRaiseUnder(t *testing.T, f checkpointFixture, operationID, effectID string, oldBytes, oldInodes, newBytes, newInodes int64) (control.SetDataQuotaCommand, []byte, []byte) {
	t.Helper()
	authority, key := dataCommandAuthority()
	now := time.Now().UTC().UnixMilli()
	cp := f.command.Payload
	var fence int64
	if err := f.store.db.QueryRow(`SELECT control_fence FROM runner_resources WHERE id=? AND kind='volume'`, cp.NamespaceID).Scan(&fence); err != nil {
		t.Fatal(err)
	}
	command := control.SetDataQuotaCommand{CommandHeader: control.CommandHeader{
		Format: control.SetDataQuotaFormat, DeploymentID: f.command.DeploymentID, ControllerEpoch: f.command.ControllerEpoch,
		ControlRevision: "3", InstallationID: f.command.InstallationID, OperationID: operationID,
		TargetGeneration: f.command.TargetGeneration, Stage: control.SetDataQuotaStage, EffectNumber: "1",
		EffectID: effectID, Kind: control.SetDataQuotaKind, InstallationFence: strconv.FormatInt(fence, 10),
	}, Payload: control.SetDataQuotaPayload{
		NamespaceID: cp.NamespaceID, ReleaseSHA256: cp.ReleaseSHA256, DataFormat: cp.DataFormat, WriterFence: cp.WriterFence,
		ProjectID: cp.ProjectID, NamespaceInode: cp.NamespaceInode, Device: cp.Device,
		DataEffectID: cp.SourceDataEffectID, DataReceiptSHA256: cp.SourceDataReceiptSHA256,
		OldBytes: strconv.FormatInt(oldBytes, 10), OldInodes: strconv.FormatInt(oldInodes, 10),
		NewBytes: strconv.FormatInt(newBytes, 10), NewInodes: strconv.FormatInt(newInodes, 10),
	}}
	digest, err := control.SetDataQuotaLogicalDigest(command)
	if err != nil {
		t.Fatal(err)
	}
	command.PayloadSHA256 = digest
	command.IssuedMS, command.ExpiresMS = strconv.FormatInt(now, 10), strconv.FormatInt(now+25000, 10)
	raw, signature, err := control.SignSetDataQuota(command, key, authority, now)
	if err != nil {
		t.Fatal(err)
	}
	return command, raw, signature
}

func raiseNative(command control.SetDataQuotaCommand, calls *int) SetDataQuotaNative {
	return func(ctx context.Context, request data.QuotaRaiseRequest) (data.QuotaRaiseSnapshot, error) {
		*calls++
		return data.QuotaRaiseSnapshot{
			NamespaceID: request.NamespaceID, ProjectID: request.ProjectID, ByteLimit: request.NewBytes, InodeLimit: request.NewInodes,
			UsedBytes: 4096, UsedInodes: 3, ObservedMS: time.Now().UTC().UnixMilli(),
			NamespaceDevice: request.ExpectedDevice, NamespaceInode: request.ExpectedNamespaceInode,
		}, nil
	}
}

func fixtureVolumePresent(t *testing.T, f checkpointFixture) {
	t.Helper()
	mustJournalExec(t, f.store.db, `UPDATE runner_resources SET observed_state='present' WHERE id=? AND kind='volume'`, f.command.Payload.NamespaceID)
}

func TestSetDataQuotaRaisesOnceAndExtendsTheQuotaChain(t *testing.T) {
	ctx := context.Background()
	f, _ := appliedCheckpointFixture(t)
	fixtureVolumePresent(t, f)
	oldBytes, oldInodes := mustDataInt(f.data.QuotaBytes), mustDataInt(f.data.QuotaInodes)
	calls := 0
	first, raw, sig := quotaRaise(t, f, 1, oldBytes, oldInodes, oldBytes*2, oldInodes*2)
	receipt, err := f.store.DispatchSetDataQuota(ctx, raw, sig, raiseNative(first, &calls))
	if err != nil || calls != 1 || receipt.State != "raised" || receipt.NewBytes != strconv.FormatInt(oldBytes*2, 10) {
		t.Fatalf("raise: %+v calls=%d %v", receipt, calls, err)
	}
	// A replay returns the receipt without touching the native layer.
	again, err := f.store.DispatchSetDataQuota(ctx, raw, sig, raiseNative(first, &calls))
	if err != nil || calls != 1 || again != receipt {
		t.Fatalf("replay: %+v calls=%d %v", again, calls, err)
	}
	if got, err := f.store.ReadSetDataQuotaReceipt(ctx, raw, sig); err == nil || got != (SetDataQuotaReceipt{}) {
		// the execute signature is not a read signature
		t.Fatalf("an execute signature was accepted as a read: %v", err)
	}
	_, key := dataCommandAuthority()
	authority, _ := dataCommandAuthority()
	readSig, err := control.SignSetDataQuotaRead(raw, key, authority, time.Now().UTC().UnixMilli())
	if err != nil {
		t.Fatal(err)
	}
	if got, err := f.store.ReadSetDataQuotaReceipt(ctx, raw, readSig); err != nil || got != receipt {
		t.Fatalf("read: %+v %v", got, err)
	}

	// The chain now ends at the raised limits; a second raise must start there.
	tx, err := f.store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	b, i, err := currentQuota(ctx, tx, f.command.Payload.NamespaceID, f.data.QuotaBytes, f.data.QuotaInodes)
	tx.Rollback()
	if err != nil || b != strconv.FormatInt(oldBytes*2, 10) || i != strconv.FormatInt(oldInodes*2, 10) {
		t.Fatalf("current quota %s %s %v", b, i, err)
	}
	stale, staleRaw, staleSig := quotaRaise(t, f, 2, oldBytes, oldInodes, oldBytes*3, oldInodes*3)
	if _, err := f.store.DispatchSetDataQuota(ctx, staleRaw, staleSig, raiseNative(stale, &calls)); !errors.Is(err, ErrFenceReconciliation) || calls != 1 {
		t.Fatalf("a raise over stale limits was accepted: %v calls=%d", err, calls)
	}
	second, secondRaw, secondSig := quotaRaise(t, f, 3, oldBytes*2, oldInodes*2, oldBytes*3, oldInodes*3)
	if _, err := f.store.DispatchSetDataQuota(ctx, secondRaw, secondSig, raiseNative(second, &calls)); err != nil || calls != 2 {
		t.Fatalf("second raise: %v calls=%d", err, calls)
	}

	// Later checkpoints are capped by the current quota, while the install's receipt keeps its own digest.
	tx, err = f.store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	source, err := loadCheckpointSourceExcluding(ctx, tx, f.command, time.Now().UTC().UnixMilli(), f.command.EffectID)
	if err != nil || source.quotaBytes != strconv.FormatInt(oldBytes*3, 10) || source.data.QuotaBytes != f.data.QuotaBytes {
		t.Fatalf("checkpoint source quota %q data %q: %v", source.quotaBytes, source.data.QuotaBytes, err)
	}
}

func TestSetDataQuotaFinishesAnUnknownRaiseUnderTheSameClaim(t *testing.T) {
	ctx := context.Background()
	f, _ := appliedCheckpointFixture(t)
	fixtureVolumePresent(t, f)
	oldBytes, oldInodes := mustDataInt(f.data.QuotaBytes), mustDataInt(f.data.QuotaInodes)
	command, raw, sig := quotaRaise(t, f, 1, oldBytes, oldInodes, oldBytes*2, oldInodes)
	failing := func(ctx context.Context, request data.QuotaRaiseRequest) (data.QuotaRaiseSnapshot, error) {
		return data.QuotaRaiseSnapshot{}, data.ErrQuotaRaiseIncomplete
	}
	if _, err := f.store.DispatchSetDataQuota(ctx, raw, sig, failing); err == nil {
		t.Fatal("a failed native raise settled")
	}
	var state string
	if err := f.store.db.QueryRow(`SELECT state FROM runner_commands WHERE effect_id=?`, command.EffectID).Scan(&state); err != nil || state != "unknown" {
		t.Fatalf("an incomplete raise is %q: %v", state, err)
	}
	calls := 0
	if _, err := f.store.DispatchSetDataQuota(ctx, raw, sig, raiseNative(command, &calls)); err != nil || calls != 1 {
		t.Fatalf("the unknown raise was not finished: %v", err)
	}
	if scalarJournalInt(t, f.store.db, `SELECT COUNT(*) FROM runner_commands WHERE kind='set-data-quota'`) != 1 {
		t.Fatal("finishing a raise claimed it again")
	}
}

func TestSetDataQuotaRefusesWithoutCapacityNamingTheShortfall(t *testing.T) {
	ctx := context.Background()
	f, _ := appliedCheckpointFixture(t)
	fixtureVolumePresent(t, f)
	if err := f.store.SetCapacityBudgets(CapacityBudgets{MemoryMiB: 1 << 20, CPUMillis: 1 << 20, DiskBytes: 2 << 30, ImageStoreBytes: 1 << 40, RunningBackends: 64}); err != nil {
		t.Fatal(err)
	}
	oldBytes, oldInodes := mustDataInt(f.data.QuotaBytes), mustDataInt(f.data.QuotaInodes)
	command, raw, sig := quotaRaise(t, f, 1, oldBytes, oldInodes, 6<<30, 100000)
	calls := 0
	_, err := f.store.DispatchSetDataQuota(ctx, raw, sig, raiseNative(command, &calls))
	var refusal *CapacityRefusalError
	if !errors.As(err, &refusal) || refusal.Receipt.Dimension != CapacityDataQuota || calls != 0 {
		t.Fatalf("raise beyond capacity: %v calls=%d", err, calls)
	}
	if refusal.Receipt.Requested == "" || refusal.Receipt.Remaining == "" || !strings.Contains(refusal.Error(), "remaining=") {
		t.Fatalf("the refusal does not name the shortfall: %+v", refusal.Receipt)
	}
	if scalarJournalInt(t, f.store.db, `SELECT COUNT(*) FROM runner_commands WHERE kind='set-data-quota' AND state='reverted'`) != 1 {
		t.Fatal("the refusal is not durable")
	}
}

func TestSetDataQuotaRefusesAForeignNamespaceAndPendingWork(t *testing.T) {
	ctx := context.Background()
	f, _ := appliedCheckpointFixture(t)
	fixtureVolumePresent(t, f)
	oldBytes, oldInodes := mustDataInt(f.data.QuotaBytes), mustDataInt(f.data.QuotaInodes)
	command, raw, sig := quotaRaise(t, f, 1, oldBytes, oldInodes, oldBytes*2, oldInodes*2)
	calls := 0
	mustJournalExec(t, f.store.db, `UPDATE runner_resources SET observed_state='absent' WHERE id=? AND kind='volume'`, f.command.Payload.NamespaceID)
	if _, err := f.store.DispatchSetDataQuota(ctx, raw, sig, raiseNative(command, &calls)); err == nil || calls != 0 {
		t.Fatal("a raise over an absent volume was claimed")
	}
	fixtureVolumePresent(t, f)
	mustJournalExec(t, f.store.db, `UPDATE runner_commands SET state='unknown' WHERE effect_id=?`, f.command.EffectID)
	if _, err := f.store.DispatchSetDataQuota(ctx, raw, sig, raiseNative(command, &calls)); err == nil || calls != 0 {
		t.Fatal("a raise beside unsettled installation work was claimed")
	}
}

func importCommandFor(t *testing.T, f checkpointFixture, checkpoint CheckpointReceipt, n int, quotaBytes, quotaInodes int64) (control.ImportDataCommand, []byte, []byte) {
	t.Helper()
	authority, key := dataCommandAuthority()
	now := time.Now().UTC().UnixMilli()
	var receiptRaw string
	if err := f.store.db.QueryRow(`SELECT receipt_json FROM runner_commands WHERE effect_id=?`, checkpoint.EffectID).Scan(&receiptRaw); err != nil {
		t.Fatal(err)
	}
	var fence int64
	if err := f.store.db.QueryRow(`SELECT control_fence FROM runner_resources WHERE id=? AND kind='volume'`, checkpoint.NamespaceID).Scan(&fence); err != nil {
		t.Fatal(err)
	}
	command := control.ImportDataCommand{CommandHeader: control.CommandHeader{
		Format: control.ImportDataFormat, DeploymentID: checkpoint.DeploymentID, ControllerEpoch: checkpoint.ControllerEpoch,
		ControlRevision: "3", InstallationID: checkpoint.InstallationID, OperationID: checkpoint.OperationID,
		TargetGeneration: checkpoint.TargetGeneration, Stage: control.ImportDataStage, EffectNumber: "1",
		EffectID: fixtureID(7200 + n), Kind: control.ImportDataKind, InstallationFence: strconv.FormatInt(fence, 10),
	}, Payload: control.ImportDataPayload{
		NamespaceID: checkpoint.NamespaceID, ReleaseSHA256: checkpoint.ReleaseSHA256, DataFormat: checkpoint.DataFormat,
		WriterFence: checkpoint.WriterFence, ProjectID: checkpoint.ProjectID, NamespaceInode: checkpoint.NamespaceInode, Device: checkpoint.Device,
		QuotaBytes: strconv.FormatInt(quotaBytes, 10), QuotaInodes: strconv.FormatInt(quotaInodes, 10),
		ArchiveSHA256: strings.Repeat("a", 64), ArchiveBytes: "4096", ArchiveEntries: "3", ContentBytes: "100",
		SourceCheckpointOperationID: checkpoint.OperationID, SourceCheckpointEffectID: checkpoint.EffectID,
		SourceCheckpointReceiptSHA: candidateHash([]byte(receiptRaw)),
	}}
	digest, err := control.ImportDataLogicalDigest(command)
	if err != nil {
		t.Fatal(err)
	}
	command.PayloadSHA256 = digest
	command.IssuedMS, command.ExpiresMS = strconv.FormatInt(now, 10), strconv.FormatInt(now+25000, 10)
	raw, signature, err := control.SignImportData(command, key, authority, now)
	if err != nil {
		t.Fatal(err)
	}
	return command, raw, signature
}

func importNative(calls *int) ImportDataNative {
	return func(ctx context.Context, request data.ImportDataRequest) (data.ImportDataSnapshot, error) {
		*calls++
		return data.ImportDataSnapshot{
			NamespaceID: request.NamespaceID, ArchiveSHA256: request.ArchiveSHA256, ProjectID: request.ProjectID,
			DataFormat: request.DataFormat, Entries: request.Entries, UsedBytes: 8192, UsedInodes: request.Entries + 1,
			ByteLimit: request.QuotaBytes, InodeLimit: request.QuotaInodes, ObservedMS: time.Now().UTC().UnixMilli(),
			NamespaceDevice: request.ExpectedDevice, NamespaceInode: request.ExpectedNamespaceInode,
		}, nil
	}
}

func stoppedFixture(t *testing.T, f checkpointFixture, checkpoint CheckpointReceipt) {
	t.Helper()
	fixtureVolumePresent(t, f)
	mustJournalExec(t, f.store.db, `UPDATE runner_resources SET observed_state='stopped' WHERE data_namespace_id=? AND kind='container'`, checkpoint.NamespaceID)
}

func TestImportDataClaimsOverARetainedCheckpointAndSettlesOnce(t *testing.T) {
	ctx := context.Background()
	f, checkpoint := appliedCheckpointFixture(t)
	stoppedFixture(t, f, checkpoint)
	_, raw, sig := importCommandFor(t, f, checkpoint, 1, mustDataInt(f.data.QuotaBytes), mustDataInt(f.data.QuotaInodes))
	calls := 0
	receipt, err := f.store.DispatchImportData(ctx, raw, sig, importNative(&calls))
	if err != nil || calls != 1 || receipt.State != "imported" || receipt.UsedBytes != "8192" || receipt.UsedInodes != "4" {
		t.Fatalf("import: %+v calls=%d %v", receipt, calls, err)
	}
	again, err := f.store.DispatchImportData(ctx, raw, sig, importNative(&calls))
	if err != nil || calls != 1 || again != receipt {
		t.Fatalf("replay: %+v calls=%d %v", again, calls, err)
	}
	authority, key := dataCommandAuthority()
	readSig, err := control.SignImportDataRead(raw, key, authority, time.Now().UTC().UnixMilli())
	if err != nil {
		t.Fatal(err)
	}
	if got, err := f.store.ReadImportDataReceipt(ctx, raw, readSig); err != nil || got != receipt {
		t.Fatalf("read: %+v %v", got, err)
	}
	// An import never touches the checkpoint it keeps.
	var state string
	var revision int64
	if err := f.store.db.QueryRow(`SELECT state,revision FROM runner_checkpoints WHERE id=?`, checkpoint.CheckpointID).Scan(&state, &revision); err != nil || state != "verified" || revision != 2 {
		t.Fatalf("the pre-import checkpoint changed: %s %d %v", state, revision, err)
	}
}

func TestImportDataRefusesWithoutAKeptCheckpointOrOverAChangedQuotaOrARunningBackend(t *testing.T) {
	for _, mode := range []string{"checkpoint deleted", "checkpoint receipt", "running backend", "quota differs", "other operation's checkpoint"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			f, checkpoint := appliedCheckpointFixture(t)
			stoppedFixture(t, f, checkpoint)
			quotaBytes, quotaInodes := mustDataInt(f.data.QuotaBytes), mustDataInt(f.data.QuotaInodes)
			switch mode {
			case "checkpoint deleted":
				mustJournalExec(t, f.store.db, `UPDATE runner_checkpoints SET state='deleted' WHERE id=?`, checkpoint.CheckpointID)
			case "checkpoint receipt":
				mustJournalExec(t, f.store.db, `UPDATE runner_commands SET receipt_json='{}' WHERE effect_id=?`, checkpoint.EffectID)
			case "running backend":
				mustJournalExec(t, f.store.db, `UPDATE runner_resources SET observed_state='running' WHERE id=?`, checkpoint.ResourceID)
			case "quota differs":
				quotaBytes *= 2
			case "other operation's checkpoint":
				mustJournalExec(t, f.store.db, `UPDATE runner_commands SET operation_id=? WHERE effect_id=?`, fixtureID(7999), checkpoint.EffectID)
			}
			command, raw, sig := importCommandFor(t, f, checkpoint, 1, quotaBytes, quotaInodes)
			calls := 0
			if _, err := f.store.DispatchImportData(ctx, raw, sig, importNative(&calls)); err == nil || calls != 0 {
				t.Fatalf("an unsafe import was claimed: %v", err)
			}
			if scalarJournalInt(t, f.store.db, `SELECT COUNT(*) FROM runner_commands WHERE effect_id=?`, command.EffectID) != 0 {
				t.Fatal("a refused import wrote a command row")
			}
		})
	}
}

func TestImportDataFinishesAnUnknownImportUnderTheSameClaim(t *testing.T) {
	ctx := context.Background()
	f, checkpoint := appliedCheckpointFixture(t)
	stoppedFixture(t, f, checkpoint)
	command, raw, sig := importCommandFor(t, f, checkpoint, 1, mustDataInt(f.data.QuotaBytes), mustDataInt(f.data.QuotaInodes))
	// The crash window: the claim is written and the process dies before the native call returns.
	if _, _, err := f.store.claimImportData(ctx, raw, sig); err != nil {
		t.Fatal(err)
	}
	var state string
	if err := f.store.db.QueryRow(`SELECT state FROM runner_commands WHERE effect_id=?`, command.EffectID).Scan(&state); err != nil || state != "unknown" {
		t.Fatalf("a claimed import is %q: %v", state, err)
	}
	calls := 0
	if _, err := f.store.DispatchImportData(ctx, raw, sig, importNative(&calls)); err != nil || calls != 1 {
		t.Fatalf("the unknown import was not finished: %v", err)
	}
	if scalarJournalInt(t, f.store.db, `SELECT COUNT(*) FROM runner_commands WHERE kind='import-data'`) != 1 {
		t.Fatal("finishing an import claimed it again")
	}
}
