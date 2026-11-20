//go:build linux || darwin

package journal

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/data"
)

func failingImport(err error, calls *int) ImportDataNative {
	return func(ctx context.Context, request data.ImportDataRequest) (data.ImportDataSnapshot, error) {
		*calls++
		return data.ImportDataSnapshot{}, err
	}
}

func commandRow(t *testing.T, f checkpointFixture, effect string) (state, receipt string) {
	t.Helper()
	if err := f.store.db.QueryRow(`SELECT state,receipt_json FROM runner_commands WHERE effect_id=?`, effect).Scan(&state, &receipt); err != nil {
		t.Fatal(err)
	}
	return state, receipt
}

func guardRefuses(t *testing.T, f checkpointFixture, namespace string) bool {
	t.Helper()
	tx, err := f.store.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	err = requireNoUnrestoredImport(context.Background(), tx, f.command.InstallationID, namespace)
	if err != nil && !errors.Is(err, ErrFenceReconciliation) {
		t.Fatal(err)
	}
	return err != nil
}

// An import that fails after it began to change the namespace is a known failure: its row is failed, so the
// rollback restore of the pre-import checkpoint is not blocked, and the namespace is not startable until it applied.
func TestImportDataFailureAfterClearIsFailedAndTheRollbackRestoreApplies(t *testing.T) {
	ctx := context.Background()
	f, checkpoint, restore, restoreRaw, restoreSig := restoreJournalFixture(t)
	stoppedFixture(t, f, checkpoint)
	command, raw, sig := importCommandFor(t, f, checkpoint, 1, mustDataInt(f.data.QuotaBytes), mustDataInt(f.data.QuotaInodes))
	calls := 0
	cause := errors.Join(data.ErrImportIncomplete, errors.New("extract archive"))
	_, err := f.store.DispatchImportData(ctx, raw, sig, failingImport(cause, &calls))
	var failed *DataEffectFailedError
	if !errors.As(err, &failed) || failed.Code != dataFailureIncomplete || !errors.Is(err, data.ErrImportIncomplete) || calls != 1 {
		t.Fatalf("a failed import: %v calls=%d", err, calls)
	}
	state, receipt := commandRow(t, f, command.EffectID)
	if state != "reverted" {
		t.Fatalf("a failed import left its row %q", state)
	}
	if record, ok := parseDataEffectFailure(receipt); !ok || !record.Dirty || record.Code != dataFailureIncomplete || record.NamespaceID != checkpoint.NamespaceID {
		t.Fatalf("failure record %q", receipt)
	}
	// The same signed command answers the same failure and never runs the native import again.
	if _, err := f.store.DispatchImportData(ctx, raw, sig, importNative(&calls)); !errors.As(err, &failed) || failed.Code != dataFailureIncomplete || calls != 1 {
		t.Fatalf("replay of a failed import: %v calls=%d", err, calls)
	}
	// A read-back is a definite typed status, not a pending or orphaned one.
	authority, key := dataCommandAuthority()
	readSig, err := control.SignImportDataRead(raw, key, authority, time.Now().UTC().UnixMilli())
	if err != nil {
		t.Fatal(err)
	}
	answer, err := f.store.ReadImportDataOrStatus(ctx, raw, readSig)
	status, ok := answer.(MigrateDataStatus)
	if err != nil || !ok || status.State != "failed" || status.Code != dataFailureIncomplete || !DataEffectStatusMatchesHeader(status, command.CommandHeader) {
		t.Fatalf("read-back of a failed import: %#v %v", answer, err)
	}
	encoded, _ := json.Marshal(status)
	if parsed, err := ParseMigrateDataStatus(encoded); err != nil || parsed != status {
		t.Fatalf("a failed status does not round-trip: %v", err)
	}
	// The namespace is dirty: no start until a restore of it has applied.
	if !guardRefuses(t, f, checkpoint.NamespaceID) {
		t.Fatal("a start was allowed on a half-imported namespace")
	}
	// The rollback restore of the pre-import checkpoint is not blocked by the failed import.
	run := func(ctx context.Context, request data.RestoreRequest) (data.RestoreSnapshot, error) {
		return restoreSnapshotFor(restore), nil
	}
	restored, err := f.store.dispatchRestore(ctx, restoreRaw, restoreSig, run)
	if err != nil || restored.State != "restored" {
		t.Fatalf("the rollback restore after a failed import: %+v %v", restored, err)
	}
	if guardRefuses(t, f, checkpoint.NamespaceID) {
		t.Fatal("a restored namespace is still refused a start")
	}
}

