package app

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/transport"
)

// platformProxyFixture is the capability fixture whose release also declares the optional
// network.proxy with suggested hosts. The grant is off, as it is after install.
func platformProxyFixture(t *testing.T, pg bool) (*Server, platformActorSession, string) {
	t.Helper()
	platformFixtureManifestHook.set(t, func(m *appplatform.Manifest) {
		m.Capabilities.Required = []string{"background.run", "network.http", "notifications.publish"}
		m.Capabilities.Optional = []string{"network.proxy"}
		m.NetworkProxy = &appplatform.NetworkProxy{Hosts: []string{"*.eastmoney.com", "qt.gtimg.cn"}}
	})
	platformFixtureNetworkTarget.set(t, true)
	s, actor, lease := platformWritableAccessFixture(t, pg)
	mustExec(t, s.st, `UPDATE app_platform_capability_grants SET granted=1 WHERE installation_id=? AND capability<>'network.proxy'`, lease.installationID)
	runner := &platformProxyRunner{profile: transport.NewProxyProfile()}
	s.platformObserveProxy = runner.observe
	t.Cleanup(func() { s.platformObserveProxy = nil })
	proxyRunners.Store(s, runner)
	return s, actor, lease.installationID
}

var proxyRunners sync.Map // *Server -> *platformProxyRunner

func proxyRunnerOf(s *Server) *platformProxyRunner {
	v, _ := proxyRunners.Load(s)
	return v.(*platformProxyRunner)
}

// platformProxyRunner stands a runner behind the review: what PRPX0001 answers, or an error.
type platformProxyRunner struct {
	mu      sync.Mutex
	profile transport.ProxyProfile
	err     error
	asked   int
	// pinsSupported and pins are what PRPX0002 answers (doc 186); a runner that predates pins has none.
	pinsSupported bool
	pins          []appplatform.ProxyPin
}

func (r *platformProxyRunner) observe(context.Context, platformRuntimeEndpoint) (transport.ProxyRunnerState, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.asked++
	if r.err != nil {
		return transport.ProxyRunnerState{}, r.err
	}
	return transport.ProxyRunnerState{Profile: r.profile, PinsSupported: r.pinsSupported, Pins: append([]appplatform.ProxyPin{}, r.pins...)}, nil
}

// servePins makes the runner answer PRPX0002 with the allowlist pins.
func (r *platformProxyRunner) servePins(pins ...appplatform.ProxyPin) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.pinsSupported, r.pins = true, pins
}

func (r *platformProxyRunner) fail(err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.err = err
}

func (r *platformProxyRunner) queries() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.asked
}

func platformProxyRequestFor(t *testing.T, s *Server, installation string, hosts []string, daily int64) platformProxyRequest {
	t.Helper()
	return platformProxyRequest{
		PolicyRevision:       scalar[int64](t, s.st, `SELECT policy_revision FROM app_platform_installations WHERE id=?`, installation),
		PreviousPolicySHA256: platformDigest([]byte(scalar[string](t, s.st, `SELECT policy_json FROM app_platform_capability_grants WHERE installation_id=? AND capability='network.proxy'`, installation))),
		Hosts:                hosts, DailyBytes: daily,
	}
}

func platformProxyCreate(t *testing.T, s *Server, actor platformActorSession, installation string, hosts []string, daily int64) (platformProxyResult, error) {
	t.Helper()
	return s.createPlatformProxyReview(context.Background(), actor, platformRuntimeEndpoint{}, installation, platformProxyRequestFor(t, s, installation, hosts, daily))
}

func platformProxyGrantRow(t *testing.T, s *Server, installation string) (granted int, policy, review string) {
	t.Helper()
	granted = scalar[int](t, s.st, `SELECT granted FROM app_platform_capability_grants WHERE installation_id=? AND capability='network.proxy'`, installation)
	policy = scalar[string](t, s.st, `SELECT policy_json FROM app_platform_capability_grants WHERE installation_id=? AND capability='network.proxy'`, installation)
	review = scalar[string](t, s.st, `SELECT review_plan_id FROM app_platform_capability_grants WHERE installation_id=? AND capability='network.proxy'`, installation)
	return
}

var proxyHosts = []string{"*.eastmoney.com", "qt.gtimg.cn"}

