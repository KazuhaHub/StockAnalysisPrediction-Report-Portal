package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"math"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

const platformUpdateReviewFormat = "portal.update-review/1-dev.1"

type platformUpdateProjection struct {
	Format               string                    `json:"format"`
	InstallationID       string                    `json:"installation_id"`
	AppID                string                    `json:"app_id"`
	PublisherID          string                    `json:"publisher_id"`
	SourceReleaseSHA256  string                    `json:"source_release_sha256"`
	SourceGeneration     int64                     `json:"source_generation"`
	TargetGeneration     int64                     `json:"target_generation"`
	InstallationRevision int64                     `json:"installation_revision"`
	PolicyRevision       int64                     `json:"policy_revision"`
	TargetPolicyRevision int64                     `json:"target_policy_revision"`
	TrustRevision        int64                     `json:"trust_revision"`
	ControllerEpoch      string                    `json:"controller_epoch"`
	ActorAuthority       string                    `json:"actor_authority"`
	CurrentDataFormat    int64                     `json:"current_data_format"`
	PreviousCapabilities []string                  `json:"previous_capabilities"`
	TargetCapabilities   []string                  `json:"target_capabilities"`
	GrantsSHA256         string                    `json:"grants_sha256"`
	Target               platformInstallProjection `json:"target"`
}

func platformCurrentCapabilities(e migExec, installation string) ([]string, error) {
	rows, err := e.query(`SELECT capability FROM app_platform_capability_grants WHERE installation_id=? AND granted=1 ORDER BY capability`, installation)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []string
	for rows.Next() {
		var capability string
		if err := rows.Scan(&capability); err != nil {
			return nil, err
		}
		result = append(result, capability)
	}
	return result, rows.Err()
}

func platformBuildUpdateProjection(e migExec, installation string, sourceRelease string, sourceGeneration, installationRevision, policyRevision int64, currentFormat int64, actor platformActorFacts, control platformActorControl, targetRelease platformStagedRelease) (platformUpdateProjection, error) {
	var descriptor []byte
	if err := e.queryRow(`SELECT descriptor FROM app_platform_releases WHERE sha256=?`, targetRelease.sha256).Scan(&descriptor); err != nil {
		return platformUpdateProjection{}, err
	}
	parsed, err := appplatform.ParseDescriptor(descriptor)
	if err != nil || parsed.Manifest.Backend != nil || parsed.Manifest.ID != targetRelease.appID || parsed.Manifest.Publisher != targetRelease.publisherID {
		return platformUpdateProjection{}, errAppPlatformPlanChanged
	}
	if err := platformInstallRequirements(parsed.Manifest); err != nil {
		return platformUpdateProjection{}, err
	}
	if err := appplatform.RequireDataUpgrade(int(currentFormat), parsed.Manifest.Data); err != nil {
		return platformUpdateProjection{}, errAppPlatformInstallUnavailable
	}
	if int64(parsed.Manifest.Data.Schema) != currentFormat {
		// Migration execution is a later H13 gate. Declaring readability is not
		// evidence that the target bytes can migrate the current data.
		return platformUpdateProjection{}, errAppPlatformInstallUnavailable
	}
	selection, err := platformInstallCapabilitySelection(parsed.Manifest, nil)
	if err != nil {
		return platformUpdateProjection{}, err
	}
	previous, err := platformCurrentCapabilities(e, installation)
	if err != nil {
		return platformUpdateProjection{}, err
	}
	targetCapabilities := make([]string, 0, len(selection))
	for _, capability := range selection {
		if capability.granted {
			targetCapabilities = append(targetCapabilities, capability.capability)
		}
	}
	grants, err := platformRemovalGrantsDigest(e, installation)
	if err != nil {
		return platformUpdateProjection{}, err
	}
	target := platformInstallProjection{
		Format: platformFrontendInstallFormat, AppID: targetRelease.appID, PublisherID: targetRelease.publisherID,
		ReleaseSHA256: targetRelease.sha256, TrustRevision: targetRelease.trustRevision, KeyRevision: targetRelease.keyRevision,
		KeyID: targetRelease.verificationKeyID, ControllerEpoch: control.epoch, ActorAuthority: actor.authorityFingerprint,
		PlatformAPI: platformDevelopmentAPI, DeclinedCapabilities: platformDeclinedCapabilities(selection), DataQuotaBytes: 0,
	}
	for _, capability := range parsed.Manifest.Capabilities.Required {
		if capability == "storage.files" {
			target.DataQuotaBytes = 256 << 20
		}
	}
	return platformUpdateProjection{
		Format: platformUpdateReviewFormat, InstallationID: installation, AppID: targetRelease.appID, PublisherID: targetRelease.publisherID,
		SourceReleaseSHA256: sourceRelease, SourceGeneration: sourceGeneration, TargetGeneration: sourceGeneration + 1,
		InstallationRevision: installationRevision, PolicyRevision: policyRevision, TargetPolicyRevision: policyRevision + 1, TrustRevision: targetRelease.trustRevision,
		ControllerEpoch: control.epoch, ActorAuthority: actor.authorityFingerprint, CurrentDataFormat: currentFormat, TargetCapabilities: targetCapabilities,
		PreviousCapabilities: previous, GrantsSHA256: grants, Target: target,
	}, nil
}

