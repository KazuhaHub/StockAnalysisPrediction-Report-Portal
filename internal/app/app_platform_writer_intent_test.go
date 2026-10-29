package app

import (
	"bytes"
	"context"
	"encoding/json"
	"strconv"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/transport"
)

// Repository metadata only: no native runtime/proof or writer success is minted.
func platformWriterIntentFixture(t *testing.T, pg bool) (*Server, platformOperationLease) {
	t.Helper()
	s, lease := platformCandidateIntentFixture(t, pg)
	var p platformInstallProjection
	raw := scalar[string](t, s.st, `SELECT p.plan_json FROM app_platform_review_plans p JOIN app_platform_operations o ON o.review_plan_id=p.id WHERE o.id=?`, lease.id)
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		t.Fatal(err)
	}
	p.Format = platformWritableInstallFormat
	p.Runtime.Profile.WritableSandboxProfile = control.WritableCandidateSandboxProfile
	encoded, _ := json.Marshal(p)
	digest := platformDigest(encoded)
	plan := scalar[string](t, s.st, `SELECT review_plan_id FROM app_platform_operations WHERE id=?`, lease.id)
	request, _ := json.Marshal([]string{"portal-operation-commit/v1", plan, digest})
	mustExec(t, s.st, `UPDATE app_platform_review_plans SET plan_json=?,plan_sha256=? WHERE id=?`, string(encoded), digest, plan)
	mustExec(t, s.st, `UPDATE app_platform_operations SET request_sha256=? WHERE id=?`, platformDigest(request), lease.id)
	return s, lease
}
func testPlatformWriterIntent(t *testing.T, pg bool) {
	s, lease := platformWriterIntentFixture(t, pg)
	i, err := s.preparePlatformInstallWriter(context.Background(), lease)
	if err != nil {
		t.Fatal(err)
	}
	v, err := control.VerifyDataWriter(i.raw, i.signature, i.authority, time.Now().UnixMilli())
	if err != nil || v.Command().PayloadSHA256 != i.command.PayloadSHA256 || v.Command().ResourceID != i.resourceID || v.Command().DataReceiptSHA256 != i.dataSHA {
		t.Fatal("signed writer", err)
	}
	expires, _ := strconv.ParseInt(v.Data().ExpiresMS, 10, 64)
	if expires > lease.leaseUntilMS-1000 {
		t.Fatal("writer outlives lease")
	}
	again, err := s.preparePlatformInstallWriter(context.Background(), lease)
	if err != nil || !samePlatformWriterIntent(i, again) {
		t.Fatal("writer retry changed identity", err)
	}
	if scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_operation_effects WHERE operation_id=?`, lease.id) != 2 || scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_activations WHERE installation_id=? AND state='candidate' AND readiness_evidence='{}' AND observed_ms IS NULL`, lease.installationID) != 1 || scalar[int](t, s.st, `SELECT COUNT(*) FROM audit_log WHERE action='app_platform.writer_intent' AND target_id=?`, lease.id) != 1 {
		t.Fatal("writer intent not exact/idempotent")
	}
	if i.applied || scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_access_grants WHERE installation_id=?`, lease.installationID) != 0 || scalar[int](t, s.st, `SELECT revision FROM app_platform_data_namespaces WHERE id=?`, i.namespaceID) != 2 {
		t.Fatal("intent fabricated writer/access")
	}
	before := dumpOf(t, s.st)
	if _, err := s.preparePlatformInstallCandidate(context.Background(), lease); err == nil {
		t.Fatal("readonly source adopted writable review")
	}
	endpoint := platformRuntimeEndpoint{path: "/run/portal-apps/runner.sock", controllerUID: 41001, controllerGID: 41001}
	if _, err := s.commitPlatformObservedWriter(context.Background(), lease, i, endpoint, transport.WritableLifecycleObservation{}); err == nil {
		t.Fatal("zero proof committed")
	}
	if !bytes.Equal(stripHeader(before), stripHeader(dumpOf(t, s.st))) {
		t.Fatal("rejected proof mutated storage")
	}
}
func TestPlatformWriterIntentSQLite(t *testing.T)   { testPlatformWriterIntent(t, false) }
func TestPlatformWriterIntentPostgres(t *testing.T) { testPlatformWriterIntent(t, true) }
func testPlatformWriterRefusals(t *testing.T, pg bool) {
	for _, mode := range []string{"runtime", "key", "trust", "publisher_key", "sponsor", "pause", "lease", "fence", "epoch", "namespace", "namespace_created", "source_created", "source", "resource", "effect", "partial", "extra", "capability"} {
		t.Run(mode, func(t *testing.T) {
			s, lease := platformWriterIntentFixture(t, pg)
			i, err := s.preparePlatformInstallWriter(context.Background(), lease)
			if err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "runtime":
				mustExec(t, s.st, `UPDATE app_platform_runtime_bindings SET enabled=0`)
			case "key":
				mustExec(t, s.st, `UPDATE app_platform_runtime_bindings SET sealed_control_key='sealed:invalid'`)
			case "trust":
				mustExec(t, s.st, `UPDATE app_platform_publishers SET enabled=0`)
			case "publisher_key":
				mustExec(t, s.st, `UPDATE app_platform_publisher_keys SET revoked_ms=1`)
			case "sponsor":
				mustExec(t, s.st, `UPDATE users SET active=0 WHERE username=(SELECT username FROM app_platform_review_plans)`)
			case "pause":
				mustExec(t, s.st, `UPDATE meta SET v='1' WHERE k='app_platform_restore_paused'`)
			case "lease":
				mustExec(t, s.st, `UPDATE app_platform_operations SET lease_until_ms=0`)
			case "fence":
				mustExec(t, s.st, `UPDATE app_platform_operations SET fence=fence+1`)
			case "epoch":
				mustExec(t, s.st, `UPDATE app_platform_operations SET controller_epoch=?`, platformFixtureID(900))
			case "namespace":
				mustExec(t, s.st, `UPDATE app_platform_data_namespaces SET revision=3`)
			case "namespace_created":
				mustExec(t, s.st, `UPDATE app_platform_data_namespaces SET created_ms=created_ms-1`)
			case "source_created":
				mustExec(t, s.st, `UPDATE app_platform_operation_effects SET created_ms=created_ms-1 WHERE effect_number=1`)
			case "source":
				mustExec(t, s.st, `UPDATE app_platform_operation_effects SET receipt_json='{}' WHERE effect_number=1`)
			case "resource":
				mustExec(t, s.st, `UPDATE app_platform_activations SET state='active'`)
			case "effect":
				mustExec(t, s.st, `UPDATE app_platform_operation_effects SET state='unknown' WHERE effect_number=2`)
			case "partial":
				mustExec(t, s.st, `DELETE FROM app_platform_operation_effects WHERE effect_number=2`)
			case "extra":
				mustExec(t, s.st, `INSERT INTO app_platform_operation_effects(operation_id,stage,effect_number,effect_id,kind,payload_sha256,runtime_id,controller_epoch,admitted_fence,observed_fence,state,receipt_json,created_ms,updated_ms) SELECT operation_id,'start',1,?,'start-candidate',payload_sha256,runtime_id,controller_epoch,admitted_fence,0,'prepared','{}',created_ms,updated_ms FROM app_platform_operation_effects WHERE effect_number=2`, platformFixtureID(901))
			case "capability":
				raw := scalar[string](t, s.st, `SELECT plan_json FROM app_platform_review_plans`)
				var p platformInstallProjection
				json.Unmarshal([]byte(raw), &p)
				p.Runtime.Profile.WritableSandboxProfile = ""
				encoded, _ := json.Marshal(p)
				mustExec(t, s.st, `UPDATE app_platform_review_plans SET plan_json=?`, string(encoded))
			}
			before := dumpOf(t, s.st)
			if _, err := s.preparePlatformInstallWriter(context.Background(), lease); err == nil {
				t.Fatal("invalid writer source accepted", mode, i.effectID)
			}
			if !bytes.Equal(stripHeader(before), stripHeader(dumpOf(t, s.st))) {
				t.Fatal("invalid writer source changed state", mode)
			}
		})
	}
}
func TestPlatformWriterRefusalsSQLite(t *testing.T)   { testPlatformWriterRefusals(t, false) }
func TestPlatformWriterRefusalsPostgres(t *testing.T) { testPlatformWriterRefusals(t, true) }
func TestWritableReviewCannotUseReadonlyRuntime(t *testing.T) {
	s, session, release := platformInstallFixture(t, false)
	if _, err := s.createPlatformWritableInstallReview(context.Background(), session, release.sha256, 1, nil, platformRuntimeEndpoint{}); err == nil {
		t.Fatal("zero runtime minted writable review")
	}
	if _, err := s.createPlatformInstallReviewForFormatContext(context.Background(), session, release.sha256, 1, nil, platformWritableInstallFormat); err == nil {
		t.Fatal("format text minted runtime proof")
	}
	s2, lease := platformCandidateIntentFixture(t, false)
	if _, err := s2.preparePlatformInstallWriter(context.Background(), lease); err == nil {
		t.Fatal("readonly review authorized writer")
	}
}
