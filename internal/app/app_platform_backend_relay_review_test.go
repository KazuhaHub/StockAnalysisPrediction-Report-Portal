package app

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

// A derived registration holds a private key. When completing it fails, the registration that
// was built must still be closed, not a zero value.
func TestPlatformUpdateStartRegistrationClosesBuiltKeyWhenCompletionFails(t *testing.T) {
	for _, mode := range []string{"digest", "receipt"} {
		t.Run(mode, func(t *testing.T) {
			s, lease, intent, endpoint, observation := platformWritableUpdateStartRegistrationCommitFixture(t, false)
			next, err := s.commitPlatformUpdateStartRegistration(context.Background(), lease, intent, endpoint, observation)
			if err != nil {
				t.Fatal(err)
			}
			applied, _ := platformUpdateStartBootFixture(t, s, next, endpoint)
			defer applied.registration.Close()
			if mode == "digest" {
				mustExec(t, s.st, `UPDATE app_platform_operation_effects SET payload_sha256=? WHERE operation_id=? AND stage='activate' AND kind='update-start-registration'`, platformDigest([]byte("other")), lease.id)
			} else {
				mustExec(t, s.st, `UPDATE app_platform_operation_effects SET receipt_json='{}' WHERE operation_id=? AND stage='activate' AND kind='update-start-registration'`, lease.id)
			}
			var built []appplatform.BackendRegistration
			platformUpdateRegistrationBuiltHook = func(r appplatform.BackendRegistration) { built = append(built, r) }
			t.Cleanup(func() { platformUpdateRegistrationBuiltHook = nil })
			dek, err := s.dek()
			if err != nil {
				t.Fatal(err)
			}
			defer clear(dek)
			tx, err := s.st.db.Begin()
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			e := migExec{s: s.st, ex: tx}
			runtime, seed, err := s.platformDataRuntime(e, next.controllerEpoch, dek)
			if err != nil {
				t.Fatal(err)
			}
			defer clear(seed)
			now, err := platformDatabaseNow(e)
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err := loadPlatformUpdateStartRegistration(e, next, applied.start, runtime, seed, dek, now, false); err == nil {
				t.Fatal("a mismatched registration effect was accepted")
			}
			if len(built) != 1 {
				t.Fatalf("expected one built registration, got %d", len(built))
			}
			if _, err := built[0].Signer(""); err == nil {
				t.Fatal("the built registration key was left open after a failed completion")
			}
		})
	}
}

func TestPlatformUpdateStartRegistrationRowErrorsKeepTheirType(t *testing.T) {
	now := time.Now().UnixMilli()
	start := platformUpdateStartIntent{activationObserved: now - 1}
	lease := platformOperationLease{}
	// A missing sealed seed is reported only after the effect row has been judged, as it always was.
	bad := platformUpdateStartRegistrationRows{fresh: true, count: 1, baseEffects: 2, sealedErr: sql.ErrNoRows}
	if _, err := buildPlatformUpdateStartRegistration(bad, lease, start, platformRuntimeRegistration{}, nil, now, true); !errors.Is(err, errAppPlatformStageEvidence) {
		t.Fatalf("a refused effect row was reported as %v", err)
	}
	ok := platformUpdateStartRegistrationRows{fresh: true, count: 2, baseEffects: 2, sealedErr: sql.ErrNoRows}
	if _, err := buildPlatformUpdateStartRegistration(ok, lease, start, platformRuntimeRegistration{}, nil, now, true); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("a missing sealed seed was reported as %v", err)
	}
}

func TestPlatformDataCommandRowErrorsKeepTheirType(t *testing.T) {
	missing := platformDataCommandRows{plan: "not json", targetErr: sql.ErrNoRows}
	if _, err := platformDataCommandFromRows(missing, platformManagedSource{}, platformRuntimeRegistration{}, "", ""); !errors.Is(err, errAppPlatformStageEvidence) {
		t.Fatalf("an invalid plan was reported as %v", err)
	}
	// A missing release row is the error of the second query, after the profile was accepted.
	s, _, lease := platformWritableAccessFixture(t, false)
	plan := scalar[string](t, s.st, `SELECT p.plan_json FROM app_platform_operations o JOIN app_platform_review_plans p ON p.id=o.review_plan_id WHERE o.installation_id=?`, lease.installationID)
	good := platformDataCommandRows{plan: plan, targetErr: sql.ErrNoRows}
	if _, err := platformDataCommandFromRows(good, platformManagedSource{}, platformRuntimeRegistration{}, "", ""); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("a missing release row was reported as %v", err)
	}
}

// A release that fails verification is refused as such, never as whatever a later read would say,
// and a callback that opens secrets is never reached for it.
func TestPlatformBackendReadRefusesUnverifiedReleaseFirst(t *testing.T) {
	for _, also := range []string{"", "history"} {
		t.Run("also-"+also, func(t *testing.T) {
			s, actor, lease := platformWritableAccessFixture(t, false)
			mustExec(t, s.st, `UPDATE app_platform_releases SET signature=?`, []byte("broken"))
			if also == "history" {
				mustExec(t, s.st, `UPDATE app_platform_operations SET updated_ms=?`, time.Now().UnixMilli()+60000)
			}
			reached := false
			_, err := withPlatformBackendRead(s, context.Background(), actor, lease.installationID, s.loadPlatformWritableBackendHistory, func(migExec, platformWritableBackendAccess, []byte) error {
				reached = true
				return nil
			})
			if reached {
				t.Fatal("the in-transaction callback ran for an unverified release")
			}
			if !errors.Is(err, errAppPlatformLaunchUnavailable) || err == nil || !strings.Contains(err.Error(), "stored release") {
				t.Fatalf("unverified release reported as %v", err)
			}
			// The deferred path reports the same error.
			_, err = s.preparePlatformWritableBackendAccess(context.Background(), actor, lease.installationID)
			if !errors.Is(err, errAppPlatformLaunchUnavailable) || err == nil || !strings.Contains(err.Error(), "stored release") {
				t.Fatalf("unverified release reported as %v on the relay path", err)
			}
		})
	}
}