// secondOperationCheckpoint gives the installation a second pre-import checkpoint under its own operation, as a
// later import operation would have.
func secondOperationCheckpoint(t *testing.T, f checkpointFixture, first CheckpointReceipt) CheckpointReceipt {
	t.Helper()
	second := first
	second.OperationID, second.EffectID, second.CheckpointID = fixtureID(7700), fixtureID(7701), fixtureID(7702)
	raw, err := json.Marshal(second)
	if err != nil {
		t.Fatal(err)
	}
	mustJournalExec(t, f.store.db, `INSERT INTO runner_commands(effect_id,operation_id,installation_id,stage,effect_number,kind,payload_sha256,target_generation,controller_epoch,admitted_fence,state,receipt_json,ack_attempted_ms,created_ms,updated_ms)
		SELECT ?,?,installation_id,stage,effect_number,kind,payload_sha256,target_generation,controller_epoch,admitted_fence,state,?,ack_attempted_ms,created_ms,updated_ms FROM runner_commands WHERE effect_id=?`,
		second.EffectID, second.OperationID, string(raw), first.EffectID)
	mustJournalExec(t, f.store.db, `INSERT INTO runner_checkpoints(id,data_namespace_id,installation_id,operation_id,release_sha256,data_format,writer_fence,manifest_sha256,bytes,state,revision,verified_ms,created_ms,updated_ms)
		SELECT ?,data_namespace_id,installation_id,?,release_sha256,data_format,writer_fence,manifest_sha256,bytes,state,revision,verified_ms,created_ms,updated_ms FROM runner_checkpoints WHERE id=?`,
		second.CheckpointID, second.OperationID, first.CheckpointID)
	return second
}

// An import the native layer refused before it touched anything ends failed and clean: it blocks no later command
// of the installation and no start.
func TestImportDataRefusedBeforeTouchingAnythingBlocksNothing(t *testing.T) {
	for name, cause := range map[string]error{
		"archive missing":    data.ErrImportArchiveMissing,
		"archive ambiguous":  data.ErrImportArchiveAmbiguous,
		"archive invalid":    data.ErrImportArchiveInvalid,
		"too large":          data.ErrImportTooLarge,
		"format mismatch":    data.ErrImportFormatMismatch,
		"reviewed quota off": errors.Join(data.ErrImportUnsafe, errors.New("namespace quota differs from the reviewed quota")),
	} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			f, checkpoint := appliedCheckpointFixture(t)
			stoppedFixture(t, f, checkpoint)
			quotaBytes, quotaInodes := mustDataInt(f.data.QuotaBytes), mustDataInt(f.data.QuotaInodes)
			command, raw, sig := importCommandFor(t, f, checkpoint, 1, quotaBytes, quotaInodes)
			calls := 0
			if _, err := f.store.DispatchImportData(ctx, raw, sig, failingImport(cause, &calls)); !errors.Is(err, cause) {
				t.Fatalf("refused import: %v", err)
			}
			state, receipt := commandRow(t, f, command.EffectID)
			if record, ok := parseDataEffectFailure(receipt); state != "reverted" || !ok || record.Dirty {
				t.Fatalf("a refused import is %q %q", state, receipt)
			}
			if guardRefuses(t, f, checkpoint.NamespaceID) {
				t.Fatal("a clean refusal blocks a start")
			}
			// A second import of the installation (a later operation, with its own checkpoint) is claimed and applied.
			second := secondOperationCheckpoint(t, f, checkpoint)
			_, raw2, sig2 := importCommandFor(t, f, second, 2, quotaBytes, quotaInodes)
			if receipt, err := f.store.DispatchImportData(ctx, raw2, sig2, importNative(&calls)); err != nil || receipt.State != "imported" {
				t.Fatalf("a second import after a refusal: %+v %v", receipt, err)
			}
		})
	}
}

