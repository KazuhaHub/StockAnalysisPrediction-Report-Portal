package app

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/transport"
)

func testPlatformBackendBootRequiresAppliedDelivery(t *testing.T, pg bool) {
	s, lease := platformBackendDeliveryFixture(t, pg)
	intent, err := s.preparePlatformBackendDelivery(context.Background(), lease)
	if err != nil {
		t.Fatal(err)
	}
	defer intent.registration.Close()
	if validPlatformBackendHandshakeObservation(transport.BackendHandshakeObservation{}, intent, platformRuntimeEndpoint{}, "a.b") {
		t.Fatal("zero handshake proof accepted")
	}
	before := dumpOf(t, s.st)
	if _, err := s.probePlatformBackendBoot(context.Background(), lease, platformRuntimeEndpoint{}); err == nil {
		t.Fatal("prepared delivery became boot proof")
	}
	if string(stripHeader(before)) != string(stripHeader(dumpOf(t, s.st))) {
		t.Fatal("unapplied boot probe wrote state")
	}
	var proof platformBackendBootObservation
	if _, err := json.Marshal(proof); err == nil {
		t.Fatal("boot observation serialized")
	}
	if err := json.Unmarshal([]byte(`{}`), &proof); err == nil {
		t.Fatal("boot observation imported")
	}
}

func TestPlatformBackendBootRequiresAppliedDeliverySQLite(t *testing.T) {
	testPlatformBackendBootRequiresAppliedDelivery(t, false)
}
func TestPlatformBackendBootRequiresAppliedDeliveryPostgres(t *testing.T) {
	testPlatformBackendBootRequiresAppliedDelivery(t, true)
}
