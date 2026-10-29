package app

import (
	"bytes"
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/driver"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/journal"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/transport"
)

// Synthetic historical process metadata tests repository validation only. No
// opaque native/transport evidence is manufactured or used for live settlement.
func platformWritableProbeFixture(t *testing.T, pg bool) (*Server, platformOperationLease, platformWritableStartIntent) {
	t.Helper()
	s, lease, _ := platformWritableStartFixture(t, pg)
	i, err := s.preparePlatformWritableStart(context.Background(), lease)
	if err != nil {
		t.Fatal(err)
	}
	v, err := control.VerifyWritableCandidate(i.raw, i.signature, i.writer.authority, time.Now().UnixMilli())
	if err != nil {
		t.Fatal(err)
	}
	var r journal.WritableCandidateRunningReceipt
	if err := json.Unmarshal([]byte(`{"claim":{"preflight":{"native":{}}}}`), &r); err != nil {
		t.Fatal(err)
	}
	now := strconv.FormatInt(time.Now().UnixMilli(), 10)
	index := v.Index()
	r.Format = "portal.runner.writable-candidate-running-receipt/1-dev.1"
	r.NativeID = strings.Repeat("e", 64)
	r.NativeObservedMS = now
	r.SettledMS = now
	m := &r.Claim
	m.Format = "portal.runner.writable-candidate-claim/1-dev.1"
	m.ClaimID = platformFixtureID(905)
	m.EffectID = i.command.EffectID
	m.ResourceID = i.writer.resourceID
	m.PayloadSHA256 = i.command.PayloadSHA256
	m.DataReceiptSHA256 = i.writer.dataSHA
	m.WriterReceiptSHA256 = i.command.WriterReceiptSHA256
	m.ControlRevision = index.ControlRevision
	m.AdmittedFence = index.InstallationFence
	m.SandboxProfile = control.WritableCandidateSandboxProfile
	m.ClaimedMS = now
	m.ExpiresMS = index.ExpiresMS
	m.PreflightBoundMS = now
	owner := struct{ Format, Deployment, Epoch, Effect, Resource, Installation, Operation, Namespace, Release, Generation, Fence, Writer, Payload, DataReceipt, WriterReceipt string }{"portal.runner.writable-candidate-ownership/1-dev.1", index.DeploymentID, index.ControllerEpoch, index.EffectID, index.Payload.ResourceID, index.InstallationID, index.OperationID, index.Payload.DataNamespaceID, index.Payload.ReleaseSHA256, index.TargetGeneration, index.InstallationFence, index.Payload.WriterFence, index.PayloadSHA256, i.writer.dataSHA, i.command.WriterReceiptSHA256}
	ownerRaw, _ := json.Marshal(owner)
	m.OwnershipSHA256 = platformDigest(ownerRaw)
	m.Preflight.Format = "portal.runner.writable-candidate-preflight/1-dev.1"
	m.Preflight.SeccompSHA256 = driver.DataWriterPolicySHA256()
	p, n := &m.Preflight.Native, i.writer.receipt.Native
	p.Format = "portal.runner.candidate-preflight/1-dev.1"
	p.ImageID = n.ImageID
	p.Platform = n.Platform
	p.PolicySHA256 = strings.Repeat("c", 64)
	p.MountsSHA256 = strings.Repeat("d", 64)
	p.RootFilesystemUUID = n.RootFilesystemUUID
	p.RootInode = n.RootInode
	p.ProjectID = n.ProjectID
	p.Device = n.Device
	p.NamespaceInode = n.NamespaceInode
	p.QuotaBytes = n.QuotaBytes
	p.QuotaInodes = n.QuotaInodes
	encoded, _ := json.Marshal(r)
	if _, err := journal.ParseWritableCandidateRunningReceipt(encoded); err != nil || !platformWritableRunningMatches(r, i, lease, time.Now().UnixMilli()) {
		t.Fatal("synthetic running shape", err)
	}
	model, err := appplatform.OperationFromSnapshot(lease.snapshot)
	if err != nil {
		t.Fatal(err)
	}
	next, err := model.Advance(lease.snapshot.Revision, appplatform.StageProbe)
	if err != nil {
		t.Fatal(err)
	}
	lease.snapshot = next.Snapshot()
	lease.rowRevision++
	databaseNow, err := platformDatabaseNow(migExec{s: s.st, ex: s.st.db})
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, s.st, `UPDATE app_platform_operation_effects SET state='applied',observed_fence=admitted_fence,receipt_json=?,updated_ms=? WHERE operation_id=? AND stage='start'`, string(encoded), databaseNow, lease.id)
	mustExec(t, s.st, `UPDATE app_platform_operations SET stage='probe',state_revision=?,row_revision=? WHERE id=?`, int64(lease.snapshot.Revision), lease.rowRevision, lease.id)
	prepared, err := s.preparePlatformWritableProbe(context.Background(), lease)
	if err != nil || !prepared.applied {
		t.Fatal("applied probe source", err)
	}
	return s, lease, prepared
}
func seedPlatformStoredWritableReadiness(t *testing.T, s *Server, lease platformOperationLease, i platformWritableStartIntent) (platformOperationLease, journal.WritableCandidateReadiness) {
	t.Helper()
	running, _ := json.Marshal(i.running)
	r := journal.WritableCandidateReadiness{Format: "portal.runner.writable-candidate-readiness/1-dev.1", PayloadSHA256: i.command.PayloadSHA256, RunningReceiptSHA256: platformDigest(running), NativeID: i.running.NativeID, Protocol: "portal.backend/1", DataFormat: i.writer.data.DataFormat, ObservedMS: strconv.FormatInt(time.Now().UnixMilli(), 10)}
	encoded, _ := json.Marshal(r)
	observed, _ := strconv.ParseInt(r.ObservedMS, 10, 64)
	now, err := platformDatabaseNow(migExec{s: s.st, ex: s.st.db})
	if err != nil {
		t.Fatal(err)
	}
	model, err := appplatform.OperationFromSnapshot(lease.snapshot)
	if err != nil {
		t.Fatal(err)
	}
	next, err := model.Advance(lease.snapshot.Revision, appplatform.StageActivate)
	if err != nil {
		t.Fatal(err)
	}
	lease.snapshot = next.Snapshot()
	lease.rowRevision++
	mustExec(t, s.st, `UPDATE app_platform_activations SET readiness_evidence=?,observed_ms=?,updated_ms=? WHERE id=?`, string(encoded), observed, now, i.writer.resourceID)
	mustExec(t, s.st, `UPDATE app_platform_operations SET stage='activate',state_revision=?,row_revision=? WHERE id=?`, int64(lease.snapshot.Revision), lease.rowRevision, lease.id)
	return lease, r
}
func testPlatformWritableReadinessSource(t *testing.T, pg bool) {
	s, lease, i := platformWritableProbeFixture(t, pg)
	again, err := s.preparePlatformWritableProbe(context.Background(), lease)
	if err != nil || !samePlatformWritableStartIntent(i, again) {
		t.Fatal("probe changed source", err)
	}
	before := dumpOf(t, s.st)
	if _, err := s.commitPlatformWritableReadiness(context.Background(), lease, i, platformRuntimeEndpoint{}, transport.WritableLifecycleObservation{}); err == nil {
		t.Fatal("zero probe committed")
	}
	if !bytes.Equal(stripHeader(before), stripHeader(dumpOf(t, s.st))) {
		t.Fatal("zero probe changed state")
	}
	activated, r := seedPlatformStoredWritableReadiness(t, s, lease, i)
	current, err := s.preparePlatformWritableProbe(context.Background(), activated)
	if err != nil || !validStoredPlatformWritableReadiness(current, time.Now().UnixMilli()) {
		t.Fatal("stored current source", err)
	}
	if !platformWritableReadinessMatches(r, current) || !validStoredPlatformWritableReadiness(current, time.Now().Add(5*time.Second).UnixMilli()) {
		t.Fatal("historical source treated as expiring authority")
	}
	if validPlatformWritableReadinessObservation(transport.WritableLifecycleObservation{}, current, platformRuntimeEndpoint{}) {
		t.Fatal("stored history minted fresh proof")
	}
	if _, err := s.preparePlatformWritableStart(context.Background(), activated); err == nil {
		t.Fatal("start source adopted readiness history")
	}
	if _, err := s.preparePlatformInstallCandidate(context.Background(), activated); err == nil {
		t.Fatal("readonly source adopted writable readiness")
	}
	if scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_access_grants WHERE installation_id=?`, lease.installationID) != 0 || scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_operation_effects WHERE operation_id=?`, lease.id) != 3 {
		t.Fatal("readiness source created activation/grant")
	}
}
func TestPlatformWritableReadinessSourceSQLite(t *testing.T) {
	testPlatformWritableReadinessSource(t, false)
}
func TestPlatformWritableReadinessSourcePostgres(t *testing.T) {
	testPlatformWritableReadinessSource(t, true)
}
func testPlatformWritableReadinessRefusals(t *testing.T, pg bool) {
	for _, mode := range []string{"native", "payload", "source", "schema", "observed", "timestamp", "unknown", "candidate_time", "writer", "start", "namespace", "runtime", "publisher", "pause", "lease", "extra"} {
		t.Run(mode, func(t *testing.T) {
			s, lease, i := platformWritableProbeFixture(t, pg)
			lease, r := seedPlatformStoredWritableReadiness(t, s, lease, i)
			switch mode {
			case "native":
				r.NativeID = strings.Repeat("a", 64)
			case "payload":
				r.PayloadSHA256 = strings.Repeat("b", 64)
			case "source":
				r.RunningReceiptSHA256 = strings.Repeat("c", 64)
			case "schema":
				r.DataFormat = "2"
			case "timestamp":
				r.ObservedMS = strconv.FormatInt(time.Now().Add(time.Hour).UnixMilli(), 10)
			case "observed":
				mustExec(t, s.st, `UPDATE app_platform_activations SET observed_ms=observed_ms-1`)
			case "candidate_time":
				mustExec(t, s.st, `UPDATE app_platform_activations SET updated_ms=created_ms-1`)
			case "writer":
				mustExec(t, s.st, `UPDATE app_platform_operation_effects SET receipt_json='{}' WHERE effect_number=2`)
			case "start":
				mustExec(t, s.st, `UPDATE app_platform_operation_effects SET state='unknown' WHERE stage='start'`)
			case "namespace":
				mustExec(t, s.st, `UPDATE app_platform_data_namespaces SET revision=2`)
			case "runtime":
				mustExec(t, s.st, `UPDATE app_platform_runtime_bindings SET enabled=0`)
			case "publisher":
				mustExec(t, s.st, `UPDATE app_platform_publishers SET enabled=0`)
			case "pause":
				mustExec(t, s.st, `UPDATE meta SET v='1' WHERE k='app_platform_restore_paused'`)
			case "lease":
				mustExec(t, s.st, `UPDATE app_platform_operations SET lease_until_ms=0`)
			case "extra":
				mustExec(t, s.st, `INSERT INTO app_platform_operation_effects(operation_id,stage,effect_number,effect_id,kind,payload_sha256,runtime_id,controller_epoch,admitted_fence,observed_fence,state,receipt_json,created_ms,updated_ms) SELECT operation_id,'activate',1,?,'activate',payload_sha256,runtime_id,controller_epoch,admitted_fence,0,'prepared','{}',created_ms,updated_ms FROM app_platform_operation_effects WHERE stage='start'`, platformFixtureID(906))
			}
			if mode == "native" || mode == "payload" || mode == "source" || mode == "schema" || mode == "timestamp" || mode == "unknown" {
				raw, _ := json.Marshal(r)
				if mode == "unknown" {
					raw = append(raw[:len(raw)-1], []byte(`,"extra":true}`)...)
				}
				mustExec(t, s.st, `UPDATE app_platform_activations SET readiness_evidence=?`, string(raw))
			}
			before := dumpOf(t, s.st)
			if _, err := s.preparePlatformWritableProbe(context.Background(), lease); err == nil {
				t.Fatal("invalid readiness source accepted", mode)
			}
			if !bytes.Equal(stripHeader(before), stripHeader(dumpOf(t, s.st))) {
				t.Fatal("readiness refusal mutated state", mode)
			}
		})
	}
}
func TestPlatformWritableReadinessRefusalsSQLite(t *testing.T) {
	testPlatformWritableReadinessRefusals(t, false)
}
func TestPlatformWritableReadinessRefusalsPostgres(t *testing.T) {
	testPlatformWritableReadinessRefusals(t, true)
}
