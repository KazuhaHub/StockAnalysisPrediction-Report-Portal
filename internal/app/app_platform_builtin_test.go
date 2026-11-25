package app

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"net/http"
	"strings"
	"testing"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

var builtinTestNamespaces = []appplatform.PublisherNamespace{{Kind: "prefix", Value: "kazuha-hub-"}}

func builtinTestPublisher(keys ...appplatform.PublisherKey) platformBuiltinPublisher {
	b := platformBuiltinPublisher{id: "kazuha-hub", displayName: "Kazuha Hub", namespaces: builtinTestNamespaces}
	for _, k := range keys {
		b.keys = append(b.keys, platformBuiltinKey{id: k.ID, publicKey: k.PublicKey})
	}
	return b
}

func builtinTrust(t *testing.T, s *Server) appplatform.PublisherTrust {
	t.Helper()
	tx, err := s.st.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	trust, err := loadPlatformPublisherTrust(migExec{s: s.st, ex: platformContextTx{Tx: tx, ctx: context.Background()}})
	if err != nil {
		t.Fatal(err)
	}
	return trust
}

func ptr[T any](v T) *T { return &v }

func builtinRow(trust appplatform.PublisherTrust) *appplatform.TrustedPublisher {
	for i := range trust.Publishers {
		if trust.Publishers[i].ID == "kazuha-hub" {
			return &trust.Publishers[i]
		}
	}
	return nil
}

func builtinAudits(t *testing.T, s *Server) int {
	return scalar[int](t, s.st, `SELECT COUNT(*) FROM audit_log WHERE actor='system' AND action='app_platform.publisher_trust_changed' AND target_id='kazuha-hub'`)
}

func builtinEnsure(t *testing.T, s *Server) {
	t.Helper()
	if err := s.ensurePlatformBuiltinTrust(context.Background()); err != nil {
		t.Fatal(err)
	}
}

// Fails if a fresh deployment's initialization does not trust the built-in publisher, or does so unaudited.
func TestBuiltinPublisherIsSeededByInitialization(t *testing.T) {
	t.Parallel()
	eachPlatformDriver(t, testBuiltinPublisherIsSeededByInitialization)
}

func testBuiltinPublisherIsSeededByInitialization(t *testing.T, pg bool) {
	s, actor := platformActorTestServer(t, pg)
	key := platformTrustFixtureKey(9, "kazuha-hub-2026-11")
	s.platformBuiltinPublishers = []platformBuiltinPublisher{builtinTestPublisher(key)}
	if err := s.initializeAppPlatformControl(actor); err != nil {
		t.Fatal(err)
	}
	row := builtinRow(builtinTrust(t, s))
	if row == nil || !row.Enabled || row.Revision != "1" || len(row.Keys) != 1 || row.Keys[0].PublicKey != key.PublicKey || row.Keys[0].NotAfterMS != nil || row.Namespaces[0] != builtinTestNamespaces[0] {
		t.Fatalf("built-in publisher not seeded: %+v", row)
	}
	if builtinAudits(t, s) != 1 {
		t.Fatal("seeding was not audited once")
	}
}

// Fails if the startup step for an initialized deployment does not seed, is not idempotent, re-enables a
// publisher an administrator disabled, or does anything before initialization.
func TestBuiltinPublisherEnsureAtStartup(t *testing.T) {
	t.Parallel()
	eachPlatformDriver(t, testBuiltinPublisherEnsureAtStartup)
}

func testBuiltinPublisherEnsureAtStartup(t *testing.T, pg bool) {
	uninitialized, _ := platformActorTestServer(t, pg)
	key := platformTrustFixtureKey(9, "kazuha-hub-2026-11")
	uninitialized.platformBuiltinPublishers = []platformBuiltinPublisher{builtinTestPublisher(key)}
	builtinEnsure(t, uninitialized)
	if builtinRow(builtinTrust(t, uninitialized)) != nil {
		t.Fatal("seeded before initialization: that would block initialization as history")
	}

	s, actor := platformTrustTestServer(t, pg)
	s.platformBuiltinPublishers = []platformBuiltinPublisher{builtinTestPublisher(key)}
	builtinEnsure(t, s)
	builtinEnsure(t, s)
	row := builtinRow(builtinTrust(t, s))
	if row == nil || row.Revision != "1" || builtinAudits(t, s) != 1 {
		t.Fatalf("ensure not idempotent: %+v audits %d", row, builtinAudits(t, s))
	}
	if _, err := s.changePlatformPublisherTrust(actor, platformPublisherChange{kind: "policy", publisherID: "kazuha-hub", expectedRevision: 1, enabled: false, namespaces: builtinTestNamespaces}); err != nil {
		t.Fatal("administrator could not disable the built-in publisher", err)
	}
	builtinEnsure(t, s)
	if row := builtinRow(builtinTrust(t, s)); row.Enabled || row.Revision != "2" {
		t.Fatalf("ensure rewrote the administrator's disable: %+v", row)
	}
}

