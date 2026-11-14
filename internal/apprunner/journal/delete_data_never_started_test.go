//go:build linux || darwin

package journal

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/data"
)

// neverStartedDelete is a namespace the runner prepared for an install whose start it then refused: the volume and
// its disk reservation exist, the start command is reverted, and no container resource was ever created. The
// signed delete-data command is the one the Portal builds for the failed install's applied preparation.
type neverStartedDelete struct {
	s         *Store
	command   control.DeleteDataCommand
	namespace string
}

func newNeverStartedDelete(t *testing.T) neverStartedDelete {
	t.Helper()
	s, raw, _, receipt := appliedReceiptFixture(t)
	var prepare control.Command
	if err := json.Unmarshal(raw, &prepare); err != nil {
		t.Fatal(err)
	}
	namespace := prepare.Payload.DataNamespaceID
	// The disk reservation the namespace holds until it is destroyed.
	if _, err := s.db.Exec(`INSERT INTO runner_reservations(id,resource_id,installation_id,kind,memory_mib,cpu_millis,disk_bytes,image_bytes,state,created_ms,updated_ms) VALUES(?,?,?,'namespace',0,0,?,0,'held',1,1)`,
		fixtureID(8800), namespace, prepare.InstallationID, NamespaceReservationBytes); err != nil {
		t.Fatal(err)
	}
	command := control.DeleteDataCommand{
		Format: control.DeleteDataFormat, DeploymentID: prepare.DeploymentID, ControllerEpoch: prepare.ControllerEpoch, ControlRevision: prepare.ControlRevision,
		InstallationID: prepare.InstallationID, OperationID: fixtureID(8801), TargetGeneration: "2", Stage: "remove_data", EffectNumber: "1",
		EffectID: fixtureID(8802), Kind: "delete-data", InstallationFence: "3",
		Payload: control.DeleteDataPayload{
			NamespaceID: namespace, ReleaseSHA256: prepare.Payload.ReleaseSHA256, ProjectID: receipt.ProjectID, DataFormat: receipt.DataFormat,
			QuotaBytes: receipt.QuotaBytes, QuotaInodes: receipt.QuotaInodes, ExpectedDevice: receipt.Device, ExpectedNamespaceInode: receipt.NamespaceInode,
			WriterFence: "2",
		},
	}
	var err error
	if command.PayloadSHA256, err = control.DeleteDataLogicalDigest(command); err != nil {
		t.Fatal(err)
	}
	return neverStartedDelete{s: s, command: command, namespace: namespace}
}

func (n neverStartedDelete) startCommand(t *testing.T, state string) {
	t.Helper()
	receipt := "{}"
	if state == "reverted" {
		receipt = `{"outcome":"capacity_exhausted"}`
	}
	if _, err := n.s.db.Exec(`INSERT INTO runner_commands(effect_id,operation_id,installation_id,stage,effect_number,kind,payload_sha256,target_generation,controller_epoch,admitted_fence,state,receipt_json,ack_attempted_ms,created_ms,updated_ms)
		VALUES(?,?,?,'start',1,'start-candidate',?,1,?,2,?,?,NULL,1,1)`,
		fixtureID(8810), fixtureID(8811), n.command.InstallationID, n.command.PayloadSHA256, n.command.ControllerEpoch, state, receipt); err != nil {
		t.Fatal(err)
	}
}

func (n neverStartedDelete) load(t *testing.T, command control.DeleteDataCommand) error {
	t.Helper()
	tx, err := n.s.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	_, err = loadDeleteDataSource(context.Background(), tx, command, time.Now().UnixMilli())
	return err
}

// The runner's delete-data accepts a namespace that never had a container, as long as no start-bearing command of
// the installation is anything but reverted. Fails while the load demands a container history, which is what leaves
// the namespace and its disk reservation in place after a refused install.
func TestDeleteDataAcceptsANamespaceThatNeverStarted(t *testing.T) {
	n := newNeverStartedDelete(t)
	if err := n.load(t, n.command); err != nil {
		t.Fatalf("a namespace with no container history was refused: %v", err)
	}
	n.startCommand(t, "reverted")
	if err := n.load(t, n.command); err != nil {
		t.Fatalf("a namespace whose start was refused was refused: %v", err)
	}
}

