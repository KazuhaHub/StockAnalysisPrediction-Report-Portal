package app

import (
	"context"
	"os"
	"testing"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/transport"
)

func TestManagedRuntimeProfileDoesNotCreateAuthority(t *testing.T) {
	raw, err := os.ReadFile("../appplatform/testdata/descriptors/managed.json")
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := appplatform.ParseDescriptor(raw)
	if err != nil {
		t.Fatal(err)
	}
	e := platformRuntimeEndpoint{path: "/run/portal/runner.sock", controllerUID: 41001, controllerGID: 41001}
	if validPlatformManagedRuntimeObservation(transport.ManagedRuntimeObservation{}, platformRuntimeRegistration{}, e) {
		t.Fatal("zero proof authorized runtime")
	}
	if got, err := platformManagedRuntimeCompatibility(parsed.Manifest, platformManagedRuntimeFacts{}, e); err == nil || got.Compatible {
		t.Fatal("caller metadata became compatibility evidence")
	}
	s, session, _ := platformInstallFixture(t, false)
	before := scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_installations`)
	if _, err := s.observePlatformManagedRuntime(context.Background(), session, e); err == nil {
		t.Fatal("missing runtime fabricated profile")
	}
	if scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_installations`) != before || scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_operation_effects`)+scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_access_grants`) != 0 {
		t.Fatal("profile probe granted/installed app")
	}
}
