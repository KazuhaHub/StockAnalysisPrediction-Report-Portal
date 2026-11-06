package app

import (
	"bytes"
	"database/sql"
	"errors"
	"math"
	"path/filepath"
	"sync"
	"testing"
)

func platformSeedLeaseControl(t *testing.T, st *Store) {
	t.Helper()
	for _, key := range []string{"app_platform_controller_epoch", "app_platform_restore_paused"} {
		var previous string
		err := st.queryRow(`SELECT v FROM meta WHERE k=?`, key).Scan(&previous)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			t.Fatal(err)
		}
		existed := err == nil
		t.Cleanup(func() {
			if existed {
				if err := st.poolExec().setSetting(key, previous); err != nil {
					t.Error(err)
				}
			} else if _, err := st.exec(`DELETE FROM meta WHERE k=?`, key); err != nil {
				t.Error(err)
			}
		})
	}
	platformSeedPendingControl(t, st)
	mustExec(t, st, `UPDATE app_platform_operations SET lease_owner=NULL,lease_until_ms=NULL`)
	if err := st.poolExec().setSetting("app_platform_controller_epoch", platformFixtureID(99)); err != nil {
		t.Fatal(err)
	}
	if err := st.poolExec().setSetting("app_platform_restore_paused", "0"); err != nil {
		t.Fatal(err)
	}
}

func testPlatformLeaseLifecycle(t *testing.T, postgres bool) {
	st := platformTestStore(t, postgres)
	platformSeedLeaseControl(t, st)
	first, err := st.claimPlatformOperation(platformFixtureID(3), platformFixtureID(20))
	if err != nil {
		t.Fatal(err)
	}
	if first.fence != 2 || first.rowRevision != 2 || first.snapshot.Revision != 1 || first.leaseUntilMS <= 0 {
		t.Fatalf("invalid initial claim: %+v", first)
	}
	if _, err := st.claimPlatformOperation(platformFixtureID(3), platformFixtureID(21)); !errors.Is(err, errAppPlatformLeaseConflict) {
		t.Fatalf("live lease stolen: %v", err)
	}
	renewed, err := st.heartbeatPlatformOperation(first)
	if err != nil {
		t.Fatal(err)
	}
	if renewed.rowRevision != 3 || renewed.fence != first.fence || renewed.snapshot != first.snapshot {
		t.Fatal("heartbeat changed model state or fence")
	}
	if _, err := st.heartbeatPlatformOperation(first); !errors.Is(err, errAppPlatformLeaseConflict) {
		t.Fatalf("stale revision renewed: %v", err)
	}
	mustExec(t, st, `UPDATE app_platform_operations SET lease_until_ms=0`)
	if _, err := st.heartbeatPlatformOperation(renewed); !errors.Is(err, errAppPlatformLeaseConflict) {
		t.Fatalf("expired owner renewed: %v", err)
	}
	second, err := st.claimPlatformOperation(platformFixtureID(3), platformFixtureID(21))
	if err != nil {
		t.Fatal(err)
	}
	if second.fence != 3 || second.rowRevision != 4 || second.snapshot != first.snapshot {
		t.Fatalf("invalid reclaim: %+v", second)
	}
	if _, err := st.heartbeatPlatformOperation(renewed); !errors.Is(err, errAppPlatformLeaseConflict) {
		t.Fatalf("displaced owner renewed: %v", err)
	}
	if err := st.poolExec().setSetting("app_platform_restore_paused", "1"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.heartbeatPlatformOperation(second); !errors.Is(err, errAppPlatformControlPaused) {
		t.Fatalf("paused control renewed: %v", err)
	}
}

func testPlatformLeaseConcurrentClaim(t *testing.T, postgres bool) {
	st := platformTestStore(t, postgres)
	platformSeedLeaseControl(t, st)
	var wg sync.WaitGroup
	results := make(chan error, 8)
	for i := range 8 {
		wg.Go(func() {
			_, err := st.claimPlatformOperation(platformFixtureID(3), platformFixtureID(30+i))
			results <- err
		})
	}
	wg.Wait()
	close(results)
	winners := 0
	for err := range results {
		if err == nil {
			winners++
		} else if !errors.Is(err, errAppPlatformLeaseConflict) {
			t.Fatal(err)
		}
	}
	if winners != 1 || scalar[int](t, st, `SELECT control_fence FROM app_platform_installations`) != 2 {
		t.Fatalf("concurrent claims admitted %d owners", winners)
	}
}

func TestAppPlatformLeaseLifecycle(t *testing.T)         { testPlatformLeaseLifecycle(t, false) }
func TestPostgresAppPlatformLeaseLifecycle(t *testing.T) { testPlatformLeaseLifecycle(t, true) }
func TestAppPlatformLeaseConcurrentClaim(t *testing.T)   { testPlatformLeaseConcurrentClaim(t, false) }
func TestPostgresAppPlatformLeaseConcurrentClaim(t *testing.T) {
	testPlatformLeaseConcurrentClaim(t, true)
}

func testPlatformLeaseGuards(t *testing.T, postgres bool) {
	for _, fixture := range []struct {
		name, statement string
		args            []any
		want            error
	}{
		{"missing_pause", `DELETE FROM meta WHERE k='app_platform_restore_paused'`, nil, errAppPlatformControlPaused},
		{"missing_epoch", `DELETE FROM meta WHERE k='app_platform_controller_epoch'`, nil, errAppPlatformControlPaused},
		{"epoch_changed", `UPDATE meta SET v=? WHERE k='app_platform_controller_epoch'`, []any{platformFixtureID(98)}, errAppPlatformControlPaused},
		{"restore_review", `UPDATE app_platform_installations SET phase='review_required'`, nil, errAppPlatformLeaseConflict},
		{"recovery", `UPDATE app_platform_installations SET phase='recovery_required'`, nil, errAppPlatformLeaseConflict},
		{"removed", `UPDATE app_platform_installations SET desired_state='removed'`, nil, errAppPlatformLeaseConflict},
		{"generation_changed", `UPDATE app_platform_installations SET generation=3`, nil, errAppPlatformLeaseConflict},
		{"impossible_stage_revision", `UPDATE app_platform_operations SET state_revision=99`, nil, errAppPlatformLeaseConflict},
		{"completed", `UPDATE app_platform_operations SET stage='complete',state_revision=4,completed_ms=1`, nil, errAppPlatformLeaseConflict},
		{"row_exhausted", `UPDATE app_platform_operations SET row_revision=?`, []any{int64(math.MaxInt64)}, errAppPlatformLeaseConflict},
		{"fence_exhausted", `UPDATE app_platform_installations SET control_fence=?`, []any{int64(math.MaxInt64)}, errAppPlatformLeaseConflict},
		{"installation_revision_exhausted", `UPDATE app_platform_installations SET revision=?`, []any{int64(math.MaxInt64)}, errAppPlatformLeaseConflict},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			st := platformTestStore(t, postgres)
			platformSeedLeaseControl(t, st)
			mustExec(t, st, fixture.statement, fixture.args...)
			before := dumpOf(t, st)
			if _, err := st.claimPlatformOperation(platformFixtureID(3), platformFixtureID(20)); !errors.Is(err, fixture.want) {
				t.Fatalf("claim error: %v, want %v", err, fixture.want)
			}
			if !bytes.Equal(stripHeader(before), stripHeader(dumpOf(t, st))) {
				t.Fatal("refused claim mutated database")
			}
		})
	}
}

