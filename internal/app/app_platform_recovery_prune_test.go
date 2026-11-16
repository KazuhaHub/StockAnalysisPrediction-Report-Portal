package app

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/journal"
)

// platformFakePruneReply answers as a runner would for one delete-checkpoint command.
type platformFakePruneReply struct {
	platformFakeObservation
	receipt *journal.DeleteCheckpointReceipt
	status  *journal.DeleteCheckpointStatus
}

func (r platformFakePruneReply) DeleteCheckpointReceipt() (journal.DeleteCheckpointReceipt, error) {
	if r.receipt == nil {
		return journal.DeleteCheckpointReceipt{}, errFakeNoSuchReply
	}
	return *r.receipt, nil
}

func (r platformFakePruneReply) DeleteCheckpointStatus() (journal.DeleteCheckpointStatus, error) {
	if r.status == nil {
		return journal.DeleteCheckpointStatus{}, errFakeNoSuchReply
	}
	return *r.status, nil
}

// platformFakePruneRunner models what matters to the sender of a delete-checkpoint command: an effect journal keyed
// by effect id, and the checkpoints that exist. A checkpoint answers a refusal when refuse names one for it.
type platformFakePruneRunner struct {
	t        *testing.T
	endpoint platformRuntimeEndpoint
	mu       sync.Mutex
	calls    []string
	effects  map[string]string // effect id -> "unknown" | "applied" | "reverted"
	outcomes map[string]string // effect id -> the outcome its receipt carries
	// refuse maps a checkpoint id to the refusal the runner answers for it.
	refuse map[string]string
	// gone are the checkpoints whose archive the runner removed, in order.
	gone []string
	sent []control.DeleteCheckpointCommand
	// loseExecuteReplyOnce makes an execute run on the runner and then fail on the wire.
	loseExecuteReplyOnce bool
	// readFails makes every read-back fail on the wire.
	readFails bool
	// unclaimed makes the runner refuse before it claims anything: the connection drops, and a read-back finds no effect.
	unclaimed bool
	// readPending answers every read-back of an effect with no receipt with pending, as a runner whose gate is held.
	readPending bool
	// pendingFor makes that many executes answer pending without claiming anything.
	pendingFor int
}

func newPlatformFakePruneRunner(t *testing.T, endpoint platformRuntimeEndpoint) *platformFakePruneRunner {
	return &platformFakePruneRunner{t: t, endpoint: endpoint, effects: map[string]string{}, outcomes: map[string]string{}, refuse: map[string]string{}}
}

func (f *platformFakePruneRunner) count(name string) int {
	n := 0
	for _, c := range f.calls {
		if c == name {
			n++
		}
	}
	return n
}

// effectIDs is the set of distinct effect ids the runner was sent.
func (f *platformFakePruneRunner) effectIDs() map[string]bool {
	out := map[string]bool{}
	for _, c := range f.sent {
		out[c.EffectID] = true
	}
	return out
}

func (f *platformFakePruneRunner) receipt(call platformDeleteCheckpointCall, outcome string) *journal.DeleteCheckpointReceipt {
	c := call.command
	now := platformFakeFirstObserved(c.EffectID)
	at, _ := strconv.ParseInt(now, 10, 64)
	p := c.Payload
	claim := journal.DeleteCheckpointClaim{
		Format: "portal.runner.delete-checkpoint-claim/1-dev.1", ClaimID: platformDerivedUUID("claim", c.EffectID), EffectID: c.EffectID,
		RecoverySetID: p.RecoverySetID, CheckpointID: p.CheckpointID, NamespaceID: p.NamespaceID, SourceOperationID: p.SourceOperationID,
		ReleaseSHA256: p.ReleaseSHA256, ManifestSHA256: p.ManifestSHA256, ArchiveBytes: p.ArchiveBytes, DataFormat: p.DataFormat, WriterFence: p.WriterFence,
		AdmittedFence: c.InstallationFence, ClaimedMS: strconv.FormatInt(at-1, 10),
	}
	r := &journal.DeleteCheckpointReceipt{
		Format: "portal.runner.delete-checkpoint-receipt/1-dev.1", Claim: claim, DeploymentID: c.DeploymentID, ControllerEpoch: c.ControllerEpoch,
		InstallationID: c.InstallationID, OperationID: c.OperationID, TargetGeneration: c.TargetGeneration, Stage: c.Stage, EffectID: c.EffectID,
		PayloadSHA256: c.PayloadSHA256, RecoverySetID: p.RecoverySetID, CheckpointID: p.CheckpointID, NamespaceID: p.NamespaceID,
		ManifestSHA256: p.ManifestSHA256, ArchiveBytes: p.ArchiveBytes, Outcome: outcome, ArchiveFound: outcome == journal.DeleteOutcomeRemoved,
		ObservedMS: now, SettledMS: now,
	}
	raw, err := json.Marshal(r)
	if err != nil {
		f.t.Fatal(err)
	}
	if _, err := journal.ParseDeleteCheckpointReceipt(raw); err != nil {
		f.t.Fatalf("the fake runner built a receipt the Portal's parser rejects: %v", err)
	}
	return r
}

