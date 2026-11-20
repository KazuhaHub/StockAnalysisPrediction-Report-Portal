package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/transport"
)

// observePlatformProxy asks the runner whether it serves the proxy (PRPX0002, then PRPX0001 for a runner that
// predates pins) before any transaction, as the capability policy review asks PRNT0001. A runner that does not,
// or that enforces other limits than this Portal's, refuses the review: nothing is planned or granted against
// it.
func (s *Server) observePlatformProxy(ctx context.Context, endpoint platformRuntimeEndpoint) (transport.ProxyRunnerState, error) {
	var state transport.ProxyRunnerState
	var err error
	if s.platformObserveProxy != nil {
		state, err = s.platformObserveProxy(ctx, endpoint)
	} else {
		state, err = platformQueryProxy(ctx, endpoint)
	}
	if err != nil {
		return transport.ProxyRunnerState{}, err
	}
	if state.Profile != transport.NewProxyProfile() {
		return transport.ProxyRunnerState{}, transport.ErrProxyUnsupported
	}
	return state, nil
}

// platformProxyPinsListed reports whether the runner serves pins and its allowlist holds every one of pins
// exactly (host, address and port). A review never grants a pin the operator has not listed.
func platformProxyPinsListed(state transport.ProxyRunnerState, pins []appplatform.ProxyPin) bool {
	if len(pins) == 0 {
		return true
	}
	if !state.PinsSupported {
		return false
	}
	for _, p := range pins {
		if !slices.Contains(state.Pins, p) {
			return false
		}
	}
	return true
}

// platformProxyRequest is what an administrator asks for: the hosts and the daily budget the grant should
// hold, made against a policy revision and the digest of the stored policy text. An empty host list withdraws
// the grant.
type platformProxyRequest struct {
	PolicyRevision       int64
	PreviousPolicySHA256 string
	Hosts                []string
	// Pins are the pinned destinations (doc 186); none is the same as before pins existed.
	Pins       []appplatform.ProxyPin
	DailyBytes int64
}

// platformProxyFacts is what a review and its consume must agree on, read with the installation row locked.
type platformProxyFacts struct {
	appID, publisherID, epoch, release   string
	generation, revision, policyRevision int64
	trustRevision                        int64
	declared                             []string
	granted                              bool
	stored                               string
	current                              appplatform.ParsedProxyPolicy
}

// loadPlatformProxyFacts reads, with the installation row locked: a ready, enabled installation with no
// unfinished operation or effect, whose active release declares network.proxy and whose background.run is
// granted, and the grant row at the current policy revision. The row is returned as stored; a caller that
// needs it to be a valid grant checks that itself.
func (s *Server) loadPlatformProxyFacts(e migExec, installation string, control platformActorControl) (platformProxyFacts, error) {
	var f platformProxyFacts
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
	if err != nil || parsed.SHA256 != f.release || parsed.Manifest.ID != f.appID || parsed.Manifest.Publisher != f.publisherID {
		return f, errAppPlatformInstallUnavailable
	}
	if parsed.Manifest.Backend == nil || parsed.Manifest.NetworkProxy == nil || !slices.Contains(parsed.Manifest.Capabilities.Optional, platformProxyCapability) {
		return f, errPlatformCapabilityPolicyBusy
	}
	f.declared = slices.Clone(parsed.Manifest.NetworkProxy.Hosts)
	var background int
	if err := e.queryRow(`SELECT granted FROM app_platform_capability_grants WHERE installation_id=? AND capability='background.run' AND policy_revision=?`, installation, f.policyRevision).Scan(&background); err != nil || background != 1 {
		return f, errPlatformCapabilityPolicyBusy
	}
	var granted int
	if err := e.queryRow(`SELECT granted,policy_json FROM app_platform_capability_grants WHERE installation_id=? AND capability='network.proxy' AND policy_revision=?`, installation, f.policyRevision).Scan(&granted, &f.stored); err != nil || (granted != 0 && granted != 1) {
		return f, errPlatformCapabilityPolicyBusy
	}
	f.granted = granted == 1
	return f, nil
}

// validate says whether the row is a valid state: off with the empty policy, or on with a policy the
// installation's latest consumed review backs. It is a separate step so that a consume can first tell a plan
// that was made against another policy from a row that is simply invalid.
func (f *platformProxyFacts) validate(e migExec, installation string) error {
	if !f.granted {
		if f.stored != appplatform.EmptyProxyPolicyJSON {
			return errPlatformCapabilityPolicyBusy
		}
		return nil
	}
	current, err := loadPlatformProxyGrant(e, installation, f.policyRevision, f.release, f.epoch)
	if err != nil {
		return errPlatformCapabilityPolicyBusy
	}
	f.current = current
	return nil
}

