package app

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/broker"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/transport"
)

var proxySeedCounter int

// platformServiceEgressProxyFixture is the writable egress fixture whose release also declares network.proxy
// with the grant off, as after install.
func platformServiceEgressProxyFixture(t *testing.T, pg bool) (*Server, string) {
	t.Helper()
	platformFixtureManifestHook = func(m *appplatform.Manifest) {
		m.Capabilities.Required = []string{"background.run", "network.http", "notifications.publish"}
		m.Capabilities.Optional = []string{"network.proxy"}
		m.NetworkProxy = &appplatform.NetworkProxy{Hosts: []string{"qt.gtimg.cn"}}
	}
	platformFixtureNetworkTarget = true
	t.Cleanup(func() { platformFixtureManifestHook, platformFixtureNetworkTarget = nil, false })
	s, _, lease := platformWritableAccessFixture(t, pg)
	mustExec(t, s.st, `UPDATE app_platform_capability_grants SET granted=1 WHERE installation_id=? AND capability<>'network.proxy'`, lease.installationID)
	return s, lease.installationID
}

// platformProxySeedGrant writes what a consumed proxy review leaves behind: a consumed plan, the grant row
// and one revision for the installation, its grants and its active activation.
func platformProxySeedGrant(t *testing.T, s *Server, installation string, hosts []string, daily int64) string {
	t.Helper()
	raw, err := appplatform.CanonicalProxyPolicy(appplatform.ProxyPolicy{Hosts: hosts, DailyBytes: daily})
	if err != nil {
		t.Fatal(err)
	}
	return platformProxySeedPolicy(t, s, installation, string(raw), true)
}

func platformProxySeedPolicy(t *testing.T, s *Server, installation, policy string, granted bool) string {
	t.Helper()
	var appID, publisherID, epoch string
	var generation, revision, policyRevision int64
	if err := s.st.queryRow(`SELECT app_id,publisher_id,controller_epoch,generation,revision,policy_revision FROM app_platform_installations WHERE id=?`, installation).Scan(&appID, &publisherID, &epoch, &generation, &revision, &policyRevision); err != nil {
		t.Fatal(err)
	}
	release := scalar[string](t, s.st, `SELECT release_sha256 FROM app_platform_activations WHERE installation_id=? AND state='active'`, installation)
	trust := scalar[int64](t, s.st, `SELECT revision FROM app_platform_publishers WHERE id=?`, publisherID)
	plan := platformProxyPlan{
		Format: platformProxyReviewFormat, InstallationID: installation, AppID: appID, PublisherID: publisherID,
		Generation: generation, InstallationRevision: revision, PolicyRevision: policyRevision, TrustRevision: trust,
		ControllerEpoch: epoch, ReleaseSHA256: release, Capability: platformProxyCapability,
		PreviousPolicySHA256: platformDigest([]byte("{}")), Policy: policy, PolicySHA256: platformDigest([]byte(policy)),
		HostsAdded: []string{}, HostsRemoved: []string{}, DailyBytesOld: appplatform.ProxyDailyBytesMax, Runner: transport.NewProxyProfile(),
		ActorAuthority: platformDigest([]byte("fixture-authority")),
	}
	if policy != "{}" {
		parsed, err := appplatform.ParseProxyPolicy([]byte(policy))
		if err != nil {
			t.Fatal(err)
		}
		plan.HostsAdded, plan.DailyBytesNew = parsed.Policy.Hosts, parsed.Policy.DailyBytes
		if len(parsed.Policy.Pins) > 0 {
			// A policy with pins is backed by a 1-dev.2 plan (doc 186); one without keeps the 1-dev.1 plan.
			plan.Format = platformProxyReviewFormatPins
			plan.PinsAdded, plan.PinsRemoved, plan.PinsChanged = parsed.Policy.Pins, []appplatform.ProxyPin{}, []platformProxyPinChange{}
			plan.RunnerPins = parsed.Policy.Pins
		}
	}
	encoded, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	proxySeedCounter++
	id := platformFixtureID(8000 + proxySeedCounter)
	now := time.Now().UnixMilli()
	mustExec(t, s.st, `INSERT INTO app_platform_review_plans(id,installation_id,app_id,publisher_id,action,source_release_sha256,target_release_sha256,installation_revision,policy_revision,trust_revision,controller_epoch,actor_scope,username,account_created,account_generation,authority_fingerprint,plan_sha256,plan_json,expires_ms,consumed_ms,created_ms,updated_ms)
		VALUES(?,?,?,?,'update',?,NULL,?,?,?,?,?,NULL,'',0,?,?,?,?,?,?,?)`,
		id, installation, appID, publisherID, release, revision, policyRevision, trust, epoch,
		platformDigest([]byte("fixture-scope")), plan.ActorAuthority, platformDigest(encoded), string(encoded), now+60000, now, now, now)
	next := policyRevision + 1
	mustExec(t, s.st, `UPDATE app_platform_capability_grants SET policy_revision=? WHERE installation_id=?`, next, installation)
	mustExec(t, s.st, `UPDATE app_platform_capability_grants SET granted=?,policy_json=? WHERE installation_id=? AND capability='network.proxy'`, boolInt(granted), policy, installation)
	mustExec(t, s.st, `UPDATE app_platform_activations SET policy_revision=? WHERE installation_id=? AND state='active'`, next, installation)
	mustExec(t, s.st, `UPDATE app_platform_installations SET policy_revision=? WHERE id=?`, next, installation)
	return id
}

