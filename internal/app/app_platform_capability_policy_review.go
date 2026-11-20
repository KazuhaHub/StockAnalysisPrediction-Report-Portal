package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/netip"
	"reflect"
	"slices"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/transport"
)

var errPlatformCapabilityPolicyBusy = errors.New("installation is not ready for a capability policy change")

// errPlatformPrivateTargetNotAllowed says a private target of the policy is not
// admitted by the runner's observed private-network allowlist.
var errPlatformPrivateTargetNotAllowed = errors.New("a private target is not admitted by the runner allowlist")

// platformCapabilityObservation is what a review reads from the runner, as plain
// data: the identity it registered, its managed and network profiles and the
// PRNT0001 answer (the operator's private-network allowlist).
type platformCapabilityObservation struct {
	Runtime platformRuntimeRegistration
	Managed transport.ManagedRuntimeProfile
	Network transport.NetworkRuntimeProfile
	Targets transport.NetworkTargets
}

func (s *Server) observePlatformCapabilityRuntime(ctx context.Context, session platformActorSession, endpoint platformRuntimeEndpoint) (platformCapabilityObservation, error) {
	if s.platformObserveCapability != nil {
		return s.platformObserveCapability(ctx, session, endpoint)
	}
	var zero platformCapabilityObservation
	facts, err := s.observePlatformManagedRuntime(ctx, session, endpoint)
	if err != nil {
		return zero, err
	}
	network, err := s.observePlatformNetworkRuntime(ctx, session, endpoint)
	if err != nil {
		return zero, err
	}
	profile := network.observation.Profile()
	if !samePlatformManagedRuntime(facts.runtime, network.runtime) || transport.ValidateNetworkRuntimeProfile(profile) != nil || !profile.WritableBackend {
		return zero, errAppPlatformRuntimeConflict
	}
	targets, err := platformQueryNetworkTargets(ctx, endpoint)
	if err != nil {
		return zero, err
	}
	return platformCapabilityObservation{Runtime: facts.runtime, Managed: facts.observation.Profile(), Network: profile, Targets: targets}, nil
}

// platformCapabilityBounds replaces shared policy bounds; a zero field keeps the current value.
type platformCapabilityBounds struct {
	RequestBytes  int64
	ResponseBytes int64
	Deadline      time.Duration
	Concurrency   int
}

type platformCapabilityRequest struct {
	PolicyRevision       int64
	PreviousPolicySHA256 string
	Add                  []appplatform.HTTPTarget
	Remove               []string
	Bounds               platformCapabilityBounds
}

