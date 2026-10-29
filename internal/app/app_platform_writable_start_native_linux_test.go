//go:build linux && (amd64 || arm64)

package app

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/driver"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/journal"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/transport"
)

type platformWritableStartFixtureMessage struct {
	Raw, Signature []byte
	Receipt        journal.WritableCandidateRunningReceipt
	WriterReceipt  journal.DataWriterReceipt
}

func testNativePortalWritableStart(t *testing.T, s *Server, lease platformOperationLease, endpoint platformRuntimeEndpoint, writer platformWriterIntent) {
	t.Helper()
	ctx := context.Background()
	intent, err := s.preparePlatformWritableStart(ctx, lease)
	if err != nil {
		t.Fatal("writable start intent", err)
	}
	if intent.writer.resourceID != writer.resourceID || intent.writer.effectID != writer.effectID || intent.writer.receipt != writer.receipt {
		t.Fatal("start replaced confirmed writer")
	}
	again, err := s.preparePlatformWritableStart(ctx, lease)
	if err != nil || !samePlatformWritableStartIntent(intent, again) {
		t.Fatal("start intent replay", err)
	}
	path := filepath.Join(os.Getenv("TMPDIR"), "writable-start.json")
	save := func(receipt journal.WritableCandidateRunningReceipt) {
		t.Helper()
		encoded, _ := json.Marshal(platformWritableStartFixtureMessage{Raw: intent.raw, Signature: intent.signature, Receipt: receipt, WriterReceipt: writer.receipt})
		if err := os.WriteFile(path, encoded, 0600); err != nil {
			t.Fatal(err)
		}
	}
	save(journal.WritableCandidateRunningReceipt{})
	if _, err := s.reconcilePlatformWritableStart(ctx, lease, endpoint); err == nil {
		t.Fatal("unattempted start recovered")
	}
	mustExec(t, s.st, `CREATE TRIGGER portal_writable_start_failure BEFORE INSERT ON audit_log WHEN NEW.action='app_platform.writable_start_observed' BEGIN SELECT RAISE(ABORT,'writable start settlement fixture'); END`)
	if _, err := s.dispatchPlatformWritableStart(ctx, lease, endpoint); err == nil {
		t.Fatal("start audit failure ignored")
	}
	if scalar[string](t, s.st, `SELECT stage FROM app_platform_operations WHERE id=?`, lease.id) != "start" || scalar[string](t, s.st, `SELECT state FROM app_platform_operation_effects WHERE operation_id=? AND stage='start'`, lease.id) != "prepared" {
		t.Fatal("failed Portal start partially committed")
	}
	observed, err := platformRunWritableStart(ctx, endpoint, intent, transport.WritableCandidateRead)
	if err != nil {
		t.Fatal("runner original start receipt", err)
	}
	running, err := observed.RunningReceipt()
	if err != nil || !platformWritableRunningMatches(running, intent, lease, time.Now().UnixMilli()) {
		t.Fatal("runner lost original writable process", err)
	}
	save(running)
	mustExec(t, s.st, `DROP TRIGGER portal_writable_start_failure`)
	completed, err := s.completePlatformWritableStart(ctx, lease, endpoint)
	if err != nil {
		t.Fatal("writable start historical completion", err)
	}
	if completed.snapshot.Stage != appplatform.StageProbe || completed.rowRevision != lease.rowRevision+1 || completed.fence != lease.fence {
		t.Fatal("start did not advance exact operation")
	}
	final, err := s.preparePlatformWritableStart(ctx, completed)
	if err != nil || !final.applied || final.command.PayloadSHA256 != intent.command.PayloadSHA256 || final.writer.resourceID != writer.resourceID {
		t.Fatal("applied source changed", err)
	}
	before, _ := json.Marshal(running)
	after, _ := json.Marshal(final.running)
	if !bytes.Equal(before, after) {
		t.Fatal("Portal replaced native start receipt")
	}
	if _, err := s.dispatchPlatformWritableStart(ctx, lease, endpoint); err == nil {
		t.Fatal("stale start lease dispatched")
	}
	if scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_operation_effects WHERE operation_id=?`, lease.id) != 3 || scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_access_grants WHERE installation_id=?`, lease.installationID) != 0 || scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_activations WHERE installation_id=? AND state='candidate' AND readiness_evidence='{}' AND observed_ms IS NULL`, lease.installationID) != 1 {
		t.Fatal("start fabricated readiness or access")
	}
	t.Log("PORTAL_WRITABLE_START_RECONCILED")
	t.Log("PORTAL_WRITABLE_START_RETAINED_IDENTITY")
}

// Root witness/cleanup authenticates the historical signed intent and every
// native ownership label before inspecting or removing its exact container.
func checkPlatformWritableStartFixture(t *testing.T, path string, a control.Authority, socket string, native *driver.CandidateRuntime, cleanup bool) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return
	}
	if err != nil || len(raw) > 32768 {
		t.Fatal("writable fixture bytes", err)
	}
	var m platformWritableStartFixtureMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	var c control.WritableCandidateCommand
	var w control.DataWriterCommand
	var ref control.Command
	if json.Unmarshal(m.Raw, &c) != nil || json.Unmarshal(c.WriterRaw, &w) != nil || json.Unmarshal(w.DataRaw, &ref) != nil {
		t.Fatal("writable fixture reference")
	}
	issued, err := strconv.ParseInt(ref.IssuedMS, 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	v, err := control.VerifyWritableCandidate(m.Raw, m.Signature, a, issued)
	if err != nil {
		t.Fatal("writable fixture signature", err)
	}
	index := v.Index()
	writerBytes, _ := json.Marshal(m.WriterReceipt)
	if _, err := journal.ParseDataWriterReceipt(writerBytes); err != nil || platformDigest(writerBytes) != c.WriterReceiptSHA256 {
		t.Fatal("writable fixture writer source", err)
	}
	run := func(args ...string) []byte {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		out, err := exec.CommandContext(ctx, "docker", append([]string{"--host", "unix://" + socket}, args...)...).CombinedOutput()
		if err != nil || len(out) > 65536 {
			t.Fatal("owned writable inspection", err)
		}
		return out
	}
	ids := bytes.Fields(run("container", "ls", "-a", "--no-trunc", "--filter", "label=org.portal.app.resource="+w.ResourceID, "--format", "{{.ID}}"))
	if len(ids) == 0 {
		if !cleanup {
			t.Fatal("actual writable process missing")
		}
		return
	}
	if len(ids) != 1 || len(ids[0]) != 64 {
		t.Fatal("ambiguous writable process")
	}
	id := string(ids[0])
	var actual []struct {
		ID, Name, Image string
		Config          struct{ Labels map[string]string }
		State           struct {
			Running   bool
			StartedAt string
		}
		RestartCount int
	}
	if json.Unmarshal(run("container", "inspect", id), &actual) != nil || len(actual) != 1 || actual[0].ID != id || actual[0].Name != "/portal-app-"+w.ResourceID {
		t.Fatal("writable process identity")
	}
	for name, value := range map[string]string{"format": "writable-candidate/1-dev.1", "sandbox-profile": control.WritableCandidateSandboxProfile, "data-receipt": w.DataReceiptSHA256, "writer-receipt": c.WriterReceiptSHA256, "deployment": index.DeploymentID, "epoch": index.ControllerEpoch, "installation": index.InstallationID, "operation": index.OperationID, "resource": w.ResourceID, "namespace": index.Payload.DataNamespaceID, "release": index.Payload.ReleaseSHA256, "generation": index.TargetGeneration, "control-fence": index.InstallationFence, "writer-fence": index.Payload.WriterFence, "start-effect": c.EffectID, "start-payload": c.PayloadSHA256} {
		if actual[0].Config.Labels["org.portal.app."+name] != value {
			t.Fatal("refuse foreign writable cleanup", name)
		}
	}
	if actual[0].Image != m.WriterReceipt.Native.ImageID {
		t.Fatal("writable fixture image changed")
	}
	if m.Receipt.NativeID != "" && (actual[0].Config.Labels["org.portal.app.start-claim"] != m.Receipt.Claim.ClaimID || actual[0].Config.Labels["org.portal.app.start-ownership"] != m.Receipt.Claim.OwnershipSHA256) {
		t.Fatal("writable fixture claim binding changed")
	}
	if m.Receipt.NativeID != "" && (m.Receipt.NativeID != id || m.Receipt.Claim.Preflight == nil || actual[0].Image != m.Receipt.Claim.Preflight.Native.ImageID) {
		t.Fatal("writable witness disagrees with exact receipt")
	}
	if cleanup {
		run("container", "stop", "--time", "10", id)
		run("container", "rm", id)
		identity := driver.CandidateIdentity{DeploymentID: index.DeploymentID, ControllerEpoch: index.ControllerEpoch, InstallationID: index.InstallationID, OperationID: index.OperationID, ResourceID: w.ResourceID, NamespaceID: index.Payload.DataNamespaceID, ReleaseSHA256: index.Payload.ReleaseSHA256, Generation: index.TargetGeneration, ControlFence: index.InstallationFence, WriterFence: index.Payload.WriterFence}
		if err := native.ReleaseCandidateHTTPStorage(context.Background(), identity); err != nil {
			t.Error("owned writable IPC cleanup", err)
		}
	} else {
		if !actual[0].State.Running || actual[0].RestartCount != 0 || actual[0].State.StartedAt == "" {
			t.Fatal("writable process not continuously running")
		}
		started, err := time.Parse(time.RFC3339Nano, actual[0].State.StartedAt)
		observed, _ := strconv.ParseInt(m.Receipt.NativeObservedMS, 10, 64)
		if err != nil || started.UnixMilli() > observed {
			t.Fatal("writable process restarted after original observation", err)
		}
		t.Log("PORTAL_WRITABLE_START_NATIVE_WITNESS")
	}
}
