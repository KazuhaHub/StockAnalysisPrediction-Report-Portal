package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

// Abandoning the replacement runtime (doc 168, owner decision).
//
// An import or resume whose effect stays unknown wedges recovery: the grant stays issued, the
// installation cannot be imported or resumed again, and recovery cannot end. When the owner decides the
// replacement host is to be discarded, the operator attests it, and this one reviewed action settles
// everything that rested on it without asking it anything:
//
//   - the replacement runtime binding is retired (enabled=0, no key), which is the state a restore
//     leaves old bindings in, so a new replacement with a fresh journal and deployment can be enrolled;
//   - every grant still issued on it is revoked;
//   - every unsettled import and resume effect on it is marked reverted, with the reason recorded and
//     no native action: the host is gone, and a late receipt from it is refused because the runtime is
//     retired, the grant revoked and the effect settled;
//   - the operations still in flight on it are ended;
//   - every installation placed on it returns to awaiting review: disabled, with the recovery set and
//     the data namespace awaiting review again and no activation, ready for a new import.
//
// It is admitted only while the Portal is paused. The review lists exactly what will change, and the
// commit refuses if any of it changed since.

const (
	platformAbandonRuntimeFormat = "portal.recovery-abandon-runtime-review/1-dev.1"
	platformAbandonReason        = "runtime abandoned"
)

var errAppPlatformAbandonUnavailable = errors.New("application recovery has no replacement runtime to abandon")

type platformAbandonRuntimePlan struct {
	Format          string   `json:"format"`
	RuntimeID       string   `json:"runtime_id"`
	DeploymentID    string   `json:"deployment_id"`
	RuntimeRevision int64    `json:"runtime_revision"`
	ControllerEpoch string   `json:"controller_epoch"`
	Installations   []string `json:"installations"`
	Grants          []string `json:"grants"`
	Effects         []string `json:"effects"`
	Operations      []string `json:"operations"`
	Reason          string   `json:"reason"`
}

func platformPlanIsAbandonRuntime(planRaw string) bool {
	return strings.HasPrefix(planRaw, `{"format":"`+platformAbandonRuntimeFormat+`"`)
}

func platformIDList(e migExec, query string, args ...any) ([]string, error) {
	rows, err := e.query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		result = append(result, id)
	}
	return result, rows.Err()
}

func loadPlatformAbandonRuntimePlan(e migExec, runtimeID string, control platformActorControl) (platformAbandonRuntimePlan, error) {
	var p platformAbandonRuntimePlan
	runtime, found, _, err := platformActiveRuntime(e, true)
	if err != nil {
		return p, err
	}
	if !found || runtime.enabled != 1 || !runtime.sealed.Valid || !runtime.pin.Valid || runtime.id != runtimeID || runtime.controllerEpoch != control.epoch {
		return p, errAppPlatformAbandonUnavailable
	}
	p.Format, p.RuntimeID, p.DeploymentID, p.RuntimeRevision, p.ControllerEpoch, p.Reason =
		platformAbandonRuntimeFormat, runtime.id, runtime.deploymentID, runtime.revision, runtime.controllerEpoch, platformAbandonReason
	if p.Installations, err = platformIDList(e, `SELECT id FROM app_platform_installations WHERE runtime_id=? AND desired_state<>'removed' ORDER BY id`, runtime.id); err != nil {
		return p, err
	}
	if p.Grants, err = platformIDList(e, `SELECT id FROM app_platform_import_grants WHERE runtime_id=? AND state='issued' ORDER BY id`, runtime.id); err != nil {
		return p, err
	}
	if p.Effects, err = platformIDList(e, `SELECT f.effect_id FROM app_platform_operation_effects f JOIN app_platform_operations o ON o.id=f.operation_id
		WHERE f.runtime_id=? AND o.action IN ('restore_import','restore') AND f.state IN ('prepared','unknown') ORDER BY f.effect_id`, runtime.id); err != nil {
		return p, err
	}
	if p.Operations, err = platformIDList(e, `SELECT o.id FROM app_platform_operations o JOIN app_platform_installations i ON i.id=o.installation_id
		WHERE i.runtime_id=? AND o.action IN ('restore_import','restore') AND o.completed_ms IS NULL ORDER BY o.id`, runtime.id); err != nil {
		return p, err
	}
	return p, nil
}

