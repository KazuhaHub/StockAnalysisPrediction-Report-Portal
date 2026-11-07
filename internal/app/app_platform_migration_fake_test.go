package app

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/journal"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/transport"
)

// platformFakeObservation answers whichever lifecycle accessor the Portal asks, with what the test set.
type platformFakeObservation struct {
	authority  control.Authority
	endpoint   transport.CandidateReceiptEndpoint
	receivedAt time.Time
	commandSHA string
	migrate    *journal.MigrateDataReceipt
	discard    *journal.DiscardMigrationReceipt
	restore    *journal.RestoreReceipt
	removal    *journal.ManagedRemovalReceipt
	status     *journal.MigrateDataStatus
}

var errFakeNoSuchReply = errors.New("fake reply holds no such receipt")

func (o platformFakeObservation) Controller() control.Authority { return o.authority }
func (o platformFakeObservation) Endpoint() transport.CandidateReceiptEndpoint {
	return o.endpoint
}
// ReceivedAt is the time the reply is asked for, never earlier than what it says: a reply built after the proof was
// taken would otherwise be observed in a later millisecond than it was received.
func (o platformFakeObservation) ReceivedAt() time.Time { return time.Now().UTC() }
func (o platformFakeObservation) CommandSHA256() string { return o.commandSHA }
func (o platformFakeObservation) MigrateDataReceipt() (journal.MigrateDataReceipt, error) {
	if o.migrate == nil {
		return journal.MigrateDataReceipt{}, errFakeNoSuchReply
	}
	return *o.migrate, nil
}
func (o platformFakeObservation) DiscardMigrationReceipt() (journal.DiscardMigrationReceipt, error) {
	if o.discard == nil {
		return journal.DiscardMigrationReceipt{}, errFakeNoSuchReply
	}
	return *o.discard, nil
}
func (o platformFakeObservation) RestoreReceipt() (journal.RestoreReceipt, error) {
	if o.restore == nil {
		return journal.RestoreReceipt{}, errFakeNoSuchReply
	}
	return *o.restore, nil
}
func (o platformFakeObservation) UpdateStartRemovalReceipt() (journal.ManagedRemovalReceipt, error) {
	if o.removal == nil {
		return journal.ManagedRemovalReceipt{}, errFakeNoSuchReply
	}
	return *o.removal, nil
}
func (o platformFakeObservation) MigrationStatus() (journal.MigrateDataStatus, error) {
	if o.status == nil {
		return journal.MigrateDataStatus{}, errFakeNoSuchReply
	}
	return *o.status, nil
}

func platformFakeProof(authority control.Authority, endpoint platformRuntimeEndpoint, raw []byte) platformFakeObservation {
	return platformFakeObservation{
		authority: authority, receivedAt: time.Now().UTC(), commandSHA: platformDigest(raw),
		endpoint: transport.CandidateReceiptEndpoint{
			Path: endpoint.path, RunnerUID: 0, ControllerUID: endpoint.controllerUID, ControllerGID: endpoint.controllerGID, RootController: true,
		},
	}
}

// platformFakeMigrationRunner is a scripted runner. A nil step fails the test; every call is recorded.
type platformFakeMigrationRunner struct {
	t              *testing.T
	calls          []string
	executeMigrate func(platformMigrateIntent) (platformMigrateObservation, error)
	readMigrate    func(platformMigrateIntent) (platformMigrateObservation, error)
	executeDiscard func(platformDiscardIntent) (platformDiscardObservation, error)
	readDiscard    func(platformDiscardIntent) (platformDiscardObservation, error)
	executeRestore func(platformRestoreIntent) (platformSettlingRestoreObservation, error)
	readRestore    func(platformRestoreIntent) (platformSettlingRestoreObservation, error)
	removeTarget   func(platformUpdateTargetRemovalIntent) (platformUpdateTargetRemovalObservation, error)
}

func (f *platformFakeMigrationRunner) count(name string) int {
	n := 0
	for _, c := range f.calls {
		if c == name {
			n++
		}
	}
	return n
}

func (f *platformFakeMigrationRunner) unscripted(name string) error {
	f.t.Helper()
	f.t.Errorf("unscripted runner call %s", name)
	return errors.New("unscripted " + name)
}

