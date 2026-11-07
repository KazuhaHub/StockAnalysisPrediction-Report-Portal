//go:build linux || darwin

package journal

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/data"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/driver"
)

type fakeMigrationNative struct {
	t          *testing.T
	runs       int
	absents    int
	onRun      func(driver.MigrationJobRequest)
	exit       int
	killed     bool
	runErr     error
	absentErr  error
	marker     data.SchemaMarkerSnapshot
	markerErr  error
	markers    int
	lastRun    driver.MigrationJobRequest
	absentHook func()
	runCtx     func(context.Context)
	absentIDs  []driver.MigrationJobRequest
}

func (n *fakeMigrationNative) RunMigrationJob(ctx context.Context, r driver.MigrationJobRequest) (driver.MigrationJobSnapshot, error) {
	n.runs++
	n.lastRun = r
	if n.onRun != nil {
		n.onRun(r)
	}
	if n.runCtx != nil {
		n.runCtx(ctx)
	}
	if n.runErr != nil {
		return driver.MigrationJobSnapshot{}, n.runErr
	}
	return driver.MigrationJobSnapshot{
		Binding: r.Binding, Identity: r.Identity, NativeID: strings.Repeat("c", 64),
		ImageID: "sha256:" + strings.Repeat("b", 64), Platform: r.Platform,
		ExitCode: n.exit, Killed: n.killed, ObservedMS: time.Now().UnixMilli(),
	}, nil
}
func (n *fakeMigrationNative) ProveMigrationAbsent(_ context.Context, r driver.MigrationJobRequest) error {
	n.absents++
	n.absentIDs = append(n.absentIDs, r)
	if n.absentHook != nil {
		n.absentHook()
	}
	return n.absentErr
}
func (n *fakeMigrationNative) ReadSchemaMarker(_ context.Context, _ data.RootIdentity, id string, project uint32) (data.SchemaMarkerSnapshot, error) {
	n.markers++
	if n.markerErr != nil {
		return data.SchemaMarkerSnapshot{}, n.markerErr
	}
	s := n.marker
	if n.runs == 0 {
		// Before the job the namespace still has the source schema (no marker yet).
		s.Present, s.Schema = false, 0
	}
	s.NamespaceID, s.ProjectID = id, project
	return s, nil
}

type migrateFixture struct {
	f          checkpointFixture
	checkpoint CheckpointReceipt
	command    control.MigrateDataCommand
	raw, sig   []byte
	descriptor []byte
	step       appplatform.DataMigration
	native     *fakeMigrationNative
}

func migrateDescriptor(t *testing.T) ([]byte, appplatform.DataMigration, string) {
	t.Helper()
	raw, err := os.ReadFile("../../appplatform/testdata/descriptors/managed.json")
	if err != nil {
		t.Fatal(err)
	}
	var d map[string]any
	if err := json.Unmarshal(raw, &d); err != nil {
		t.Fatal(err)
	}
	dataSection := d["data"].(map[string]any)
	dataSection["schema"], dataSection["readable_schemas"] = 2, []int{2}
	dataSection["migrations"] = []any{map[string]any{"from": 1, "to": 2, "entrypoint": []string{"/app/migrate", "--apply"}, "wall_clock_seconds": 60}}
	migrated, _ := json.Marshal(d)
	parsed, err := appplatform.ParseDescriptor(migrated)
	if err != nil {
		t.Fatal(err)
	}
	return migrated, parsed.Manifest.Data.Migrations[0], parsed.SHA256
}

func signMigrate(t *testing.T, c control.MigrateDataCommand) ([]byte, []byte) {
	t.Helper()
	digest, err := control.MigrateDataLogicalDigest(c)
	if err != nil {
		t.Fatal(err)
	}
	c.PayloadSHA256 = digest
	raw, sig, err := control.SignMigrateData(c, ed25519.NewKeyFromSeed(bytes.Repeat([]byte{30}, 32)), importAuthority(), time.Now().UTC().UnixMilli())
	if err != nil {
		t.Fatal(err)
	}
	return raw, sig
}

func migrateRead(t *testing.T, raw []byte) []byte {
	t.Helper()
	sig, err := control.SignMigrateDataRead(raw, ed25519.NewKeyFromSeed(bytes.Repeat([]byte{30}, 32)), importAuthority(), time.Now().UTC().UnixMilli())
	if err != nil {
		t.Fatal(err)
	}
	return sig
}