func testPlatformProxyPlanThenConsume(t *testing.T, pg bool) {
	s, actor, installation := platformProxyFixture(t, pg)
	revision := scalar[int64](t, s.st, `SELECT policy_revision FROM app_platform_installations WHERE id=?`, installation)
	installReview := scalar[string](t, s.st, `SELECT review_plan_id FROM app_platform_capability_grants WHERE installation_id=? AND capability='network.proxy'`, installation)
	if granted, policy, _ := platformProxyGrantRow(t, s, installation); granted != 0 || policy != "{}" {
		t.Fatalf("the grant is not off after install: %d %s", granted, policy)
	}
	before := platformCapabilityTableDump(t, s)
	// Unsorted input is sorted for the administrator; the plan changes nothing by itself.
	result, err := platformProxyCreate(t, s, actor, installation, []string{"qt.gtimg.cn", "*.eastmoney.com"}, 1<<30)
	if err != nil {
		t.Fatal(err)
	}
	if proxyRunnerOf(s).queries() != 1 {
		t.Fatal("the plan did not ask the runner (PRPX0001)")
	}
	if strings.Join(result.Plan.HostsAdded, ",") != "*.eastmoney.com,qt.gtimg.cn" || len(result.Plan.HostsRemoved) != 0 || result.Plan.DailyBytesOld != appplatform.ProxyDailyBytesMax || result.Plan.DailyBytesNew != 1<<30 {
		t.Fatalf("plan: %+v", result.Plan)
	}
	if got := platformCapabilityTableDump(t, s); string(got) == string(before) {
		t.Fatal("the plan wrote nothing")
	}
	if granted, _, _ := platformProxyGrantRow(t, s, installation); granted != 0 {
		t.Fatal("a plan granted the capability")
	}
	if scalar[int64](t, s.st, `SELECT policy_revision FROM app_platform_installations WHERE id=?`, installation) != revision {
		t.Fatal("a plan moved the policy revision")
	}
	change, err := s.consumePlatformProxyReview(context.Background(), actor, platformRuntimeEndpoint{}, result.ID, result.SHA256)
	if err != nil {
		t.Fatal(err)
	}
	if change.PolicyRevision != revision+1 {
		t.Fatalf("policy revision %d, want %d", change.PolicyRevision, revision+1)
	}
	granted, policy, review := platformProxyGrantRow(t, s, installation)
	wantPolicy, _ := appplatform.CanonicalProxyPolicy(appplatform.ProxyPolicy{Hosts: proxyHosts, DailyBytes: 1 << 30})
	if granted != 1 || policy != string(wantPolicy) {
		t.Fatalf("grant row after consume: %d %s", granted, policy)
	}
	if review != installReview {
		t.Fatal("the grant row's review plan changed; the install snapshot checks it")
	}
	// The consume moved the installation, every grant row and the active activation to one revision.
	for _, count := range []int{
		scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_capability_grants WHERE installation_id=? AND policy_revision<>?`, installation, revision+1),
		scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_activations WHERE installation_id=? AND state='active' AND policy_revision<>?`, installation, revision+1),
	} {
		if count != 0 {
			t.Fatal("a row kept the old policy revision")
		}
	}
	if scalar[int](t, s.st, `SELECT COUNT(*) FROM audit_log WHERE action='app_platform.proxy_policy_changed' AND target_id=?`, installation) != 1 {
		t.Fatal("no audit row")
	}
	detail := scalar[string](t, s.st, `SELECT detail FROM audit_log WHERE action='app_platform.proxy_policy_changed' AND target_id=?`, installation)
	for _, want := range []string{result.ID, platformDigest(wantPolicy), "qt.gtimg.cn", "*.eastmoney.com"} {
		if !strings.Contains(detail, want) {
			t.Fatalf("audit detail lacks %s: %s", want, detail)
		}
	}
	if consumed := scalar[int64](t, s.st, `SELECT COALESCE(consumed_ms,0) FROM app_platform_review_plans WHERE id=?`, result.ID); consumed == 0 {
		t.Fatal("the plan was not consumed")
	}
	// A plan is consumed once.
	if _, err := s.consumePlatformProxyReview(context.Background(), actor, platformRuntimeEndpoint{}, result.ID, result.SHA256); !errors.Is(err, errAppPlatformOperationConflict) {
		t.Fatalf("second consume: %v", err)
	}
	// The grant row is still a valid selection for launches and service identities.
	if err := platformCapabilityLaunch(t, s, actor, installation); err != nil {
		t.Fatalf("launch after the grant: %v", err)
	}
	a, err := s.loadPlatformServiceEgressCurrent(context.Background(), installation, platformServiceEgressLoad{})
	if err != nil {
		t.Fatalf("egress authority after the grant: %v", err)
	}
	defer a.Close()
	if !strings.Contains(strings.Join(a.resources, ","), "network.proxy") || a.proxy == nil || a.proxy.SHA256 != platformDigest(wantPolicy) {
		t.Fatalf("discovered resources %v proxy %+v", a.resources, a.proxy)
	}
}

