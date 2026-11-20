package app

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/transport"
)

// The network.postgres grant review (doc 178, amendment C2). The capability is optional and install
// writes its row ungranted with the policy "{}". An administrator grants it, changes it or withdraws
// it with its own two-call review: a plan is created from a fresh observation of the runner (does it
// serve the probe; which private networks may a private target use), and a second call consumes it.
// The consume updates the existing grant row in place under a compare-and-swap and writes an audit
// row; it does not move the installation's policy revision, so no other record is disturbed. The
// plan lives in app_platform_review_plans as an 'update' plan with no target release, as the other
// capability plans do.
const (
	platformPostgresPolicyFormat   = "portal.postgres-policy-review/1-dev.1"
	platformPostgresPolicyLifetime = 5 * time.Minute
	platformPostgresPolicyAudit    = "app_platform.postgres_policy_changed"
)

var (
	errPlatformPostgresBusy        = errors.New("installation is not ready for a PostgreSQL policy change")
	errPlatformPostgresUnsupported = errors.New("the runner does not serve the PostgreSQL probe")
)

// platformPostgresObservation is what a review reads from the runner: whether it serves the probe
// and its private-network allowlist.
type platformPostgresObservation struct {
	Probe   transport.PostgresProbeProfile
	Targets transport.NetworkTargets
}

func (s *Server) observePlatformPostgresRuntime(ctx context.Context, endpoint platformRuntimeEndpoint, needTargets bool) (platformPostgresObservation, error) {
	if s.platformObservePostgres != nil {
		return s.platformObservePostgres(ctx, endpoint, needTargets)
	}
	var obs platformPostgresObservation
	probe, err := platformQueryPostgresProbe(ctx, endpoint)
	if err != nil {
		if errors.Is(err, transport.ErrPostgresProbeUnsupported) {
			return obs, errPlatformPostgresUnsupported
		}
		return obs, err
	}
	obs.Probe = probe
	if needTargets {
		if obs.Targets, err = platformQueryNetworkTargets(ctx, endpoint); err != nil {
			return obs, err
		}
	}
	return obs, nil
}

// platformPostgresTargetView is one target of a plan, with the digest the use-time bindings are
// matched to and the fingerprint of its reviewed certificate authority.
type platformPostgresTargetView struct {
	ID            string `json:"id"`
	Host          string `json:"host"`
	Port          int    `json:"port"`
	Database      string `json:"database"`
	Network       string `json:"network"`
	CAFingerprint string `json:"ca_sha256,omitempty"`
	Label         string `json:"label"`
	SHA256        string `json:"network_target_sha256"`
}

func platformPostgresTargetViews(policy appplatform.PostgresPolicy) ([]platformPostgresTargetView, error) {
	views := make([]platformPostgresTargetView, 0, len(policy.Targets))
	for _, t := range policy.Targets {
		digest, err := appplatform.PostgresTargetSHA256(t)
		if err != nil {
			return nil, err
		}
		view := platformPostgresTargetView{ID: t.ID, Host: t.Host, Port: t.Port, Database: t.Database, Network: t.Network, Label: platformPostgresTargetLabel(t), SHA256: digest}
		if t.CAPEM != "" {
			if _, view.CAFingerprint, err = appplatform.CanonicalPostgresCA(t.CAPEM); err != nil {
				return nil, err
			}
		}
		views = append(views, view)
	}
	return views, nil
}

