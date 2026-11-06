package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"math"
	"strings"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

// The two recovery actions that need no runner (doc 168, step 9).
//
//   - recovery_disable settles any restored installation by disabling it. It is the way out for an
//     installation that has no recovery set to import, one whose import the administrator gives up on,
//     and one whose publisher was not trusted before the restore.
//   - frontend_resume settles a restored static frontend app by activating its restored release again
//     on the current epoch. A static app has no runner, no data and no recovery set, so there is
//     nothing to import and nothing to start.
//
// Both are reviewed on the ordinary plans route and committed on the ordinary operations route. Each
// one is a single database transition, so the commit performs it and records an operation that was
// admitted already complete; there are no stages that would carry no work. A review plan reuses the
// stored action of the ordinary disable or enable it resembles, and its format tells the two apart,
// as a resume does for restore.

const (
	platformRecoveryDisableFormat = "portal.recovery-disable-review/1-dev.1"
	platformFrontendResumeFormat  = "portal.recovery-frontend-resume-review/1-dev.1"
)

type platformRecoveryDisablePlan struct {
	Format               string `json:"format"`
	InstallationID       string `json:"installation_id"`
	AppID                string `json:"app_id"`
	PublisherID          string `json:"publisher_id"`
	RuntimeID            string `json:"runtime_id"`
	SourceGeneration     int64  `json:"source_generation"`
	TargetGeneration     int64  `json:"target_generation"`
	InstallationRevision int64  `json:"installation_revision"`
	PolicyRevision       int64  `json:"policy_revision"`
	ControllerEpoch      string `json:"controller_epoch"`
	DataEffect           string `json:"data_effect"`
}

func platformPlanIsRecoveryDisable(planRaw string) bool {
	return strings.HasPrefix(planRaw, `{"format":"`+platformRecoveryDisableFormat+`"`)
}

func platformPlanIsFrontendResume(planRaw string) bool {
	return strings.HasPrefix(planRaw, `{"format":"`+platformFrontendResumeFormat+`"`)
}

func loadPlatformRecoveryDisablePlan(e migExec, installation string, control platformActorControl) (platformRecoveryDisablePlan, error) {
	var p platformRecoveryDisablePlan
	if !platformValidUUID(installation) {
		return p, errAppPlatformPlanChanged
	}
	lock := ""
	if e.s.driver == "postgres" {
		lock = " FOR UPDATE"
	}
	var runtime sql.NullString
	var desired, phase string
	if err := e.queryRow(`SELECT app_id,publisher_id,desired_state,phase,generation,revision,policy_revision,controller_epoch,runtime_id
		FROM app_platform_installations WHERE id=?`+lock, installation).
		Scan(&p.AppID, &p.PublisherID, &desired, &phase, &p.SourceGeneration, &p.InstallationRevision, &p.PolicyRevision, &p.ControllerEpoch, &runtime); err != nil {
		return p, errAppPlatformPlanChanged
	}
	if desired != "disabled" || phase != "review_required" || p.ControllerEpoch != control.epoch ||
		p.SourceGeneration < 1 || p.SourceGeneration == math.MaxInt64 || p.InstallationRevision == math.MaxInt64 {
		return p, errAppPlatformPlanChanged
	}
	var pending, unsettledImport int
	if err := e.queryRow(`SELECT COUNT(*) FROM app_platform_operations WHERE installation_id=? AND completed_ms IS NULL`, installation).Scan(&pending); err != nil {
		return p, err
	}
	if pending != 0 {
		return p, errAppPlatformOperationConflict
	}
	// An import whose effect the runner has not settled may have placed data on the replacement host.
	// Giving the installation up while that is unknown would orphan it, so it is refused until settled.
	if err := e.queryRow(`SELECT COUNT(*) FROM app_platform_operation_effects f JOIN app_platform_operations o ON o.id=f.operation_id
		WHERE o.installation_id=? AND o.action='restore_import' AND o.controller_epoch=? AND f.state IN ('prepared','unknown')`,
		installation, control.epoch).Scan(&unsettledImport); err != nil {
		return p, err
	}
	if unsettledImport != 0 {
		return p, errAppPlatformOperationConflict
	}
	p.Format, p.InstallationID, p.RuntimeID = platformRecoveryDisableFormat, installation, runtime.String
	p.TargetGeneration, p.DataEffect = p.SourceGeneration+1, "preserve"
	return p, nil
}