func (s *Server) createPlatformFrontendUpdateReviewContext(ctx context.Context, session platformActorSession, installation, release string, trustRevision int64) (platformRemovalReview, error) {
	if !platformValidUUID(installation) || !platformValidSHA(release) || trustRevision < 1 {
		return platformRemovalReview{}, errAppPlatformPlanChanged
	}
	var result platformRemovalReview
	err := s.withPlatformStagedReleaseContext(ctx, session, release, trustRevision, func(e migExec, actor platformActorFacts, control platformActorControl, verified platformStagedRelease) error {
		if verified.appID == "" {
			return errAppPlatformPlanChanged
		}
		lock := ""
		if e.s.driver == "postgres" {
			lock = " FOR UPDATE"
		}
		var appID, publisher, desired, phase, epoch string
		var runtime sql.NullString
		var generation, revision, policy int64
		if err := e.queryRow(`SELECT app_id,publisher_id,desired_state,phase,generation,revision,policy_revision,controller_epoch,runtime_id FROM app_platform_installations WHERE id=?`+lock, installation).
			Scan(&appID, &publisher, &desired, &phase, &generation, &revision, &policy, &epoch, &runtime); err != nil {
			return errAppPlatformPlanChanged
		}
		if runtime.Valid || desired != "enabled" || phase != "ready" || epoch != control.epoch || generation < 1 || generation == math.MaxInt64 || revision < 1 || revision == math.MaxInt64 || policy < 1 || policy == math.MaxInt64 {
			return errAppPlatformPlanChanged
		}
		var pending int
		if err := e.queryRow(`SELECT COUNT(*) FROM app_platform_operations WHERE installation_id=? AND completed_ms IS NULL`, installation).Scan(&pending); err != nil || pending != 0 {
			return errAppPlatformOperationConflict
		}
		var source string
		var currentFormat int64
		if err := e.queryRow(`SELECT release_sha256,data_format FROM app_platform_activations WHERE installation_id=? AND generation=? AND policy_revision=? AND state='active'`, installation, generation, policy).
			Scan(&source, &currentFormat); err != nil || !platformValidSHA(source) || source == verified.sha256 {
			return errAppPlatformPlanChanged
		}
		var sourceApp, sourcePublisher string
		var sourceDescriptor []byte
		if err := e.queryRow(`SELECT app_id,publisher_id,descriptor FROM app_platform_releases WHERE sha256=?`, source).Scan(&sourceApp, &sourcePublisher, &sourceDescriptor); err != nil || sourceApp != appID || sourcePublisher != publisher {
			return errAppPlatformPlanChanged
		}
		sourceManifest, err := appplatform.ParseDescriptor(sourceDescriptor)
		if err != nil || sourceManifest.Manifest.ID != appID {
			return errAppPlatformPlanChanged
		}
		if currentFormat == 0 {
			currentFormat = int64(sourceManifest.Manifest.Data.Schema)
		} else if int64(sourceManifest.Manifest.Data.Schema) != currentFormat {
			return errAppPlatformPlanChanged
		}
		if verified.appID != appID || verified.publisherID != publisher {
			return errAppPlatformPlanChanged
		}
		projection, err := platformBuildUpdateProjection(e, installation, source, generation, revision, policy, currentFormat, actor, control, verified)
		if err != nil {
			return err
		}
		control, err = platformRefreshActor(e, session, actor, control)
		if err != nil {
			return err
		}
		raw, err := json.Marshal(projection)
		if err != nil || len(raw) > 65536 || control.nowMS > math.MaxInt64-900000 {
			return errAppPlatformPlanChanged
		}
		id, err := platformNewUUID()
		if err != nil {
			return err
		}
		digest := platformDigest(raw)
		expires := control.nowMS + 900000
		if _, err := e.exec(`INSERT INTO app_platform_review_plans(id,installation_id,app_id,publisher_id,action,source_release_sha256,target_release_sha256,installation_revision,policy_revision,trust_revision,controller_epoch,actor_scope,username,account_created,account_generation,authority_fingerprint,plan_sha256,plan_json,expires_ms,consumed_ms,created_ms,updated_ms)
			VALUES(?,?,?,?,'update',?,?,?,?,?,?,?,?,?,?,?,?,?,?,NULL,?,?)`,
			id, installation, appID, publisher, source, verified.sha256, revision, policy, trustRevision, control.epoch,
			actor.scope, actor.username, actor.created, actor.generation, actor.authorityFingerprint, digest, string(raw), expires, control.nowMS, control.nowMS); err != nil {
			return err
		}
		result = platformRemovalReview{ID: id, SHA256: digest, JSON: string(raw), ExpiresMS: expires}
		_, err = platformRefreshActor(e, session, actor, control)
		return err
	})
	return result, err
}