type platformPostgresPolicyPlan struct {
	Format               string `json:"format"`
	InstallationID       string `json:"installation_id"`
	AppID                string `json:"app_id"`
	PublisherID          string `json:"publisher_id"`
	Generation           int64  `json:"generation"`
	InstallationRevision int64  `json:"installation_revision"`
	PolicyRevision       int64  `json:"policy_revision"`
	TrustRevision        int64  `json:"trust_revision"`
	ControllerEpoch      string `json:"controller_epoch"`
	ReleaseSHA256        string `json:"release_sha256"`
	// PreviousGranted and PreviousPolicySHA256 are the row the plan replaces; Granted and Policy
	// what it becomes ("{}" when it withdraws the grant).
	PreviousGranted       bool                         `json:"previous_granted"`
	PreviousPolicySHA256  string                       `json:"previous_policy_sha256"`
	Granted               bool                         `json:"granted"`
	Policy                string                       `json:"policy"`
	Targets               []platformPostgresTargetView `json:"targets"`
	PrivateNetworksSHA256 string                       `json:"private_networks_sha256"`
	PrivateNetworks       []appplatform.PrivateNetwork `json:"private_networks"`
	ActorAuthority        string                       `json:"actor_authority"`
}

// parsePlatformPostgresPolicyPlan accepts only the exact canonical encoding of a plan whose own
// fields are all valid. It reads nothing from the database.
func parsePlatformPostgresPolicyPlan(raw string) (platformPostgresPolicyPlan, error) {
	var plan platformPostgresPolicyPlan
	fail := errPlatformCapabilityPolicy
	if len(raw) == 0 || len(raw) > 65536 {
		return plan, fail
	}
	d := json.NewDecoder(bytes.NewReader([]byte(raw)))
	d.DisallowUnknownFields()
	if d.Decode(&plan) != nil {
		return platformPostgresPolicyPlan{}, fail
	}
	canonical, err := json.Marshal(plan)
	if err != nil || !bytes.Equal(canonical, []byte(raw)) {
		return platformPostgresPolicyPlan{}, fail
	}
	if plan.Format != platformPostgresPolicyFormat || !platformValidUUID(plan.InstallationID) || !platformDirectoryAppID.MatchString(plan.AppID) || !platformDirectoryAppID.MatchString(plan.PublisherID) ||
		!platformValidUUID(plan.ControllerEpoch) || !platformValidSHA(plan.ReleaseSHA256) || !platformValidSHA(plan.PreviousPolicySHA256) || !platformValidSHA(plan.ActorAuthority) ||
		plan.Generation < 1 || plan.InstallationRevision < 1 || plan.PolicyRevision < 1 || plan.TrustRevision < 1 ||
		plan.Targets == nil || plan.PrivateNetworks == nil || appplatform.ValidatePrivateNetworks(plan.PrivateNetworks) != nil {
		return platformPostgresPolicyPlan{}, fail
	}
	if len(plan.PrivateNetworks) == 0 {
		if plan.PrivateNetworksSHA256 != "" {
			return platformPostgresPolicyPlan{}, fail
		}
	} else if sum, err := appplatform.PrivateNetworksSHA256(plan.PrivateNetworks); err != nil || sum != plan.PrivateNetworksSHA256 {
		return platformPostgresPolicyPlan{}, fail
	}
	if !plan.Granted {
		if plan.Policy != "{}" || len(plan.Targets) != 0 {
			return platformPostgresPolicyPlan{}, fail
		}
		return plan, nil
	}
	parsed, err := appplatform.ParsePostgresPolicy([]byte(plan.Policy))
	if err != nil {
		return platformPostgresPolicyPlan{}, fail
	}
	views, err := platformPostgresTargetViews(parsed.Policy)
	if err != nil || !reflect.DeepEqual(views, plan.Targets) || parsed.Policy.HasPrivate() != (parsed.Policy.PrivateNetworksSHA256 != "") || parsed.Policy.PrivateNetworksSHA256 != "" && parsed.Policy.PrivateNetworksSHA256 != plan.PrivateNetworksSHA256 {
		return platformPostgresPolicyPlan{}, fail
	}
	return plan, nil
}

type platformPostgresFacts struct {
	appID, publisherID, epoch, release   string
	generation, revision, policyRevision int64
	trustRevision                        int64
	granted                              bool
	stored                               string
}

