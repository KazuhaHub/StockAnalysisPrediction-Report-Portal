//go:build linux || darwin

package journal

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/driver"
)

type fakeInspector struct {
	inspection driver.TargetInspection
	err        error
	calls      int
	// listed is what the namespace listing returns until a stray is removed; removeErr fails the removal.
	listed    []driver.NamespaceContainer
	listErr   error
	removeErr error
	removed   []string
	// beforeRemove and afterRemove observe each removal.
	beforeRemove, afterRemove func()
}

func (f *fakeInspector) ListNamespaceContainers(context.Context, string) ([]driver.NamespaceContainer, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return append([]driver.NamespaceContainer(nil), f.listed...), nil
}

func (f *fakeInspector) RemoveStrayContainer(_ context.Context, nativeID string) error {
	if f.removeErr != nil {
		return f.removeErr
	}
	if f.beforeRemove != nil {
		f.beforeRemove()
	}
	if f.afterRemove != nil {
		defer f.afterRemove()
	}
	f.removed = append(f.removed, nativeID)
	kept := f.listed[:0:0]
	for _, c := range f.listed {
		if c.NativeID != nativeID {
			kept = append(kept, c)
		}
	}
	f.listed = kept
	return nil
}

func (f *fakeInspector) InspectTarget(context.Context, string) (driver.TargetInspection, error) {
	f.calls++
	return f.inspection, f.err
}

func (r reservedStartFixture) settleCommand(t *testing.T, effect int) ([]byte, []byte) {
	t.Helper()
	return r.modeCommand(t, effect, control.SettleModeSettle)
}

func (r reservedStartFixture) modeCommand(t *testing.T, effect int, mode string) ([]byte, []byte) {
	t.Helper()
	now := time.Now().UTC().UnixMilli()
	command := control.SettleUpdateStartCommand{
		Format: control.SettleUpdateStartFormat, Mode: mode, EffectID: fixtureID(2800 + effect), OperationID: fixtureID(2890),
		InstallationFence: r.command.InstallationFence, IssuedMS: strconv.FormatInt(now, 10), ExpiresMS: strconv.FormatInt(now+25000, 10),
		StartRaw: r.raw, StartReadSignature: updateStartReadSignature(t, r.raw),
	}
	var err error
	if command.PayloadSHA256, err = control.SettleUpdateStartLogicalDigest(command); err != nil {
		t.Fatal(err)
	}
	raw, sig, err := control.SignSettleUpdateStart(command, ed25519.NewKeyFromSeed(bytes.Repeat([]byte{30}, 32)), importAuthority(), now)
	if err != nil {
		t.Fatal(err)
	}
	return raw, sig
}

