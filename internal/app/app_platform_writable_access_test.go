package app

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/journal"
	"reflect"
	"strconv"
	"testing"
	"time"
)

// Historical repository metadata only. This does not run an activator or produce
// a host/native boot proof. Actual activation succeeds only in the Linux fixture.
func platformWritableAppliedFixture(t *testing.T, pg bool) (*Server, platformOperationLease, platformWritableRegistrationIntent) {
	t.Helper()
	s, lease := platformWritableRegistrationFixture(t, pg)
	actor := platformManagedFixtureSession(t, s)
	nowAudience, err := platformDatabaseNow(s.st.poolExec())
	if err != nil {
		t.Fatal(err)
	}
	audience, err := platformAudienceUser(s.st.poolExec(), actor.username, nowAudience)
	if err != nil {
		t.Fatal(err)
	}
	var plan platformInstallProjection
	planID := scalar[string](t, s.st, `SELECT review_plan_id FROM app_platform_operations WHERE id=?`, lease.id)
	original := scalar[string](t, s.st, `SELECT plan_json FROM app_platform_review_plans WHERE id=?`, planID)
	if err := json.Unmarshal([]byte(original), &plan); err != nil {
		t.Fatal(err)
	}
	plan.Audience = []platformInstallAudience{audience}
	encodedPlan, _ := json.Marshal(plan)
	planSHA := platformDigest(encodedPlan)
	request, _ := json.Marshal([]string{"portal-operation-commit/v1", planID, planSHA})
	mustExec(t, s.st, `UPDATE app_platform_review_plans SET plan_json=?,plan_sha256=? WHERE id=?`, string(encodedPlan), planSHA, planID)
	mustExec(t, s.st, `UPDATE app_platform_operations SET request_sha256=? WHERE id=?`, platformDigest(request), lease.id)

	i, err := s.preparePlatformWritableRegistration(context.Background(), lease)
	if err != nil {
		t.Fatal(err)
	}
	defer i.registration.Close()
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
	return s, lease, applied
}
func platformWritableAccessFixture(t *testing.T, pg bool) (*Server, platformActorSession, platformOperationLease) {
	t.Helper()
	s, lease, i := platformWritableAppliedFixture(t, pg)
	defer i.registration.Close()
	now, err := platformDatabaseNow(s.st.poolExec())
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, s.st, `UPDATE app_platform_operations SET stage='complete',state_revision=state_revision+1,row_revision=row_revision+1,completed_ms=?,updated_ms=?,lease_owner=NULL,lease_until_ms=NULL WHERE id=?`, now, now, lease.id)
	mustExec(t, s.st, `UPDATE app_platform_activations SET state='active',updated_ms=? WHERE installation_id=?`, now, lease.installationID)
	mustExec(t, s.st, `UPDATE app_platform_installations SET phase='ready' WHERE id=?`, lease.installationID)
	actor := platformManagedFixtureSession(t, s)
	platformGrantLaunchUser(t, s, actor, lease.installationID)
	return s, actor, lease
}
func testPlatformWritableAccess(t *testing.T, pg bool) {
	s, actor, lease := platformWritableAccessFixture(t, pg)
	before := dumpOf(t, s.st)
	a, err := s.preparePlatformWritableBackendAccess(context.Background(), actor, lease.installationID)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if !a.intent.applied || a.policy != 1 || a.fence != lease.fence || len(a.viewer.Subject) != 64 || a.viewer.Role != "admin" {
		t.Fatal("incomplete writable source")
	}
	if !bytes.Equal(stripHeader(before), stripHeader(dumpOf(t, s.st))) {
		t.Fatal("source reader changed storage")
	}
	if _, err := s.st.heartbeatPlatformOperation(lease); err == nil {
		t.Fatal("completed operation regained lease")
	}
	mustExec(t, s.st, `UPDATE app_platform_installations SET policy_revision=2 WHERE id=?`, lease.installationID)
	mustExec(t, s.st, `UPDATE app_platform_activations SET policy_revision=2 WHERE installation_id=?`, lease.installationID)
	mustExec(t, s.st, `UPDATE app_platform_capability_grants SET policy_revision=2 WHERE installation_id=?`, lease.installationID)
	b, err := s.preparePlatformWritableBackendAccess(context.Background(), actor, lease.installationID)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if b.policy != 2 || b.intent.command.PayloadSHA256 != a.intent.command.PayloadSHA256 || b.intent.command.DocumentSHA256 != a.intent.command.DocumentSHA256 || !reflect.DeepEqual(a.viewer, b.viewer) {
		t.Fatal("audience policy changed immutable execution identity")
	}
	meta, err := b.intent.registration.Metadata()
	if err != nil || meta.Identity.PolicyRevision != "1" {
		t.Fatal("bootstrap policy rewritten", err)
	}
	if _, err := json.Marshal(b); err == nil {
		t.Fatal("private access serialized")
	}
	mustExec(t, s.st, `UPDATE users SET role='user' WHERE username=?`, actor.username)
	reader, err := s.preparePlatformWritableBackendAccess(context.Background(), actor, lease.installationID)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	if reader.viewer.Role != "user" || reader.viewer.Subject != b.viewer.Subject || samePlatformWritableBackendAccess(b, reader) {
		t.Fatal("viewer role cached or installer admin status required")
	}
	if old, err := s.preparePlatformManagedBackendAccess(context.Background(), actor, lease.installationID); err == nil {
		old.Close()
		t.Fatal("readonly reader accepted writable history")
	}
	if _, err := s.preparePlatformWritableRegistration(context.Background(), lease); err == nil {
		t.Fatal("completed source became live install authority")
	}
	v, err := control.VerifyWritableBackendRegistration(b.intent.raw, b.intent.signature, b.intent.start.writer.authority, time.Now().UnixMilli())
	if err != nil || v.Command().PayloadSHA256 != b.intent.command.PayloadSHA256 {
		t.Fatal("renewed signed source", err)
	}
	if _, err := s.inspectPlatformLaunchAccess(actor, lease.installationID); err == nil {
		t.Fatal("source read opened generic managed launch")
	}
}
func TestPlatformWritableAccessSQLite(t *testing.T)   { testPlatformWritableAccess(t, false) }
func TestPlatformWritableAccessPostgres(t *testing.T) { testPlatformWritableAccess(t, true) }