// loadPlatformPostgresFacts reads, with the installation row locked, what a review and its consume
// must agree on: a ready enabled installation with no unfinished operation or effect, a current
// release that declares network.postgres, and the grant row of the current policy revision.
func (s *Server) loadPlatformPostgresFacts(e migExec, installation string, control platformActorControl) (platformPostgresFacts, error) {
	var f platformPostgresFacts
	lock := ""
	if e.s.driver == "postgres" {
		lock = " FOR UPDATE"
	}
	var runtimeID sql.NullString
	var desired, phase string
	if err := e.queryRow(`SELECT app_id,publisher_id,generation,revision,policy_revision,controller_epoch,runtime_id,desired_state,phase FROM app_platform_installations WHERE id=?`+lock, installation).
		Scan(&f.appID, &f.publisherID, &f.generation, &f.revision, &f.policyRevision, &f.epoch, &runtimeID, &desired, &phase); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return f, errPlatformInstallationMissing
		}
		return f, err
	}
	if desired != "enabled" || phase != "ready" || !runtimeID.Valid || f.epoch != control.epoch || f.generation < 1 || f.revision < 1 || f.policyRevision < 1 {
		return f, errPlatformPostgresBusy
	}
	operations, err := platformUnresolvedOperationCount(e, installation)
	if err != nil {
		return f, err
	}
	effects, err := platformUnresolvedOperationEffectCount(e, installation)
	if err != nil {
		return f, err
	}
	if operations+effects != 0 {
		return f, errPlatformPostgresBusy
	}
	if err := e.queryRow(`SELECT revision FROM app_platform_publishers WHERE id=?`, f.publisherID).Scan(&f.trustRevision); err != nil || f.trustRevision < 1 {
		return f, errPlatformPostgresBusy
	}
	var descriptor []byte
	if err := e.queryRow(`SELECT a.release_sha256,r.descriptor FROM app_platform_activations a JOIN app_platform_releases r ON r.sha256=a.release_sha256
		WHERE a.installation_id=? AND a.generation=? AND a.policy_revision=? AND a.state='active'`, installation, f.generation, f.policyRevision).Scan(&f.release, &descriptor); err != nil {
		return f, errPlatformPostgresBusy
	}
	parsed, err := appplatform.ParseDescriptor(descriptor)
	if err != nil || parsed.SHA256 != f.release || parsed.Manifest.ID != f.appID || parsed.Manifest.Publisher != f.publisherID || parsed.Manifest.Backend == nil ||
		!slices.Contains(parsed.Manifest.Capabilities.Optional, platformCapabilityNetworkPostgres) {
		return f, errAppPlatformInstallUnavailable
	}
	var granted int
	if err := e.queryRow(`SELECT granted,policy_json FROM app_platform_capability_grants WHERE installation_id=? AND capability='network.postgres' AND policy_revision=?`+lock, installation, f.policyRevision).Scan(&granted, &f.stored); err != nil {
		return f, errPlatformPostgresBusy
	}
	f.granted = granted == 1
	if f.granted != (f.stored != "{}") || f.granted && !appplatform.ValidPostgresPolicy(f.stored) {
		return f, errPlatformPostgresBusy
	}
	return f, nil
}

type platformPostgresRequest struct {
	PolicyRevision       int64
	PreviousPolicySHA256 string
	// Targets empty withdraws the grant.
	Targets     []appplatform.PostgresTarget
	Deadline    time.Duration
	Concurrency int
}

type platformPostgresPolicyResult struct {
	ID, SHA256, JSON string
	ExpiresMS        int64
	Plan             platformPostgresPolicyPlan
}

