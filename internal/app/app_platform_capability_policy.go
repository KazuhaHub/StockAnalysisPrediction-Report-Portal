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

// The capability policy review (doc 175 section 2). An administrator changes the
// reviewed network.http destinations of a running installation without an
// update: a plan is created from a fresh observation of the runner, and a
// second call consumes it. Only network.http uses it, and the result is always a
// v2 policy. The plan lives in app_platform_review_plans as an 'update' plan
// whose source release is the installation's release and whose target release is
// NULL, so no operation row points at it.
const (
	platformCapabilityPolicyFormat   = "portal.capability-policy-review/1-dev.1"
	platformCapabilityPolicyLifetime = 5 * time.Minute
	platformCapabilityPolicyAudit    = "app_platform.capability_policy_changed"
	platformCapabilityNetworkHTTP    = "network.http"
	platformCapabilityTargetsMax     = 16
)

var errPlatformCapabilityPolicy = errors.New("capability policy review unavailable")

// platformCapabilityTarget is one target of a plan, in the digest's own key
// order, with the digest the use-time bindings are matched to.
type platformCapabilityTarget struct {
	ID            string   `json:"id"`
	Origin        string   `json:"origin"`
	Paths         []string `json:"paths"`
	Methods       []string `json:"methods"`
	Network       string   `json:"network"`
	RequestBytes  int64    `json:"request_bytes,omitempty"`
	ResponseBytes int64    `json:"response_bytes,omitempty"`
	DeadlineMS    int64    `json:"deadline_ms,omitempty"`
	SHA256        string   `json:"network_target_sha256"`
}

type platformCapabilityPolicyPlan struct {
	Format                string                        `json:"format"`
	InstallationID        string                        `json:"installation_id"`
	AppID                 string                        `json:"app_id"`
	PublisherID           string                        `json:"publisher_id"`
	Generation            int64                         `json:"generation"`
	InstallationRevision  int64                         `json:"installation_revision"`
	PolicyRevision        int64                         `json:"policy_revision"`
	TrustRevision         int64                         `json:"trust_revision"`
	ControllerEpoch       string                        `json:"controller_epoch"`
	ReleaseSHA256         string                        `json:"release_sha256"`
	Capability            string                        `json:"capability"`
	PreviousReviewPlanID  string                        `json:"previous_review_plan_id"`
	PreviousPolicySHA256  string                        `json:"previous_policy_sha256"`
	Runtime               *platformManagedInstallTarget `json:"runtime"`
	TargetsAdded          []platformCapabilityTarget    `json:"targets_added"`
	TargetsRemoved        []platformCapabilityTarget    `json:"targets_removed"`
	PrivateNetworksSHA256 string                        `json:"private_networks_sha256"`
	PrivateNetworks       []appplatform.PrivateNetwork  `json:"private_networks"`
	ActorAuthority        string                        `json:"actor_authority"`
}

func platformCapabilityTargetView(t appplatform.HTTPTarget) (platformCapabilityTarget, error) {
	digest, err := appplatform.HTTPTargetSHA256(t)
	if err != nil {
		return platformCapabilityTarget{}, err
	}
	return platformCapabilityTarget{
		ID: t.ID, Origin: t.Origin, Paths: slices.Clone(t.Paths), Methods: slices.Clone(t.Methods), Network: t.Network,
		RequestBytes: t.RequestBytes, ResponseBytes: t.ResponseBytes, DeadlineMS: t.Deadline.Milliseconds(), SHA256: digest,
	}, nil
}

func platformCapabilityTargetViews(targets []appplatform.HTTPTarget) ([]platformCapabilityTarget, error) {
	result := make([]platformCapabilityTarget, 0, len(targets))
	for _, t := range targets {
		view, err := platformCapabilityTargetView(t)
		if err != nil {
			return nil, err
		}
		result = append(result, view)
	}
	return result, nil
}