func (f *platformFakePruneRunner) statusOf(call platformDeleteCheckpointCall, state string) *journal.DeleteCheckpointStatus {
	c := call.command
	return &journal.DeleteCheckpointStatus{
		Format: "portal.runner.delete-checkpoint-status/1-dev.1", DeploymentID: c.DeploymentID, ControllerEpoch: c.ControllerEpoch,
		InstallationID: c.InstallationID, OperationID: c.OperationID, TargetGeneration: c.TargetGeneration, Stage: c.Stage,
		EffectNumber: c.EffectNumber, EffectID: c.EffectID, PayloadSHA256: c.PayloadSHA256, State: state,
		ObservedMS: strconv.FormatInt(time.Now().UTC().UnixMilli(), 10),
	}
}

func (f *platformFakePruneRunner) answer(call platformDeleteCheckpointCall) platformFakePruneReply {
	reply := platformFakePruneReply{platformFakeObservation: platformFakeProof(call.authority, f.endpoint, call.raw)}
	if f.effects[call.command.EffectID] == "applied" || f.effects[call.command.EffectID] == "reverted" {
		reply.receipt = f.receipt(call, f.outcomes[call.command.EffectID])
	}
	return reply
}

func (f *platformFakePruneRunner) DeleteCheckpoint(_ context.Context, _ platformRuntimeEndpoint, call platformDeleteCheckpointCall) (platformDeleteCheckpointReply, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "delete")
	f.sent = append(f.sent, call.command)
	if _, err := control.VerifyDeleteCheckpoint(call.raw, call.signature, call.authority, time.Now().UnixMilli()); err != nil {
		f.t.Errorf("the runner would refuse the command: %v", err)
		return nil, err
	}
	if f.unclaimed {
		return nil, errors.New("connection dropped before the runner claimed anything")
	}
	if f.pendingFor > 0 {
		f.pendingFor--
		reply := platformFakePruneReply{platformFakeObservation: platformFakeProof(call.authority, f.endpoint, call.raw)}
		reply.status = f.statusOf(call, "pending")
		return reply, nil
	}
	id, checkpoint := call.command.EffectID, call.command.Payload.CheckpointID
	if f.effects[id] == "" {
		f.effects[id] = "unknown"
	}
	if f.effects[id] == "unknown" {
		outcome := journal.DeleteOutcomeRemoved
		for _, gone := range f.gone {
			if gone == checkpoint {
				// Another effect already retired it: a later effect id settles absent.
				outcome = journal.DeleteOutcomeAbsent
			}
		}
		if refusal := f.refuse[checkpoint]; refusal != "" {
			outcome = refusal
		}
		f.outcomes[id] = outcome
		if outcome == journal.DeleteOutcomeRemoved || outcome == journal.DeleteOutcomeAbsent {
			f.effects[id] = "applied"
			if outcome == journal.DeleteOutcomeRemoved {
				f.gone = append(f.gone, checkpoint)
			}
		} else {
			f.effects[id] = "reverted"
		}
	}
	reply := f.answer(call)
	if f.loseExecuteReplyOnce {
		f.loseExecuteReplyOnce = false
		return nil, errors.New("connection lost after the runner ran the command")
	}
	return reply, nil
}

func (f *platformFakePruneRunner) ReadDeleteCheckpoint(_ context.Context, _ platformRuntimeEndpoint, call platformDeleteCheckpointCall) (platformDeleteCheckpointReply, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "read")
	if f.readFails {
		return nil, errors.New("read-back lost on the wire")
	}
	if _, err := control.VerifyDeleteCheckpointRead(call.raw, call.readSignature, call.authority, time.Now().UnixMilli()); err != nil {
		f.t.Errorf("the runner would refuse the read: %v", err)
		return nil, err
	}
	if f.effects[call.command.EffectID] == "" {
		return nil, errPlatformMigrationAbsent
	}
	reply := f.answer(call)
	if reply.receipt != nil {
		return reply, nil
	}
	if f.readPending {
		reply.status = f.statusOf(call, "pending")
		return reply, nil
	}
	reply.status = f.statusOf(call, "orphaned")
	return reply, nil
}

// pruneFixture is a managed installation whose operation is finished, a runtime that can be sent commands, and
// helpers to add recovery sets.
type pruneFixture struct {
	*reclaimFixture
	runner    *platformFakePruneRunner
	namespace string
	release   string
	sets      int
}

func newPruneFixture(t *testing.T, pg bool) *pruneFixture {
	t.Helper()
	f := newReclaimFixtureFor(t, pg)
	p := &pruneFixture{reclaimFixture: f}
	p.runner = newPlatformFakePruneRunner(t, f.endpoint)
	f.s.platformPruneRunner = p.runner
	p.namespace = scalar[string](t, f.s.st, `SELECT data_namespace_id FROM app_platform_activations WHERE installation_id=? AND state='active'`, f.installation)
	p.release = scalar[string](t, f.s.st, `SELECT release_sha256 FROM app_platform_activations WHERE installation_id=? AND state='active'`, f.installation)
	noPruneBackoff(t)
	// The sets the fixture inherited are not part of what a test says.
	mustExec(t, f.s.st, `DELETE FROM app_platform_recovery_sets`)
	// Keep one set per installation unless a test says otherwise.
	f.s.st.SetSetting(platformRetentionRecoverySetsKey, "1")
	return p
}

// noPruneBackoff lets a refused deletion be retried at once for the length of the test.
func noPruneBackoff(t *testing.T) {
	t.Helper()
	old := platformPruneBackoff
	platformPruneBackoff = 0
	t.Cleanup(func() { platformPruneBackoff = old })
}