func testPlatformServiceEgressProxySessionCarriesTheReviewedPolicy(t *testing.T, pg bool) {
	s, installation := platformServiceEgressProxyFixture(t, pg)
	f := newFakeEgressRunner()
	c, _ := newEgressTestCoordinator(t, s, f)
	ctx := context.Background()
	// Without a grant the session is the first format, byte for byte what it was.
	c.tick(ctx)
	plain := nextEgressSession(t, f)
	if plain.command.Format != "portal.service-egress-session/1-dev.1" || plain.command.Proxy != nil || strings.Contains(strings.Join(plain.command.Resources, ","), "network.proxy") {
		t.Fatalf("a session without a proxy grant: %+v", plain.command)
	}
	// A reviewed grant makes the next session a proxy session; the policy digest is in the audit row.
	platformProxySeedGrant(t, s, installation, []string{"*.eastmoney.com", "qt.gtimg.cn"}, 1<<30)
	c.tick(ctx) // the drift of the old session is observed here
	waitEgressEnded(t, plain.runner, transport.EgressCloseDrift)
	c.tick(ctx)
	with := nextEgressSession(t, f)
	cmd := with.command
	if cmd.Format != "portal.service-egress-session/1-dev.2" || cmd.Proxy == nil || strings.Join(cmd.Resources, ",") != "network.http,network.proxy,notifications.publish" {
		t.Fatalf("a session with a proxy grant: %+v", cmd)
	}
	parsed, err := appplatform.ParseProxyPolicy([]byte(cmd.Proxy.Policy))
	if err != nil || strings.Join(parsed.Policy.Hosts, ",") != "*.eastmoney.com,qt.gtimg.cn" || parsed.Policy.DailyBytes != 1<<30 || cmd.Proxy.PolicySHA256 != parsed.SHA256 {
		t.Fatalf("carried policy: %+v %v", cmd.Proxy, err)
	}
	rows := egressAuditRows(t, s, platformServiceEgressAuditOpened, installation)
	if last := rows[len(rows)-1]; !strings.Contains(last, "network.proxy") {
		t.Fatalf("the opened audit row does not name the proxy resource: %s", last)
	}
	// Withdrawing the grant, through the same revision bump a review makes, drifts the session; the next one is plain again.
	platformProxySeedPolicy(t, s, installation, "{}", false)
	c.tick(ctx)
	waitEgressEnded(t, with.runner, transport.EgressCloseDrift)
	c.tick(ctx)
	back := nextEgressSession(t, f)
	if back.command.Proxy != nil || back.command.Format != "portal.service-egress-session/1-dev.1" {
		t.Fatalf("a withdrawn grant is still carried: %+v", back.command)
	}
}

func TestPlatformServiceEgressProxySessionCarriesTheReviewedPolicy(t *testing.T) {
	testPlatformServiceEgressProxySessionCarriesTheReviewedPolicy(t, false)
}
func TestPlatformServiceEgressProxySessionCarriesTheReviewedPolicyPostgres(t *testing.T) {
	testPlatformServiceEgressProxySessionCarriesTheReviewedPolicy(t, true)
}

