package app

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/journal"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/transport"
)

func platformWritableRegistrationFixture(t *testing.T, pg bool) (*Server, platformOperationLease) {
	t.Helper()
	s, lease, start := platformWritableProbeFixture(t, pg)
	lease, _ = seedPlatformStoredWritableReadiness(t, s, lease, start)
	return s, lease
}
func testPlatformWritableRegistrationIntent(t *testing.T, pg bool) {
	s, lease := platformWritableRegistrationFixture(t, pg)
	i, err := s.preparePlatformWritableRegistration(context.Background(), lease)
	if err != nil {
		t.Fatal(err)
	}
	defer i.registration.Close()
	v, err := control.VerifyWritableBackendRegistration(i.raw, i.signature, i.start.writer.authority, time.Now().UnixMilli())
	if err != nil {
		t.Fatal(err)
	}
	document, err := i.registration.Document()
	if err != nil {
		t.Fatal(err)
	}
	defer clear(document)
	parsed, err := control.ValidateWritableBackendRegistrationDocument(v, i.start.writer.descriptor, document)
	if err != nil {
		t.Fatal(err)
	}
	parsed.Close()
	before := dumpOf(t, s.st)
	var private struct {
		Key []byte `json:"signing_key"`
	}
	if err := json.Unmarshal(document, &private); err != nil || len(private.Key) != 32 {
		t.Fatal("private document shape", err)
	}
	defer clear(private.Key)
	if bytes.Contains(before, document) || bytes.Contains(before, []byte(base64.StdEncoding.EncodeToString(private.Key))) || bytes.Contains(i.raw, []byte(base64.StdEncoding.EncodeToString(private.Key))) {
		t.Fatal("private registration persisted")
	}
	time.Sleep(3100 * time.Millisecond)
	if freshPlatformWritableRegistrationSource(i.start) {
		t.Fatal("history unexpectedly fresh")
	}
	again, err := s.preparePlatformWritableRegistration(context.Background(), lease)
	if err != nil {
		t.Fatal(err)
	}
	defer again.registration.Close()
	if !samePlatformWritableRegistrationIntent(i, again) || i.applied {
		t.Fatal("intent duplicated/replaced/fabricated delivery")
	}
	if scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_operation_effects WHERE operation_id=?`, lease.id) != 4 || scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_access_grants WHERE installation_id=?`, lease.installationID) != 0 || scalar[int](t, s.st, `SELECT COUNT(*) FROM audit_log WHERE action='app_platform.writable_registration_intent' AND target_id=?`, lease.id) != 1 {
		t.Fatal("intent changed grants/history")
	}
	if _, err := s.commitPlatformWritableRegistration(context.Background(), lease, i, platformRuntimeEndpoint{}, transport.WritableLifecycleObservation{}); err == nil {
		t.Fatal("zero proof committed")
	}
	if !bytes.Equal(stripHeader(before), stripHeader(dumpOf(t, s.st))) {
		t.Fatal("refusal changed state")
	}
	if _, err := s.preparePlatformWritableProbe(context.Background(), lease); err == nil {
		t.Fatal("readiness reader accepted fourth effect")
	}
	if _, err := s.preparePlatformBackendDelivery(context.Background(), lease); err == nil {
		t.Fatal("readonly delivery accepted writable source")
	}
	// Construct historical file metadata only: it cannot create an opaque proof.
	var receipt journal.WritableBackendRegistrationReceipt
	if err := json.Unmarshal([]byte(`{"claim":{}}`), &receipt); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UnixMilli()
	receipt.Format = "portal.runner.writable-backend-registration-delivery/1-dev.1"
	m := &receipt.Claim
	m.Format = "portal.runner.writable-backend-registration-claim/1-dev.1"
	m.ClaimID = platformFixtureID(921)
	m.EffectID = i.command.EffectID
	m.PayloadSHA256 = i.command.PayloadSHA256
	m.SourceSHA256 = i.command.RunningReceiptSHA256
	m.DocumentSHA256 = i.command.DocumentSHA256
	m.NativeID = i.start.running.NativeID
	m.ControlRevision = strconv.FormatInt(i.start.writer.runtime.controlRevision, 10)
	m.ClaimedMS = strconv.FormatInt(now, 10)
	m.ExpiresMS = strconv.FormatInt(now+10000, 10)
	claim, _ := json.Marshal(receipt.Claim)
	receipt.BindingSHA256 = platformDigest(claim)
	receipt.Device = "9"
	receipt.Inode = "10"
	receipt.ObservedMS = strconv.FormatInt(now, 10)
	receipt.SettledMS = receipt.ObservedMS
	raw, _ := json.Marshal(receipt)
	if _, err := journal.ParseWritableBackendRegistrationReceipt(raw); err != nil || !platformWritableRegistrationReceiptMatches(receipt, i, now) {
		t.Fatal("synthetic receipt shape", err)
	}
	dbNow, err := platformDatabaseNow(migExec{s: s.st, ex: s.st.db})
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, s.st, `UPDATE app_platform_operation_effects SET state='applied',observed_fence=admitted_fence,receipt_json=?,updated_ms=? WHERE operation_id=? AND stage='activate'`, string(raw), dbNow, lease.id)
	applied, err := s.preparePlatformWritableRegistration(context.Background(), lease)
	if err != nil {
		t.Fatal(err)
	}
	defer applied.registration.Close()
	if !applied.applied || applied.receipt != receipt || applied.command.PayloadSHA256 != i.command.PayloadSHA256 {
		t.Fatal("applied history changed")
	}
}
func TestPlatformWritableRegistrationIntentSQLite(t *testing.T) {
	testPlatformWritableRegistrationIntent(t, false)
}
func TestPlatformWritableRegistrationIntentPostgres(t *testing.T) {
	testPlatformWritableRegistrationIntent(t, true)
}