// Fails if a rotation released in Portal does not reach an existing deployment.
func TestBuiltinPublisherEnsureAddsARotatedKey(t *testing.T) {
	t.Parallel()
	s, _ := platformTrustTestServer(t, false)
	first, second := platformTrustFixtureKey(9, "kazuha-hub-2026-11"), platformTrustFixtureKey(10, "kazuha-hub-2027-05")
	s.platformBuiltinPublishers = []platformBuiltinPublisher{builtinTestPublisher(first)}
	builtinEnsure(t, s)
	s.platformBuiltinPublishers = []platformBuiltinPublisher{builtinTestPublisher(first, second)}
	builtinEnsure(t, s)
	row := builtinRow(builtinTrust(t, s))
	if row == nil || len(row.Keys) != 2 || row.Revision != "2" || builtinAudits(t, s) != 2 {
		t.Fatalf("rotated key not added: %+v", row)
	}
}

// Fails if an identical hand-registered publisher is not taken as the built-in one, or a conflicting one
// is overwritten instead of reported.
func TestBuiltinPublisherMigratesIdenticalAndReportsConflicts(t *testing.T) {
	t.Parallel()
	key := platformTrustFixtureKey(9, "kazuha-hub-2026-11")
	t.Run("identical", func(t *testing.T) {
		s, actor := platformTrustTestServer(t, false)
		if _, err := s.changePlatformPublisherTrust(actor, platformPublisherChange{kind: "register", publisherID: "kazuha-hub", enabled: true, namespaces: builtinTestNamespaces, key: &key}); err != nil {
			t.Fatal(err)
		}
		s.platformBuiltinPublishers = []platformBuiltinPublisher{builtinTestPublisher(key)}
		builtinEnsure(t, s)
		if builtinAudits(t, s) != 0 || builtinRow(builtinTrust(t, s)).Revision != "1" {
			t.Fatal("an identical publisher was rewritten")
		}
		directory := builtinDirectory(t, s, actor)
		if !strings.Contains(directory, `"builtin":true`) || !strings.Contains(directory, `"display_name":"Kazuha Hub"`) || strings.Contains(directory, `"builtin_conflict"`) {
			t.Fatal(directory)
		}
	})
	for name, register := range map[string]platformPublisherChange{
		"other key":             {kind: "register", publisherID: "kazuha-hub", enabled: true, namespaces: builtinTestNamespaces, key: ptr(platformTrustFixtureKey(11, "kazuha-hub-2026-11"))},
		"other namespace":       {kind: "register", publisherID: "kazuha-hub", enabled: true, namespaces: []appplatform.PublisherNamespace{{Kind: "exact", Value: "market-monitor"}}, key: &key},
		"key under another ID":  {kind: "register", publisherID: "kazuha-hub", enabled: true, namespaces: builtinTestNamespaces, key: ptr(platformTrustFixtureKey(11, "own-key"))},
		"overlapping publisher": {kind: "register", publisherID: "someone", enabled: true, namespaces: []appplatform.PublisherNamespace{{Kind: "prefix", Value: "kazuha-"}}, key: &key},
	} {
		t.Run(name, func(t *testing.T) {
			s, actor := platformTrustTestServer(t, false)
			if _, err := s.changePlatformPublisherTrust(actor, register); err != nil {
				t.Fatal(err)
			}
			before := builtinTrust(t, s)
			s.platformBuiltinPublishers = []platformBuiltinPublisher{builtinTestPublisher(key)}
			builtinEnsure(t, s)
			after := builtinTrust(t, s)
			if len(after.Publishers) != len(before.Publishers) || builtinAudits(t, s) != 0 {
				t.Fatal("a conflicting publisher was rewritten or the built-in seeded beside it")
			}
			directory := builtinDirectory(t, s, actor)
			if !strings.Contains(directory, `"builtin_conflict":true`) || strings.Contains(directory, `"builtin":true`) {
				t.Fatal(directory)
			}
		})
	}
}