func testPlatformServiceEgressProxyDriftOnPolicyChange(t *testing.T, pg bool) {
	s, installation := platformServiceEgressProxyFixture(t, pg)
	platformProxySeedGrant(t, s, installation, []string{"qt.gtimg.cn"}, appplatform.ProxyDailyBytesMax)
	f := newFakeEgressRunner()
	c, _ := newEgressTestCoordinator(t, s, f)
	ctx := context.Background()
	c.tick(ctx)
	first := nextEgressSession(t, f)
	// A changed policy is a changed revision: the session closes as drift, and the next carries the new policy.
	platformProxySeedGrant(t, s, installation, []string{"*.eastmoney.com", "qt.gtimg.cn"}, appplatform.ProxyDailyBytesMax)
	c.tick(ctx)
	waitEgressEnded(t, first.runner, transport.EgressCloseDrift)
	c.tick(ctx)
	second := nextEgressSession(t, f)
	if !strings.Contains(second.command.Proxy.Policy, "*.eastmoney.com") {
		t.Fatalf("the new session carries the old policy: %s", second.command.Proxy.Policy)
	}
}

func TestPlatformServiceEgressProxyDriftOnPolicyChange(t *testing.T) {
	testPlatformServiceEgressProxyDriftOnPolicyChange(t, false)
}

func TestPlatformServiceEgressProxyGrantNeedsAReviewThatBacksIt(t *testing.T) {
	s, installation := platformServiceEgressProxyFixture(t, false)
	// A grant row nobody reviewed is no resource: the session is a plain one.
	policy, _ := appplatform.CanonicalProxyPolicy(appplatform.ProxyPolicy{Hosts: []string{"qt.gtimg.cn"}, DailyBytes: appplatform.ProxyDailyBytesMax})
	mustExec(t, s.st, `UPDATE app_platform_capability_grants SET granted=1,policy_json=? WHERE installation_id=? AND capability='network.proxy'`, string(policy), installation)
	a, err := s.loadPlatformServiceEgressCurrent(context.Background(), installation, platformServiceEgressLoad{})
	if err != nil {
		t.Fatal(err)
	}
	if a.proxy != nil || strings.Contains(strings.Join(a.resources, ","), "network.proxy") {
		t.Fatalf("an unreviewed grant row became a resource: %v", a.resources)
	}
	a.Close()
	// Naming it explicitly fails.
	if a, err := s.loadPlatformServiceEgressCurrent(context.Background(), installation, platformServiceEgressLoad{resources: []string{"network.http", "network.proxy"}}); err == nil {
		a.Close()
		t.Fatal("an unreviewed proxy grant was accepted by name")
	}
	// A review of another release, another policy or another epoch does not back it either.
	for name, change := range map[string]func(t *testing.T, s *Server, installation string){
		"another policy": func(t *testing.T, s *Server, installation string) {
			mustExec(t, s.st, `UPDATE app_platform_capability_grants SET policy_json=REPLACE(policy_json,'qt.gtimg.cn','other.example.com') WHERE installation_id=? AND capability='network.proxy'`, installation)
		},
		"another release": func(t *testing.T, s *Server, installation string) {
			other := platformCapabilityOtherRelease(t, s, installation)
			mustExec(t, s.st, `UPDATE app_platform_review_plans SET source_release_sha256=? WHERE installation_id=? AND plan_json LIKE '%proxy-policy-review%'`, other, installation)
		},
		"another epoch": func(t *testing.T, s *Server, installation string) {
			mustExec(t, s.st, `UPDATE app_platform_review_plans SET controller_epoch=? WHERE installation_id=? AND plan_json LIKE '%proxy-policy-review%'`, platformFixtureID(7791), installation)
		},
	} {
		t.Run(name, func(t *testing.T) {
			s, installation := platformServiceEgressProxyFixture(t, false)
			platformProxySeedGrant(t, s, installation, []string{"qt.gtimg.cn"}, appplatform.ProxyDailyBytesMax)
			a, err := s.loadPlatformServiceEgressCurrent(context.Background(), installation, platformServiceEgressLoad{})
			if err != nil || a.proxy == nil {
				t.Fatalf("a reviewed grant is not a resource: %v", err)
			}
			a.Close()
			change(t, s, installation)
			a, err = s.loadPlatformServiceEgressCurrent(context.Background(), installation, platformServiceEgressLoad{})
			if err != nil {
				t.Fatal(err)
			}
			defer a.Close()
			if a.proxy != nil {
				t.Fatal("a grant that no consumed review backs became a resource")
			}
		})
	}
}

