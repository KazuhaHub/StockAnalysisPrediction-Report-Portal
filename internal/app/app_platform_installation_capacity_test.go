package app

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

// seedInstallations adds n installations of other applications, in the given phase. Each counts as one
// installation whether it is static or managed: the cap covers both.
func seedInstallations(t *testing.T, s *Server, n int, phase string) {
	t.Helper()
	base := scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_installations`)
	for i := 0; i < n; i++ {
		id := platformFixtureID(5000 + base + i)
		if err := platformSeedInstallation(t, s.st.poolExec(), id, "fixture-publisher", fmt.Sprintf("seeded-app-%d", base+i)); err != nil {
			t.Fatal(err)
		}
	}
	mustExec(t, s.st, `UPDATE app_platform_installations SET phase=? WHERE app_id LIKE 'seeded-app-%'`, phase)
}

func testPlatformInstallationCapacity(t *testing.T, pg bool) {
	t.Run("review_refused_at_the_cap", func(t *testing.T) {
		s, session, staged := platformInstallFixture(t, pg)
		seedInstallations(t, s, platformInstallationCap, "ready")
		if _, err := s.createPlatformNativeInstallReview(session, staged.sha256, 1); !errors.Is(err, errPlatformInstallationCapacity) {
			t.Fatalf("review at the cap: %v", err)
		}
		if scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_review_plans`) != 0 {
			t.Fatal("a refused review left a plan behind")
		}
	})
	t.Run("last_slot_is_admitted_and_the_next_is_not", func(t *testing.T) {
		s, session, staged := platformInstallFixture(t, pg)
		seedInstallations(t, s, platformInstallationCap-1, "ready")
		review, err := s.createPlatformNativeInstallReview(session, staged.sha256, 1)
		if err != nil {
			t.Fatalf("the 50th installation was refused at review: %v", err)
		}
		// Another installation takes the last slot between the review and its consumption: the consumption
		// transaction is authoritative and refuses the 51st.
		seedInstallations(t, s, 1, "ready")
		before := scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_installations`)
		if _, err := s.consumePlatformInstallReview(session, review.ID, review.SHA256, platformFixtureID(300)); !errors.Is(err, errPlatformInstallationCapacity) {
			t.Fatalf("the 51st installation was admitted at consumption: %v", err)
		}
		if scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_installations`) != before ||
			scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_operations`) != 0 ||
			scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_review_plans WHERE consumed_ms IS NOT NULL`) != 0 {
			t.Fatal("a refused consumption wrote state")
		}
	})
	t.Run("removed_installation_frees_a_slot", func(t *testing.T) {
		s, session, staged := platformInstallFixture(t, pg)
		seedInstallations(t, s, platformInstallationCap, "ready")
		mustExec(t, s.st, `UPDATE app_platform_installations SET phase='removed',desired_state='removed' WHERE id=?`, platformFixtureID(5000))
		review, err := s.createPlatformNativeInstallReview(session, staged.sha256, 1)
		if err != nil {
			t.Fatalf("a freed slot was not reusable at review: %v", err)
		}
		if _, err := s.consumePlatformInstallReview(session, review.ID, review.SHA256, platformFixtureID(301)); err != nil {
			t.Fatalf("a freed slot was not reusable at consumption: %v", err)
		}
	})
	t.Run("every_other_phase_counts", func(t *testing.T) {
		for _, phase := range []string{"pending", "setup_required", "ready", "disabled", "recovery_required", "quarantined", "review_required"} {
			s, session, staged := platformInstallFixture(t, pg)
			seedInstallations(t, s, platformInstallationCap, phase)
			if _, err := s.createPlatformNativeInstallReview(session, staged.sha256, 1); !errors.Is(err, errPlatformInstallationCapacity) {
				t.Fatalf("phase %s did not count: %v", phase, err)
			}
		}
	})
}

func TestPlatformInstallationCapacitySQLite(t *testing.T) { testPlatformInstallationCapacity(t, false) }
func TestPlatformInstallationCapacityPostgres(t *testing.T) {
	testPlatformInstallationCapacity(t, true)
}