func (s *Server) consumePlatformUpdateReview(session platformActorSession, planID, planSHA, key string) (platformRemovalCommit, error) {
	var result platformRemovalCommit
	if !platformValidUUID(planID) || !platformValidSHA(planSHA) || !platformValidUUID(key) {
		return result, errAppPlatformPlanChanged
	}
	request, _ := json.Marshal([]string{"portal-operation-commit/v1", planID, planSHA})
	requestDigest, keyDigest := platformDigest(request), platformIdempotencySHA(key)
	err := s.withPlatformTrustTransaction(session, func(e migExec, actor platformActorFacts, control platformActorControl) error {
		found, exists, err := platformFindRemovalCommit(e, actor.scope, keyDigest, requestDigest)
		if err != nil {
			return err
		}
		if exists {
			if found.Stage != "" {
				var action string
				if err := e.queryRow(`SELECT action FROM app_platform_operations WHERE id=?`, found.OperationID).Scan(&action); err != nil || action != string(appplatform.ActionUpdate) {
					return errAppPlatformOperationConflict
				}
			}
			result = found
			return nil
		}
		lock := ""
		if e.s.driver == "postgres" {
			lock = " FOR UPDATE"
		}
		var raw, digest, scope, username, created, authority, epoch, action, appID, publisher string
		var installation, source, target sql.NullString
		var expires, revision, policy, trustRevision, accountGeneration int64
		var consumed sql.NullInt64
		if err := e.queryRow(`SELECT plan_json,plan_sha256,actor_scope,username,account_created,account_generation,authority_fingerprint,expires_ms,consumed_ms,installation_revision,policy_revision,trust_revision,controller_epoch,action,installation_id,source_release_sha256,target_release_sha256,app_id,publisher_id FROM app_platform_review_plans WHERE id=?`+lock, planID).
			Scan(&raw, &digest, &scope, &username, &created, &accountGeneration, &authority, &expires, &consumed, &revision, &policy, &trustRevision, &epoch, &action, &installation, &source, &target, &appID, &publisher); err != nil {
			return errAppPlatformPlanChanged
		}
		control, err = platformRefreshActor(e, session, actor, control)
		if err != nil {
			return err
		}
		if consumed.Valid || expires <= control.nowMS || action != "update" || !installation.Valid || !source.Valid || !target.Valid || scope != actor.scope || username != actor.username || created != actor.created || accountGeneration != actor.generation || authority != actor.authorityFingerprint || epoch != control.epoch || digest != planSHA || platformDigest([]byte(raw)) != planSHA {
			return errAppPlatformPlanChanged
		}
		var p platformUpdateProjection
		if json.Unmarshal([]byte(raw), &p) != nil || p.Format != platformUpdateReviewFormat || p.InstallationID != installation.String || p.AppID != appID || p.PublisherID != publisher || p.SourceReleaseSHA256 != source.String || p.Target.ReleaseSHA256 != target.String || p.InstallationRevision != revision || p.PolicyRevision != policy || p.TrustRevision != trustRevision || p.ControllerEpoch != epoch || p.ActorAuthority != authority || p.TargetGeneration != p.SourceGeneration+1 {
			return errAppPlatformPlanChanged
		}
		canonical, err := json.Marshal(p)
		if err != nil || string(canonical) != raw {
			return errAppPlatformPlanChanged
		}
		var currentApp, currentPublisher, desired, phase, currentEpoch string
		var currentRuntime sql.NullString
		var generation, currentRevision, currentPolicy int64
		if err := e.queryRow(`SELECT app_id,publisher_id,desired_state,phase,generation,revision,policy_revision,controller_epoch,runtime_id FROM app_platform_installations WHERE id=?`+lock, installation.String).
			Scan(&currentApp, &currentPublisher, &desired, &phase, &generation, &currentRevision, &currentPolicy, &currentEpoch, &currentRuntime); err != nil {
			return errAppPlatformPlanChanged
		}
		if currentRuntime.Valid || currentApp != appID || currentPublisher != publisher || desired != "enabled" || phase != "ready" || generation != p.SourceGeneration || currentRevision != revision || currentPolicy != policy || currentEpoch != epoch {
			return errAppPlatformPlanChanged
		}
		var activeRelease string
		var currentFormat int64
		if err := e.queryRow(`SELECT release_sha256,data_format FROM app_platform_activations WHERE installation_id=? AND generation=? AND policy_revision=? AND state='active'`, installation.String, generation, policy).Scan(&activeRelease, &currentFormat); err != nil || activeRelease != source.String {
			return errAppPlatformPlanChanged
		}
		if currentFormat == 0 {
			var sourceDescriptor []byte
			if err := e.queryRow(`SELECT descriptor FROM app_platform_releases WHERE sha256=?`, source.String).Scan(&sourceDescriptor); err != nil {
				return errAppPlatformPlanChanged
			}
			parsed, err := appplatform.ParseDescriptor(sourceDescriptor)
			if err != nil {
				return errAppPlatformPlanChanged
			}
			currentFormat = int64(parsed.Manifest.Data.Schema)
		}
		if currentFormat != p.CurrentDataFormat {
			return errAppPlatformPlanChanged
		}
		var pending int
		if err := e.queryRow(`SELECT COUNT(*) FROM app_platform_operations WHERE installation_id=? AND completed_ms IS NULL`, installation.String).Scan(&pending); err != nil || pending != 0 {
			return errAppPlatformOperationConflict
		}
		trust, err := loadPlatformPublisherTrust(e)
		if err != nil {
			return err
		}
		verified, err := platformVerifyStoredRelease(e, target.String, trustRevision, trust, control.nowMS)
		if err != nil || verified.appID != appID || verified.publisherID != publisher {
			return errAppPlatformPlanChanged
		}
		expected, err := platformBuildUpdateProjection(e, installation.String, source.String, generation, revision, policy, currentFormat, actor, platformActorControl{epoch: epoch, nowMS: control.nowMS}, verified)
		expectedRaw, canonicalErr := json.Marshal(expected)
		if err != nil || canonicalErr != nil || string(expectedRaw) != raw {
			return errAppPlatformPlanChanged
		}
		operationID, err := platformNewUUID()
		if err != nil {
			return err
		}
		if _, err := e.exec(`INSERT INTO app_platform_operations(id,installation_id,review_plan_id,actor_scope,idempotency_sha256,request_sha256,action,managed,source_generation,target_generation,stage,state_revision,row_revision,lease_owner,lease_until_ms,fence,controller_epoch,error_code,completed_ms,created_ms,updated_ms)
			VALUES(?,?,?,?,?,?,'update',0,?,?, 'queued',1,1,NULL,NULL,0,?,NULL,NULL,?,?)`,
			operationID, installation.String, planID, actor.scope, keyDigest, requestDigest, generation, p.TargetGeneration, epoch, control.nowMS, control.nowMS); err != nil {
			return err
		}
		changed, err := e.exec(`UPDATE app_platform_review_plans SET consumed_ms=?,updated_ms=? WHERE id=? AND consumed_ms IS NULL AND expires_ms>? AND plan_sha256=?`, control.nowMS, control.nowMS, planID, control.nowMS, planSHA)
		if err := platformLeaseCAS(changed, err); err != nil {
			return err
		}
		result = platformRemovalCommit{OperationID: operationID, InstallationID: installation.String, Generation: p.TargetGeneration, Revision: 1, Stage: appplatform.StageQueued}
		_, err = platformRefreshActor(e, session, actor, control)
		return err
	})
	return result, err
}