// migrateDataJournalFixture is an update whose checkpoint is verified, whose source container is
// removed by an applied remove-code effect and whose command is not yet admitted.
func migrateDataJournalFixture(t *testing.T) migrateFixture {
	t.Helper()
	f, checkpoint := appliedCheckpointFixture(t)
	checkpointRaw, _ := json.Marshal(checkpoint)
	descriptor, step, release := migrateDescriptor(t)
	now := time.Now().UTC().UnixMilli()
	removal := ManagedRemovalReceipt{
		Format: managedRemovalReceiptFormat, DeploymentID: f.command.DeploymentID, ControllerEpoch: f.command.ControllerEpoch,
		InstallationID: f.command.InstallationID, OperationID: checkpoint.OperationID,
		SourceOperationID: checkpoint.SourceOperationID, SourceEffectID: checkpoint.SourceStartEffectID,
		TargetGeneration: checkpoint.TargetGeneration, Stage: "remove_code", EffectID: fixtureID(1801),
		PayloadSHA256: strings.Repeat("d", 64), SourceReceiptSHA256: checkpoint.SourceReceiptSHA256,
		ResourceID: checkpoint.ResourceID, DataNamespaceID: checkpoint.NamespaceID, ReleaseSHA256: checkpoint.ReleaseSHA256,
		NativeID: strings.Repeat("e", 64), State: "absent", ObservedMS: strconv.FormatInt(now, 10), SettledMS: strconv.FormatInt(now, 10),
	}
	removalRaw, _ := json.Marshal(removal)
	mustJournalExec(t, f.store.db, `INSERT INTO runner_commands(effect_id,operation_id,installation_id,stage,effect_number,kind,payload_sha256,target_generation,controller_epoch,admitted_fence,state,receipt_json,ack_attempted_ms,created_ms,updated_ms) VALUES(?,?,?,?,?,?,?,?,?,?,'applied',?,NULL,?,?)`,
		removal.EffectID, removal.OperationID, removal.InstallationID, "remove_code", 1, "remove-code", removal.PayloadSHA256, 2, removal.ControllerEpoch, 2, string(removalRaw), now, now)
	mustJournalExec(t, f.store.db, `UPDATE runner_resources SET desired_state='absent',observed_state='absent',revision=4,observed_ms=?,updated_ms=? WHERE id=?`, now, now, checkpoint.ResourceID)
	writer, _ := strconv.Atoi(checkpoint.WriterFence)
	command := control.MigrateDataCommand{CommandHeader: control.CommandHeader{
		Format: control.MigrateDataFormat, DeploymentID: f.command.DeploymentID, ControllerEpoch: f.command.ControllerEpoch,
		ControlRevision: "3", InstallationID: f.command.InstallationID, OperationID: checkpoint.OperationID,
		TargetGeneration: checkpoint.TargetGeneration, Stage: "migrate", EffectNumber: "2", EffectID: fixtureID(1802),
		Kind: "migrate-data", InstallationFence: "2",
		IssuedMS: strconv.FormatInt(now, 10), ExpiresMS: strconv.FormatInt(now+25000, 10),
	}, Payload: control.MigrateDataPayload{
		NamespaceID: checkpoint.NamespaceID, ResourceID: fixtureID(1803), CheckpointID: checkpoint.CheckpointID,
		CheckpointEffectID: checkpoint.EffectID, CheckpointReceiptSHA256: candidateHash(checkpointRaw),
		RemoveCodeEffectID: removal.EffectID, RemoveCodeReceiptSHA256: candidateHash(removalRaw),
		SourceReleaseSHA256: checkpoint.ReleaseSHA256, TargetReleaseSHA256: release,
		SourceSchema: "1", TargetSchema: "2",
		CheckpointWriterFence: checkpoint.WriterFence, WriterFence: strconv.Itoa(writer + 1), StartWriterFence: strconv.Itoa(writer + 2),
		ProjectID: checkpoint.ProjectID, NamespaceInode: checkpoint.NamespaceInode, Device: checkpoint.Device,
		QuotaBytes: f.data.QuotaBytes, QuotaInodes: f.data.QuotaInodes, WallClockSeconds: "60",
		EntrypointSHA256: control.MigrationEntrypointSHA256(step.Entrypoint),
	}}
	raw, sig := signMigrate(t, command)
	command.PayloadSHA256, _ = control.MigrateDataLogicalDigest(command)
	native := &fakeMigrationNative{t: t}
	quota, _ := strconv.ParseInt(f.data.QuotaBytes, 10, 64)
	inodes, _ := strconv.ParseInt(f.data.QuotaInodes, 10, 64)
	native.marker = data.SchemaMarkerSnapshot{
		Present: true, Schema: 2, UsedBytes: 4096, UsedInodes: 4, ByteLimit: quota, InodeLimit: inodes,
		NamespaceDevice: mustDataUint(checkpoint.Device), NamespaceInode: mustDataUint(checkpoint.NamespaceInode), ObservedMS: now,
	}
	return migrateFixture{f: f, checkpoint: checkpoint, command: command, raw: raw, sig: sig, descriptor: descriptor, step: step, native: native}
}

