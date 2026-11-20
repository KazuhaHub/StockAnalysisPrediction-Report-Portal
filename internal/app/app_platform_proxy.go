package app

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/transport"
)

// The reviewed forward proxy, Portal side (doc 184). An administrator reviews the hosts a backend's CONNECT
// tunnels may reach: a plan from a fresh observation of the runner (PRPX0001), then a second call that
// consumes it. The plan is an 'update' plan of app_platform_review_plans whose target release is NULL,
// like the capability policy review of doc 175, and with a format of its own so neither review ever reads
// the other's plans. The grant row keeps its install-time review plan: the policy it holds is backed by
// the latest consumed proxy plan, which the session loader checks.
const (
	platformProxyReviewFormat = "portal.proxy-policy-review/1-dev.1"
	platformProxyLifetime     = 5 * time.Minute
	platformProxyAudit        = "app_platform.proxy_policy_changed"
	platformProxyCapability   = appplatform.ProxyCapability
)

var errPlatformProxyPolicy = errors.New("proxy policy review unavailable")

// platformProxyPlan is the closed canonical plan. Policy is the canonical policy the consume writes, or
// "{}" for a withdrawal.
type platformProxyPlan struct {
	Format               string                 `json:"format"`
	InstallationID       string                 `json:"installation_id"`
	AppID                string                 `json:"app_id"`
	PublisherID          string                 `json:"publisher_id"`
	Generation           int64                  `json:"generation"`
	InstallationRevision int64                  `json:"installation_revision"`
	PolicyRevision       int64                  `json:"policy_revision"`
	TrustRevision        int64                  `json:"trust_revision"`
	ControllerEpoch      string                 `json:"controller_epoch"`
	ReleaseSHA256        string                 `json:"release_sha256"`
	Capability           string                 `json:"capability"`
	PreviousPolicySHA256 string                 `json:"previous_policy_sha256"`
	Policy               string                 `json:"policy"`
	PolicySHA256         string                 `json:"policy_sha256"`
	HostsAdded           []string               `json:"hosts_added"`
	HostsRemoved         []string               `json:"hosts_removed"`
	DailyBytesOld        int64                  `json:"daily_bytes_old"`
	DailyBytesNew        int64                  `json:"daily_bytes_new"`
	Runner               transport.ProxyProfile `json:"runner"`
	ActorAuthority       string                 `json:"actor_authority"`
}

// parsePlatformProxyPlan accepts only the exact canonical encoding of a plan whose own fields are valid.
// It reads nothing from the database.
func parsePlatformProxyPlan(raw string) (platformProxyPlan, error) {
	var plan platformProxyPlan
	fail := errPlatformProxyPolicy
	if len(raw) == 0 || len(raw) > 65536 {
		return plan, fail
	}
	d := json.NewDecoder(bytes.NewReader([]byte(raw)))
	d.DisallowUnknownFields()
	if d.Decode(&plan) != nil {
		return platformProxyPlan{}, fail
	}
	canonical, err := json.Marshal(plan)
	if err != nil || !bytes.Equal(canonical, []byte(raw)) {
		return platformProxyPlan{}, fail
	}
	if plan.Format != platformProxyReviewFormat || plan.Capability != platformProxyCapability ||
		!platformValidUUID(plan.InstallationID) || !platformDirectoryAppID.MatchString(plan.AppID) || !platformDirectoryAppID.MatchString(plan.PublisherID) ||
		!platformValidUUID(plan.ControllerEpoch) || !platformValidSHA(plan.ReleaseSHA256) || !platformValidSHA(plan.PreviousPolicySHA256) || !platformValidSHA(plan.ActorAuthority) ||
		plan.Generation < 1 || plan.InstallationRevision < 1 || plan.PolicyRevision < 1 || plan.TrustRevision < 1 ||
		plan.HostsAdded == nil || plan.HostsRemoved == nil || !appplatform.ValidProxyHosts(plan.HostsAdded) || !appplatform.ValidProxyHosts(plan.HostsRemoved) ||
		plan.DailyBytesOld < appplatform.ProxyDailyBytesMin || plan.DailyBytesOld > appplatform.ProxyDailyBytesMax ||
		plan.Runner != transport.NewProxyProfile() || platformDigest([]byte(plan.Policy)) != plan.PolicySHA256 {
		return platformProxyPlan{}, fail
	}
	if plan.Policy == appplatform.EmptyProxyPolicyJSON {
		if plan.DailyBytesNew != 0 || len(plan.HostsAdded) != 0 {
			return platformProxyPlan{}, fail
		}
		return plan, nil
	}
	parsed, err := appplatform.ParseProxyPolicy([]byte(plan.Policy))
	if err != nil || parsed.Policy.DailyBytes != plan.DailyBytesNew {
		return platformProxyPlan{}, fail
	}
	return plan, nil
}

