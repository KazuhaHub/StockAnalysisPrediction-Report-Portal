package app

import (
	"testing"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

func legacyPlan() platformInstallProjection {
	return platformInstallProjection{Format: platformManagedInstallFormat, DataQuotaBytes: 256 << 20}
}

// A plan written before the quota fields existed was reviewed at 256 MiB and 65,536 inodes. One that is still
// unconsumed when the build is upgraded, or an install in flight across the deploy, must keep working.
func TestPreUpgradePlanStillVerifiesAtTheFixedQuota(t *testing.T) {
	manifest := quotaManifest(nil)
	if err := platformCheckInstallQuota(legacyPlan(), platformManagedInstallFormat, manifest); err != nil {
		t.Fatalf("a pre-upgrade plan was refused at stage: %v", err)
	}
	if b, i, err := platformPlanQuota(mustJSON(t, legacyPlan())); err != nil || b != 256<<20 || i != 65536 {
		t.Fatalf("prepare-data of a pre-upgrade plan reads %d %d %v", b, i, err)
	}
	// It cannot be used to carry anything else: a changed byte quota, or a descriptor that now asks for more.
	tampered := legacyPlan()
	tampered.DataQuotaBytes = 512 << 20
	if platformCheckInstallQuota(tampered, platformManagedInstallFormat, manifest) == nil {
		t.Fatal("a pre-upgrade plan with another byte quota verified")
	}
	if platformCheckInstallQuota(legacyPlan(), platformManagedInstallFormat, quotaManifest(&appplatform.DataQuota{Bytes: 1 << 30, Inodes: 65536})) == nil {
		t.Fatal("a pre-upgrade plan verified for a descriptor that requests more")
	}
	partial := legacyPlan()
	partial.RequestedDataQuotaBytes = 256 << 20
	if platformCheckInstallQuota(partial, platformManagedInstallFormat, manifest) == nil {
		t.Fatal("a half-written plan verified")
	}
}

// Consuming rebuilds the projection and compares bytes with the stored plan, so for a pre-upgrade plan the
// rebuild must come out in the pre-upgrade form.
func TestRebuiltProjectionOfAPreUpgradePlanMatchesIt(t *testing.T) {
	manifest := quotaManifest(nil)
	stored := legacyPlan()
	var rebuilt platformInstallProjection
	rebuilt.Format = stored.Format
	approval := platformApprovalOfStoredPlan(stored)
	if err := platformApplyInstallQuota(&rebuilt, rebuilt.Format, manifest, approval); err != nil {
		t.Fatal(err)
	}
	platformKeepPreUpgradeForm(stored, &rebuilt)
	if mustJSON(t, rebuilt) != mustJSON(t, stored) {
		t.Fatalf("rebuilt %s, stored %s", mustJSON(t, rebuilt), mustJSON(t, stored))
	}
	// A current plan is left alone.
	current := platformInstallProjection{Format: platformManagedInstallFormat}
	if err := platformApplyInstallQuota(&current, current.Format, manifest, platformQuotaApproval{}); err != nil {
		t.Fatal(err)
	}
	again := platformInstallProjection{Format: current.Format}
	if err := platformApplyInstallQuota(&again, again.Format, manifest, platformApprovalOfStoredPlan(current)); err != nil {
		t.Fatal(err)
	}
	platformKeepPreUpgradeForm(current, &again)
	if mustJSON(t, again) != mustJSON(t, current) || current.DataQuotaInodes != 65536 {
		t.Fatalf("a current plan changed: %s vs %s", mustJSON(t, again), mustJSON(t, current))
	}
}