func (m migrateFixture) dispatch(t *testing.T) (MigrateDataReceipt, error) {
	t.Helper()
	return m.f.store.DispatchMigrateData(context.Background(), m.raw, m.sig, m.descriptor, m.native, importDataPin)
}

func (m migrateFixture) resign(t *testing.T, mutate func(*control.MigrateDataCommand)) ([]byte, []byte) {
	t.Helper()
	c := m.command
	mutate(&c)
	return signMigrate(t, c)
}

func rowState(t *testing.T, s *Store, effect string) string {
	t.Helper()
	var state string
	if err := s.db.QueryRow(`SELECT state FROM runner_commands WHERE effect_id=?`, effect).Scan(&state); err != nil {
		t.Fatal(err)
	}
	return state
}

func TestMigrateDataRequiresAppliedSourceRemoval(t *testing.T) {
	for name, mutate := range map[string]func(m migrateFixture){
		"no removal row": func(m migrateFixture) {
			mustJournalExec(t, m.f.store.db, `DELETE FROM runner_commands WHERE effect_id=?`, m.command.Payload.RemoveCodeEffectID)
		},
		"removal unknown": func(m migrateFixture) {
			mustJournalExec(t, m.f.store.db, `UPDATE runner_commands SET state='unknown' WHERE effect_id=?`, m.command.Payload.RemoveCodeEffectID)
		},
		"source container not absent": func(m migrateFixture) {
			mustJournalExec(t, m.f.store.db, `UPDATE runner_resources SET desired_state='present',observed_state='stopped',revision=3 WHERE id=?`, m.checkpoint.ResourceID)
		},
		"removal receipt changed": func(m migrateFixture) {
			mustJournalExec(t, m.f.store.db, `UPDATE runner_commands SET receipt_json=replace(receipt_json,'absent','stopped') WHERE effect_id=?`, m.command.Payload.RemoveCodeEffectID)
		},
	} {
		t.Run(name, func(t *testing.T) {
			m := migrateDataJournalFixture(t)
			mutate(m)
			if _, _, err := m.f.store.beginMigrateData(context.Background(), m.raw, m.sig); err == nil {
				t.Fatal("migration admitted without an applied source removal")
			}
			if scalar(t, m.f.store.db, `SELECT COUNT(*) FROM runner_commands WHERE kind='migrate-data'`) != 0 {
				t.Fatal("refused admission wrote a row")
			}
		})
	}
}

func TestMigrateDataRefusesRunningNamespaceContainer(t *testing.T) {
	m := migrateDataJournalFixture(t)
	now := time.Now().UTC().UnixMilli()
	mustJournalExec(t, m.f.store.db, `INSERT INTO runner_resources(id,installation_id,data_namespace_id,kind,native_id,release_sha256,generation,controller_epoch,control_fence,writer_fence,desired_state,observed_state,ownership_sha256,revision,observed_ms,created_ms,updated_ms) VALUES(?,?,?,'container',?,?,2,?,2,1,'present','running',?,2,?,?,?)`,
		fixtureID(1850), m.command.InstallationID, m.command.Payload.NamespaceID, strings.Repeat("9", 64), m.checkpoint.ReleaseSHA256, m.command.ControllerEpoch, strings.Repeat("8", 64), now, now, now)
	if _, _, err := m.f.store.beginMigrateData(context.Background(), m.raw, m.sig); err == nil {
		t.Fatal("migration admitted beside a running namespace container")
	}
}