// platformProxyPlanPattern selects proxy plans out of the 'update' plans of an installation. The format is
// the first member of the canonical encoding; the exact parse is what decides.
func platformProxyPlanPattern() string {
	return `{"format":"` + platformProxyReviewFormat + `"%`
}

// platformProxyPlanRow is a stored proxy plan, verified against its own digest and canonical encoding.
type platformProxyPlanRow struct {
	id   string
	plan platformProxyPlan
}

// loadPlatformLatestProxyPlan reads the installation's latest consumed proxy plan. Plans are ordered by the
// policy revision they were made at: each consume raises it, so a later plan always sorts after an earlier one.
func loadPlatformLatestProxyPlan(e migExec, installation string) (platformProxyPlanRow, error) {
	var row platformProxyPlanRow
	var raw, digest, epoch string
	var source sql.NullString
	if err := e.queryRow(`SELECT id,plan_json,plan_sha256,source_release_sha256,controller_epoch
		FROM app_platform_review_plans
		WHERE installation_id=? AND action='update' AND consumed_ms IS NOT NULL AND target_release_sha256 IS NULL
		AND plan_json LIKE ?
		ORDER BY policy_revision DESC,consumed_ms DESC,id DESC LIMIT 1`, installation, platformProxyPlanPattern()).
		Scan(&row.id, &raw, &digest, &source, &epoch); err != nil {
		return row, err
	}
	plan, err := parsePlatformProxyPlan(raw)
	if err != nil || platformDigest([]byte(raw)) != digest || !source.Valid || source.String != plan.ReleaseSHA256 ||
		epoch != plan.ControllerEpoch || plan.InstallationID != installation || !platformValidUUID(row.id) {
		return platformProxyPlanRow{}, errPlatformProxyPolicy
	}
	row.plan = plan
	return row, nil
}

// loadPlatformProxyGrant reads a granted network.proxy row and proves it: the stored policy parses and is
// exactly the policy of the installation's latest consumed proxy plan, made for this release and epoch at or
// before this generation. A row that is not granted, or whose policy no review backs, is no grant.
func loadPlatformProxyGrant(e migExec, installation string, policyRevision int64, release, epoch string) (appplatform.ParsedProxyPolicy, error) {
	var zero appplatform.ParsedProxyPolicy
	var granted int
	var stored string
	if err := e.queryRow(`SELECT granted,policy_json FROM app_platform_capability_grants WHERE installation_id=? AND capability='network.proxy' AND policy_revision=?`, installation, policyRevision).Scan(&granted, &stored); err != nil || granted != 1 {
		return zero, errPlatformProxyPolicy
	}
	parsed, err := appplatform.ParseProxyPolicy([]byte(stored))
	if err != nil {
		return zero, errPlatformProxyPolicy
	}
	row, err := loadPlatformLatestProxyPlan(e, installation)
	if err != nil {
		return zero, errPlatformProxyPolicy
	}
	var generation int64
	if err := e.queryRow(`SELECT generation FROM app_platform_installations WHERE id=?`, installation).Scan(&generation); err != nil {
		return zero, errPlatformProxyPolicy
	}
	if row.plan.Policy != stored || row.plan.ReleaseSHA256 != release || row.plan.ControllerEpoch != epoch || row.plan.Generation > generation {
		return zero, errPlatformProxyPolicy
	}
	return parsed, nil
}

// platformProxyHostsDiff is the entries of next that current lacks and the reverse, both sorted.
func platformProxyHostsDiff(current, next []string) (added, removed []string) {
	added, removed = []string{}, []string{}
	for _, h := range next {
		if !slices.Contains(current, h) {
			added = append(added, h)
		}
	}
	for _, h := range current {
		if !slices.Contains(next, h) {
			removed = append(removed, h)
		}
	}
	return added, removed
}