func TestPlatformProxyPlanThenConsume(t *testing.T) {
	t.Parallel()
	testPlatformProxyPlanThenConsume(t, false)
}
func TestPlatformProxyPlanThenConsumePostgres(t *testing.T) {
	t.Parallel()
	runOnPostgres(t, testPlatformProxyPlanThenConsume)
}

// runOnPostgres runs a test body on PostgreSQL; the fixtures skip the test when TEST_POSTGRES_DSN is not set.
func runOnPostgres(t *testing.T, body func(*testing.T, bool)) {
	t.Helper()
	body(t, true)
}

func testPlatformProxyCreateRefusals(t *testing.T, pg bool) {
	cases := []struct {
		name  string
		setup func(t *testing.T, s *Server, installation string)
		hosts []string
		daily int64
		want  error
	}{
		{name: "background.run not granted", setup: func(t *testing.T, s *Server, i string) {
			mustExec(t, s.st, `UPDATE app_platform_capability_grants SET granted=0 WHERE installation_id=? AND capability='background.run'`, i)
		}, hosts: proxyHosts, daily: 1 << 30, want: errPlatformCapabilityPolicyBusy},
		{name: "not ready", setup: func(t *testing.T, s *Server, i string) {
			mustExec(t, s.st, `UPDATE app_platform_installations SET phase='review_required' WHERE id=?`, i)
		}, hosts: proxyHosts, daily: 1 << 30, want: errPlatformCapabilityPolicyBusy},
		{name: "unfinished operation", setup: func(t *testing.T, s *Server, i string) {
			mustExec(t, s.st, `UPDATE app_platform_operations SET stage='start',completed_ms=NULL WHERE installation_id=?`, i)
		}, hosts: proxyHosts, daily: 1 << 30, want: errPlatformCapabilityPolicyBusy},
		{name: "old runner", setup: func(t *testing.T, s *Server, i string) {
			proxyRunnerOf(s).fail(transport.ErrProxyUnsupported)
		}, hosts: proxyHosts, daily: 1 << 30, want: transport.ErrProxyUnsupported},
		{name: "public suffix wildcard", hosts: []string{"*.com"}, daily: 1 << 30, want: errPlatformManagementInput},
		{name: "two label wildcard base under a suffix", hosts: []string{"*.com.cn"}, daily: 1 << 30, want: errPlatformManagementInput},
		{name: "github.io wildcard", hosts: []string{"*.github.io"}, daily: 1 << 30, want: errPlatformManagementInput},
		{name: "ip literal", hosts: []string{"1.2.3.4"}, daily: 1 << 30, want: errPlatformManagementInput},
		{name: "duplicate entry", hosts: []string{"qt.gtimg.cn", "qt.gtimg.cn"}, daily: 1 << 30, want: errPlatformManagementInput},
		{name: "upper case", hosts: []string{"QT.gtimg.cn"}, daily: 1 << 30, want: errPlatformManagementInput},
		{name: "too many entries", hosts: func() []string {
			var list []string
			for i := 0; i < 65; i++ {
				list = append(list, "h"+string(rune('a'+i/26))+string(rune('a'+i%26))+".example.com")
			}
			return list
		}(), daily: 1 << 30, want: errPlatformManagementInput},
		{name: "budget above the platform's", hosts: proxyHosts, daily: appplatform.ProxyDailyBytesMax + 1, want: errPlatformManagementInput},
		{name: "budget zero", hosts: proxyHosts, daily: 0, want: errPlatformManagementInput},
		{name: "withdraw what is not granted", hosts: []string{}, daily: 0, want: errPlatformManagementInput},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, actor, installation := platformProxyFixture(t, pg)
			if tc.setup != nil {
				tc.setup(t, s, installation)
			}
			before := platformCapabilityTableDump(t, s)
			if _, err := platformProxyCreate(t, s, actor, installation, tc.hosts, tc.daily); !errors.Is(err, tc.want) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
			if after := platformCapabilityTableDump(t, s); string(after) != string(before) {
				t.Fatal("a refused plan changed the database")
			}
		})
	}
	// A release that does not declare the capability has no grant row to review.
	s, actor, installation := platformProxyFixture(t, pg)
	req := platformProxyRequestFor(t, s, installation, proxyHosts, 1<<30)
	mustExec(t, s.st, `DELETE FROM app_platform_capability_grants WHERE installation_id=? AND capability='network.proxy'`, installation)
	if _, err := s.createPlatformProxyReview(context.Background(), actor, platformRuntimeEndpoint{}, installation, req); !errors.Is(err, errPlatformCapabilityPolicyBusy) {
		t.Fatalf("no grant row: %v", err)
	}
	// A stale request names the policy it was made against.
	s, actor, installation = platformProxyFixture(t, pg)
	req = platformProxyRequestFor(t, s, installation, proxyHosts, 1<<30)
	req.PreviousPolicySHA256 = platformDigest([]byte("another policy"))
	if _, err := s.createPlatformProxyReview(context.Background(), actor, platformRuntimeEndpoint{}, installation, req); !errors.Is(err, errAppPlatformPlanChanged) {
		t.Fatalf("stale previous policy: %v", err)
	}
	req = platformProxyRequestFor(t, s, installation, proxyHosts, 1<<30)
	req.PolicyRevision++
	if _, err := s.createPlatformProxyReview(context.Background(), actor, platformRuntimeEndpoint{}, installation, req); !errors.Is(err, errAppPlatformPlanChanged) {
		t.Fatalf("stale revision: %v", err)
	}
	// Only an administrator may plan.
	mustExec(t, s.st, `UPDATE users SET role='user' WHERE username=?`, actor.username)
	if _, err := platformProxyCreate(t, s, actor, installation, proxyHosts, 1<<30); !errors.Is(err, errAppPlatformActorUnavailable) {
		t.Fatalf("non-admin: %v", err)
	}
}

