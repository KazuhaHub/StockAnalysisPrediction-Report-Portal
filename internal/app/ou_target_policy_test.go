package app

import (
	"bytes"
	"testing"
)

func TestOUTargetPolicyEmptyOverrideAndExplicitInheritance(t *testing.T) {
	s, org, team, target, _ := ouFixture(t)
	if err := s.st.SetGroupTargets(org, []GroupTarget{{TargetID: target}}); err != nil {
		t.Fatal(err)
	}
	if !s.runAllowed("ext", target, SurfaceRun) {
		t.Fatal("unset child did not inherit")
	}
	if err := s.st.SetGroupTargets(team, []GroupTarget{}); err != nil {
		t.Fatal(err)
	}
	if s.runAllowed("ext", target, SurfaceRun) {
		t.Fatal("explicit empty child inherited the parent's grant")
	}
	if err := s.st.InheritGroupTargets(team); err != nil {
		t.Fatal(err)
	}
	if !s.runAllowed("ext", target, SurfaceRun) {
		t.Fatal("explicit inherit did not restore the parent")
	}
}

func TestOUTargetPolicyDeletionPreservesLegacyEmptyOverride(t *testing.T) {
	s, org, team, target, other := ouFixture(t)
	if err := s.st.SetGroupTargets(org, []GroupTarget{{TargetID: other}}); err != nil {
		t.Fatal(err)
	}
	// An upgraded list has entries but no presence header. Deleting its last target must not
	// silently turn its replacement semantics into a grant from the parent.
	if _, err := s.st.exec(`INSERT INTO group_targets(group_id,target_id,surfaces) VALUES(?,?,'')`, team, target); err != nil {
		t.Fatal(err)
	}
	if err := s.st.DeleteTarget(target); err != nil {
		t.Fatal(err)
	}
	if got := s.st.GroupTargets(team); len(got) != 0 {
		t.Fatalf("stale grant rows: %+v", got)
	}
	if s.runAllowed("ext", other, SurfaceRun) {
		t.Fatal("deleting the last legacy target widened access")
	}
	if got := scalar[int](t, s.st, `SELECT COUNT(*) FROM group_target_policies WHERE group_id=?`, team); got != 1 {
		t.Fatal("configured empty state was lost")
	}
}

func TestOUTargetPolicyRejectedSaveIsAtomic(t *testing.T) {
	s, _, team, target, _ := ouFixture(t)
	if err := s.st.SetGroupTargets(team, []GroupTarget{{TargetID: target, Surfaces: SurfaceRun}}); err != nil {
		t.Fatal(err)
	}
	for _, rows := range [][]GroupTarget{
		{{TargetID: 999999}}, {{TargetID: target, Surfaces: "unknown"}}, {{TargetID: target}, {TargetID: target}},
	} {
		if err := s.st.SetGroupTargets(team, rows); err == nil {
			t.Fatalf("accepted invalid targets: %+v", rows)
		}
		got := s.st.GroupTargets(team)
		if len(got) != 1 || got[0].TargetID != target || got[0].Surfaces != SurfaceRun {
			t.Fatalf("rejected save changed grants: %+v", got)
		}
	}
	if err := s.st.SetGroupTargets(999999, nil); err == nil {
		t.Fatal("policy for missing OU accepted")
	}
}

func TestOUTargetPolicyOlderDumpKeepsLegacyGrants(t *testing.T) {
	dump := preMigrationDump(t,
		`INSERT INTO group_targets(group_id,target_id,surfaces) VALUES(1,12,'run')`,
	)
	st := newTestStore(t)
	if _, err := st.restoreFrom(bytes.NewReader(dump), true); err != nil {
		t.Fatal(err)
	}
	if got := st.GroupTargets(1); len(got) != 1 || got[0].TargetID != 12 {
		t.Fatalf("older dump lost entries: %+v", got)
	}
	if got := scalar[int](t, st, `SELECT COUNT(*) FROM group_target_policies`); got != 0 {
		t.Fatal("restore invented policy intent")
	}
}

func TestOUTargetPolicyEmptyHeaderSurvivesBackupRestore(t *testing.T) {
	s, _, team, _, _ := ouFixture(t)
	if err := s.st.SetGroupTargets(team, nil); err != nil {
		t.Fatal(err)
	}
	dump := dumpOf(t, s.st)
	dst := newTestStore(t)
	if _, err := dst.restoreFrom(bytes.NewReader(dump), true); err != nil {
		t.Fatal(err)
	}
	if got := scalar[int](t, dst, `SELECT COUNT(*) FROM group_target_policies WHERE group_id=?`, team); got != 1 {
		t.Fatal("empty override vanished on restore")
	}
	if got, err := dst.resolveGroupTargetsErr("ext"); err != nil || len(got) != 0 {
		t.Fatalf("restored empty override: %v %v", got, err)
	}
}

func TestOUTargetPolicyRejectsDamagedHeaderShape(t *testing.T) {
	st := newTestStore(t)
	if _, err := st.exec(`DROP TABLE group_target_policies`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.exec(`CREATE TABLE group_target_policies(group_id TEXT PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}
	if err := st.verifyMigrationProducts(st.poolExec(), st.migrations()); err == nil {
		t.Fatal("recorded migration accepted a damaged identity key")
	}
}

func TestPostgresOUTargetPolicyPresenceAndDeletion(t *testing.T) {
	st := pgStore(t, "group_target_policies", "group_targets", "batch_targets", "user_groups", "users")
	root := st.EnsureDefaultGroup()
	org, err := st.CreateUserGroup("ou-pg-org", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	team, err := st.CreateUserGroup("ou-pg-team", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetGroupParent(org, root); err != nil {
		t.Fatal(err)
	}
	if err := st.SetGroupParent(team, org); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertUser(User{Username: "ou-pg-member", PasswordHash: "h", Role: "user"}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetPrimaryGroup("ou-pg-member", team); err != nil {
		t.Fatal(err)
	}
	target, err := st.CreateTarget("p", "ou-pg-target", "{}")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetGroupTargets(org, []GroupTarget{{TargetID: target, Surfaces: SurfaceRun}}); err != nil {
		t.Fatal(err)
	}
	if got, err := st.resolveGroupTargetsErr("ou-pg-member"); err != nil || len(got) != 1 {
		t.Fatalf("inherited list: %v %v", got, err)
	}
	if err := st.SetGroupTargets(team, nil); err != nil {
		t.Fatal(err)
	}
	if got, err := st.resolveGroupTargetsErr("ou-pg-member"); err != nil || len(got) != 0 {
		t.Fatalf("empty override: %v %v", got, err)
	}
	if err := st.InheritGroupTargets(team); err != nil {
		t.Fatal(err)
	}
	if got, err := st.resolveGroupTargetsErr("ou-pg-member"); err != nil || len(got) != 1 {
		t.Fatalf("explicit inherit: %v %v", got, err)
	}
	if err := st.DeleteTarget(target); err != nil {
		t.Fatal(err)
	}
	if got := st.GroupTargets(org); len(got) != 0 {
		t.Fatalf("stale target grants: %v", got)
	}
	if err := verifyOUTargetPolicyHeader(st.poolExec()); err != nil {
		t.Fatal(err)
	}
}