func platformRecoveryPublisherRevision(e migExec, publisher string) (int64, error) {
	var revision int64
	if err := e.queryRow(`SELECT revision FROM app_platform_publishers WHERE id=?`, publisher).Scan(&revision); err != nil || revision < 1 {
		return 0, errAppPlatformPlanChanged
	}
	return revision, nil
}

// platformRecoveryKind describes one runner-free recovery action: how its plan is rebuilt from the
// database, and the one transition its commit performs.
type platformRecoveryKind struct {
	action     appplatform.Action
	format     string
	audit      string
	fresh      func(e migExec, installation string, actor platformActorFacts, control platformActorControl) (platformRecoveryPlanFacts, error)
	transition func(e migExec, planID, planSHA string, facts platformRecoveryPlanFacts, now int64) error
}

type platformRecoveryPlanFacts struct {
	json                                       string
	appID, publisherID                         string
	release                                    string
	installationRevision, policyRevision       int64
	trustRevision, targetGeneration, sourceGen int64
	controllerEpoch                            string
	// managed records whether the installation ran on a runtime, which the operation row keeps.
	managed bool
}

var platformRecoveryDisableKind = platformRecoveryKind{
	action: appplatform.ActionDisable, format: platformRecoveryDisableFormat, audit: "app_platform.recovery_disabled",
	fresh: func(e migExec, installation string, _ platformActorFacts, control platformActorControl) (platformRecoveryPlanFacts, error) {
		p, err := loadPlatformRecoveryDisablePlan(e, installation, control)
		if err != nil {
			return platformRecoveryPlanFacts{}, err
		}
		raw, err := json.Marshal(p)
		if err != nil || len(raw) > 65536 {
			return platformRecoveryPlanFacts{}, errAppPlatformPlanChanged
		}
		trust, err := platformRecoveryPublisherRevision(e, p.PublisherID)
		if err != nil {
			return platformRecoveryPlanFacts{}, err
		}
		return platformRecoveryPlanFacts{json: string(raw), appID: p.AppID, publisherID: p.PublisherID,
			installationRevision: p.InstallationRevision, policyRevision: p.PolicyRevision, trustRevision: trust,
			targetGeneration: p.TargetGeneration, sourceGen: p.SourceGeneration, controllerEpoch: p.ControllerEpoch, managed: p.RuntimeID != ""}, nil
	},
	transition: func(e migExec, planID, planSHA string, facts platformRecoveryPlanFacts, now int64) error {
		var p platformRecoveryDisablePlan
		if json.Unmarshal([]byte(facts.json), &p) != nil {
			return errAppPlatformPlanChanged
		}
		changed, err := e.exec(`UPDATE app_platform_installations SET phase='disabled',generation=?,revision=revision+1,control_fence=control_fence+1,updated_ms=?
			WHERE id=? AND desired_state='disabled' AND phase='review_required' AND generation=? AND revision=? AND policy_revision=? AND controller_epoch=?`,
			p.TargetGeneration, now, p.InstallationID, p.SourceGeneration, p.InstallationRevision, p.PolicyRevision, p.ControllerEpoch)
		return platformLeaseCAS(changed, err)
	},
}