func TestPlatformServiceEgressProxyOldRunnerGetsNoProxy(t *testing.T) {
	s, installation := platformServiceEgressProxyFixture(t, false)
	platformProxySeedGrant(t, s, installation, []string{"qt.gtimg.cn"}, appplatform.ProxyDailyBytesMax)
	f := newFakeEgressRunner()
	f.setProxy(transport.ProxyProfile{}, transport.ErrProxyUnsupported)
	c, clock := newEgressTestCoordinator(t, s, f)
	c.tick(context.Background())
	session := nextEgressSession(t, f)
	if session.command.Proxy != nil || strings.Contains(strings.Join(session.command.Resources, ","), "network.proxy") || session.command.Format != "portal.service-egress-session/1-dev.1" {
		t.Fatalf("a runner without the proxy was sent a policy: %+v", session.command)
	}
	if strings.Join(session.command.Resources, ",") != "network.http,notifications.publish" {
		t.Fatalf("the other resources were lost: %v", session.command.Resources)
	}
	// A runner whose answer carries other limits is no runner for the proxy either.
	other := transport.NewProxyProfile()
	other.Tunnels = 99
	f.setProxy(other, nil)
	clock.Advance(4*time.Minute + 10*time.Second)
	c.tick(context.Background())
	renewed := nextEgressSession(t, f)
	if renewed.command.Proxy != nil || strings.Contains(strings.Join(renewed.command.Resources, ","), "network.proxy") {
		t.Fatalf("a runner enforcing other limits was sent a policy: %+v", renewed.command)
	}
	if queries := f.proxyQueries(); queries != 2 {
		t.Fatalf("proxy queries %d, want one per session", queries)
	}
	// A failure to ask is not "unsupported": the session is not opened, so a granted proxy is never silently dropped.
	f.setProxy(transport.ProxyProfile{}, context.DeadlineExceeded)
	clock.Advance(4*time.Minute + 10*time.Second)
	c.tick(context.Background())
	requireNoEgressSession(t, f)
}

func TestPlatformServiceEgressProxyOnlyInstallationWithOldRunnerOpensNothing(t *testing.T) {
	s, installation := platformServiceEgressProxyFixture(t, false)
	mustExec(t, s.st, `UPDATE app_platform_capability_grants SET granted=0 WHERE installation_id=? AND capability IN ('network.http','notifications.publish')`, installation)
	platformProxySeedGrant(t, s, installation, []string{"qt.gtimg.cn"}, appplatform.ProxyDailyBytesMax)
	f := newFakeEgressRunner()
	f.setProxy(transport.ProxyProfile{}, transport.ErrProxyUnsupported)
	c, _ := newEgressTestCoordinator(t, s, f)
	c.tick(context.Background())
	requireNoEgressSession(t, f)
	f.setProxy(transport.NewProxyProfile(), nil)
	c.tick(context.Background())
	c.mu.Lock()
	c.retryAt = map[string]time.Time{}
	c.mu.Unlock()
	c.tick(context.Background())
	session := nextEgressSession(t, f)
	if strings.Join(session.command.Resources, ",") != "network.proxy" || session.command.Proxy == nil {
		t.Fatalf("a proxy-only installation: %+v", session.command)
	}
}

