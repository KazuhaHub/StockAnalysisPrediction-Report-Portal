package app

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/transport"
)

func TestPlatformWritableRequestPayloadBoundary(t *testing.T) {
	// An invalid payload must be refused before even opening the database.
	s := &Server{}
	for _, c := range []struct{ method, path, body string }{
		{"GET", "/v1/echo", "{}"},
		{"PATCH", "/v1/echo", "{}"},
		{"POST", "/v1/handshake", "{}"},
		{"POST", "https://example.test/v1/echo", "{}"},
		{"POST", "/v1/echo", "not JSON"},
		{"POST", "/v1/echo", `"` + strings.Repeat("a", 65536) + `"`},
	} {
		r, err := s.requestPlatformWritableBackend(context.Background(), platformActorSession{}, platformFixtureID(11), c.method, c.path, []byte(c.body), platformRuntimeEndpoint{})
		if err == nil || len(r.Body) != 0 || errors.Is(err, errAppPlatformBackendRequestUncertain) {
			t.Fatal("invalid payload was dispatched or misreported as uncertain")
		}
	}
}

func TestPlatformWritableRequestSigningSQLite(t *testing.T) {
	s, actor, lease := platformWritableAccessFixture(t, false)
	var raw, sig []byte
	var assertion string
	var command control.WritableBackendRequestCommand
	body := []byte(`{"role":"admin","subject":"app-controlled"}`)
	a, err := s.withPlatformWritableBackendAccess(context.Background(), actor, lease.installationID, func(a platformWritableBackendAccess, seed []byte) error {
		var err error
		raw, sig, assertion, command, err = signPlatformWritableBackendRequest(a, seed, "POST", "/v1/echo", body, "fixture-boot")
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	v, err := control.VerifyWritableBackendRequest(raw, sig, a.intent.start.writer.authority, time.Now().UnixMilli())
	if err != nil || control.ValidateWritableBackendRequestData(v, body, assertion) != nil {
		t.Fatal("host request failed independent control validation", err)
	}
	if r, err := s.completePlatformWritableBackendRequest(context.Background(), actor, lease.installationID, a, transport.WritableBackendRequestObservation{}, platformRuntimeEndpoint{}, raw, sig, command); !errors.Is(err, errAppPlatformBackendRequestUncertain) || len(r.Body) != 0 {
		t.Fatal("missing native provenance released a response")
	}
	// A role or session/OU authority change fences response consumption even if
	// immutable registration/source identity survives the change.
	copy := a
	copy.policy++
	if samePlatformWritableBackendAccess(a, copy) {
		t.Fatal("policy drift not fenced")
	}
	copy = a
	copy.account.restricted++
	if samePlatformWritableBackendAccess(a, copy) {
		t.Fatal("account facts drift not fenced")
	}
}
