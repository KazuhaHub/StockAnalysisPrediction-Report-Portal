package app

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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

func TestPlatformInstallationCapacitySQLite(t *testing.T)   { testPlatformInstallationCapacity(t, false) }
func TestPlatformInstallationCapacityPostgres(t *testing.T) { testPlatformInstallationCapacity(t, true) }

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