// A raise refused with nothing changed ends failed and no longer blocks the installation; one that may have applied
// stays unknown.
func TestSetDataQuotaRefusedByTheNativeLayerEndsFailed(t *testing.T) {
	ctx := context.Background()
	f, _ := appliedCheckpointFixture(t)
	fixtureVolumePresent(t, f)
	oldBytes, oldInodes := mustDataInt(f.data.QuotaBytes), mustDataInt(f.data.QuotaInodes)
	command, raw, sig := quotaRaise(t, f, 1, oldBytes, oldInodes, oldBytes*2, oldInodes)
	calls := 0
	refuse := func(ctx context.Context, request data.QuotaRaiseRequest) (data.QuotaRaiseSnapshot, error) {
		calls++
		return data.QuotaRaiseSnapshot{}, errors.Join(data.ErrQuotaRaiseUnsafe, errors.New("live limits differ"))
	}
	_, err := f.store.DispatchSetDataQuota(ctx, raw, sig, refuse)
	var failed *DataEffectFailedError
	if !errors.As(err, &failed) || failed.Code != dataFailureRaiseUnsafe || !errors.Is(err, data.ErrQuotaRaiseUnsafe) {
		t.Fatalf("refused raise: %v", err)
	}
	if state, _ := commandRow(t, f, command.EffectID); state != "reverted" {
		t.Fatalf("a refused raise is %q", state)
	}
	if _, err := f.store.DispatchSetDataQuota(ctx, raw, sig, raiseNative(command, &calls)); !errors.As(err, &failed) || calls != 1 {
		t.Fatalf("replay of a refused raise: %v calls=%d", err, calls)
	}
	authority, key := dataCommandAuthority()
	readSig, err := control.SignSetDataQuotaRead(raw, key, authority, time.Now().UTC().UnixMilli())
	if err != nil {
		t.Fatal(err)
	}
	answer, err := f.store.ReadSetDataQuotaOrStatus(ctx, raw, readSig)
	if status, ok := answer.(MigrateDataStatus); err != nil || !ok || status.State != "failed" || status.Code != dataFailureRaiseUnsafe {
		t.Fatalf("read-back of a refused raise: %#v %v", answer, err)
	}
	// The installation is not blocked: a second raise is claimed and applied, and a start guard has nothing to refuse.
	_, raw2, sig2 := quotaRaise(t, f, 2, oldBytes, oldInodes, oldBytes*2, oldInodes)
	second, _, _ := quotaRaise(t, f, 2, oldBytes, oldInodes, oldBytes*2, oldInodes)
	if receipt, err := f.store.DispatchSetDataQuota(ctx, raw2, sig2, raiseNative(second, &calls)); err != nil || receipt.State != "raised" {
		t.Fatalf("a raise after a refused raise: %+v %v", receipt, err)
	}
}

func TestSetDataQuotaThatMayHaveAppliedStaysUnknown(t *testing.T) {
	ctx := context.Background()
	f, _ := appliedCheckpointFixture(t)
	fixtureVolumePresent(t, f)
	oldBytes, oldInodes := mustDataInt(f.data.QuotaBytes), mustDataInt(f.data.QuotaInodes)
	command, raw, sig := quotaRaise(t, f, 1, oldBytes, oldInodes, oldBytes*2, oldInodes)
	incomplete := func(ctx context.Context, request data.QuotaRaiseRequest) (data.QuotaRaiseSnapshot, error) {
		return data.QuotaRaiseSnapshot{}, errors.Join(data.ErrQuotaRaiseIncomplete, errors.New("sync"))
	}
	if _, err := f.store.DispatchSetDataQuota(ctx, raw, sig, incomplete); err == nil {
		t.Fatal("an incomplete raise settled")
	}
	if state, _ := commandRow(t, f, command.EffectID); state != "unknown" {
		t.Fatalf("an incomplete raise is %q", state)
	}
}

