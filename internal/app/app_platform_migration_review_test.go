package app

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
)

// platformMigrationManifest declares one migration from the installed schema (which the fixture has
// already set as the manifest's) to the next.
func platformMigrationManifest(wallClock *int) func(*appplatform.Manifest) {
	return func(m *appplatform.Manifest) {
		format := m.Data.Schema
		m.Data.Schema = format + 1
		m.Data.ReadableSchemas = []int{format + 1}
		m.Data.Migrations = []appplatform.DataMigration{{From: format, To: format + 1, Entrypoint: []string{"/bin/migrate", "--apply"}, WallClockSeconds: wallClock}}
	}
}

func buildPlatformMigrationProjection(t *testing.T, f platformManagedUpdateReviewFixture) (platformUpdateProjection, error) {
	t.Helper()
	return platformBuildManagedUpdateProjection(
		f.server.st.poolExec(), f.installation, f.source, f.revision, f.policy, f.format,
		platformActorFacts{authorityFingerprint: f.sourcePlan.ActorAuthority},
		platformActorControl{epoch: f.activeEpoch, nowMS: 1}, f.target, f.release)
}

// A review of an update whose descriptor declares a migration from the installed schema carries that
// step, so the reviewed plan binds the entrypoint and the wall-clock bound the job will run under.
func testManagedUpdateReviewBindsMigrationStep(t *testing.T, pg bool) {
	seconds := 120
	f := platformManagedUpdateProjectionFixtureWith(t, pg, platformMigrationManifest(&seconds))
	projection, err := buildPlatformMigrationProjection(t, f)
	if err != nil {
		t.Fatal(err)
	}
	m := projection.Migration
	if m == nil || m.From != f.format || m.To != f.format+1 || m.WallClockSeconds != 120 ||
		m.EntrypointSHA256 != control.MigrationEntrypointSHA256([]string{"/bin/migrate", "--apply"}) {
		t.Fatalf("migration step not bound: %+v", m)
	}
	if projection.CurrentDataFormat != f.format {
		t.Fatalf("the reviewed current format moved: %d", projection.CurrentDataFormat)
	}
	raw, err := json.Marshal(projection)
	if err != nil {
		t.Fatal(err)
	}
	var round platformUpdateProjection
	if err := json.Unmarshal(raw, &round); err != nil || round.Migration == nil || *round.Migration != *m {
		t.Fatalf("migration step does not round trip: %v", err)
	}
	// An unset wall clock takes the descriptor default.
	f = platformManagedUpdateProjectionFixtureWith(t, pg, platformMigrationManifest(nil))
	if projection, err = buildPlatformMigrationProjection(t, f); err != nil || projection.Migration == nil || projection.Migration.WallClockSeconds != 600 {
		t.Fatalf("default wall clock: %+v %v", projection.Migration, err)
	}
	// An update that needs no migration carries none, and its review text is unchanged.
	f = platformManagedUpdateProjectionFixtureWith(t, pg, nil)
	projection, err = buildPlatformMigrationProjection(t, f)
	if err != nil || projection.Migration != nil {
		t.Fatalf("plain update carries a migration: %+v %v", projection.Migration, err)
	}
	raw, _ = json.Marshal(projection)
	if strings.Contains(string(raw), "migration") {
		t.Fatal("a plain update review changed shape")
	}
}

func TestManagedUpdateReviewBindsMigrationStepSQLite(t *testing.T) {
	testManagedUpdateReviewBindsMigrationStep(t, false)
}
func TestManagedUpdateReviewBindsMigrationStepPostgres(t *testing.T) {
	testManagedUpdateReviewBindsMigrationStep(t, true)
}

// A target whose data schema differs from the installed one and that declares no step from it stays
// refused, exactly as before.
func testManagedUpdateReviewRefusesSchemaWithoutStep(t *testing.T, pg bool) {
	f := platformManagedUpdateProjectionFixtureWith(t, pg, nil)
	format := int(f.format)
	for name, mutate := range map[string]func(*appplatform.Manifest){
		"schema ahead, readable, no step": func(m *appplatform.Manifest) {
			m.Data.Schema = format + 1
			m.Data.ReadableSchemas = []int{format, format + 1}
		},
		"step from another schema": func(m *appplatform.Manifest) {
			m.Data.Schema = format + 2
			m.Data.ReadableSchemas = []int{format + 2}
			m.Data.Migrations = []appplatform.DataMigration{{From: format + 1, To: format + 2, Entrypoint: []string{"/bin/migrate"}}}
		},
		"schema behind": func(m *appplatform.Manifest) {
			m.Data.Schema = format - 1
			m.Data.ReadableSchemas = []int{format - 1}
		},
	} {
		t.Run(name, func(t *testing.T) {
			if format < 2 && name == "schema behind" {
				t.Skip("installed schema is the minimum")
			}
			local := platformManagedUpdateProjectionFixtureWith(t, pg, mutate)
			if _, err := buildPlatformMigrationProjection(t, local); err == nil {
				t.Fatal("an unsupported schema change was admitted")
			}
		})
	}
}

func TestManagedUpdateReviewRefusesSchemaWithoutStepSQLite(t *testing.T) {
	testManagedUpdateReviewRefusesSchemaWithoutStep(t, false)
}
func TestManagedUpdateReviewRefusesSchemaWithoutStepPostgres(t *testing.T) {
	testManagedUpdateReviewRefusesSchemaWithoutStep(t, true)
}
