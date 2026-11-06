package app

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
	"strconv"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/journal"
)

// Synthetic repository history only. This is not a managed activator, native
// readiness proof, writer grant or evidence that normal managed installs finish.
func platformManagedAccessFixture(t *testing.T, pg bool) (*Server, platformActorSession, platformOperationLease) {
	t.Helper()
	s, lease := platformBackendDeliveryFixture(t, pg)
	intent, err := s.preparePlatformBackendDelivery(context.Background(), lease)
	if err != nil {
		t.Fatal(err)
	}
	defer intent.registration.Close()
	now, err := platformDatabaseNow(s.st.poolExec())
	if err != nil {
		t.Fatal(err)
	}
	claim := struct {
		Format         string `json:"format"`
		ClaimID        string `json:"claim_id"`
		EffectID       string `json:"effect_id"`
		PayloadSHA256  string `json:"payload_sha256"`
		SourceSHA256   string `json:"source_sha256"`
		NativeID       string `json:"native_id"`
		DocumentSHA256 string `json:"document_sha256"`
		ClaimedMS      string `json:"claimed_ms"`
	}{"portal.runner.backend-registration-claim/1-dev.1", platformFixtureID(9981), intent.command.EffectID, intent.command.PayloadSHA256, "", intent.candidate.running.NativeID, intent.command.DocumentSHA256, strconv.FormatInt(now, 10)}
	start, _ := json.Marshal(intent.candidate.running)
	claim.SourceSHA256 = platformDigest(start)
	claimRaw, _ := json.Marshal(claim)
	wire := struct {
		Format   string `json:"format"`
		Claim    any    `json:"claim"`
		Binding  string `json:"binding_sha256"`
		Device   string `json:"device"`
		Inode    string `json:"inode"`
		Observed string `json:"observed_ms"`
		Settled  string `json:"settled_ms"`
	}{"portal.runner.backend-registration-delivery/1-dev.1", claim, platformDigest(claimRaw), "10", "100", strconv.FormatInt(now, 10), strconv.FormatInt(now, 10)}
	raw, _ := json.Marshal(wire)
	r, err := journal.ParseBackendRegistrationReceipt(raw)
	if err != nil || !r.MatchesCommand(intent.command) {
		t.Fatal("synthetic registration contract", err)
	}
	mustExec(t, s.st, `UPDATE app_platform_operation_effects SET state='applied',observed_fence=admitted_fence,receipt_json=?,updated_ms=? WHERE operation_id=? AND stage='activate'`, string(raw), now, lease.id)
	mustExec(t, s.st, `UPDATE app_platform_operations SET stage='complete',state_revision=state_revision+1,row_revision=row_revision+1,completed_ms=?,updated_ms=?,lease_owner=NULL,lease_until_ms=NULL WHERE id=?`, now, now, lease.id)
	mustExec(t, s.st, `UPDATE app_platform_activations SET state='active' WHERE installation_id=?`, lease.installationID)
	mustExec(t, s.st, `UPDATE app_platform_installations SET phase='ready' WHERE id=?`, lease.installationID)
	// Reuse the genuine administrative fixture session, but it still requires
	// an explicit audience grant and is read without administrator bypass.
	actor := platformManagedFixtureSession(t, s)
	platformGrantLaunchUser(t, s, actor, lease.installationID)
	return s, actor, lease
}

func platformManagedFixtureSession(t *testing.T, s *Server) platformActorSession {
	t.Helper()
	var actor platformActorSession
	actor.username = scalar[string](t, s.st, `SELECT username FROM user_sessions ORDER BY id LIMIT 1`)
	u := s.st.GetUser(actor.username)
	actor.created, actor.sessionRevision = u.CreatedAt, u.SessionRev
	var err error
	actor.generation, err = notificationAccountGeneration(s.st.poolExec(), actor.username)
	if err != nil {
		t.Fatal(err)
	}
	var expires int64
	if err := s.st.queryRow(`SELECT id,expires_at FROM user_sessions WHERE username=? ORDER BY id LIMIT 1`, actor.username).Scan(&actor.sessionID, &expires); err != nil {
		t.Fatal(err)
	}
	actor.expires = time.Unix(expires, 0)
	return actor
}