func TestDeleteDataRefusesANamespaceWhoseStartIsNotReverted(t *testing.T) {
	for _, state := range []string{"prepared", "unknown", "applied"} {
		t.Run(state, func(t *testing.T) {
			n := newNeverStartedDelete(t)
			n.startCommand(t, state)
			if err := n.load(t, n.command); err == nil {
				t.Fatalf("a namespace with a %s start was accepted", state)
			}
		})
	}
}

// Without a container to bind to, the volume carries the binding: the release, the generation and the writer
// fence must be exactly those of the volume the install prepared.
func TestDeleteDataNeverStartedBindsTheVolume(t *testing.T) {
	for name, mutate := range map[string]func(*control.DeleteDataCommand){
		"release": func(c *control.DeleteDataCommand) {
			c.Payload.ReleaseSHA256 = fixtureID(1)[:0] + "b" + c.Payload.ReleaseSHA256[1:]
		},
		"generation": func(c *control.DeleteDataCommand) { c.TargetGeneration = "3" },
		"writer":     func(c *control.DeleteDataCommand) { c.Payload.WriterFence = "3" },
		"fence":      func(c *control.DeleteDataCommand) { c.InstallationFence = "0" },
	} {
		t.Run(name, func(t *testing.T) {
			n := newNeverStartedDelete(t)
			changed := n.command
			mutate(&changed)
			if changed.Payload.ReleaseSHA256 == n.command.Payload.ReleaseSHA256 && changed.TargetGeneration == n.command.TargetGeneration &&
				changed.Payload.WriterFence == n.command.Payload.WriterFence && changed.InstallationFence == n.command.InstallationFence {
				t.Fatal("mutation was not applied")
			}
			if err := n.load(t, changed); err == nil {
				t.Fatal("a command not bound to the volume was accepted")
			}
		})
	}
}

// Any container the installation ever had keeps the container binding, so a started installation is never taken
// for a never-started one.
func TestDeleteDataWithAContainerKeepsTheContainerBinding(t *testing.T) {
	n := newNeverStartedDelete(t)
	if _, err := n.s.db.Exec(`INSERT INTO runner_resources(id,installation_id,data_namespace_id,kind,native_id,release_sha256,generation,controller_epoch,control_fence,writer_fence,desired_state,observed_state,ownership_sha256,revision,observed_ms,created_ms,updated_ms)
		VALUES(?,?,?,'container','portal-container',?,1,?,2,1,'present','running',?,2,1,1,1)`,
		fixtureID(8820), n.command.InstallationID, n.namespace, n.command.Payload.ReleaseSHA256, n.command.ControllerEpoch, n.command.PayloadSHA256); err != nil {
		t.Fatal(err)
	}
	if err := n.load(t, n.command); err == nil {
		t.Fatal("a namespace with a running container was accepted")
	}
}

