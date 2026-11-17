package app

import (
	"bytes"
	"encoding/json"
	"strings"
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
	targets := []platformAudienceTarget{{kind: "user", username: session.username}, {kind: "ou", ouID: group}}
	review, err := s.createPlatformNativeInstallAudienceReview(session, release.sha256, 1, targets)
	if err != nil {
		t.Fatal(err)
	}
	var p platformInstallProjection
	if json.Unmarshal([]byte(review.JSON), &p) != nil || len(p.Audience) != 2 || p.Audience[0].Type != "ou" || p.Audience[1].AccountGeneration == "" {
		t.Fatal("audience not digest-bound/canonical")
	}
	if scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_access_grants`) != 0 {
		t.Fatal("review granted access")
	}
	if _, err := s.consumePlatformInstallReview(session, review.ID, review.SHA256, platformFixtureID(920)); err != nil {
		t.Fatal(err)
	}
	if scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_access_grants`) != 0 {
		t.Fatal("operation admission granted access before activation")
	}
}
func TestPlatformInstallAudienceReviewSQLite(t *testing.T) {
	testPlatformInstallAudienceReview(t, false)
}
func TestPlatformInstallAudienceReviewPostgres(t *testing.T) {
	testPlatformInstallAudienceReview(t, true)
}
func testPlatformInstallAudienceRefusals(t *testing.T, pg bool) {
	for _, kind := range []string{"duplicate", "missing_user", "zero_ou", "mixed", "unknown_kind", "too_many", "deleted_user", "recreated_user", "inactive_user", "expired_user", "changed_ou"} {
		t.Run(kind, func(t *testing.T) {
			s, session, release := platformInstallFixture(t, pg)
			targets := []platformAudienceTarget{{kind: "user", username: session.username}}
			switch kind {
			case "duplicate":
				targets = append(targets, targets[0])
			case "missing_user":
				targets[0].username = "missing-audience-account"
			case "zero_ou":
				targets = []platformAudienceTarget{{kind: "ou", ouID: 0}}
			case "mixed":
				targets[0].ouID = 1
			case "unknown_kind":
				targets[0].kind = "all"
			case "too_many":
				targets = make([]platformAudienceTarget, maxPlatformInstallAudience+1)
			}
			if kind == "changed_ou" {
				group, err := s.st.CreateUserGroup("Audience identity fixture", "", 0)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					if _, _, err := s.st.DeleteUserGroup(group); err != nil {
						t.Error(err)
					}
				})
				targets = []platformAudienceTarget{{kind: "ou", ouID: group}}
			}
			if strings.HasSuffix(kind, "user") && kind != "missing_user" {
				if err := s.st.UpsertUser(User{Username: "audience-life", PasswordHash: "fixture", Role: "user"}); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					if err := s.st.DeleteUser("audience-life"); err != nil {
						t.Error(err)
					}
				})
				targets = []platformAudienceTarget{{kind: "user", username: "audience-life"}}
			}
			before := dumpOf(t, s.st)
			review, err := s.createPlatformNativeInstallAudienceReview(session, release.sha256, 1, targets)
			switch kind {
			case "deleted_user", "recreated_user", "inactive_user", "expired_user", "changed_ou":
				if err != nil {
					t.Fatal(err)
				}
				switch kind {
				case "deleted_user", "recreated_user":
					if err := s.st.DeleteUser("audience-life"); err != nil {
						t.Fatal(err)
					}
					if kind == "recreated_user" {
						if err := s.st.UpsertUser(User{Username: "audience-life", PasswordHash: "fixture", Role: "user"}); err != nil {
							t.Fatal(err)
						}
					}
				case "inactive_user":
					mustExec(t, s.st, `UPDATE users SET active=0 WHERE username='audience-life'`)
				case "expired_user":
					mustExec(t, s.st, `UPDATE users SET expires_at='2000-01-01' WHERE username='audience-life'`)
				case "changed_ou":
					mustExec(t, s.st, `UPDATE user_groups SET created_at='different-creation' WHERE id=?`, targets[0].ouID)
				}
				before = dumpOf(t, s.st)
				if _, err := s.consumePlatformInstallReview(session, review.ID, review.SHA256, platformFixtureID(921)); err == nil {
					t.Fatal("stale principal identity admitted", kind)
				}
			default:
				if err == nil {
					t.Fatal("invalid audience reviewed", kind)
				}
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