func testPlatformManagedAccess(t *testing.T, pg bool) {
	s, actor, lease := platformManagedAccessFixture(t, pg)
	before := dumpOf(t, s.st)
	a, err := s.preparePlatformManagedBackendAccess(context.Background(), actor, lease.installationID)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if !a.intent.applied || a.policy != 1 || a.fence != lease.fence || len(a.viewer.Subject) != 64 || a.viewer.Role != "admin" {
		t.Fatal("incomplete active access")
	}
	if !bytes.Equal(stripHeader(before), stripHeader(dumpOf(t, s.st))) {
		t.Fatal("read mutated completed operation/history")
	}
	if _, err := s.st.heartbeatPlatformOperation(lease); err == nil {
		t.Fatal("completed operation regained worker lease")
	}
	// Audience-only policy changes do not rewrite the protected registration.
	mustExec(t, s.st, `UPDATE app_platform_installations SET policy_revision=2 WHERE id=?`, lease.installationID)
	mustExec(t, s.st, `UPDATE app_platform_activations SET policy_revision=2 WHERE installation_id=?`, lease.installationID)
	mustExec(t, s.st, `UPDATE app_platform_capability_grants SET policy_revision=2 WHERE installation_id=?`, lease.installationID)
	b, err := s.preparePlatformManagedBackendAccess(context.Background(), actor, lease.installationID)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if b.policy != 2 || b.intent.command.PayloadSHA256 != a.intent.command.PayloadSHA256 || b.intent.command.DocumentSHA256 != a.intent.command.DocumentSHA256 || !reflect.DeepEqual(a.viewer, b.viewer) {
		t.Fatal("mutable audience policy rotated immutable execution identity")
	}
	metadata, err := b.intent.registration.Metadata()
	if err != nil || metadata.Identity.PolicyRevision != "1" {
		t.Fatal("bootstrap policy was rewritten", err)
	}
	if _, err := json.Marshal(b); err == nil {
		t.Fatal("private snapshot serialized")
	}
	mustExec(t, s.st, `UPDATE users SET role='user' WHERE username=?`, actor.username)
	reader, err := s.preparePlatformManagedBackendAccess(context.Background(), actor, lease.installationID)
	if err != nil {
		t.Fatal("installed app depended on installer staying an administrator", err)
	}
	defer reader.Close()
	if reader.viewer.Role != "user" || reader.viewer.Subject != b.viewer.Subject || samePlatformManagedBackendAccess(b, reader) {
		t.Fatal("role drift was cached or not fenced")
	}
	if v, err := s.inspectPlatformLaunchAccess(actor, lease.installationID); err == nil || v != (platformLaunchAccess{}) {
		t.Fatal("source read opened public managed launch")
	}
	verified, err := control.VerifyBackendRegistration(b.intent.raw, b.intent.signature, b.intent.candidate.authority, time.Now().UnixMilli())
	if err != nil || verified.Command().PayloadSHA256 != b.intent.command.PayloadSHA256 {
		t.Fatal("fresh independent signed reference", err)
	}
}
func TestPlatformManagedAccessSQLite(t *testing.T)   { testPlatformManagedAccess(t, false) }
func TestPlatformManagedAccessPostgres(t *testing.T) { testPlatformManagedAccess(t, true) }

func testPlatformManagedAccessRefusals(t *testing.T, pg bool) {
	for _, mode := range []string{"pending", "candidate", "unfinished", "lease", "fence", "grant", "session", "publisher", "runtime", "policy", "namespace", "effect", "receipt", "plan", "keyring", "seed", "future"} {
		t.Run(mode, func(t *testing.T) {
			s, actor, lease := platformManagedAccessFixture(t, pg)
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
			a, err := s.preparePlatformManagedBackendAccess(context.Background(), actor, lease.installationID)
			if err == nil {
				a.Close()
				t.Fatal("invalid current authority/history admitted")
			}
			a.Close()
			if !reflect.DeepEqual(a, platformManagedBackendAccess{}) {
				t.Fatal("refused access returned private material")
			}
			if !bytes.Equal(stripHeader(before), stripHeader(dumpOf(t, s.st))) {
				t.Fatal("refused read rewrote history")
			}
		})
	}
}
func TestPlatformManagedAccessRefusalsSQLite(t *testing.T) {
	testPlatformManagedAccessRefusals(t, false)
}
func TestPlatformManagedAccessRefusalsPostgres(t *testing.T) {
	testPlatformManagedAccessRefusals(t, true)
}
