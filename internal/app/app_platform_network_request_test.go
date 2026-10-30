package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/transport"
)

func TestPlatformNetworkRequestPayloadBoundary(t *testing.T) {
	s := &Server{}
	binding := platformBackendRequestBinding{Generation: "1", PolicyRevision: "1", ReleaseSHA256: strings.Repeat("a", 64)}
	for _, tc := range []struct {
		method, target, accept string
		body                   []byte
	}{
		{method: "GET", target: "https://api.example.com/v1", body: bytes.Repeat([]byte("x"), 65537)},
		{method: strings.Repeat("X", 17), target: "https://api.example.com/v1"},
		{method: "GET", target: strings.Repeat("x", 4097)},
		{method: "GET", target: "https://api.example.com/v1", accept: "bad\r\nheader"},
	} {
		if _, err := s.preparePlatformNetworkRequest(context.Background(), platformActorSession{}, platformFixtureID(1), binding, tc.method, tc.target, tc.accept, "", tc.body); !errors.Is(err, errAppPlatformNetworkUnavailable) {
			t.Fatal("invalid payload reached authority lookup", err)
		}
	}
	if _, err := json.Marshal(platformNetworkRequestIntent{}); err == nil {
		t.Fatal("request intent serialized")
	}
	var intent platformNetworkRequestIntent
	if json.Unmarshal([]byte(`{}`), &intent) == nil {
		t.Fatal("intent reconstituted")
	}
	if _, err := s.completePlatformNetworkRequest(context.Background(), platformActorSession{}, platformFixtureID(1), intent, transport.NetworkObservation{}, platformRuntimeEndpoint{}); !errors.Is(err, errAppPlatformNetworkUncertain) {
		t.Fatal("zero observation released response", err)
	}
}
func testPlatformNetworkRequestRequiresGrant(t *testing.T, pg bool) {
	s, actor, lease := platformWritableAccessFixture(t, pg)
	binding := platformBackendRequestBinding{Generation: "1", PolicyRevision: "1", ReleaseSHA256: scalar[string](t, s.st, `SELECT release_sha256 FROM app_platform_activations WHERE installation_id=?`, lease.installationID)}
	before := dumpOf(t, s.st)
	intent, err := s.preparePlatformNetworkRequest(context.Background(), actor, lease.installationID, binding, "GET", "https://api.example.com/v1", "application/json", "", nil)
	if err == nil {
		intent.Close()
		t.Fatal("denied optional capability signed a request")
	}
	if result, err := s.requestPlatformNetwork(context.Background(), actor, lease.installationID, binding, "GET", "https://api.example.com/v1", "application/json", "", nil, platformRuntimeEndpoint{}); !errors.Is(err, errAppPlatformNetworkUnavailable) || len(result.Body) != 0 {
		t.Fatal("denied capability reached dispatch or released a response", err)
	}
	if !bytes.Equal(stripHeader(before), stripHeader(dumpOf(t, s.st))) {
		t.Fatal("refused request wrote authority")
	}
}
func TestPlatformNetworkRequestRequiresGrantSQLite(t *testing.T) {
	testPlatformNetworkRequestRequiresGrant(t, false)
}
func TestPlatformNetworkRequestRequiresGrantPostgres(t *testing.T) {
	testPlatformNetworkRequestRequiresGrant(t, true)
}