func testPlatformWritableAccessRefusals(t *testing.T, pg bool) {
	for _, mode := range []string{"pending", "candidate", "unfinished", "lease", "fence", "grant", "session", "publisher", "runtime", "policy", "namespace", "effect", "receipt", "plan", "keyring", "seed", "future"} {
		t.Run(mode, func(t *testing.T) {
			s, actor, lease := platformWritableAccessFixture(t, pg)
			switch mode {
			case "pending":
				mustExec(t, s.st, `UPDATE app_platform_installations SET phase='pending'`)
			case "candidate":
				mustExec(t, s.st, `UPDATE app_platform_activations SET state='candidate'`)
			case "unfinished":
				mustExec(t, s.st, `UPDATE app_platform_operations SET stage='activate',completed_ms=NULL`)
			case "lease":
				mustExec(t, s.st, `UPDATE app_platform_operations SET stage='activate',completed_ms=NULL,lease_owner=?,lease_until_ms=?`, platformFixtureID(9988), time.Now().UnixMilli()+30000)
			case "fence":
				mustExec(t, s.st, `UPDATE app_platform_installations SET control_fence=control_fence+1`)
			case "grant":
				mustExec(t, s.st, `DELETE FROM app_platform_access_grants`)
			case "session":
				mustExec(t, s.st, `UPDATE user_sessions SET revoked=1 WHERE id=?`, actor.sessionID)
			case "publisher":
				mustExec(t, s.st, `UPDATE app_platform_publishers SET enabled=0`)
			case "runtime":
				mustExec(t, s.st, `UPDATE app_platform_runtime_bindings SET enabled=0`)
			case "policy":
				mustExec(t, s.st, `UPDATE app_platform_installations SET policy_revision=2`)
			case "namespace":
				mustExec(t, s.st, `UPDATE app_platform_data_namespaces SET state='retained'`)
			case "effect":
				mustExec(t, s.st, `UPDATE app_platform_operation_effects SET state='unknown' WHERE stage='activate'`)
			case "receipt":
				mustExec(t, s.st, `UPDATE app_platform_operation_effects SET receipt_json='{}' WHERE stage='activate'`)
			case "plan":
				mustExec(t, s.st, `UPDATE app_platform_review_plans SET plan_sha256=?`, platformDigest([]byte("changed")))
			case "keyring":
				mustExec(t, s.st, `DELETE FROM meta WHERE k IN (?,?)`, setKeyringSalt, setKeyringDEK)
			case "seed":
				mustExec(t, s.st, `UPDATE app_platform_installations SET sealed_identity_seed='broken'`)
			case "future":
				mustExec(t, s.st, `UPDATE app_platform_operations SET updated_ms=?`, time.Now().UnixMilli()+60000)
			}
			before := dumpOf(t, s.st)
			a, err := s.preparePlatformWritableBackendAccess(context.Background(), actor, lease.installationID)
			if err == nil {
				a.Close()
				t.Fatal("invalid current authority/history admitted")
			}
			a.Close()
			if !reflect.DeepEqual(a, platformWritableBackendAccess{}) {
				t.Fatal("refused access returned private material")
			}
			if !bytes.Equal(stripHeader(before), stripHeader(dumpOf(t, s.st))) {
				t.Fatal("refused read rewrote history")
			}
		})
	}
}
func TestPlatformWritableAccessRefusalsSQLite(t *testing.T) {
	testPlatformWritableAccessRefusals(t, false)
}
func TestPlatformWritableAccessRefusalsPostgres(t *testing.T) {
	testPlatformWritableAccessRefusals(t, true)
}
