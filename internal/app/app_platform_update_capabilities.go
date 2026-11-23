package app

import (
	"context"
	"encoding/json"
	"slices"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/transport"
)

// platformUpdateRetainable says which optional capabilities an update keeps when the installation holds them
// granted and the new release still declares them. These are grants an administrator turned on after install
// whose proof does not depend on the release: network.proxy (its policy is carried with a fresh backing plan, see
// platformCarryProxyPlan) and principals.read (a plain switch). public.projection, machine.read, ingress.webhook
// and network.postgres are bound to a per-release disclosure or review, so a new release asks for them again.
// network.http and network.stream are not kept: their policy is rebuilt from the runtime target, and the review
// lists a policy that differs from the current one as reset (platformUpdateSelection.reset).
func platformUpdateRetainable(capability string) bool {
	return capability == appplatform.ProxyCapability || capability == "principals.read"
}

// platformProxyRunnerCheck is what the runner said about the proxy when an update was reviewed or activated. A
// carried proxy grant must pass the checks a real proxy approval runs: the runner serves the proxy with the
// profile this Portal enforces (observePlatformProxy) and its allowlist still holds every pin exactly. A nil check
// is a database-only rebuild (stage and queue), which only reproduces what a checked review recorded.
type platformProxyRunnerCheck struct {
	state transport.ProxyRunnerState
	err   error
}

func (c *platformProxyRunnerCheck) allows(policy appplatform.ProxyPolicy) error {
	if c == nil {
		return nil
	}
	if c.err != nil {
		return c.err
	}
	if !platformProxyPinsListed(c.state, policy.Pins) {
		return transport.ErrProxyUnsupported
	}
	return nil
}

// platformUpdateSelection is the capability selection of an update with what the review must show about it.
type platformUpdateSelection struct {
	capabilities []platformSelectedCapability
	// kept are the retained optional grants. keptProxy is the carried proxy policy, for the review to show.
	kept      []string
	keptProxy *platformKeptProxy
	// reset are the network capabilities granted before and after whose policy is the release default, not the
	// one currently stored.
	reset []string
}

// platformKeptProxy is the proxy policy an update carries, shown to the administrator in the review.
type platformKeptProxy struct {
	Hosts      []string               `json:"hosts"`
	Pins       []appplatform.ProxyPin `json:"pins"`
	DailyBytes int64                  `json:"daily_bytes"`
}

// platformUpdateCapabilities is the capability selection of an update to release m. It starts from the install
// selection (required capabilities granted, optional ones declined) and then keeps every retainable optional
// capability the installation currently holds granted, with its current policy, as long as the new release still
// declares it. A capability the new release no longer declares is not in the selection at all, so it is removed;
// a newly declared optional capability stays declined. The rows are read with e, so a caller inside a
// transaction sees the rows it is about to replace. A carried proxy grant the runner no longer vouches for makes
// the whole selection fail rather than being carried or silently dropped.
func platformUpdateCapabilities(e migExec, installation string, m appplatform.Manifest, target *platformManagedInstallTarget, check *platformProxyRunnerCheck) (platformUpdateSelection, error) {
	var result platformUpdateSelection
	selection, err := platformInstallCapabilitySelection(m, target)
	if err != nil {
		return result, err
	}
	result.capabilities = selection
	rows, err := readPlatformCapabilityRows(e, installation)
	if err != nil {
		return result, err
	}
	rowOf := func(name string) *platformCapabilityRow {
		if i := slices.IndexFunc(rows, func(r platformCapabilityRow) bool { return r.name == name }); i >= 0 {
			return &rows[i]
		}
		return nil
	}
	var policy int64
	var epoch, release string
	loaded := false
	for i := range selection {
		c := &selection[i]
		row := rowOf(c.capability)
		if c.granted && (c.capability == platformCapabilityNetworkHTTP || c.capability == "network.stream") && row != nil && row.granted == 1 && row.restriction != c.policy {
			result.reset = append(result.reset, c.capability)
		}
		if c.granted || !platformUpdateRetainable(c.capability) || !slices.Contains(m.Capabilities.Optional, c.capability) || row == nil || row.granted != 1 {
			continue
		}
		switch c.capability {
		case appplatform.ProxyCapability:
			if m.Backend == nil || m.NetworkProxy == nil {
				continue
			}
			parsed, err := appplatform.ParseProxyPolicy([]byte(row.restriction))
			if err != nil {
				continue
			}
			if !loaded {
				if err := e.queryRow(`SELECT i.policy_revision,i.controller_epoch,a.release_sha256 FROM app_platform_installations i JOIN app_platform_activations a ON a.installation_id=i.id AND a.state='active' WHERE i.id=?`, installation).Scan(&policy, &epoch, &release); err != nil {
					return result, err
				}
				loaded = true
			}
			// Only a grant the installation's latest consumed review still backs is carried: a stale row
			// stays declined rather than being re-attested by an update.
			if _, err := loadPlatformProxyGrant(e, installation, policy, release, epoch); err != nil {
				continue
			}
			if err := check.allows(parsed.Policy); err != nil {
				return result, err
			}
			c.granted, c.policy = true, row.restriction
			result.keptProxy = &platformKeptProxy{Hosts: slices.Clone(parsed.Policy.Hosts), Pins: append([]appplatform.ProxyPin{}, parsed.Policy.Pins...), DailyBytes: parsed.Policy.DailyBytes}
		default:
			c.granted, c.policy = true, "{}"
		}
		result.kept = append(result.kept, c.capability)
	}
	return result, nil
}

