package app

import (
	"bytes"
	"strings"
	"testing"
)

func testMachineTokenRestore(t *testing.T, pg bool) {
	st := platformTestStore(t, pg)
	platformSeedActionFixtures(t, st)
	inst := platformFixtureID(1)
	sha := func(c string) string { return strings.Repeat(c, 64) }
	// A and B are active when the backup is taken.
	if err := machineTokenInsert(st, platformFixtureID(71), inst, "aaaaaaaaaaaa", sha("a"), "active", nil, 90_000); err != nil {
		t.Fatal(err)
	}
	if err := machineTokenInsert(st, platformFixtureID(72), inst, "bbbbbbbbbbbb", sha("b"), "active", nil, 90_000); err != nil {
		t.Fatal(err)
	}
	backup := dumpOf(t, st)
	// After the backup: B is revoked, C is created and then revoked, D is created and stays active.
	mustExec(t, st, `UPDATE app_platform_machine_tokens SET state='revoked',revoked_ms=500,revision=2,updated_ms=500 WHERE handle='bbbbbbbbbbbb'`)
	if err := machineTokenInsert(st, platformFixtureID(73), inst, "cccccccccccc", sha("c"), "revoked", 600, 90_000); err != nil {
		t.Fatal(err)
	}
	if err := machineTokenInsert(st, platformFixtureID(74), inst, "dddddddddddd", sha("d"), "active", nil, 90_000); err != nil {
		t.Fatal(err)
	}
	rep, err := st.restoreFrom(bytes.NewReader(backup), true)
	if err != nil {
		t.Fatal(err)
	}
	state := func(handle string) string {
		return scalar[string](t, st, `SELECT COALESCE((SELECT state FROM app_platform_machine_tokens WHERE handle='`+handle+`'),'absent')`)
	}
	if state("aaaaaaaaaaaa") != "active" {
		t.Fatal("a token the live database never revoked must come back as the backup had it")
	}
	if state("bbbbbbbbbbbb") != "revoked" {
		t.Fatal("the backup resurrected a revoked token")
	}
	if scalar[int](t, st, `SELECT revoked_ms FROM app_platform_machine_tokens WHERE handle='bbbbbbbbbbbb'`) != 500 || scalar[int](t, st, `SELECT revision FROM app_platform_machine_tokens WHERE handle='bbbbbbbbbbbb'`) < 2 {
		t.Fatal("the earlier revocation time was not kept")
	}
	// A token revoked after the backup and absent from it stays revoked; one that was never revoked is lost.
	if state("cccccccccccc") != "revoked" || scalar[int](t, st, `SELECT revoked_ms FROM app_platform_machine_tokens WHERE handle='cccccccccccc'`) != 600 {
		t.Fatal("the floor of a token created after the backup was dropped")
	}
	if state("dddddddddddd") != "absent" {
		t.Fatal("a token created after the backup came back")
	}
	if rep.MachineTokensLost != 1 {
		t.Fatalf("restore summary counts %d lost tokens, want 1 (D)", rep.MachineTokensLost)
	}
}

func TestMachineTokenRestoreKeepsRevocationFloorSQLite(t *testing.T) {
	testMachineTokenRestore(t, false)
}
func TestMachineTokenRestoreKeepsRevocationFloorPostgres(t *testing.T) {
	testMachineTokenRestore(t, true)
}

// A revoked token whose installation is not in the backup has nothing to attach to: it is skipped,
// and the restore is not refused for it.
func TestMachineTokenRestoreSkipsAFloorWithoutItsInstallation(t *testing.T) {
	src := newTestStore(t)
	backup := dumpOf(t, src)
	st := newTestStore(t)
	platformSeedActionFixtures(t, st)
	if err := machineTokenInsert(st, platformFixtureID(81), platformFixtureID(1), "eeeeeeeeeeee", strings.Repeat("e", 64), "revoked", 600, 90_000); err != nil {
		t.Fatal(err)
	}
	rep, err := st.restoreFrom(bytes.NewReader(backup), true)
	if err != nil {
		t.Fatal(err)
	}
	if scalar[int](t, st, `SELECT COUNT(*) FROM app_platform_machine_tokens`) != 0 || rep.MachineTokensLost != 0 {
		t.Fatalf("lost=%d", rep.MachineTokensLost)
	}
}
