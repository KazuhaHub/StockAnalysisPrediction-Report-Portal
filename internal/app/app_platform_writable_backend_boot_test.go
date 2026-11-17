package app

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/transport"
)

func testPlatformWritableBootRequiresAppliedDelivery(t *testing.T, pg bool) {
	s, lease := platformWritableRegistrationFixture(t, pg)
	i, err := s.preparePlatformWritableRegistration(context.Background(), lease)
	if err != nil {
		t.Fatal(err)
	}
	defer i.registration.Close()
	before := dumpOf(t, s.st)
	if validPlatformWritableHandshakeObservation(transport.WritableBackendHandshakeObservation{}, i, platformRuntimeEndpoint{}, "a.b") {
		t.Fatal("zero handshake trusted")
	}
	if _, err := s.probePlatformWritableBackendBoot(context.Background(), lease, platformRuntimeEndpoint{}); err == nil {
		t.Fatal("undelivered identity became boot proof")
	}
	if _, err := s.finishPlatformWritableBackendBoot(context.Background(), lease, i, platformRuntimeEndpoint{}, "a.b", transport.WritableBackendHandshakeObservation{}); err == nil {
		t.Fatal("zero proof became boot identity")
	}
	if !bytes.Equal(stripHeader(before), stripHeader(dumpOf(t, s.st))) {
		t.Fatal("failed boot changed control storage")
	}
	var proof platformWritableBootObservation
	if _, err := json.Marshal(proof); err == nil {
		t.Fatal("boot observation serialized")
	}
	if err := json.Unmarshal([]byte(`{}`), &proof); err == nil {
		t.Fatal("boot observation imported")
	}
}
func TestPlatformWritableBootRequiresAppliedDeliverySQLite(t *testing.T) {
	testPlatformWritableBootRequiresAppliedDelivery(t, false)
}
func TestPlatformWritableBootRequiresAppliedDeliveryPostgres(t *testing.T) {
	testPlatformWritableBootRequiresAppliedDelivery(t, true)
}