func (s *Server) createPlatformPostgresPolicyReview(ctx context.Context, session platformActorSession, endpoint platformRuntimeEndpoint, installation string, req platformPostgresRequest) (platformPostgresPolicyResult, error) {
	var result platformPostgresPolicyResult
	if !platformValidUUID(installation) || req.PolicyRevision < 1 || !platformValidSHA(req.PreviousPolicySHA256) || len(req.Targets) > appplatform.MaxPostgresTargets {
		return result, errPlatformManagementInput
	}
	// The new policy first, without any digest: what is asked for must be a valid policy before
	// the runner is asked anything.
	grant := len(req.Targets) > 0
	var draft appplatform.PostgresPolicy
	if grant {
		draft = appplatform.PostgresPolicy{Targets: slices.Clone(req.Targets), Deadline: req.Deadline, Concurrency: req.Concurrency}
		if draft.HasPrivate() {
			draft.PrivateNetworksSHA256 = "0000000000000000000000000000000000000000000000000000000000000000"
		}
		if _, err := appplatform.NormalizePostgresPolicy(draft); err != nil {
			return result, errPlatformManagementInput
		}
	}
	var obs platformPostgresObservation
	if grant {
		var err error
		if obs, err = s.observePlatformPostgresRuntime(ctx, endpoint, draft.HasPrivate()); err != nil {
			return result, err
		}
	}
	err := s.withPlatformTrustTransactionContext(ctx, session, func(e migExec, actor platformActorFacts, control platformActorControl) error {
		if err := lockPlatformPublisherTrust(e); err != nil {
			return err
		}
		f, err := s.loadPlatformPostgresFacts(e, installation, control)
		if err != nil {
			return err
		}
		if f.policyRevision != req.PolicyRevision || platformDigest([]byte(f.stored)) != req.PreviousPolicySHA256 {
			return errAppPlatformPlanChanged
		}
		plan := platformPostgresPolicyPlan{
			Format: platformPostgresPolicyFormat, InstallationID: installation, AppID: f.appID, PublisherID: f.publisherID,
			Generation: f.generation, InstallationRevision: f.revision, PolicyRevision: f.policyRevision, TrustRevision: f.trustRevision,
			ControllerEpoch: f.epoch, ReleaseSHA256: f.release, PreviousGranted: f.granted, PreviousPolicySHA256: req.PreviousPolicySHA256,
			Granted: grant, Policy: "{}", Targets: []platformPostgresTargetView{}, PrivateNetworks: []appplatform.PrivateNetwork{}, ActorAuthority: actor.authorityFingerprint,
		}
		if grant {
			policy := draft
			policy.PrivateNetworksSHA256 = ""
			if policy.HasPrivate() {
				// A private target is judged against the allowlist the runner reports now, and the
				// policy is bound to its digest, which every probe command then carries.
				if !policy.CheckPrivate(obs.Targets.PrivateNetworks) {
					return errPlatformPrivateTargetNotAllowed
				}
				policy.PrivateNetworksSHA256 = obs.Targets.PrivateNetworksSHA256
				plan.PrivateNetworks = slices.Clone(obs.Targets.PrivateNetworks)
				if plan.PrivateNetworks == nil {
					plan.PrivateNetworks = []appplatform.PrivateNetwork{}
				}
				plan.PrivateNetworksSHA256 = obs.Targets.PrivateNetworksSHA256
			}
			rawPolicy, err := appplatform.CanonicalPostgresPolicy(policy)
			if err != nil {
				return errPlatformManagementInput
			}
			parsed, err := appplatform.ParsePostgresPolicy(rawPolicy)
			if err != nil {
				return errPlatformManagementInput
			}
			if plan.Targets, err = platformPostgresTargetViews(parsed.Policy); err != nil {
				return errPlatformManagementInput
			}
			plan.Policy = string(rawPolicy)
		}
		if !grant && !f.granted {
			// Withdrawing what is not granted changes nothing.
			return errPlatformManagementInput
		}
		if grant && f.granted && f.stored == plan.Policy {
			return errPlatformManagementInput
		}
		raw, err := json.Marshal(plan)
		if err != nil || len(raw) > 65536 {
			return errPlatformManagementInput
		}
		if _, err := parsePlatformPostgresPolicyPlan(string(raw)); err != nil {
			return errPlatformManagementInput
		}
		planID, err := platformNewUUID()
		if err != nil {
			return err
		}
		control, err = platformRefreshActor(e, session, actor, control)
		if err != nil {
			return err
		}
		expires := control.nowMS + platformPostgresPolicyLifetime.Milliseconds()
		digest := platformDigest(raw)
		if _, err := e.exec(`INSERT INTO app_platform_review_plans(id,installation_id,app_id,publisher_id,action,source_release_sha256,target_release_sha256,installation_revision,policy_revision,trust_revision,controller_epoch,actor_scope,username,account_created,account_generation,authority_fingerprint,plan_sha256,plan_json,expires_ms,consumed_ms,created_ms,updated_ms)
			VALUES(?,?,?,?,'update',?,NULL,?,?,?,?,?,?,?,?,?,?,?,?,NULL,?,?)`,
			planID, installation, f.appID, f.publisherID, f.release, f.revision, f.policyRevision, f.trustRevision,
			f.epoch, actor.scope, actor.username, actor.created, actor.generation, actor.authorityFingerprint, digest, string(raw), expires, control.nowMS, control.nowMS); err != nil {
			return err
		}
		result = platformPostgresPolicyResult{ID: planID, SHA256: digest, JSON: string(raw), ExpiresMS: expires, Plan: plan}
		_, err = platformRefreshActor(e, session, actor, control)
		return err
	})
	if err != nil {
		return platformPostgresPolicyResult{}, err
	}
	return result, nil
}

