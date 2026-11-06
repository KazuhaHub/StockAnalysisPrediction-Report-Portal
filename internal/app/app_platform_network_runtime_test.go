package app

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/transport"
	"testing"
)

func testPlatformNetworkRuntimeNoAuthority(t *testing.T, pg bool) {
	s, actor := platformActorTestServer(t, pg)
	if err := s.initializeAppPlatformControl(actor); err != nil {
		t.Fatal(err)
	}
	endpoint := platformRuntimeEndpoint{path: "/run/portal/missing.sock", controllerUID: 41001, controllerGID: 41001}
	if validPlatformNetworkRuntimeObservation(transport.NetworkRuntimeObservation{}, platformRuntimeRegistration{}, endpoint) {
		t.Fatal("zero observation became authority")
	}
	if _, err := json.Marshal(platformNetworkRuntimeFacts{}); err == nil {
		t.Fatal("runtime facts became a wire proof")
	}
	before := dumpOf(t, s.st)
	if _, err := s.observePlatformNetworkRuntime(context.Background(), actor, endpoint); err == nil {
		t.Fatal("missing current runtime fabricated network support")
	}
	if !bytes.Equal(stripHeader(before), stripHeader(dumpOf(t, s.st))) {
		t.Fatal("failed probe created persistent authority")
	}
	mustExec(t, s.st, `UPDATE users SET role='user' WHERE username=?`, actor.username)
	if _, err := s.observePlatformNetworkRuntime(context.Background(), actor, endpoint); err == nil {
		t.Fatal("demoted administrator sampled runtime")
	}
}
func TestPlatformNetworkRuntimeNoAuthoritySQLite(t *testing.T) {
	testPlatformNetworkRuntimeNoAuthority(t, false)
}
func TestPlatformNetworkRuntimeNoAuthorityPostgres(t *testing.T) {
	testPlatformNetworkRuntimeNoAuthority(t, true)
}
