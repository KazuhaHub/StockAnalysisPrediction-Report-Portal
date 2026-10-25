package app

import (
	"archive/zip"
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

func platformSignedReleaseFixture(t *testing.T, version string) (raw, signature, frontend []byte) {
	t.Helper()
	var archive bytes.Buffer
	w := zip.NewWriter(&archive)
	file, err := w.Create("index.html")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = file.Write([]byte("<!doctype html><title>Fixture</title>")); err != nil {
		t.Fatal(err)
	}
	if err = w.Close(); err != nil {
		t.Fatal(err)
	}
	frontend = archive.Bytes()
	raw, err = os.ReadFile("../appplatform/testdata/descriptors/static.json")
	if err != nil {
		t.Fatal(err)
	}
	raw = bytes.ReplaceAll(raw, []byte("example-publisher"), []byte("fixture-publisher"))
	raw = bytes.ReplaceAll(raw, []byte(strings.Repeat("a", 64)), []byte(platformDigest(frontend)))
	raw = bytes.ReplaceAll(raw, []byte("1.0.0-beta.1"), []byte(version))
	signature = platformSignTestDescriptor(t, raw)
	return
}
func platformSignTestDescriptor(t *testing.T, raw []byte) []byte {
	t.Helper()
	prefix := []byte("portal.app/descriptor/v2\x00")
	frame := append([]byte{}, prefix...)
	length := make([]byte, 8)
	binary.BigEndian.PutUint64(length, uint64(len(raw)))
	frame = append(frame, length...)
	frame = append(frame, raw...)
	private := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{7}, ed25519.SeedSize))
	envelope, err := json.Marshal(map[string]string{"format": "portal.app.signature/1-dev.1", "algorithm": "Ed25519", "publisher": "fixture-publisher", "key_id": "first-key", "descriptor_sha256": platformDigest(raw), "signature": base64.RawURLEncoding.EncodeToString(ed25519.Sign(private, frame))})
	if err != nil {
		t.Fatal(err)
	}
	return envelope
}
func testPlatformStagedRelease(t *testing.T, pg bool) {
	s, session := platformTrustTestServer(t, pg)
	platformRegisterTestPublisher(t, s, session)
	raw, sig, frontend := platformSignedReleaseFixture(t, "1.0.0-beta.1")
	staged, err := s.stagePlatformRelease(session, raw, sig, frontend)
	if err != nil {
		t.Fatal(err)
	}
	if staged.sha256 != platformDigest(raw) || staged.trustRevision != 1 || staged.keyRevision != 1 || staged.reused {
		t.Fatal("stage lost verified identity")
	}
	if scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_installations`) != 0 || scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_operations`) != 0 {
		t.Fatal("staging granted installation authority")
	}
	again, err := s.stagePlatformRelease(session, raw, sig, frontend)
	if err != nil || !again.reused {
		t.Fatalf("immutable replay: %v", err)
	}
	if scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_releases`) != 1 || scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_frontend_artifacts`) != 1 {
		t.Fatal("duplicate immutable bytes")
	}
	altered := append(append([]byte{}, raw...), byte('\n'))
	if _, err := s.stagePlatformRelease(session, altered, platformSignTestDescriptor(t, altered), frontend); !errors.Is(err, errAppPlatformReleaseConflict) {
		t.Fatalf("published version changed bytes: %v", err)
	}
	fresh := &Server{st: s.st, cfg: s.cfg}
	if _, err := fresh.recheckPlatformStagedRelease(session, staged.sha256, 1); err != nil {
		t.Fatal(err)
	}
	key := platformTrustFixtureKey(8, "next-key")
	if _, err := s.changePlatformPublisherTrust(session, platformPublisherChange{kind: "add_key", publisherID: "fixture-publisher", expectedRevision: 1, key: &key}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.recheckPlatformStagedRelease(session, staged.sha256, 1); !errors.Is(err, errAppPlatformReleaseConflict) {
		t.Fatalf("stale review survived trust revision: %v", err)
	}
	if current, err := s.recheckPlatformStagedRelease(session, staged.sha256, 2); err != nil || current.trustRevision != 2 {
		t.Fatalf("unchanged release at fresh trust revision: %v", err)
	}
	if _, err := s.changePlatformPublisherTrust(session, platformPublisherChange{kind: "revoke_key", publisherID: "fixture-publisher", expectedRevision: 2, keyID: "first-key", expectedKeyRevision: 1}); err != nil {
		t.Fatal(err)
	}
	before := dumpOf(t, s.st)
	if _, err := s.recheckPlatformStagedRelease(session, staged.sha256, 3); err == nil {
		t.Fatal("staged release survived key revocation")
	}
	if _, err := s.stagePlatformRelease(session, raw, sig, frontend); err == nil {
		t.Fatal("cached release bypassed current revocation")
	}
	if !bytes.Equal(stripHeader(before), stripHeader(dumpOf(t, s.st))) {
		t.Fatal("refused validation wrote state")
	}
}
func TestPlatformStagedReleaseSQLite(t *testing.T)   { testPlatformStagedRelease(t, false) }
func TestPlatformStagedReleasePostgres(t *testing.T) { testPlatformStagedRelease(t, true) }

