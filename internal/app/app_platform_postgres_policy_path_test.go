package app

import (
	"context"
	"testing"
)

// A plan is consumed only by the installation it was made for.
func TestPlatformPostgresConsumeRefusesAnotherInstallationPath(t *testing.T) {
	s, admin, installation, _, target := platformPostgresFixture(t, false, false)
	plan, err := s.createPlatformPostgresPolicyReview(context.Background(), admin, platformRuntimeEndpoint{}, installation, platformPostgresRequestFor(t, s, installation, target))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.consumePlatformPostgresPolicyReview(context.Background(), admin, platformRuntimeEndpoint{}, platformFixtureID(9901), plan.ID, plan.SHA256); err == nil {
		t.Fatal("a plan was consumed by another installation")
	}
	if scalar[int](t, s.st, `SELECT granted FROM app_platform_capability_grants WHERE installation_id=? AND capability='network.postgres'`, installation) != 0 {
		t.Fatal("the refused consume granted")
	}
	if _, err := s.consumePlatformPostgresPolicyReview(context.Background(), admin, platformRuntimeEndpoint{}, installation, plan.ID, plan.SHA256); err != nil {
		t.Fatal(err)
	}
}
