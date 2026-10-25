package app

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"
	"testing"
)

func platformTestInsert(t *testing.T, e migExec, table string, values map[string]any) {
	t.Helper()
	var fields []string
	for name := range values {
		fields = append(fields, name)
	}
	sort.Strings(fields)
	args := make([]any, len(fields))
	marks := make([]string, len(fields))
	for i, name := range fields {
		args[i] = values[name]
		marks[i] = "?"
	}
	if _, err := e.exec("INSERT INTO "+platformPrefix+table+"("+strings.Join(fields, ",")+") VALUES("+strings.Join(marks, ",")+")", args...); err != nil {
		t.Fatal(err)
	}
}

func platformSeedPendingControl(t *testing.T, st *Store) {
	t.Helper()
	e := st.poolExec()
	hash := strings.Repeat("a", 64)
	platformSeedPublisher(t, e, "fixture-publisher")
	if err := platformSeedInstallation(t, e, platformFixtureID(1), "fixture-publisher", "fixture-app"); err != nil {
		t.Fatal(err)
	}
	platformTestInsert(t, e, "review_plans", map[string]any{
		"id": platformFixtureID(2), "installation_id": platformFixtureID(1), "app_id": "fixture-app", "publisher_id": "fixture-publisher", "action": "disable",
		"source_release_sha256": nil, "target_release_sha256": nil, "installation_revision": 1, "policy_revision": 1, "trust_revision": 1, "controller_epoch": platformFixtureID(99),
		"actor_scope": hash, "username": nil, "account_created": "", "account_generation": 0, "authority_fingerprint": hash, "plan_sha256": hash, "plan_json": "{}", "expires_ms": 10000, "consumed_ms": 1, "created_ms": 1, "updated_ms": 1,
	})
	platformTestInsert(t, e, "operations", map[string]any{
		"id": platformFixtureID(3), "installation_id": platformFixtureID(1), "review_plan_id": platformFixtureID(2), "actor_scope": hash, "idempotency_sha256": hash, "request_sha256": hash,
		"action": "disable", "managed": 0, "source_generation": 1, "target_generation": 2, "stage": "queued", "state_revision": 1, "row_revision": 1,
		"lease_owner": platformFixtureID(4), "lease_until_ms": 20000, "fence": 1, "controller_epoch": platformFixtureID(99), "error_code": nil, "completed_ms": nil, "created_ms": 1, "updated_ms": 1,
	})
	mustExec(t, st, `UPDATE app_platform_installations SET control_fence=1`)
	platformTestInsert(t, e, "runtime_bindings", map[string]any{
		"id": platformFixtureID(5), "deployment_id": platformFixtureID(6), "controller_epoch": platformFixtureID(99), "control_revision": 1, "enabled": 1,
		"verification_key_fingerprint": hash, "sealed_control_key": "fixture-envelope", "revision": 1, "created_ms": 1, "updated_ms": 1,
	})
}

func testPlatformRestorePausesAuthority(t *testing.T, postgres bool) {
	st := platformTestStore(t, postgres)
	platformSeedPendingControl(t, st)
	dump := dumpOf(t, st)
	before := dumpOf(t, st)
	if _, err := st.restoreFrom(bytes.NewReader(dump), false); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(stripHeader(before), stripHeader(dumpOf(t, st))) {
		t.Fatal("dry restore wrote state")
	}
	if _, err := st.restoreFrom(bytes.NewReader(dump), true); err != nil {
		t.Fatal(err)
	}
	if got := scalar[string](t, st, `SELECT phase FROM app_platform_installations`); got != "review_required" {
		t.Fatalf("restored installation not paused: %s", got)
	}
	if scalar[int](t, st, `SELECT enabled FROM app_platform_runtime_bindings`) != 0 {
		t.Fatal("runtime auto-enabled")
	}
	if scalar[int](t, st, `SELECT COUNT(*) FROM app_platform_runtime_bindings WHERE sealed_control_key IS NOT NULL`) != 0 {
		t.Fatal("restored control credential retained")
	}
	if scalar[string](t, st, `SELECT stage FROM app_platform_operations`) != "recovery_required" {
		t.Fatal("pending operation could be replayed")
	}
	if scalar[int](t, st, `SELECT COUNT(*) FROM app_platform_operations WHERE lease_owner IS NOT NULL OR completed_ms IS NULL`) != 0 {
		t.Fatal("restored lease survived")
	}
	if scalar[string](t, st, `SELECT controller_epoch FROM app_platform_installations`) == platformFixtureID(99) {
		t.Fatal("old controller epoch survived")
	}
	if scalar[int](t, st, `SELECT generation FROM app_platform_installations`) != 2 {
		t.Fatal("generation not fenced")
	}
}
func TestAppPlatformRestorePausesAuthority(t *testing.T) {
	testPlatformRestorePausesAuthority(t, false)
}
func TestPostgresAppPlatformRestorePausesAuthority(t *testing.T) {
	testPlatformRestorePausesAuthority(t, true)
}

