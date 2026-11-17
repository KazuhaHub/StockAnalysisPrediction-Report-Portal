package app

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

// evidence runs the never-started eligibility directly, the way the review, the consumption and every later stage do.
func (f refusedInstall) evidence() (platformRemovalNeverStarted, error) {
	var epoch, runtime string
	var namespace string
	var format int64
	if err := f.s.st.queryRow(`SELECT i.controller_epoch,i.runtime_id,n.id,n.data_format FROM app_platform_installations i
		JOIN app_platform_data_namespaces n ON n.owner_installation_id=i.id WHERE i.id=?`, f.installation).Scan(&epoch, &runtime, &namespace, &format); err != nil {
		return platformRemovalNeverStarted{}, err
	}
	return platformLoadNeverStartedEvidence(migExec{s: f.s.st, ex: f.s.st.db}, f.installation, epoch, runtime, namespace, format, "")
}

// refusedBecause asserts the typed refusal and its specific reason, so a guard cannot be satisfied by another one.
func refusedBecause(t *testing.T, err error, reason string) {
	t.Helper()
	if !errors.Is(err, errAppPlatformPlanChanged) || !strings.Contains(err.Error(), "("+reason+")") {
		t.Fatalf("want a plan-changed refusal for %q, got: %v", reason, err)
	}
}

// Item 1: an install at stage failed is not admitted. A failed install with a namespace is not reachable
// (finalizePlatformInstallForReview ends any install that owns a resource at recovery_required), and the close
// accepts only recovery_required, so admitting it would let the runner delete the namespace and then fail every
// settlement retry on the close.
func TestNeverStartedRemovalDoesNotAdmitAnInstallAtStageFailed(t *testing.T) {
	f := newRefusedInstall(t, false)
	if _, err := f.evidence(); err != nil {
		t.Fatalf("the refused install is not admitted: %v", err)
	}
	mustExec(t, f.s.st, `UPDATE app_platform_operations SET stage='failed' WHERE action='install'`)
	_, err := f.evidence()
	refusedBecause(t, err, "the install did not end in recovery_required")
	if _, err := f.review(appplatform.ActionUninstall); !errors.Is(err, errAppPlatformPlanChanged) {
		t.Fatalf("the review admitted a failed install: %v", err)
	}
	// And the close itself refuses everything but recovery_required.
	if err := closePlatformNeverStartedInstall(migExec{s: f.s.st, ex: f.s.st.db}, f.install, platformFixtureID(7300), 1); !errors.Is(err, errAppPlatformStageEvidence) {
		t.Fatalf("the close accepted an install at stage failed: %v", err)
	}
}

// Item 2: every guard of the eligibility refuses for its own reason.
func TestNeverStartedEvidenceGuardsRefuseForTheirOwnReason(t *testing.T) {
	for _, tc := range []struct{ name, statement, reason string }{
		{"install_generation_source", `UPDATE app_platform_operations SET source_generation=1,target_generation=2 WHERE action='install'`, "the install is not the first-generation install"},
		{"install_generation_target", `UPDATE app_platform_operations SET target_generation=2 WHERE action='install'`, "the install is not the first-generation install"},
		{"active_activation", `UPDATE app_platform_activations SET state='active'`, "an activation is active or past the first generation"},
		{"activation_past_first_generation", `UPDATE app_platform_activations SET generation=2`, "an activation is active or past the first generation"},
		{"receipt_names_another_namespace", `UPDATE app_platform_operation_effects SET receipt_json=REPLACE(receipt_json,(SELECT id FROM app_platform_data_namespaces LIMIT 1),'` + platformFixtureID(7310) + `') WHERE kind='prepare-data'`, "the prepare_data receipt names another namespace"},
		{"namespace_not_active", `UPDATE app_platform_data_namespaces SET state='review_required'`, "the namespace is not active and owned by the installation"},
		{"unsettled_load_image", ``, "an effect of the install is unsettled"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newRefusedInstall(t, false)
			if tc.statement == "" {
				f.addImageEffect(t, "unknown")
			} else {
				mustExec(t, f.s.st, tc.statement)
			}
			_, err := f.evidence()
			refusedBecause(t, err, tc.reason)
		})
	}
	t.Run("second_namespace", func(t *testing.T) {
		f := newRefusedInstall(t, false)
		mustExec(t, f.s.st, `INSERT INTO app_platform_data_namespaces(id,origin_installation_id,owner_installation_id,state,data_format,quota_bytes,observed_bytes,writer_fence,revision,retained_ms,deleted_ms,created_ms,updated_ms)
			VALUES(?,?,NULL,'deleted',1,268435456,0,1,3,NULL,1,1,1)`, platformFixtureID(7320), f.installation)
		_, err := f.evidence()
		refusedBecause(t, err, "the installation has another namespace")
	})
}

func (f refusedInstall) addImageEffect(t *testing.T, state string) {
	t.Helper()
	mustExec(t, f.s.st, `INSERT INTO app_platform_operation_effects(operation_id,stage,effect_number,effect_id,kind,payload_sha256,runtime_id,controller_epoch,admitted_fence,observed_fence,state,receipt_json,created_ms,updated_ms)
		SELECT operation_id,'stage',9,?,'load_image',payload_sha256,runtime_id,controller_epoch,admitted_fence,0,?,'{}',created_ms,updated_ms FROM app_platform_operation_effects WHERE kind='prepare-data'`, platformFixtureID(7330), state)
}

