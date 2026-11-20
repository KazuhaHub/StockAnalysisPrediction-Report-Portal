package app

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"strings"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

const (
	platformSecretBindingReviewFormat = "portal.secret-binding-review/1-dev.1"
	platformSecretBindingLimit        = 32
	platformSecretReviewLifetime      = 5 * time.Minute
)

var platformSecretBindingNamePattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,63}$`)

// platformTargetIDPattern is the spelling of a network target id.
var platformTargetIDPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,47}$`)

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
	// NetworkTargetSHA256 is the digest of the one target the binding is for, and
	// NetworkTarget its origin, for display. Both are absent in a review made
	// before bindings were scoped to a target (the policy it writes is then the
	// whole-policy scope of the older formats).
	NetworkTargetSHA256 string `json:"network_target_sha256,omitempty"`
	NetworkTarget       string `json:"network_target,omitempty"`
	// Principals is absent for a viewer-only binding (every review made before
	// doc 176 reads back unchanged) and otherwise the sorted list that includes
	// "service".
	Principals []string `json:"principals,omitempty"`
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
	// Principals is who may use the binding: "viewer", "service" or both, as
	// the reviewed policy says.
	Principals []string `json:"principals"`
	// NetworkTargetSHA256 and NetworkTarget (its origin) say which network target
	// a target binding is for. Both are absent on a whole-policy binding.
	NetworkTargetSHA256 string `json:"network_target_sha256,omitempty"`
	NetworkTarget       string `json:"network_target,omitempty"`
}

type platformSecretBindingFacts struct {
	appID, publisherID, releaseSHA, controllerEpoch  string
	generation, installationRevision, policyRevision int64
	trustRevision                                    int64
	networkPolicySHA                                 string
	// networkPolicyRaw is the grant's policy bytes. It is what the digest above
	// is of, so it is left out when facts are compared with a review's.
	networkPolicyRaw string
}

type platformSecretBindingExisting struct {
	id, name, credentialID, operation                 string
	revision, credentialValueRevision, policyRevision int64
	state                                             string
}

type platformSecretBindingUse struct {
	id, credentialID, operation, header, principals   string
	networkTargetSHA256                               string
	revision, credentialValueRevision, policyRevision int64
}

func loadPlatformSecretBindingFacts(e migExec, installation string, lock bool) (platformSecretBindingFacts, error) {
	return loadPlatformSecretBindingFactsMode(e, installation, lock, false)
}