// platformUpdateCapabilitySelection is the selection alone, for callers that show nothing and check no runner.
func platformUpdateCapabilitySelection(e migExec, installation string, m appplatform.Manifest, target *platformManagedInstallTarget) ([]platformSelectedCapability, error) {
	r, err := platformUpdateCapabilities(e, installation, m, target, nil)
	return r.capabilities, err
}

// observePlatformProxyCheck asks the runner about the proxy when the installation holds a granted proxy grant, so
// that a review or activation that would carry it can check the runner. It is called outside any transaction.
// With no granted grant there is nothing to carry and no check is needed.
func (s *Server) observePlatformProxyCheck(ctx context.Context, e migExec, installation string, endpoint platformRuntimeEndpoint) *platformProxyRunnerCheck {
	var granted int
	if err := e.queryRow(`SELECT granted FROM app_platform_capability_grants WHERE installation_id=? AND capability='network.proxy'`, installation).Scan(&granted); err != nil || granted != 1 {
		return nil
	}
	state, err := s.observePlatformProxy(ctx, endpoint)
	return &platformProxyRunnerCheck{state: state, err: err}
}

// platformCarryProxyPlan writes the consumed proxy plan that backs a carried network.proxy grant under the new
// release and returns its id ("" when no proxy grant is carried). loadPlatformProxyGrant proves a granted row
// against the installation's latest consumed proxy plan for the active release, so an update to another release
// needs a plan of its own for the grant to stay usable. The plan changes nothing: it names the policy the row
// already holds (no host or pin added or removed), it follows the convention of a real plan (the policy revision
// and installation revision it was made at, which are the source's), and it says in carried_from_plan which plan
// it carries. It is written in the transaction that activates the update the administrator confirmed with the
// retained grant in its review, with the actor and trust facts of that update plan.
func platformCarryProxyPlan(e migExec, installation, updatePlanID, release string, generation, sourcePolicy int64, selection []platformSelectedCapability, now int64) (string, error) {
	var stored string
	for _, c := range selection {
		if c.capability == appplatform.ProxyCapability && c.granted {
			stored = c.policy
		}
	}
	if stored == "" {
		return "", nil
	}
	parsed, err := appplatform.ParseProxyPolicy([]byte(stored))
	if err != nil {
		return "", errAppPlatformStageEvidence
	}
	previous, err := loadPlatformLatestProxyPlan(e, installation)
	if err != nil || previous.plan.Policy != stored {
		return "", errAppPlatformStageEvidence
	}
	var appID, publisher, epoch, scope, username, created, authority string
	var installationRevision, trustRevision, accountGeneration int64
	if err := e.queryRow(`SELECT app_id,publisher_id,controller_epoch,actor_scope,COALESCE(username,''),account_created,account_generation,authority_fingerprint,installation_revision,trust_revision FROM app_platform_review_plans WHERE id=?`, updatePlanID).
		Scan(&appID, &publisher, &epoch, &scope, &username, &created, &accountGeneration, &authority, &installationRevision, &trustRevision); err != nil {
		return "", err
	}
	plan := platformProxyPlan{
		Format: platformProxyReviewFormatPins, InstallationID: installation, AppID: appID, PublisherID: publisher,
		Generation: generation, InstallationRevision: installationRevision, PolicyRevision: sourcePolicy, TrustRevision: trustRevision,
		ControllerEpoch: epoch, ReleaseSHA256: release, Capability: platformProxyCapability,
		PreviousPolicySHA256: platformDigest([]byte(stored)), Policy: stored, PolicySHA256: platformDigest([]byte(stored)),
		HostsAdded: []string{}, HostsRemoved: []string{}, DailyBytesOld: parsed.Policy.DailyBytes, DailyBytesNew: parsed.Policy.DailyBytes,
		Runner: transport.NewProxyProfile(), ActorAuthority: authority,
		PinsAdded: []appplatform.ProxyPin{}, PinsRemoved: []appplatform.ProxyPin{}, PinsChanged: []platformProxyPinChange{},
		RunnerPins:      append([]appplatform.ProxyPin{}, parsed.Policy.Pins...),
		CarriedFromPlan: previous.id,
	}
	raw, err := json.Marshal(plan)
	if err != nil || len(raw) > 65536 {
		return "", errAppPlatformStageEvidence
	}
	if _, err := parsePlatformProxyPlan(string(raw)); err != nil {
		return "", errAppPlatformStageEvidence
	}
	id, err := platformNewUUID()
	if err != nil {
		return "", err
	}
	_, err = e.exec(`INSERT INTO app_platform_review_plans(id,installation_id,app_id,publisher_id,action,source_release_sha256,target_release_sha256,installation_revision,policy_revision,trust_revision,controller_epoch,actor_scope,username,account_created,account_generation,authority_fingerprint,plan_sha256,plan_json,expires_ms,consumed_ms,created_ms,updated_ms)
		VALUES(?,?,?,?,'update',?,NULL,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		id, installation, appID, publisher, release, installationRevision, sourcePolicy, trustRevision, epoch, scope, username, created, accountGeneration, authority,
		platformDigest(raw), string(raw), now, now, now, now)
	return id, err
}

// platformAuditUpdateKept records which optional grants an update kept, so that a carried grant (and the plan that
// backs it under the new release) can be told from one an administrator reviewed.
func platformAuditUpdateKept(e migExec, installation, updatePlanID, carriedPlan string, selection []platformSelectedCapability, now int64) error {
	kept := []string{}
	for _, c := range selection {
		if c.granted && platformUpdateRetainable(c.capability) {
			kept = append(kept, c.capability)
		}
	}
	if len(kept) == 0 {
		return nil
	}
	detail, err := json.Marshal(map[string]any{"update_plan_id": updatePlanID, "kept_capabilities": kept, "carried_proxy_plan_id": carriedPlan})
	if err != nil {
		return err
	}
	_, err = e.exec(`INSERT INTO audit_log(at,actor,actor_ou,action,target_type,target_id,detail,ip) VALUES(?,'system',0,'app_platform.update_kept_capabilities','app_platform_installation',?,?,'')`,
		time.UnixMilli(now).UTC().Format(time.RFC3339), installation, string(detail))
	return err
}

// platformDeclinedMatches says whether a stored decline list is the install-time one or that list with only
// retainable capabilities left out, the ones an update kept. Each omitted capability must have a grant row: an
// administrator may switch principals.read off again afterwards, so the row's state is not part of the proof,
// but a decline list cannot omit a capability the installation never held.
func platformDeclinedMatches(e migExec, installation string, actual, install []string) bool {
	if slices.Equal(actual, install) {
		return true
	}
	rows, err := readPlatformCapabilityRows(e, installation)
	if err != nil {
		return false
	}
	next := 0
	for _, c := range install {
		if next < len(actual) && actual[next] == c {
			next++
			continue
		}
		if !platformUpdateRetainable(c) || !slices.ContainsFunc(rows, func(r platformCapabilityRow) bool { return r.name == c }) {
			return false
		}
	}
	return next == len(actual)
}