func TestMigrateDataFenceStrictlyBetween(t *testing.T) {
	m := migrateDataJournalFixture(t)
	for name, mutate := range map[string]func(*control.MigrateDataCommand){
		"checkpoint fence differs from the receipt": func(c *control.MigrateDataCommand) {
			c.Payload.CheckpointWriterFence = "9"
			c.Payload.WriterFence, c.Payload.StartWriterFence = "10", "11"
		},
		"source schema differs from the checkpoint format": func(c *control.MigrateDataCommand) {
			c.Payload.SourceSchema = "5"
			c.Payload.TargetSchema = "6"
		},
	} {
		t.Run(name, func(t *testing.T) {
			raw, sig := m.resign(t, mutate)
			if _, _, err := m.f.store.beginMigrateData(context.Background(), raw, sig); err == nil {
				t.Fatal("migration with unbound fences or schemas admitted")
			}
		})
	}
	// A live namespace resource at or above the migration's writer fence leaves no room below it.
	now := time.Now().UTC().UnixMilli()
	mustJournalExec(t, m.f.store.db, `INSERT INTO runner_resources(id,installation_id,data_namespace_id,kind,native_id,release_sha256,generation,controller_epoch,control_fence,writer_fence,desired_state,observed_state,ownership_sha256,revision,observed_ms,created_ms,updated_ms) VALUES(?,?,?,'container',?,?,2,?,2,?,'present','stopped',?,2,?,?,?)`,
		fixtureID(1851), m.command.InstallationID, m.command.Payload.NamespaceID, strings.Repeat("7", 64), m.checkpoint.ReleaseSHA256, m.command.ControllerEpoch, m.command.Payload.WriterFence, strings.Repeat("6", 64), now, now, now)
	if _, _, err := m.f.store.beginMigrateData(context.Background(), m.raw, m.sig); err == nil {
		t.Fatal("writer fence not strictly above the namespace's resources")
	}
}

func TestMigrateDataOnePerOperation(t *testing.T) {
	m := migrateDataJournalFixture(t)
	if _, _, err := m.f.store.beginMigrateData(context.Background(), m.raw, m.sig); err != nil {
		t.Fatal(err)
	}
	mustJournalExec(t, m.f.store.db, `UPDATE runner_commands SET state='reverted' WHERE effect_id=?`, m.command.EffectID)
	mustJournalExec(t, m.f.store.db, `UPDATE runner_resources SET desired_state='absent',observed_state='absent',revision=2,observed_ms=1 WHERE id=?`, m.command.Payload.ResourceID)
	raw, sig := m.resign(t, func(c *control.MigrateDataCommand) {
		c.EffectID = fixtureID(1860)
		c.Payload.ResourceID = fixtureID(1861)
	})
	if _, _, err := m.f.store.beginMigrateData(context.Background(), raw, sig); err == nil {
		t.Fatal("a second migration in one operation admitted")
	}
}

func TestMigrateClaimCommitsUnknownBeforeNative(t *testing.T) {
	m := migrateDataJournalFixture(t)
	m.native.onRun = func(r driver.MigrationJobRequest) {
		if rowState(t, m.f.store, m.command.EffectID) != "unknown" {
			t.Error("native job started before the effect was durably unknown")
		}
		var observed string
		if err := m.f.store.db.QueryRow(`SELECT observed_state FROM runner_resources WHERE id=?`, m.command.Payload.ResourceID).Scan(&observed); err != nil || observed != "unknown" {
			t.Errorf("job resource not reserved before native: %q %v", observed, err)
		}
		if r.Identity.WriterFence != m.command.Payload.WriterFence || r.Identity.Generation != m.command.TargetGeneration ||
			r.Identity.ReleaseSHA256 != m.command.Payload.TargetReleaseSHA256 || r.Step.From != 1 || r.Data.QuotaBytes <= 0 {
			t.Errorf("job identity: %+v", r.Identity)
		}
	}
	if _, err := m.dispatch(t); err != nil {
		t.Fatal(err)
	}
	if m.native.runs != 1 {
		t.Fatal("job did not run once")
	}
}

