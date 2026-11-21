package app

import (
	"bytes"
	"encoding/json"
	"errors"
	"testing"
)

func testPlatformInstallAudienceReview(t *testing.T, pg bool) {
	s, session, release := platformInstallFixture(t, pg)
	group, err := s.st.CreateUserGroup("Install audience fixture", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, _, err := s.st.DeleteUserGroup(group); err != nil {
			t.Error(err)
		}
	})
	targets := []platformAudienceTarget{{kind: "ou", ouID: group}}
	review, err := s.createPlatformNativeInstallAudienceReview(session, release.sha256, 1, targets)
	if err != nil {
		t.Fatal(err)
	}
	var p platformInstallProjection
	if json.Unmarshal([]byte(review.JSON), &p) != nil || len(p.Audience) != 1 || p.Audience[0].Type != "ou" || p.Audience[0].GroupCreated == "" {
		t.Fatal("open_to_ous not digest-bound/canonical")
	}
	if scalar[int](t, s.st, `SELECT COUNT(*) FROM app_ou_access WHERE app_key LIKE 'platform:%'`) != 0 {
		t.Fatal("review opened the installation")
	}
	admitted, err := s.consumePlatformInstallReview(session, review.ID, review.SHA256, platformFixtureID(920))
	if err != nil {
		t.Fatal(err)
	}
	if scalar[int](t, s.st, `SELECT COUNT(*) FROM app_ou_access WHERE app_key LIKE 'platform:%'`) != 0 || scalar[int](t, s.st, `SELECT COUNT(*) FROM app_order WHERE app_key LIKE 'platform:%'`) != 0 {
		t.Fatal("operation admission opened the installation before activation", admitted.InstallationID)
	}
}
func TestPlatformInstallAudienceReviewSQLite(t *testing.T) {
	testPlatformInstallAudienceReview(t, false)
}
func TestPlatformInstallAudienceReviewPostgres(t *testing.T) {
	testPlatformInstallAudienceReview(t, true)
}

// Install access is per OU only: a user principal is refused with its own error, nothing is written.
func TestPlatformInstallPlanRefusesUserPrincipalsSQLite(t *testing.T) {
	s, session, release := platformInstallFixture(t, false)
	before := dumpOf(t, s.st)
	_, err := s.createPlatformNativeInstallAudienceReview(session, release.sha256, 1, []platformAudienceTarget{{kind: "user", username: session.username}})
	if !errors.Is(err, errPlatformUserAudience) {
		t.Fatal("user principal accepted in an install plan:", err)
	}
	if !bytes.Equal(stripHeader(before), stripHeader(dumpOf(t, s.st))) {
		t.Fatal("refused plan mutated state")
	}
}

func testPlatformInstallAudienceRefusals(t *testing.T, pg bool) {
	for _, kind := range []string{"duplicate", "zero_ou", "unknown_kind", "too_many", "missing_ou", "user_with_ou", "changed_ou"} {
		t.Run(kind, func(t *testing.T) {
			s, session, release := platformInstallFixture(t, pg)
			group, err := s.st.CreateUserGroup("Audience refusal fixture", "", 0)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if _, _, err := s.st.DeleteUserGroup(group); err != nil {
					t.Error(err)
				}
			})
			targets := []platformAudienceTarget{{kind: "ou", ouID: group}}
			switch kind {
			case "duplicate":
				targets = append(targets, targets[0])
			case "zero_ou":
				targets = []platformAudienceTarget{{kind: "ou", ouID: 0}}
			case "unknown_kind":
				targets[0].kind = "all"
			case "too_many":
				targets = make([]platformAudienceTarget, maxPlatformInstallAudience+1)
			case "missing_ou":
				targets = []platformAudienceTarget{{kind: "ou", ouID: group + 1000}}
			case "user_with_ou":
				targets[0].username = session.username
			}
			before := dumpOf(t, s.st)
			review, err := s.createPlatformNativeInstallAudienceReview(session, release.sha256, 1, targets)
			if kind == "changed_ou" {
				if err != nil {
					t.Fatal(err)
				}
				mustExec(t, s.st, `UPDATE user_groups SET created_at='different-creation' WHERE id=?`, group)
				before = dumpOf(t, s.st)
				if _, err := s.consumePlatformInstallReview(session, review.ID, review.SHA256, platformFixtureID(921)); err == nil {
					t.Fatal("stale OU identity admitted")
				}
			} else if err == nil {
				t.Fatal("invalid open_to_ous reviewed", kind)
			}
			if !bytes.Equal(stripHeader(before), stripHeader(dumpOf(t, s.st))) {
				t.Fatal("audience refusal mutated state", kind)
			}
		})
	}
}
func TestPlatformInstallAudienceRefusalsSQLite(t *testing.T) {
	testPlatformInstallAudienceRefusals(t, false)
}
func TestPlatformInstallAudienceRefusalsPostgres(t *testing.T) {
	testPlatformInstallAudienceRefusals(t, true)
}
