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
//
// A pinned destination (doc 186) is reviewed in the same plan: new plans are portal.proxy-policy-review/1-dev.2,
// which adds the pin diff and the operator entries the plan's own pins use. A consumed 1-dev.1 plan keeps backing
// the grant it made. The grant row's policy is 1-dev.2 only when it holds pins.
const (
	// platformProxyReviewFormat is the plan format before pins, still read for the grants it backs.
	platformProxyReviewFormat = "portal.proxy-policy-review/1-dev.1"
	// platformProxyReviewFormatPins is the format of every new plan.
	platformProxyReviewFormatPins = "portal.proxy-policy-review/1-dev.2"
	platformProxyLifetime         = 5 * time.Minute
	platformProxyAudit            = "app_platform.proxy_policy_changed"
	platformProxyCapability       = appplatform.ProxyCapability
)

var errPlatformProxyPolicy = errors.New("proxy policy review unavailable")

// platformProxyPlan is the closed canonical plan. Policy is the canonical policy the consume writes, or
// "{}" for a withdrawal. The pin fields exist in 1-dev.2 plans only: in a 1-dev.1 plan they are nil, and they
// are not written.
type platformProxyPlan struct {
	Format               string
	InstallationID       string
	AppID                string
	PublisherID          string
	Generation           int64
	InstallationRevision int64
	PolicyRevision       int64
	TrustRevision        int64
	ControllerEpoch      string
	ReleaseSHA256        string
	Capability           string
	PreviousPolicySHA256 string
	Policy               string
	PolicySHA256         string
	HostsAdded           []string
	HostsRemoved         []string
	DailyBytesOld        int64
	DailyBytesNew        int64
	// Runner is the fixed 1-dev.1 profile in both formats.
	Runner         transport.ProxyProfile
	ActorAuthority string
	// PinsAdded, PinsRemoved and PinsChanged are the diff of the policy's pins by host name. RunnerPins are the
	// operator entries the policy's own pins use, one per pin and equal to it: never the whole operator list.
	PinsAdded   []appplatform.ProxyPin
	PinsRemoved []appplatform.ProxyPin
	PinsChanged []platformProxyPinChange
	RunnerPins  []appplatform.ProxyPin
	// CarriedFromPlan is set only on a plan an update wrote to carry a grant under a new release (never on an
	// administrator's review): the id of the proxy plan whose policy it carries. 1-dev.2 only.
	CarriedFromPlan string
}

// platformProxyPinChange is a pin whose address or port changed, under the same host name.
type platformProxyPinChange struct {
	Host string               `json:"host"`
	Old  appplatform.ProxyPin `json:"old"`
	New  appplatform.ProxyPin `json:"new"`
}