func (s *Server) createPlatformAbandonRuntimeReview(ctx context.Context, session platformActorSession, runtimeID string) (platformRemovalReview, error) {
	if !platformValidUUID(runtimeID) {
		return platformRemovalReview{}, errAppPlatformPlanChanged
	}
	var result platformRemovalReview
	err := s.withPlatformRecoveryTransactionContext(ctx, session, func(e migExec, actor platformActorFacts, control platformActorControl) error {
		plan, err := loadPlatformAbandonRuntimePlan(e, runtimeID, control)
		if err != nil {
			return err
		}
		publisher, trust, err := platformAbandonReviewPublisher(e)
		if err != nil {
			return err
		}
		control, err = platformRefreshActor(e, session, actor, control)
		if err != nil {
			return err
		}
		raw, err := json.Marshal(plan)
		if err != nil || len(raw) > 65536 || control.nowMS > math.MaxInt64-900000 {
			return errAppPlatformPlanChanged
		}
		id, err := platformNewUUID()
		if err != nil {
			return err
		}
		digest, expires := platformDigest(raw), control.nowMS+900000
		// The plan row's columns are installation-shaped; a runtime-level review fills them with the
		// first publisher and a fixed application name, and carries no installation or release. The plan
		// reuses the stored action of a disable, and its format tells it apart, as every recovery review does.
		if _, err := e.exec(`INSERT INTO app_platform_review_plans(id,installation_id,app_id,publisher_id,action,source_release_sha256,target_release_sha256,
			installation_revision,policy_revision,trust_revision,controller_epoch,actor_scope,username,account_created,account_generation,authority_fingerprint,
			plan_sha256,plan_json,expires_ms,consumed_ms,created_ms,updated_ms)
			VALUES(?,NULL,'recovery',?,'disable',NULL,NULL,0,0,?,?,?,?,?,?,?,?,?,?,NULL,?,?)`,
			id, publisher, trust, control.epoch, actor.scope, actor.username, actor.created, actor.generation, actor.authorityFingerprint,
			digest, string(raw), expires, control.nowMS, control.nowMS); err != nil {
			return err
		}
		result = platformRemovalReview{ID: id, SHA256: digest, JSON: string(raw), ExpiresMS: expires}
		_, err = platformRefreshActor(e, session, actor, control)
		return err
	})
	if err != nil {
		return platformRemovalReview{}, err
	}
	return result, nil
}

func platformAbandonReviewPublisher(e migExec) (string, int64, error) {
	var id string
	var revision int64
	if err := e.queryRow(`SELECT id,revision FROM app_platform_publishers ORDER BY id LIMIT 1`).Scan(&id, &revision); err != nil || revision < 1 {
		return "", 0, errAppPlatformAbandonUnavailable
	}
	return id, revision, nil
}

type platformAbandonResult struct {
	RuntimeID                                  string
	Installations, Grants, Effects, Operations int
}

func (s *Server) consumePlatformAbandonRuntimeReview(ctx context.Context, session platformActorSession, planID, planSHA string) (platformAbandonResult, error) {
	var result platformAbandonResult
	if !platformValidUUID(planID) || !platformValidSHA(planSHA) {
		return result, errAppPlatformPlanChanged
	}
	err := s.withPlatformRecoveryTransactionContext(ctx, session, func(e migExec, actor platformActorFacts, control platformActorControl) error {
		lock := ""
		if e.s.driver == "postgres" {
			lock = " FOR UPDATE"
		}
		var raw, digest, scope, username, created, authority, action, epoch string
		var installation sql.NullString
		var expires, accountGeneration int64
		var consumed sql.NullInt64
		if err := e.queryRow(`SELECT plan_json,plan_sha256,actor_scope,username,account_created,account_generation,authority_fingerprint,expires_ms,consumed_ms,
			controller_epoch,action,installation_id FROM app_platform_review_plans WHERE id=?`+lock, planID).
			Scan(&raw, &digest, &scope, &username, &created, &accountGeneration, &authority, &expires, &consumed, &epoch, &action, &installation); err != nil {
			return errAppPlatformPlanChanged
		}
		if scope != actor.scope {
			return errAppPlatformActorUnavailable
		}
		if consumed.Valid {
			return errAppPlatformOperationConflict
		}
		control, err := platformRefreshActor(e, session, actor, control)
		if err != nil {
			return err
		}
		if expires <= control.nowMS {
			return errAppPlatformPlanExpired
		}
		if installation.Valid || action != "disable" || !platformPlanIsAbandonRuntime(raw) || username != actor.username || created != actor.created ||
			accountGeneration != actor.generation || authority != actor.authorityFingerprint || epoch != control.epoch || digest != planSHA || platformDigest([]byte(raw)) != planSHA {
			return errAppPlatformPlanChanged
		}
		var plan platformAbandonRuntimePlan
		if json.Unmarshal([]byte(raw), &plan) != nil {
			return errAppPlatformPlanChanged
		}
		fresh, err := loadPlatformAbandonRuntimePlan(e, plan.RuntimeID, control)
		if err != nil {
			return errAppPlatformPlanChanged
		}
		if encoded, err := json.Marshal(fresh); err != nil || string(encoded) != raw {
			return errAppPlatformPlanChanged
		}
		now := control.nowMS
		changed, err := e.exec(`UPDATE app_platform_review_plans SET consumed_ms=?,updated_ms=? WHERE id=? AND consumed_ms IS NULL AND expires_ms>? AND plan_sha256=?`, now, now, planID, now, planSHA)
		if err := platformLeaseCAS(changed, err); err != nil {
			return err
		}
		if err := applyPlatformAbandonRuntime(e, plan, now); err != nil {
			return err
		}
		detail, _ := json.Marshal(struct {
			Plan          string   `json:"plan_id"`
			RuntimeID     string   `json:"runtime_id"`
			DeploymentID  string   `json:"deployment_id"`
			Installations []string `json:"installations"`
			Grants        []string `json:"grants"`
			Effects       []string `json:"effects"`
			Operations    []string `json:"operations"`
			Reason        string   `json:"reason"`
		}{planID, plan.RuntimeID, plan.DeploymentID, plan.Installations, plan.Grants, plan.Effects, plan.Operations, plan.Reason})
		if _, err := e.exec(`INSERT INTO audit_log(at,actor,actor_ou,action,target_type,target_id,detail,ip) VALUES(?,?,?,'app_platform.runtime_abandoned','app_platform_runtime',?,?,'')`,
			time.UnixMilli(now).UTC().Format(time.RFC3339), actor.username, actor.ouID, plan.RuntimeID, string(detail)); err != nil {
			return err
		}
		result = platformAbandonResult{RuntimeID: plan.RuntimeID, Installations: len(plan.Installations), Grants: len(plan.Grants), Effects: len(plan.Effects), Operations: len(plan.Operations)}
		_, err = platformRefreshActor(e, session, actor, control)
		return err
	})
	if err != nil {
		return platformAbandonResult{}, err
	}
	return result, nil
}