// platformMergeCapabilityPolicy applies a change to the current policy. It
// refuses a rename (an added id that already exists, or an added origin that a
// removed target had), a change that changes nothing, and the removal of the last
// target; the merged policy must be a valid v2 policy. A change that changes
// nothing is accepted only as a rebind: the policy is kept and the review binds it
// to the runner's new allowlist digest.
func platformMergeCapabilityPolicy(current appplatform.HTTPPolicyV2, req platformCapabilityRequest, rebind bool) (appplatform.HTTPPolicyV2, []appplatform.HTTPTarget, []appplatform.HTTPTarget, error) {
	var zero appplatform.HTTPPolicyV2
	bad := errPlatformManagementInput
	if len(req.Add) > platformCapabilityTargetsMax || len(req.Remove) > appplatform.MaxHTTPPolicyTargets {
		return zero, nil, nil, bad
	}
	byID := make(map[string]appplatform.HTTPTarget, len(current.Targets))
	for _, t := range current.Targets {
		byID[t.ID] = t
	}
	removedIDs := map[string]bool{}
	removedOrigins := map[string]bool{}
	var removed []appplatform.HTTPTarget
	for _, id := range req.Remove {
		t, ok := byID[id]
		if !ok || removedIDs[id] {
			return zero, nil, nil, bad
		}
		removedIDs[id] = true
		removedOrigins[t.Origin] = true
		removed = append(removed, t)
	}
	var retained []appplatform.HTTPTarget
	for _, t := range current.Targets {
		if !removedIDs[t.ID] {
			retained = append(retained, t)
		}
	}
	if len(retained) == 0 {
		return zero, nil, nil, bad
	}
	for _, t := range req.Add {
		if _, exists := byID[t.ID]; exists || removedOrigins[t.Origin] {
			return zero, nil, nil, bad
		}
	}
	merged := current
	merged.Targets = append(slices.Clone(retained), req.Add...)
	if req.Bounds.RequestBytes != 0 {
		merged.RequestBytes = req.Bounds.RequestBytes
	}
	if req.Bounds.ResponseBytes != 0 {
		merged.ResponseBytes = req.Bounds.ResponseBytes
	}
	if req.Bounds.Deadline != 0 {
		merged.Deadline = req.Bounds.Deadline
	}
	if req.Bounds.Concurrency != 0 {
		merged.Concurrency = req.Bounds.Concurrency
	}
	if !rebind && len(req.Add) == 0 && len(req.Remove) == 0 && merged.RequestBytes == current.RequestBytes && merged.ResponseBytes == current.ResponseBytes && merged.Deadline == current.Deadline && merged.Concurrency == current.Concurrency {
		return zero, nil, nil, bad
	}
	normalized, err := appplatform.NormalizeHTTPPolicyV2(merged)
	if err != nil {
		return zero, nil, nil, bad
	}
	index := map[string]appplatform.HTTPTarget{}
	for _, t := range normalized.Targets {
		index[t.ID] = t
	}
	added := make([]appplatform.HTTPTarget, 0, len(req.Add))
	for _, t := range req.Add {
		added = append(added, index[t.ID])
	}
	return normalized, added, removed, nil
}

// platformAllowlistEntry is the allowlist entry that admits a private target, or
// ok=false. An IP literal must lie in an entry whose port and scheme match; a DNS
// name needs some entry that admits its port and scheme (the addresses it
// resolves to are judged at call time).
func platformAllowlistEntry(networks []appplatform.PrivateNetwork, policy appplatform.HTTPPolicyV2, t appplatform.HTTPTarget) (appplatform.PrivateNetwork, bool) {
	var zero appplatform.PrivateNetwork
	if len(t.Methods) == 0 || len(t.Paths) == 0 {
		return zero, false
	}
	eff, err := policy.Select(t.Methods[0], t.Origin+t.Paths[0])
	if err != nil {
		return zero, false
	}
	for _, n := range networks {
		if !slices.Contains(n.Ports, int(eff.Port)) || (eff.Scheme == "http" && !n.PlainHTTP) {
			continue
		}
		if eff.Literal.IsValid() {
			prefix, err := netip.ParsePrefix(n.Prefix)
			if err != nil || !prefix.Contains(appplatform.Unmap(eff.Literal)) || !appplatform.Admit([]appplatform.PrivateNetwork{n}, eff.Literal, eff.Port, eff.Scheme) {
				continue
			}
		}
		return n, true
	}
	return zero, false
}

// platformCheckCapabilityPrivate checks every private target of the policy, the
// retained ones as well as the added ones, against the observed allowlist.
func platformCheckCapabilityPrivate(networks []appplatform.PrivateNetwork, policy appplatform.HTTPPolicyV2) error {
	for _, t := range policy.Targets {
		if t.Network != appplatform.HTTPNetworkPrivate {
			continue
		}
		if len(networks) == 0 {
			return errPlatformPrivateTargetNotAllowed
		}
		if _, ok := platformAllowlistEntry(networks, policy, t); !ok {
			return errPlatformPrivateTargetNotAllowed
		}
	}
	return nil
}

type platformCapabilityFacts struct {
	appID, publisherID, epoch, release, stream string
	generation, revision, policyRevision       int64
	trustRevision                              int64
	runtime                                    platformRuntimeRegistration
	grant                                      platformNetworkGrant
	current                                    appplatform.ParsedHTTPPolicyAny
}