// Fails if an administrator can take over, extend or re-scope the built-in publisher.
func TestBuiltinPublisherRefusesAdministratorTakeover(t *testing.T) {
	t.Parallel()
	key := platformTrustFixtureKey(9, "kazuha-hub-2026-11")
	s, actor := platformTrustTestServer(t, false)
	s.platformBuiltinPublishers = []platformBuiltinPublisher{builtinTestPublisher(key)}
	builtinEnsure(t, s)
	other := platformTrustFixtureKey(12, "admin-key")
	for name, change := range map[string]platformPublisherChange{
		"add key":           {kind: "add_key", publisherID: "kazuha-hub", expectedRevision: 1, key: &other},
		"change namespaces": {kind: "policy", publisherID: "kazuha-hub", expectedRevision: 1, enabled: true, namespaces: []appplatform.PublisherNamespace{{Kind: "prefix", Value: "anything-"}}},
		"overlap":           {kind: "register", publisherID: "shadow", enabled: true, namespaces: []appplatform.PublisherNamespace{{Kind: "exact", Value: "kazuha-hub-market-monitor"}}, key: &other},
	} {
		if _, err := s.changePlatformPublisherTrust(actor, change); err == nil {
			t.Fatal(name, "accepted")
		}
	}
	// The ID stays reserved even when the built-in row could not be seeded.
	blocked, blockedActor := platformTrustTestServer(t, false)
	blocked.platformBuiltinPublishers = []platformBuiltinPublisher{builtinTestPublisher(key)}
	if _, err := blocked.changePlatformPublisherTrust(blockedActor, platformPublisherChange{kind: "register", publisherID: "kazuha-hub", enabled: true, namespaces: builtinTestNamespaces, key: &other}); err == nil {
		t.Fatal("the built-in ID was registered with another key")
	}
}

func builtinDirectory(t *testing.T, s *Server, actor platformActorSession) string {
	t.Helper()
	mux := http.NewServeMux()
	s.wireRoutes(mux)
	cookie := s.managedCookie(*s.st.GetUser(actor.username), actor.sessionID, actor.expires.Unix())
	got := sessionTestRequest(mux, "GET", "/api/admin/app-platform/publishers", cookie, "")
	if got.Code != 200 {
		t.Fatal(got.Code, got.Body.String())
	}
	return got.Body.String()
}

// Fails if the official catalogue is not offered, cannot be disabled and re-enabled, or changes unaudited.
func TestOfficialCatalogueSourceCanBeDisabled(t *testing.T) {
	t.Parallel()
	s, actor := platformTrustTestServer(t, false)
	s.platformOfficialCatalogue = "https://raw.githubusercontent.com/KazuhaHub/StockAnalysisPrediction-Report-Portal-Apps/catalogue/catalogue.json"
	mux := http.NewServeMux()
	s.wireRoutes(mux)
	cookie := s.managedCookie(*s.st.GetUser(actor.username), actor.sessionID, actor.expires.Unix())
	read := func() string {
		got := sessionTestRequest(mux, "GET", "/api/admin/app-platform/market/sources", cookie, "")
		if got.Code != 200 {
			t.Fatal(got.Code, got.Body.String())
		}
		return got.Body.String()
	}
	if body := read(); !strings.Contains(body, `"url":"`+s.platformOfficialCatalogue+`"`) || !strings.Contains(body, `"enabled":true`) || !strings.Contains(body, `"name":"Kazuha Hub"`) {
		t.Fatal(body)
	}
	for _, enabled := range []string{"false", "true"} {
		got := sessionTestRequest(mux, "POST", "/api/admin/app-platform/market/sources", cookie, `{"official_enabled":`+enabled+`}`)
		if got.Code != 200 || !strings.Contains(read(), `"enabled":`+enabled) {
			t.Fatal(enabled, got.Code, got.Body.String())
		}
	}
	if scalar[int](t, s.st, `SELECT COUNT(*) FROM audit_log WHERE action='app_platform.market_source_changed' AND actor=?`, actor.username) != 2 {
		t.Fatal("source changes not audited")
	}
	for _, bad := range []string{`{}`, `{"official_enabled":null}`, `{"official_enabled":"no"}`, `{"official_enabled":true,"x":1}`} {
		if got := sessionTestRequest(mux, "POST", "/api/admin/app-platform/market/sources", cookie, bad); got.Code != 400 {
			t.Fatal(bad, got.Code)
		}
	}
}

// Fails if the production initialize endpoint does not seed the built-in publisher.
func TestBuiltinPublisherIsSeededByTheInitializeEndpoint(t *testing.T) {
	t.Parallel()
	s, actor := platformActorTestServer(t, false)
	s.platformBuiltinPublishers = []platformBuiltinPublisher{builtinTestPublisher(platformTrustFixtureKey(9, "kazuha-hub-2026-11"))}
	mux := http.NewServeMux()
	s.wireRoutes(mux)
	cookie := s.managedCookie(*s.st.GetUser(actor.username), actor.sessionID, actor.expires.Unix())
	if got := sessionTestRequest(mux, "POST", "/api/admin/app-platform/control/initialize", cookie, `{"action":"initialize"}`); got.Code != 200 {
		t.Fatal(got.Code, got.Body.String())
	}
	if row := builtinRow(builtinTrust(t, s)); row == nil || !row.Enabled {
		t.Fatal("initialize endpoint did not seed the built-in publisher")
	}
}