func applyPlatformAbandonRuntime(e migExec, plan platformAbandonRuntimePlan, now int64) error {
	// Retire the binding first: every settlement below is then final, because nothing can settle on it.
	changed, err := e.exec(`UPDATE app_platform_runtime_bindings SET enabled=0,sealed_control_key=NULL,verification_key_fingerprint=NULL,
		control_revision=control_revision+1,revision=revision+1,updated_ms=? WHERE id=? AND enabled=1 AND revision=? AND controller_epoch=?`,
		now, plan.RuntimeID, plan.RuntimeRevision, plan.ControllerEpoch)
	if err := platformLeaseCAS(changed, err); err != nil {
		return err
	}
	if _, err := e.exec(`UPDATE app_platform_import_grants SET state='revoked',updated_ms=? WHERE runtime_id=? AND state='issued'`, now, plan.RuntimeID); err != nil {
		return err
	}
	reason, _ := json.Marshal(struct {
		Reason string `json:"reason"`
	}{platformAbandonReason})
	for _, effect := range plan.Effects {
		changed, err := e.exec(`UPDATE app_platform_operation_effects SET state='reverted',receipt_json=?,updated_ms=? WHERE effect_id=? AND runtime_id=? AND state IN ('prepared','unknown')`,
			string(reason), now, effect, plan.RuntimeID)
		if err := platformLeaseCAS(changed, err); err != nil {
			return err
		}
	}
	for _, id := range plan.Operations {
		var action, stage string
		var managed int
		var generation, revision int64
		if err := e.queryRow(`SELECT action,managed,target_generation,state_revision,stage FROM app_platform_operations WHERE id=? AND completed_ms IS NULL`, id).
			Scan(&action, &managed, &generation, &revision, &stage); err != nil {
			return errAppPlatformPlanChanged
		}
		model, err := appplatform.OperationFromSnapshot(appplatform.OperationSnapshot{Action: appplatform.Action(action), Managed: managed == 1,
			Generation: uint64(generation), Revision: uint64(revision), Stage: appplatform.Stage(stage)})
		if err != nil {
			return errAppPlatformStageEvidence
		}
		ended, err := model.Fail(model.Snapshot().Revision, appplatform.EffectsNone)
		if err != nil {
			return errAppPlatformStageEvidence
		}
		changed, err := e.exec(`UPDATE app_platform_operations SET stage=?,state_revision=?,row_revision=row_revision+1,error_code='runtime_abandoned',completed_ms=?,updated_ms=?,lease_owner=NULL,lease_until_ms=NULL
			WHERE id=? AND completed_ms IS NULL`, string(ended.Snapshot().Stage), int64(ended.Snapshot().Revision), now, now, id)
		if err := platformLeaseCAS(changed, err); err != nil {
			return err
		}
	}
	for _, installation := range plan.Installations {
		if _, err := e.exec(`UPDATE app_platform_installations SET desired_state='disabled',phase='review_required',generation=generation+1,revision=revision+1,
			policy_revision=policy_revision+1,control_fence=control_fence+1,updated_ms=? WHERE id=? AND runtime_id=? AND desired_state<>'removed'`,
			now, installation, plan.RuntimeID); err != nil {
			return err
		}
		for _, q := range []string{
			`UPDATE app_platform_activations SET state='retired',updated_ms=? WHERE installation_id=? AND state='active'`,
			`UPDATE app_platform_data_namespaces SET state='review_required',owner_installation_id=NULL,writer_fence=writer_fence+1,revision=revision+1,updated_ms=?
				WHERE origin_installation_id=? AND state NOT IN ('deleted','deleting')`,
			`UPDATE app_platform_recovery_sets SET state='review_required',updated_ms=? WHERE installation_id=? AND state NOT IN ('deleted','review_required')`,
		} {
			if _, err := e.exec(q, now, installation); err != nil {
				return err
			}
		}
		if _, err := e.exec(`DELETE FROM app_platform_activations WHERE installation_id=? AND state='candidate'`, installation); err != nil {
			return err
		}
	}
	return nil
}