var platformFrontendResumeKind = platformRecoveryKind{
	action: appplatform.ActionEnable, format: platformFrontendResumeFormat, audit: "app_platform.frontend_resumed",
	fresh: func(e migExec, installation string, actor platformActorFacts, control platformActorControl) (platformRecoveryPlanFacts, error) {
		p, err := platformLoadFrontendEnablePlanMode(e, installation, appplatform.ActionEnable, actor, control, true)
		if err != nil {
			return platformRecoveryPlanFacts{}, err
		}
		raw, err := json.Marshal(p)
		if err != nil || len(raw) > 65536 {
			return platformRecoveryPlanFacts{}, errAppPlatformPlanChanged
		}
		return platformRecoveryPlanFacts{json: string(raw), appID: p.AppID, publisherID: p.PublisherID, release: p.SourceReleaseSHA256,
			installationRevision: p.InstallationRevision, policyRevision: p.SourcePolicyRevision, trustRevision: p.TrustRevision,
			targetGeneration: p.TargetGeneration, sourceGen: p.SourceGeneration, controllerEpoch: p.ControllerEpoch}, nil
	},
	transition: func(e migExec, planID, planSHA string, facts platformRecoveryPlanFacts, now int64) error {
		var p platformFrontendEnablePlan
		if json.Unmarshal([]byte(facts.json), &p) != nil {
			return errAppPlatformPlanChanged
		}
		evidence := platformFrontendEvidence(platformStaticCandidate{
			frontendOnly: true, reviewSHA: planSHA, declaredSchema: int(p.DeclaredDataSchema),
			releaseSHA: p.SourceReleaseSHA256, frontendSHA: p.FrontendSHA256, documentSHA: p.DocumentSHA256,
		}, p.ControllerEpoch, p.InstallationID, p.AudienceSHA256, p.TargetGeneration, p.TargetPolicyRevision)
		if len(evidence) > 8192 {
			return errAppPlatformPlanChanged
		}
		activation, err := platformNewUUID()
		if err != nil {
			return err
		}
		if _, err := e.exec(`INSERT INTO app_platform_activations(id,installation_id,release_sha256,generation,policy_revision,data_namespace_id,data_format,state,readiness_evidence,observed_ms,created_ms,updated_ms)
			VALUES(?,?,?,?,?,NULL,0,'active',?,?,?,?)`,
			activation, p.InstallationID, p.SourceReleaseSHA256, p.TargetGeneration, p.TargetPolicyRevision, string(evidence), now, now, now); err != nil {
			return err
		}
		// The restore raised the installation's policy revision past the capability rows the review
		// listed. The administrator approved resuming with exactly those rows, so they are carried to the
		// resumed policy revision; launch accepts only rows at the installation's own revision.
		if _, err := e.exec(`UPDATE app_platform_capability_grants SET policy_revision=?,updated_ms=? WHERE installation_id=?`,
			p.TargetPolicyRevision, now, p.InstallationID); err != nil {
			return err
		}
		phase, err := platformReadyPhaseForInstallation(e, p.InstallationID, p.TargetPolicyRevision)
		if err != nil {
			return err
		}
		changed, err := e.exec(`UPDATE app_platform_installations SET desired_state='enabled',phase=?,generation=?,revision=revision+1,control_fence=control_fence+1,updated_ms=?
			WHERE id=? AND desired_state='disabled' AND phase='review_required' AND generation=? AND revision=? AND policy_revision=? AND controller_epoch=? AND runtime_id IS NULL`,
			phase, p.TargetGeneration, now, p.InstallationID, p.SourceGeneration, p.InstallationRevision, p.SourcePolicyRevision, p.ControllerEpoch)
		return platformLeaseCAS(changed, err)
	},
}

func (s *Server) createPlatformRecoveryReview(ctx context.Context, session platformActorSession, kind platformRecoveryKind, installation string) (platformRemovalReview, error) {
	if !platformValidUUID(installation) {
		return platformRemovalReview{}, errAppPlatformPlanChanged
	}
	var result platformRemovalReview
	err := s.withPlatformRecoveryTransactionContext(ctx, session, func(e migExec, actor platformActorFacts, control platformActorControl) error {
		facts, err := kind.fresh(e, installation, actor, control)
		if err != nil {
			return err
		}
		control, err = platformRefreshActor(e, session, actor, control)
		if err != nil {
			return err
		}
		if control.nowMS > math.MaxInt64-900000 {
			return errAppPlatformPlanChanged
		}
		id, err := platformNewUUID()
		if err != nil {
			return err
		}
		digest, expires := platformDigest([]byte(facts.json)), control.nowMS+900000
		var release any
		if facts.release != "" {
			release = facts.release
		}
		if _, err := e.exec(`INSERT INTO app_platform_review_plans(id,installation_id,app_id,publisher_id,action,source_release_sha256,target_release_sha256,
			installation_revision,policy_revision,trust_revision,controller_epoch,actor_scope,username,account_created,account_generation,authority_fingerprint,
			plan_sha256,plan_json,expires_ms,consumed_ms,created_ms,updated_ms)
			VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,NULL,?,?)`,
			id, installation, facts.appID, facts.publisherID, string(kind.action), release, release,
			facts.installationRevision, facts.policyRevision, facts.trustRevision, control.epoch, actor.scope, actor.username,
			actor.created, actor.generation, actor.authorityFingerprint, digest, facts.json, expires, control.nowMS, control.nowMS); err != nil {
			return err
		}
		result = platformRemovalReview{ID: id, SHA256: digest, JSON: facts.json, ExpiresMS: expires}
		_, err = platformRefreshActor(e, session, actor, control)
		return err
	})
	if err != nil {
		return platformRemovalReview{}, err
	}
	return result, nil
}