// Fails if an administrator cannot resolve a conflict through the console: a hand-registered row with its own
// key is handed over by revoking that key, a row with another scope can be disabled, and an overlapping
// publisher that is rescoped lets the built-in one in without a restart.
func TestBuiltinPublisherConflictsCanBeResolvedByAnAdministrator(t *testing.T) {
	t.Parallel()
	key := platformTrustFixtureKey(9, "kazuha-hub-2026-11")
	t.Run("own key revoked", func(t *testing.T) {
		s, actor := platformTrustTestServer(t, false)
		own := platformTrustFixtureKey(11, "own-key")
		if _, err := s.changePlatformPublisherTrust(actor, platformPublisherChange{kind: "register", publisherID: "kazuha-hub", enabled: true, namespaces: builtinTestNamespaces, key: &own}); err != nil {
			t.Fatal(err)
		}
		s.platformBuiltinPublishers = []platformBuiltinPublisher{builtinTestPublisher(key)}
		builtinEnsure(t, s)
		if _, err := s.changePlatformPublisherTrust(actor, platformPublisherChange{kind: "revoke_key", publisherID: "kazuha-hub", expectedRevision: 1, keyID: "own-key", expectedKeyRevision: 1}); err != nil {
			t.Fatal(err)
		}
		row := builtinRow(builtinTrust(t, s))
		if len(row.Keys) != 2 || !strings.Contains(builtinDirectory(t, s, actor), `"builtin":true`) {
			t.Fatalf("revoking the own key did not hand the row over: %+v", row)
		}
	})
	t.Run("other scope disabled", func(t *testing.T) {
		s, actor := platformTrustTestServer(t, false)
		scope := []appplatform.PublisherNamespace{{Kind: "exact", Value: "market-monitor"}}
		if _, err := s.changePlatformPublisherTrust(actor, platformPublisherChange{kind: "register", publisherID: "kazuha-hub", enabled: true, namespaces: scope, key: &key}); err != nil {
			t.Fatal(err)
		}
		s.platformBuiltinPublishers = []platformBuiltinPublisher{builtinTestPublisher(key)}
		if _, err := s.changePlatformPublisherTrust(actor, platformPublisherChange{kind: "policy", publisherID: "kazuha-hub", expectedRevision: 1, enabled: false, namespaces: scope}); err != nil {
			t.Fatal("a conflicting row could not be disabled", err)
		}
	})
	t.Run("overlap rescoped", func(t *testing.T) {
		s, actor := platformTrustTestServer(t, false)
		if _, err := s.changePlatformPublisherTrust(actor, platformPublisherChange{kind: "register", publisherID: "someone", enabled: true, namespaces: []appplatform.PublisherNamespace{{Kind: "prefix", Value: "kazuha-"}}, key: ptr(platformTrustFixtureKey(11, "k"))}); err != nil {
			t.Fatal(err)
		}
		s.platformBuiltinPublishers = []platformBuiltinPublisher{builtinTestPublisher(key)}
		builtinEnsure(t, s)
		if builtinRow(builtinTrust(t, s)) != nil {
			t.Fatal("seeded beside an overlap")
		}
		if _, err := s.changePlatformPublisherTrust(actor, platformPublisherChange{kind: "policy", publisherID: "someone", expectedRevision: 1, enabled: true, namespaces: []appplatform.PublisherNamespace{{Kind: "prefix", Value: "notes-"}}}); err != nil {
			t.Fatal(err)
		}
		if builtinRow(builtinTrust(t, s)) == nil {
			t.Fatal("the built-in publisher waited for a restart")
		}
	})
}

// Fails if the shipped built-in key is not the 32-byte Ed25519 key the Apps release environment signs with.
func TestOfficialPublisherKeyIsTheReleasedOne(t *testing.T) {
	t.Parallel()
	k := platformOfficialPublishers[0].keys[0]
	raw, err := base64.RawURLEncoding.Strict().DecodeString(k.publicKey)
	sum := sha256.Sum256(raw)
	if err != nil || len(raw) != 32 || k.id != "kazuha-hub-2026-11" || hex.EncodeToString(sum[:]) != "a9072bdb6b2911ebd09af406792abda71282a9eaa1e6ab94dfa5b8af721d0860" {
		t.Fatal("built-in key is not the released kazuha-hub-2026-11 key")
	}
}
