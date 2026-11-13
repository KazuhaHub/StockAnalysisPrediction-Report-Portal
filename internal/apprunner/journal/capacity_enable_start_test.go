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
)

// enableStartFixture reserves an enable-start for the update fixture's installation. The installation's old
// backend is the stopped source the start replaces; it is returned with the reserved start.
func enableStartFixture(t *testing.T) (checkpointFixture, control.UpdateStartCommand, []byte, []byte, string) {
	t.Helper()
	f, _, command, _, _ := updateStartJournalFixture(t)
	command.Kind = "enable-start"
	command.Payload.TargetReleaseSHA256 = command.Payload.SourceReleaseSHA256
	var err error
	if command.PayloadSHA256, err = control.UpdateStartLogicalDigest(command); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().UnixMilli()
	command.IssuedMS, command.ExpiresMS = strconv.FormatInt(now, 10), strconv.FormatInt(now+25000, 10)
	enrollment := enrollmentFixture(30)
	authority := control.Authority{DeploymentID: enrollment.DeploymentID, ControllerEpoch: enrollment.ControllerEpoch, ControlRevision: enrollment.ControlRevision, PublicKey: enrollment.PublicKey, Fingerprint: enrollment.Fingerprint}
	raw, sig, err := control.SignUpdateStart(command, ed25519.NewKeyFromSeed(bytes.Repeat([]byte{30}, 32)), authority, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.ReserveUpdateStart(context.Background(), raw, sig); err != nil {
		t.Fatal(err)
	}
	var source string
	if err := f.store.db.QueryRow(`SELECT id FROM runner_resources WHERE kind='container' AND installation_id=? AND id<>?`, command.InstallationID, command.Payload.ResourceID).Scan(&source); err != nil {
		t.Fatalf("no source container in the fixture: %v", err)
	}
	return f, command, raw, sig, source
}

func holdSource(t *testing.T, f checkpointFixture, source, installation string, memory, cpu int64) {
	t.Helper()
	if _, err := f.store.db.Exec(`UPDATE runner_resources SET desired_state='stopped',observed_state='stopped',revision=3 WHERE id=?`, source); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.db.Exec(`INSERT INTO runner_reservations(id,resource_id,installation_id,kind,memory_mib,cpu_millis,disk_bytes,image_bytes,state,created_ms,updated_ms) VALUES(?,?,?,'backend',?,?,0,0,'held',1,1)`, fixtureID(8400), source, installation, memory, cpu); err != nil {
		t.Fatal(err)
	}
}

// A full runner of eight backends, one of them the disabled installation's stopped source: enabling it replaces
// the source with the target, so it is admitted. Fails if source and target are both counted.
func TestEnableStartTransfersTheStoppedSourceReservation(t *testing.T) {
	f, command, raw, sig, source := enableStartFixture(t)
	if err := f.store.SetCapacityBudgets(CapacityBudgets{MemoryMiB: 1 << 20, CPUMillis: 1 << 20, DiskBytes: 1 << 40, ImageStoreBytes: 1 << 40, RunningBackends: 8}); err != nil {
		t.Fatal(err)
	}
	holdSource(t, f, source, command.InstallationID, 256, 500)
	for n := 1; n <= 7; n++ {
		if _, err := f.store.db.Exec(`INSERT INTO runner_reservations(id,resource_id,installation_id,kind,memory_mib,cpu_millis,disk_bytes,image_bytes,state,created_ms,updated_ms) VALUES(?,?,?,'backend',16,25,0,0,'held',1,1)`, fixtureID(8500+n), fixtureID(8600+n), fixtureID(8700+n)); err != nil {
			t.Fatal(err)
		}
	}
	ctx := withBackendResources(context.Background(), fixtureResources())
	if _, err := f.store.ClaimUpdateStart(ctx, raw, sig); err != nil {
		var refusal *CapacityRefusalError
		if errors.As(err, &refusal) {
			t.Fatalf("enabling the disabled backend was refused: %+v", refusal.Receipt)
		}
		t.Fatal(err)
	}
	if scalar(t, f.store.db, `SELECT COUNT(*) FROM runner_reservations WHERE kind='backend' AND state='held'`) != 8 {
		t.Fatal("the source and the target were both counted")
	}
	if scalar(t, f.store.db, `SELECT COUNT(*) FROM runner_reservations WHERE resource_id='`+source+`' AND state='released'`) != 1 {
		t.Fatal("the source reservation was not transferred")
	}
}

// Transfer applies only to a source of the same installation that is stopped or absent: a running source, or
// another installation's backend, is still counted next to the target.
func TestTransferRequiresAStoppedSourceOfTheSameInstallation(t *testing.T) {
	budgets := CapacityBudgets{MemoryMiB: 100, CPUMillis: 1000, DiskBytes: 1 << 40, ImageStoreBytes: 1 << 40, RunningBackends: 8}
	for _, tc := range []struct {
		name, observed                string
		sameInstallation, transferred bool
	}{
		{"stopped", "stopped", true, true},
		{"absent", "absent", true, true},
		{"running", "running", true, false},
		{"other_installation", "stopped", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := capacityStore(t, budgets)
			old := backendPlan(1, 100, 10)
			if refusal := admitPlan(t, s, old); refusal != nil {
				t.Fatal(refusal)
			}
			insertResourceRow(t, s.db, 1, "container", "present", tc.observed)
			next := capacityPlan{kind: reservationBackend, resourceID: fixtureID(2), installationID: old.installationID, memoryMiB: 100, cpuMillis: 10, transferFrom: fixtureID(1)}
			if !tc.sameInstallation {
				next.installationID = fixtureID(999)
			}
			refusal := admitPlan(t, s, next)
			if tc.transferred && refusal != nil {
				t.Fatalf("refused: %+v", refusal)
			}
			if !tc.transferred && (refusal == nil || refusal.dimension != CapacityMemory) {
				t.Fatalf("a source that cannot be replaced was transferred: %+v", refusal)
			}
		})
	}
}

// Ten disable and enable cycles leave the totals where they started.
func TestRepeatedTransfersLeaveTheTotalsUnchanged(t *testing.T) {
	s := capacityStore(t, CapacityBudgets{MemoryMiB: 256, CPUMillis: 1000, DiskBytes: 1 << 40, ImageStoreBytes: 1 << 40, RunningBackends: 1})
	current := backendPlan(1, 256, 10)
	if refusal := admitPlan(t, s, current); refusal != nil {
		t.Fatal(refusal)
	}
	for cycle := 2; cycle <= 11; cycle++ {
		insertResourceRow(t, s.db, cycle-1, "container", "stopped", "stopped")
		if _, err := s.db.Exec(`UPDATE runner_resources SET installation_id=? WHERE id=?`, current.installationID, current.resourceID); err != nil {
			t.Fatal(err)
		}
		next := capacityPlan{kind: reservationBackend, resourceID: fixtureID(cycle), installationID: current.installationID, memoryMiB: 256, cpuMillis: 10, transferFrom: current.resourceID}
		if refusal := admitPlan(t, s, next); refusal != nil {
			t.Fatalf("cycle %d refused: %+v", cycle, refusal)
		}
		current = next
		var mem int64
		if err := s.db.QueryRow(`SELECT COALESCE(SUM(memory_mib),0) FROM runner_reservations WHERE state='held'`).Scan(&mem); err != nil || mem != 256 || heldCount(t, s) != 1 {
			t.Fatalf("cycle %d totals: %d %d %v", cycle, mem, heldCount(t, s), err)
		}
	}
}