func TestPlatformProxyCreateRefusals(t *testing.T) {
	t.Parallel()
	testPlatformProxyCreateRefusals(t, false)
}
func TestPlatformProxyCreateRefusalsPostgres(t *testing.T) {
	t.Parallel()
	runOnPostgres(t, testPlatformProxyCreateRefusals)
}

func testPlatformProxyConsumeRefusals(t *testing.T, pg bool) {
	granted := func(t *testing.T, s *Server, actor platformActorSession, installation string) {
		t.Helper()
		r, err := platformProxyCreate(t, s, actor, installation, proxyHosts, appplatform.ProxyDailyBytesMax)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.consumePlatformProxyReview(context.Background(), actor, platformRuntimeEndpoint{}, r.ID, r.SHA256); err != nil {
			t.Fatal(err)
		}
	}
	cases := []struct {
		name   string
		change func(t *testing.T, s *Server, actor platformActorSession, installation string, r platformProxyResult)
		want   error
	}{
		{name: "another review consumed first", change: func(t *testing.T, s *Server, actor platformActorSession, i string, r platformProxyResult) {
			other, err := platformProxyCreate(t, s, actor, i, []string{"qt.gtimg.cn"}, 1<<30)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.consumePlatformProxyReview(context.Background(), actor, platformRuntimeEndpoint{}, other.ID, other.SHA256); err != nil {
				t.Fatal(err)
			}
			// A consume expires every other pending plan of the installation.
		}, want: errAppPlatformPlanExpired},
		{name: "grant row edited", change: func(t *testing.T, s *Server, _ platformActorSession, i string, _ platformProxyResult) {
			mustExec(t, s.st, `UPDATE app_platform_capability_grants SET policy_json='{"x":1}' WHERE installation_id=? AND capability='network.proxy'`, i)
		}, want: errAppPlatformPlanChanged},
		{name: "policy revision moved", change: func(t *testing.T, s *Server, _ platformActorSession, i string, _ platformProxyResult) {
			mustExec(t, s.st, `UPDATE app_platform_installations SET policy_revision=policy_revision+1 WHERE id=?`, i)
		}, want: errPlatformCapabilityPolicyBusy},
		{name: "installation revision moved", change: func(t *testing.T, s *Server, _ platformActorSession, i string, _ platformProxyResult) {
			mustExec(t, s.st, `UPDATE app_platform_installations SET revision=revision+1 WHERE id=?`, i)
		}, want: errAppPlatformPlanChanged},
		{name: "trust revision moved", change: func(t *testing.T, s *Server, _ platformActorSession, i string, r platformProxyResult) {
			mustExec(t, s.st, `UPDATE app_platform_publishers SET revision=revision+1 WHERE id=?`, r.Plan.PublisherID)
		}, want: errAppPlatformPlanChanged},
		{name: "plan expired", change: func(t *testing.T, s *Server, _ platformActorSession, i string, r platformProxyResult) {
			mustExec(t, s.st, `UPDATE app_platform_review_plans SET expires_ms=? WHERE id=?`, time.Now().Add(-time.Second).UnixMilli(), r.ID)
		}, want: errAppPlatformPlanExpired},
		{name: "plan edited", change: func(t *testing.T, s *Server, _ platformActorSession, i string, r platformProxyResult) {
			mustExec(t, s.st, `UPDATE app_platform_review_plans SET plan_json=plan_json||' ' WHERE id=?`, r.ID)
		}, want: errAppPlatformPlanChanged},
		{name: "background.run withdrawn", change: func(t *testing.T, s *Server, _ platformActorSession, i string, _ platformProxyResult) {
			mustExec(t, s.st, `UPDATE app_platform_capability_grants SET granted=0 WHERE installation_id=? AND capability='background.run'`, i)
		}, want: errPlatformCapabilityPolicyBusy},
		{name: "installation disabled", change: func(t *testing.T, s *Server, _ platformActorSession, i string, _ platformProxyResult) {
			mustExec(t, s.st, `UPDATE app_platform_installations SET desired_state='disabled' WHERE id=?`, i)
		}, want: errPlatformCapabilityPolicyBusy},
		{name: "runner no longer answers", change: func(t *testing.T, s *Server, _ platformActorSession, i string, _ platformProxyResult) {
			proxyRunnerOf(s).fail(transport.ErrProxyUnsupported)
		}, want: transport.ErrProxyUnsupported},
		{name: "runner changed its limits", change: func(t *testing.T, s *Server, _ platformActorSession, i string, _ platformProxyResult) {
			r := proxyRunnerOf(s)
			r.mu.Lock()
			r.profile.Tunnels = 99
			r.mu.Unlock()
		}, want: transport.ErrProxyUnsupported},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, actor, installation := platformProxyFixture(t, pg)
			r, err := platformProxyCreate(t, s, actor, installation, proxyHosts, appplatform.ProxyDailyBytesMax)
			if err != nil {
				t.Fatal(err)
			}
			tc.change(t, s, actor, installation, r)
			before := platformCapabilityTableDump(t, s)
			if _, err := s.consumePlatformProxyReview(context.Background(), actor, platformRuntimeEndpoint{}, r.ID, r.SHA256); !errors.Is(err, tc.want) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
			if after := platformCapabilityTableDump(t, s); string(after) != string(before) {
				t.Fatal("a refused consume changed the database")
			}
		})
	}
	t.Run("another administrator's plan", func(t *testing.T) {
		s, actor, installation := platformProxyFixture(t, pg)
		r, err := platformProxyCreate(t, s, actor, installation, proxyHosts, appplatform.ProxyDailyBytesMax)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.consumePlatformProxyReview(context.Background(), actor, platformRuntimeEndpoint{}, r.ID, platformDigest([]byte("not the plan"))); !errors.Is(err, errAppPlatformPlanChanged) {
			t.Fatalf("wrong digest: %v", err)
		}
	})
	t.Run("withdraw", func(t *testing.T) {
		s, actor, installation := platformProxyFixture(t, pg)
		granted(t, s, actor, installation)
		revision := scalar[int64](t, s.st, `SELECT policy_revision FROM app_platform_installations WHERE id=?`, installation)
		r, err := platformProxyCreate(t, s, actor, installation, []string{}, 0)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Join(r.Plan.HostsRemoved, ",") != "*.eastmoney.com,qt.gtimg.cn" {
			t.Fatalf("plan: %+v", r.Plan)
		}
		if _, err := s.consumePlatformProxyReview(context.Background(), actor, platformRuntimeEndpoint{}, r.ID, r.SHA256); err != nil {
			t.Fatal(err)
		}
		if g, policy, _ := platformProxyGrantRow(t, s, installation); g != 0 || policy != "{}" {
			t.Fatalf("after withdrawal: %d %s", g, policy)
		}
		if scalar[int64](t, s.st, `SELECT policy_revision FROM app_platform_installations WHERE id=?`, installation) != revision+1 {
			t.Fatal("a withdrawal did not move the policy revision")
		}
		// A withdrawn grant is no resource of a session.
		a, err := s.loadPlatformServiceEgressCurrent(context.Background(), installation, platformServiceEgressLoad{})
		if err != nil {
			t.Fatal(err)
		}
		defer a.Close()
		if strings.Contains(strings.Join(a.resources, ","), "network.proxy") || a.proxy != nil {
			t.Fatalf("withdrawn grant still planned: %v", a.resources)
		}
	})
	// Withdrawing never waits for the runner and never needs a review to back the row: an old, offline or
	// changed runner, or a row nothing proves, is no reason to leave a proxy on.
	for name, break_ := range map[string]func(t *testing.T, s *Server, installation string){
		"old runner": func(t *testing.T, s *Server, _ string) { proxyRunnerOf(s).fail(transport.ErrProxyUnsupported) },
		"unbacked row": func(t *testing.T, s *Server, installation string) {
			other, _ := appplatform.CanonicalProxyPolicy(appplatform.ProxyPolicy{Hosts: []string{"other.example.com"}, DailyBytes: appplatform.ProxyDailyBytesMax})
			mustExec(t, s.st, `UPDATE app_platform_capability_grants SET policy_json=? WHERE installation_id=? AND capability='network.proxy'`, string(other), installation)
		},
	} {
		t.Run("withdraw with "+name, func(t *testing.T) {
			s, actor, installation := platformProxyFixture(t, pg)
			granted(t, s, actor, installation)
			break_(t, s, installation)
			if name == "unbacked row" {
				if _, err := platformProxyCreate(t, s, actor, installation, proxyHosts, 1<<30); !errors.Is(err, errPlatformCapabilityPolicyBusy) {
					t.Fatalf("a change to an unbacked row: %v", err)
				}
			}
			r, err := platformProxyCreate(t, s, actor, installation, []string{}, 0)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.consumePlatformProxyReview(context.Background(), actor, platformRuntimeEndpoint{}, r.ID, r.SHA256); err != nil {
				t.Fatal(err)
			}
			if g, policy, _ := platformProxyGrantRow(t, s, installation); g != 0 || policy != "{}" {
				t.Fatalf("after withdrawal: %d %s", g, policy)
			}
		})
	}
	t.Run("a change changes something", func(t *testing.T) {
		s, actor, installation := platformProxyFixture(t, pg)
		granted(t, s, actor, installation)
		if _, err := platformProxyCreate(t, s, actor, installation, proxyHosts, appplatform.ProxyDailyBytesMax); !errors.Is(err, errPlatformManagementInput) {
			t.Fatalf("an unchanged policy was planned: %v", err)
		}
		r, err := platformProxyCreate(t, s, actor, installation, []string{"*.eastmoney.com", "*.push2.eastmoney.com", "qt.gtimg.cn"}, appplatform.ProxyDailyBytesMax)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Join(r.Plan.HostsAdded, ",") != "*.push2.eastmoney.com" || len(r.Plan.HostsRemoved) != 0 {
			t.Fatalf("diff: %+v", r.Plan)
		}
		lower, err := platformProxyCreate(t, s, actor, installation, proxyHosts, 1<<30)
		if err != nil || lower.Plan.DailyBytesNew != 1<<30 {
			t.Fatalf("a lower budget alone is a change: %v %+v", err, lower.Plan)
		}
	})
}

func TestPlatformProxyConsumeRefusals(t *testing.T) {
	t.Parallel()
	testPlatformProxyConsumeRefusals(t, false)
}
func TestPlatformProxyConsumeRefusalsPostgres(t *testing.T) {
	t.Parallel()
	runOnPostgres(t, testPlatformProxyConsumeRefusals)
}