// Item 3: the effect cannot have been admitted at a newer fence than the install operation holds. The delete-data
// intent compares them too, so a change after the review is caught where the command is built.
func TestNeverStartedFenceIsComparedWithTheInstallOperation(t *testing.T) {
	f := newRefusedInstall(t, false)
	mustExec(t, f.s.st, `UPDATE app_platform_operation_effects SET admitted_fence=(SELECT fence FROM app_platform_operations WHERE action='install')+5 WHERE kind='prepare-data'`)
	_, err := f.evidence()
	refusedBecause(t, err, "the prepare_data effect was admitted at a newer fence than the install")

	g := newRefusedInstall(t, false)
	g.uninstall(t, platformFixtureID(7340))
	lease := g.claimAtStop(t, platformFixtureID(7341))
	var aerr error
	for range 2 {
		if lease, aerr = g.s.advancePlatformNeverStartedRemoval(context.Background(), lease); aerr != nil {
			t.Fatal(aerr)
		}
	}
	// The install operation's fence moves below the effect's after the review was consumed.
	mustExec(t, g.s.st, `UPDATE app_platform_operations SET fence=fence-1 WHERE action='install'`)
	if _, err := g.s.preparePlatformDeleteDataIntent(context.Background(), lease); !errors.Is(err, errAppPlatformStageEvidence) {
		t.Fatalf("the delete-data intent was built over a fence mismatch: %v", err)
	}
	if g.effects(t, lease.id, `kind='delete-data'`) != 0 {
		t.Fatal("a delete-data effect was written over a fence mismatch")
	}
}

// Item 4: an unsettled load_image effect blocks the variant and the close the same way; settled ones do not.
func TestNeverStartedLoadImageEffectsBlockOnlyWhenUnsettled(t *testing.T) {
	for _, state := range []string{"prepared", "unknown"} {
		f := newRefusedInstall(t, false)
		f.addImageEffect(t, state)
		_, err := f.evidence()
		refusedBecause(t, err, "an effect of the install is unsettled")
		if err := closePlatformNeverStartedInstall(migExec{s: f.s.st, ex: f.s.st.db}, f.install, platformFixtureID(7350), 1); !errors.Is(err, errAppPlatformStageEvidence) {
			t.Fatalf("the close accepted a %s load: %v", state, err)
		}
		if scalar[string](t, f.s.st, `SELECT stage FROM app_platform_operations WHERE id=?`, f.install) != "recovery_required" {
			t.Fatal("the close changed an install it refused")
		}
	}
	for _, state := range []string{"applied", "reverted"} {
		f := newRefusedInstall(t, false)
		f.addImageEffect(t, state)
		if _, err := f.evidence(); err != nil {
			t.Fatalf("a %s load blocked the variant: %v", state, err)
		}
		if err := closePlatformNeverStartedInstall(migExec{s: f.s.st, ex: f.s.st.db}, f.install, platformFixtureID(7351), 1); err != nil {
			t.Fatalf("a %s load blocked the close: %v", state, err)
		}
	}
}

// Item 5: a started install in review_required or recovery_required keeps the error type it always had, whichever
// of its states stops the review first.
func TestStartedInstallInReviewPhasesKeepsThePlanChangedError(t *testing.T) {
	for _, phase := range []string{"review_required", "recovery_required"} {
		for _, tc := range []struct{ name, statement string }{
			{"plain", ``},
			{"pending_operation", `INSERT INTO app_platform_operations(id,installation_id,review_plan_id,actor_scope,idempotency_sha256,request_sha256,action,managed,source_generation,target_generation,stage,state_revision,row_revision,fence,controller_epoch,created_ms,updated_ms)
				SELECT '` + platformFixtureID(7360) + `',installation_id,(SELECT id FROM app_platform_review_plans LIMIT 1),actor_scope,'` + strings.Repeat("3", 64) + `','` + strings.Repeat("4", 64) + `','disable',managed,1,2,'queued',1,1,fence,controller_epoch,created_ms+1,updated_ms FROM app_platform_operations WHERE action='install'`},
			{"namespace_retained", `UPDATE app_platform_data_namespaces SET state='retained'`},
			{"namespace_detached", `UPDATE app_platform_data_namespaces SET state='deleted',owner_installation_id=NULL,deleted_ms=1`},
		} {
			t.Run(phase+"/"+tc.name, func(t *testing.T) {
				s, actor, lease := platformWritableAccessFixture(t, false)
				mustExec(t, s.st, `UPDATE app_platform_installations SET phase=? WHERE id=?`, phase, lease.installationID)
				if tc.statement != "" {
					if tc.name == "pending_operation" {
						mustExec(t, s.st, `INSERT INTO app_platform_review_plans(id,installation_id,app_id,publisher_id,action,source_release_sha256,target_release_sha256,installation_revision,policy_revision,trust_revision,controller_epoch,actor_scope,username,account_created,account_generation,authority_fingerprint,plan_sha256,plan_json,expires_ms,consumed_ms,created_ms,updated_ms)
							SELECT '`+platformFixtureID(7361)+`',installation_id,app_id,publisher_id,action,source_release_sha256,target_release_sha256,installation_revision,policy_revision,trust_revision,controller_epoch,actor_scope,username,account_created,account_generation,authority_fingerprint,plan_sha256,plan_json,expires_ms,consumed_ms,created_ms,updated_ms
							FROM app_platform_review_plans LIMIT 1`)
						tc.statement = strings.Replace(tc.statement, "(SELECT id FROM app_platform_review_plans LIMIT 1)", "'"+platformFixtureID(7361)+"'", 1)
					}
					mustExec(t, s.st, tc.statement)
				}
				_, err := s.createPlatformRemovalReview(actor, lease.installationID, appplatform.ActionUninstall)
				if !errors.Is(err, errAppPlatformPlanChanged) || errors.Is(err, errAppPlatformOperationConflict) {
					t.Fatalf("a started install in %s (%s) was refused with %v", phase, tc.name, err)
				}
			})
		}
	}
}