// loadPlatformCapabilityFacts reads, with the installation row locked, what a
// review and its consume must agree on: a ready enabled installation with no
// unfinished operation or effect, a declared and currently valid network.http
// grant and the registered runtime.
func (s *Server) loadPlatformCapabilityFacts(e migExec, installation string, control platformActorControl) (platformCapabilityFacts, error) {
	var f platformCapabilityFacts
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
		return f, errPlatformCapabilityPolicyBusy
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
		return f, errPlatformCapabilityPolicyBusy
	}
	if err := e.queryRow(`SELECT revision FROM app_platform_publishers WHERE id=?`, f.publisherID).Scan(&f.trustRevision); err != nil || f.trustRevision < 1 {
		return f, errPlatformCapabilityPolicyBusy
	}
	var descriptor []byte
	if err := e.queryRow(`SELECT a.release_sha256,r.descriptor FROM app_platform_activations a JOIN app_platform_releases r ON r.sha256=a.release_sha256
		WHERE a.installation_id=? AND a.generation=? AND a.policy_revision=? AND a.state='active'`, installation, f.generation, f.policyRevision).Scan(&f.release, &descriptor); err != nil {
		return f, errPlatformCapabilityPolicyBusy
	}
	parsed, err := appplatform.ParseDescriptor(descriptor)
	if err != nil || parsed.SHA256 != f.release || parsed.Manifest.ID != f.appID || parsed.Manifest.Publisher != f.publisherID ||
		!slices.Contains(parsed.Manifest.Capabilities.Required, platformCapabilityNetworkHTTP) && !slices.Contains(parsed.Manifest.Capabilities.Optional, platformCapabilityNetworkHTTP) {
		return f, errAppPlatformInstallUnavailable
	}
	dek, err := s.readPlatformDEK(e)
	if err != nil {
		return f, err
	}
	defer clear(dek)
	runtime, seed, err := s.platformDataRuntimeLocked(e, f.epoch, dek, " FOR SHARE")
	clear(seed)
	if err != nil {
		return f, err
	}
	if runtime.id != runtimeID.String {
		return f, errAppPlatformRuntimeConflict
	}
	f.runtime = runtime
	grant, err := loadPlatformNetworkGrantForIntent(e, installation, platformNetworkGrantTarget{release: f.release, generation: f.generation, policy: f.policyRevision, runtime: runtime}, platformCapabilityNetworkHTTP)
	if err != nil {
		return f, errPlatformCapabilityPolicyBusy
	}
	current, err := appplatform.ParseHTTPPolicyAny([]byte(grant.policy))
	if err != nil {
		return f, errPlatformCapabilityPolicyBusy
	}
	f.grant, f.current = grant, current
	var stream string
	switch err := e.queryRow(`SELECT policy_json FROM app_platform_capability_grants WHERE installation_id=? AND capability='network.stream' AND granted=1 AND policy_revision=?`, installation, f.policyRevision).Scan(&stream); {
	case err == nil:
		f.stream = stream
	case !errors.Is(err, sql.ErrNoRows):
		return f, err
	}
	return f, nil
}

type platformCapabilityPolicyResult struct {
	ID, SHA256, JSON string
	ExpiresMS        int64
	Plan             platformCapabilityPolicyPlan
	// Previous is the policy the plan replaces, Paused the bindings its consume pauses and
	// PreviousNetworksSHA256 the allowlist digest the replaced policy was bound to, all read
	// in the plan's own transaction.
	Previous               appplatform.HTTPPolicyV2
	Paused                 []string
	PreviousNetworksSHA256 string
}

