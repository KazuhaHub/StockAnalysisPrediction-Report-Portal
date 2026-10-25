package app

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

func platformTrustFixtureKey(seed byte, id string) appplatform.PublisherKey {
	private := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{seed}, ed25519.SeedSize))
	return appplatform.PublisherKey{ID: id, Algorithm: "Ed25519", PublicKey: base64.RawURLEncoding.EncodeToString(private.Public().(ed25519.PublicKey)), NotBeforeMS: "0", Revision: "1"}
}
func platformTrustTestServer(t *testing.T, pg bool) (*Server, platformActorSession) {
	t.Helper()
	s, session := platformActorTestServer(t, pg)
	if err := s.initializeAppPlatformControl(session); err != nil {
		t.Fatal(err)
	}
	return s, session
}
func platformRegisterTestPublisher(t *testing.T, s *Server, session platformActorSession) platformPublisherTrustRevision {
	t.Helper()
	key := platformTrustFixtureKey(7, "first-key")
	result, err := s.changePlatformPublisherTrust(session, platformPublisherChange{kind: "register", publisherID: "fixture-publisher", enabled: true, namespaces: []appplatform.PublisherNamespace{{Kind: "prefix", Value: "notes-"}}, key: &key})
	if err != nil {
		t.Fatal(err)
	}
	return result
}
func testPlatformPublisherTrust(t *testing.T, pg bool) {
	s, session := platformTrustTestServer(t, pg)
	registered := platformRegisterTestPublisher(t, s, session)
	if registered.revision != 1 || registered.keyRevision != 1 {
		t.Fatal("initial trust revision")
	}
	before := dumpOf(t, s.st)
	key := platformTrustFixtureKey(8, "first-key")
	if _, err := s.changePlatformPublisherTrust(session, platformPublisherChange{kind: "add_key", publisherID: registered.publisherID, expectedRevision: 1, key: &key}); !errors.Is(err, errAppPlatformTrustConflict) {
		t.Fatalf("overwritten key: %v", err)
	}
	if !bytes.Equal(stripHeader(before), stripHeader(dumpOf(t, s.st))) {
		t.Fatal("refusal mutated trust")
	}
	key = platformTrustFixtureKey(8, "second-key")
	rotated, err := s.changePlatformPublisherTrust(session, platformPublisherChange{kind: "add_key", publisherID: registered.publisherID, expectedRevision: 1, key: &key})
	if err != nil {
		t.Fatal(err)
	}
	if rotated.revision != 2 || rotated.keyRevision != 1 {
		t.Fatal("rotation revision")
	}
	revoked, err := s.changePlatformPublisherTrust(session, platformPublisherChange{kind: "revoke_key", publisherID: registered.publisherID, expectedRevision: 2, keyID: "first-key", expectedKeyRevision: 1})
	if err != nil {
		t.Fatal(err)
	}
	if revoked.revision != 3 || revoked.keyRevision != 2 {
		t.Fatal("revocation revision")
	}
	if scalar[int64](t, s.st, `SELECT revoked_ms FROM app_platform_publisher_keys WHERE key_id='first-key'`) <= 0 {
		t.Fatal("revocation missing")
	}
	repeat, err := s.changePlatformPublisherTrust(session, platformPublisherChange{kind: "revoke_key", publisherID: registered.publisherID, expectedRevision: 3, keyID: "first-key", expectedKeyRevision: 2})
	if err != nil || repeat != revoked {
		t.Fatalf("repeat revocation: %v", err)
	}
	disabled, err := s.changePlatformPublisherTrust(session, platformPublisherChange{kind: "policy", publisherID: registered.publisherID, expectedRevision: 3, enabled: false, namespaces: []appplatform.PublisherNamespace{{Kind: "prefix", Value: "notes-"}}})
	if err != nil || disabled.revision != 4 {
		t.Fatalf("disable: %v", err)
	}
	competitor := platformTrustFixtureKey(9, "competitor-key")
	if _, err := s.changePlatformPublisherTrust(session, platformPublisherChange{kind: "register", publisherID: "competitor", enabled: true, namespaces: []appplatform.PublisherNamespace{{Kind: "exact", Value: "notes-sample"}}, key: &competitor}); !errors.Is(err, errAppPlatformTrustConflict) {
		t.Fatalf("disabled owner lost namespace: %v", err)
	}
	fresh := &Server{st: s.st, cfg: s.cfg}
	if err := fresh.withAppPlatformActor(session, false, func(e migExec, _ platformActorFacts, _ platformActorControl) error {
		if err := lockPlatformPublisherTrust(e); err != nil {
			return err
		}
		trust, err := loadPlatformPublisherTrust(e)
		if err != nil {
			return err
		}
		if len(trust.Publishers) != 1 || len(trust.Publishers[0].Keys) != 2 || trust.Publishers[0].Enabled || trust.Publishers[0].Keys[0].RevokedMS == nil {
			t.Fatal("fresh process lost persisted trust")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
func testPlatformPublisherConcurrentNamespaces(t *testing.T, pg bool) {
	s, session := platformTrustTestServer(t, pg)
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, publisher := range []string{"publisher-a", "publisher-b"} {
		wg.Go(func() {
			key := platformTrustFixtureKey(7, "key")
			_, err := s.changePlatformPublisherTrust(session, platformPublisherChange{kind: "register", publisherID: publisher, enabled: true, namespaces: []appplatform.PublisherNamespace{{Kind: "prefix", Value: "notes-"}}, key: &key})
			results <- err
		})
	}
	wg.Wait()
	close(results)
	success := 0
	for err := range results {
		if err == nil {
			success++
		} else if !errors.Is(err, errAppPlatformTrustConflict) {
			t.Fatal(err)
		}
	}
	if success != 1 || scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_publishers`) != 1 {
		t.Fatal("concurrent namespace takeover")
	}
}
func TestPlatformPublisherTrustSQLite(t *testing.T) {
	testPlatformPublisherTrust(t, false)
	testPlatformPublisherConcurrentNamespaces(t, false)
}
func TestPlatformPublisherTrustPostgres(t *testing.T) {
	testPlatformPublisherTrust(t, true)
	testPlatformPublisherConcurrentNamespaces(t, true)
}

func testPlatformTrustRefusal(t *testing.T, pg bool) {
	for _, name := range []string{"stale", "unknown_kind", "extra_fields", "key_revision", "bad_algorithm", "duplicate_json", "paused", "demoted", "overflow"} {
		t.Run(name, func(t *testing.T) {
			s, session := platformTrustTestServer(t, pg)
			platformRegisterTestPublisher(t, s, session)
			change := platformPublisherChange{kind: "policy", publisherID: "fixture-publisher", expectedRevision: 1, enabled: true, namespaces: []appplatform.PublisherNamespace{{Kind: "prefix", Value: "notes-"}}}
			switch name {
			case "stale":
				change.expectedRevision = 2
			case "unknown_kind":
				change.kind = "unrevoke"
			case "extra_fields":
				key := platformTrustFixtureKey(9, "ignored")
				change.key = &key
			case "key_revision", "bad_algorithm":
				key := platformTrustFixtureKey(9, "next")
				change = platformPublisherChange{kind: "add_key", publisherID: "fixture-publisher", expectedRevision: 1, key: &key}
				if name == "key_revision" {
					key.Revision = "2"
				} else {
					key.Algorithm = "RSA"
				}
			case "duplicate_json":
				mustExec(t, s.st, `UPDATE app_platform_publishers SET namespaces_json='[{"kind":"exact","kind":"prefix","value":"notes-"}]'`)
			case "paused":
				mustExec(t, s.st, `UPDATE meta SET v='1' WHERE k='app_platform_restore_paused'`)
			case "demoted":
				mustExec(t, s.st, `UPDATE users SET role='user' WHERE username=?`, session.username)
			case "overflow":
				mustExec(t, s.st, `UPDATE app_platform_publishers SET revision=9223372036854775807`)
				change.expectedRevision = 9223372036854775807
			}
			before := dumpOf(t, s.st)
			if _, err := s.changePlatformPublisherTrust(session, change); err == nil {
				t.Fatal("invalid authority/trust mutation accepted")
			}
			if !bytes.Equal(stripHeader(before), stripHeader(dumpOf(t, s.st))) {
				t.Fatal("refused change wrote state")
			}
		})
	}
}
func platformFailTrustAudit(t *testing.T, s *Server) {
	t.Helper()
	if s.st.driver == "postgres" {
		mustExec(t, s.st, `CREATE FUNCTION fixture_trust_audit_failure() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action IN ('app_platform.publisher_trust_changed','app_platform.release_staged') THEN RAISE EXCEPTION 'fixture trust audit failure'; END IF; RETURN NEW; END $$`)
		mustExec(t, s.st, `CREATE TRIGGER fixture_trust_audit_failure BEFORE INSERT ON audit_log FOR EACH ROW EXECUTE FUNCTION fixture_trust_audit_failure()`)
		t.Cleanup(func() {
			if _, err := s.st.exec(`DROP TRIGGER IF EXISTS fixture_trust_audit_failure ON audit_log`); err != nil {
				t.Error(err)
			}
			if _, err := s.st.exec(`DROP FUNCTION IF EXISTS fixture_trust_audit_failure()`); err != nil {
				t.Error(err)
			}
		})
	} else {
		mustExec(t, s.st, `CREATE TRIGGER fixture_trust_audit_failure BEFORE INSERT ON audit_log WHEN NEW.action IN ('app_platform.publisher_trust_changed','app_platform.release_staged') BEGIN SELECT RAISE(ABORT,'fixture trust audit failure');END`)
	}
}
func testPlatformTrustAtomicAudit(t *testing.T, pg bool) {
	s, session := platformTrustTestServer(t, pg)
	platformRegisterTestPublisher(t, s, session)
	platformFailTrustAudit(t, s)
	before := dumpOf(t, s.st)
	if _, err := s.changePlatformPublisherTrust(session, platformPublisherChange{kind: "revoke_key", publisherID: "fixture-publisher", expectedRevision: 1, keyID: "first-key", expectedKeyRevision: 1}); err == nil {
		t.Fatal("failed audit accepted")
	}
	if !bytes.Equal(stripHeader(before), stripHeader(dumpOf(t, s.st))) {
		t.Fatal("failed audit left key revoked or revision advanced")
	}
	raw, sig, frontend := platformSignedReleaseFixture(t, "1.0.0-beta.1")
	if _, err := s.stagePlatformRelease(session, raw, sig, frontend); err == nil {
		t.Fatal("failed release audit accepted")
	}
	if !bytes.Equal(stripHeader(before), stripHeader(dumpOf(t, s.st))) {
		t.Fatal("failed release audit left orphaned bytes")
	}
}
func TestPlatformTrustRefusalSQLite(t *testing.T) {
	testPlatformTrustRefusal(t, false)
	testPlatformTrustAtomicAudit(t, false)
}
func TestPlatformTrustRefusalPostgres(t *testing.T) {
	testPlatformTrustRefusal(t, true)
	testPlatformTrustAtomicAudit(t, true)
}

func testPlatformTrustInvalidatesReviews(t *testing.T, pg bool) {
	s, session := platformTrustTestServer(t, pg)
	platformRegisterTestPublisher(t, s, session)
	now, err := platformDatabaseNow(s.st.poolExec())
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		var consumed any
		if i == 1 {
			consumed = int64(1)
		}
		platformTestInsert(t, s.st.poolExec(), "review_plans", map[string]any{"id": platformFixtureID(150 + i), "installation_id": nil, "app_id": "notes-sample", "publisher_id": "fixture-publisher", "action": "install", "source_release_sha256": nil, "target_release_sha256": nil, "installation_revision": 0, "policy_revision": 0, "trust_revision": 1, "controller_epoch": s.st.GetSetting("app_platform_controller_epoch", ""), "actor_scope": platformDigest([]byte("fixture")), "username": nil, "account_created": "", "account_generation": 0, "authority_fingerprint": platformDigest([]byte("authority")), "plan_sha256": platformDigest([]byte("plan")), "plan_json": "{}", "expires_ms": now + 60000, "consumed_ms": consumed, "created_ms": now, "updated_ms": now})
	}
	if _, err := s.changePlatformPublisherTrust(session, platformPublisherChange{kind: "revoke_key", publisherID: "fixture-publisher", expectedRevision: 1, keyID: "first-key", expectedKeyRevision: 1}); err != nil {
		t.Fatal(err)
	}
	current, err := platformDatabaseNow(s.st.poolExec())
	if err != nil {
		t.Fatal(err)
	}
	if expiry := scalar[int64](t, s.st, `SELECT expires_ms FROM app_platform_review_plans WHERE consumed_ms IS NULL`); expiry > current {
		t.Fatal("uncommitted review not invalidated")
	}
	if expiry := scalar[int64](t, s.st, `SELECT expires_ms FROM app_platform_review_plans WHERE consumed_ms IS NOT NULL`); expiry != now+60000 {
		t.Fatal("consumed historical review changed")
	}
}
func TestPlatformTrustReviewInvalidationSQLite(t *testing.T) {
	testPlatformTrustInvalidatesReviews(t, false)
}
func TestPlatformTrustReviewInvalidationPostgres(t *testing.T) {
	testPlatformTrustInvalidatesReviews(t, true)
}

func TestPlatformTrustAcrossIndependentSQLiteStores(t *testing.T) {
	path := filepath.Join(t.TempDir(), "trust.db")
	first, err := OpenStore("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = first.Close() })
	s, session := platformActorTestServerWithStore(t, first)
	if err := s.initializeAppPlatformControl(session); err != nil {
		t.Fatal(err)
	}
	platformRegisterTestPublisher(t, s, session)
	raw, sig, archive := platformSignedReleaseFixture(t, "1.0.0-beta.1")
	staged, err := s.stagePlatformRelease(session, raw, sig, archive)
	if err != nil {
		t.Fatal(err)
	}
	second, err := OpenStore("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = second.Close() })
	other := &Server{st: second, cfg: s.cfg}
	if _, err := other.recheckPlatformStagedRelease(session, staged.sha256, 1); err != nil {
		t.Fatal(err)
	}
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for i, server := range []*Server{s, other} {
		wg.Go(func() {
			key := platformTrustFixtureKey(9, "key")
			_, err := server.changePlatformPublisherTrust(session, platformPublisherChange{kind: "register", publisherID: fmt.Sprintf("competing-%d", i), enabled: true, namespaces: []appplatform.PublisherNamespace{{Kind: "prefix", Value: "contended-"}}, key: &key})
			results <- err
		})
	}
	wg.Wait()
	close(results)
	success := 0
	for err := range results {
		if err == nil {
			success++
		} else if !errors.Is(err, errAppPlatformTrustConflict) {
			t.Fatal(err)
		}
	}
	if success != 1 {
		t.Fatal("independent stores accepted overlapping ownership")
	}
	if _, err := other.changePlatformPublisherTrust(session, platformPublisherChange{kind: "revoke_key", publisherID: "fixture-publisher", expectedRevision: 1, keyID: "first-key", expectedKeyRevision: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.recheckPlatformStagedRelease(session, staged.sha256, 2); err == nil {
		t.Fatal("independent revocation invisible")
	}
	if err := second.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenStore("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	fresh := &Server{st: reopened, cfg: s.cfg}
	if _, err := fresh.recheckPlatformStagedRelease(session, staged.sha256, 2); err == nil {
		t.Fatal("reopen forgot revocation")
	}
}
