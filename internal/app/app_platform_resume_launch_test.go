package app

import (
	"testing"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

// resumedLaunchFixture is a restored installation, resumed to ready when resume is set. The fixture
// actor already holds the launch grant.
func resumedLaunchFixture(t *testing.T, pg, resume bool) (*platformResumeFixtureState, platformActorSession) {
	t.Helper()
	f := platformResumeFixture(t, pg)
	// A bundle is cut from a ready Portal, whose retained capability rows sit at the installation's
	// policy revision. This fixture's source is a Portal caught mid-update, whose installation policy
	// has moved on while its rows have not, so align them the way a ready Portal's would be.
	mustExec(t, f.s.st, `UPDATE app_platform_capability_grants SET policy_revision=(SELECT policy_revision FROM app_platform_installations WHERE id=?) WHERE installation_id=?`, f.installation, f.installation)
	if resume {
		commit := f.reviewAndConsumeResume(t)
		startLease := f.driveResume(t, commit.OperationID, appplatform.StageStart)
		if done := platformResumeStartToReady(t, f.s, startLease, f.endpoint); done.snapshot.Stage != appplatform.StageComplete {
			t.Fatal("fixture: the resume did not complete")
		}
	}
	return f, f.actor
}

func platformResumeLaunchManifest(t *testing.T, f *platformResumeFixtureState) (appplatform.Manifest, string, int64, string) {
	t.Helper()
	release := scalar[string](t, f.s.st, `SELECT release_sha256 FROM app_platform_recovery_sets WHERE id=?`, f.recoverySet)
	var descriptor []byte
	if err := f.s.st.queryRow(`SELECT descriptor FROM app_platform_releases WHERE sha256=?`, release).Scan(&descriptor); err != nil {
		t.Fatal(err)
	}
	parsed, err := appplatform.ParseDescriptor(descriptor)
	if err != nil {
		t.Fatal(err)
	}
	policy := scalar[int64](t, f.s.st, `SELECT policy_revision FROM app_platform_installations WHERE id=?`, f.installation)
	return parsed.Manifest, release, policy, f.s.st.GetSetting("app_platform_controller_epoch", "")
}

// The capability snapshot a launch is checked against is the one a completed lifecycle operation of
// the current controller epoch consumed. A restore rotates the epoch, so the install-time snapshot
// belongs to an epoch that no longer exists; a resume is the operation that re-establishes it. A
// restored installation that has not been resumed has no snapshot in the current epoch and must not
// launch, and the old epoch's operations must not stand in for it.
func testResumedInstallationLaunchSnapshot(t *testing.T, pg bool) {
	f, _ := resumedLaunchFixture(t, pg, true)
	manifest, release, policy, epoch := platformResumeLaunchManifest(t, f)
	if err := verifyPlatformLaunchCapabilitySnapshot(f.s.st.poolExec(), f.installation, release, epoch, policy, manifest); err != nil {
		t.Fatalf("a resumed installation has no launch capability snapshot: %v", err)
	}
	// The snapshot is bound to the epoch, the release and the resumed generation.
	if err := verifyPlatformLaunchCapabilitySnapshot(f.s.st.poolExec(), f.installation, release, platformFixtureID(9999), policy, manifest); err == nil {
		t.Fatal("a foreign epoch satisfied the snapshot")
	}
	if err := verifyPlatformLaunchCapabilitySnapshot(f.s.st.poolExec(), f.installation, platformFixtureID(9998)+platformFixtureID(9997)[:32], epoch, policy, manifest); err == nil {
		t.Fatal("a foreign release satisfied the snapshot")
	}
	mustExec(t, f.s.st, `UPDATE app_platform_installations SET generation=generation+1 WHERE id=?`, f.installation)
	if err := verifyPlatformLaunchCapabilitySnapshot(f.s.st.poolExec(), f.installation, release, epoch, policy, manifest); err == nil {
		t.Fatal("a resume of an earlier generation satisfied the snapshot")
	}
}

func TestResumedInstallationLaunchSnapshotSQLite(t *testing.T) {
	testResumedInstallationLaunchSnapshot(t, false)
}
func TestResumedInstallationLaunchSnapshotPostgres(t *testing.T) {
	testResumedInstallationLaunchSnapshot(t, true)
}

func testRestoredInstallationWithoutResumeHasNoSnapshot(t *testing.T, pg bool) {
	f, _ := resumedLaunchFixture(t, pg, false)
	manifest, release, policy, epoch := platformResumeLaunchManifest(t, f)
	if err := verifyPlatformLaunchCapabilitySnapshot(f.s.st.poolExec(), f.installation, release, epoch, policy, manifest); err == nil {
		t.Fatal("a restored installation that was not resumed has a launch capability snapshot")
	}
	// Nor does a resume that did not complete.
	f.reviewAndConsumeResume(t)
	if err := verifyPlatformLaunchCapabilitySnapshot(f.s.st.poolExec(), f.installation, release, epoch, policy, manifest); err == nil {
		t.Fatal("a resume in flight has a launch capability snapshot")
	}
}

func TestRestoredInstallationWithoutResumeHasNoSnapshotSQLite(t *testing.T) {
	testRestoredInstallationWithoutResumeHasNoSnapshot(t, false)
}
func TestRestoredInstallationWithoutResumeHasNoSnapshotPostgres(t *testing.T) {
	testRestoredInstallationWithoutResumeHasNoSnapshot(t, true)
}