// The quota chain of a namespace reads only that namespace's raises: raises of other namespaces never count against
// its bound.
func TestQuotaChainIgnoresOtherNamespacesRaises(t *testing.T) {
	ctx := context.Background()
	f, _ := appliedCheckpointFixture(t)
	fixtureVolumePresent(t, f)
	oldBytes, oldInodes := mustDataInt(f.data.QuotaBytes), mustDataInt(f.data.QuotaInodes)
	command, raw, sig := quotaRaise(t, f, 1, oldBytes, oldInodes, oldBytes*2, oldInodes*2)
	calls := 0
	if _, err := f.store.DispatchSetDataQuota(ctx, raw, sig, raiseNative(command, &calls)); err != nil {
		t.Fatal(err)
	}
	other := fixtureID(7800)
	tx, err := f.store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 4200; i++ {
		if _, err := tx.Exec(`INSERT INTO runner_commands(effect_id,operation_id,installation_id,stage,effect_number,kind,payload_sha256,target_generation,controller_epoch,admitted_fence,state,receipt_json,ack_attempted_ms,created_ms,updated_ms)
			VALUES(?,?,?,'prepare_data',1,'set-data-quota',?,1,?,1,'applied',?,NULL,1,1)`,
			fixtureID(100000+i), fixtureID(200000+i), fixtureID(300000+i), candidateHash([]byte("b")), command.ControllerEpoch,
			`{"namespace_id":"`+other+`"}`); err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	chainTx, err := f.store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer chainTx.Rollback()
	chain, err := loadQuotaChain(ctx, chainTx, command.Payload.NamespaceID, f.data.QuotaBytes, f.data.QuotaInodes)
	if err != nil || len(chain) != 2 || chain[1] != (quotaLink{oldBytes * 2, oldInodes * 2}) {
		t.Fatalf("chain of the raised namespace: %+v %v", chain, err)
	}
	// The other namespace's own chain is its install limits plus nothing it can parse, and it is bounded by itself.
	if _, err := loadQuotaChain(ctx, chainTx, other, f.data.QuotaBytes, f.data.QuotaInodes); err == nil {
		t.Fatal("a chain of 4,200 unparseable raises of one namespace was accepted")
	}
}

// A start is refused while the namespace carries a failed import no restore has repaired.
func TestUpdateStartRefusedOverAFailedImportUntilARestoreApplied(t *testing.T) {
	insertFailure := func(t *testing.T, m migrateFixture, dirty bool) {
		t.Helper()
		record, _ := json.Marshal(dataEffectFailure{Format: dataEffectFailureFormat, NamespaceID: m.checkpoint.NamespaceID, Code: dataFailureIncomplete, Dirty: dirty, SettledMS: strconv.FormatInt(time.Now().UTC().UnixMilli(), 10)})
		mustJournalExec(t, m.f.store.db, `INSERT INTO runner_commands(effect_id,operation_id,installation_id,stage,effect_number,kind,payload_sha256,target_generation,controller_epoch,admitted_fence,state,receipt_json,ack_attempted_ms,created_ms,updated_ms)
			VALUES(?,?,?,'restore_data',1,'import-data',?,1,?,1,'reverted',?,NULL,1,1)`,
			fixtureID(7900), fixtureID(7901), m.f.command.InstallationID, candidateHash([]byte("x")), m.f.command.ControllerEpoch, string(record))
	}
	m := migrateDataJournalFixture(t)
	if _, err := m.dispatch(t); err != nil {
		t.Fatal(err)
	}
	insertFailure(t, m, true)
	raw, sig := signUpdateStartCommand(t, migratedStartCommand(t, m))
	if _, err := m.f.store.ReserveUpdateStart(context.Background(), raw, sig); !errors.Is(err, ErrFenceReconciliation) {
		t.Fatalf("a start was reserved on a half-imported namespace: %v", err)
	}
	clean := migrateDataJournalFixture(t)
	if _, err := clean.dispatch(t); err != nil {
		t.Fatal(err)
	}
	insertFailure(t, clean, false)
	raw, sig = signUpdateStartCommand(t, migratedStartCommand(t, clean))
	if _, err := clean.f.store.ReserveUpdateStart(context.Background(), raw, sig); err != nil {
		t.Fatalf("a clean refusal blocked a start: %v", err)
	}
}

// An import that fails because its own bound expired is still recorded: the failure is written on a context of its
// own, so the row never stays unknown and the rollback restore is not refused.
func TestImportDataFailureOnAnExpiredBoundIsRecordedAndTheRollbackApplies(t *testing.T) {
	ctx := context.Background()
	f, checkpoint, restore, restoreRaw, restoreSig := restoreJournalFixture(t)
	stoppedFixture(t, f, checkpoint)
	command, raw, sig := importCommandFor(t, f, checkpoint, 1, mustDataInt(f.data.QuotaBytes), mustDataInt(f.data.QuotaInodes))
	previous := newImportContext
	newImportContext = func(parent context.Context, _ int64) (context.Context, context.CancelFunc) {
		return context.WithTimeout(context.WithoutCancel(parent), time.Millisecond)
	}
	t.Cleanup(func() { newImportContext = previous })
	expiring := func(ctx context.Context, request data.ImportDataRequest) (data.ImportDataSnapshot, error) {
		<-ctx.Done()
		return data.ImportDataSnapshot{}, errors.Join(data.ErrImportIncomplete, ctx.Err())
	}
	_, err := f.store.DispatchImportData(ctx, raw, sig, expiring)
	var failed *DataEffectFailedError
	if !errors.As(err, &failed) || failed.Code != dataFailureIncomplete {
		t.Fatalf("an import that expired: %v", err)
	}
	state, receipt := commandRow(t, f, command.EffectID)
	if record, ok := parseDataEffectFailure(receipt); state != "reverted" || !ok || !record.Dirty {
		t.Fatalf("an expired import is %q %q", state, receipt)
	}
	run := func(ctx context.Context, request data.RestoreRequest) (data.RestoreSnapshot, error) {
		return restoreSnapshotFor(restore), nil
	}
	if restored, err := f.store.dispatchRestore(ctx, restoreRaw, restoreSig, run); err != nil || restored.State != "restored" {
		t.Fatalf("the rollback restore after an expired import: %+v %v", restored, err)
	}
}

// A claim that resumes an unknown row cannot know what the crashed run did, so even a pre-clear refusal is dirty.
func TestImportDataResumedClaimRecordsEveryFailureDirty(t *testing.T) {
	ctx := context.Background()
	f, checkpoint := appliedCheckpointFixture(t)
	stoppedFixture(t, f, checkpoint)
	command, raw, sig := importCommandFor(t, f, checkpoint, 1, mustDataInt(f.data.QuotaBytes), mustDataInt(f.data.QuotaInodes))
	if _, _, err := f.store.claimImportData(ctx, raw, sig); err != nil {
		t.Fatal(err)
	}
	calls := 0
	if _, err := f.store.DispatchImportData(ctx, raw, sig, failingImport(data.ErrImportArchiveMissing, &calls)); !errors.Is(err, data.ErrImportArchiveMissing) {
		t.Fatalf("resumed import: %v", err)
	}
	_, receipt := commandRow(t, f, command.EffectID)
	if record, ok := parseDataEffectFailure(receipt); !ok || !record.Dirty || record.Code != dataFailureArchiveMissing {
		t.Fatalf("a resumed refusal is %q", receipt)
	}
	if !guardRefuses(t, f, checkpoint.NamespaceID) {
		t.Fatal("a start was allowed after a resumed, possibly half-done import")
	}
}

// A raise that ends definitively failed gives back the larger reservation its claim took.
func TestSetDataQuotaRefusedRaiseRestoresTheNamespaceReservations(t *testing.T) {
	ctx := context.Background()
	f, _ := appliedCheckpointFixture(t)
	fixtureVolumePresent(t, f)
	if err := f.store.SetCapacityBudgets(CapacityBudgets{MemoryMiB: 1 << 20, CPUMillis: 1 << 20, DiskBytes: 1 << 50, CheckpointBytes: 1 << 50, ImageStoreBytes: 1 << 40, RunningBackends: 64}); err != nil {
		t.Fatal(err)
	}
	oldBytes, oldInodes := mustDataInt(f.data.QuotaBytes), mustDataInt(f.data.QuotaInodes)
	namespace := f.command.Payload.NamespaceID
	oldPlan, _ := f.store.namespacePlan(namespace, f.command.InstallationID, oldBytes, oldInodes)
	for _, row := range []struct {
		resource string
		disk     int64
	}{{namespace, oldPlan.diskBytes}, {checkpointReservationID(namespace), oldPlan.checkpointBytes}} {
		mustJournalExec(t, f.store.db, `INSERT INTO runner_reservations(id,resource_id,installation_id,kind,memory_mib,cpu_millis,disk_bytes,image_bytes,state,created_ms,updated_ms)
			VALUES(?,?,?,'namespace',0,0,?,0,'held',1,1)`, fixtureID(7950+len(row.resource)%7), row.resource, f.command.InstallationID, row.disk)
	}
	reserved := func() (int64, int64) {
		return scalarJournalInt(t, f.store.db, `SELECT disk_bytes FROM runner_reservations WHERE resource_id=?`, namespace),
			scalarJournalInt(t, f.store.db, `SELECT disk_bytes FROM runner_reservations WHERE resource_id=?`, checkpointReservationID(namespace))
	}
	_, raw, sig := quotaRaise(t, f, 1, oldBytes, oldInodes, oldBytes*2, oldInodes*2)
	enlargedInCall := false
	refuse := func(ctx context.Context, request data.QuotaRaiseRequest) (data.QuotaRaiseSnapshot, error) {
		d, c := reserved()
		enlargedInCall = d > oldPlan.diskBytes || c > oldPlan.checkpointBytes
		return data.QuotaRaiseSnapshot{}, errors.Join(data.ErrQuotaRaiseUnsafe, errors.New("live limits differ"))
	}
	if _, err := f.store.DispatchSetDataQuota(ctx, raw, sig, refuse); !errors.Is(err, data.ErrQuotaRaiseUnsafe) {
		t.Fatalf("refused raise: %v", err)
	}
	if d, c := reserved(); !enlargedInCall || d != oldPlan.diskBytes || c != oldPlan.checkpointBytes {
		t.Fatalf("reservations after a refused raise: data=%d checkpoints=%d want %d %d (enlarged during the claim: %v)", d, c, oldPlan.diskBytes, oldPlan.checkpointBytes, enlargedInCall)
	}
}