func TestAppPlatformLeaseGuards(t *testing.T)         { testPlatformLeaseGuards(t, false) }
func TestPostgresAppPlatformLeaseGuards(t *testing.T) { testPlatformLeaseGuards(t, true) }

func TestAppPlatformLeaseSurvivesStoreRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "leases.db")
	first, err := OpenStore("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = first.Close() })
	platformSeedPendingControl(t, first)
	mustExec(t, first, `UPDATE app_platform_operations SET lease_owner=NULL,lease_until_ms=NULL`)
	for key, value := range map[string]string{"app_platform_controller_epoch": platformFixtureID(99), "app_platform_restore_paused": "0"} {
		if err := first.poolExec().setSetting(key, value); err != nil {
			t.Fatal(err)
		}
	}
	previous, err := first.claimPlatformOperation(platformFixtureID(3), platformFixtureID(20))
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	second, err := OpenStore("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = second.Close() })
	if _, err := second.claimPlatformOperation(platformFixtureID(3), platformFixtureID(21)); !errors.Is(err, errAppPlatformLeaseConflict) {
		t.Fatalf("restart forgot live lease: %v", err)
	}
	mustExec(t, second, `UPDATE app_platform_operations SET lease_until_ms=0`)
	current, err := second.claimPlatformOperation(platformFixtureID(3), platformFixtureID(21))
	if err != nil {
		t.Fatal(err)
	}
	if current.fence != previous.fence+1 || current.rowRevision != previous.rowRevision+1 {
		t.Fatal("restart reset durable fence/revision")
	}
	if _, err := second.heartbeatPlatformOperation(previous); !errors.Is(err, errAppPlatformLeaseConflict) {
		t.Fatalf("pre-restart owner renewed after displacement: %v", err)
	}
}