func TestMigrateAppliedOnlyOnExit0MarkerQuota(t *testing.T) {
	for name, tc := range map[string]struct {
		exit    int
		killed  bool
		marker  func(*data.SchemaMarkerSnapshot)
		applied bool
		outcome string
	}{
		"success":          {applied: true},
		"nonzero exit":     {exit: 1, outcome: "exit_nonzero"},
		"killed":           {exit: 137, killed: true, outcome: "wall_clock"},
		"marker missing":   {marker: func(s *data.SchemaMarkerSnapshot) { s.Present, s.Schema = false, 0 }, outcome: "marker_missing"},
		"marker wrong":     {marker: func(s *data.SchemaMarkerSnapshot) { s.Schema = 9 }, outcome: "marker_mismatch"},
		"marker still old": {marker: func(s *data.SchemaMarkerSnapshot) { s.Schema = 1 }, outcome: "marker_mismatch"},
		"over byte quota":  {marker: func(s *data.SchemaMarkerSnapshot) { s.UsedBytes = s.ByteLimit + 1 }, outcome: "over_quota"},
	} {
		t.Run(name, func(t *testing.T) {
			m := migrateDataJournalFixture(t)
			m.native.exit, m.native.killed = tc.exit, tc.killed
			if tc.marker != nil {
				tc.marker(&m.native.marker)
			}
			receipt, err := m.dispatch(t)
			if err != nil {
				t.Fatal(err)
			}
			wantState, wantReceipt := "reverted", "failed"
			if tc.applied {
				wantState, wantReceipt = "applied", "migrated"
			}
			if got := rowState(t, m.f.store, m.command.EffectID); got != wantState || receipt.State != wantReceipt || receipt.Outcome != tc.outcome {
				t.Fatalf("state=%s receipt=%s outcome=%q", got, receipt.State, receipt.Outcome)
			}
			if tc.exit != 0 || tc.killed {
				if m.native.markers != 1 {
					t.Fatal("marker read after a failed exit (only the pre-claim read is allowed)")
				}
			}
			var observed string
			if err := m.f.store.db.QueryRow(`SELECT observed_state FROM runner_resources WHERE id=?`, m.command.Payload.ResourceID).Scan(&observed); err != nil || observed != "absent" {
				t.Fatalf("job resource after a definite end: %q %v", observed, err)
			}
			again, err := m.dispatch(t)
			if err != nil || again.State != wantReceipt || m.native.runs != 1 {
				t.Fatalf("replay reran or changed the receipt: %+v %v runs=%d", again, err, m.native.runs)
			}
		})
	}
}

func TestMigrateKnownFailureRevertedAfterContainerAbsent(t *testing.T) {
	m := migrateDataJournalFixture(t)
	m.native.exit = 3
	if _, err := m.dispatch(t); err != nil {
		t.Fatal(err)
	}
	if rowState(t, m.f.store, m.command.EffectID) != "reverted" {
		t.Fatal("known failure not reverted")
	}
	// When the native side cannot prove the container gone it reports an error: nothing is reverted.
	m = migrateDataJournalFixture(t)
	m.native.runErr = driver.ErrContainerUncertain
	if _, err := m.dispatch(t); err == nil {
		t.Fatal("uncertain removal reported success")
	}
	if rowState(t, m.f.store, m.command.EffectID) != "unknown" {
		t.Fatal("effect settled although the container was not proven absent")
	}
}

func TestMigrateUnknownNeverRerun(t *testing.T) {
	m := migrateDataJournalFixture(t)
	m.native.runErr = driver.ErrContainerUncertain
	if _, err := m.dispatch(t); err == nil {
		t.Fatal("uncertain run succeeded")
	}
	m.native.runErr = nil
	if _, err := m.dispatch(t); err == nil || !errors.Is(err, ErrMigrationUnknown) {
		t.Fatalf("unknown migration was not refused: %v", err)
	}
	if m.native.runs != 1 {
		t.Fatalf("an unknown migration ran again: %d", m.native.runs)
	}
}