// platformProxyPlanWire1 is the 1-dev.1 encoding, byte for byte what it was before pins.
type platformProxyPlanWire1 struct {
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

// platformProxyPlanWire2 is the 1-dev.2 encoding: the 1-dev.1 members, with the pin members added.
type platformProxyPlanWire2 struct {
	Format               string                   `json:"format"`
	InstallationID       string                   `json:"installation_id"`
	AppID                string                   `json:"app_id"`
	PublisherID          string                   `json:"publisher_id"`
	Generation           int64                    `json:"generation"`
	InstallationRevision int64                    `json:"installation_revision"`
	PolicyRevision       int64                    `json:"policy_revision"`
	TrustRevision        int64                    `json:"trust_revision"`
	ControllerEpoch      string                   `json:"controller_epoch"`
	ReleaseSHA256        string                   `json:"release_sha256"`
	Capability           string                   `json:"capability"`
	PreviousPolicySHA256 string                   `json:"previous_policy_sha256"`
	Policy               string                   `json:"policy"`
	PolicySHA256         string                   `json:"policy_sha256"`
	HostsAdded           []string                 `json:"hosts_added"`
	HostsRemoved         []string                 `json:"hosts_removed"`
	PinsAdded            []appplatform.ProxyPin   `json:"pins_added"`
	PinsRemoved          []appplatform.ProxyPin   `json:"pins_removed"`
	PinsChanged          []platformProxyPinChange `json:"pins_changed"`
	DailyBytesOld        int64                    `json:"daily_bytes_old"`
	DailyBytesNew        int64                    `json:"daily_bytes_new"`
	Runner               transport.ProxyProfile   `json:"runner"`
	RunnerPins           []appplatform.ProxyPin   `json:"runner_pins"`
	ActorAuthority       string                   `json:"actor_authority"`
	CarriedFromPlan      string                   `json:"carried_from_plan,omitempty"`
}

func (p platformProxyPlan) wire1() platformProxyPlanWire1 {
	return platformProxyPlanWire1{Format: p.Format, InstallationID: p.InstallationID, AppID: p.AppID, PublisherID: p.PublisherID, Generation: p.Generation,
		InstallationRevision: p.InstallationRevision, PolicyRevision: p.PolicyRevision, TrustRevision: p.TrustRevision, ControllerEpoch: p.ControllerEpoch,
		ReleaseSHA256: p.ReleaseSHA256, Capability: p.Capability, PreviousPolicySHA256: p.PreviousPolicySHA256, Policy: p.Policy, PolicySHA256: p.PolicySHA256,
		HostsAdded: p.HostsAdded, HostsRemoved: p.HostsRemoved, DailyBytesOld: p.DailyBytesOld, DailyBytesNew: p.DailyBytesNew, Runner: p.Runner, ActorAuthority: p.ActorAuthority}
}

func (p platformProxyPlan) wire2() platformProxyPlanWire2 {
	return platformProxyPlanWire2{Format: p.Format, InstallationID: p.InstallationID, AppID: p.AppID, PublisherID: p.PublisherID, Generation: p.Generation,
		InstallationRevision: p.InstallationRevision, PolicyRevision: p.PolicyRevision, TrustRevision: p.TrustRevision, ControllerEpoch: p.ControllerEpoch,
		ReleaseSHA256: p.ReleaseSHA256, Capability: p.Capability, PreviousPolicySHA256: p.PreviousPolicySHA256, Policy: p.Policy, PolicySHA256: p.PolicySHA256,
		HostsAdded: p.HostsAdded, HostsRemoved: p.HostsRemoved, PinsAdded: p.PinsAdded, PinsRemoved: p.PinsRemoved, PinsChanged: p.PinsChanged,
		DailyBytesOld: p.DailyBytesOld, DailyBytesNew: p.DailyBytesNew, Runner: p.Runner, RunnerPins: p.RunnerPins, ActorAuthority: p.ActorAuthority, CarriedFromPlan: p.CarriedFromPlan}
}

func (w platformProxyPlanWire1) plan() platformProxyPlan {
	return platformProxyPlan{Format: w.Format, InstallationID: w.InstallationID, AppID: w.AppID, PublisherID: w.PublisherID, Generation: w.Generation,
		InstallationRevision: w.InstallationRevision, PolicyRevision: w.PolicyRevision, TrustRevision: w.TrustRevision, ControllerEpoch: w.ControllerEpoch,
		ReleaseSHA256: w.ReleaseSHA256, Capability: w.Capability, PreviousPolicySHA256: w.PreviousPolicySHA256, Policy: w.Policy, PolicySHA256: w.PolicySHA256,
		HostsAdded: w.HostsAdded, HostsRemoved: w.HostsRemoved, DailyBytesOld: w.DailyBytesOld, DailyBytesNew: w.DailyBytesNew, Runner: w.Runner, ActorAuthority: w.ActorAuthority}
}

func (w platformProxyPlanWire2) plan() platformProxyPlan {
	return platformProxyPlan{Format: w.Format, InstallationID: w.InstallationID, AppID: w.AppID, PublisherID: w.PublisherID, Generation: w.Generation,
		InstallationRevision: w.InstallationRevision, PolicyRevision: w.PolicyRevision, TrustRevision: w.TrustRevision, ControllerEpoch: w.ControllerEpoch,
		ReleaseSHA256: w.ReleaseSHA256, Capability: w.Capability, PreviousPolicySHA256: w.PreviousPolicySHA256, Policy: w.Policy, PolicySHA256: w.PolicySHA256,
		HostsAdded: w.HostsAdded, HostsRemoved: w.HostsRemoved, PinsAdded: w.PinsAdded, PinsRemoved: w.PinsRemoved, PinsChanged: w.PinsChanged,
		DailyBytesOld: w.DailyBytesOld, DailyBytesNew: w.DailyBytesNew, Runner: w.Runner, RunnerPins: w.RunnerPins, ActorAuthority: w.ActorAuthority, CarriedFromPlan: w.CarriedFromPlan}
}

// MarshalJSON is the canonical encoding of the plan's own format.
func (p platformProxyPlan) MarshalJSON() ([]byte, error) {
	switch p.Format {
	case platformProxyReviewFormat:
		return json.Marshal(p.wire1())
	case platformProxyReviewFormatPins:
		return json.Marshal(p.wire2())
	}
	return nil, errPlatformProxyPolicy
}

// parsePlatformProxyPlan accepts only the exact canonical encoding of a plan whose own fields are valid, in
// either format. It reads nothing from the database.
func parsePlatformProxyPlan(raw string) (platformProxyPlan, error) {
	var plan platformProxyPlan
	fail := errPlatformProxyPolicy
	if len(raw) == 0 || len(raw) > 65536 {
		return plan, fail
	}
	var head struct {
		Format string `json:"format"`
	}
	if json.Unmarshal([]byte(raw), &head) != nil {
		return plan, fail
	}
	d := json.NewDecoder(bytes.NewReader([]byte(raw)))
	d.DisallowUnknownFields()
	switch head.Format {
	case platformProxyReviewFormat:
		var w platformProxyPlanWire1
		if d.Decode(&w) != nil {
			return platformProxyPlan{}, fail
		}
		plan = w.plan()
	case platformProxyReviewFormatPins:
		var w platformProxyPlanWire2
		if d.Decode(&w) != nil {
			return platformProxyPlan{}, fail
		}
		plan = w.plan()
	default:
		return platformProxyPlan{}, fail
	}
	canonical, err := json.Marshal(plan)
	if err != nil || !bytes.Equal(canonical, []byte(raw)) {
		return platformProxyPlan{}, fail
	}
	if plan.Capability != platformProxyCapability ||
		!platformValidUUID(plan.InstallationID) || !platformDirectoryAppID.MatchString(plan.AppID) || !platformDirectoryAppID.MatchString(plan.PublisherID) ||
		!platformValidUUID(plan.ControllerEpoch) || !platformValidSHA(plan.ReleaseSHA256) || !platformValidSHA(plan.PreviousPolicySHA256) || !platformValidSHA(plan.ActorAuthority) ||
		plan.Generation < 1 || plan.InstallationRevision < 1 || plan.PolicyRevision < 1 || plan.TrustRevision < 1 ||
		plan.HostsAdded == nil || plan.HostsRemoved == nil || !appplatform.ValidProxyHosts(plan.HostsAdded) || !appplatform.ValidProxyHosts(plan.HostsRemoved) ||
		plan.DailyBytesOld < appplatform.ProxyDailyBytesMin || plan.DailyBytesOld > appplatform.ProxyDailyBytesMax ||
		plan.Runner != transport.NewProxyProfile() || platformDigest([]byte(plan.Policy)) != plan.PolicySHA256 {
		return platformProxyPlan{}, fail
	}
	var policyPins []appplatform.ProxyPin
	if plan.Policy == appplatform.EmptyProxyPolicyJSON {
		if plan.DailyBytesNew != 0 || len(plan.HostsAdded) != 0 {
			return platformProxyPlan{}, fail
		}
	} else {
		parsed, err := appplatform.ParseProxyPolicy([]byte(plan.Policy))
		if err != nil || parsed.Policy.DailyBytes != plan.DailyBytesNew {
			return platformProxyPlan{}, fail
		}
		policyPins = parsed.Policy.Pins
	}
	if plan.CarriedFromPlan != "" && !platformValidUUID(plan.CarriedFromPlan) {
		return platformProxyPlan{}, fail
	}
	if plan.Format == platformProxyReviewFormat {
		// A 1-dev.1 plan predates pins: nothing in it binds a pin to the runner's entries.
		if len(policyPins) > 0 {
			return platformProxyPlan{}, fail
		}
		return plan, nil
	}
	if !validPlatformProxyPinDiff(plan, policyPins) {
		return platformProxyPlan{}, fail
	}
	return plan, nil
}

// validPlatformProxyPinDiff checks the pin members of a 1-dev.2 plan against the policy the plan writes: the
// diff lists are valid, sorted, disjoint and name only pins of that policy, and the runner pins are exactly the
// policy's own pins. A withdrawal has none.
func validPlatformProxyPinDiff(plan platformProxyPlan, policyPins []appplatform.ProxyPin) bool {
	if plan.PinsAdded == nil || plan.PinsRemoved == nil || plan.PinsChanged == nil || plan.RunnerPins == nil ||
		!appplatform.ValidProxyPins(plan.PinsAdded) || !appplatform.ValidProxyPins(plan.PinsRemoved) || !slices.Equal(plan.RunnerPins, append([]appplatform.ProxyPin{}, policyPins...)) {
		return false
	}
	hosts := map[string]bool{}
	for _, p := range plan.PinsAdded {
		if !slices.Contains(policyPins, p) || hosts[p.Host] {
			return false
		}
		hosts[p.Host] = true
	}
	for _, p := range plan.PinsRemoved {
		if slices.ContainsFunc(policyPins, func(q appplatform.ProxyPin) bool { return q.Host == p.Host }) || hosts[p.Host] {
			return false
		}
		hosts[p.Host] = true
	}
	for i, c := range plan.PinsChanged {
		if c.Host != c.Old.Host || c.Host != c.New.Host || c.Old == c.New || !appplatform.ValidProxyPin(c.Old) || !appplatform.ValidProxyPin(c.New) ||
			!slices.Contains(policyPins, c.New) || hosts[c.Host] || i > 0 && plan.PinsChanged[i-1].Host >= c.Host {
			return false
		}
		hosts[c.Host] = true
	}
	return true
}

// platformProxyPlanPattern selects proxy plans, of either format, out of the 'update' plans of an installation.
// The format is the first member of the canonical encoding; the exact parse is what decides.
func platformProxyPlanPattern() string {
	return `{"format":"portal.proxy-policy-review/1-dev.%`
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
		ORDER BY policy_revision DESC,id DESC LIMIT 1`, installation, platformProxyPlanPattern()).
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

// platformProxyPinsDiff is the pins of next whose host current lacks, the pins of current whose host next lacks,
// and the hosts both have with another address or port. All three are sorted by host and never nil.
func platformProxyPinsDiff(current, next []appplatform.ProxyPin) (added, removed []appplatform.ProxyPin, changed []platformProxyPinChange) {
	added, removed, changed = []appplatform.ProxyPin{}, []appplatform.ProxyPin{}, []platformProxyPinChange{}
	for _, n := range next {
		i := slices.IndexFunc(current, func(c appplatform.ProxyPin) bool { return c.Host == n.Host })
		switch {
		case i < 0:
			added = append(added, n)
		case current[i] != n:
			changed = append(changed, platformProxyPinChange{Host: n.Host, Old: current[i], New: n})
		}
	}
	for _, c := range current {
		if !slices.ContainsFunc(next, func(n appplatform.ProxyPin) bool { return n.Host == c.Host }) {
			removed = append(removed, c)
		}
	}
	return added, removed, changed
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
