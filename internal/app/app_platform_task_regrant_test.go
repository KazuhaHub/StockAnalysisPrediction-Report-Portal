package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

// A generation-1 task is admitted and its service identity issued only while the capability rows
// sit at the installation's current policy revision. invalidateAppPlatformPrincipal raises the
// revision without moving those rows, by design: the app fails closed. Step (a) pins that
// fail-closed behaviour through the real admit and issue paths, and the facts the task preflight
// reads first.
//
// Step (b) is a fixture device, not a production flow: it moves the rows with a raw UPDATE to show
// that the paths accept rows at the current revision. At the same generation no production re-grant
// exists for runner-backed apps. The recovery action that carries rows forward
// (app_platform_recovery_actions.go) applies to static installs only, and the managed restore's
// resume moves the app to a new generation. After an access change a runner-backed app's tasks stay
// refused until a managed restore or an update; see doc 170, follow-up.
func testPlatformGenerationOneTaskNeedsAReGrantAfterAnAccessChange(t *testing.T, pg bool) {
	s, installation := platformServiceIdentityFixtureWithDatabase(t, pg)
	ctx := context.Background()
	admit := func(occurrence string) error {
		now, err := platformDatabaseNow(s.st.poolExec())
		if err != nil {
			t.Fatal(err)
		}
		_, _, err = s.admitPlatformTask(ctx, installation, platformTaskAdmission{
			OccurrenceID: occurrence, Handler: appplatform.TaskHandlerBackend,
			Payload: []byte(`{"fixture":true}`), ExpiresMS: now + int64(time.Hour/time.Millisecond),
		})
		return err
	}
	issue := func(lease string) error {
		identity, _, err := s.issuePlatformServiceIdentity(ctx, installation, lease, []string{"tasks.schedule"}, time.Minute)
		clear(identity)
		return err
	}
	// Baseline: both paths work at the install's revision.
	if err := admit("before:1"); err != nil {
		t.Fatal("baseline admission:", err)
	}
	if err := issue(platformFixtureID(7601)); err != nil {
		t.Fatal("baseline identity:", err)
	}
	// An access change: the revision moves, the capability rows do not.
	tx, err := s.st.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	e := migExec{s: s.st, ex: platformContextTx{Tx: tx, ctx: ctx}}
	if err := invalidateAppPlatformPrincipal(e, &platformPrincipalImpact{installations: []string{installation}}); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	revision := scalar[int64](t, s.st, `SELECT policy_revision FROM app_platform_installations WHERE id=?`, installation)
	if revision < 2 || scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_capability_grants WHERE installation_id=? AND policy_revision<>?`, installation, revision) == 0 {
		t.Fatal("fixture: the access change must leave the capability rows behind", revision)
	}
	// (a) No re-grant: fail closed, on admission and on identity issue.
	if err := admit("after:1"); !errors.Is(err, errAppPlatformTaskUnavailable) {
		t.Fatal("a task was admitted after an access change without a re-grant:", err)
	}
	if err := issue(platformFixtureID(7602)); !errors.Is(err, errAppPlatformServiceUnavailable) {
		t.Fatal("an identity was issued after an access change without a re-grant:", err)
	}
	// (b) Fixture device only (see above): rows at the current revision are accepted.
	mustExec(t, s.st, `UPDATE app_platform_capability_grants SET policy_revision=?,updated_ms=? WHERE installation_id=?`, revision, time.Now().UnixMilli(), installation)
	if err := admit("after:2"); err != nil {
		t.Fatal("admission after the re-grant:", err)
	}
	if err := issue(platformFixtureID(7603)); err != nil {
		t.Fatal("identity after the re-grant:", err)
	}
}

func TestPlatformGenerationOneTaskNeedsAReGrantAfterAnAccessChangeSQLite(t *testing.T) {
	t.Parallel()
	testPlatformGenerationOneTaskNeedsAReGrantAfterAnAccessChange(t, false)
}
func TestPlatformGenerationOneTaskNeedsAReGrantAfterAnAccessChangePostgres(t *testing.T) {
	t.Parallel()
	testPlatformGenerationOneTaskNeedsAReGrantAfterAnAccessChange(t, true)
}