// set adds a recovery set in the given state; created orders the sets of the installation, newest highest.
func (p *pruneFixture) set(t *testing.T, state string, created int64) string {
	t.Helper()
	p.sets++
	n := 9000 + p.sets
	id := platformFixtureID(n)
	var deleted any
	var verified any
	if state == "verified" || state == "restoring" || state == "review_required" || state == "deleted" {
		verified = int64(1)
	}
	if state == "deleted" {
		deleted = int64(2)
	}
	platformTestInsert(t, p.s.st.poolExec(), "recovery_sets", map[string]any{
		"id": id, "installation_id": p.installation, "data_namespace_id": p.namespace, "operation_id": p.hostOp,
		"release_sha256": p.release, "data_format": 1, "checkpoint_id": platformFixtureID(n + 500), "manifest_sha256": platformDigest([]byte("archive " + id)),
		"bytes": 4096, "state": state, "controller_epoch": p.epoch, "writer_fence": 1, "verified_ms": verified, "deleted_ms": deleted,
		"created_ms": created, "updated_ms": created,
	})
	return id
}

func (p *pruneFixture) prune(t *testing.T) platformPruneResult {
	t.Helper()
	result, err := p.s.prunePlatformRecoverySets(context.Background(), p.endpoint)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func (p *pruneFixture) state(t *testing.T, id string) string {
	return scalar[string](t, p.s.st, `SELECT state FROM app_platform_recovery_sets WHERE id=?`, id)
}

func (p *pruneFixture) checkpoint(t *testing.T, id string) string {
	return scalar[string](t, p.s.st, `SELECT checkpoint_id FROM app_platform_recovery_sets WHERE id=?`, id)
}

func (p *pruneFixture) effects(t *testing.T, state string) int {
	return scalar[int](t, p.s.st, `SELECT COUNT(*) FROM app_platform_operation_effects WHERE kind='delete_checkpoint' AND state=?`, state)
}

// Test 15. Fails if the newest set is selected, if a set beyond the retention is skipped, or if a set is marked
// deleted on the Portal before the runner's applied receipt.
func TestPruneDeletesTheOldestSetsBeyondTheRetentionAndNeverTheNewest(t *testing.T) {
	eachPlatformDriver(t, func(t *testing.T, pg bool) {
		p := newPruneFixture(t, pg)
		oldest, middle, newest := p.set(t, "verified", 100), p.set(t, "verified", 200), p.set(t, "verified", 300)
		result := p.prune(t)
		if result.Settled != 2 {
			t.Fatalf("settled %d, want the two oldest", result.Settled)
		}
		if p.state(t, oldest) != "deleted" || p.state(t, middle) != "deleted" || p.state(t, newest) != "verified" {
			t.Fatalf("states %s %s %s", p.state(t, oldest), p.state(t, middle), p.state(t, newest))
		}
		if len(p.runner.gone) != 2 || len(p.runner.sent) != 2 {
			t.Fatalf("the runner was sent %d commands and removed %v", len(p.runner.sent), p.runner.gone)
		}
		for _, c := range p.runner.sent {
			if c.Payload.RecoverySetID == newest || c.Payload.CheckpointID == p.checkpoint(t, newest) {
				t.Fatal("the newest set was sent for deletion")
			}
			if c.Kind != "delete-checkpoint" || c.Stage != "remove_data" || mustInt64(c.EffectNumber) < 2 || c.OperationID != p.hostOp || c.InstallationID != p.installation {
				t.Fatalf("the command is not shaped as designed: %+v", c.CommandHeader)
			}
		}
		if p.effects(t, "applied") != 2 || scalar[int](t, p.s.st, `SELECT COUNT(*) FROM audit_log WHERE action='app_platform.recovery_set_pruned'`) != 2 {
			t.Fatal("the deletions are not settled and audited")
		}
		// A second pass sends nothing: the applied rows are the record.
		before := len(p.runner.calls)
		if again := p.prune(t); again.Settled != 0 || len(p.runner.calls) != before {
			t.Fatalf("a second pass sent more: %v", p.runner.calls[before:])
		}
	})
}

// The set is retired on the Portal only through the settlement of an applied receipt, never at the decision.
func TestPruneLeavesTheSetUntouchedUntilTheRunnerAnswers(t *testing.T) {
	p := newPruneFixture(t, false)
	p.set(t, "verified", 100)
	victim := p.set(t, "verified", 50)
	p.set(t, "verified", 300)
	p.runner.loseExecuteReplyOnce, p.runner.readFails = true, true
	p.prune(t)
	if p.state(t, victim) != "verified" {
		t.Fatal("a set with no verdict was retired on the Portal")
	}
}

// Test 16. Fails if any of these sets, each beyond the retention, is given a deletion: the exclusions are the only
// thing between the sweeper and a checkpoint something is using.
func TestPruneNeverSelectsAHeldSet(t *testing.T) {
	cases := map[string]func(t *testing.T, p *pruneFixture, victim string){
		"restoring": func(t *testing.T, p *pruneFixture, victim string) {
			mustExec(t, p.s.st, `UPDATE app_platform_recovery_sets SET state='restoring' WHERE id=?`, victim)
		},
		"review_required": func(t *testing.T, p *pruneFixture, victim string) {
			mustExec(t, p.s.st, `UPDATE app_platform_recovery_sets SET state='review_required' WHERE id=?`, victim)
		},
		"issued import grant":   func(t *testing.T, p *pruneFixture, victim string) { p.grant(t, victim, "issued") },
		"consumed import grant": func(t *testing.T, p *pruneFixture, victim string) { p.grant(t, victim, "consumed") },
		"installation with an unfinished operation": func(t *testing.T, p *pruneFixture, victim string) {
			mustExec(t, p.s.st, `UPDATE app_platform_operations SET stage='queued',completed_ms=NULL WHERE id=?`, p.hostOp)
		},
		"installation with an operation in recovery_required": func(t *testing.T, p *pruneFixture, victim string) {
			mustExec(t, p.s.st, `UPDATE app_platform_operations SET stage='recovery_required' WHERE id=?`, p.hostOp)
		},
		"restore pause": func(t *testing.T, p *pruneFixture, victim string) {
			mustExec(t, p.s.st, `INSERT INTO meta(k,v) VALUES('app_platform_restore_paused','1') ON CONFLICT(k) DO UPDATE SET v=excluded.v`)
		},
	}
	for name, hold := range cases {
		t.Run(name, func(t *testing.T) {
			p := newPruneFixture(t, false)
			victim := p.set(t, "verified", 100)
			p.set(t, "verified", 300)
			hold(t, p, victim)
			p.prune(t)
			if len(p.runner.calls) != 0 || p.effects(t, "prepared") != 0 || scalar[int](t, p.s.st, `SELECT COUNT(*) FROM app_platform_operation_effects WHERE kind='delete_checkpoint'`) != 0 {
				t.Fatalf("a held set was given a deletion: %v", p.runner.calls)
			}
		})
	}
}

func (p *pruneFixture) grant(t *testing.T, set, state string) {
	t.Helper()
	var consumed any
	if state == "consumed" {
		consumed = int64(5)
	}
	platformTestInsert(t, p.s.st.poolExec(), "import_grants", map[string]any{
		"id": platformFixtureID(9800 + p.sets), "recovery_set_id": set, "runtime_id": p.runtimeID, "controller_epoch": p.epoch,
		"state": state, "consumed_ms": consumed, "created_ms": 1, "updated_ms": 1,
	})
}

// A set whose restore pause ends is selected again: the pause only defers.
func TestPruneResumesOnceTheRestorePauseEnds(t *testing.T) {
	p := newPruneFixture(t, false)
	victim := p.set(t, "verified", 100)
	p.set(t, "verified", 300)
	mustExec(t, p.s.st, `INSERT INTO meta(k,v) VALUES('app_platform_restore_paused','1') ON CONFLICT(k) DO UPDATE SET v=excluded.v`)
	p.prune(t)
	mustExec(t, p.s.st, `DELETE FROM meta WHERE k='app_platform_restore_paused'`)
	if p.prune(t); p.state(t, victim) != "deleted" {
		t.Fatal("the set was not deleted once the pause ended")
	}
}

// Test 20. Fails if a lost receipt leads to a second effect id, or to a settlement without the runner's receipt.
func TestPruneReadsALostReceiptBackUnderTheSameEffectId(t *testing.T) {
	eachPlatformDriver(t, func(t *testing.T, pg bool) {
		p := newPruneFixture(t, pg)
		victim := p.set(t, "verified", 100)
		p.set(t, "verified", 300)
		p.runner.loseExecuteReplyOnce = true
		if result := p.prune(t); result.Settled != 1 {
			t.Fatalf("the read-back did not settle the deletion: %+v calls %v", result, p.runner.calls)
		}
		if p.runner.count("delete") != 1 || p.runner.count("read") != 1 || len(p.runner.effectIDs()) != 1 || p.state(t, victim) != "deleted" {
			t.Fatalf("calls %v ids %v", p.runner.calls, p.runner.effectIDs())
		}
		want := platformPruneEffectID(victim, p.checkpoint(t, victim), 0)
		if !p.runner.effectIDs()[want] {
			t.Fatal("the effect id is not the one derived from the set, its checkpoint and the attempt")
		}
	})
}

// A deletion left unknown (no reply, no read-back) is resolved on a later tick under the same id, and nothing is
// signed under another.
func TestPruneResolvesAnUnknownDeletionOnALaterTickWithoutANewIdentity(t *testing.T) {
	p := newPruneFixture(t, false)
	victim := p.set(t, "verified", 100)
	p.set(t, "verified", 300)
	p.runner.loseExecuteReplyOnce, p.runner.readFails = true, true
	if result := p.prune(t); result.Settled != 0 {
		t.Fatalf("an unresolved deletion counted as settled: %+v", result)
	}
	if p.effects(t, "unknown") != 1 || p.state(t, victim) != "verified" {
		t.Fatal("the deletion with no verdict is not unknown")
	}
	p.runner.readFails = false
	if result := p.prune(t); result.Settled != 1 {
		t.Fatalf("the later tick: %+v", result)
	}
	if len(p.runner.effectIDs()) != 1 || len(p.runner.gone) != 1 || p.state(t, victim) != "deleted" {
		t.Fatalf("ids %v gone %v", p.runner.effectIDs(), p.runner.gone)
	}
}

// The runner proves it never claimed the effect (installation busy there): the row stays prepared, nothing is
// settled, and the same effect id is sent again on a later tick.
func TestPruneSendsTheSameEffectAgainWhenTheRunnerNeverClaimedIt(t *testing.T) {
	p := newPruneFixture(t, false)
	victim := p.set(t, "verified", 100)
	p.set(t, "verified", 300)
	p.runner.unclaimed = true
	if result := p.prune(t); result.Settled != 0 || result.Unclaimed != 1 {
		t.Fatalf("%+v", result)
	}
	if p.effects(t, "prepared") != 1 || p.state(t, victim) != "verified" {
		t.Fatal("an unclaimed deletion is not left prepared")
	}
	p.runner.unclaimed = false
	if result := p.prune(t); result.Settled != 1 {
		t.Fatalf("%+v", result)
	}
	if len(p.runner.effectIDs()) != 1 || p.state(t, victim) != "deleted" {
		t.Fatalf("the second attempt used a new identity: %v", p.runner.effectIDs())
	}
}

func TestPruneReportsABusyRunnerAndReadsTheSameEffectAgain(t *testing.T) {
	p := newPruneFixture(t, false)
	victim := p.set(t, "verified", 100)
	p.set(t, "verified", 300)
	p.runner.pendingFor = 1
	if result := p.prune(t); result.Settled != 0 || result.Busy != 1 || p.state(t, victim) != "verified" || p.effects(t, "unknown") != 1 {
		t.Fatalf("%+v", result)
	}
	if result := p.prune(t); result.Settled != 1 || len(p.runner.effectIDs()) != 1 {
		t.Fatalf("%+v ids %v", result, p.runner.effectIDs())
	}
}

// Test 21. A refusal that may pass is retried under attempt+1, a new id; one that will not is recorded as a refusal
// for an operator and never sent again. Fails if the retry reuses the refused id (the runner would replay the
// refusal), if the mismatch is retried, or if the refusal leaves no audit trace.
func TestPruneRetriesAPassingRefusalUnderTheNextAttemptAndStopsOnAFinalOne(t *testing.T) {
	eachPlatformDriver(t, func(t *testing.T, pg bool) {
		p := newPruneFixture(t, pg)
		latest := p.set(t, "verified", 100)
		mismatch := p.set(t, "verified", 150)
		p.set(t, "verified", 300)
		p.runner.refuse[p.checkpoint(t, latest)] = journal.DeleteOutcomeLatest
		p.runner.refuse[p.checkpoint(t, mismatch)] = journal.DeleteOutcomeIdentityMismatch
		p.prune(t)
		if p.state(t, latest) != "verified" || p.state(t, mismatch) != "verified" || p.effects(t, "reverted") != 2 {
			t.Fatalf("a refusal changed a set or was not recorded: %s %s", p.state(t, latest), p.state(t, mismatch))
		}
		if scalar[int](t, p.s.st, `SELECT COUNT(*) FROM audit_log WHERE action='app_platform.recovery_set_prune_refused'`) != 1 {
			t.Fatal("the final refusal is not recorded for an operator")
		}
		status, statusErr := p.s.platformImageStorageStatus(context.Background())
		if statusErr != nil || len(status.PruneRefused) != 1 || status.PruneRefused[0].RecoverySetID != mismatch {
			t.Fatalf("the storage status does not carry the refusal: %+v %v", status.PruneRefused, statusErr)
		}
		refused, err := p.s.platformPruneRefusals(context.Background())
		if err != nil || len(refused) != 1 || refused[0].RecoverySetID != mismatch || refused[0].Reason != journal.DeleteOutcomeIdentityMismatch {
			t.Fatalf("the status does not report the mismatch for an operator: %+v %v", refused, err)
		}
		// The checkpoint is no longer the newest of a live namespace: the retry is accepted.
		delete(p.runner.refuse, p.checkpoint(t, latest))
		p.prune(t)
		if p.state(t, latest) != "deleted" {
			t.Fatal("the passing refusal was not retried")
		}
		ids := p.runner.effectIDs()
		for _, attempt := range []int{0, 1} {
			if !ids[platformPruneEffectID(latest, p.checkpoint(t, latest), attempt)] {
				t.Fatalf("attempt %d was never signed", attempt)
			}
		}
		if ids[platformPruneEffectID(latest, p.checkpoint(t, latest), 2)] {
			t.Fatal("a third attempt was signed")
		}
		// The mismatch is not sent again, however often the sweep runs.
		sent := len(p.runner.sent)
		p.prune(t)
		p.prune(t)
		if len(p.runner.sent) != sent || p.state(t, mismatch) != "verified" {
			t.Fatal("a refusal that does not pass was retried")
		}
	})
}

// A refusal that may pass waits out the backoff, and stops after the attempts run out.
func TestPruneBacksOffAfterARefusalAndStopsAfterTheAttemptsRunOut(t *testing.T) {
	p := newPruneFixture(t, false)
	victim := p.set(t, "verified", 100)
	p.set(t, "verified", 300)
	p.runner.refuse[p.checkpoint(t, victim)] = journal.DeleteOutcomeInUse
	platformPruneBackoff = time.Hour
	p.prune(t)
	p.prune(t)
	if len(p.runner.sent) != 1 {
		t.Fatalf("a refused deletion was retried inside its backoff: %d sends", len(p.runner.sent))
	}
	platformPruneBackoff = 0
	for range platformPruneMaxAttempts + 3 {
		p.prune(t)
	}
	if len(p.runner.sent) != platformPruneMaxAttempts {
		t.Fatalf("%d attempts, want the bound %d", len(p.runner.sent), platformPruneMaxAttempts)
	}
	refused, err := p.s.platformPruneRefusals(context.Background())
	if err != nil || len(refused) != 1 || refused[0].Attempts != platformPruneMaxAttempts {
		t.Fatalf("the exhausted set is not reported: %+v %v", refused, err)
	}
}

// A later effect id for a checkpoint the runner already retired settles absent, and the set is retired.
func TestPruneTreatsAnAbsentReceiptAsApplied(t *testing.T) {
	p := newPruneFixture(t, false)
	victim := p.set(t, "verified", 100)
	p.set(t, "verified", 300)
	p.runner.gone = append(p.runner.gone, p.checkpoint(t, victim))
	p.prune(t)
	if p.state(t, victim) != "deleted" || p.effects(t, "applied") != 1 {
		t.Fatal("an absent receipt did not retire the set")
	}
}

// The effect numbers of the stage are bounded; a host with none free records it and waits.
func TestPruneWaitsWhenTheHostHasNoFreeEffectNumber(t *testing.T) {
	p := newPruneFixture(t, false)
	victim := p.set(t, "verified", 100)
	p.set(t, "verified", 300)
	addEffectRow(t, p.s, p.hostOp, "delete-data", "remove_data", 32, platformFixtureID(9990))
	result := p.prune(t)
	if result.Exhausted != 1 || len(p.runner.calls) != 0 || p.state(t, victim) != "verified" {
		t.Fatalf("%+v %v", result, p.runner.calls)
	}
}

// cloneRow copies one row of a platform table under a new id, with the given columns replaced.
func cloneRow(t *testing.T, st *Store, table, oldID string, replace map[string]any) {
	t.Helper()
	full := platformPrefix + table
	rows, err := st.query(`SELECT * FROM `+full+` WHERE id=?`, oldID)
	if err != nil {
		t.Fatal(err)
	}
	columns, err := rows.Columns()
	rows.Close()
	if err != nil {
		t.Fatal(err)
	}
	var exprs []string
	var args []any
	for _, c := range columns {
		if v, ok := replace[c]; ok {
			exprs, args = append(exprs, "?"), append(args, v)
		} else {
			exprs = append(exprs, c)
		}
	}
	mustExec(t, st, `INSERT INTO `+full+`(`+strings.Join(columns, ",")+`) SELECT `+strings.Join(exprs, ",")+` FROM `+full+` WHERE id=?`, append(args, oldID)...)
}

// newHost is what a later operation of the installation does to the host of an open deletion: a newer finished
// operation carries the installation's new fence, so the old host can no longer sign for the installation, and the
// next deletion is hosted by the new one.
func (p *pruneFixture) newHost(t *testing.T) {
	t.Helper()
	plan := scalar[string](t, p.s.st, `SELECT review_plan_id FROM app_platform_operations WHERE id=?`, p.hostOp)
	fence := scalar[int64](t, p.s.st, `SELECT fence FROM app_platform_operations WHERE id=?`, p.hostOp) + 1
	completed := scalar[int64](t, p.s.st, `SELECT completed_ms FROM app_platform_operations WHERE id=?`, p.hostOp) + 1000
	newPlan, newOp := platformFixtureID(9300+p.sets), platformFixtureID(9400+p.sets)
	p.sets++
	cloneRow(t, p.s.st, "review_plans", plan, map[string]any{"id": newPlan})
	cloneRow(t, p.s.st, "operations", p.hostOp, map[string]any{
		"id": newOp, "review_plan_id": newPlan, "idempotency_sha256": platformDigest([]byte(newOp)), "fence": fence,
		"completed_ms": completed, "created_ms": completed, "updated_ms": completed,
	})
	mustExec(t, p.s.st, `UPDATE app_platform_installations SET control_fence=? WHERE id=?`, fence, p.installation)
	p.hostOp = newOp
}

func (p *pruneFixture) decided(t *testing.T, set string) bool {
	t.Helper()
	decided, err := platformRecoverySetDeletionDecided(p.s.st.poolExec(), set)
	if err != nil {
		t.Fatal(err)
	}
	return decided
}

func (p *pruneFixture) retained(t *testing.T, set string) bool {
	t.Helper()
	retained, err := loadPlatformRetainedSets(p.s.st.poolExec())
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range retained {
		if r.id == set {
			return true
		}
	}
	return false
}

// A deletion with no verdict is never reverted without the runner's word. The host's fence moves: the row is stranded,
// stays open, and the set stays decided, so restore and the recovery bundle refuse it although its checkpoint may be
// gone. A new attempt on the current host then settles it, and the old row is closed by that verdict. Fails if the
// stranded row is reverted (the set would count retained and restorable again), or if the set is offered meanwhile.
func TestPruneNeverRevertsADeletionWithNoVerdictAndSettlesItWithANewAttempt(t *testing.T) {
	eachPlatformDriver(t, func(t *testing.T, pg bool) {
		p := newPruneFixture(t, pg)
		victim := p.set(t, "verified", 100)
		p.set(t, "verified", 300)
		p.runner.loseExecuteReplyOnce, p.runner.readFails = true, true
		p.prune(t)
		if p.effects(t, "unknown") != 1 || !p.decided(t, victim) || p.retained(t, victim) {
			t.Fatal("setup: the lost receipt did not leave a decided, unknown deletion")
		}
		p.newHost(t)
		p.runner.readFails = false
		sent := len(p.runner.sent)
		p.prune(t)
		if p.effects(t, "reverted") != 0 || p.effects(t, "unknown") != 1 || len(p.runner.sent) != sent {
			t.Fatal("a deletion with no verdict was reverted, or sent under a fence it was not written for")
		}
		if !p.decided(t, victim) || p.retained(t, victim) || p.state(t, victim) != "verified" {
			t.Fatal("the set is offered for restore or the bundle while its deletion has no verdict")
		}
		if scalar[string](t, p.s.st, `SELECT receipt_json FROM app_platform_operation_effects WHERE kind='delete_checkpoint'`) !=
			`{"recovery_set_id":"`+victim+`","checkpoint_id":"`+p.checkpoint(t, victim)+`","source":"retention","attempt":0,"stranded":true}` {
			t.Fatal("the row is not marked stranded")
		}
		if p.prune(t); len(p.runner.sent) != sent+1 {
			t.Fatalf("the next pass sent %d commands, want the one new attempt", len(p.runner.sent)-sent)
		}
		if p.state(t, victim) != "deleted" || p.effects(t, "applied") != 1 || p.effects(t, "unknown") != 0 {
			t.Fatal("the new attempt did not settle the set")
		}
		if scalar[int](t, p.s.st, `SELECT COUNT(*) FROM app_platform_operation_effects WHERE kind='delete_checkpoint' AND state='reverted' AND receipt_json LIKE '%superseded_applied%'`) != 1 {
			t.Fatal("the stranded row was not closed by the verdict that settled the set")
		}
		ids := p.runner.effectIDs()
		for _, attempt := range []int{0, 1} {
			if !ids[platformPruneEffectID(victim, p.checkpoint(t, victim), attempt)] {
				t.Fatalf("attempt %d was not signed", attempt)
			}
		}
	})
}

// If the stranded claim never happened, the new attempt is refused as an ordinary refusal; the old row is closed as
// never claimed, and the set is retained again only then.
func TestPruneStrandedDeletionTheRunnerNeverClaimedIsClosedByTheRefusal(t *testing.T) {
	p := newPruneFixture(t, false)
	victim := p.set(t, "verified", 100)
	p.set(t, "verified", 300)
	p.runner.unclaimed = true
	p.prune(t)
	p.runner.unclaimed = false
	p.newHost(t)
	p.runner.refuse[p.checkpoint(t, victim)] = journal.DeleteOutcomeLatest
	p.prune(t)
	if !p.decided(t, victim) {
		t.Fatal("the set was released before any verdict")
	}
	p.prune(t)
	if p.effects(t, "unknown") != 0 || p.effects(t, "prepared") != 0 || p.decided(t, victim) || !p.retained(t, victim) {
		t.Fatalf("the refusal did not release the set: unknown=%d prepared=%d", p.effects(t, "unknown"), p.effects(t, "prepared"))
	}
	if scalar[int](t, p.s.st, `SELECT COUNT(*) FROM app_platform_operation_effects WHERE receipt_json LIKE '%superseded_not_claimed%'`) != 1 {
		t.Fatal("the earlier row is not closed as never claimed")
	}
}

// A deletion issued to a runtime that was retired cannot be read back by anyone, and is never reverted: the set stays
// decided, the row takes no window slot, and the storage status reports it for an operator.
func TestPruneKeepsADeletionOfARetiredRuntimeOpenAndReportsIt(t *testing.T) {
	p := newPruneFixture(t, false)
	victim := p.set(t, "verified", 100)
	p.set(t, "verified", 300)
	p.runner.loseExecuteReplyOnce, p.runner.readFails = true, true
	p.prune(t)
	mustExec(t, p.s.st, `UPDATE app_platform_runtime_bindings SET enabled=0 WHERE id=?`, p.runtimeID)
	sent := len(p.runner.calls)
	p.prune(t)
	if p.effects(t, "unknown") != 1 || p.effects(t, "reverted") != 0 || len(p.runner.calls) != sent || !p.decided(t, victim) {
		t.Fatal("the deletion of a retired runtime was closed or sent")
	}
	refused, err := p.s.platformPruneRefusals(context.Background())
	if err != nil || len(refused) != 1 || refused[0].RecoverySetID != victim || refused[0].Reason != "runtime_retired" {
		t.Fatalf("the status does not report it: %+v %v", refused, err)
	}
}

// A release whose descriptor is gone cannot be sent (the transport requires it). Nothing is prepared, the set stays,
// and the pass does not fail.
func TestPruneLeavesASetWhoseReleaseDescriptorIsMissing(t *testing.T) {
	p := newPruneFixture(t, false)
	victim := p.set(t, "verified", 100)
	p.set(t, "verified", 300)
	mustExec(t, p.s.st, `UPDATE app_platform_releases SET descriptor=? WHERE sha256=?`, []byte{}, p.release)
	p.prune(t)
	if len(p.runner.calls) != 0 || p.state(t, victim) != "verified" || p.effects(t, "prepared") != 0 {
		t.Fatal("a deletion was begun without a descriptor to send")
	}
}

// Fails if the command the production seam sends is not the exact signed command designed for it: every field is
// compared with a value taken from the set, not from the code under test, and the signatures are verified with the
// runtime's authority.
func TestPruneSignsTheExactCommandTheSeamSends(t *testing.T) {
	p := newPruneFixture(t, false)
	victim := p.set(t, "verified", 100)
	p.set(t, "verified", 300)
	p.prune(t)
	if len(p.runner.sent) != 1 {
		t.Fatalf("%d commands sent", len(p.runner.sent))
	}
	c := p.runner.sent[0]
	h := c.CommandHeader
	want := map[string]string{
		"format": "portal.runner.delete-checkpoint-command/1-dev.1", "stage": "remove_data", "kind": "delete-checkpoint",
		"operation_id": p.hostOp, "installation_id": p.installation, "controller_epoch": p.epoch,
		"effect_id":          platformPruneEffectID(victim, p.checkpoint(t, victim), 0),
		"installation_fence": scalar[string](t, p.s.st, `SELECT CAST(control_fence AS TEXT) FROM app_platform_installations WHERE id=?`, p.installation),
		"effect_number":      "2",
	}
	got := map[string]string{
		"format": h.Format, "stage": h.Stage, "kind": h.Kind, "operation_id": h.OperationID, "installation_id": h.InstallationID,
		"controller_epoch": h.ControllerEpoch, "effect_id": h.EffectID, "installation_fence": h.InstallationFence, "effect_number": h.EffectNumber,
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
	payload := map[string]string{
		"recovery_set_id": victim, "checkpoint_id": p.checkpoint(t, victim), "namespace_id": p.namespace, "source_operation_id": p.hostOp,
		"release_sha256": p.release, "manifest_sha256": platformDigest([]byte("archive " + victim)), "archive_bytes": "4096", "data_format": "1", "writer_fence": "1",
	}
	rawPayload, err := json.Marshal(c.Payload)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]string
	if err := json.Unmarshal(rawPayload, &decoded); err != nil || len(decoded) != len(payload) {
		t.Fatalf("payload %s: %v", rawPayload, err)
	}
	for k, v := range payload {
		if decoded[k] != v {
			t.Errorf("payload %s = %q, want %q", k, decoded[k], v)
		}
	}
	// The bytes on the wire are the canonical encoding of that command and carry a valid signature.
	if encoded, _ := json.Marshal(c); !strings.Contains(string(encoded), `"payload_sha256":"`+c.PayloadSHA256+`"`) {
		t.Fatal("the command carries no payload digest")
	}
	if digest, err := control.DeleteCheckpointLogicalDigest(c); err != nil || digest != c.PayloadSHA256 {
		t.Fatalf("the payload digest is not the logical digest of the command: %v", err)
	}
}

// The production seam must name the runner's delete-checkpoint actions, each with the signature made for it: the
// execute action with the command signature and the read action with the read signature. The transport itself needs a
// root-owned socket, so this reads the source as the scheduler guard test does.
func TestPruneProductionSeamUsesTheDeleteCheckpointActions(t *testing.T) {
	raw, err := os.ReadFile("app_platform_recovery_prune.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(raw)
	for _, want := range []string{
		"transport.DeleteCheckpointExecute, call.raw, call.signature, call.authority, call.descriptor",
		"transport.DeleteCheckpointRead, call.raw, call.readSignature, call.authority, call.descriptor",
	} {
		if strings.Count(source, want) != 1 {
			t.Errorf("the production seam does not contain %q", want)
		}
	}
	var _ platformPruneRunner = platformImageTransport{}
}

// Both operation endings and the idle tick must run the pruning step: an ending that forgot it would leave archives.
func TestEveryOperationEndingAndTheIdleTickPruneRecoverySets(t *testing.T) {
	raw, err := os.ReadFile("app_platform_install_scheduler.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(raw)
	if !strings.Contains(source, "w.server.pruneRecoverySetsOnTick(bulkCtx, w.endpoint)") {
		t.Fatal("the idle tick does not prune recovery sets")
	}
	if !strings.Contains(source, "w.server.pruneRecoverySetsOnTick(ctx, w.endpoint)") {
		t.Fatal("an operation ending does not prune recovery sets")
	}
	// reclaimImages is what both operation endings call (TestEveryOperationEndingSweepsBlobsAndThenReclaimsImages).
	if strings.Count(source, "w.reclaimImages(ctx)") < 2 {
		t.Fatal("the operation endings no longer share the reclaim hook the pruning step hangs on")
	}
}

func TestSchedulerIdleTickPrunesRecoverySets(t *testing.T) {
	p := newPruneFixture(t, false)
	victim := p.set(t, "verified", 100)
	p.set(t, "verified", 300)
	w, err := newPlatformInstallScheduler(p.s, p.endpoint, platformFixtureID(933), 50*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	_ = w.tick(context.Background())
	if p.state(t, victim) != "deleted" {
		t.Fatalf("the idle tick did not prune: %v", p.runner.calls)
	}
}

func TestDeleteCheckpointKindIsInTheEffectAllowList(t *testing.T) {
	if !platformEffectKindAllowed("remove_data", platformDeleteCheckpointKind) || !platformEffectKindAllowed("remove_data", "delete-data") {
		t.Fatal("remove_data does not accept delete_checkpoint beside delete-data")
	}
	if platformEffectKindAllowed("stage", platformDeleteCheckpointKind) || platformEffectKindAllowed("start", platformDeleteCheckpointKind) {
		t.Fatal("delete_checkpoint is allowed in a stage it must not be")
	}
}