func loadPlatformSecretBindingFactsMode(e migExec, installation string, lock, allowNoNetwork bool) (platformSecretBindingFacts, error) {
	var f platformSecretBindingFacts
	lockSQL := ""
	if lock && e.s.driver == "postgres" {
		lockSQL = " FOR UPDATE"
	}
	var runtime sql.NullString
	var phase string
	if err := e.queryRow(`SELECT app_id,publisher_id,generation,revision,policy_revision,controller_epoch,runtime_id,phase FROM app_platform_installations WHERE id=? AND desired_state='enabled' AND phase IN ('ready','setup_required')`+lockSQL, installation).
		Scan(&f.appID, &f.publisherID, &f.generation, &f.installationRevision, &f.policyRevision, &f.controllerEpoch, &runtime, &phase); err != nil {
		return f, errAppPlatformCredentialUnavailable
	}
	if !platformValidUUID(installation) || !platformDirectoryAppID.MatchString(f.appID) || !platformDirectoryAppID.MatchString(f.publisherID) || !platformValidUUID(f.controllerEpoch) || !runtime.Valid || f.generation < 1 || f.installationRevision < 1 || f.policyRevision < 1 ||
		(phase != "ready" && (phase != "setup_required" || len(platformRequiredSetupBindings(f.appID)) == 0)) {
		return f, errAppPlatformCredentialUnavailable
	}
	if err := e.queryRow(`SELECT revision FROM app_platform_publishers WHERE id=?`, f.publisherID).Scan(&f.trustRevision); err != nil || f.trustRevision < 1 {
		return f, errAppPlatformCredentialUnavailable
	}
	if err := e.queryRow(`SELECT release_sha256 FROM app_platform_activations WHERE installation_id=? AND generation=? AND policy_revision=? AND state='active'`, installation, f.generation, f.policyRevision).Scan(&f.releaseSHA); err != nil || !platformValidSHA(f.releaseSHA) {
		return f, errAppPlatformCredentialUnavailable
	}
	var networkRaw, networkReview string
	err := e.queryRow(`SELECT g.policy_json,g.review_plan_id FROM app_platform_capability_grants g JOIN app_platform_review_plans p ON p.id=g.review_plan_id WHERE g.installation_id=? AND g.capability='network.http' AND g.granted=1 AND g.policy_revision=? AND p.consumed_ms IS NOT NULL`, installation, f.policyRevision).
		Scan(&networkRaw, &networkReview)
	switch {
	case err == nil:
		network, parseErr := appplatform.ParseHTTPPolicyAny([]byte(networkRaw))
		if parseErr != nil || !platformValidUUID(networkReview) {
			return f, errAppPlatformCredentialUnavailable
		}
		f.networkPolicySHA, f.networkPolicyRaw = network.SHA256, networkRaw
	case errors.Is(err, sql.ErrNoRows) && allowNoNetwork:
		f.networkPolicySHA = ""
	default:
		return f, errAppPlatformCredentialUnavailable
	}
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

func platformSecretBindingPolicyFor(operation, networkPolicySHA, principals string) ([]byte, error) {
	return canonicalPlatformSecretBindingPolicy(platformSecretBindingPolicy{operation: operation, networkPolicySHA256: networkPolicySHA, principals: principals})
}

// platformBindingDestination is the request a binding is used for. The target
// its URL selects in the grant's policy is what a target binding is judged
// against. The zero value names no destination: a target binding is then only
// checked to still be a target of the policy.
type platformBindingDestination struct{ method, url string }

// platformSecretBindingTargets is the grant's network policy, read as targets.
func platformSecretBindingTargets(facts platformSecretBindingFacts) (appplatform.HTTPPolicyV2, error) {
	parsed, err := appplatform.ParseHTTPPolicyAny([]byte(facts.networkPolicyRaw))
	if err != nil || parsed.SHA256 != facts.networkPolicySHA {
		return appplatform.HTTPPolicyV2{}, errAppPlatformCredentialUnavailable
	}
	return parsed.Policy, nil
}

// platformSecretBindingSelect is the target a destination selects in the grant,
// judged before any binding or credential is read. A destination the policy does
// not select, or one that selects a plain-http target, is refused: no binding
// opens a credential for an unencrypted request.
func platformSecretBindingSelect(facts platformSecretBindingFacts, dest platformBindingDestination) (appplatform.EffectiveHTTPTarget, error) {
	var zero appplatform.EffectiveHTTPTarget
	policy, err := platformSecretBindingTargets(facts)
	if err != nil {
		return zero, err
	}
	selected, err := policy.Select(dest.method, dest.url)
	if err != nil || selected.Scheme != "https" {
		return zero, errAppPlatformCredentialUnavailable
	}
	return selected, nil
}

// platformSecretBindingTargetPresent says whether the grant's policy has a
// target with this digest. A review uses it; a call never does.
func platformSecretBindingTargetPresent(facts platformSecretBindingFacts, targetSHA string) error {
	policy, err := platformSecretBindingTargets(facts)
	if err != nil {
		return err
	}
	for _, t := range policy.Targets {
		if digest, err := appplatform.HTTPTargetSHA256(t); err == nil && digest == targetSHA {
			return nil
		}
	}
	return errAppPlatformCredentialUnavailable
}

// platformSecretBindingTargetAllowed says whether a target binding may be used
// for dest now: its target must still be one of the policy and the one the
// destination selects. A call with no destination selects nothing and is refused.
func platformSecretBindingTargetAllowed(facts platformSecretBindingFacts, targetSHA string, dest platformBindingDestination) error {
	if dest.url == "" || platformSecretBindingTargetPresent(facts, targetSHA) != nil {
		return errAppPlatformCredentialUnavailable
	}
	if selected, err := platformSecretBindingSelect(facts, dest); err == nil && selected.SHA256 == targetSHA {
		return nil
	}
	return errAppPlatformCredentialUnavailable
}

// platformSecretBindingReviewTarget picks the target a review binds. An empty
// id is the policy's only target and is refused when there are several. The
// target must be https.
func platformSecretBindingReviewTarget(facts platformSecretBindingFacts, id string) (digest, origin string, err error) {
	policy, err := platformSecretBindingTargets(facts)
	if err != nil {
		return "", "", err
	}
	if id == "" && len(policy.Targets) == 1 {
		id = policy.Targets[0].ID
	}
	for _, t := range policy.Targets {
		if t.ID != id {
			continue
		}
		digest, err := appplatform.HTTPTargetSHA256(t)
		if err != nil || !strings.HasPrefix(t.Origin, "https://") {
			return "", "", errPlatformManagementInput
		}
		return digest, t.Origin, nil
	}
	return "", "", errPlatformManagementInput
}

// platformSecretBindingPrincipalsOfPlan is the policy spelling of a review's
// principals list, and whether the list is one a review may carry: absent
// (viewer only), ["service"] or ["service","viewer"].
func platformSecretBindingPrincipalsOfPlan(list []string) (string, bool) {
	switch strings.Join(list, ",") {
	case "":
		return "", list == nil
	case platformPrincipalsServiceOnly:
		return platformPrincipalsServiceOnly, true
	case platformPrincipalsBoth:
		return platformPrincipalsBoth, true
	default:
		return "", false
	}
}

func (s *Server) createPlatformSecretBindingReview(session platformActorSession, installation, name, credentialID, operation string) (platformSecretBindingReviewResult, error) {
	return s.createPlatformSecretBindingReviewPrincipals(session, installation, name, credentialID, operation, "")
}

// createPlatformSecretBindingReviewPrincipals is the review that decides who may
// use the binding: principals is "" (the viewer only), "service" or
// "service,viewer". Changing it is a new review of the same binding.
func (s *Server) createPlatformSecretBindingReviewPrincipals(session platformActorSession, installation, name, credentialID, operation, principals string) (platformSecretBindingReviewResult, error) {
	return s.createPlatformSecretBindingReviewTarget(session, installation, name, credentialID, operation, principals, "")
}

// createPlatformSecretBindingReviewTarget also names the target of the network
// policy the binding is for (its id; empty is the policy's only target). An HTTP
// binding is always scoped to that one target and a plain-http target is refused.
func (s *Server) createPlatformSecretBindingReviewTarget(session platformActorSession, installation, name, credentialID, operation, principals, target string) (platformSecretBindingReviewResult, error) {
	var result platformSecretBindingReviewResult
	if !platformValidUUID(installation) || !platformSecretBindingNamePattern.MatchString(name) || !platformValidUUID(credentialID) || (operation != "http_bearer" && operation != "http_api_key" && operation != "raw_read") ||
		!validPlatformSecretBindingPrincipals(principals) || (operation == "raw_read" && (principals != "" || target != "")) || (target != "" && !platformTargetIDPattern.MatchString(target)) {
		return result, errPlatformManagementInput
	}
	err := s.withPlatformTrustTransaction(session, func(e migExec, actor platformActorFacts, control platformActorControl) error {
		if err := lockPlatformPublisherTrust(e); err != nil {
			return err
		}
		allowNoNetwork := operation == "raw_read"
		facts, err := loadPlatformSecretBindingFactsMode(
			e, installation, true, allowNoNetwork)
		if err != nil {
			return err
		}
		if allowNoNetwork &&
			!platformRequiredSetupBinding(facts.appID, name) {
			return errPlatformManagementInput
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
		if principals != "" {
			plan.Principals = platformSecretBindingPrincipalList(principals)
		}
		if operation != "raw_read" {
			if plan.NetworkTargetSHA256, plan.NetworkTarget, err = platformSecretBindingReviewTarget(facts, target); err != nil {
				return err
			}
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
	if err != nil || !bytes.Equal(canonical, []byte(raw)) || plan.Format != platformSecretBindingReviewFormat || !platformValidUUID(plan.InstallationID) || !platformDirectoryAppID.MatchString(plan.AppID) || !platformDirectoryAppID.MatchString(plan.PublisherID) || !platformValidUUID(plan.ControllerEpoch) || !platformValidSHA(plan.ReleaseSHA256) || !platformValidUUID(plan.BindingID) || plan.BindingRevision < 0 || !platformSecretBindingNamePattern.MatchString(plan.Name) || !platformValidUUID(plan.CredentialID) || !validPlatformCredentialLabel(plan.CredentialLabel) || (plan.Operation != "http_bearer" && plan.Operation != "http_api_key" && plan.Operation != "raw_read") || (plan.Operation == "raw_read" && (!platformRequiredSetupBinding(plan.AppID, plan.Name) || plan.NetworkPolicySHA256 != "")) || (plan.Operation != "raw_read" && !platformValidSHA(plan.NetworkPolicySHA256)) || !platformPlanPrincipalsValid(plan) || !platformPlanTargetValid(plan) || !platformValidSHA(plan.ActorAuthority) || plan.Generation < 1 || plan.InstallationRevision < 1 || plan.PolicyRevision < 1 || plan.TrustRevision < 1 || plan.CredentialRevision < 1 || plan.CredentialValueRevision < 1 {
		return platformSecretBindingReview{}, errAppPlatformCredentialUnavailable
	}
	return plan, nil
}

// platformPlanTargetValid: a review names its target (a digest and an https
// origin) or neither, and a raw read names none.
func platformPlanTargetValid(plan platformSecretBindingReview) bool {
	if plan.NetworkTargetSHA256 == "" && plan.NetworkTarget == "" {
		return true
	}
	return plan.Operation != "raw_read" && platformValidSHA(plan.NetworkTargetSHA256) && len(plan.NetworkTarget) <= 512 && strings.HasPrefix(plan.NetworkTarget, "https://")
}

func platformPlanPrincipalsValid(plan platformSecretBindingReview) bool {
	principals, ok := platformSecretBindingPrincipalsOfPlan(plan.Principals)
	return ok && (plan.Operation != "raw_read" || principals == "")
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
		current, err := loadPlatformSecretBindingFactsMode(
			e, plan.InstallationID, true, plan.Operation == "raw_read")
		networkRaw := current.networkPolicyRaw
		current.networkPolicyRaw = ""
		if err != nil || current != planFacts {
			return errAppPlatformPlanChanged
		}
		var label, credentialState string
		var credentialRevision, valueRevision int64
		if err := e.queryRow(`SELECT label,state,revision,value_revision FROM app_platform_credentials WHERE id=?`, plan.CredentialID).Scan(&label, &credentialState, &credentialRevision, &valueRevision); err != nil || credentialState != "active" || label != plan.CredentialLabel || credentialRevision != plan.CredentialRevision || valueRevision != plan.CredentialValueRevision {
			return errAppPlatformPlanChanged
		}
		principals, _ := platformSecretBindingPrincipalsOfPlan(plan.Principals)
		// A review that names a target writes the target scope, if the target is
		// still one the grant has; any other writes the whole-policy scope.
		bindingScope := platformSecretBindingPolicy{operation: plan.Operation, networkPolicySHA256: plan.NetworkPolicySHA256, principals: principals}
		if plan.NetworkTargetSHA256 != "" {
			current.networkPolicyRaw = networkRaw
			if platformSecretBindingTargetPresent(current, plan.NetworkTargetSHA256) != nil {
				return errAppPlatformPlanChanged
			}
			bindingScope.networkPolicySHA256, bindingScope.networkTargetSHA256 = "", plan.NetworkTargetSHA256
		}
		policy, err := canonicalPlatformSecretBindingPolicy(bindingScope)
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
			result = platformSecretBindingMetadata{ID: plan.BindingID, Name: plan.Name, CredentialID: plan.CredentialID, CredentialLabel: label, CredentialValueRevision: plan.CredentialValueRevision, Operation: plan.Operation, State: "active", PolicyRevision: plan.PolicyRevision, Revision: 1, CreatedMS: now, UpdatedMS: now, Principals: platformSecretBindingPrincipalList(principals)}
		} else {
			changed, err := e.exec(`UPDATE app_platform_secret_bindings SET credential_id=?,credential_value_revision=?,operation=?,state='active',policy_json=?,policy_revision=?,review_plan_id=?,revision=revision+1,updated_ms=?,revoked_ms=NULL WHERE id=? AND installation_id=? AND name=? AND revision=? AND state IN ('active','review_required')`, plan.CredentialID, plan.CredentialValueRevision, plan.Operation, string(policy), plan.PolicyRevision, planID, now, plan.BindingID, plan.InstallationID, plan.Name, plan.BindingRevision)
			if err := platformObjectCAS(changed, err); err != nil {
				return errAppPlatformPlanChanged
			}
			result = platformSecretBindingMetadata{ID: plan.BindingID, Name: plan.Name, CredentialID: plan.CredentialID, CredentialLabel: label, CredentialValueRevision: plan.CredentialValueRevision, Operation: plan.Operation, State: "active", PolicyRevision: plan.PolicyRevision, Revision: plan.BindingRevision + 1, CreatedMS: now, UpdatedMS: now, Principals: platformSecretBindingPrincipalList(principals)}
		}
		if err := platformApplySetupPhase(
			e, plan.InstallationID, now); err != nil {
			return err
		}
		changed, err := e.exec(`UPDATE app_platform_review_plans SET consumed_ms=?,updated_ms=? WHERE id=? AND consumed_ms IS NULL AND plan_sha256=? AND expires_ms>?`, now, now, planID, planSHA, now)
		if err := platformObjectCAS(changed, err); err != nil {
			return errAppPlatformPlanChanged
		}
		detail, _ := json.Marshal(struct {
			Name, Operation                    string
			Revision                           int64
			Principals                         []string
			NetworkTargetSHA256, NetworkTarget string `json:",omitempty"`
		}{plan.Name, plan.Operation, result.Revision, result.Principals, plan.NetworkTargetSHA256, plan.NetworkTarget})
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
		rows, err := e.query(`SELECT b.id,b.name,b.credential_id,c.label,b.credential_value_revision,b.operation,b.state,b.policy_revision,b.revision,b.created_ms,b.updated_ms,b.policy_json,b.review_plan_id FROM app_platform_secret_bindings b JOIN app_platform_credentials c ON c.id=b.credential_id WHERE b.installation_id=? ORDER BY b.name`, installation)
		if err != nil {
			return err
		}
		var reviews []string
		for rows.Next() {
			var m platformSecretBindingMetadata
			var policyRaw, reviewID string
			if err := rows.Scan(&m.ID, &m.Name, &m.CredentialID, &m.CredentialLabel, &m.CredentialValueRevision, &m.Operation, &m.State, &m.PolicyRevision, &m.Revision, &m.CreatedMS, &m.UpdatedMS, &policyRaw, &reviewID); err != nil {
				rows.Close()
				return err
			}
			// A policy that does not parse authorizes nobody.
			m.Principals = []string{}
			if policy, err := parsePlatformSecretBindingPolicy([]byte(policyRaw)); err == nil {
				m.Principals = platformSecretBindingPrincipalList(policy.principals)
				m.NetworkTargetSHA256 = policy.networkTargetSHA256
			}
			result = append(result, m)
			reviews = append(reviews, reviewID)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		// The origin is the one the consumed review showed the admin, and only
		// when that review is for the digest the binding holds.
		for i := range result {
			if result[i].NetworkTargetSHA256 == "" {
				continue
			}
			var raw string
			if e.queryRow(`SELECT plan_json FROM app_platform_review_plans WHERE id=? AND installation_id=?`, reviews[i], installation).Scan(&raw) == nil {
				if plan, err := parsePlatformSecretBindingReview(raw); err == nil && plan.NetworkTargetSHA256 == result[i].NetworkTargetSHA256 {
					result[i].NetworkTarget = plan.NetworkTarget
				}
			}
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
		var installation, policyRaw, revokedTarget string
		if err := e.queryRow(`SELECT installation_id,policy_json
			FROM app_platform_secret_bindings WHERE id=?`, id).Scan(
			&installation, &policyRaw); err != nil {
			return errAppPlatformOperationConflict
		}
		if policy, err := parsePlatformSecretBindingPolicy([]byte(policyRaw)); err == nil {
			revokedTarget = policy.networkTargetSHA256
		}
		changed, err := e.exec(`UPDATE app_platform_secret_bindings SET state='revoked',revision=revision+1,updated_ms=?,revoked_ms=? WHERE id=? AND revision=? AND state<>'revoked'`, control.nowMS, control.nowMS, id, revision)
		if err := platformObjectCAS(changed, err); err != nil {
			return errAppPlatformOperationConflict
		}
		if err := platformApplySetupPhase(
			e, installation, control.nowMS); err != nil {
			return err
		}
		detail, _ := json.Marshal(struct {
			Revision            int64  `json:"revision"`
			NetworkTargetSHA256 string `json:"network_target_sha256,omitempty"`
		}{revision + 1, revokedTarget})
		if _, err := e.exec(`INSERT INTO audit_log(at,actor,actor_ou,action,target_type,target_id,detail,ip) VALUES(?,?,?,'app_platform.secret_binding_revoked','app_platform_secret_binding',?,?,'')`, time.UnixMilli(control.nowMS).UTC().Format(time.RFC3339), actor.username, actor.ouID, id, string(detail)); err != nil {
			return err
		}
		_, err = platformRefreshActor(e, session, actor, control)
		return err
	})
}

// loadPlatformSecretBindingUse is the viewer path's binding read.
func loadPlatformSecretBindingUse(e migExec, installation, name string, facts platformSecretBindingFacts, dest platformBindingDestination) (platformSecretBindingUse, string, error) {
	return loadPlatformSecretBindingUseFor(e, installation, name, facts, platformPrincipalViewer, dest)
}

// loadPlatformSecretBindingUseFor reads the active binding for one principal
// (platformPrincipalViewer or platformPrincipalService) for a destination. The
// reviewed policy must name that principal, and the consumed review must say the
// same. Nothing is decrypted here.
//
// The destination is judged first: a URL the grant does not select, or one that
// selects a plain-http target, is refused before any binding, review or
// credential is read. A target binding (3-dev.1) is then valid while its target
// is one of the current policy and the one the destination selects, whatever else
// that policy gained or lost; it is not tied to the policy revision or the policy
// digest. An older binding stays tied to both.
func loadPlatformSecretBindingUseFor(e migExec, installation, name string, facts platformSecretBindingFacts, principal string, dest platformBindingDestination) (platformSecretBindingUse, string, error) {
	var use platformSecretBindingUse
	if dest.url != "" {
		if _, err := platformSecretBindingSelect(facts, dest); err != nil {
			return use, "", err
		}
	}
	var policyRaw, reviewID, sealed string
	if err := e.queryRow(`SELECT id,credential_id,credential_value_revision,operation,policy_json,policy_revision,review_plan_id,revision FROM app_platform_secret_bindings WHERE installation_id=? AND name=? AND state='active'`, installation, name).
		Scan(&use.id, &use.credentialID, &use.credentialValueRevision, &use.operation, &policyRaw, &use.policyRevision, &reviewID, &use.revision); err != nil {
		return use, "", platformBindingReadError(err)
	}
	if !platformValidUUID(reviewID) || !platformValidUUID(use.id) || !platformValidUUID(use.credentialID) || use.credentialValueRevision < 1 || use.revision < 1 {
		return use, "", errAppPlatformCredentialUnavailable
	}
	policy, err := parsePlatformSecretBindingPolicy([]byte(policyRaw))
	if err != nil || !policy.allows(principal) || policy.operation != use.operation || (use.operation != "http_bearer" && use.operation != "http_api_key") {
		return use, "", errAppPlatformCredentialUnavailable
	}
	wholePolicy := policy.networkTargetSHA256 == ""
	if wholePolicy {
		if use.policyRevision != facts.policyRevision || validatePlatformHTTPSecretBindingPolicy([]byte(policyRaw), use.operation, facts.networkPolicySHA) != nil {
			return use, "", errAppPlatformCredentialUnavailable
		}
	} else if platformSecretBindingTargetAllowed(facts, policy.networkTargetSHA256, dest) != nil {
		return use, "", errAppPlatformCredentialUnavailable
	}
	use.principals, use.networkTargetSHA256 = policy.principals, policy.networkTargetSHA256
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
	if err := e.queryRow(`SELECT plan_json,plan_sha256,consumed_ms FROM app_platform_review_plans WHERE id=? AND action='update' AND installation_id=?`, reviewID, installation).Scan(&raw, &digest, &consumed); err != nil {
		return use, "", platformBindingReadError(err)
	} else if !consumed.Valid || platformDigest([]byte(raw)) != digest {
		return use, "", errAppPlatformCredentialUnavailable
	}
	plan, err := parsePlatformSecretBindingReview(raw)
	if planPrincipals, _ := platformSecretBindingPrincipalsOfPlan(plan.Principals); err != nil || planPrincipals != policy.principals || plan.InstallationID != installation || plan.AppID != facts.appID || plan.PublisherID != facts.publisherID || plan.Generation != facts.generation || plan.InstallationRevision != facts.installationRevision || plan.TrustRevision != facts.trustRevision || plan.BindingID != use.id || use.revision != plan.BindingRevision+1 || plan.Name != name || plan.CredentialID != use.credentialID || plan.CredentialValueRevision != use.credentialValueRevision || plan.Operation != use.operation || plan.NetworkTargetSHA256 != policy.networkTargetSHA256 || (wholePolicy && (plan.PolicyRevision != facts.policyRevision || plan.NetworkPolicySHA256 != facts.networkPolicySHA)) || plan.ReleaseSHA256 != facts.releaseSHA || plan.ControllerEpoch != facts.controllerEpoch {
		return use, "", errAppPlatformCredentialUnavailable
	}
	var label, state string
	var valueRevision, revision int64
	if err := e.queryRow(`SELECT label,state,value_revision,revision,sealed_value FROM app_platform_credentials WHERE id=?`, use.credentialID).Scan(&label, &state, &valueRevision, &revision, &sealed); err != nil {
		return use, "", platformBindingReadError(err)
	} else if label != plan.CredentialLabel || state != "active" || valueRevision != use.credentialValueRevision || revision != plan.CredentialRevision || sealed == "" {
		return use, "", errAppPlatformCredentialUnavailable
	}
	return use, sealed, nil
}

// errPlatformSecretBindingStore marks a binding read that failed for a reason of
// the store (a database error, a cancelled or timed-out context), as opposed to
// a binding that is absent or does not match. The first is no verdict on the
// binding; every caller that treats a refusal as a denial must tell them apart.
var errPlatformSecretBindingStore = errors.New("credential binding store unavailable")

// platformBindingReadError classifies a failed binding query: no row is a plain
// refusal, anything else is a store failure. Both still satisfy
// errAppPlatformCredentialUnavailable, as every failure of this reader always did.
func platformBindingReadError(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return errAppPlatformCredentialUnavailable
	}
	return fmt.Errorf("%w: %w: %v", errAppPlatformCredentialUnavailable, errPlatformSecretBindingStore, err)
}

func (s *Server) resolvePlatformSecretBindingCommand(e migExec, installation, name string, dest platformBindingDestination) (platformSecretBindingUse, string, []byte, error) {
	return s.resolvePlatformSecretBindingCommandFor(e, installation, name, platformPrincipalViewer, dest)
}

func (s *Server) resolvePlatformSecretBindingCommandFor(e migExec, installation, name, principal string, dest platformBindingDestination) (platformSecretBindingUse, string, []byte, error) {
	facts, err := loadPlatformSecretBindingFacts(e, installation, false)
	if err != nil {
		return platformSecretBindingUse{}, "", nil, err
	}
	use, sealed, err := loadPlatformSecretBindingUseFor(e, installation, name, facts, principal, dest)
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

// platformSecretBindingTargetChoice is a target of the grant a binding can be
// reviewed for: an https one, by id and origin.
type platformSecretBindingTargetChoice struct {
	ID     string `json:"id"`
	Origin string `json:"origin"`
}

// listPlatformSecretBindingTargets is the grant's https targets, sorted by id,
// for the review form. A grant that cannot be read has none.
func (s *Server) listPlatformSecretBindingTargets(session platformActorSession, installation string) ([]platformSecretBindingTargetChoice, error) {
	if !platformValidUUID(installation) {
		return nil, errPlatformManagementInput
	}
	result := []platformSecretBindingTargetChoice{}
	err := s.withAppPlatformActor(session, false, func(e migExec, actor platformActorFacts, control platformActorControl) error {
		if facts, err := loadPlatformSecretBindingFacts(e, installation, false); err == nil {
			if policy, err := platformSecretBindingTargets(facts); err == nil {
				for _, t := range policy.Targets {
					if strings.HasPrefix(t.Origin, "https://") {
						result = append(result, platformSecretBindingTargetChoice{ID: t.ID, Origin: t.Origin})
					}
				}
			}
		}
		_, err := platformRefreshActor(e, session, actor, control)
		return err
	})
	return result, err
}