// parsePlatformCapabilityPolicyPlan accepts only the exact canonical encoding of
// a plan whose own fields are all valid. It reads nothing from the database.
func parsePlatformCapabilityPolicyPlan(raw string) (platformCapabilityPolicyPlan, error) {
	var plan platformCapabilityPolicyPlan
	fail := errPlatformCapabilityPolicy
	if len(raw) == 0 || len(raw) > 65536 {
		return plan, fail
	}
	d := json.NewDecoder(bytes.NewReader([]byte(raw)))
	d.DisallowUnknownFields()
	if d.Decode(&plan) != nil {
		return platformCapabilityPolicyPlan{}, fail
	}
	canonical, err := json.Marshal(plan)
	if err != nil || !bytes.Equal(canonical, []byte(raw)) {
		return platformCapabilityPolicyPlan{}, fail
	}
	if plan.Format != platformCapabilityPolicyFormat || plan.Capability != platformCapabilityNetworkHTTP ||
		!platformValidUUID(plan.InstallationID) || !platformDirectoryAppID.MatchString(plan.AppID) || !platformDirectoryAppID.MatchString(plan.PublisherID) ||
		!platformValidUUID(plan.ControllerEpoch) || !platformValidSHA(plan.ReleaseSHA256) ||
		!platformValidUUID(plan.PreviousReviewPlanID) || !platformValidSHA(plan.PreviousPolicySHA256) || !platformValidSHA(plan.ActorAuthority) ||
		plan.Generation < 1 || plan.InstallationRevision < 1 || plan.PolicyRevision < 1 || plan.TrustRevision < 1 ||
		plan.Runtime == nil || plan.Runtime.NetworkPolicy == "" ||
		plan.TargetsAdded == nil || plan.TargetsRemoved == nil || len(plan.TargetsAdded) > platformCapabilityTargetsMax || len(plan.TargetsRemoved) > appplatform.MaxHTTPPolicyTargets ||
		plan.PrivateNetworks == nil || appplatform.ValidatePrivateNetworks(plan.PrivateNetworks) != nil {
		return platformCapabilityPolicyPlan{}, fail
	}
	if sum, err := appplatform.PrivateNetworksSHA256(plan.PrivateNetworks); err != nil || sum != plan.PrivateNetworksSHA256 {
		return platformCapabilityPolicyPlan{}, fail
	}
	if _, err := appplatform.ParseHTTPPolicyV2([]byte(plan.Runtime.NetworkPolicy)); err != nil {
		return platformCapabilityPolicyPlan{}, fail
	}
	if plan.Runtime.RuntimeID == "" || plan.Runtime.ControllerEpoch != plan.ControllerEpoch ||
		transport.ValidateNetworkRuntimeProfile(plan.Runtime.NetworkProfile) != nil || !plan.Runtime.NetworkProfile.WritableBackend {
		return platformCapabilityPolicyPlan{}, fail
	}
	return plan, nil
}

// platformCapabilityPlanPattern selects capability plans out of the 'update'
// plans of an installation. The format is the first member of the canonical
// encoding; the exact parse is what decides.
func platformCapabilityPlanPattern() string {
	return `{"format":"` + platformCapabilityPolicyFormat + `"%`
}

// platformCapabilityPlanRow is a stored capability plan, verified against its own
// digest and canonical encoding.
type platformCapabilityPlanRow struct {
	id       string
	plan     platformCapabilityPolicyPlan
	raw      string
	digest   string
	consumed bool
}

// loadPlatformLatestCapabilityPlan reads the installation's latest consumed
// capability plan. Plans are ordered by the policy revision they were made at:
// each consume raises it, so a later plan always sorts after an earlier one.
func loadPlatformLatestCapabilityPlan(e migExec, installation string) (platformCapabilityPlanRow, error) {
	var row platformCapabilityPlanRow
	var source sql.NullString
	var epoch string
	if err := e.queryRow(`SELECT id,plan_json,plan_sha256,source_release_sha256,controller_epoch
		FROM app_platform_review_plans
		WHERE installation_id=? AND action='update' AND consumed_ms IS NOT NULL AND target_release_sha256 IS NULL
		AND plan_json LIKE ?
		ORDER BY policy_revision DESC,consumed_ms DESC,id DESC LIMIT 1`, installation, platformCapabilityPlanPattern()).
		Scan(&row.id, &row.raw, &row.digest, &source, &epoch); err != nil {
		return row, platformBindingReadError(err)
	}
	plan, err := parsePlatformCapabilityPolicyPlan(row.raw)
	if err != nil || platformDigest([]byte(row.raw)) != row.digest || !source.Valid || source.String != plan.ReleaseSHA256 ||
		epoch != plan.ControllerEpoch || plan.InstallationID != installation || !platformValidUUID(row.id) {
		return platformCapabilityPlanRow{}, errPlatformCapabilityPolicy
	}
	row.plan, row.consumed = plan, true
	return row, nil
}

