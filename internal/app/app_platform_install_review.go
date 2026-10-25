package app

import (
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"slices"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

var errAppPlatformInstallUnavailable = errors.New("application installation requirements unavailable")

const platformDevelopmentAPI = "2.0.0-dev.1"

type platformInstallProjection struct {
	Format               string   `json:"format"`
	AppID                string   `json:"app_id"`
	PublisherID          string   `json:"publisher_id"`
	ReleaseSHA256        string   `json:"release_sha256"`
	TrustRevision        int64    `json:"trust_revision"`
	KeyRevision          int64    `json:"key_revision"`
	KeyID                string   `json:"key_id"`
	ControllerEpoch      string   `json:"controller_epoch"`
	ActorAuthority       string   `json:"actor_authority"`
	PlatformAPI          string   `json:"platform_api"`
	DeclinedCapabilities []string `json:"declined_capabilities"`
	DataQuotaBytes       int64    `json:"data_quota_bytes"`
}

// Only an internal development protocol is assigned here; this is not Portal's
// CalVer/channel or a released public API version. Required providers are denied
// until their actual enforcement adapters exist. Optional capabilities decline.
func platformInstallRequirements(manifest appplatform.Manifest) error {
	compatibility, err := appplatform.EvaluateCompatibility(manifest, appplatform.CompatibilityTarget{PlatformAPI: platformDevelopmentAPI})
	if err != nil || !compatibility.Compatible || len(manifest.Capabilities.Required) != 0 {
		return errAppPlatformInstallUnavailable
	}
	return nil
}

func platformNoPriorInstall(e migExec, appID string) error {
	var count int
	err := e.queryRow(`SELECT COUNT(*) FROM app_platform_installations i WHERE i.app_id=? AND
 (i.desired_state<>'removed' OR i.phase<>'removed' OR EXISTS(SELECT 1 FROM app_platform_operations o WHERE o.installation_id=i.id AND (o.completed_ms IS NULL OR o.stage='recovery_required')) OR EXISTS(SELECT 1 FROM app_platform_operation_effects f JOIN app_platform_operations o ON o.id=f.operation_id WHERE o.installation_id=i.id AND f.state IN ('prepared','unknown')))`, appID).Scan(&count)
	if err != nil {
		return err
	}
	if count != 0 {
		return errAppPlatformOperationConflict
	}
	return nil
}

func platformLoadInstallProjection(e migExec, release platformStagedRelease, actor platformActorFacts, control platformActorControl) (platformInstallProjection, error) {
	var raw []byte
	if err := e.queryRow(`SELECT descriptor FROM app_platform_releases WHERE sha256=?`, release.sha256).Scan(&raw); err != nil {
		return platformInstallProjection{}, err
	}
	parsed, err := appplatform.ParseDescriptor(raw)
	if err != nil {
		return platformInstallProjection{}, errAppPlatformPlanChanged
	}
	if err := platformInstallRequirements(parsed.Manifest); err != nil {
		return platformInstallProjection{}, err
	}
	if err := platformNoPriorInstall(e, parsed.Manifest.ID); err != nil {
		return platformInstallProjection{}, err
	}
	declined := slices.Clone(parsed.Manifest.Capabilities.Optional)
	if declined == nil {
		declined = []string{}
	}
	slices.Sort(declined)
	return platformInstallProjection{Format: "portal.install-review/1-dev.1", AppID: release.appID, PublisherID: release.publisherID, ReleaseSHA256: release.sha256, TrustRevision: release.trustRevision, KeyRevision: release.keyRevision, KeyID: release.verificationKeyID, ControllerEpoch: control.epoch, ActorAuthority: actor.authorityFingerprint, PlatformAPI: platformDevelopmentAPI, DeclinedCapabilities: declined, DataQuotaBytes: 256 << 20}, nil
}

func (s *Server) createPlatformInstallReview(session platformActorSession, release string, trustRevision int64) (platformRemovalReview, error) {
	var result platformRemovalReview
	err := s.withPlatformStagedRelease(session, release, trustRevision, func(e migExec, actor platformActorFacts, control platformActorControl, verified platformStagedRelease) error {
		projection, err := platformLoadInstallProjection(e, verified, actor, control)
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
		raw, err := json.Marshal(projection)
		if err != nil {
			return err
		}
		id, err := platformNewUUID()
		if err != nil {
			return err
		}
		result = platformRemovalReview{ID: id, SHA256: platformDigest(raw), JSON: string(raw), ExpiresMS: control.nowMS + 900000}
		_, err = e.exec(`INSERT INTO app_platform_review_plans(id,installation_id,app_id,publisher_id,action,source_release_sha256,target_release_sha256,installation_revision,policy_revision,trust_revision,controller_epoch,actor_scope,username,account_created,account_generation,authority_fingerprint,plan_sha256,plan_json,expires_ms,consumed_ms,created_ms,updated_ms) VALUES(?,NULL,?,?,'install',NULL,?,0,0,?,?,?,?,?,?,?,?,?,?,NULL,?,?)`, id, projection.AppID, projection.PublisherID, release, trustRevision, control.epoch, actor.scope, actor.username, actor.created, actor.generation, actor.authorityFingerprint, result.SHA256, result.JSON, result.ExpiresMS, control.nowMS, control.nowMS)
		return err
	})
	if err != nil {
		return platformRemovalReview{}, err
	}
	return result, nil
}

func (s *Server) consumePlatformInstallReview(session platformActorSession, planID, planSHA, key string) (platformRemovalCommit, error) {
	var result platformRemovalCommit
	if !platformValidUUID(planID) || !platformValidSHA(planSHA) || !platformValidUUID(key) {
		return result, errAppPlatformPlanChanged
	}
	now, err := platformDatabaseNow(s.st.poolExec())
	if err != nil {
		return result, err
	}
	if _, err := platformReadActorAccount(s.st.poolExec(), session, now, false); err != nil {
		return result, err
	}
	dek, err := s.dek()
	if err != nil {
		return result, err
	}
	request, _ := json.Marshal([]string{"portal-operation-commit/v1", planID, planSHA})
	requestDigest, keyDigest := platformDigest(request), platformIdempotencySHA(key)
	err = s.withPlatformTrustActor(session, func(e migExec, actor platformActorFacts, control platformActorControl, trust appplatform.PublisherTrust) error {
		previous, exists, err := platformFindRemovalCommit(e, actor.scope, keyDigest, requestDigest)
		if err != nil {
			return err
		}
		if exists {
			result = previous
			return nil
		}
		lock := ""
		if e.s.driver == "postgres" {
			lock = " FOR UPDATE"
		}
		var storedSHA, raw, action, appID, publisher, epoch, scope, created, authority string
		var installation, source, target, username sql.NullString
		var expires, installationRevision, policyRevision, trustRevision, accountGeneration int64
		var consumed sql.NullInt64
		err = e.queryRow(`SELECT installation_id,source_release_sha256,target_release_sha256,plan_sha256,plan_json,action,app_id,publisher_id,controller_epoch,actor_scope,username,account_created,account_generation,authority_fingerprint,expires_ms,consumed_ms,installation_revision,policy_revision,trust_revision FROM app_platform_review_plans WHERE id=?`+lock, planID).Scan(&installation, &source, &target, &storedSHA, &raw, &action, &appID, &publisher, &epoch, &scope, &username, &created, &accountGeneration, &authority, &expires, &consumed, &installationRevision, &policyRevision, &trustRevision)
		if errors.Is(err, sql.ErrNoRows) {
			return errAppPlatformPlanChanged
		}
		if err != nil {
			return err
		}
		if scope != actor.scope || !username.Valid || username.String != actor.username || created != actor.created || accountGeneration != actor.generation {
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
		if action != "install" || installation.Valid || source.Valid || !target.Valid || installationRevision != 0 || policyRevision != 0 || epoch != control.epoch || authority != actor.authorityFingerprint || len(raw) > 65536 || storedSHA != planSHA || platformDigest([]byte(raw)) != planSHA {
			return errAppPlatformPlanChanged
		}
		verified, err := platformVerifyStoredRelease(e, target.String, trustRevision, trust, control.nowMS)
		if err != nil {
			return err
		}
		projection, err := platformLoadInstallProjection(e, verified, actor, control)
		if err != nil {
			return err
		}
		current, err := json.Marshal(projection)
		if err != nil {
			return err
		}
		if raw != string(current) || appID != verified.appID || publisher != verified.publisherID {
			return errAppPlatformPlanChanged
		}
		installationID, err := platformNewUUID()
		if err != nil {
			return err
		}
		operationID, err := platformNewUUID()
		if err != nil {
			return err
		}
		seed := make([]byte, 32)
		if _, err := rand.Read(seed); err != nil {
			return err
		}
		sealed, err := aesSeal(dek, seed, aad("app-platform", installationID))
		if err != nil {
			return err
		}
		if _, err = e.exec(`INSERT INTO app_platform_installations(id,app_id,publisher_id,runtime_id,desired_state,phase,generation,revision,policy_revision,control_fence,controller_epoch,sealed_identity_seed,created_ms,updated_ms) VALUES(?,?,?,NULL,'enabled','pending',1,1,1,0,?,?,?,?)`, installationID, appID, publisher, control.epoch, sealPrefix+sealed, control.nowMS, control.nowMS); err != nil {
			return err
		}
		updated, err := e.exec(`UPDATE app_platform_review_plans SET consumed_ms=?,updated_ms=? WHERE id=? AND consumed_ms IS NULL AND expires_ms>? AND plan_sha256=?`, control.nowMS, control.nowMS, planID, control.nowMS, planSHA)
		if err := platformLeaseCAS(updated, err); err != nil {
			return err
		}
		if _, err = e.exec(`INSERT INTO app_platform_operations(id,installation_id,review_plan_id,actor_scope,idempotency_sha256,request_sha256,action,managed,source_generation,target_generation,stage,state_revision,row_revision,lease_owner,lease_until_ms,fence,controller_epoch,error_code,completed_ms,created_ms,updated_ms) VALUES(?,?,?,?,?,?,'install',0,0,1,'queued',1,1,NULL,NULL,0,?,NULL,NULL,?,?)`, operationID, installationID, planID, actor.scope, keyDigest, requestDigest, control.epoch, control.nowMS, control.nowMS); err != nil {
			return err
		}
		for _, capability := range projection.DeclinedCapabilities {
			if _, err := e.exec(`INSERT INTO app_platform_capability_grants(installation_id,capability,granted,policy_json,policy_revision,review_plan_id,created_ms,updated_ms) VALUES(?,?,0,'{}',1,?,?,?)`, installationID, capability, planID, control.nowMS, control.nowMS); err != nil {
				return err
			}
		}
		detail, _ := json.Marshal(struct {
			Plan, Action string
			Generation   int64
		}{planID, "install", 1})
		if _, err := e.exec(`INSERT INTO audit_log(at,actor,actor_ou,action,target_type,target_id,detail,ip) VALUES(?,?,?,'app_platform.operation_admitted','app_platform_operation',?,?,'')`, time.UnixMilli(control.nowMS).UTC().Format(time.RFC3339), actor.username, actor.ouID, operationID, string(detail)); err != nil {
			return err
		}
		if err := platformFinalStoredReleaseTrust(e, session, actor, control, target.String, trustRevision, trust); err != nil {
			return err
		}
		final, err := platformRefreshActor(e, session, actor, control)
		if err != nil {
			return err
		}
		if expires <= final.nowMS {
			return errAppPlatformPlanExpired
		}
		result = platformRemovalCommit{OperationID: operationID, InstallationID: installationID, Generation: 1, Revision: 1, Stage: appplatform.StageQueued}
		return nil
	})
	if err != nil {
		return platformRemovalCommit{}, err
	}
	return result, nil
}