// The signed command is admitted and settled through the runner's own delete-data begin and settlement, with the
// native deletion replaced by its observation. The settlement destroys the never-started namespace and releases
// its disk reservation in the same transaction, so eight refused installs no longer exhaust the disk budget.
func TestDeleteDataNeverStartedReleasesTheNamespaceReservation(t *testing.T) {
	n := newNeverStartedDelete(t)
	n.startCommand(t, "reverted")
	deployment, err := n.s.ControllerDeployment(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	authority := control.Authority{DeploymentID: deployment.DeploymentID, ControllerEpoch: deployment.ControllerEpoch, ControlRevision: deployment.ControlRevision,
		PublicKey: deployment.PublicKey, Fingerprint: deployment.Fingerprint}
	command := n.command
	command.ControlRevision = strconv.FormatInt(deployment.ControlRevision, 10)
	command.DeploymentID, command.ControllerEpoch = deployment.DeploymentID, deployment.ControllerEpoch
	command.PayloadSHA256, err = control.DeleteDataLogicalDigest(command)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UnixMilli()
	command.IssuedMS, command.ExpiresMS = strconv.FormatInt(now-1000, 10), strconv.FormatInt(now+20000, 10)
	key := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{30}, 32))
	raw, signature, err := control.SignDeleteData(command, key, authority, now)
	if err != nil {
		t.Fatal(err)
	}
	source, replay, err := n.s.beginDeleteData(context.Background(), raw, signature)
	if err != nil || replay {
		t.Fatalf("the never-started delete-data command was refused at admission: %v", err)
	}
	source.raw, source.signature = raw, signature
	if scalar(t, n.s.db, `SELECT COUNT(*) FROM runner_reservations WHERE resource_id='`+n.namespace+`' AND state='held'`) != 1 {
		t.Fatal("the namespace does not hold its reservation before the delete")
	}
	project, _ := strconv.ParseUint(command.Payload.ProjectID, 10, 32)
	device, _ := strconv.ParseUint(command.Payload.ExpectedDevice, 10, 64)
	inode, _ := strconv.ParseUint(command.Payload.ExpectedNamespaceInode, 10, 64)
	receipt, err := n.s.settleDeleteDataSnapshot(context.Background(), source, data.DeleteSnapshot{
		NamespaceID: n.namespace, ReleaseSHA256: command.Payload.ReleaseSHA256, ProjectID: uint32(project), DataFormat: 1, WriterFence: 2,
		NamespaceDevice: device, NamespaceInode: inode, ObservedMS: now,
	})
	if err != nil {
		t.Fatalf("the never-started namespace could not be settled: %v", err)
	}
	if receipt.NamespaceID != n.namespace ||
		scalar(t, n.s.db, `SELECT COUNT(*) FROM runner_reservations WHERE resource_id='`+n.namespace+`' AND state='released'`) != 1 ||
		scalar(t, n.s.db, `SELECT COUNT(*) FROM runner_resources WHERE id='`+n.namespace+`' AND observed_state='absent' AND desired_state='absent'`) != 1 ||
		scalar(t, n.s.db, `SELECT COUNT(*) FROM runner_commands WHERE effect_id='`+command.EffectID+`' AND state='applied'`) != 1 {
		t.Fatal("the settlement did not destroy the namespace and release its reservation")
	}
	// The released disk is available again: a namespace-sized plan fits in a budget of exactly one namespace.
	budgets := CapacityBudgets{MemoryMiB: 1 << 20, CPUMillis: 1 << 20, DiskBytes: NamespaceReservationBytes, ImageStoreBytes: 1 << 40, RunningBackends: 8}
	if err := n.s.SetCapacityBudgets(budgets); err != nil {
		t.Fatal(err)
	}
	plan := capacityPlan{kind: reservationNamespace, resourceID: fixtureID(8830), installationID: fixtureID(8831), diskBytes: NamespaceReservationBytes}
	if refusal := admitPlan(t, n.s, plan); refusal != nil {
		t.Fatalf("the released reservation still counts against the disk: %+v", refusal)
	}
	// A replay of the same signed command is answered from the applied row.
	if _, replayed, err := n.s.beginDeleteData(context.Background(), raw, signature); err != nil || !replayed {
		t.Fatalf("the applied delete-data command did not replay: %v", err)
	}
}

// A container of the installation on another namespace, or on none, is a container the installation had: the
// never-started binding refuses for that reason, not for the start commands or the volume.
func TestDeleteDataNeverStartedRefusesAnyContainerOfTheInstallation(t *testing.T) {
	for name, namespace := range map[string]any{"another_namespace": fixtureID(8840), "no_namespace": nil} {
		t.Run(name, func(t *testing.T) {
			n := newNeverStartedDelete(t)
			if _, err := n.s.db.Exec(`INSERT INTO runner_resources(id,installation_id,data_namespace_id,kind,native_id,release_sha256,generation,controller_epoch,control_fence,writer_fence,desired_state,observed_state,ownership_sha256,revision,observed_ms,created_ms,updated_ms)
				VALUES(?,?,?,'container','portal-other-container',?,1,?,2,1,'absent','absent',?,2,1,1,1)`,
				fixtureID(8841), n.command.InstallationID, namespace, n.command.Payload.ReleaseSHA256, n.command.ControllerEpoch, n.command.PayloadSHA256); err != nil {
				t.Fatal(err)
			}
			err := n.load(t, n.command)
			if err == nil || !strings.Contains(err.Error(), "never-started container") {
				t.Fatalf("want the container refusal, got: %v", err)
			}
		})
	}
}

func TestDeleteDataNeverStartedNamesItsReasons(t *testing.T) {
	n := newNeverStartedDelete(t)
	n.startCommand(t, "unknown")
	if err := n.load(t, n.command); err == nil || !strings.Contains(err.Error(), "never-started command") {
		t.Fatalf("want the command refusal, got: %v", err)
	}
	m := newNeverStartedDelete(t)
	changed := m.command
	changed.TargetGeneration = "3"
	if err := m.load(t, changed); err == nil || !strings.Contains(err.Error(), "never-started volume binding") {
		t.Fatalf("want the volume refusal, got: %v", err)
	}
}
