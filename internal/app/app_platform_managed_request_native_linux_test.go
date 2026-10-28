//go:build linux && (amd64 || arm64)

package app

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	protocol "github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal-Apps/packages/extensionprotocol"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/driver"
)

// Actual native execution/history and live Portal sessions exercise the private
// request path. Only the final Portal complete/active rows and explicit audience
// grant are seeded here: this is not a normal managed activation implementation.
func testNativeManagedViewerRequest(t *testing.T, s *Server, lease platformOperationLease, endpoint platformRuntimeEndpoint, boot platformBackendBootObservation) {
	t.Helper()
	ctx := context.Background()
	actor := platformManagedFixtureSession(t, s)
	if r, err := s.requestPlatformManagedBackend(ctx, actor, lease.installationID, "POST", "/v1/echo", []byte(`{}`), endpoint); err == nil || len(r.Body) != 0 || errors.Is(err, errAppPlatformBackendRequestUncertain) {
		t.Fatal("pending install entered managed viewer request")
	}
	now, err := platformDatabaseNow(s.st.poolExec())
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, s.st, `UPDATE app_platform_operations SET stage='complete',state_revision=state_revision+1,row_revision=row_revision+1,completed_ms=?,updated_ms=?,lease_owner=NULL,lease_until_ms=NULL WHERE id=?`, now, now, lease.id)
	mustExec(t, s.st, `UPDATE app_platform_activations SET state='active' WHERE installation_id=?`, lease.installationID)
	mustExec(t, s.st, `UPDATE app_platform_installations SET phase='ready' WHERE id=?`, lease.installationID)
	platformGrantLaunchUser(t, s, actor, lease.installationID)
	mustExec(t, s.st, `UPDATE users SET role='user' WHERE username=?`, actor.username)
	decode := func(r driver.BackendResponse) (int64, protocol.Viewer) {
		t.Helper()
		var v struct {
			Count  int64           `json:"count"`
			Viewer protocol.Viewer `json:"viewer"`
		}
		if r.Status != 200 || json.Unmarshal(r.Body, &v) != nil || v.Viewer.Role != "user" || len(v.Viewer.Subject) != 64 || len(v.Viewer.OUs) < 1 {
			t.Fatal("backend did not authenticate the current Portal viewer")
		}
		return v.Count, v.Viewer
	}
	response, err := s.requestPlatformManagedBackend(ctx, actor, lease.installationID, "POST", "/v1/echo", []byte(`{"role":"admin","subject":"forged"}`), endpoint)
	if err != nil {
		t.Fatal("current viewer request over actual root transport", err)
	}
	count, viewer := decode(response)
	if count != 2 {
		t.Fatal("request was repeated or did not execute exactly once")
	}
	mustExec(t, s.st, `UPDATE app_platform_installations SET policy_revision=2 WHERE id=?`, lease.installationID)
	mustExec(t, s.st, `UPDATE app_platform_activations SET policy_revision=2 WHERE installation_id=?`, lease.installationID)
	mustExec(t, s.st, `UPDATE app_platform_capability_grants SET policy_revision=2 WHERE installation_id=?`, lease.installationID)
	response, err = s.requestPlatformManagedBackend(ctx, actor, lease.installationID, "GET", "/v1/echo", nil, endpoint)
	if err != nil {
		t.Fatal("audience-only change broke the actual protected registration", err)
	}
	count, again := decode(response)
	if count != 2 || again.Subject != viewer.Subject {
		t.Fatal("policy edit rotated user identity or repeated a write")
	}
	// Exercise the exact production response-completion boundary after an actual
	// write, then revoke the audience before consuming its buffered native proof.
	body := []byte(`{"value":"post-io-revocation"}`)
	var raw, sig []byte
	var assertion string
	var command control.BackendRequestCommand
	access, err := s.withPlatformManagedBackendAccess(ctx, actor, lease.installationID, func(a platformManagedBackendAccess, seed []byte) error {
		var err error
		raw, sig, assertion, command, err = signPlatformManagedBackendRequest(a, seed, "POST", "/v1/echo", body, boot.bootID)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	defer access.Close()
	proof, err := platformRequestBackend(ctx, endpoint, access.intent, raw, sig, body, assertion)
	if err != nil || proof.Result().Response.Status != 200 {
		t.Fatal("actual write before revocation", err)
	}
	mustExec(t, s.st, `DELETE FROM app_platform_access_grants WHERE installation_id=?`, lease.installationID)
	withheld, err := s.completePlatformManagedBackendRequest(ctx, actor, lease.installationID, access, proof, endpoint, raw, sig, command)
	if !errors.Is(err, errAppPlatformBackendRequestUncertain) || len(withheld.Body) != 0 {
		t.Fatal("revoked audience received buffered response or false rollback claim")
	}
	if r, err := s.requestPlatformManagedBackend(ctx, actor, lease.installationID, "POST", "/v1/echo", body, endpoint); err == nil || len(r.Body) != 0 || errors.Is(err, errAppPlatformBackendRequestUncertain) {
		t.Fatal("revoked viewer dispatched a new write")
	}
	platformGrantLaunchUser(t, s, actor, lease.installationID)
	response, err = s.requestPlatformManagedBackend(ctx, actor, lease.installationID, "GET", "/v1/echo", nil, endpoint)
	if err != nil {
		t.Fatal(err)
	}
	count, _ = decode(response)
	if count != 3 {
		t.Fatal("uncertain or refused write was automatically retried")
	}
	if _, err := s.inspectPlatformLaunchAccess(actor, lease.installationID); err == nil {
		t.Fatal("native request fixture enabled public managed launch")
	}
	t.Log("MANAGED_CURRENT_VIEWER_REQUEST_VERIFIED")
}