func (s *Server) consumePlatformRecoveryReview(ctx context.Context, session platformActorSession, kind platformRecoveryKind, planID, planSHA, key string) (platformRemovalCommit, error) {
	var result platformRemovalCommit
	if !platformValidUUID(planID) || !platformValidSHA(planSHA) || !platformValidUUID(key) {
		return result, errAppPlatformPlanChanged
	}
	request, _ := json.Marshal([]string{"portal-operation-commit/v1", planID, planSHA})
	requestDigest, keyDigest := platformDigest(request), platformIdempotencySHA(key)
	err := s.withPlatformRecoveryTransactionContext(ctx, session, func(e migExec, actor platformActorFacts, control platformActorControl) error {
		previous, exists, err := platformFindRemovalCommit(e, actor.scope, keyDigest, requestDigest)
		if err != nil {
			return err
		}
		if exists {
			var n int
			if err := e.queryRow(`SELECT COUNT(*) FROM app_platform_operations o JOIN app_platform_review_plans p ON p.id=o.review_plan_id
				WHERE o.id=? AND p.plan_json LIKE ?`, previous.OperationID, `{"format":"`+kind.format+`"%`).Scan(&n); err != nil || n != 1 {
				return errAppPlatformOperationConflict
			}
			result = previous
			return nil
		}
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
		control, err = platformRefreshActor(e, session, actor, control)
		if err != nil {
			return err
		}
		if expires <= control.nowMS {
			return errAppPlatformPlanExpired
		}
		if !installation.Valid || action != string(kind.action) || !strings.HasPrefix(raw, `{"format":"`+kind.format+`"`) ||
			username != actor.username || created != actor.created || accountGeneration != actor.generation || authority != actor.authorityFingerprint ||
			epoch != control.epoch || digest != planSHA || platformDigest([]byte(raw)) != planSHA {
			return errAppPlatformPlanChanged
		}
		facts, err := kind.fresh(e, installation.String, actor, control)
		if err != nil || facts.json != raw || facts.controllerEpoch != epoch {
			return errAppPlatformPlanChanged
		}
		now := control.nowMS
		changed, err := e.exec(`UPDATE app_platform_review_plans SET consumed_ms=?,updated_ms=? WHERE id=? AND consumed_ms IS NULL AND expires_ms>? AND plan_sha256=?`,
			now, now, planID, now, planSHA)
		if err := platformLeaseCAS(changed, err); err != nil {
			return err
		}
		if err := kind.transition(e, planID, planSHA, facts, now); err != nil {
			return err
		}
		snapshot, err := appplatform.CompletedOperation(kind.action, facts.managed, uint64(facts.targetGeneration))
		if err != nil {
			return errAppPlatformPlanChanged
		}
		operationID, err := platformNewUUID()
		if err != nil {
			return err
		}
		if _, err := e.exec(`INSERT INTO app_platform_operations(id,installation_id,review_plan_id,actor_scope,idempotency_sha256,request_sha256,action,managed,
			source_generation,target_generation,stage,state_revision,row_revision,lease_owner,lease_until_ms,fence,controller_epoch,error_code,completed_ms,created_ms,updated_ms)
			VALUES(?,?,?,?,?,?,?,?,?,?,'complete',?,1,NULL,NULL,0,?,NULL,?,?,?)`,
			operationID, installation.String, planID, actor.scope, keyDigest, requestDigest, string(kind.action), boolInt(facts.managed),
			facts.sourceGen, facts.targetGeneration, int64(snapshot.Revision), epoch, now, now, now); err != nil {
			return err
		}
		detail, _ := json.Marshal(struct {
			Plan       string `json:"plan_id"`
			Action     string `json:"action"`
			Generation int64  `json:"generation"`
			Recovery   bool   `json:"recovery"`
		}{planID, string(kind.action), facts.targetGeneration, true})
		at := time.UnixMilli(now).UTC().Format(time.RFC3339)
		if _, err := e.exec(`INSERT INTO audit_log(at,actor,actor_ou,action,target_type,target_id,detail,ip) VALUES(?,?,?,'app_platform.operation_admitted','app_platform_operation',?,?,'')`,
			at, actor.username, actor.ouID, operationID, string(detail)); err != nil {
			return err
		}
		if _, err := e.exec(`INSERT INTO audit_log(at,actor,actor_ou,action,target_type,target_id,detail,ip) VALUES(?,?,?,?,'app_platform_installation',?,?,'')`,
			at, actor.username, actor.ouID, kind.audit, installation.String, string(detail)); err != nil {
			return err
		}
		result = platformRemovalCommit{OperationID: operationID, InstallationID: installation.String, Generation: facts.targetGeneration,
			Revision: int64(snapshot.Revision), Stage: appplatform.StageComplete}
		_, err = platformRefreshActor(e, session, actor, control)
		return err
	})
	if err != nil {
		return platformRemovalCommit{}, err
	}
	return result, nil
}
