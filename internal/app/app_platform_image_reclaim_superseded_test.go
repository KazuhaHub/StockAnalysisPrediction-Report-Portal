package app

import (
	"context"
	"testing"
)

// reclaimSet adds a recovery set of the fixture's installation for the named release.
func (f *reclaimFixture) reclaimSet(t *testing.T, n int, name, state string, created int64) {
	t.Helper()
	// After the fixture's own set, which was cut at wall-clock time.
	created += 9_000_000_000_000
	namespace := scalar[string](t, f.s.st, `SELECT data_namespace_id FROM app_platform_activations WHERE installation_id=? AND state='active'`, f.installation)
	var verified any = created
	if state != "verified" && state != "restoring" && state != "review_required" {
		verified = nil
	}
	var manifest any = platformDigest([]byte(name + state))
	if state == "preparing" {
		manifest = nil
	}
	platformTestInsert(t, f.s.st.poolExec(), "recovery_sets", map[string]any{
		"id": platformFixtureID(n), "installation_id": f.installation, "data_namespace_id": namespace, "operation_id": f.hostOp,
		"release_sha256": f.shas[name], "data_format": 1, "checkpoint_id": platformFixtureID(n + 1), "manifest_sha256": manifest,
		"bytes": 1, "state": state, "controller_epoch": f.epoch, "writer_fence": 1, "verified_ms": verified, "deleted_ms": nil, "created_ms": created, "updated_ms": created,
	})
}

// Fails if every retained recovery set keeps its release's image on the runner. The 2026-11-27 market-monitor update
// failed at load_image because the image store was full of superseded releases that older recovery sets pinned. Only
// the newest retained set of an installation keeps its image loaded; an older set is still restorable because the
// restore loads its release again from the Portal's own blobs, which stay pinned by the bundle's rule.
func TestReclaimFreesTheImagesOfOlderRecoverySetsAndKeepsTheNewest(t *testing.T) {
	t.Parallel()
	f := newReclaimFixture(t)
	for _, name := range []string{"active", "oldest", "older", "newest"} {
		f.release(t, name)
		f.load(t, name)
	}
	f.setActive(t, "active")
	f.reclaimSet(t, 8920, "oldest", "verified", 10)
	f.reclaimSet(t, 8922, "older", "verified", 20)
	f.reclaimSet(t, 8924, "newest", "verified", 30)

	got := f.targets(t)
	for _, freed := range []string{"oldest", "older"} {
		if !got[f.config(freed)] {
			t.Fatalf("the image of the %s recovery set's release is still kept on the runner: %v", freed, got)
		}
	}
	for _, kept := range []string{"active", "newest"} {
		if got[f.config(kept)] {
			t.Fatalf("the image of the %s release would be removed", kept)
		}
	}

	// The Portal's blobs (and the recovery bundle) still keep every retained set's release: that is what lets a
	// restore of an older set load its image again.
	pins, err := loadPlatformReleasePins(f.s.st.poolExec(), 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"oldest", "older", "newest"} {
		if !pins[f.shas[name]]["pinned"] {
			t.Fatalf("the %s recovery set's release is no longer pinned for the bundle and the blob store", name)
		}
	}
}

// Fails if a set that is not settled loses its image: one being cut, being restored or held for review belongs to
// work that is not finished, whatever its age.
func TestReclaimKeepsTheImagesOfUnsettledRecoverySets(t *testing.T) {
	t.Parallel()
	f := newReclaimFixture(t)
	for _, name := range []string{"active", "restoring", "review", "preparing", "newest"} {
		f.release(t, name)
		f.load(t, name)
	}
	f.setActive(t, "active")
	f.reclaimSet(t, 8930, "restoring", "restoring", 10)
	f.reclaimSet(t, 8932, "review", "review_required", 20)
	f.reclaimSet(t, 8934, "preparing", "preparing", 25)
	f.reclaimSet(t, 8936, "newest", "verified", 30)

	got := f.targets(t)
	for _, kept := range []string{"active", "restoring", "review", "preparing", "newest"} {
		if got[f.config(kept)] {
			t.Fatalf("the image of the %s release would be removed", kept)
		}
	}
}

// Fails if an open restore plan of an older set does not keep its image until the restore has run: the plan pin
// still applies to the runner.
func TestReclaimKeepsTheImageOfAnOlderSetAnOpenPlanRestores(t *testing.T) {
	t.Parallel()
	f := newReclaimFixture(t)
	for _, name := range []string{"active", "older", "newest"} {
		f.release(t, name)
		f.load(t, name)
	}
	f.setActive(t, "active")
	f.reclaimSet(t, 8940, "older", "verified", 10)
	f.reclaimSet(t, 8942, "newest", "verified", 20)
	if !f.targets(t)[f.config("older")] {
		t.Fatal("the older set's image is kept without any plan")
	}
	plan := scalar[string](t, f.s.st, `SELECT review_plan_id FROM app_platform_operations WHERE id=?`, f.hostOp)
	mustExec(t, f.s.st, `UPDATE app_platform_review_plans SET consumed_ms=NULL,expires_ms=9999999999999,target_release_sha256=? WHERE id=?`, f.shas["older"], plan)
	targets, _, err := f.s.platformReclaimTargets(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range targets {
		if target.intent.ConfigDigest == f.config("older") {
			t.Fatal("the image a still-open restore plan names would be removed")
		}
	}
}
