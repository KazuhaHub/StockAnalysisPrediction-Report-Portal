package app

import (
	"bytes"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func platformSeedEffectLease(t *testing.T, st *Store) platformOperationLease {
	t.Helper()
	platformSeedLeaseControl(t, st)
	mustExec(t, st, `UPDATE app_platform_installations SET runtime_id=?,generation=2,desired_state='disabled',phase='disabled'`, platformFixtureID(5))
	mustExec(t, st, `UPDATE app_platform_operations SET managed=1,stage='stop',state_revision=4`)
	lease, err := st.claimPlatformOperation(platformFixtureID(3), platformFixtureID(20))
	if err != nil {
		t.Fatal(err)
	}
	return lease
}

func testPlatformEffectIdentity(t *testing.T, postgres bool) {
	st := platformTestStore(t, postgres)
	lease := platformSeedEffectLease(t, st)
	intent := platformEffectIntent{number: 1, kind: "stop", payloadSHA: strings.Repeat("a", 64)}
	first, err := st.preparePlatformEffect(lease, intent)
	if err != nil {
		t.Fatal(err)
	}
	if !first.created || !platformValidUUID(first.id) || first.admittedFence != lease.fence || first.state != "prepared" || first.observedFence != 0 {
		t.Fatalf("invalid intent: %+v", first)
	}
	second, err := st.preparePlatformEffect(lease, intent)
	if err != nil || second.created || second.id != first.id {
		t.Fatalf("repeated preparation: %+v %v", second, err)
	}
	changed := intent
	changed.payloadSHA = strings.Repeat("b", 64)
	if _, err := st.preparePlatformEffect(lease, changed); !errors.Is(err, errAppPlatformEffectConflict) {
		t.Fatalf("changed payload accepted: %v", err)
	}
	// An uncertain prior dispatch keeps its original identity and execution fence.
	mustExec(t, st, `UPDATE app_platform_operation_effects SET state='unknown',receipt_json='{"historical":true}'`)
	mustExec(t, st, `UPDATE app_platform_operations SET lease_until_ms=0`)
	current, err := st.claimPlatformOperation(lease.id, platformFixtureID(21))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.preparePlatformEffect(lease, intent); !errors.Is(err, errAppPlatformLeaseConflict) {
		t.Fatalf("stale worker admitted: %v", err)
	}
	recovered, err := st.preparePlatformEffect(current, intent)
	if err != nil || recovered.created || recovered.id != first.id || recovered.admittedFence != first.admittedFence || recovered.state != "unknown" {
		t.Fatalf("reclaim replaced uncertain effect: %+v %v", recovered, err)
	}
	if scalar[int](t, st, `SELECT COUNT(*) FROM app_platform_operation_effects`) != 1 {
		t.Fatal("duplicate physical intent")
	}
	if scalar[string](t, st, `SELECT stage FROM app_platform_operations`) != "stop" {
		t.Fatal("intent completed stage")
	}
	if _, err := st.heartbeatPlatformOperation(current); err != nil {
		t.Fatalf("prepare unexpectedly changed worker lease: %v", err)
	}
}

func TestAppPlatformEffectIdentity(t *testing.T)         { testPlatformEffectIdentity(t, false) }
func TestPostgresAppPlatformEffectIdentity(t *testing.T) { testPlatformEffectIdentity(t, true) }

func testPlatformEffectConcurrentPrepare(t *testing.T, postgres bool) {
	st := platformTestStore(t, postgres)
	lease := platformSeedEffectLease(t, st)
	intent := platformEffectIntent{number: 1, kind: "stop", payloadSHA: strings.Repeat("a", 64)}
	var wg sync.WaitGroup
	results := make(chan platformEffectRecord, 8)
	errs := make(chan error, 8)
	for range 8 {
		wg.Go(func() { r, err := st.preparePlatformEffect(lease, intent); results <- r; errs <- err })
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	created, id := 0, ""
	for r := range results {
		if r.created {
			created++
		}
		if id == "" {
			id = r.id
		}
		if id != r.id {
			t.Fatal("concurrent preparations have different identities")
		}
	}
	if created != 1 {
		t.Fatalf("created %d effects", created)
	}
}

func TestAppPlatformEffectConcurrentPrepare(t *testing.T) {
	testPlatformEffectConcurrentPrepare(t, false)
}
func TestPostgresAppPlatformEffectConcurrentPrepare(t *testing.T) {
	testPlatformEffectConcurrentPrepare(t, true)
}

func testPlatformEffectGuards(t *testing.T, postgres bool) {
	for _, tc := range []struct {
		name, sql string
		want      error
	}{
		{"expired", `UPDATE app_platform_operations SET lease_until_ms=0`, errAppPlatformLeaseConflict},
		{"paused", `UPDATE meta SET v='1' WHERE k='app_platform_restore_paused'`, errAppPlatformControlPaused},
		{"epoch", `UPDATE meta SET v='` + platformFixtureID(98) + `' WHERE k='app_platform_controller_epoch'`, errAppPlatformControlPaused},
		{"fence", `UPDATE app_platform_installations SET control_fence=control_fence+1`, errAppPlatformLeaseConflict},
		{"generation", `UPDATE app_platform_installations SET generation=3`, errAppPlatformLeaseConflict},
		{"runtime_epoch", `UPDATE app_platform_runtime_bindings SET controller_epoch='` + platformFixtureID(98) + `'`, errAppPlatformEffectConflict},
		{"runtime_removed", `UPDATE app_platform_installations SET runtime_id=NULL`, errAppPlatformEffectConflict},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := platformTestStore(t, postgres)
			lease := platformSeedEffectLease(t, st)
			mustExec(t, st, tc.sql)
			before := dumpOf(t, st)
			if _, err := st.preparePlatformEffect(lease, platformEffectIntent{number: 1, kind: "stop", payloadSHA: strings.Repeat("a", 64)}); !errors.Is(err, tc.want) {
				t.Fatalf("got %v want %v", err, tc.want)
			}
			if !bytes.Equal(stripHeader(before), stripHeader(dumpOf(t, st))) {
				t.Fatal("refusal mutated database")
			}
		})
	}
}

