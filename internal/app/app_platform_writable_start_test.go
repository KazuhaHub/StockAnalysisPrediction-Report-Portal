package app

import (
	"bytes"
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/driver"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/journal"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/transport"
)

// Synthetic historical writer rows exercise SQL/source validators only. No live
// native or opaque transport proof is constructed or accepted for settlement.
func platformWritableStartFixture(t *testing.T, pg bool) (*Server, platformOperationLease, platformWriterIntent) {
	t.Helper()
	s, lease := platformWriterIntentFixture(t, pg)
	i, err := s.preparePlatformInstallWriter(context.Background(), lease)
	if err != nil {
		t.Fatal(err)
	}
	v, err := control.VerifyDataWriter(i.raw, i.signature, i.authority, time.Now().UnixMilli())
	if err != nil {
		t.Fatal(err)
	}
	ref := v.Data()
	now := strconv.FormatInt(time.Now().UnixMilli(), 10)
	var r journal.DataWriterReceipt
	r.Format = "portal.runner.data-writer-receipt/1-dev.1"
	r.ControlRevision = ref.ControlRevision
	r.SettledMS = now
	r.Claim.Format = "portal.runner.data-writer-claim/1-dev.1"
	r.Claim.ClaimID = platformFixtureID(903)
	r.Claim.EffectID = i.effectID
	r.Claim.PayloadSHA256 = i.command.PayloadSHA256
	r.Claim.DataReceiptSHA256 = i.dataSHA
	r.Claim.ResourceID = i.resourceID
	r.Claim.AdmittedFence = ref.InstallationFence
	r.Claim.ClaimedMS = now
	r.Claim.ExpiresMS = ref.ExpiresMS
	n := &r.Native
	n.Format = "portal.runner.data-writer-native/1-dev.1"
	n.ImageID = "sha256:" + strings.Repeat("b", 64)
	n.Platform = i.command.Platform
	n.Profile = control.DataWriterSandboxProfile
	n.SeccompSHA256 = driver.DataWriterPolicySHA256()
	n.RootFilesystemUUID = i.data.Claim.RootFilesystemUUID
	n.RootInode = i.data.Claim.RootInode
	n.ProjectID = i.data.ProjectID
	n.Device = i.data.Device
	n.NamespaceInode = i.data.NamespaceInode
	n.QuotaBytes = i.data.QuotaBytes
	n.QuotaInodes = i.data.QuotaInodes
	n.UsedBytes = i.data.UsedBytes
	n.UsedInodes = i.data.UsedInodes
	n.ObservedMS = now
	binding, _ := json.Marshal(struct {
		Purpose  string `json:"purpose"`
		Claim    any    `json:"claim"`
		Revision string `json:"control_revision"`
		Native   any    `json:"native"`
	}{"portal.runner.data-writer-binding/1-dev.1", r.Claim, r.ControlRevision, r.Native})
	r.BindingSHA256 = platformDigest(binding)
	encoded, _ := json.Marshal(r)
	if _, err := journal.ParseDataWriterReceipt(encoded); err != nil {
		t.Fatal("synthetic writer shape", err)
	}
	databaseNow, err := platformDatabaseNow(migExec{s: s.st, ex: s.st.db})
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, s.st, `UPDATE app_platform_operation_effects SET state='applied',observed_fence=admitted_fence,receipt_json=?,updated_ms=? WHERE operation_id=? AND effect_number=2`, string(encoded), databaseNow, lease.id)
	mustExec(t, s.st, `UPDATE app_platform_data_namespaces SET revision=3,updated_ms=? WHERE id=?`, databaseNow, i.namespaceID)
	applied, err := s.preparePlatformInstallWriter(context.Background(), lease)
	if err != nil || !applied.applied {
		t.Fatal("synthetic writer source", err)
	}
	return s, lease, applied
}
func testPlatformWritableStartIntent(t *testing.T, pg bool) {
	s, lease, writer := platformWritableStartFixture(t, pg)
	i, err := s.preparePlatformWritableStart(context.Background(), lease)
	if err != nil {
		t.Fatal(err)
	}
	v, err := control.VerifyWritableCandidate(i.raw, i.signature, i.writer.authority, time.Now().UnixMilli())
	if err != nil || v.Command().PayloadSHA256 != i.command.PayloadSHA256 || v.Index().Payload.ResourceID != writer.resourceID || v.Writer().Command().EffectID != writer.effectID {
		t.Fatal("signed writable start", err)
	}
	encoded, _ := json.Marshal(writer.receipt)
	if v.Command().WriterReceiptSHA256 != platformDigest(encoded) {
		t.Fatal("start source receipt changed")
	}
	expires, _ := strconv.ParseInt(v.Index().ExpiresMS, 10, 64)
	if expires > lease.leaseUntilMS-1000 {
		t.Fatal("start outlives lease")
	}
	again, err := s.preparePlatformWritableStart(context.Background(), lease)
	if err != nil || !samePlatformWritableStartIntent(i, again) {
		t.Fatal("start replaced identity", err)
	}
	if scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_operation_effects WHERE operation_id=?`, lease.id) != 3 || scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_activations WHERE installation_id=?`, lease.installationID) != 1 || i.applied {
		t.Fatal("start intent fabricated running result")
	}
	if _, err := s.preparePlatformInstallWriter(context.Background(), lease); err == nil {
		t.Fatal("pre-start writer reader accepted later history")
	}
	if _, err := s.preparePlatformInstallCandidate(context.Background(), lease); err == nil {
		t.Fatal("readonly candidate adopted writable start")
	}
	before := dumpOf(t, s.st)
	if _, err := s.commitPlatformObservedWritableStart(context.Background(), lease, i, platformRuntimeEndpoint{}, transport.WritableLifecycleObservation{}); err == nil {
		t.Fatal("zero proof settled start")
	}
	if !bytes.Equal(stripHeader(before), stripHeader(dumpOf(t, s.st))) {
		t.Fatal("invalid proof changed state")
	}
	if scalar[int](t, s.st, `SELECT COUNT(*) FROM audit_log WHERE action='app_platform.writable_start_intent' AND target_id=?`, lease.id) != 1 {
		t.Fatal("start audit not idempotent")
	}
}
func TestPlatformWritableStartIntentSQLite(t *testing.T)   { testPlatformWritableStartIntent(t, false) }
func TestPlatformWritableStartIntentPostgres(t *testing.T) { testPlatformWritableStartIntent(t, true) }
func testPlatformWritableStartRefusals(t *testing.T, pg bool) {
	for _, mode := range []string{"writer", "writer_receipt", "namespace", "runtime", "publisher", "pause", "lease", "fence", "candidate", "start_unknown", "start_digest", "start_time", "partial", "extra"} {
		t.Run(mode, func(t *testing.T) {
			s, lease, _ := platformWritableStartFixture(t, pg)
			if _, err := s.preparePlatformWritableStart(context.Background(), lease); err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "writer":
				mustExec(t, s.st, `UPDATE app_platform_operation_effects SET state='prepared' WHERE effect_number=2`)
			case "writer_receipt":
				mustExec(t, s.st, `UPDATE app_platform_operation_effects SET receipt_json='{}' WHERE effect_number=2`)
			case "namespace":
				mustExec(t, s.st, `UPDATE app_platform_data_namespaces SET writer_fence=2`)
			case "runtime":
				mustExec(t, s.st, `UPDATE app_platform_runtime_bindings SET enabled=0`)
			case "publisher":
				mustExec(t, s.st, `UPDATE app_platform_publishers SET enabled=0`)
			case "pause":
				mustExec(t, s.st, `UPDATE meta SET v='1' WHERE k='app_platform_restore_paused'`)
			case "lease":
				mustExec(t, s.st, `UPDATE app_platform_operations SET lease_until_ms=0`)
			case "fence":
				mustExec(t, s.st, `UPDATE app_platform_operations SET fence=fence+1`)
			case "candidate":
				mustExec(t, s.st, `UPDATE app_platform_activations SET readiness_evidence='{"ready":true}'`)
			case "start_unknown":
				mustExec(t, s.st, `UPDATE app_platform_operation_effects SET state='unknown' WHERE stage='start'`)
			case "start_digest":
				mustExec(t, s.st, `UPDATE app_platform_operation_effects SET payload_sha256=? WHERE stage='start'`, strings.Repeat("f", 64))
			case "start_time":
				mustExec(t, s.st, `UPDATE app_platform_operation_effects SET updated_ms=updated_ms-1 WHERE stage='start'`)
			case "partial":
				mustExec(t, s.st, `DELETE FROM app_platform_activations`)
			case "extra":
				mustExec(t, s.st, `INSERT INTO app_platform_operation_effects(operation_id,stage,effect_number,effect_id,kind,payload_sha256,runtime_id,controller_epoch,admitted_fence,observed_fence,state,receipt_json,created_ms,updated_ms) SELECT operation_id,'activate',1,?,'activate',payload_sha256,runtime_id,controller_epoch,admitted_fence,0,'prepared','{}',created_ms,updated_ms FROM app_platform_operation_effects WHERE stage='start'`, platformFixtureID(904))
			}
			before := dumpOf(t, s.st)
			if _, err := s.preparePlatformWritableStart(context.Background(), lease); err == nil {
				t.Fatal("invalid writable source accepted", mode)
			}
			if !bytes.Equal(stripHeader(before), stripHeader(dumpOf(t, s.st))) {
				t.Fatal("refusal changed state", mode)
			}
		})
	}
}
func TestPlatformWritableStartRefusalsSQLite(t *testing.T) {
	testPlatformWritableStartRefusals(t, false)
}
func TestPlatformWritableStartRefusalsPostgres(t *testing.T) {
	testPlatformWritableStartRefusals(t, true)
}
