package app

import (
	"context"
	"errors"
	"testing"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/transport"
)

// platformReplacementRootSeams makes the runner report the given data root for whichever runtime the
// test has just enrolled.
func platformReplacementRootSeams(s *Server, registered *platformRuntimeRegistration, filesystem string) {
	s.platformObserveRuntime = func(context.Context, platformRuntimeEndpoint) (platformRuntimeObservation, error) {
		return runtimeObservationFixture(*registered), nil
	}
	s.platformRecoveryTarget = func(context.Context, platformActorSession, platformRuntimeEndpoint) (*platformManagedInstallTarget, error) {
		r := *registered
		return &platformManagedInstallTarget{
			RuntimeID: r.id, DeploymentID: r.deploymentID, ControllerEpoch: r.controllerEpoch, BindingRevision: 2,
			ControlRevision: r.controlRevision, Fingerprint: r.fingerprint,
			Profile: transport.ManagedRuntimeProfile{RootFilesystemUUID: filesystem, RootInode: "555"},
		}, nil
	}
}

// One journal binds one data root. The root a replacement runtime reports when it is confirmed is
// recorded, so abandoning that runtime remembers its root even when no import was ever applied on it:
// an import in flight, or lost, may still have landed there.
func TestAbandonRecordsTheReplacementRootWithoutAnAppliedImport(t *testing.T) {
	f, _ := unknownImportFixture(t)
	root := platformFixtureID(77) + "/555"
	registered := f.replacement
	pending := f.replacement
	pending.revision = 1
	platformReplacementRootSeams(f.s, &registered, platformFixtureID(77))
	if _, err := f.s.confirmPlatformReplacementRuntime(context.Background(), f.actor, pending, platformUpdateStartEndpointFixture()); err != nil {
		t.Fatal("confirming the replacement:", err)
	}
	plan, digest, err := f.s.previewPlatformAbandonRuntime(context.Background(), f.actor, f.replacement.id)
	if err != nil || len(plan.DataRoots) != 1 || plan.DataRoots[0] != root {
		t.Fatalf("the listing does not carry the replacement's recorded root: %+v %v", plan, err)
	}
	if _, _, err := f.s.confirmPlatformAbandonRuntime(context.Background(), f.actor, f.replacement.id, digest); err != nil {
		t.Fatal(err)
	}
	roots, err := loadPlatformAbandonedRoots(f.s.st.poolExec())
	if err != nil || len(roots) != 1 || roots[0].RuntimeID != f.replacement.id || roots[0].Filesystem != platformFixtureID(77) || roots[0].RootInode != "555" {
		t.Fatalf("the abandon did not record the root of an unknown import: %+v %v", roots, err)
	}
}

// A replacement whose runner reports an abandoned root is refused when it is confirmed, and its import
// is not reviewed either; a replacement on another root is accepted.
func TestReplacementOnAnAbandonedRootIsRefusedAtConfirm(t *testing.T) {
	f, _ := unknownImportFixture(t)
	registered := f.replacement
	pending := f.replacement
	pending.revision = 1
	platformReplacementRootSeams(f.s, &registered, platformFixtureID(77))
	if _, err := f.s.confirmPlatformReplacementRuntime(context.Background(), f.actor, pending, platformUpdateStartEndpointFixture()); err != nil {
		t.Fatal(err)
	}
	if _, err := f.abandon(t); err != nil {
		t.Fatal(err)
	}
	enroll := func(deployment string) platformRuntimeRegistration {
		reg, err := f.s.preparePlatformReplacementRuntime(f.actor, deployment)
		if err != nil {
			t.Fatal(err)
		}
		registered = reg
		return reg
	}
	reg := enroll(platformFixtureID(5102))
	if _, err := f.s.confirmPlatformReplacementRuntime(context.Background(), f.actor, reg, platformUpdateStartEndpointFixture()); !errors.Is(err, errAppPlatformRuntimeConflict) {
		t.Fatalf("a replacement on an abandoned root was confirmed: %v", err)
	}
	if _, err := f.s.createPlatformRestoreImportReview(context.Background(), f.actor, f.installation, f.recoverySet); err == nil {
		t.Fatal("an import was reviewed for a runtime on an abandoned root")
	}
	// The refused runtime is itself abandoned, and another host on a different root is accepted.
	if _, err := f.abandonRuntime(reg.id); err != nil {
		t.Fatal(err)
	}
	platformReplacementRootSeams(f.s, &registered, platformFixtureID(78))
	reg = enroll(platformFixtureID(5103))
	if _, err := f.s.confirmPlatformReplacementRuntime(context.Background(), f.actor, reg, platformUpdateStartEndpointFixture()); err != nil {
		t.Fatal("a replacement on a different root was refused:", err)
	}
}