func platformSeedKey(t *testing.T, st *Store, public []byte) {
	t.Helper()
	platformTestInsert(t, st.poolExec(), "publisher_keys", map[string]any{
		"publisher_id": "fixture-publisher", "key_id": "fixture-key", "algorithm": "ed25519", "public_key": public, "not_before_ms": 0, "not_after_ms": nil,
		"revoked_ms": nil, "revision": 1, "created_ms": 1, "updated_ms": 1,
	})
}
func testPlatformRestoreRetainsRevocations(t *testing.T, postgres bool) {
	st := platformTestStore(t, postgres)
	platformSeedPublisher(t, st.poolExec(), "fixture-publisher")
	platformSeedKey(t, st, bytes.Repeat([]byte{1}, 32))
	older := dumpOf(t, st)
	mustExec(t, st, `UPDATE app_platform_publisher_keys SET revoked_ms=100,revision=2`)
	if _, err := st.restoreFrom(bytes.NewReader(older), true); err != nil {
		t.Fatal(err)
	}
	if scalar[int64](t, st, `SELECT revoked_ms FROM app_platform_publisher_keys`) != 100 {
		t.Fatal("older backup cleared current key revocation")
	}
	if scalar[int](t, st, `SELECT enabled FROM app_platform_publishers`) != 0 {
		t.Fatal("trust enabled on restore")
	}
	conflicting := newTestStore(t)
	platformSeedPublisher(t, conflicting.poolExec(), "fixture-publisher")
	platformSeedKey(t, conflicting, bytes.Repeat([]byte{2}, 32))
	before := dumpOf(t, st)
	if _, err := st.restoreFrom(bytes.NewReader(dumpOf(t, conflicting)), true); err == nil {
		t.Fatal("key identity replacement silently erased revocation")
	}
	if !bytes.Equal(stripHeader(before), stripHeader(dumpOf(t, st))) {
		t.Fatal("refused restore changed its target")
	}
}
func TestAppPlatformRestoreRetainsRevocations(t *testing.T) {
	testPlatformRestoreRetainsRevocations(t, false)
}
func TestPostgresAppPlatformRestoreRetainsRevocations(t *testing.T) {
	testPlatformRestoreRetainsRevocations(t, true)
}

func testPlatformRestoreRejectsCorruptArtifact(t *testing.T, postgres bool) {
	st := platformTestStore(t, postgres)
	platformSeedPendingControl(t, st)
	content := []byte("fixture public artifact")
	digest := sha256.Sum256(content)
	platformTestInsert(t, st.poolExec(), "frontend_artifacts", map[string]any{"sha256": hex.EncodeToString(digest[:]), "archive_bytes": len(content), "content": content, "created_ms": 1})
	mustExec(t, st, `UPDATE app_platform_frontend_artifacts SET content=?`, bytes.Repeat([]byte{'x'}, len(content)))
	bad := dumpOf(t, st)
	before := dumpOf(t, st)
	if _, err := st.restoreFrom(bytes.NewReader(bad), true); err == nil {
		t.Fatal("corrupt artifact accepted")
	}
	if !bytes.Equal(stripHeader(before), stripHeader(dumpOf(t, st))) {
		t.Fatal("corrupt restore mutated target")
	}
}
func TestAppPlatformRestoreRejectsCorruptArtifact(t *testing.T) {
	testPlatformRestoreRejectsCorruptArtifact(t, false)
}
func TestPostgresAppPlatformRestoreRejectsCorruptArtifact(t *testing.T) {
	testPlatformRestoreRejectsCorruptArtifact(t, true)
}