// platformProxyHosts is a request's host list in canonical form: sorted, with no entry twice.
func platformProxyHosts(hosts []string) ([]string, error) {
	if hosts == nil || len(hosts) > appplatform.MaxProxyHosts {
		return nil, errPlatformManagementInput
	}
	sorted := slices.Clone(hosts)
	slices.Sort(sorted)
	if !appplatform.ValidProxyHosts(sorted) {
		return nil, errPlatformManagementInput
	}
	return sorted, nil
}

// platformProxyPins is a request's pins in canonical form: sorted by host, valid, each host once, at most
// appplatform.MaxProxyPins. No pins is an empty, non-nil list.
func platformProxyPins(pins []appplatform.ProxyPin) ([]appplatform.ProxyPin, error) {
	if len(pins) > appplatform.MaxProxyPins {
		return nil, errPlatformManagementInput
	}
	sorted := append([]appplatform.ProxyPin{}, pins...)
	slices.SortFunc(sorted, func(a, b appplatform.ProxyPin) int { return strings.Compare(a.Host, b.Host) })
	if !appplatform.ValidProxyPins(sorted) {
		return nil, errPlatformManagementInput
	}
	return sorted, nil
}

type platformProxyResult struct {
	ID, SHA256, JSON string
	ExpiresMS        int64
	Plan             platformProxyPlan
}