func TestMigrateReadPendingWhileGateHeld(t *testing.T) {
	m := migrateDataJournalFixture(t)
	m.native.runErr = driver.ErrContainerUncertain
	_, _ = m.dispatch(t)
	if err := m.f.store.acquireDataExecution(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer m.f.store.releaseDataExecution()
	got, err := m.f.store.ReadMigrateDataOrStatus(context.Background(), m.raw, migrateRead(t, m.raw))
	status, ok := got.(MigrateDataStatus)
	if err != nil || !ok || status.State != "pending" {
		t.Fatalf("held gate must read pending: %#v %v", got, err)
	}
}

func TestMigrateReadOrphanedAfterRestart(t *testing.T) {
	m := migrateDataJournalFixture(t)
	m.native.runErr = driver.ErrContainerUncertain
	_, _ = m.dispatch(t)
	got, err := m.f.store.ReadMigrateDataOrStatus(context.Background(), m.raw, migrateRead(t, m.raw))
	status, ok := got.(MigrateDataStatus)
	if err != nil || !ok || status.State != "orphaned" {
		t.Fatalf("unknown with a free gate must read orphaned: %#v %v", got, err)
	}
}

func TestMigrateAbsenceOnlyUnderGate(t *testing.T) {
	m := migrateDataJournalFixture(t)
	read := migrateRead(t, m.raw)
	if err := m.f.store.acquireDataExecution(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, err := m.f.store.ReadMigrateDataOrStatus(context.Background(), m.raw, read)
	if status, ok := got.(MigrateDataStatus); err != nil || !ok || status.State != "pending" {
		t.Fatalf("no row while the gate is held must not read absent: %#v %v", got, err)
	}
	m.f.store.releaseDataExecution()
	got, err = m.f.store.ReadMigrateDataOrStatus(context.Background(), m.raw, read)
	status, ok := got.(MigrateDataStatus)
	if err != nil || !ok || status.State != "absent" {
		t.Fatalf("no row with a free gate must read absent: %#v %v", got, err)
	}
	if !MigrateDataStatusMatchesCommand(status, m.command) {
		t.Fatal("absence does not bind the command")
	}
	// The gate is released again.
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := m.f.store.acquireDataExecution(ctx); err != nil {
		t.Fatal("absence read kept the gate")
	}
	m.f.store.releaseDataExecution()
	// A claimed row is never absent.
	if _, err := m.dispatch(t); err != nil {
		t.Fatal(err)
	}
	got, err = m.f.store.ReadMigrateDataOrStatus(context.Background(), m.raw, read)
	if _, ok := got.(MigrateDataReceipt); err != nil || !ok {
		t.Fatalf("settled read: %#v %v", got, err)
	}
}

// The connection that carried the command times out and is cancelled while the job runs: the claimed
// job still runs to a definite end and settles, with a context that is not cancelled.
func TestMigrateConnectionTimeoutJobContinues(t *testing.T) {
	m := migrateDataJournalFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	var observed error
	m.native.onRun = func(driver.MigrationJobRequest) {
		cancel()
	}
	m.native.runCtx = func(c context.Context) {
		observed = c.Err()
		if deadline, ok := c.Deadline(); !ok || time.Until(deadline) < time.Minute {
			t.Error("claimed migration has no generous own bound")
		}
	}
	receipt, err := m.f.store.DispatchMigrateData(ctx, m.raw, m.sig, m.descriptor, m.native, importDataPin)
	if err != nil || receipt.State != "migrated" || observed != nil {
		t.Fatalf("claimed migration followed its connection: %+v err=%v ctx=%v", receipt, err, observed)
	}
	if rowState(t, m.f.store, m.command.EffectID) != "applied" {
		t.Fatal("migration left unsettled by a cancelled connection")
	}
	if bound := MigrateDataBound(60); bound != 60*time.Second+driver.MigrationGrace {
		t.Fatalf("bound = %s", bound)
	}
}

// The writer fences of a migration are exact: the job runs one above the checkpoint's and the target starts one above
// that, so no other writer can hold a fence in between.
func TestMigrateFencesAreExactlyConsecutive(t *testing.T) {
	marker := migrateClaimMarker{
		Format: migrateClaimFormat, ClaimID: fixtureID(1), EffectID: fixtureID(2), ResourceID: fixtureID(3), OwnershipSHA256: strings.Repeat("a", 64),
		AdmittedFence: "2", ClaimedMS: "1", NamespaceID: fixtureID(4), CheckpointID: fixtureID(5), CheckpointReceiptSHA256: strings.Repeat("b", 64),
		RemoveCodeReceiptSHA256: strings.Repeat("c", 64), SourceReleaseSHA256: strings.Repeat("d", 64), TargetReleaseSHA256: strings.Repeat("e", 64),
		SourceSchema: "1", TargetSchema: "2", CheckpointWriterFence: "1", WriterFence: "2", StartWriterFence: "3", ProjectID: "1",
		NamespaceInode: "1", Device: "1", QuotaBytes: "512", QuotaInodes: "1",
	}
	if !validMigrateMarker(marker) {
		t.Fatal("consecutive fences refused")
	}
	marker.StartWriterFence = "4"
	if validMigrateMarker(marker) {
		t.Fatal("a claim with a skipped start fence is valid")
	}
}
