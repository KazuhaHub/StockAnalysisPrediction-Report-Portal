package app

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"regexp"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

const (
	platformSecretBindingReviewFormat = "portal.secret-binding-review/1-dev.1"
	platformSecretBindingLimit        = 32
	platformSecretReviewLifetime      = 5 * time.Minute
)

var platformSecretBindingNamePattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,63}$`)

func platformValidCredentialHeaderValue(value []byte) bool {
	if len(value) < 1 || len(value) > platformCredentialValueLimit {
		return false
	}
	for _, b := range value {
		if b < 32 || b > 126 {
			return false
		}
	}
	return true
}

func platformSecretHeader(operation string, value []byte) (string, []byte, bool) {
	if !platformValidCredentialHeaderValue(value) {
		return "", nil, false
	}
	switch operation {
	case "http_bearer":
		header := make([]byte, 0, len("Bearer ")+len(value))
		header = append(header, "Bearer "...)
		header = append(header, value...)
		return "Authorization", header, true
	case "http_api_key":
		return "X-API-Key", append([]byte(nil), value...), true
	default:
		return "", nil, false
	}
}

type platformSecretBindingReview struct {
	Format                  string `json:"format"`
	InstallationID          string `json:"installation_id"`
	AppID                   string `json:"app_id"`
	PublisherID             string `json:"publisher_id"`
	Generation              int64  `json:"generation"`
	InstallationRevision    int64  `json:"installation_revision"`
	PolicyRevision          int64  `json:"policy_revision"`
	TrustRevision           int64  `json:"trust_revision"`
	ControllerEpoch         string `json:"controller_epoch"`
	ReleaseSHA256           string `json:"release_sha256"`
	BindingID               string `json:"binding_id"`
	BindingRevision         int64  `json:"binding_revision"`
	Name                    string `json:"name"`
	CredentialID            string `json:"credential_id"`
	CredentialLabel         string `json:"credential_label"`
	CredentialRevision      int64  `json:"credential_revision"`
	CredentialValueRevision int64  `json:"credential_value_revision"`
	Operation               string `json:"operation"`
	NetworkPolicySHA256     string `json:"network_policy_sha256"`
	ActorAuthority          string `json:"actor_authority"`
}

type platformSecretBindingReviewResult struct {
	ID, SHA256, JSON string
	ExpiresMS        int64
}

type platformSecretBindingMetadata struct {
	ID                      string `json:"id"`
	Name                    string `json:"name"`
	CredentialID            string `json:"credential_id"`
	CredentialLabel         string `json:"credential_label"`
	CredentialValueRevision int64  `json:"credential_value_revision"`
	Operation               string `json:"operation"`
	State                   string `json:"state"`
	PolicyRevision          int64  `json:"policy_revision"`
	Revision                int64  `json:"revision"`
	CreatedMS               int64  `json:"created_ms"`
	UpdatedMS               int64  `json:"updated_ms"`
}

type platformSecretBindingFacts struct {
	appID, publisherID, releaseSHA, controllerEpoch  string
	generation, installationRevision, policyRevision int64
	trustRevision                                    int64
	networkPolicySHA                                 string
}

type platformSecretBindingExisting struct {
	id, name, credentialID, operation                 string
	revision, credentialValueRevision, policyRevision int64
	state                                             string
}

type platformSecretBindingUse struct {
	id, credentialID, operation, header               string
	revision, credentialValueRevision, policyRevision int64
}

func loadPlatformSecretBindingFacts(e migExec, installation string, lock bool) (platformSecretBindingFacts, error) {
	var f platformSecretBindingFacts
	lockSQL := ""
	if lock && e.s.driver == "postgres" {
		lockSQL = " FOR UPDATE"
	}
	var runtime sql.NullString
	if err := e.queryRow(`SELECT app_id,publisher_id,generation,revision,policy_revision,controller_epoch,runtime_id FROM app_platform_installations WHERE id=? AND desired_state='enabled' AND phase='ready'`+lockSQL, installation).
		Scan(&f.appID, &f.publisherID, &f.generation, &f.installationRevision, &f.policyRevision, &f.controllerEpoch, &runtime); err != nil {
		return f, errAppPlatformCredentialUnavailable
	}
	if !platformValidUUID(installation) || !platformDirectoryAppID.MatchString(f.appID) || !platformDirectoryAppID.MatchString(f.publisherID) || !platformValidUUID(f.controllerEpoch) || !runtime.Valid || f.generation < 1 || f.installationRevision < 1 || f.policyRevision < 1 {
		return f, errAppPlatformCredentialUnavailable
	}
	if err := e.queryRow(`SELECT revision FROM app_platform_publishers WHERE id=?`, f.publisherID).Scan(&f.trustRevision); err != nil || f.trustRevision < 1 {
		return f, errAppPlatformCredentialUnavailable
	}
	if err := e.queryRow(`SELECT release_sha256 FROM app_platform_activations WHERE installation_id=? AND generation=? AND policy_revision=? AND state='active'`, installation, f.generation, f.policyRevision).Scan(&f.releaseSHA); err != nil || !platformValidSHA(f.releaseSHA) {
		return f, errAppPlatformCredentialUnavailable
	}
	var networkRaw, networkReview string
	if err := e.queryRow(`SELECT g.policy_json,g.review_plan_id FROM app_platform_capability_grants g JOIN app_platform_review_plans p ON p.id=g.review_plan_id WHERE g.installation_id=? AND g.capability='network.http' AND g.granted=1 AND g.policy_revision=? AND p.consumed_ms IS NOT NULL`, installation, f.policyRevision).
		Scan(&networkRaw, &networkReview); err != nil {
		return f, errAppPlatformCredentialUnavailable
	}
	network, err := appplatform.ParseHTTPPolicy([]byte(networkRaw))
	if err != nil || !platformValidUUID(networkReview) {
		return f, errAppPlatformCredentialUnavailable
	}
	f.networkPolicySHA = network.SHA256
	var secretsGrant, currentEpoch, restorePaused string
	if err := e.queryRow(`SELECT COALESCE((SELECT COUNT(*) FROM app_platform_capability_grants WHERE installation_id=? AND capability='secrets.use' AND granted=1 AND policy_revision=?),0),
		COALESCE((SELECT v FROM meta WHERE k='app_platform_controller_epoch'),''),
		COALESCE((SELECT v FROM meta WHERE k='app_platform_restore_paused'),'')`, installation, f.policyRevision).Scan(&secretsGrant, &currentEpoch, &restorePaused); err != nil {
		return f, errAppPlatformCredentialUnavailable
	}
	if secretsGrant != "1" || currentEpoch != f.controllerEpoch || restorePaused != "0" {
		return f, errAppPlatformCredentialUnavailable
	}
	return f, nil
}

func platformSecretBindingPolicyFor(operation, networkPolicySHA string) ([]byte, error) {
	return canonicalPlatformSecretBindingPolicy(platformSecretBindingPolicy{operation: operation, networkPolicySHA256: networkPolicySHA})
}

func (s *Server) createPlatformSecretBindingReview(session platformActorSession, installation, name, credentialID, operation string) (platformSecretBindingReviewResult, error) {
	var result platformSecretBindingReviewResult
	if !platformValidUUID(installation) || !platformSecretBindingNamePattern.MatchString(name) || !platformValidUUID(credentialID) || (operation != "http_bearer" && operation != "http_api_key") {
		return result, errPlatformManagementInput
	}
	err := s.withPlatformTrustTransaction(session, func(e migExec, actor platformActorFacts, control platformActorControl) error {
		if err := lockPlatformPublisherTrust(e); err != nil {
			return err
		}
		facts, err := loadPlatformSecretBindingFacts(e, installation, true)
		if err != nil {
			return err
		}
		var label, credentialState string
		var credentialRevision, valueRevision int64
		if err := e.queryRow(`SELECT label,state,revision,value_revision FROM app_platform_credentials WHERE id=?`, credentialID).Scan(&label, &credentialState, &credentialRevision, &valueRevision); err != nil || credentialState != "active" || credentialRevision < 1 || valueRevision < 1 {
			return errAppPlatformCredentialUnavailable
		}
		bindingID, err := platformNewUUID()
		if err != nil {
			return err
		}
		var bindingRevision int64
		var existing platformSecretBindingExisting
		err = e.queryRow(`SELECT id,name,credential_id,credential_value_revision,operation,state,policy_revision,revision FROM app_platform_secret_bindings WHERE installation_id=? AND name=?`, installation, name).
			Scan(&existing.id, &existing.name, &existing.credentialID, &existing.credentialValueRevision, &existing.operation, &existing.state, &existing.policyRevision, &existing.revision)
		if err == nil {
			if existing.state == "revoked" || existing.revision < 1 || existing.revision == math.MaxInt64 {
				return errAppPlatformCredentialUnavailable
			}
			bindingID, bindingRevision = existing.id, existing.revision
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		} else {
			var count int
			if err := e.queryRow(`SELECT COUNT(*) FROM app_platform_secret_bindings WHERE installation_id=?`, installation).Scan(&count); err != nil || count >= platformSecretBindingLimit {
				return errAppPlatformCredentialUnavailable
			}
		}
		plan := platformSecretBindingReview{
			Format: platformSecretBindingReviewFormat, InstallationID: installation, AppID: facts.appID, PublisherID: facts.publisherID,
			Generation: facts.generation, InstallationRevision: facts.installationRevision, PolicyRevision: facts.policyRevision, TrustRevision: facts.trustRevision,
			ControllerEpoch: facts.controllerEpoch, ReleaseSHA256: facts.releaseSHA, BindingID: bindingID, BindingRevision: bindingRevision,
			Name: name, CredentialID: credentialID, CredentialLabel: label, CredentialRevision: credentialRevision, CredentialValueRevision: valueRevision,
			Operation: operation, NetworkPolicySHA256: facts.networkPolicySHA, ActorAuthority: actor.authorityFingerprint,
		}
		raw, err := json.Marshal(plan)
		if err != nil || len(raw) > 65536 {
			return errAppPlatformCredentialUnavailable
		}
		planID, err := platformNewUUID()
		if err != nil {
			return err
		}
		control, err = platformRefreshActor(e, session, actor, control)
		if err != nil {
			return err
		}
		expires := control.nowMS + platformSecretReviewLifetime.Milliseconds()
		digest := platformDigest(raw)
		if _, err := e.exec(`INSERT INTO app_platform_review_plans(id,installation_id,app_id,publisher_id,action,source_release_sha256,target_release_sha256,installation_revision,policy_revision,trust_revision,controller_epoch,actor_scope,username,account_created,account_generation,authority_fingerprint,plan_sha256,plan_json,expires_ms,consumed_ms,created_ms,updated_ms)
			VALUES(?,?,?,?,'update',?,NULL,?,?,?,?,?,?,?,?,?,?,?,?,NULL,?,?)`,
			planID, installation, facts.appID, facts.publisherID, facts.releaseSHA, facts.installationRevision, facts.policyRevision, facts.trustRevision,
			facts.controllerEpoch, actor.scope, actor.username, actor.created, actor.generation, actor.authorityFingerprint, digest, string(raw), expires, control.nowMS, control.nowMS); err != nil {
			return err
		}
		result = platformSecretBindingReviewResult{ID: planID, SHA256: digest, JSON: string(raw), ExpiresMS: expires}
		_, err = platformRefreshActor(e, session, actor, control)
		return err
	})
	return result, err
}

func parsePlatformSecretBindingReview(raw string) (platformSecretBindingReview, error) {
	var plan platformSecretBindingReview
	if len(raw) == 0 || len(raw) > 65536 || json.Unmarshal([]byte(raw), &plan) != nil {
		return plan, errAppPlatformCredentialUnavailable
	}
	canonical, err := json.Marshal(plan)
	if err != nil || !bytes.Equal(canonical, []byte(raw)) || plan.Format != platformSecretBindingReviewFormat || !platformValidUUID(plan.InstallationID) || !platformDirectoryAppID.MatchString(plan.AppID) || !platformDirectoryAppID.MatchString(plan.PublisherID) || !platformValidUUID(plan.ControllerEpoch) || !platformValidSHA(plan.ReleaseSHA256) || !platformValidUUID(plan.BindingID) || plan.BindingRevision < 0 || !platformSecretBindingNamePattern.MatchString(plan.Name) || !platformValidUUID(plan.CredentialID) || !validPlatformCredentialLabel(plan.CredentialLabel) || (plan.Operation != "http_bearer" && plan.Operation != "http_api_key") || !platformValidSHA(plan.NetworkPolicySHA256) || !platformValidSHA(plan.ActorAuthority) || plan.Generation < 1 || plan.InstallationRevision < 1 || plan.PolicyRevision < 1 || plan.TrustRevision < 1 || plan.CredentialRevision < 1 || plan.CredentialValueRevision < 1 {
		return platformSecretBindingReview{}, errAppPlatformCredentialUnavailable
	}
	return plan, nil
}

func (s *Server) consumePlatformSecretBindingReview(session platformActorSession, planID, planSHA string) (platformSecretBindingMetadata, error) {
	var result platformSecretBindingMetadata
	if !platformValidUUID(planID) || !platformValidSHA(planSHA) {
		return result, errPlatformManagementInput
	}
	err := s.withPlatformTrustTransaction(session, func(e migExec, actor platformActorFacts, control platformActorControl) error {
		if err := lockPlatformPublisherTrust(e); err != nil {
			return err
		}
		lock := ""
		if e.s.driver == "postgres" {
			lock = " FOR UPDATE"
		}
		var raw, digest, action, source, scope, authority, username, created, epoch string
		var installation sql.NullString
		var expires, accountGeneration, installationRevision, policyRevision, trustRevision int64
		var consumed sql.NullInt64
		if err := e.queryRow(`SELECT plan_json,plan_sha256,action,source_release_sha256,installation_id,expires_ms,consumed_ms,installation_revision,policy_revision,trust_revision,controller_epoch,actor_scope,username,account_created,account_generation,authority_fingerprint FROM app_platform_review_plans WHERE id=?`+lock, planID).
			Scan(&raw, &digest, &action, &source, &installation, &expires, &consumed, &installationRevision, &policyRevision, &trustRevision, &epoch, &scope, &username, &created, &accountGeneration, &authority); err != nil {
			return errAppPlatformPlanChanged
		}
		control, err := platformRefreshActor(e, session, actor, control)
		if err != nil {
			return err
		}
		if consumed.Valid {
			return errAppPlatformOperationConflict
		}
		if expires <= control.nowMS {
			return errAppPlatformPlanExpired
		}
		if action != "update" || installationRevision < 1 || policyRevision < 1 || trustRevision < 1 || accountGeneration < 0 {
			return errAppPlatformPlanChanged
		}
		if !installation.Valid || username != actor.username || created != actor.created || accountGeneration != actor.generation || scope != actor.scope || authority != actor.authorityFingerprint || digest != planSHA || platformDigest([]byte(raw)) != planSHA {
			return errAppPlatformPlanChanged
		}
		plan, err := parsePlatformSecretBindingReview(raw)
		if err != nil || plan.InstallationID != installation.String || plan.ActorAuthority != authority || plan.ReleaseSHA256 != source || plan.ControllerEpoch != epoch {
			return errAppPlatformPlanChanged
		}
		planFacts := platformSecretBindingFacts{appID: plan.AppID, publisherID: plan.PublisherID, generation: plan.Generation, installationRevision: plan.InstallationRevision, policyRevision: plan.PolicyRevision, trustRevision: plan.TrustRevision, controllerEpoch: plan.ControllerEpoch, releaseSHA: plan.ReleaseSHA256, networkPolicySHA: plan.NetworkPolicySHA256}
		current, err := loadPlatformSecretBindingFacts(e, plan.InstallationID, true)
		if err != nil || current != planFacts {
			return errAppPlatformPlanChanged
		}
		var label, credentialState string
		var credentialRevision, valueRevision int64
		if err := e.queryRow(`SELECT label,state,revision,value_revision FROM app_platform_credentials WHERE id=?`, plan.CredentialID).Scan(&label, &credentialState, &credentialRevision, &valueRevision); err != nil || credentialState != "active" || label != plan.CredentialLabel || credentialRevision != plan.CredentialRevision || valueRevision != plan.CredentialValueRevision {
			return errAppPlatformPlanChanged
		}
		policy, err := platformSecretBindingPolicyFor(plan.Operation, plan.NetworkPolicySHA256)
		if err != nil {
			return err
		}
		now := control.nowMS
		if plan.BindingRevision == 0 {
			var count int
			if err := e.queryRow(`SELECT COUNT(*) FROM app_platform_secret_bindings WHERE installation_id=?`, plan.InstallationID).Scan(&count); err != nil || count >= platformSecretBindingLimit {
				return errAppPlatformCredentialUnavailable
			}
			if _, err := e.exec(`INSERT INTO app_platform_secret_bindings(id,installation_id,name,credential_id,credential_value_revision,operation,state,policy_json,policy_revision,review_plan_id,revision,created_ms,updated_ms,revoked_ms) VALUES(?,?,?,?,?,?,'active',?,?,?,1,?,?,NULL)`, plan.BindingID, plan.InstallationID, plan.Name, plan.CredentialID, plan.CredentialValueRevision, plan.Operation, string(policy), plan.PolicyRevision, planID, now, now); err != nil {
				return err
			}
			result = platformSecretBindingMetadata{ID: plan.BindingID, Name: plan.Name, CredentialID: plan.CredentialID, CredentialLabel: label, CredentialValueRevision: plan.CredentialValueRevision, Operation: plan.Operation, State: "active", PolicyRevision: plan.PolicyRevision, Revision: 1, CreatedMS: now, UpdatedMS: now}
		} else {
			changed, err := e.exec(`UPDATE app_platform_secret_bindings SET credential_id=?,credential_value_revision=?,operation=?,state='active',policy_json=?,policy_revision=?,review_plan_id=?,revision=revision+1,updated_ms=?,revoked_ms=NULL WHERE id=? AND installation_id=? AND name=? AND revision=? AND state IN ('active','review_required')`, plan.CredentialID, plan.CredentialValueRevision, plan.Operation, string(policy), plan.PolicyRevision, planID, now, plan.BindingID, plan.InstallationID, plan.Name, plan.BindingRevision)
			if err := platformObjectCAS(changed, err); err != nil {
				return errAppPlatformPlanChanged
			}
			result = platformSecretBindingMetadata{ID: plan.BindingID, Name: plan.Name, CredentialID: plan.CredentialID, CredentialLabel: label, CredentialValueRevision: plan.CredentialValueRevision, Operation: plan.Operation, State: "active", PolicyRevision: plan.PolicyRevision, Revision: plan.BindingRevision + 1, CreatedMS: now, UpdatedMS: now}
		}
		changed, err := e.exec(`UPDATE app_platform_review_plans SET consumed_ms=?,updated_ms=? WHERE id=? AND consumed_ms IS NULL AND plan_sha256=? AND expires_ms>?`, now, now, planID, planSHA, now)
		if err := platformObjectCAS(changed, err); err != nil {
			return errAppPlatformPlanChanged
		}
		detail, _ := json.Marshal(struct {
			Name, Operation string
			Revision        int64
		}{plan.Name, plan.Operation, result.Revision})
		if _, err := e.exec(`INSERT INTO audit_log(at,actor,actor_ou,action,target_type,target_id,detail,ip) VALUES(?,?,?,'app_platform.secret_binding_active','app_platform_secret_binding',?,?,'')`, time.UnixMilli(now).UTC().Format(time.RFC3339), actor.username, actor.ouID, result.ID, string(detail)); err != nil {
			return err
		}
		_, err = platformRefreshActor(e, session, actor, control)
		return err
	})
	return result, err
}

func (s *Server) listPlatformSecretBindings(session platformActorSession, installation string) ([]platformSecretBindingMetadata, error) {
	if !platformValidUUID(installation) {
		return nil, errPlatformManagementInput
	}
	var result []platformSecretBindingMetadata
	err := s.withAppPlatformActor(session, false, func(e migExec, actor platformActorFacts, control platformActorControl) error {
		rows, err := e.query(`SELECT b.id,b.name,b.credential_id,c.label,b.credential_value_revision,b.operation,b.state,b.policy_revision,b.revision,b.created_ms,b.updated_ms FROM app_platform_secret_bindings b JOIN app_platform_credentials c ON c.id=b.credential_id WHERE b.installation_id=? ORDER BY b.name`, installation)
		if err != nil {
			return err
		}
		for rows.Next() {
			var m platformSecretBindingMetadata
			if err := rows.Scan(&m.ID, &m.Name, &m.CredentialID, &m.CredentialLabel, &m.CredentialValueRevision, &m.Operation, &m.State, &m.PolicyRevision, &m.Revision, &m.CreatedMS, &m.UpdatedMS); err != nil {
				rows.Close()
				return err
			}
			result = append(result, m)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		_, err = platformRefreshActor(e, session, actor, control)
		return err
	})
	if result == nil {
		result = []platformSecretBindingMetadata{}
	}
	return result, err
}

func (s *Server) revokePlatformSecretBinding(session platformActorSession, id string, revision int64) error {
	if !platformValidUUID(id) || revision < 1 || revision == math.MaxInt64 {
		return errPlatformManagementInput
	}
	return s.withAppPlatformActor(session, false, func(e migExec, actor platformActorFacts, control platformActorControl) error {
		control, err := platformRefreshActor(e, session, actor, control)
		if err != nil {
			return err
		}
		changed, err := e.exec(`UPDATE app_platform_secret_bindings SET state='revoked',revision=revision+1,updated_ms=?,revoked_ms=? WHERE id=? AND revision=? AND state<>'revoked'`, control.nowMS, control.nowMS, id, revision)
		if err := platformObjectCAS(changed, err); err != nil {
			return errAppPlatformOperationConflict
		}
		detail, _ := json.Marshal(struct {
			Revision int64 `json:"revision"`
		}{revision + 1})
		if _, err := e.exec(`INSERT INTO audit_log(at,actor,actor_ou,action,target_type,target_id,detail,ip) VALUES(?,?,?,'app_platform.secret_binding_revoked','app_platform_secret_binding',?,?,'')`, time.UnixMilli(control.nowMS).UTC().Format(time.RFC3339), actor.username, actor.ouID, id, string(detail)); err != nil {
			return err
		}
		_, err = platformRefreshActor(e, session, actor, control)
		return err
	})
}

func loadPlatformSecretBindingUse(e migExec, installation, name string, facts platformSecretBindingFacts) (platformSecretBindingUse, string, error) {
	var use platformSecretBindingUse
	var policyRaw, reviewID, sealed string
	if err := e.queryRow(`SELECT id,credential_id,credential_value_revision,operation,policy_json,policy_revision,review_plan_id,revision FROM app_platform_secret_bindings WHERE installation_id=? AND name=? AND state='active'`, installation, name).
		Scan(&use.id, &use.credentialID, &use.credentialValueRevision, &use.operation, &policyRaw, &use.policyRevision, &reviewID, &use.revision); err != nil {
		return use, "", errAppPlatformCredentialUnavailable
	}
	if use.policyRevision != facts.policyRevision || !platformValidUUID(reviewID) || !platformValidUUID(use.id) || !platformValidUUID(use.credentialID) || use.credentialValueRevision < 1 || use.revision < 1 {
		return use, "", errAppPlatformCredentialUnavailable
	}
	if err := validatePlatformHTTPSecretBindingPolicy([]byte(policyRaw), use.operation, facts.networkPolicySHA); err != nil {
		return use, "", errAppPlatformCredentialUnavailable
	}
	switch use.operation {
	case "http_bearer":
		use.header = "Authorization"
	case "http_api_key":
		use.header = "X-API-Key"
	default:
		return use, "", errAppPlatformCredentialUnavailable
	}
	var raw, digest string
	var consumed sql.NullInt64
	if err := e.queryRow(`SELECT plan_json,plan_sha256,consumed_ms FROM app_platform_review_plans WHERE id=? AND action='update' AND installation_id=?`, reviewID, installation).Scan(&raw, &digest, &consumed); err != nil || !consumed.Valid || platformDigest([]byte(raw)) != digest {
		return use, "", errAppPlatformCredentialUnavailable
	}
	plan, err := parsePlatformSecretBindingReview(raw)
	if err != nil || plan.InstallationID != installation || plan.AppID != facts.appID || plan.PublisherID != facts.publisherID || plan.Generation != facts.generation || plan.InstallationRevision != facts.installationRevision || plan.TrustRevision != facts.trustRevision || plan.BindingID != use.id || use.revision != plan.BindingRevision+1 || plan.Name != name || plan.CredentialID != use.credentialID || plan.CredentialValueRevision != use.credentialValueRevision || plan.Operation != use.operation || plan.PolicyRevision != facts.policyRevision || plan.NetworkPolicySHA256 != facts.networkPolicySHA || plan.ReleaseSHA256 != facts.releaseSHA || plan.ControllerEpoch != facts.controllerEpoch {
		return use, "", errAppPlatformCredentialUnavailable
	}
	var label, state string
	var valueRevision, revision int64
	if err := e.queryRow(`SELECT label,state,value_revision,revision,sealed_value FROM app_platform_credentials WHERE id=?`, use.credentialID).Scan(&label, &state, &valueRevision, &revision, &sealed); err != nil || label != plan.CredentialLabel || state != "active" || valueRevision != use.credentialValueRevision || revision != plan.CredentialRevision || sealed == "" {
		return use, "", errAppPlatformCredentialUnavailable
	}
	return use, sealed, nil
}

func (s *Server) resolvePlatformSecretBindingCommand(e migExec, installation, name string) (platformSecretBindingUse, string, []byte, error) {
	facts, err := loadPlatformSecretBindingFacts(e, installation, false)
	if err != nil {
		return platformSecretBindingUse{}, "", nil, err
	}
	use, sealed, err := loadPlatformSecretBindingUse(e, installation, name, facts)
	if err != nil {
		return platformSecretBindingUse{}, "", nil, err
	}
	plain, err := s.openPlatformCredentialValue(use.credentialID, use.credentialValueRevision, sealed)
	if err != nil {
		return platformSecretBindingUse{}, "", nil, err
	}
	header, value, ok := platformSecretHeader(use.operation, plain)
	clear(plain)
	if !ok {
		return platformSecretBindingUse{}, "", nil, errAppPlatformCredentialUnavailable
	}
	return use, header, value, nil
}