func TestPostgresPlatformReleaseExpiryAfterAuditWait(t *testing.T) {
	for _, mode := range []string{"stage", "admission"} {
		t.Run(mode, func(t *testing.T) {
			s, session := platformTrustTestServer(t, true)
			priorAdmissionAudits := scalar[int](t, s.st, `SELECT COUNT(*) FROM audit_log WHERE action='fixture.release_admission'`)
			platformRegisterTestPublisher(t, s, session)
			raw, sig, frontend := platformSignedReleaseFixture(t, "1.0.0-beta.1")
			var staged platformStagedRelease
			if mode == "admission" {
				var err error
				staged, err = s.stagePlatformRelease(session, raw, sig, frontend)
				if err != nil {
					t.Fatal(err)
				}
			}
			now, err := platformDatabaseNow(s.st.poolExec())
			if err != nil {
				t.Fatal(err)
			}
			expiry := now + 500
			mustExec(t, s.st, `UPDATE app_platform_publisher_keys SET not_after_ms=? WHERE key_id='first-key'`, expiry)
			holder, err := s.st.db.Begin()
			if err != nil {
				t.Fatal(err)
			}
			defer holder.Rollback()
			if _, err := holder.Exec(`LOCK TABLE audit_log IN SHARE MODE`); err != nil {
				t.Fatal(err)
			}
			result := make(chan error, 1)
			go func() {
				if mode == "stage" {
					_, err := s.stagePlatformRelease(session, raw, sig, frontend)
					result <- err
					return
				}
				result <- s.withPlatformStagedRelease(session, staged.sha256, 1, func(e migExec, _ platformActorFacts, _ platformActorControl, _ platformStagedRelease) error {
					_, err := e.exec(`INSERT INTO audit_log(at,actor,actor_ou,action,target_type,target_id,detail,ip) VALUES('fixture','fixture',0,'fixture.release_admission','fixture','fixture','{}','')`)
					return err
				})
			}()
			blocked := false
			deadline := time.Now().Add(5 * time.Second)
			for time.Now().Before(deadline) {
				var n int
				if err := s.st.queryRow(`SELECT COUNT(*) FROM pg_stat_activity WHERE datname=current_database() AND pid<>pg_backend_pid() AND wait_event_type='Lock' AND query LIKE '%INSERT INTO audit_log%'`).Scan(&n); err != nil {
					t.Fatal(err)
				}
				if n > 0 {
					blocked = true
					break
				}
				time.Sleep(5 * time.Millisecond)
			}
			if !blocked {
				holder.Rollback()
				err := <-result
				t.Fatalf("release did not reach held audit lock: %v", err)
			}
			for {
				now, err = platformDatabaseNow(s.st.poolExec())
				if err != nil {
					t.Fatal(err)
				}
				if now >= expiry {
					break
				}
				time.Sleep(5 * time.Millisecond)
			}
			if err := holder.Commit(); err != nil {
				t.Fatal(err)
			}
			if err := <-result; !errors.Is(err, errAppPlatformReleaseConflict) {
				t.Fatalf("expired key accepted after wait: %v", err)
			}
			if scalar[int](t, s.st, `SELECT COUNT(*) FROM audit_log WHERE action='fixture.release_admission'`) != priorAdmissionAudits {
				t.Fatal("expired admission left intent committed")
			}
			if mode == "stage" && (scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_releases`) != 0 || scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_frontend_artifacts`) != 0) {
				t.Fatal("expired staging left archive or release")
			}
		})
	}
}