// platformCapabilityGrantFor returns the consumed capability plan that backs a
// network.http grant row, or an error. The plan must be the installation's
// latest, belong to this release and epoch, and carry exactly the policy the row
// holds. Enable, repair and restore advance the generation while they retain the
// grant rows, so the plan stays valid for any generation at or after its own; an
// update to another release rewrites the rows and changes the release. This is the one place a row policy different from the
// reviewed install policy is accepted.
func platformCapabilityGrantFor(e migExec, installation, release, epoch, review, stored string) (platformCapabilityPlanRow, error) {
	row, err := loadPlatformLatestCapabilityPlan(e, installation)
	if err != nil {
		return row, err
	}
	var generation int64
	if err := e.queryRow(`SELECT generation FROM app_platform_installations WHERE id=?`, installation).Scan(&generation); err != nil {
		return platformCapabilityPlanRow{}, platformBindingReadError(err)
	}
	plan := row.plan
	if row.id != review || plan.ReleaseSHA256 != release || plan.ControllerEpoch != epoch || plan.Generation > generation ||
		plan.Runtime == nil || plan.Runtime.NetworkPolicy != stored {
		return platformCapabilityPlanRow{}, errPlatformCapabilityPolicy
	}
	return row, nil
}

// platformNetworkRowOverlay says whether a granted network.http row holds a
// policy backed by a valid latest consumed capability plan. Activation callers
// pass nil, which accepts only the reviewed install selection.
type platformNetworkRowOverlay func(review, stored string) bool

func platformCapabilityOverlay(e migExec, installation, release, epoch string) platformNetworkRowOverlay {
	return func(review, stored string) bool {
		_, err := platformCapabilityGrantFor(e, installation, release, epoch, review, stored)
		return err == nil
	}
}

// loadPlatformCapabilityNetworkGrant is the second grant query (doc 175 section
// 2.5): a network.http row whose review is not an operation's plan but a consumed
// capability plan. The plan must be the installation's latest and must carry the
// current runtime and exactly the row's policy; the profile is the one it
// recorded when it was made.
func loadPlatformCapabilityNetworkGrant(e migExec, installation string, t platformNetworkGrantTarget) (platformNetworkGrant, error) {
	var g platformNetworkGrant
	fail := errAppPlatformNetworkUnavailable
	if err := e.queryRow(`SELECT g.policy_json,g.review_plan_id FROM app_platform_capability_grants g
		WHERE g.installation_id=? AND g.capability='network.http' AND g.granted=1 AND g.policy_revision=?
		AND NOT EXISTS (SELECT 1 FROM app_platform_operations o WHERE o.review_plan_id=g.review_plan_id)`,
		installation, t.policy).Scan(&g.policy, &g.review); err != nil || len(g.policy) > appplatform.MaxHTTPPolicyV2Bytes || !platformValidUUID(g.review) {
		return platformNetworkGrant{}, fail
	}
	row, err := platformCapabilityGrantFor(e, installation, t.release, t.runtime.controllerEpoch, g.review, g.policy)
	if err != nil || row.plan.Generation > t.generation {
		return platformNetworkGrant{}, fail
	}
	target := row.plan.Runtime
	if target == nil ||
		target.RuntimeID != t.runtime.id ||
		target.DeploymentID != t.runtime.deploymentID ||
		target.ControllerEpoch != t.runtime.controllerEpoch ||
		target.ControlRevision != t.runtime.controlRevision ||
		target.Fingerprint != t.runtime.fingerprint {
		return platformNetworkGrant{}, fail
	}
	g.profile = target.NetworkProfile
	g.privateNetworksSHA = row.plan.PrivateNetworksSHA256
	return g, nil
}