type platformPostgresPolicyChange struct {
	PolicyRevision int64
	Granted        bool
}

func (s *Server) consumePlatformPostgresPolicyReview(ctx context.Context, session platformActorSession, endpoint platformRuntimeEndpoint, planID, planSHA string) (platformPostgresPolicyChange, error) {
	var result platformPostgresPolicyChange
	if !platformValidUUID(planID) || !platformValidSHA(planSHA) {
		return result, errPlatformManagementInput
	}
	err := s.withPlatformTrustTransactionContext(ctx, session, func(e migExec, actor platformActorFacts, control platformActorControl) error {
		if err := lockPlatformPublisherTrust(e); err != nil {
			return err
		}
		lock := ""
		if e.s.driver == "postgres" {
			lock = " FOR UPDATE"
		}
		var raw, digest, action, source, scope, authority, username, created, epoch string
		var installation, target sql.NullString
		var expires, accountGeneration, installationRevision, policyRevision, trustRevision int64
		var consumed sql.NullInt64
		if err := e.queryRow(`SELECT plan_json,plan_sha256,action,source_release_sha256,target_release_sha256,installation_id,expires_ms,consumed_ms,installation_revision,policy_revision,trust_revision,controller_epoch,actor_scope,COALESCE(username,''),account_created,account_generation,authority_fingerprint FROM app_platform_review_plans WHERE id=?`+lock, planID).
			Scan(&raw, &digest, &action, &source, &target, &installation, &expires, &consumed, &installationRevision, &policyRevision, &trustRevision, &epoch, &scope, &username, &created, &accountGeneration, &authority); err != nil {
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
		if action != "update" || target.Valid || !installation.Valid || username != actor.username || created != actor.created || accountGeneration != actor.generation ||
			scope != actor.scope || authority != actor.authorityFingerprint || digest != planSHA || platformDigest([]byte(raw)) != planSHA {
			return errAppPlatformPlanChanged
		}
		plan, err := parsePlatformPostgresPolicyPlan(raw)
		if err != nil || plan.InstallationID != installation.String || plan.ActorAuthority != authority || plan.ReleaseSHA256 != source || plan.ControllerEpoch != epoch ||
			plan.InstallationRevision != installationRevision || plan.PolicyRevision != policyRevision || plan.TrustRevision != trustRevision {
			return errAppPlatformPlanChanged
		}
		f, err := s.loadPlatformPostgresFacts(e, plan.InstallationID, control)
		if err != nil {
			return err
		}
		if f.appID != plan.AppID || f.publisherID != plan.PublisherID || f.generation != plan.Generation || f.revision != plan.InstallationRevision ||
			f.policyRevision != plan.PolicyRevision || f.trustRevision != plan.TrustRevision || f.epoch != plan.ControllerEpoch || f.release != plan.ReleaseSHA256 ||
			f.granted != plan.PreviousGranted || platformDigest([]byte(f.stored)) != plan.PreviousPolicySHA256 {
			return errAppPlatformPlanChanged
		}
		if plan.Granted {
			// The runner is asked again: it must still serve the probe, and a policy bound to an
			// allowlist digest is only applied while the runner's allowlist is that one.
			obs, err := s.observePlatformPostgresRuntime(ctx, endpoint, plan.PrivateNetworksSHA256 != "")
			if err != nil {
				return err
			}
			policy, err := appplatform.ParsePostgresPolicy([]byte(plan.Policy))
			if err != nil {
				return errAppPlatformPlanChanged
			}
			if policy.Policy.HasPrivate() {
				same := len(plan.PrivateNetworks) == 0 && len(obs.Targets.PrivateNetworks) == 0 || reflect.DeepEqual(plan.PrivateNetworks, obs.Targets.PrivateNetworks)
				if plan.PrivateNetworksSHA256 != obs.Targets.PrivateNetworksSHA256 || !same {
					return errAppPlatformPlanChanged
				}
				if !policy.Policy.CheckPrivate(obs.Targets.PrivateNetworks) {
					return errPlatformPrivateTargetNotAllowed
				}
			}
		}
		now := control.nowMS
		changed, err := e.exec(`UPDATE app_platform_capability_grants SET granted=?,policy_json=?,updated_ms=?
			WHERE installation_id=? AND capability='network.postgres' AND policy_revision=? AND granted=? AND policy_json=?`,
			boolInt(plan.Granted), plan.Policy, now, plan.InstallationID, f.policyRevision, boolInt(plan.PreviousGranted), f.stored)
		if err := platformObjectCAS(changed, err); err != nil {
			return errAppPlatformPlanChanged
		}
		changed, err = e.exec(`UPDATE app_platform_review_plans SET consumed_ms=?,updated_ms=? WHERE id=? AND consumed_ms IS NULL AND plan_sha256=? AND expires_ms>?`, now, now, planID, planSHA, now)
		if err := platformObjectCAS(changed, err); err != nil {
			return errAppPlatformPlanChanged
		}
		targets := make([]map[string]string, 0, len(plan.Targets))
		for _, t := range plan.Targets {
			targets = append(targets, map[string]string{"id": t.ID, "label": t.Label, "network": t.Network, "ca_sha256": t.CAFingerprint})
		}
		detail, err := json.Marshal(map[string]any{
			"capability": platformCapabilityNetworkPostgres, "plan_id": planID, "policy_revision": f.policyRevision, "granted": plan.Granted,
			"previous_policy_sha256": plan.PreviousPolicySHA256, "policy_sha256": platformDigest([]byte(plan.Policy)),
			"targets": targets, "private_networks_sha256": plan.PrivateNetworksSHA256,
		})
		if err != nil {
			return err
		}
		if _, err := e.exec(`INSERT INTO audit_log(at,actor,actor_ou,action,target_type,target_id,detail,ip) VALUES(?,?,?,?,'app_platform_installation',?,?,'')`, time.UnixMilli(now).UTC().Format(time.RFC3339), actor.username, actor.ouID, platformPostgresPolicyAudit, plan.InstallationID, string(detail)); err != nil {
			return err
		}
		result = platformPostgresPolicyChange{PolicyRevision: f.policyRevision, Granted: plan.Granted}
		_, err = platformRefreshActor(e, session, actor, control)
		return err
	})
	if err != nil {
		return platformPostgresPolicyChange{}, err
	}
	return result, nil
}
