package app

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestPlatformRequiredSetupBindingControlsRunnablePhase(t *testing.T) {
	s, admin, installation, credential := platformSecretBindingFixture(t, false)
	mustExec(t, s.st, `UPDATE app_platform_installations
		SET app_id='scheduled-digest',phase='setup_required'
		WHERE id=?`, installation)
	e := s.st.poolExec()
	missing, err := platformMissingSetupBindings(
		e, installation, "scheduled-digest", 1)
	if err != nil || len(missing) != 1 ||
		missing[0] != platformDigestFeedBinding {
		t.Fatalf("initial setup gap = %v, %v", missing, err)
	}

	plan, err := s.createPlatformSecretBindingReview(
		admin, installation, platformDigestFeedBinding,
		credential.ID, "http_bearer")
	if err != nil {
		t.Fatal("setup binding review", err)
	}
	binding, err := s.consumePlatformSecretBindingReview(
		admin, plan.ID, plan.SHA256)
	if err != nil || binding.State != "active" {
		t.Fatal("setup binding activation", binding, err)
	}
	if got := scalar[string](t, s.st, `SELECT phase
		FROM app_platform_installations WHERE id=?`,
		installation); got != "ready" {
		t.Fatalf("configured phase = %q", got)
	}
	missing, err = platformMissingSetupBindings(
		e, installation, "scheduled-digest", 1)
	if err != nil || len(missing) != 0 {
		t.Fatalf("configured setup gap = %v, %v", missing, err)
	}

	rotation, err := s.previewPlatformCredentialChange(
		admin, credential.ID, credential.Revision,
		credential.ValueRevision, "replace")
	if err != nil {
		t.Fatal("setup credential rotation review", err)
	}
	rotated, err := s.consumePlatformCredentialReview(
		admin, rotation.ID, rotation.SHA256,
		[]byte("replacement-digest-feed"))
	if err != nil {
		t.Fatal("setup credential rotation", err)
	}
	if got := scalar[string](t, s.st, `SELECT phase
		FROM app_platform_installations WHERE id=?`,
		installation); got != "setup_required" {
		t.Fatalf("rotated phase = %q", got)
	}
	plan, err = s.createPlatformSecretBindingReview(
		admin, installation, platformDigestFeedBinding,
		rotated.ID, "http_bearer")
	if err != nil {
		t.Fatal("rotated setup binding review", err)
	}
	binding, err = s.consumePlatformSecretBindingReview(
		admin, plan.ID, plan.SHA256)
	if err != nil {
		t.Fatal("rotated setup binding activation", err)
	}
	if got := scalar[string](t, s.st, `SELECT phase
		FROM app_platform_installations WHERE id=?`,
		installation); got != "ready" {
		t.Fatalf("reapproved phase = %q", got)
	}
	if err := s.revokePlatformSecretBinding(
		admin, binding.ID, binding.Revision); err != nil {
		t.Fatal("setup binding revoke", err)
	}
	if got := scalar[string](t, s.st, `SELECT phase
		FROM app_platform_installations WHERE id=?`,
		installation); got != "setup_required" {
		t.Fatalf("revoked phase = %q", got)
	}
}

func TestPlatformSetupOnlyBindingDoesNotRequireNetworkAuthority(t *testing.T) {
	s, admin, installation, credential := platformSecretBindingFixture(t, false)
	mustExec(t, s.st, `UPDATE app_platform_installations
		SET app_id='scheduled-digest',phase='setup_required'
		WHERE id=?`, installation)
	mustExec(t, s.st, `DELETE FROM app_platform_capability_grants
		WHERE installation_id=? AND capability='network.http'`, installation)
	plan, err := s.createPlatformSecretBindingReview(
		admin, installation, platformDigestFeedBinding,
		credential.ID, "raw_read")
	if err != nil {
		t.Fatal("setup-only binding review", err)
	}
	binding, err := s.consumePlatformSecretBindingReview(
		admin, plan.ID, plan.SHA256)
	if err != nil || binding.Operation != "raw_read" {
		t.Fatal("setup-only binding activation", binding, err)
	}
	if got := scalar[string](t, s.st, `SELECT phase
		FROM app_platform_installations WHERE id=?`,
		installation); got != "ready" {
		t.Fatalf("setup-only configured phase = %q", got)
	}
}

func TestPlatformTaskAdmissionRefusesMissingBusinessSetup(t *testing.T) {
	s, installation := platformServiceIdentityFixtureForApp(
		t, "scheduled-digest")
	now, err := platformDatabaseNow(s.st.poolExec())
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, s.st, `UPDATE app_platform_installations
		SET phase='setup_required' WHERE id=?`, installation)
	if phase, err := platformReadyPhaseForInstallation(
		s.st.poolExec(), installation, 1); err != nil ||
		phase != "setup_required" {
		t.Fatalf("activation phase = %q, %v", phase, err)
	}
	// Even an inconsistent ready row must not admit work while the required
	// business binding is absent.
	mustExec(t, s.st, `UPDATE app_platform_installations
		SET phase='ready' WHERE id=?`, installation)
	admission := platformTaskAdmission{
		OccurrenceID: "digest:setup-missing",
		Handler:      "scheduled-digest.run",
		Payload:      []byte(`{"scheduleId":"daily","reportIds":[1]}`),
		ExpiresMS:    now + int64(time.Hour/time.Millisecond),
	}
	_, _, err = s.admitPlatformTask(
		context.Background(), installation, admission)
	if !errors.Is(err, errAppPlatformTaskUnavailable) {
		t.Fatalf("unconfigured task admission = %v", err)
	}
	if got := scalar[int](t, s.st, `SELECT COUNT(*)
		FROM app_platform_tasks WHERE installation_id=?`,
		installation); got != 0 {
		t.Fatal("unconfigured task was persisted")
	}
}