func (f *platformFakeMigrationRunner) CheckRoot(context.Context, string, platformRuntimeRegistration, control.Authority, platformRuntimeEndpoint) error {
	f.calls = append(f.calls, "check-root")
	return nil
}
func (f *platformFakeMigrationRunner) ExecuteMigrate(_ context.Context, _ platformRuntimeEndpoint, i platformMigrateIntent) (platformMigrateObservation, error) {
	f.calls = append(f.calls, "execute-migrate")
	if f.executeMigrate == nil {
		return nil, f.unscripted("execute-migrate")
	}
	return f.executeMigrate(i)
}
func (f *platformFakeMigrationRunner) ReadMigrate(_ context.Context, _ platformRuntimeEndpoint, i platformMigrateIntent) (platformMigrateObservation, error) {
	f.calls = append(f.calls, "read-migrate")
	if f.readMigrate == nil {
		return nil, f.unscripted("read-migrate")
	}
	return f.readMigrate(i)
}
func (f *platformFakeMigrationRunner) ExecuteDiscard(_ context.Context, _ platformRuntimeEndpoint, i platformDiscardIntent) (platformDiscardObservation, error) {
	f.calls = append(f.calls, "execute-discard")
	if f.executeDiscard == nil {
		return nil, f.unscripted("execute-discard")
	}
	return f.executeDiscard(i)
}
func (f *platformFakeMigrationRunner) ReadDiscard(_ context.Context, _ platformRuntimeEndpoint, i platformDiscardIntent) (platformDiscardObservation, error) {
	f.calls = append(f.calls, "read-discard")
	if f.readDiscard == nil {
		return nil, f.unscripted("read-discard")
	}
	return f.readDiscard(i)
}
func (f *platformFakeMigrationRunner) ExecuteRestore(_ context.Context, _ platformRuntimeEndpoint, i platformRestoreIntent) (platformSettlingRestoreObservation, error) {
	f.calls = append(f.calls, "execute-restore")
	if f.executeRestore == nil {
		return nil, f.unscripted("execute-restore")
	}
	return f.executeRestore(i)
}
func (f *platformFakeMigrationRunner) ReadRestore(_ context.Context, _ platformRuntimeEndpoint, i platformRestoreIntent) (platformSettlingRestoreObservation, error) {
	f.calls = append(f.calls, "read-restore")
	if f.readRestore == nil {
		return nil, f.unscripted("read-restore")
	}
	return f.readRestore(i)
}
func (f *platformFakeMigrationRunner) RemoveTarget(_ context.Context, _ platformRuntimeEndpoint, i platformUpdateTargetRemovalIntent) (platformUpdateTargetRemovalObservation, error) {
	f.calls = append(f.calls, "remove-"+i.command.Stage)
	if f.removeTarget == nil {
		return nil, f.unscripted("remove-target")
	}
	return f.removeTarget(i)
}

// platformFakeObservedAt remembers when a runner first settled an effect. A terminal receipt is immutable, so a
// read-back of it later returns the time it was first observed, not the time of the read.
var platformFakeObservedAt sync.Map

func platformFakeFirstObserved(effect string) string {
	now := strconv.FormatInt(time.Now().UTC().UnixMilli(), 10)
	stored, _ := platformFakeObservedAt.LoadOrStore(effect, now)
	return stored.(string)
}

// platformMigrateReceiptFor builds the receipt a runner would return for the command: a valid claim, the
// state and outcome asked for, and the marker the job wrote.
func platformMigrateReceiptFor(c control.MigrateDataCommand, state, outcome string) journal.MigrateDataReceipt {
	p := c.Payload
	now := platformFakeFirstObserved(c.EffectID)
	observedAt, _ := strconv.ParseInt(now, 10, 64)
	claimed := strconv.FormatInt(observedAt-1, 10)
	claim := journal.MigrateDataClaim{
		Format: "portal.runner.migrate-data-claim/1-dev.1", ClaimID: platformDerivedUUID("claim", c.EffectID), EffectID: c.EffectID,
		ResourceID: p.ResourceID, OwnershipSHA256: platformDigest([]byte("ownership" + c.EffectID)), AdmittedFence: c.InstallationFence,
		ClaimedMS: claimed, NamespaceID: p.NamespaceID, CheckpointID: p.CheckpointID, CheckpointReceiptSHA256: p.CheckpointReceiptSHA256,
		RemoveCodeReceiptSHA256: p.RemoveCodeReceiptSHA256, SourceReleaseSHA256: p.SourceReleaseSHA256, TargetReleaseSHA256: p.TargetReleaseSHA256,
		SourceSchema: p.SourceSchema, TargetSchema: p.TargetSchema, CheckpointWriterFence: p.CheckpointWriterFence, WriterFence: p.WriterFence,
		StartWriterFence: p.StartWriterFence, ProjectID: p.ProjectID, NamespaceInode: p.NamespaceInode, Device: p.Device,
		QuotaBytes: p.QuotaBytes, QuotaInodes: p.QuotaInodes, RecoversChain: p.RecoversChain,
	}
	exit, marker := "0", p.TargetSchema
	if state != "migrated" {
		exit, marker = "1", "0"
	}
	return journal.MigrateDataReceipt{
		Format: "portal.runner.migrate-data-receipt/1-dev.1", Claim: claim, DeploymentID: c.DeploymentID, ControllerEpoch: c.ControllerEpoch,
		InstallationID: c.InstallationID, OperationID: c.OperationID, TargetGeneration: c.TargetGeneration, Stage: "migrate",
		EffectID: c.EffectID, PayloadSHA256: c.PayloadSHA256, ExitCode: exit, MarkerSchema: marker, UsedBytes: "4096", UsedInodes: "4",
		State: state, Outcome: outcome, ObservedMS: now, SettledMS: now,
	}
}

