package app

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/transport"
)

// platformCapabilityFixture is a writable installation, ready and active, whose
// reviewed network.http policy is the v1 fixture policy (https://api.example.com,
// /v1, GET), with an administrator who may launch it.
func platformCapabilityFixture(t *testing.T, pg bool) (*Server, platformActorSession, string) {
	t.Helper()
	platformFixtureManifestHook.set(t, func(m *appplatform.Manifest) {
		m.Capabilities.Required = []string{"background.run", "network.http", "notifications.publish"}
		m.Capabilities.Optional = nil
	})
	platformFixtureNetworkTarget.set(t, true)
	s, actor, lease := platformWritableAccessFixture(t, pg)
	mustExec(t, s.st, `UPDATE app_platform_capability_grants SET granted=1 WHERE installation_id=?`, lease.installationID)
	return s, actor, lease.installationID
}

func platformTestTarget(id, origin, network string, paths, methods []string) appplatform.HTTPTarget {
	return appplatform.HTTPTarget{ID: id, Origin: origin, Paths: paths, Methods: methods, Network: network}
}

// platformV2PolicyFixture is a canonical v2 policy with the fixture's shared
// bounds and the given targets.
func platformV2PolicyFixture(t *testing.T, targets ...appplatform.HTTPTarget) string {
	t.Helper()
	raw, err := appplatform.CanonicalHTTPPolicyV2(appplatform.HTTPPolicyV2{
		Targets: targets, RequestBytes: 1024, ResponseBytes: 4096, Deadline: time.Second, Concurrency: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func platformV2DefaultTarget() appplatform.HTTPTarget {
	return platformTestTarget("default", "https://api.example.com", "public", []string{"/v1"}, []string{"GET"})
}
func platformV2ExtraTarget() appplatform.HTTPTarget {
	return platformTestTarget("extra", "https://other.example.com", "public", []string{"/v2"}, []string{"GET"})
}

var platformCapabilitySeedCounter atomic.Int64

type platformCapabilitySeed struct {
	// policy is the v2 policy the plan carries (default: default + extra).
	policy string
	// mutate edits the plan before it is encoded.
	mutate func(*platformCapabilityPolicyPlan)
	// source overrides the plan row's source release column.
	source string
	// epoch overrides the plan row's controller epoch column.
	epoch string
	// unconsumed leaves the plan pending.
	unconsumed bool
	// noStamp leaves grants, activation and installation as they were.
	noStamp bool
	// rowPolicy overrides the policy the grant row is given (default: the plan's).
	rowPolicy string
	// id of the plan row (default: a fresh fixture id).
	id string
	// revision overrides the policy revision column the plan was made at (default: current).
	revision int64
}

func platformCapabilityFixtureFacts(t *testing.T, s *Server, installation string) (plan platformCapabilityPolicyPlan, install platformInstallProjection, previousReview, previousPolicy string) {
	t.Helper()
	var raw string
	if err := s.st.queryRow(`SELECT p.plan_json FROM app_platform_operations o JOIN app_platform_review_plans p ON p.id=o.review_plan_id WHERE o.installation_id=? AND o.action='install'`, installation).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(raw), &install); err != nil {
		t.Fatal(err)
	}
	previousReview = scalar[string](t, s.st, `SELECT review_plan_id FROM app_platform_capability_grants WHERE installation_id=? AND capability='network.http'`, installation)
	previousPolicy = scalar[string](t, s.st, `SELECT policy_json FROM app_platform_capability_grants WHERE installation_id=? AND capability='network.http'`, installation)
	plan = platformCapabilityPolicyPlan{
		Format: platformCapabilityPolicyFormat, InstallationID: installation, AppID: install.AppID, PublisherID: install.PublisherID,
		Generation:           scalar[int64](t, s.st, `SELECT generation FROM app_platform_installations WHERE id=?`, installation),
		InstallationRevision: scalar[int64](t, s.st, `SELECT revision FROM app_platform_installations WHERE id=?`, installation),
		PolicyRevision:       scalar[int64](t, s.st, `SELECT policy_revision FROM app_platform_installations WHERE id=?`, installation),
		TrustRevision:        scalar[int64](t, s.st, `SELECT revision FROM app_platform_publishers WHERE id=?`, install.PublisherID),
		ControllerEpoch:      install.ControllerEpoch, ReleaseSHA256: install.ReleaseSHA256, Capability: platformCapabilityNetworkHTTP,
		PreviousReviewPlanID: previousReview, PreviousPolicySHA256: platformDigest([]byte(previousPolicy)),
		TargetsAdded: []platformCapabilityTarget{}, TargetsRemoved: []platformCapabilityTarget{},
		PrivateNetworks: []appplatform.PrivateNetwork{}, ActorAuthority: platformDigest([]byte("fixture-authority")),
	}
	sum, err := appplatform.PrivateNetworksSHA256(plan.PrivateNetworks)
	if err != nil {
		t.Fatal(err)
	}
	plan.PrivateNetworksSHA256 = sum
	return plan, install, previousReview, previousPolicy
}

// platformCapabilitySeedPlan writes a capability plan row and, unless noStamp,
// the state a consume leaves behind. It returns the plan id and the plan.
func platformCapabilitySeedPlan(t *testing.T, s *Server, installation string, o platformCapabilitySeed) (string, platformCapabilityPolicyPlan) {
	t.Helper()
	plan, install, _, _ := platformCapabilityFixtureFacts(t, s, installation)
	if o.policy == "" {
		o.policy = platformV2PolicyFixture(t, platformV2DefaultTarget(), platformV2ExtraTarget())
	}
	runtime := *install.Runtime
	runtime.NetworkPolicy = o.policy
	plan.Runtime = &runtime
	if o.revision > 0 {
		plan.PolicyRevision = o.revision
	}
	extra, err := platformCapabilityTargetView(platformV2ExtraTarget())
	if err != nil {
		t.Fatal(err)
	}
	plan.TargetsAdded = []platformCapabilityTarget{extra}
	if o.mutate != nil {
		o.mutate(&plan)
	}
	raw, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	id := o.id
	if id == "" {
		id = platformFixtureID(7000 + int(platformCapabilitySeedCounter.Add(1)))
	}
	source, epoch := o.source, o.epoch
	if source == "" {
		source = plan.ReleaseSHA256
	}
	if epoch == "" {
		epoch = plan.ControllerEpoch
	}
	var consumed any
	now := time.Now().UnixMilli()
	if !o.unconsumed {
		consumed = now
	}
	mustExec(t, s.st, `INSERT INTO app_platform_review_plans(id,installation_id,app_id,publisher_id,action,source_release_sha256,target_release_sha256,installation_revision,policy_revision,trust_revision,controller_epoch,actor_scope,username,account_created,account_generation,authority_fingerprint,plan_sha256,plan_json,expires_ms,consumed_ms,created_ms,updated_ms)
		VALUES(?,?,?,?,'update',?,NULL,?,?,?,?,?,NULL,'',0,?,?,?,?,?,?,?)`,
		id, installation, plan.AppID, plan.PublisherID, source, plan.InstallationRevision, plan.PolicyRevision, plan.TrustRevision, epoch,
		platformDigest([]byte("fixture-scope")), plan.ActorAuthority, platformDigest(raw), string(raw), now+60000, consumed, now, now)
	if !o.noStamp {
		next := plan.PolicyRevision + 1
		rowPolicy := o.rowPolicy
		if rowPolicy == "" {
			rowPolicy = o.policy
		}
		mustExec(t, s.st, `UPDATE app_platform_capability_grants SET policy_revision=? WHERE installation_id=?`, next, installation)
		mustExec(t, s.st, `UPDATE app_platform_capability_grants SET policy_json=?,review_plan_id=? WHERE installation_id=? AND capability='network.http'`, rowPolicy, id, installation)
		mustExec(t, s.st, `UPDATE app_platform_activations SET policy_revision=? WHERE installation_id=? AND state='active'`, next, installation)
		mustExec(t, s.st, `UPDATE app_platform_installations SET policy_revision=? WHERE id=?`, next, installation)
	}
	return id, plan
}

// platformCapabilityPrepare asks for a signed foreground request to url, as the
// administrator, at the installation's current policy revision.
func platformCapabilityPrepare(t *testing.T, s *Server, actor platformActorSession, installation, url string) error {
	t.Helper()
	release := scalar[string](t, s.st, `SELECT release_sha256 FROM app_platform_activations WHERE installation_id=? AND state='active'`, installation)
	revision := scalar[int64](t, s.st, `SELECT policy_revision FROM app_platform_installations WHERE id=?`, installation)
	generation := scalar[int64](t, s.st, `SELECT generation FROM app_platform_installations WHERE id=?`, installation)
	binding := platformBackendRequestBinding{Generation: itoa64(generation), PolicyRevision: itoa64(revision), ReleaseSHA256: release}
	intent, err := s.preparePlatformNetworkRequest(context.Background(), actor, installation, binding, "GET", url, "application/json", "", "", nil)
	if err == nil {
		intent.Close()
	}
	return err
}

func platformCapabilityLaunch(t *testing.T, s *Server, actor platformActorSession, installation string) error {
	t.Helper()
	a, err := s.preparePlatformWritableBackendAccess(context.Background(), actor, installation)
	if err == nil {
		a.Close()
	}
	return err
}

var _ = control.NetworkHTTPRequestFormat
var _ = transport.NetworkRuntimeFormat

// platformCapabilityOtherRelease adds a second release row (a copy of the
// installation's, under another digest) and returns its digest.
func platformCapabilityOtherRelease(t *testing.T, s *Server, installation string) string {
	t.Helper()
	current := scalar[string](t, s.st, `SELECT release_sha256 FROM app_platform_activations WHERE installation_id=? AND state='active'`, installation)
	other := platformDigest([]byte("another release of the same app"))
	mustExec(t, s.st, `INSERT INTO app_platform_releases(sha256,app_id,publisher_id,version,descriptor,signature,verification_key_id,trust_revision,verified_ms,frontend_sha256,managed,created_ms)
		SELECT ?,app_id,publisher_id,version||'-other',descriptor,signature,verification_key_id,trust_revision,verified_ms,frontend_sha256,managed,created_ms FROM app_platform_releases WHERE sha256=?`, other, current)
	return other
}
