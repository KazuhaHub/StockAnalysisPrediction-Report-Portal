package app

import (
	"encoding/json"
	"slices"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/transport"
)

// platformUpdateRetainable says which optional capabilities an update keeps when the installation holds them
// granted and the new release still declares them. These are grants an administrator turned on after install
// whose proof does not depend on the release: network.proxy (its policy is carried with a fresh backing plan, see
// platformCarryProxyPlan) and principals.read (a plain switch). public.projection, machine.read, ingress.webhook
// and network.postgres are bound to a per-release disclosure or review, so a new release asks for them again.
func platformUpdateRetainable(capability string) bool {
	return capability == appplatform.ProxyCapability || capability == "principals.read"
}

// platformUpdateCapabilitySelection is the capability selection of an update to release m. It starts from the
// install selection (required capabilities granted, optional ones declined) and then keeps every retainable
// optional capability the installation currently holds granted, with its current policy, as long as the new
// release still declares it. A capability the new release no longer declares is not in the selection at all, so
// it is removed; a newly declared optional capability stays declined. The rows are read with e, so a caller
// inside a transaction sees the rows it is about to replace.
func platformUpdateCapabilitySelection(e migExec, installation string, m appplatform.Manifest, target *platformManagedInstallTarget) ([]platformSelectedCapability, error) {
	selection, err := platformInstallCapabilitySelection(m, target)
	if err != nil {
		return nil, err
	}
	rows, err := readPlatformCapabilityRows(e, installation)
	if err != nil {
		return nil, err
	}
	var policy int64
	var epoch, release string
	loaded := false
	for i := range selection {
		c := &selection[i]
		if c.granted || !platformUpdateRetainable(c.capability) || !slices.Contains(m.Capabilities.Optional, c.capability) {
			continue
		}
		index := slices.IndexFunc(rows, func(r platformCapabilityRow) bool { return r.name == c.capability })
		if index < 0 || rows[index].granted != 1 {
			continue
		}
		row := rows[index]
		switch c.capability {
		case appplatform.ProxyCapability:
			if m.Backend == nil || m.NetworkProxy == nil || !appplatform.ValidProxyPolicyShape(row.restriction) {
				continue
			}
			if !loaded {
				if err := e.queryRow(`SELECT i.policy_revision,i.controller_epoch,a.release_sha256 FROM app_platform_installations i JOIN app_platform_activations a ON a.installation_id=i.id AND a.state='active' WHERE i.id=?`, installation).Scan(&policy, &epoch, &release); err != nil {
					return nil, err
				}
				loaded = true
			}
			// Only a grant the installation's latest consumed review still backs is carried: a stale row
			// stays declined rather than being re-attested by an update.
			if _, err := loadPlatformProxyGrant(e, installation, policy, release, epoch); err != nil {
				continue
			}
			c.granted, c.policy = true, row.restriction
		default:
			c.granted, c.policy = true, "{}"
		}
	}
	return selection, nil
}

// platformCarryProxyPlan writes the consumed proxy plan that backs a carried network.proxy grant under the new
// release. loadPlatformProxyGrant proves a granted row against the installation's latest consumed proxy plan for
// the active release, so an update to another release needs a plan of its own for the grant to stay usable. The
// plan changes nothing: it names the policy the row already holds (no host or pin added or removed), and it is
// written in the transaction that activates the update the administrator confirmed with the retained grant in its
// review. It takes the actor and trust facts of that update plan.
func platformCarryProxyPlan(e migExec, installation, updatePlanID, release string, generation, policyRevision int64, selection []platformSelectedCapability, now int64) error {
	var stored string
	for _, c := range selection {
		if c.capability == appplatform.ProxyCapability && c.granted {
			stored = c.policy
		}
	}
	if stored == "" {
		return nil
	}
	parsed, err := appplatform.ParseProxyPolicy([]byte(stored))
	if err != nil {
		return errAppPlatformStageEvidence
	}
	var appID, publisher, epoch, scope, username, created, authority string
	var installationRevision, trustRevision, accountGeneration int64
	if err := e.queryRow(`SELECT app_id,publisher_id,controller_epoch,actor_scope,COALESCE(username,''),account_created,account_generation,authority_fingerprint,installation_revision,trust_revision FROM app_platform_review_plans WHERE id=?`, updatePlanID).
		Scan(&appID, &publisher, &epoch, &scope, &username, &created, &accountGeneration, &authority, &installationRevision, &trustRevision); err != nil {
		return err
	}
	plan := platformProxyPlan{
		Format: platformProxyReviewFormatPins, InstallationID: installation, AppID: appID, PublisherID: publisher,
		Generation: generation, InstallationRevision: installationRevision, PolicyRevision: policyRevision, TrustRevision: trustRevision,
		ControllerEpoch: epoch, ReleaseSHA256: release, Capability: platformProxyCapability,
		PreviousPolicySHA256: platformDigest([]byte(stored)), Policy: stored, PolicySHA256: platformDigest([]byte(stored)),
		HostsAdded: []string{}, HostsRemoved: []string{}, DailyBytesOld: parsed.Policy.DailyBytes, DailyBytesNew: parsed.Policy.DailyBytes,
		Runner: transport.NewProxyProfile(), ActorAuthority: authority,
		PinsAdded: []appplatform.ProxyPin{}, PinsRemoved: []appplatform.ProxyPin{}, PinsChanged: []platformProxyPinChange{},
		RunnerPins: append([]appplatform.ProxyPin{}, parsed.Policy.Pins...),
	}
	raw, err := json.Marshal(plan)
	if err != nil || len(raw) > 65536 {
		return errAppPlatformStageEvidence
	}
	if _, err := parsePlatformProxyPlan(string(raw)); err != nil {
		return errAppPlatformStageEvidence
	}
	id, err := platformNewUUID()
	if err != nil {
		return err
	}
	_, err = e.exec(`INSERT INTO app_platform_review_plans(id,installation_id,app_id,publisher_id,action,source_release_sha256,target_release_sha256,installation_revision,policy_revision,trust_revision,controller_epoch,actor_scope,username,account_created,account_generation,authority_fingerprint,plan_sha256,plan_json,expires_ms,consumed_ms,created_ms,updated_ms)
		VALUES(?,?,?,?,'update',?,NULL,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		id, installation, appID, publisher, release, installationRevision, policyRevision, trustRevision, epoch, scope, username, created, accountGeneration, authority,
		platformDigest(raw), string(raw), now, now, now, now)
	return err
}