func testPlatformWritableRegistrationRefusals(t *testing.T, pg bool) {
	for _, mode := range []string{"readiness", "stale_readiness", "source", "key", "runtime", "publisher", "pause", "lease", "effect", "digest", "receipt", "unknown", "observed", "time", "extra", "audit"} {
		t.Run(mode, func(t *testing.T) {
			s, lease := platformWritableRegistrationFixture(t, pg)
			if mode == "audit" {

				if pg {
					t.Cleanup(func() {
						mustExec(t, s.st, `DROP TRIGGER IF EXISTS writable_registration_audit_failure ON audit_log`)
						mustExec(t, s.st, `DROP FUNCTION IF EXISTS writable_registration_audit_failure()`)
					})
					mustExec(t, s.st, `CREATE FUNCTION writable_registration_audit_failure() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'registration audit fixture'; END $$`)
					mustExec(t, s.st, `CREATE TRIGGER writable_registration_audit_failure BEFORE INSERT ON audit_log FOR EACH ROW WHEN (NEW.action='app_platform.writable_registration_intent') EXECUTE FUNCTION writable_registration_audit_failure()`)
				} else {
					mustExec(t, s.st, `CREATE TRIGGER writable_registration_audit_failure BEFORE INSERT ON audit_log WHEN NEW.action='app_platform.writable_registration_intent' BEGIN SELECT RAISE(ABORT,'registration intent fixture'); END`)
				}
			}
			if mode == "readiness" {
				mustExec(t, s.st, `UPDATE app_platform_activations SET readiness_evidence='{}',observed_ms=NULL`)
			}
			if mode == "source" {
				mustExec(t, s.st, `UPDATE app_platform_operation_effects SET receipt_json='{}' WHERE stage='start'`)
			}
			if mode != "readiness" && mode != "stale_readiness" && mode != "source" && mode != "audit" {
				i, err := s.preparePlatformWritableRegistration(context.Background(), lease)
				if err != nil {
					t.Fatal(err)
				}
				i.registration.Close()
			}
			switch mode {
			case "stale_readiness":
				time.Sleep(3100 * time.Millisecond)
			case "key":
				mustExec(t, s.st, `UPDATE app_platform_installations SET sealed_identity_seed='invalid' WHERE id=?`, lease.installationID)
			case "runtime":
				mustExec(t, s.st, `UPDATE app_platform_runtime_bindings SET enabled=0`)
			case "publisher":
				mustExec(t, s.st, `UPDATE app_platform_publishers SET enabled=0`)
			case "pause":
				mustExec(t, s.st, `UPDATE meta SET v='1' WHERE k='app_platform_restore_paused'`)
			case "lease":
				mustExec(t, s.st, `UPDATE app_platform_operations SET lease_until_ms=0`)
			case "effect":
				mustExec(t, s.st, `UPDATE app_platform_operation_effects SET effect_id=? WHERE stage='activate'`, scalar[string](t, s.st, `SELECT id FROM app_platform_data_namespaces WHERE owner_installation_id=?`, lease.installationID))
			case "digest":
				mustExec(t, s.st, `UPDATE app_platform_operation_effects SET payload_sha256=? WHERE stage='activate'`, strings.Repeat("f", 64))
			case "receipt":
				mustExec(t, s.st, `UPDATE app_platform_operation_effects SET receipt_json='{"foreign":true}' WHERE stage='activate'`)
			case "unknown":
				mustExec(t, s.st, `UPDATE app_platform_operation_effects SET state='unknown' WHERE stage='activate'`)
			case "observed":
				mustExec(t, s.st, `UPDATE app_platform_operation_effects SET observed_fence=admitted_fence WHERE stage='activate'`)
			case "time":
				mustExec(t, s.st, `UPDATE app_platform_operation_effects SET updated_ms=created_ms-1 WHERE stage='activate'`)
			case "extra":
				mustExec(t, s.st, `INSERT INTO app_platform_operation_effects(operation_id,stage,effect_number,effect_id,kind,payload_sha256,runtime_id,controller_epoch,admitted_fence,observed_fence,state,receipt_json,created_ms,updated_ms) SELECT operation_id,'activate',2,?,'activate',payload_sha256,runtime_id,controller_epoch,admitted_fence,0,'prepared','{}',created_ms,updated_ms FROM app_platform_operation_effects WHERE stage='activate'`, platformFixtureID(923))
			}
			before := dumpOf(t, s.st)
			if i, err := s.preparePlatformWritableRegistration(context.Background(), lease); err == nil {
				i.registration.Close()
				t.Fatal("invalid registration source accepted", mode)
			}
			if !bytes.Equal(stripHeader(before), stripHeader(dumpOf(t, s.st))) {
				t.Fatal("refusal changed state")
			}
		})
	}
}
func TestPlatformWritableRegistrationRefusalsSQLite(t *testing.T) {
	testPlatformWritableRegistrationRefusals(t, false)
}
func TestPlatformWritableRegistrationRefusalsPostgres(t *testing.T) {
	testPlatformWritableRegistrationRefusals(t, true)
}

func TestPlatformWritableRegistrationFreshness(t *testing.T) {
	var i platformWritableStartIntent
	i.writer.activationObserved = sql.NullInt64{Int64: 10000, Valid: true}
	if !freshPlatformWritableRegistrationSourceAt(i, 10000) || !freshPlatformWritableRegistrationSourceAt(i, 13000) || freshPlatformWritableRegistrationSourceAt(i, 13001) || freshPlatformWritableRegistrationSourceAt(i, 9999) {
		t.Fatal("fresh registration readiness boundary")
	}
	i.writer.activationObserved.Valid = false
	if freshPlatformWritableRegistrationSourceAt(i, 10000) {
		t.Fatal("missing observation accepted")
	}
}
