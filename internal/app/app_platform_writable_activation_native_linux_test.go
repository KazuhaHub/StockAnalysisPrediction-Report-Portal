//go:build linux && (amd64 || arm64)

package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"

	protocol "github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal-Apps/packages/extensionprotocol"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/driver"
)

func testNativeWritableActivationAndAccess(t *testing.T, s *Server, lease platformOperationLease, endpoint platformRuntimeEndpoint, original platformWritableRegistrationIntent) {
	t.Helper()
	ctx := context.Background()
	actor := platformManagedFixtureSession(t, s)
	if r, err := s.requestPlatformWritableBackend(ctx, actor, lease.installationID, "POST", "/v1/echo", []byte(`{}`), endpoint); err == nil || len(r.Body) != 0 || errors.Is(err, errAppPlatformBackendRequestUncertain) {
		t.Fatal("pending writable app admitted a viewer")
	}
	mustExec(t, s.st, `CREATE TRIGGER writable_activation_audit_failure BEFORE INSERT ON audit_log WHEN NEW.action='app_platform.writable_install_activated' BEGIN SELECT RAISE(ABORT,'writable activation fixture'); END`)
	if failed, err := s.runPlatformWritableInstall(ctx, lease, endpoint); err == nil || failed.lease != lease {
		t.Fatal("worker lost activation rollback progress", err)
	}
	if scalar[string](t, s.st, `SELECT phase FROM app_platform_installations WHERE id=?`, lease.installationID) != "pending" || scalar[string](t, s.st, `SELECT state FROM app_platform_activations WHERE installation_id=?`, lease.installationID) != "candidate" || scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_access_grants WHERE installation_id=?`, lease.installationID) != 0 || scalar[string](t, s.st, `SELECT stage FROM app_platform_operations WHERE id=?`, lease.id) != "activate" {
		t.Fatal("failed activation partially exposed app")
	}
	mustExec(t, s.st, `DROP TRIGGER writable_activation_audit_failure`)
	finished, err := s.runPlatformWritableInstall(ctx, lease, endpoint)
	activated := finished.activation
	if err != nil {
		t.Fatal("actual writable activation", err)
	}
	if finished.lease != (platformOperationLease{}) || activated.activationID != original.start.writer.resourceID || activated.snapshot.Stage != appplatform.StageComplete || scalar[string](t, s.st, `SELECT phase FROM app_platform_installations WHERE id=?`, lease.installationID) != "ready" || scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_access_grants WHERE installation_id=?`, lease.installationID) != 1 {
		t.Fatal("activation replaced resource or failed reviewed audience")
	}
	if _, err := s.st.heartbeatPlatformOperation(lease); err == nil {
		t.Fatal("completed activation regained worker lease")
	}
	if _, err := s.activatePlatformWritableInstall(ctx, lease, endpoint); err == nil {
		t.Fatal("stale activation lease reused")
	}
	active, err := s.preparePlatformWritableBackendAccess(ctx, actor, lease.installationID)
	if err != nil {
		t.Fatal("actual completed source", err)
	}
	defer active.Close()
	if active.intent.start.running.NativeID != original.start.running.NativeID || active.intent.receipt != original.receipt || active.intent.start.writer.receipt != original.start.writer.receipt {
		t.Fatal("activation replaced writer/process/file identity")
	}
	if scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_operation_effects WHERE operation_id=?`, lease.id) != 4 || scalar[int](t, s.st, `SELECT COUNT(*) FROM audit_log WHERE action='app_platform.writable_install_activated' AND target_id=?`, lease.installationID) != 1 {
		t.Fatal("activation duplicated effects/audit")
	}
	t.Log("PORTAL_WRITABLE_INSTALL_ACTIVATED")
	t.Log("PORTAL_WRITABLE_WORKER_ACTIVATED")
	// Return the shared disposable account to its original authority before the
	// independent readonly/other fixture paths continue; no production account used.
	group := scalar[int64](t, s.st, `SELECT COALESCE(group_id,0) FROM users WHERE username=?`, actor.username)
	role := scalar[string](t, s.st, `SELECT role FROM users WHERE username=?`, actor.username)
	defer func() {
		mustExec(t, s.st, `UPDATE users SET role=?,group_id=NULLIF(?,0) WHERE username=?`, role, group, actor.username)
	}()
	mustExec(t, s.st, `UPDATE users SET role='user' WHERE username=?`, actor.username)
	decode := func(r driver.BackendResponse) (int64, protocol.Viewer) {
		t.Helper()
		var v struct {
			Count  int64           `json:"count"`
			Viewer protocol.Viewer `json:"viewer"`
		}
		if r.Status != 200 || json.Unmarshal(r.Body, &v) != nil || v.Viewer.Role != "user" || len(v.Viewer.Subject) != 64 || len(v.Viewer.OUs) < 1 {
			t.Fatal("writable backend did not authenticate current viewer")
		}
		return v.Count, v.Viewer
	}
	response, err := s.requestPlatformWritableBackend(ctx, actor, lease.installationID, "POST", "/v1/echo", []byte(`{"role":"admin","subject":"forged"}`), endpoint)
	if err != nil {
		t.Fatal("actual writable viewer request", err)
	}
	count, viewer := decode(response)
	if count != 3 {
		t.Fatal("viewer request omitted or repeated write")
	}
	mustExec(t, s.st, `UPDATE app_platform_installations SET policy_revision=2 WHERE id=?`, lease.installationID)
	mustExec(t, s.st, `UPDATE app_platform_activations SET policy_revision=2 WHERE installation_id=?`, lease.installationID)
	mustExec(t, s.st, `UPDATE app_platform_capability_grants SET policy_revision=2 WHERE installation_id=?`, lease.installationID)
	parent, err := s.st.CreateUserGroup("Writable audience parent", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	child, err := s.st.CreateUserGroup("Writable audience child", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, s.st, `UPDATE user_groups SET parent_id=? WHERE id=?`, parent, child)
	if _, err := s.st.SetPrimaryGroups([]string{actor.username}, child); err != nil {
		t.Fatal(err)
	}
	created := scalar[string](t, s.st, `SELECT created_at FROM user_groups WHERE id=?`, parent)
	mustExec(t, s.st, `DELETE FROM app_platform_access_grants WHERE installation_id=?`, lease.installationID)
	grant, err := platformNewUUID()
	if err != nil {
		t.Fatal(err)
	}
	platformTestInsert(t, s.st.poolExec(), "access_grants", map[string]any{"id": grant, "installation_id": lease.installationID, "principal_type": "ou", "username": nil, "account_created": nil, "account_generation": nil, "group_id": parent, "group_created": created, "revision": 1, "created_ms": 1, "updated_ms": 1})
	response, err = s.requestPlatformWritableBackend(ctx, actor, lease.installationID, "GET", "/v1/echo", nil, endpoint)
	if err != nil {
		t.Fatal("actual ancestor OU grant", err)
	}
	count, member := decode(response)
	if count != 3 || member.Subject != viewer.Subject || len(member.OUs) < 2 {
		t.Fatal("OU/policy change rewrote immutable identity or executed write")
	}
	mustExec(t, s.st, `UPDATE user_groups SET parent_id=NULL WHERE id=?`, child)
	if r, err := s.requestPlatformWritableBackend(ctx, actor, lease.installationID, "POST", "/v1/echo", []byte(`{}`), endpoint); err == nil || len(r.Body) != 0 || errors.Is(err, errAppPlatformBackendRequestUncertain) {
		t.Fatal("reparented OU dispatched a new request")
	}
	mustExec(t, s.st, `UPDATE user_groups SET parent_id=? WHERE id=?`, parent, child)
	t.Log("PORTAL_WRITABLE_CURRENT_OU_REQUEST_VERIFIED")
	// Collect an actual write result, then remove membership before releasing it.
	body := []byte(`{"value":"post-io-ou-revocation"}`)
	var raw, sig []byte
	var assertion string
	var command control.WritableBackendRequestCommand
	boot, err := s.requestWritableFixtureBoot(ctx, actor, lease.installationID, endpoint)
	if err != nil {
		t.Fatal(err)
	}
	access, err := s.withPlatformWritableBackendAccess(ctx, actor, lease.installationID, func(a platformWritableBackendAccess, seed []byte) error {
		var err error
		raw, sig, assertion, command, err = signPlatformWritableBackendRequest(a, seed, "POST", "/v1/echo", body, boot)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	defer access.Close()
	proof, err := platformRequestWritableBackend(ctx, endpoint, access.intent, raw, sig, body, assertion)
	if err != nil || proof.Result().Response.Status != 200 {
		t.Fatal("actual write before OU revocation", err)
	}
	if _, err := s.st.SetPrimaryGroups([]string{actor.username}, 0); err != nil {
		t.Fatal(err)
	}
	withheld, err := s.completePlatformWritableBackendRequest(ctx, actor, lease.installationID, access, proof, endpoint, raw, sig, command)
	if !errors.Is(err, errAppPlatformBackendRequestUncertain) || len(withheld.Body) != 0 {
		t.Fatal("revoked OU received buffered write response")
	}
	if r, err := s.requestPlatformWritableBackend(ctx, actor, lease.installationID, "POST", "/v1/echo", body, endpoint); err == nil || len(r.Body) != 0 || errors.Is(err, errAppPlatformBackendRequestUncertain) {
		t.Fatal("revoked member dispatched another write")
	}
	if _, err := s.st.SetPrimaryGroups([]string{actor.username}, child); err != nil {
		t.Fatal(err)
	}
	response, err = s.requestPlatformWritableBackend(ctx, actor, lease.installationID, "GET", "/v1/echo", nil, endpoint)
	if err != nil {
		t.Fatal(err)
	}
	count, _ = decode(response)
	if count != 4 {
		t.Fatal("uncertain or denied write retried")
	}
	if binding, err := s.inspectPlatformLaunchAccess(actor, lease.installationID); err != nil || binding.installationID != lease.installationID || binding.policyRevision != "2" || binding.releaseSHA != platformDigest(original.start.writer.descriptor) {
		t.Fatal("current writable viewer launch binding", err)
	}
	// A read-only request must not change the existing completed control state.
	before := dumpOf(t, s.st)
	current, err := s.preparePlatformWritableBackendAccess(ctx, actor, lease.installationID)
	if err != nil {
		t.Fatal(err)
	}
	current.Close()
	if !bytes.Equal(stripHeader(before), stripHeader(dumpOf(t, s.st))) {
		t.Fatal("viewer admission changed completed history")
	}
	t.Log("PORTAL_WRITABLE_POST_IO_OU_REVOKED")
}