func TestPlatformInstallationCapacityIsFifty(t *testing.T) {
	if platformInstallationCap != 50 {
		t.Fatalf("cap = %d", platformInstallationCap)
	}
}

func TestPlatformInstallationCapacityMapsTo429(t *testing.T) {
	w := httptest.NewRecorder()
	platformManagementError(w, fmt.Errorf("wrapped: %w", errPlatformInstallationCapacity))
	if w.Code != http.StatusTooManyRequests || !strings.Contains(w.Body.String(), `"installation_capacity"`) {
		t.Fatalf("response = %d %s", w.Code, w.Body.String())
	}
}

// stageSecondApp stages a release of another application, so two installs can compete for one slot.
func stageSecondApp(t *testing.T, s *Server, session platformActorSession) platformStagedRelease {
	t.Helper()
	raw, _, oldArchive := platformSignedReleaseFixture(t, "1.0.0-beta.1")
	var zipped bytes.Buffer
	w := zip.NewWriter(&zipped)
	file, err := w.Create("index.html")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = file.Write([]byte("<!doctype html><title>Second</title>")); err != nil {
		t.Fatal(err)
	}
	if err = w.Close(); err != nil {
		t.Fatal(err)
	}
	archive := zipped.Bytes()
	raw = bytes.ReplaceAll(raw, []byte(platformDigest(oldArchive)), []byte(platformDigest(archive)))
	var manifest appplatform.Manifest
	if json.Unmarshal(raw, &manifest) != nil {
		t.Fatal("bad fixture")
	}
	manifest.ID, manifest.Version = "notes-second", "1.0.1-beta.1"
	manifest.Compatibility.PlatformAPI = ">=2.0.0-0 <3.0.0"
	manifest.Capabilities.Required = []string{}
	raw, err = json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if parsed, perr := appplatform.ParseDescriptor(raw); perr != nil {
		t.Fatalf("descriptor: %v", perr)
	} else if _, ierr := appplatform.InspectFrontend(parsed.Manifest, archive); ierr != nil {
		t.Fatalf("frontend: %v", ierr)
	}
	staged, err := s.stagePlatformRelease(session, raw, platformSignTestDescriptor(t, raw), archive)
	if err != nil {
		t.Fatal(err)
	}
	return staged
}

// Two consumptions of different apps race for the 50th slot: exactly one wins, the other is refused by the cap,
// and the count never passes 50. On PostgreSQL the guard is the publisher-trust table lock, on SQLite the write
// lock.
func testPlatformInstallationCapacityRace(t *testing.T, pg bool) {
	s, session, first := platformInstallFixture(t, pg)
	second := stageSecondApp(t, s, session)
	seedInstallations(t, s, platformInstallationCap-1, "ready")
	reviewA, err := s.createPlatformNativeInstallReview(session, first.sha256, 1)
	if err != nil {
		t.Fatal(err)
	}
	reviewB, err := s.createPlatformNativeInstallReview(session, second.sha256, 1)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for i, review := range []platformRemovalReview{reviewA, reviewB} {
		wg.Go(func() {
			_, err := s.consumePlatformInstallReview(session, review.ID, review.SHA256, platformFixtureID(310+i))
			errs <- err
		})
	}
	wg.Wait()
	close(errs)
	wins, refused := 0, 0
	for err := range errs {
		switch {
		case err == nil:
			wins++
		case errors.Is(err, errPlatformInstallationCapacity):
			refused++
		default:
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if wins != 1 || refused != 1 || scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_installations WHERE phase<>'removed'`) != platformInstallationCap {
		t.Fatalf("wins=%d refused=%d installations=%d", wins, refused, scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_installations`))
	}
}

func TestPlatformInstallationCapacityRaceSQLite(t *testing.T) {
	testPlatformInstallationCapacityRace(t, false)
}
func TestPlatformInstallationCapacityRacePostgres(t *testing.T) {
	testPlatformInstallationCapacityRace(t, true)
}