func TestAppPlatformEffectGuards(t *testing.T)         { testPlatformEffectGuards(t, false) }
func TestPostgresAppPlatformEffectGuards(t *testing.T) { testPlatformEffectGuards(t, true) }

func testPlatformEffectIntentRefusals(t *testing.T, postgres bool) {
	st := platformTestStore(t, postgres)
	lease := platformSeedEffectLease(t, st)
	for _, intent := range []platformEffectIntent{
		{0, "stop", strings.Repeat("a", 64)}, {33, "stop", strings.Repeat("a", 64)},
		{1, "start-candidate", strings.Repeat("a", 64)}, {1, "exec", strings.Repeat("a", 64)},
		{1, "stop", strings.Repeat("A", 64)}, {1, "stop", ""},
	} {
		before := dumpOf(t, st)
		if _, err := st.preparePlatformEffect(lease, intent); !errors.Is(err, errAppPlatformEffectConflict) {
			t.Fatalf("intent %+v: %v", intent, err)
		}
		if !bytes.Equal(stripHeader(before), stripHeader(dumpOf(t, st))) {
			t.Fatal("invalid intent mutated database")
		}
	}
	// Cleanup remains available when runtime admission has been disabled.
	mustExec(t, st, `UPDATE app_platform_runtime_bindings SET enabled=0`)
	if _, err := st.preparePlatformEffect(lease, platformEffectIntent{1, "stop", strings.Repeat("a", 64)}); err != nil {
		t.Fatal(err)
	}
}

func TestAppPlatformEffectIntentRefusals(t *testing.T) { testPlatformEffectIntentRefusals(t, false) }
func TestPostgresAppPlatformEffectIntentRefusals(t *testing.T) {
	testPlatformEffectIntentRefusals(t, true)
}

func TestAppPlatformEffectSurvivesRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "effect.db")
	st, err := OpenStore("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	platformSeedPendingControl(t, st)
	for key, value := range map[string]string{"app_platform_controller_epoch": platformFixtureID(99), "app_platform_restore_paused": "0"} {
		if err := st.poolExec().setSetting(key, value); err != nil {
			t.Fatal(err)
		}
	}
	mustExec(t, st, `UPDATE app_platform_operations SET lease_owner=NULL,lease_until_ms=NULL,stage='stop',state_revision=4,managed=1`)
	mustExec(t, st, `UPDATE app_platform_installations SET runtime_id=?,generation=2,desired_state='disabled',phase='disabled'`, platformFixtureID(5))
	lease, err := st.claimPlatformOperation(platformFixtureID(3), platformFixtureID(20))
	if err != nil {
		t.Fatal(err)
	}
	intent := platformEffectIntent{1, "stop", strings.Repeat("a", 64)}
	first, err := st.preparePlatformEffect(lease, intent)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	reopenedStore, err := OpenStore("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	st = reopenedStore
	reopened, err := st.preparePlatformEffect(lease, intent)
	if err != nil || reopened.created || reopened.id != first.id || reopened.admittedFence != first.admittedFence {
		t.Fatalf("restart lost durable identity: %+v %v", reopened, err)
	}
}

func TestPostgresAppPlatformEffectExpiryWhileWaitingForReceipt(t *testing.T) {
	st := platformTestStore(t, true)
	lease := platformSeedEffectLease(t, st)
	intent := platformEffectIntent{1, "stop", strings.Repeat("a", 64)}
	first, err := st.preparePlatformEffect(lease, intent)
	if err != nil {
		t.Fatal(err)
	}
	now, err := platformDatabaseNow(st.poolExec())
	if err != nil {
		t.Fatal(err)
	}
	lease.leaseUntilMS = now + 1000
	mustExec(t, st, `UPDATE app_platform_operations SET lease_until_ms=?`, lease.leaseUntilMS)
	holder, err := st.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Rollback()
	var id string
	if err := holder.QueryRow(`SELECT effect_id FROM app_platform_operation_effects WHERE effect_id=$1 FOR UPDATE`, first.id).Scan(&id); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := st.preparePlatformEffect(lease, intent); done <- err }()
	blocked := false
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var count int
		if err := st.queryRow(`SELECT COUNT(*) FROM pg_stat_activity WHERE datname=current_database() AND pid<>pg_backend_pid()
            AND wait_event_type='Lock' AND query LIKE '%FROM app_platform_operation_effects WHERE operation_id=%FOR UPDATE%'`).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count > 0 {
			blocked = true
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !blocked {
		holder.Rollback()
		<-done
		t.Fatal("preparation did not reach held receipt lock")
	}
	for now < lease.leaseUntilMS {
		if time.Now().After(deadline) {
			t.Fatal("database clock did not reach lease expiry within test deadline")
		}
		time.Sleep(10 * time.Millisecond)
		now, err = platformDatabaseNow(st.poolExec())
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := holder.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := <-done; !errors.Is(err, errAppPlatformLeaseConflict) {
		t.Fatalf("expired worker observed effect as current: %v", err)
	}
}