func TestPlatformServiceEgressProxyReportsBecomeAuditRows(t *testing.T) {
	s, installation := platformServiceEgressProxyFixture(t, false)
	platformProxySeedGrant(t, s, installation, []string{"qt.gtimg.cn"}, appplatform.ProxyDailyBytesMax)
	f := newFakeEgressRunner()
	c, _ := newEgressTestCoordinator(t, s, f)
	c.tick(context.Background())
	session := nextEgressSession(t, f)
	send := func(r broker.ProxyReport) {
		t.Helper()
		payload, err := transport.EncodeEgressProxyReport(r)
		if err != nil {
			t.Fatal(err)
		}
		call, err := session.runner.NewCall()
		if err != nil {
			t.Fatal(err)
		}
		defer call.Finish()
		if err := call.Send(transport.EgressFrameProxyReport, payload); err != nil {
			t.Fatal(err)
		}
	}
	send(broker.ProxyReport{Kind: broker.ProxyReportRollup, Hour: "2026-11-19T10:00:00Z", Hosts: []broker.ProxyHostRollup{
		{Host: "a.example.com", Tunnels: 2, BytesIn: 100, BytesOut: 50, Closes: map[string]int64{"eof": 2}},
		{Host: "qt.gtimg.cn", Tunnels: 1, BytesIn: 10, BytesOut: 5, Closes: map[string]int64{"idle": 1}},
	}})
	send(broker.ProxyReport{Kind: broker.ProxyReportRefusal, Host: "evil.example.com", Code: "host_not_reviewed", Count: 4})
	send(broker.ProxyReport{Kind: broker.ProxyReportUsage, Day: "2026-11-19", Bytes: 1234, Budget: appplatform.ProxyDailyBytesMax})
	waitEgress(t, func() bool {
		return len(egressAuditRows(t, s, "app_platform.proxy_rollup", installation)) == 2 &&
			len(egressAuditRows(t, s, "app_platform.proxy_refused", installation)) == 1 &&
			len(egressAuditRows(t, s, "app_platform.proxy_usage", installation)) == 1
	})
	rollups := egressAuditRows(t, s, "app_platform.proxy_rollup", installation)
	if egressAuditField(t, rollups[1], "host") != "qt.gtimg.cn" || egressAuditField(t, rollups[1], "hour") != "2026-11-19T10:00:00Z" || egressAuditField(t, rollups[1], "tunnels") != float64(1) {
		t.Fatalf("rollup row: %s", rollups[1])
	}
	refusal := egressAuditRows(t, s, "app_platform.proxy_refused", installation)[0]
	if egressAuditField(t, refusal, "host") != "evil.example.com" || egressAuditField(t, refusal, "code") != "host_not_reviewed" || egressAuditField(t, refusal, "count") != float64(4) {
		t.Fatalf("refusal row: %s", refusal)
	}
	// A second refusal row for the same host and code within a minute is dropped.
	send(broker.ProxyReport{Kind: broker.ProxyReportRefusal, Host: "evil.example.com", Code: "host_not_reviewed", Count: 9})
	send(broker.ProxyReport{Kind: broker.ProxyReportRefusal, Host: "evil.example.com", Code: "port_not_permitted", Count: 1})
	waitEgress(t, func() bool { return len(egressAuditRows(t, s, "app_platform.proxy_refused", installation)) == 2 })
	time.Sleep(200 * time.Millisecond)
	if rows := egressAuditRows(t, s, "app_platform.proxy_refused", installation); len(rows) != 2 {
		t.Fatalf("refusal rows are not rate limited: %v", rows)
	}
}

func TestPlatformProxyRefusalLimiter(t *testing.T) {
	var l platformProxyRefusalLimiter
	now := time.Now()
	a, b := platformFixtureID(1), platformFixtureID(2)
	if !l.allow(a, "x.example.com", "busy", now) || l.allow(a, "x.example.com", "busy", now.Add(30*time.Second)) {
		t.Fatal("one row per installation, host and code per minute")
	}
	if !l.allow(a, "x.example.com", "host_not_reviewed", now) || !l.allow(b, "x.example.com", "busy", now) {
		t.Fatal("another code or another installation shares the key")
	}
	if !l.allow(a, "x.example.com", "busy", now.Add(61*time.Second)) {
		t.Fatal("a minute later the key is free again")
	}
	// The host is text the application chose: one installation's variety is capped, and does not take
	// another installation's rows with it.
	c, d := platformFixtureID(3), platformFixtureID(4)
	allowed := 0
	for i := 0; i < 200; i++ {
		if l.allow(c, "h"+string(rune('a'+i/26))+string(rune('a'+i%26))+".example.com", "busy", now) {
			allowed++
		}
	}
	if allowed != platformProxyRefusalRows {
		t.Fatalf("%d rows for one installation in a minute, want %d", allowed, platformProxyRefusalRows)
	}
	if !l.allow(d, "h.example.com", "busy", now) {
		t.Fatal("an installation's flood suppressed another installation's row")
	}
	if !l.allow(c, "late.example.com", "busy", now.Add(61*time.Second)) {
		t.Fatal("the window does not slide")
	}
}

func TestPlatformServiceEgressProxyReportsAreIgnoredWithoutTheResource(t *testing.T) {
	s, installation := platformServiceEgressProxyFixture(t, false)
	f := newFakeEgressRunner()
	c, _ := newEgressTestCoordinator(t, s, f)
	c.tick(context.Background())
	session := nextEgressSession(t, f) // no proxy grant: a plain session
	payload, _ := transport.EncodeEgressProxyReport(broker.ProxyReport{Kind: broker.ProxyReportRefusal, Host: "evil.example.com", Code: "busy", Count: 1})
	call, err := session.runner.NewCall()
	if err != nil {
		t.Fatal(err)
	}
	defer call.Finish()
	if err := call.Send(transport.EgressFrameProxyReport, payload); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	if rows := egressAuditRows(t, s, "app_platform.proxy_refused", installation); len(rows) != 0 {
		t.Fatalf("a session without the proxy wrote a proxy row: %v", rows)
	}
}
