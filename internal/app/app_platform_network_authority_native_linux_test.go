//go:build linux && (amd64 || arm64)

package app

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/transport"
)

// Runs only after actual signed installation/boot/activation and a real normal
// viewer backend request. This proves current SQL signing/check authority, not
// an HTTPS execution or a browser network SDK call.
func testNativePlatformNetworkAuthority(t *testing.T, s *Server, actor platformActorSession, installation string, endpoint platformRuntimeEndpoint) {
	t.Helper()
	ctx := context.Background()
	binding := platformBackendRequestBinding{Generation: "1", PolicyRevision: "1", ReleaseSHA256: scalar[string](t, s.st, `SELECT release_sha256 FROM app_platform_activations WHERE installation_id=?`, installation)}
	i, err := s.preparePlatformNetworkRequest(ctx, actor, installation, binding, "GET", "https://api.example.com/v1", "application/json", "", nil)
	if err != nil {
		t.Fatal("actual current viewer network signing", err)
	}
	defer i.Close()
	a := i.access.intent.start.writer.authority
	verified, err := control.VerifyNetworkHTTPRequest(i.raw, i.signature, a, time.Now().UnixMilli())
	if err != nil || control.ValidateNetworkHTTPBody(verified, nil) != nil || i.access.viewer.Role != "user" || i.command.ActorScope == i.access.viewer.Subject || i.command.InstallationID != installation || bytes.Contains(i.raw, []byte(actor.username)) {
		t.Fatal("normal viewer was not independently bound to current network command", err)
	}
	challenge := strings.Repeat("c", 64)
	proof, sig, err := s.approvePlatformNetworkRequest(ctx, actor, installation, i, verified.Command(), challenge)
	if err != nil || control.VerifyNetworkHTTPCheck(verified.Command(), challenge, proof, sig, a, time.Now().UnixMilli()) != nil {
		t.Fatal("actual current SQL challenge proof", err)
	}
	wrong := verified.Command()
	wrong.RequestID = platformFixtureID(18888)
	if proof, sig, err := s.approvePlatformNetworkRequest(ctx, actor, installation, i, wrong, challenge); err == nil || len(proof) != 0 || len(sig) != 0 {
		t.Fatal("root challenge changed the pinned request")
	}
	t.Log("PORTAL_NETWORK_CURRENT_VIEWER_SIGNED")
	// Changing the current row after preparation must refuse the next actual SQL
	// authorization checkpoint; restoring this disposable row allows later tests.
	mustExec(t, s.st, `UPDATE app_platform_capability_grants SET granted=0 WHERE installation_id=? AND capability='network.http'`, installation)
	proof, sig, err = s.approvePlatformNetworkRequest(ctx, actor, installation, i, verified.Command(), strings.Repeat("d", 64))
	mustExec(t, s.st, `UPDATE app_platform_capability_grants SET granted=1 WHERE installation_id=? AND capability='network.http'`, installation)
	if err == nil || len(proof) != 0 || len(sig) != 0 {
		t.Fatal("prepared authority ignored current grant withdrawal")
	}
	if result, err := s.completePlatformNetworkRequest(ctx, actor, installation, i, transport.NetworkObservation{}, endpoint); !errors.Is(err, errAppPlatformNetworkUncertain) || len(result.Body) != 0 {
		t.Fatal("missing native result released response")
	}
	t.Log("PORTAL_NETWORK_CURRENT_GRANT_RECHECKED")
}