func testPlatformStoredReleaseRefusal(t *testing.T, pg bool) {
	for _, name := range []string{"disabled", "namespace", "expired", "descriptor", "signature", "version", "frontend", "managed"} {
		t.Run(name, func(t *testing.T) {
			s, session := platformTrustTestServer(t, pg)
			platformRegisterTestPublisher(t, s, session)
			raw, sig, frontend := platformSignedReleaseFixture(t, "1.0.0-beta.1")
			staged, err := s.stagePlatformRelease(session, raw, sig, frontend)
			if err != nil {
				t.Fatal(err)
			}
			expected := int64(1)
			switch name {
			case "disabled", "namespace":
				ns := "notes-"
				if name == "namespace" {
					ns = "other-"
				}
				if _, err := s.changePlatformPublisherTrust(session, platformPublisherChange{kind: "policy", publisherID: "fixture-publisher", expectedRevision: 1, enabled: name != "disabled", namespaces: []appplatform.PublisherNamespace{{Kind: "prefix", Value: ns}}}); err != nil {
					t.Fatal(err)
				}
				expected = 2
			case "expired":
				mustExec(t, s.st, `UPDATE app_platform_publisher_keys SET not_after_ms=1`)
			case "descriptor":
				mustExec(t, s.st, `UPDATE app_platform_releases SET descriptor=?`, append(bytes.Clone(raw), byte('\n')))
			case "signature":
				mustExec(t, s.st, `UPDATE app_platform_releases SET signature=?`, []byte("invalid"))
			case "version":
				mustExec(t, s.st, `UPDATE app_platform_releases SET version='9.9.9'`)
			case "frontend":
				mustExec(t, s.st, `UPDATE app_platform_frontend_artifacts SET content=?`, bytes.Repeat([]byte{1}, len(frontend)))
			case "managed":
				mustExec(t, s.st, `UPDATE app_platform_releases SET managed=1`)
			}
			called := false
			before := dumpOf(t, s.st)
			err = s.withPlatformStagedRelease(session, staged.sha256, expected, func(_ migExec, _ platformActorFacts, _ platformActorControl, _ platformStagedRelease) error {
				called = true
				return nil
			})
			if err == nil || called {
				t.Fatalf("invalid stored release reached admission: %v", err)
			}
			if !bytes.Equal(stripHeader(before), stripHeader(dumpOf(t, s.st))) {
				t.Fatal("refusal mutated release or trust")
			}
		})
	}
}
func TestPlatformStoredReleaseRefusalSQLite(t *testing.T) { testPlatformStoredReleaseRefusal(t, false) }
func TestPlatformStoredReleaseRefusalPostgres(t *testing.T) {
	testPlatformStoredReleaseRefusal(t, true)
}

func testPlatformSignedReleaseRestore(t *testing.T, pg bool) {
	s, session := platformTrustTestServer(t, pg)
	platformRegisterTestPublisher(t, s, session)
	raw, sig, archive := platformSignedReleaseFixture(t, "1.0.0-beta.1")
	staged, err := s.stagePlatformRelease(session, raw, sig, archive)
	if err != nil {
		t.Fatal(err)
	}
	older := dumpOf(t, s.st)
	if _, err := s.changePlatformPublisherTrust(session, platformPublisherChange{kind: "revoke_key", publisherID: "fixture-publisher", expectedRevision: 1, keyID: "first-key", expectedKeyRevision: 1}); err != nil {
		t.Fatal(err)
	}
	revoked := scalar[int64](t, s.st, `SELECT revoked_ms FROM app_platform_publisher_keys WHERE key_id='first-key'`)
	if _, err := s.st.restoreFrom(bytes.NewReader(older), true); err != nil {
		t.Fatal(err)
	}
	if scalar[int64](t, s.st, `SELECT revoked_ms FROM app_platform_publisher_keys WHERE key_id='first-key'`) != revoked {
		t.Fatal("restore resurrected real signing key")
	}
	if scalar[int](t, s.st, `SELECT enabled FROM app_platform_publishers WHERE id='fixture-publisher'`) != 0 || s.st.GetSetting("app_platform_restore_paused", "") != "1" {
		t.Fatal("restore enabled trust")
	}
	if scalar[string](t, s.st, `SELECT sha256 FROM app_platform_releases`) != staged.sha256 {
		t.Fatal("restore lost immutable release bytes")
	}
	if _, err := s.recheckPlatformStagedRelease(session, staged.sha256, 3); err == nil {
		t.Fatal("restored release admitted without recovery")
	}
}
func TestPlatformSignedReleaseRestoreSQLite(t *testing.T) { testPlatformSignedReleaseRestore(t, false) }
func TestPlatformSignedReleaseRestorePostgres(t *testing.T) {
	testPlatformSignedReleaseRestore(t, true)
}