// orphaned claims the reserved start and leaves it: the claim has no executor. When bound, the claim records the
// preflight evidence a start that got as far as creating a container would have recorded.
func (r reservedStartFixture) orphaned(t *testing.T, bound bool) {
	t.Helper()
	if _, err := r.f.store.ClaimUpdateStart(context.Background(), r.raw, r.sig); err != nil {
		t.Fatal(err)
	}
	if !bound {
		return
	}
	var raw string
	if err := r.f.store.db.QueryRow(`SELECT receipt_json FROM runner_commands WHERE effect_id=?`, r.command.EffectID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var marker UpdateStartClaimMarker
	if err := json.Unmarshal([]byte(raw), &marker); err != nil {
		t.Fatal(err)
	}
	command := r.command
	marker.Preflight = &UpdateStartPreflightEvidence{
		Format: "portal.runner.update-start-preflight/1-dev.1", ImageID: "sha256:" + strings.Repeat("a", 64), Platform: command.Payload.Platform,
		PolicySHA256: strings.Repeat("b", 64), MountsSHA256: strings.Repeat("c", 64), RootFilesystemUUID: command.Payload.RootFilesystemUUID,
		RootInode: command.Payload.RootInode, ProjectID: command.Payload.ProjectID, Device: command.Payload.Device, NamespaceInode: command.Payload.NamespaceInode,
		QuotaBytes: command.Payload.QuotaBytes, QuotaInodes: command.Payload.QuotaInodes, SeccompSHA256: driver.UpdateStartSeccompSHA256(command.Payload.SandboxProfile),
	}
	marker.PreflightBoundMS = marker.ClaimedMS
	encoded, _ := json.Marshal(marker)
	mustJournalExec(t, r.f.store.db, `UPDATE runner_commands SET receipt_json=? WHERE effect_id=?`, string(encoded), r.command.EffectID)
}

func TestSettleOrphanedStartWithNoContainerIsNotRun(t *testing.T) {
	r := reservedStart(t)
	r.orphaned(t, false)
	inspector := &fakeInspector{}
	raw, sig := r.settleCommand(t, 1)
	receipt, err := r.f.store.SettleUpdateStart(context.Background(), raw, sig, inspector)
	if err != nil || receipt.Outcome != "never_ran" || receipt.ContainerState != "" || receipt.StartEffectID != r.command.EffectID || receipt.NativeID != control.ReservedStartNativeID(r.command.Payload.ResourceID) {
		t.Fatalf("receipt: %+v %v", receipt, err)
	}
	if r.startState(t) != "reverted" {
		t.Fatalf("the start is %s", r.startState(t))
	}
	if desired, observed, revision := r.resource(t); desired != "absent" || observed != "absent" || revision != 4 {
		t.Fatalf("resource: %s %s %d", desired, observed, revision)
	}
	held, err := r.f.store.ReadUpdateStartRunningOrAbsence(context.Background(), r.raw, updateStartReadSignature(t, r.raw))
	if status, ok := held.(UpdateStartAbsence); err != nil || !ok || status.State != "never_ran" {
		t.Fatalf("the settled start reads as %#v %v", held, err)
	}
	// A replay returns the same receipt and looks at nothing.
	again, err := r.f.store.SettleUpdateStart(context.Background(), raw, sig, inspector)
	if err != nil || again != receipt || inspector.calls != 1 {
		t.Fatalf("replay: %+v %v inspections=%d", again, err, inspector.calls)
	}
	readSig, err := control.SignSettleUpdateStartRead(raw, ed25519.NewKeyFromSeed(bytes.Repeat([]byte{30}, 32)), importAuthority(), time.Now().UTC().UnixMilli())
	if err != nil {
		t.Fatal(err)
	}
	if read, err := r.f.store.ReadSettleUpdateStartReceipt(context.Background(), raw, readSig); err != nil || read != receipt {
		t.Fatalf("read-back: %+v %v", read, err)
	}
}

// A container in any state is a start that ran: it is settled as applied with a receipt that records the state, and
// the ordinary removal takes it away. Nothing is created, started, stopped or removed by the settlement itself.
func TestSettleOrphanedStartWithAContainerIsApplied(t *testing.T) {
	for _, state := range []string{"created", "running", "exited"} {
		t.Run(state, func(t *testing.T) {
			r := reservedStart(t)
			r.orphaned(t, true)
			native := strings.Repeat("ab", 32)
			inspector := &fakeInspector{inspection: driver.TargetInspection{Found: true, NativeID: native, State: state}}
			raw, sig := r.settleCommand(t, 1)
			receipt, err := r.f.store.SettleUpdateStart(context.Background(), raw, sig, inspector)
			if err != nil || receipt.Outcome != "applied" || receipt.ContainerState != state || receipt.NativeID != native {
				t.Fatalf("receipt: %+v %v", receipt, err)
			}
			if r.startState(t) != "applied" {
				t.Fatalf("the start is %s", r.startState(t))
			}
			var running string
			if err := r.f.store.db.QueryRow(`SELECT receipt_json FROM runner_commands WHERE effect_id=?`, r.command.EffectID).Scan(&running); err != nil {
				t.Fatal(err)
			}
			if parsed, err := ParseUpdateStartRunningReceipt([]byte(running)); err != nil || parsed.NativeID != native {
				t.Fatalf("the start's running receipt: %+v %v", parsed, err)
			}
			if desired, observed, revision := r.resource(t); desired != "present" || observed != "running" || revision != 2 {
				t.Fatalf("resource: %s %s %d", desired, observed, revision)
			}
			held, err := r.f.store.ReadUpdateStartRunningOrAbsence(context.Background(), r.raw, updateStartReadSignature(t, r.raw))
			if _, ok := held.(UpdateStartRunningReceipt); err != nil || !ok {
				t.Fatalf("the settled start does not read back as running: %#v %v", held, err)
			}
			if again, err := r.f.store.SettleUpdateStart(context.Background(), raw, sig, inspector); err != nil || again != receipt || inspector.calls != 1 {
				t.Fatalf("replay: %+v %v inspections=%d", again, err, inspector.calls)
			}
		})
	}
}

func TestSettleUpdateStartRefusals(t *testing.T) {
	t.Run("a start that is still reserved", func(t *testing.T) {
		r := reservedStart(t)
		raw, sig := r.settleCommand(t, 1)
		inspector := &fakeInspector{}
		if _, err := r.f.store.SettleUpdateStart(context.Background(), raw, sig, inspector); !errors.Is(err, ErrUpdateStartNotOrphaned) {
			t.Fatalf("a reserved start was settled as an orphan: %v", err)
		}
		if inspector.calls != 0 || r.startState(t) != "prepared" {
			t.Fatal("a refusal inspected or changed something")
		}
	})
	t.Run("a start that is no longer unknown", func(t *testing.T) {
		r := reservedStart(t)
		r.orphaned(t, true)
		mustJournalExec(t, r.f.store.db, `UPDATE runner_commands SET state='applied' WHERE effect_id=?`, r.command.EffectID)
		raw, sig := r.settleCommand(t, 1)
		if _, err := r.f.store.SettleUpdateStart(context.Background(), raw, sig, &fakeInspector{}); !errors.Is(err, ErrUpdateStartNotOrphaned) {
			t.Fatalf("a start that is already applied was settled: %v", err)
		}
	})
	t.Run("the gate is held", func(t *testing.T) {
		r := reservedStart(t)
		r.orphaned(t, false)
		if err := r.f.store.acquireDataExecution(context.Background()); err != nil {
			t.Fatal(err)
		}
		defer r.f.store.releaseDataExecution()
		inspector := &fakeInspector{}
		raw, sig := r.settleCommand(t, 1)
		if _, err := r.f.store.SettleUpdateStart(context.Background(), raw, sig, inspector); !errors.Is(err, ErrManagedRemovalUnavailable) {
			t.Fatalf("a start that may be executing was settled: %v", err)
		}
		if inspector.calls != 0 || r.startState(t) != "unknown" {
			t.Fatal("a refusal inspected or changed something")
		}
	})
	t.Run("an execute-purpose start is not a start reference", func(t *testing.T) {
		r := reservedStart(t)
		r.orphaned(t, false)
		now := time.Now().UTC().UnixMilli()
		command := control.SettleUpdateStartCommand{
			Format: control.SettleUpdateStartFormat, Mode: control.SettleModeSettle, EffectID: fixtureID(2899), OperationID: fixtureID(2890), InstallationFence: r.command.InstallationFence,
			IssuedMS: strconv.FormatInt(now, 10), ExpiresMS: strconv.FormatInt(now+25000, 10), StartRaw: r.raw, StartReadSignature: r.sig,
		}
		command.PayloadSHA256, _ = control.SettleUpdateStartLogicalDigest(command)
		if _, _, err := control.SignSettleUpdateStart(command, ed25519.NewKeyFromSeed(bytes.Repeat([]byte{30}, 32)), importAuthority(), now); err == nil {
			t.Fatal("a settlement was signed over an execute-capable start signature")
		}
	})
	t.Run("a container the claim cannot account for", func(t *testing.T) {
		r := reservedStart(t)
		r.orphaned(t, false) // no preflight bound: nothing could have created a container
		raw, sig := r.settleCommand(t, 1)
		inspector := &fakeInspector{inspection: driver.TargetInspection{Found: true, NativeID: strings.Repeat("ab", 32), State: "running"}}
		if _, err := r.f.store.SettleUpdateStart(context.Background(), raw, sig, inspector); err == nil {
			t.Fatal("a container was accepted for a claim that never bound its preflight")
		}
		if r.startState(t) != "unknown" {
			t.Fatal("the start changed")
		}
	})
	t.Run("the native inspection fails", func(t *testing.T) {
		r := reservedStart(t)
		r.orphaned(t, false)
		raw, sig := r.settleCommand(t, 1)
		inspector := &fakeInspector{err: errTargetExists}
		if _, err := r.f.store.SettleUpdateStart(context.Background(), raw, sig, inspector); err == nil || r.startState(t) != "unknown" {
			t.Fatalf("an inspection that failed settled the start: %v", err)
		}
		inspector.err = nil
		if _, err := r.f.store.SettleUpdateStart(context.Background(), raw, sig, inspector); err != nil {
			t.Fatalf("the settlement did not converge: %v", err)
		}
	})
}

// The reserved-only removal is for a start nothing ever claimed. An orphan may have a container, so it is refused with
// the typed reason, and nothing changes.
func TestReservedRemovalRefusesAClaimedStartWithATypedReason(t *testing.T) {
	r := reservedStart(t)
	r.orphaned(t, false)
	prover := &fakeTargetProver{}
	raw, sig := r.removal(t, "stop", 1)
	if _, err := r.f.store.RemoveReservedUpdateStart(context.Background(), raw, sig, prover); !errors.Is(err, ErrUpdateStartNotReserved) {
		t.Fatalf("an orphaned start was removed as if it were reserved: %v", err)
	}
	if len(prover.calls) != 0 || r.startState(t) != "unknown" {
		t.Fatal("the refusal proved or changed something")
	}
}

// Settling makes no native change, so it inspects first and writes the outcome in one transaction: a failed
// inspection leaves no row behind, and no installation-wide check can be blocked by a stranded unknown row.
func TestSettleAndReservedRemovalLeaveNoRowAfterAFailedInspection(t *testing.T) {
	countUnknown := func(r reservedStartFixture) int {
		var n int
		if err := r.f.store.db.QueryRow(`SELECT COUNT(*) FROM runner_commands WHERE state='unknown' AND kind<>'update-start'`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	rows := func(r reservedStartFixture) int {
		var n int
		if err := r.f.store.db.QueryRow(`SELECT COUNT(*) FROM runner_commands`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	t.Run("settle", func(t *testing.T) {
		r := reservedStart(t)
		r.orphaned(t, false)
		before := rows(r)
		raw, sig := r.settleCommand(t, 1)
		if _, err := r.f.store.SettleUpdateStart(context.Background(), raw, sig, &fakeInspector{err: errTargetExists}); err == nil {
			t.Fatal("a failed inspection settled the start")
		}
		if rows(r) != before || countUnknown(r) != 0 {
			t.Fatalf("a failed inspection left rows behind: %d -> %d, unknown %d", before, rows(r), countUnknown(r))
		}
		if _, err := r.f.store.SettleUpdateStart(context.Background(), raw, sig, &fakeInspector{}); err != nil {
			t.Fatalf("a retry did not start clean: %v", err)
		}
	})
	t.Run("reserved removal", func(t *testing.T) {
		r := reservedStart(t)
		before := rows(r)
		raw, sig := r.removal(t, "stop", 1)
		if _, err := r.f.store.RemoveReservedUpdateStart(context.Background(), raw, sig, &fakeTargetProver{err: errTargetExists}); err == nil {
			t.Fatal("a failed proof removed the reserved start")
		}
		if rows(r) != before || countUnknown(r) != 0 {
			t.Fatalf("a failed proof left rows behind: %d -> %d", before, rows(r))
		}
		if _, err := r.f.store.RemoveReservedUpdateStart(context.Background(), raw, sig, &fakeTargetProver{}); err != nil {
			t.Fatalf("a retry did not start clean: %v", err)
		}
	})
}

func TestSweepRemovesOnlyUnaccountedContainersOfTheNamespace(t *testing.T) {
	removeTarget := func(r reservedStartFixture) {
		prover := &fakeTargetProver{}
		for i, stage := range []string{"stop", "remove_code"} {
			raw, sig := r.removal(t, stage, i+1)
			if _, err := r.f.store.RemoveReservedUpdateStart(context.Background(), raw, sig, prover); err != nil {
				t.Fatal(err)
			}
		}
	}
	t.Run("a stray is removed and read back", func(t *testing.T) {
		r := reservedStart(t)
		removeTarget(r)
		stray := strings.Repeat("ee", 32)
		inspector := &fakeInspector{listed: []driver.NamespaceContainer{{NativeID: stray, State: "running", ResourceID: r.command.Payload.ResourceID}}}
		raw, sig := r.modeCommand(t, 3, control.SettleModeSweep)
		receipt, err := r.f.store.SettleUpdateStart(context.Background(), raw, sig, inspector)
		if err != nil || receipt.Outcome != "swept" || receipt.Removed != "1" || len(inspector.removed) != 1 || inspector.removed[0] != stray {
			t.Fatalf("sweep: %+v %v %v", receipt, err, inspector.removed)
		}
		again, err := r.f.store.SettleUpdateStart(context.Background(), raw, sig, &fakeInspector{listed: inspector.listed, listErr: errTargetExists})
		if err != nil || again != receipt {
			t.Fatalf("a replay inspected again or differed: %+v %v", again, err)
		}
	})
	t.Run("a clean namespace sweeps nothing", func(t *testing.T) {
		r := reservedStart(t)
		removeTarget(r)
		raw, sig := r.modeCommand(t, 3, control.SettleModeSweep)
		receipt, err := r.f.store.SettleUpdateStart(context.Background(), raw, sig, &fakeInspector{})
		if err != nil || receipt.Outcome != "swept" || receipt.Removed != "0" {
			t.Fatalf("sweep: %+v %v", receipt, err)
		}
	})
	t.Run("accounted containers are left alone", func(t *testing.T) {
		r := reservedStart(t)
		removeTarget(r)
		c := r.command
		accounted := func(id int, native string, desired, observed string) {
			mustJournalExec(t, r.f.store.db, `INSERT INTO runner_resources(id,installation_id,data_namespace_id,kind,native_id,release_sha256,generation,controller_epoch,control_fence,writer_fence,desired_state,observed_state,ownership_sha256,revision,observed_ms,created_ms,updated_ms) VALUES(?,?,?,'container',?,?,1,?,1,1,?,?,?,1,NULL,1,1)`,
				fixtureID(id), c.InstallationID, c.Payload.NamespaceID, native, c.Payload.TargetReleaseSHA256, c.ControllerEpoch, desired, observed, strings.Repeat("b", 64))
		}
		source, sibling, gone := strings.Repeat("a1", 32), strings.Repeat("a2", 32), strings.Repeat("a3", 32)
		accounted(3001, source, "present", "running")  // the live source
		accounted(3002, sibling, "stopped", "stopped") // another resource of the installation
		accounted(3003, gone, "absent", "absent")      // a resource that is gone no longer accounts for its container
		stray := strings.Repeat("ee", 32)
		inspector := &fakeInspector{listed: []driver.NamespaceContainer{
			{NativeID: source, State: "running"}, {NativeID: sibling, State: "exited"}, {NativeID: gone, State: "exited"}, {NativeID: stray, State: "running"},
		}}
		raw, sig := r.modeCommand(t, 3, control.SettleModeSweep)
		receipt, err := r.f.store.SettleUpdateStart(context.Background(), raw, sig, inspector)
		if err != nil || receipt.Removed != "2" {
			t.Fatalf("sweep: %+v %v", receipt, err)
		}
		if len(inspector.removed) != 2 || slices.Contains(inspector.removed, source) || slices.Contains(inspector.removed, sibling) {
			t.Fatalf("an accounted container was removed: %v", inspector.removed)
		}
	})
	t.Run("a failed listing writes nothing", func(t *testing.T) {
		r := reservedStart(t)
		removeTarget(r)
		raw, sig := r.modeCommand(t, 3, control.SettleModeSweep)
		if _, err := r.f.store.SettleUpdateStart(context.Background(), raw, sig, &fakeInspector{listErr: errTargetExists}); err == nil {
			t.Fatal("a failed sweep settled")
		}
		var n int
		if err := r.f.store.db.QueryRow(`SELECT COUNT(*) FROM runner_commands WHERE effect_id=?`, fixtureID(2803)).Scan(&n); err != nil || n != 0 {
			t.Fatalf("a failed listing left a row: %d %v", n, err)
		}
	})
	t.Run("nothing is removed before the claim, and a crash after the removal reports the intended set", func(t *testing.T) {
		r := reservedStart(t)
		removeTarget(r)
		raw, sig := r.modeCommand(t, 3, control.SettleModeSweep)
		stray := strings.Repeat("ee", 32)
		inspector := &fakeInspector{listed: []driver.NamespaceContainer{{NativeID: stray, State: "running"}}}
		inspector.beforeRemove = func() {
			var state, claim string
			if err := r.f.store.db.QueryRow(`SELECT state,receipt_json FROM runner_commands WHERE effect_id=?`, fixtureID(2803)).Scan(&state, &claim); err != nil {
				t.Fatalf("no claim row before the removal: %v", err)
			}
			if state != "unknown" || !strings.Contains(claim, stray) {
				t.Fatalf("the claim does not record the intended set: %s %s", state, claim)
			}
		}
		// The runner removes the container, then dies before it records the outcome: the namespace read-back fails.
		inspector.afterRemove = func() { inspector.listErr = errTargetExists }
		if _, err := r.f.store.SettleUpdateStart(context.Background(), raw, sig, inspector); err == nil {
			t.Fatal("a crash before the outcome was recorded settled")
		}
		var state string
		if err := r.f.store.db.QueryRow(`SELECT state FROM runner_commands WHERE effect_id=?`, fixtureID(2803)).Scan(&state); err != nil || state != "unknown" {
			t.Fatalf("the claim is %q %v", state, err)
		}
		// The resend finds the namespace already clean and still reports what the claim intended.
		inspector.listErr = nil
		receipt, err := r.f.store.SettleUpdateStart(context.Background(), raw, sig, inspector)
		if err != nil || receipt.Outcome != "swept" || receipt.Removed != "1" {
			t.Fatalf("resend: %+v %v", receipt, err)
		}
		if again, err := r.f.store.SettleUpdateStart(context.Background(), raw, sig, &fakeInspector{listErr: errTargetExists}); err != nil || again != receipt {
			t.Fatalf("a replay differed: %+v %v", again, err)
		}
	})
	t.Run("a failed removal keeps the claim and the resend finishes it", func(t *testing.T) {
		r := reservedStart(t)
		removeTarget(r)
		raw, sig := r.modeCommand(t, 3, control.SettleModeSweep)
		stray := strings.Repeat("ee", 32)
		inspector := &fakeInspector{listed: []driver.NamespaceContainer{{NativeID: stray, State: "running"}}, removeErr: errTargetExists}
		if _, err := r.f.store.SettleUpdateStart(context.Background(), raw, sig, inspector); err == nil {
			t.Fatal("a failed removal settled")
		}
		inspector.removeErr = nil
		if receipt, err := r.f.store.SettleUpdateStart(context.Background(), raw, sig, inspector); err != nil || receipt.Removed != "1" {
			t.Fatalf("resend: %+v %v", receipt, err)
		}
	})
	t.Run("a foreign container blocks the sweep with a typed reason and removes nothing", func(t *testing.T) {
		r := reservedStart(t)
		removeTarget(r)
		raw, sig := r.modeCommand(t, 3, control.SettleModeSweep)
		inspector := &fakeInspector{listErr: driver.ErrForeignContainer, listed: []driver.NamespaceContainer{{NativeID: strings.Repeat("ee", 32), State: "running"}}}
		receipt, err := r.f.store.SettleUpdateStart(context.Background(), raw, sig, inspector)
		if err != nil || receipt.Outcome != "blocked" || receipt.Reason != SettleReasonForeignContainer || len(inspector.removed) != 0 {
			t.Fatalf("blocked: %+v %v %v", receipt, err, inspector.removed)
		}
		if !validSettleUpdateStartReceipt(receipt) {
			t.Fatal("the blocked answer is not a valid receipt")
		}
		var n int
		if err := r.f.store.db.QueryRow(`SELECT COUNT(*) FROM runner_commands WHERE effect_id=?`, fixtureID(2803)).Scan(&n); err != nil || n != 0 {
			t.Fatalf("a blocked sweep left a row: %d %v", n, err)
		}
	})
	t.Run("the target must be removed first", func(t *testing.T) {
		r := reservedStart(t)
		raw, sig := r.modeCommand(t, 3, control.SettleModeSweep)
		if _, err := r.f.store.SettleUpdateStart(context.Background(), raw, sig, &fakeInspector{}); err == nil {
			t.Fatal("a namespace was swept while the start's own target still existed")
		}
	})
}