func (s *Server) createPlatformCapabilityPolicyReview(ctx context.Context, session platformActorSession, endpoint platformRuntimeEndpoint, installation string, req platformCapabilityRequest) (platformCapabilityPolicyResult, error) {
	var result platformCapabilityPolicyResult
	if !platformValidUUID(installation) || req.PolicyRevision < 1 || !platformValidSHA(req.PreviousPolicySHA256) ||
		len(req.Add) > platformCapabilityTargetsMax || len(req.Remove) > appplatform.MaxHTTPPolicyTargets {
		return result, errPlatformManagementInput
	}
	obs, err := s.observePlatformCapabilityRuntime(ctx, session, endpoint)
	if err != nil {
		return result, err
	}
	err = s.withPlatformTrustTransactionContext(ctx, session, func(e migExec, actor platformActorFacts, control platformActorControl) error {
		if err := lockPlatformPublisherTrust(e); err != nil {
			return err
		}
		f, err := s.loadPlatformCapabilityFacts(e, installation, control)
		if err != nil {
			return err
		}
		if f.policyRevision != req.PolicyRevision || platformDigest([]byte(f.grant.policy)) != req.PreviousPolicySHA256 {
			return errAppPlatformPlanChanged
		}
		if !samePlatformManagedRuntime(obs.Runtime, f.runtime) || !obs.Network.WritableBackend {
			return errAppPlatformRuntimeConflict
		}
		// A policy with a private target is bound to the allowlist digest its last review
		// saw; when the runner's allowlist has changed since, the review may change nothing
		// else and only bind the same policy to the new digest.
		bound := ""
		if latest, err := loadPlatformLatestCapabilityPlan(e, installation); err == nil && latest.id == f.grant.review {
			bound = latest.plan.PrivateNetworksSHA256
		}
		rebind := f.current.Policy.HasPrivate() && bound != obs.Targets.PrivateNetworksSHA256
		merged, added, removed, err := platformMergeCapabilityPolicy(f.current.Policy, req, rebind)
		if err != nil {
			return err
		}
		if err := platformCheckCapabilityPrivate(obs.Targets.PrivateNetworks, merged); err != nil {
			return err
		}
		rawPolicy, err := appplatform.CanonicalHTTPPolicyV2(merged)
		if err != nil {
			return errPlatformManagementInput
		}
		addedViews, err := platformCapabilityTargetViews(added)
		if err != nil {
			return errPlatformManagementInput
		}
		removedViews, err := platformCapabilityTargetViews(removed)
		if err != nil {
			return errPlatformManagementInput
		}
		r := f.runtime
		networks := obs.Targets.PrivateNetworks
		if networks == nil {
			networks = []appplatform.PrivateNetwork{}
		}
		plan := platformCapabilityPolicyPlan{
			Format: platformCapabilityPolicyFormat, InstallationID: installation, AppID: f.appID, PublisherID: f.publisherID,
			Generation: f.generation, InstallationRevision: f.revision, PolicyRevision: f.policyRevision, TrustRevision: f.trustRevision,
			ControllerEpoch: f.epoch, ReleaseSHA256: f.release, Capability: platformCapabilityNetworkHTTP,
			PreviousReviewPlanID: f.grant.review, PreviousPolicySHA256: req.PreviousPolicySHA256,
			Runtime: &platformManagedInstallTarget{
				RuntimeID: r.id, DeploymentID: r.deploymentID, ControllerEpoch: r.controllerEpoch, BindingRevision: r.revision,
				ControlRevision: r.controlRevision, Fingerprint: r.fingerprint, Profile: obs.Managed,
				NetworkPolicy: string(rawPolicy), NetworkStreamPolicy: f.stream, NetworkProfile: obs.Network,
			},
			TargetsAdded: addedViews, TargetsRemoved: removedViews,
			PrivateNetworksSHA256: obs.Targets.PrivateNetworksSHA256, PrivateNetworks: networks, ActorAuthority: actor.authorityFingerprint,
		}
		raw, err := json.Marshal(plan)
		if err != nil || len(raw) > 65536 {
			return errPlatformManagementInput
		}
		if _, err := parsePlatformCapabilityPolicyPlan(string(raw)); err != nil {
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
		expires := control.nowMS + platformCapabilityPolicyLifetime.Milliseconds()
		digest := platformDigest(raw)
		if _, err := e.exec(`INSERT INTO app_platform_review_plans(id,installation_id,app_id,publisher_id,action,source_release_sha256,target_release_sha256,installation_revision,policy_revision,trust_revision,controller_epoch,actor_scope,username,account_created,account_generation,authority_fingerprint,plan_sha256,plan_json,expires_ms,consumed_ms,created_ms,updated_ms)
			VALUES(?,?,?,?,'update',?,NULL,?,?,?,?,?,?,?,?,?,?,?,?,NULL,?,?)`,
			planID, installation, f.appID, f.publisherID, f.release, f.revision, f.policyRevision, f.trustRevision,
			f.epoch, actor.scope, actor.username, actor.created, actor.generation, actor.authorityFingerprint, digest, string(raw), expires, control.nowMS, control.nowMS); err != nil {
			return err
		}
		newTargets, err := platformCapabilityTargetDigests(merged)
		if err != nil {
			return errPlatformManagementInput
		}
		paused, err := platformCapabilityPausedBindings(e, installation, newTargets)
		if err != nil {
			return err
		}
		names := make([]string, 0, len(paused))
		for _, b := range paused {
			names = append(names, b.name)
		}
		result = platformCapabilityPolicyResult{ID: planID, SHA256: digest, JSON: string(raw), ExpiresMS: expires, Plan: plan, Previous: f.current.Policy, Paused: names, PreviousNetworksSHA256: bound}
		_, err = platformRefreshActor(e, session, actor, control)
		return err
	})
	if err != nil {
		return platformCapabilityPolicyResult{}, err
	}
	return result, nil
}

// platformSecretBindingTargetFormat is the target-scoped binding policy (doc 175 section 1.8).
const platformSecretBindingTargetFormat = "portal.secret-binding-policy/3-dev.1"

// platformCapabilityBindingPauses decides, for one active HTTP binding, whether a
// consumed review pauses it: every whole-policy binding (v1 and 2-dev.1) does, and
// a target-scoped one (3-dev.1) does when its target digest is not in the new
// policy. A paused binding stays review_required until it is reviewed again,
// whatever the policy becomes later.
func platformCapabilityBindingPauses(policyJSON string, newTargets map[string]bool) bool {
	var probe struct {
		Format              string `json:"format"`
		NetworkTargetSHA256 string `json:"network_target_sha256"`
	}
	if json.Unmarshal([]byte(policyJSON), &probe) != nil || probe.Format != platformSecretBindingTargetFormat {
		return true
	}
	return !newTargets[probe.NetworkTargetSHA256]
}

// platformCapabilityTargetDigests is the set of target digests of a policy.
func platformCapabilityTargetDigests(policy appplatform.HTTPPolicyV2) (map[string]bool, error) {
	digests := map[string]bool{}
	for _, t := range policy.Targets {
		sum, err := appplatform.HTTPTargetSHA256(t)
		if err != nil {
			return nil, err
		}
		digests[sum] = true
	}
	return digests, nil
}

type platformPausedBinding struct {
	id, name string
	revision int64
}

// platformCapabilityPausedBindings lists the active HTTP bindings a review that
// leaves the given targets pauses.
func platformCapabilityPausedBindings(e migExec, installation string, newTargets map[string]bool) ([]platformPausedBinding, error) {
	rows, err := e.query(`SELECT id,name,policy_json,revision FROM app_platform_secret_bindings WHERE installation_id=? AND state='active' AND operation IN ('http_bearer','http_api_key') ORDER BY name`, installation)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var pause []platformPausedBinding
	for rows.Next() {
		var b platformPausedBinding
		var policy string
		if err := rows.Scan(&b.id, &b.name, &policy, &b.revision); err != nil {
			return nil, err
		}
		if platformCapabilityBindingPauses(policy, newTargets) {
			pause = append(pause, b)
		}
	}
	return pause, rows.Err()
}

// platformCapabilityAuditTargets names the targets of a change by id and origin.
func platformCapabilityAuditTargets(targets []platformCapabilityTarget) []map[string]string {
	result := make([]map[string]string, 0, len(targets))
	for _, t := range targets {
		result = append(result, map[string]string{"id": t.ID, "origin": t.Origin})
	}
	return result
}

type platformCapabilityPolicyChange struct {
	PolicyRevision int64
	Paused         []string
}

func (s *Server) consumePlatformCapabilityPolicyReview(ctx context.Context, session platformActorSession, endpoint platformRuntimeEndpoint, planID, planSHA string) (platformCapabilityPolicyChange, error) {
	var result platformCapabilityPolicyChange
	if !platformValidUUID(planID) || !platformValidSHA(planSHA) {
		return result, errPlatformManagementInput
	}
	obs, err := s.observePlatformCapabilityRuntime(ctx, session, endpoint)
	if err != nil {
		return result, err
	}
	err = s.withPlatformTrustTransactionContext(ctx, session, func(e migExec, actor platformActorFacts, control platformActorControl) error {
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
		plan, err := parsePlatformCapabilityPolicyPlan(raw)
		if err != nil || plan.InstallationID != installation.String || plan.ActorAuthority != authority || plan.ReleaseSHA256 != source || plan.ControllerEpoch != epoch ||
			plan.InstallationRevision != installationRevision || plan.PolicyRevision != policyRevision || plan.TrustRevision != trustRevision {
			return errAppPlatformPlanChanged
		}
		f, err := s.loadPlatformCapabilityFacts(e, plan.InstallationID, control)
		if err != nil {
			return err
		}
		if f.appID != plan.AppID || f.publisherID != plan.PublisherID || f.generation != plan.Generation || f.revision != plan.InstallationRevision ||
			f.policyRevision != plan.PolicyRevision || f.trustRevision != plan.TrustRevision || f.epoch != plan.ControllerEpoch || f.release != plan.ReleaseSHA256 ||
			f.grant.review != plan.PreviousReviewPlanID || platformDigest([]byte(f.grant.policy)) != plan.PreviousPolicySHA256 {
			return errAppPlatformPlanChanged
		}
		r := f.runtime
		rt := plan.Runtime
		sameNetworks := len(plan.PrivateNetworks) == 0 && len(obs.Targets.PrivateNetworks) == 0 || reflect.DeepEqual(plan.PrivateNetworks, obs.Targets.PrivateNetworks)
		if !samePlatformManagedRuntime(obs.Runtime, r) || rt.RuntimeID != r.id || rt.DeploymentID != r.deploymentID || rt.ControllerEpoch != r.controllerEpoch ||
			rt.BindingRevision != r.revision || rt.ControlRevision != r.controlRevision || rt.Fingerprint != r.fingerprint ||
			rt.Profile != obs.Managed || rt.NetworkProfile != obs.Network || rt.NetworkStreamPolicy != f.stream ||
			plan.PrivateNetworksSHA256 != obs.Targets.PrivateNetworksSHA256 || !sameNetworks {
			return errAppPlatformPlanChanged
		}
		newPolicy, err := appplatform.ParseHTTPPolicyV2([]byte(rt.NetworkPolicy))
		if err != nil {
			return errAppPlatformPlanChanged
		}
		if err := platformCheckCapabilityPrivate(obs.Targets.PrivateNetworks, newPolicy.Policy); err != nil {
			return err
		}
		now := control.nowMS
		next := f.policyRevision + 1
		changed, err := e.exec(`UPDATE app_platform_capability_grants SET granted=1,policy_json=?,review_plan_id=?,policy_revision=?,updated_ms=?
			WHERE installation_id=? AND capability='network.http' AND granted=1 AND policy_revision=? AND review_plan_id=? AND policy_json=?`,
			rt.NetworkPolicy, planID, next, now, plan.InstallationID, f.policyRevision, plan.PreviousReviewPlanID, f.grant.policy)
		if err := platformObjectCAS(changed, err); err != nil {
			return errAppPlatformPlanChanged
		}
		// Only policy_revision moves: revision stays, so every record that pinned the installation
		// revision (target-scoped bindings among them) keeps matching.
		changed, err = e.exec(`UPDATE app_platform_installations SET policy_revision=policy_revision+1,updated_ms=? WHERE id=? AND policy_revision=? AND revision=?`,
			now, plan.InstallationID, f.policyRevision, f.revision)
		if err := platformObjectCAS(changed, err); err != nil {
			return errAppPlatformPlanChanged
		}
		if _, err := e.exec(`UPDATE app_platform_capability_grants SET policy_revision=?,updated_ms=? WHERE installation_id=? AND policy_revision=?`, next, now, plan.InstallationID, f.policyRevision); err != nil {
			return err
		}
		var stray int
		if err := e.queryRow(`SELECT COUNT(*) FROM app_platform_capability_grants WHERE installation_id=? AND policy_revision<>?`, plan.InstallationID, next).Scan(&stray); err != nil || stray != 0 {
			return errAppPlatformPlanChanged
		}
		changed, err = e.exec(`UPDATE app_platform_activations SET policy_revision=?,updated_ms=? WHERE installation_id=? AND generation=? AND policy_revision=? AND state='active'`, next, now, plan.InstallationID, f.generation, f.policyRevision)
		if err := platformObjectCAS(changed, err); err != nil {
			return errAppPlatformPlanChanged
		}
		if _, err := e.exec(`UPDATE app_platform_review_plans SET expires_ms=?,updated_ms=? WHERE installation_id=? AND consumed_ms IS NULL AND id<>? AND expires_ms>?`, now, now, plan.InstallationID, planID, now); err != nil {
			return err
		}
		newTargets, err := platformCapabilityTargetDigests(newPolicy.Policy)
		if err != nil {
			return errAppPlatformPlanChanged
		}
		pause, err := platformCapabilityPausedBindings(e, plan.InstallationID, newTargets)
		if err != nil {
			return err
		}
		result.Paused = []string{}
		for _, b := range pause {
			changed, err := e.exec(`UPDATE app_platform_secret_bindings SET state='review_required',revision=revision+1,updated_ms=? WHERE id=? AND revision=? AND state='active'`, now, b.id, b.revision)
			if err := platformObjectCAS(changed, err); err != nil {
				return errAppPlatformPlanChanged
			}
			result.Paused = append(result.Paused, b.name)
		}
		// Every binding that stays active (setup bindings and kept ones) moves to the new policy
		// revision with the installation, so none of them counts as missing afterwards.
		if _, err := e.exec(`UPDATE app_platform_secret_bindings SET policy_revision=?,updated_ms=? WHERE installation_id=? AND state='active' AND policy_revision=?`, next, now, plan.InstallationID, f.policyRevision); err != nil {
			return err
		}
		changed, err = e.exec(`UPDATE app_platform_review_plans SET consumed_ms=?,updated_ms=? WHERE id=? AND consumed_ms IS NULL AND plan_sha256=? AND expires_ms>?`, now, now, planID, planSHA, now)
		if err := platformObjectCAS(changed, err); err != nil {
			return errAppPlatformPlanChanged
		}
		if err := platformApplySetupPhase(e, plan.InstallationID, now); err != nil {
			return err
		}
		detail, err := json.Marshal(map[string]any{
			"capability": platformCapabilityNetworkHTTP, "plan_id": planID, "policy_revision": next,
			"previous_policy_sha256": plan.PreviousPolicySHA256, "policy_sha256": platformDigest([]byte(rt.NetworkPolicy)),
			"added": platformCapabilityAuditTargets(plan.TargetsAdded), "removed": platformCapabilityAuditTargets(plan.TargetsRemoved),
			"private_networks_sha256": plan.PrivateNetworksSHA256, "paused_bindings": result.Paused,
		})
		if err != nil {
			return err
		}
		if _, err := e.exec(`INSERT INTO audit_log(at,actor,actor_ou,action,target_type,target_id,detail,ip) VALUES(?,?,?,?,'app_platform_installation',?,?,'')`, time.UnixMilli(now).UTC().Format(time.RFC3339), actor.username, actor.ouID, platformCapabilityPolicyAudit, plan.InstallationID, string(detail)); err != nil {
			return err
		}
		result.PolicyRevision = next
		_, err = platformRefreshActor(e, session, actor, control)
		return err
	})
	if err != nil {
		return platformCapabilityPolicyChange{}, err
	}
	return result, nil
}