func (s *Server) createPlatformProxyReview(ctx context.Context, session platformActorSession, endpoint platformRuntimeEndpoint, installation string, req platformProxyRequest) (platformProxyResult, error) {
	var result platformProxyResult
	if !platformValidUUID(installation) || req.PolicyRevision < 1 || !platformValidSHA(req.PreviousPolicySHA256) {
		return result, errPlatformManagementInput
	}
	hosts, err := platformProxyHosts(req.Hosts)
	if err != nil {
		return result, err
	}
	pins, err := platformProxyPins(req.Pins)
	if err != nil {
		return result, err
	}
	withdraw := len(hosts)+len(pins) == 0
	if withdraw && req.DailyBytes != 0 || !withdraw && (req.DailyBytes < appplatform.ProxyDailyBytesMin || req.DailyBytes > appplatform.ProxyDailyBytesMax) {
		return result, errPlatformManagementInput
	}
	if !withdraw {
		// The policy itself must be valid (the bound on all entries, a pinned name that is also an exact
		// host) before the runner is asked, so malformed input is an input error whatever the runner says.
		if _, err := appplatform.NormalizeProxyPolicy(appplatform.ProxyPolicy{Hosts: hosts, Pins: pins, DailyBytes: req.DailyBytes}); err != nil {
			return result, errPlatformManagementInput
		}
	}
	// Withdrawing a grant is never refused for want of the runner: an old, offline or changed runner is no
	// reason to leave a reviewed proxy on.
	state := transport.ProxyRunnerState{Profile: transport.NewProxyProfile()}
	if !withdraw {
		var err error
		if state, err = s.observePlatformProxy(ctx, endpoint); err != nil {
			return result, err
		}
		// A pin is granted only when the operator has listed the exact destination: a runner without pin
		// support, or one that lacks the entry, is as unsupported as a runner without the proxy.
		if !platformProxyPinsListed(state, pins) {
			return result, transport.ErrProxyUnsupported
		}
	}
	err = s.withPlatformTrustTransactionContext(ctx, session, func(e migExec, actor platformActorFacts, control platformActorControl) error {
		if err := lockPlatformPublisherTrust(e); err != nil {
			return err
		}
		f, err := s.loadPlatformProxyFacts(e, installation, control)
		if err != nil {
			return err
		}
		if f.policyRevision != req.PolicyRevision || platformDigest([]byte(f.stored)) != req.PreviousPolicySHA256 {
			return errAppPlatformPlanChanged
		}
		// Neither is a grant that no review backs any more: it can still be withdrawn.
		if err := f.validate(e, installation); err != nil && !withdraw {
			return err
		}
		oldHosts, oldPins, oldDaily := []string{}, []appplatform.ProxyPin{}, appplatform.ProxyDailyBytesMax
		if f.granted {
			if stored, err := appplatform.ParseProxyPolicy([]byte(f.stored)); err == nil {
				oldHosts, oldPins, oldDaily = stored.Policy.Hosts, stored.Policy.Pins, stored.Policy.DailyBytes
			}
		}
		policy := appplatform.EmptyProxyPolicyJSON
		if withdraw {
			if !f.granted {
				return errPlatformManagementInput
			}
		} else {
			raw, err := appplatform.CanonicalProxyPolicy(appplatform.ProxyPolicy{Hosts: hosts, Pins: pins, DailyBytes: req.DailyBytes})
			if err != nil {
				return errPlatformManagementInput
			}
			policy = string(raw)
			if f.granted && policy == f.stored {
				return errPlatformManagementInput
			}
		}
		added, removed := platformProxyHostsDiff(oldHosts, hosts)
		pinsAdded, pinsRemoved, pinsChanged := platformProxyPinsDiff(oldPins, pins)
		plan := platformProxyPlan{
			Format: platformProxyReviewFormatPins, InstallationID: installation, AppID: f.appID, PublisherID: f.publisherID,
			Generation: f.generation, InstallationRevision: f.revision, PolicyRevision: f.policyRevision, TrustRevision: f.trustRevision,
			ControllerEpoch: f.epoch, ReleaseSHA256: f.release, Capability: platformProxyCapability,
			PreviousPolicySHA256: req.PreviousPolicySHA256, Policy: policy, PolicySHA256: platformDigest([]byte(policy)),
			HostsAdded: added, HostsRemoved: removed, DailyBytesOld: oldDaily, DailyBytesNew: req.DailyBytes,
			Runner: state.Profile, ActorAuthority: actor.authorityFingerprint,
			PinsAdded: pinsAdded, PinsRemoved: pinsRemoved, PinsChanged: pinsChanged, RunnerPins: append([]appplatform.ProxyPin{}, pins...),
		}
		raw, err := json.Marshal(plan)
		if err != nil || len(raw) > 65536 {
			return errPlatformManagementInput
		}
		if _, err := parsePlatformProxyPlan(string(raw)); err != nil {
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
		expires := control.nowMS + platformProxyLifetime.Milliseconds()
		digest := platformDigest(raw)
		if _, err := e.exec(`INSERT INTO app_platform_review_plans(id,installation_id,app_id,publisher_id,action,source_release_sha256,target_release_sha256,installation_revision,policy_revision,trust_revision,controller_epoch,actor_scope,username,account_created,account_generation,authority_fingerprint,plan_sha256,plan_json,expires_ms,consumed_ms,created_ms,updated_ms)
			VALUES(?,?,?,?,'update',?,NULL,?,?,?,?,?,?,?,?,?,?,?,?,NULL,?,?)`,
			planID, installation, f.appID, f.publisherID, f.release, f.revision, f.policyRevision, f.trustRevision,
			f.epoch, actor.scope, actor.username, actor.created, actor.generation, actor.authorityFingerprint, digest, string(raw), expires, control.nowMS, control.nowMS); err != nil {
			return err
		}
		result = platformProxyResult{ID: planID, SHA256: digest, JSON: string(raw), ExpiresMS: expires, Plan: plan}
		_, err = platformRefreshActor(e, session, actor, control)
		return err
	})
	if err != nil {
		return platformProxyResult{}, err
	}
	return result, nil
}

type platformProxyChange struct {
	PolicyRevision int64
}

func (s *Server) consumePlatformProxyReview(ctx context.Context, session platformActorSession, endpoint platformRuntimeEndpoint, planID, planSHA string) (platformProxyChange, error) {
	var result platformProxyChange
	if !platformValidUUID(planID) || !platformValidSHA(planSHA) {
		return result, errPlatformManagementInput
	}
	// The runner is asked before the transaction. What it says matters unless the plan withdraws the grant,
	// which is decided from the plan itself, below.
	state, observeErr := s.observePlatformProxy(ctx, endpoint)
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
		plan, err := parsePlatformProxyPlan(raw)
		if err != nil || plan.InstallationID != installation.String || plan.ActorAuthority != authority || plan.ReleaseSHA256 != source || plan.ControllerEpoch != epoch ||
			plan.InstallationRevision != installationRevision || plan.PolicyRevision != policyRevision || plan.TrustRevision != trustRevision {
			return errAppPlatformPlanChanged
		}
		f, err := s.loadPlatformProxyFacts(e, plan.InstallationID, control)
		if err != nil {
			return err
		}
		if f.appID != plan.AppID || f.publisherID != plan.PublisherID || f.generation != plan.Generation || f.revision != plan.InstallationRevision ||
			f.policyRevision != plan.PolicyRevision || f.trustRevision != plan.TrustRevision || f.epoch != plan.ControllerEpoch || f.release != plan.ReleaseSHA256 ||
			platformDigest([]byte(f.stored)) != plan.PreviousPolicySHA256 {
			return errAppPlatformPlanChanged
		}
		withdrawal := plan.Policy == appplatform.EmptyProxyPolicyJSON
		if err := f.validate(e, plan.InstallationID); err != nil && !withdrawal {
			return err
		}
		// What the runner reports now must be what the plan was made against. A withdrawal does not wait
		// for the runner.
		if !withdrawal {
			if observeErr != nil {
				return observeErr
			}
			if plan.Runner != state.Profile {
				return errAppPlatformPlanChanged
			}
			// Every pin of the plan must still be an entry of the runner's allowlist, exactly: an operator
			// edit between the plan and the consume is a refusal, not a grant of a pin nobody lists.
			if !platformProxyPinsListed(state, plan.RunnerPins) {
				return transport.ErrProxyUnsupported
			}
		}
		now := control.nowMS
		next := f.policyRevision + 1
		grantedNew := boolInt(plan.Policy != appplatform.EmptyProxyPolicyJSON)
		changed, err := e.exec(`UPDATE app_platform_capability_grants SET granted=?,policy_json=?,policy_revision=?,updated_ms=?
			WHERE installation_id=? AND capability='network.proxy' AND policy_revision=? AND granted=? AND policy_json=?`,
			grantedNew, plan.Policy, next, now, plan.InstallationID, f.policyRevision, boolInt(f.granted), f.stored)
		if err := platformObjectCAS(changed, err); err != nil {
			return errAppPlatformPlanChanged
		}
		// Only policy_revision moves: revision stays, so every record that pinned the installation
		// revision keeps matching. The open egress session sees the new revision as drift and closes,
		// which cuts the installation's tunnels; the coordinator opens the next with the new policy.
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
		// Bindings that stay active move to the new policy revision with the installation.
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
			"capability": platformProxyCapability, "plan_id": planID, "policy_revision": next,
			"previous_policy_sha256": plan.PreviousPolicySHA256, "policy_sha256": plan.PolicySHA256,
			"added": plan.HostsAdded, "removed": plan.HostsRemoved,
			"pins_added": platformProxyPinLabels(plan.PinsAdded), "pins_removed": platformProxyPinLabels(plan.PinsRemoved), "pins_changed": platformProxyPinChangeRows(plan.PinsChanged),
			"daily_bytes_old": plan.DailyBytesOld, "daily_bytes_new": plan.DailyBytesNew,
		})
		if err != nil {
			return err
		}
		if _, err := e.exec(`INSERT INTO audit_log(at,actor,actor_ou,action,target_type,target_id,detail,ip) VALUES(?,?,?,?,'app_platform_installation',?,?,'')`, time.UnixMilli(now).UTC().Format(time.RFC3339), actor.username, actor.ouID, platformProxyAudit, plan.InstallationID, string(detail)); err != nil {
			return err
		}
		result.PolicyRevision = next
		_, err = platformRefreshActor(e, session, actor, control)
		return err
	})
	if err != nil {
		return platformProxyChange{}, err
	}
	return result, nil
}

// platformProxyPinLabels writes each pin as "host → address:port" for the audit row; no pins is an empty list.
func platformProxyPinLabels(pins []appplatform.ProxyPin) []string {
	out := make([]string, 0, len(pins))
	for _, p := range pins {
		out = append(out, p.Label())
	}
	return out
}

// platformProxyPinChangeRows writes each changed pin as {host, old, new} with "address:port" for the audit row.
func platformProxyPinChangeRows(changes []platformProxyPinChange) []platformProxyPinChangeRow {
	out := make([]platformProxyPinChangeRow, 0, len(changes))
	for _, c := range changes {
		out = append(out, platformProxyPinChangeRow{Host: c.Host, Old: c.Old.Endpoint(), New: c.New.Endpoint()})
	}
	return out
}

type platformProxyPinChangeRow struct {
	Host string `json:"host"`
	Old  string `json:"old"`
	New  string `json:"new"`
}
