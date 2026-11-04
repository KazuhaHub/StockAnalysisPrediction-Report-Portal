package app

import (
	"bytes"
	"strings"
	"testing"
)

func TestAppPlatformEventSubscriptionSchemaProducts(t *testing.T) {
	st := newTestStore(t)
	step := migration0020()
	if got, want := strings.Join(step.tables, ","), "app_platform_event_subscriptions"; got != want {
		t.Fatalf("tables = %s, want %s", got, want)
	}
	if len(step.indices) != 4 {
		t.Fatalf("indexes = %d, want 4", len(step.indices))
	}
	if !step.restore.olderDumpOK || step.restore.upgradeOnRestore {
		t.Fatal("event subscription migration restore rule is invalid")
	}
	if !recorded(st, "0020") {
		t.Fatal("event subscription migration not recorded")
	}
	if err := st.verifyMigrationProducts(st.poolExec(), st.migrations()); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresAppPlatformEventSubscriptionSchemaProducts(t *testing.T) {
	st := platformTestStore(t, true)
	if err := migration0020().verify(st.poolExec()); err != nil {
		t.Fatal(err)
	}
}

func TestAppPlatformEventSubscriptionConstraints(t *testing.T) {
	for _, postgres := range []bool{false, true} {
		name := "sqlite"
		if postgres {
			name = "postgres"
		}
		t.Run(name, func(t *testing.T) {
			s, installation := platformServiceIdentityFixtureWithDatabase(t, postgres)
			row := appPlatformEventSubscriptionFixture(t, s, installation)
			insertAppPlatformEventSubscription(t, s.st.poolExec(), row)
			for _, q := range []string{
				`UPDATE app_platform_event_subscriptions SET event_type='extension.arbitrary.notice'`,
				`UPDATE app_platform_event_subscriptions SET resource_type='job'`,
				`UPDATE app_platform_event_subscriptions SET state='invented'`,
				`UPDATE app_platform_event_subscriptions SET max_inflight=0`,
				`UPDATE app_platform_event_subscriptions SET max_inflight=17`,
			} {
				if _, err := s.st.exec(q); err == nil {
					t.Fatalf("invalid event subscription accepted: %s", q)
				}
			}
			if _, err := s.st.exec(`INSERT INTO app_platform_event_subscriptions(
				id,installation_id,app_id,publisher_id,event_type,resource_type,
				resource_id,state,generation,policy_revision,release_sha256,
				controller_epoch,max_inflight,revision,created_ms,updated_ms)
				VALUES('00000000-0000-4000-8000-000000007999',?,?,?,
				'report.ingested','report',?,'active',1,1,?,?,4,1,1,1)`,
				row.InstallationID, row.AppID, row.PublisherID, row.ResourceID,
				row.ReleaseSHA256, row.ControllerEpoch); err == nil {
				t.Fatal("duplicate event subscription identity accepted")
			}
		})
	}
}

func TestAppPlatformEventSubscriptionOlderDumpRestoresEmpty(t *testing.T) {
	src := newTestStore(t)
	dump := withoutSections(
		t, dumpOf(t, src), migration0020().tables, []string{"0020"})
	if bytes.Contains(dump, []byte(`"0020"`)) ||
		bytes.Contains(dump, []byte("app_platform_event_subscriptions")) {
		t.Fatal("fixture still describes 0020")
	}
	dst := newTestStore(t)
	if _, err := dst.restoreFrom(bytes.NewReader(dump), true); err != nil {
		t.Fatal(err)
	}
	if !recorded(dst, "0020") {
		t.Fatal("older dump did not acquire migration 0020")
	}
	if scalar[int](t, dst, `SELECT COUNT(*) FROM app_platform_event_subscriptions`) != 0 {
		t.Fatal("older dump restored event subscriptions")
	}
}

func TestAppPlatformEventSubscriptionBackupRoundTrip(t *testing.T) {
	src, installation := platformServiceIdentityFixture(t)
	row := appPlatformEventSubscriptionFixture(t, src, installation)
	insertAppPlatformEventSubscription(t, src.st.poolExec(), row)
	revoked := row
	revoked.ID = platformFixtureID(7302)
	revoked.ResourceID = "42"
	insertAppPlatformEventSubscription(t, src.st.poolExec(), revoked)
	mustExec(t, src.st, `UPDATE app_platform_event_subscriptions
		SET state='revoked',revision=2 WHERE id=?`, revoked.ID)
	dst := newTestStore(t)
	if _, err := dst.restoreFrom(bytes.NewReader(dumpOf(t, src.st)), true); err != nil {
		t.Fatal(err)
	}
	if got := scalar[string](t, dst, `SELECT state FROM app_platform_event_subscriptions WHERE id=?`, row.ID); got != "review_required" {
		t.Fatalf("restored subscription state = %q", got)
	}
	if got := scalar[string](t, dst,
		`SELECT state FROM app_platform_event_subscriptions WHERE id=?`,
		revoked.ID); got != "revoked" {
		t.Fatalf("restored revoked subscription state = %q", got)
	}
	if scalar[int](t, dst, `SELECT COUNT(*) FROM app_platform_event_subscriptions WHERE resource_id=?`, row.ResourceID) != 1 {
		t.Fatal("backup did not retain event subscription")
	}
}

type appPlatformEventSubscriptionRow struct {
	ID, InstallationID, AppID, PublisherID, ResourceID string
	ReleaseSHA256, ControllerEpoch                     string
}

func appPlatformEventSubscriptionFixture(t *testing.T, s *Server, installation string) appPlatformEventSubscriptionRow {
	t.Helper()
	var appID, publisher, release, epoch string
	if err := s.st.queryRow(`SELECT i.app_id,i.publisher_id,a.release_sha256,
		i.controller_epoch FROM app_platform_installations i
		JOIN app_platform_activations a
		  ON a.installation_id=i.id AND a.state='active'
		WHERE i.id=?`, installation).Scan(
		&appID, &publisher, &release, &epoch); err != nil {
		t.Fatal(err)
	}
	return appPlatformEventSubscriptionRow{
		ID: platformFixtureID(7301), InstallationID: installation,
		AppID: appID, PublisherID: publisher, ResourceID: "41",
		ReleaseSHA256: release, ControllerEpoch: epoch,
	}
}

func insertAppPlatformEventSubscription(t *testing.T, e migExec, row appPlatformEventSubscriptionRow) {
	t.Helper()
	if _, err := e.exec(`INSERT INTO app_platform_event_subscriptions(
		id,installation_id,app_id,publisher_id,event_type,resource_type,
		resource_id,state,generation,policy_revision,release_sha256,
		controller_epoch,max_inflight,revision,created_ms,updated_ms)
		VALUES(?,?,?,?,'report.ingested','report',?,'active',1,1,?,?,4,1,1,1)`,
		row.ID, row.InstallationID, row.AppID, row.PublisherID,
		row.ResourceID, row.ReleaseSHA256, row.ControllerEpoch); err != nil {
		t.Fatal(err)
	}
}