func platformMigrateAnswers(i platformMigrateIntent, endpoint platformRuntimeEndpoint, state, outcome string) platformFakeObservation {
	o := platformFakeProof(i.authority, endpoint, i.raw)
	r := platformMigrateReceiptFor(i.command, state, outcome)
	o.migrate = &r
	return o
}

func platformMigrateStatusFor(h control.CommandHeader, state string) *journal.MigrateDataStatus {
	return &journal.MigrateDataStatus{
		Format: "portal.runner.migration-status/1-dev.1", Kind: h.Kind, DeploymentID: h.DeploymentID, ControllerEpoch: h.ControllerEpoch,
		InstallationID: h.InstallationID, OperationID: h.OperationID, TargetGeneration: h.TargetGeneration, Stage: h.Stage,
		EffectNumber: h.EffectNumber, EffectID: h.EffectID, PayloadSHA256: h.PayloadSHA256, State: state,
		ObservedMS: strconv.FormatInt(time.Now().UTC().UnixMilli(), 10),
	}
}

func platformMigrateStatusAnswer(i platformMigrateIntent, endpoint platformRuntimeEndpoint, state string) platformFakeObservation {
	o := platformFakeProof(i.authority, endpoint, i.raw)
	o.status = platformMigrateStatusFor(i.command.CommandHeader, state)
	return o
}

// platformMigrationStageFixture brings a reviewed managed update through quiesce and checkpoint to the migrate
// stage with the source removal applied, using the real stage code and receipts the runner would return.
func platformMigrationStageFixture(t *testing.T, pg, migrating bool) (*Server, platformOperationLease, platformRuntimeEndpoint, *platformFakeMigrationRunner) {
	t.Helper()
	platformFakeObservedAt.Clear()
	var mutate func(*appplatform.Manifest)
	if migrating {
		mutate = platformMigrationManifest(nil)
	}
	s, lease, quiesce, endpoint := platformManagedUpdateQuiesceFixtureWith(t, pg, mutate)
	snapshot, err := s.commitPlatformManagedUpdateQuiesce(context.Background(), lease, quiesce, platformManagedUpdateQuiesceReceipt(quiesce))
	if err != nil {
		t.Fatal(err)
	}
	checkpointEndpoint := platformCheckpointEndpointFixture()
	checkpoint, err := s.preparePlatformCheckpoint(context.Background(), snapshot)
	if err != nil {
		t.Fatal(err)
	}
	migrate, err := s.commitPlatformCheckpoint(context.Background(), snapshot, checkpoint, checkpointEndpoint,
		platformCheckpointObservationFixture(checkpoint, checkpointEndpoint))
	if err != nil || migrate.snapshot.Stage != appplatform.StageMigrate {
		t.Fatal("checkpoint did not reach the migrate stage", migrate.snapshot, err)
	}
	removal, err := s.preparePlatformManagedUpdateMigrate(context.Background(), migrate)
	if err != nil {
		t.Fatal(err)
	}
	after, err := s.commitPlatformManagedUpdateQuiesce(context.Background(), migrate, removal, platformManagedUpdateQuiesceReceipt(removal))
	if err != nil {
		t.Fatal(err)
	}
	fake := &platformFakeMigrationRunner{t: t}
	s.platformMigration = fake
	return s, after, endpoint, fake
}

func jsonUnmarshalPlan(raw string, plan *platformManagedUpdatePlan) error {
	return json.Unmarshal([]byte(raw), plan)
}
