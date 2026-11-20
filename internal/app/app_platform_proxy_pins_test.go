package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/broker"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/transport"
)

// Pinned destinations of the reviewed forward proxy, Portal side (doc 186).

var (
	pinQuotes = appplatform.ProxyPin{Host: "quotes.internal.anchan.kazuhahub.com", Address: "127.0.0.1", Port: 20443}
	pinMoved  = appplatform.ProxyPin{Host: "quotes.internal.anchan.kazuhahub.com", Address: "127.0.0.1", Port: 20444}
	pinOther  = appplatform.ProxyPin{Host: "other.internal.anchan.kazuhahub.com", Address: "10.0.0.5", Port: 443}
)

func platformProxyCreatePins(t *testing.T, s *Server, actor platformActorSession, installation string, hosts []string, pins []appplatform.ProxyPin, daily int64) (platformProxyResult, error) {
	t.Helper()
	req := platformProxyRequestFor(t, s, installation, hosts, daily)
	req.Pins = pins
	return s.createPlatformProxyReview(context.Background(), actor, platformRuntimeEndpoint{}, installation, req)
}

func platformProxyConsume(t *testing.T, s *Server, actor platformActorSession, r platformProxyResult) error {
	t.Helper()
	_, err := s.consumePlatformProxyReview(context.Background(), actor, platformRuntimeEndpoint{}, r.ID, r.SHA256)
	return err
}

// A plan with pins against a runner that answers only PRPY0001, or whose pins lack the exact {host, address,
// port}, is 409 app_platform_runner_unsupported, and malformed pins are input errors. Nothing is written.
// Fails closed.
func testPlatformProxyPinRefusals(t *testing.T, pg bool) {
	var nine []appplatform.ProxyPin
	for i := 0; i < 9; i++ {
		nine = append(nine, appplatform.ProxyPin{Host: string(rune('a'+i)) + ".example.com", Address: "127.0.0.1", Port: 443})
	}
	cases := []struct {
		name  string
		setup func(r *platformProxyRunner)
		hosts []string
		pins  []appplatform.ProxyPin
		want  error
	}{
		{name: "runner answers only PRPY0001", setup: func(r *platformProxyRunner) {}, pins: []appplatform.ProxyPin{pinQuotes}, want: transport.ErrProxyUnsupported},
		{name: "runner holds no pins", setup: func(r *platformProxyRunner) { r.servePins() }, pins: []appplatform.ProxyPin{pinQuotes}, want: transport.ErrProxyUnsupported},
		{name: "other port", setup: func(r *platformProxyRunner) { r.servePins(pinMoved) }, pins: []appplatform.ProxyPin{pinQuotes}, want: transport.ErrProxyUnsupported},
		{name: "other address", setup: func(r *platformProxyRunner) {
			r.servePins(appplatform.ProxyPin{Host: pinQuotes.Host, Address: "127.0.0.2", Port: 20443})
		}, pins: []appplatform.ProxyPin{pinQuotes}, want: transport.ErrProxyUnsupported},
		{name: "other host", setup: func(r *platformProxyRunner) { r.servePins(pinOther) }, pins: []appplatform.ProxyPin{pinQuotes}, want: transport.ErrProxyUnsupported},
		{name: "only one of two pins listed", setup: func(r *platformProxyRunner) { r.servePins(pinOther) }, pins: []appplatform.ProxyPin{pinOther, pinQuotes}, want: transport.ErrProxyUnsupported},
		{name: "wildcard host", setup: func(r *platformProxyRunner) { r.servePins(pinQuotes) }, pins: []appplatform.ProxyPin{{Host: "*.internal.anchan.kazuhahub.com", Address: "127.0.0.1", Port: 20443}}, want: errPlatformManagementInput},
		{name: "port zero", setup: func(r *platformProxyRunner) { r.servePins(pinQuotes) }, pins: []appplatform.ProxyPin{{Host: pinQuotes.Host, Address: "127.0.0.1", Port: 0}}, want: errPlatformManagementInput},
		{name: "denied address", setup: func(r *platformProxyRunner) { r.servePins(pinQuotes) }, pins: []appplatform.ProxyPin{{Host: pinQuotes.Host, Address: "169.254.169.254", Port: 443}}, want: errPlatformManagementInput},
		{name: "duplicate name", setup: func(r *platformProxyRunner) { r.servePins(pinQuotes, pinMoved) }, pins: []appplatform.ProxyPin{pinQuotes, pinMoved}, want: errPlatformManagementInput},
		{name: "a name that is also an exact host", setup: func(r *platformProxyRunner) { r.servePins(pinQuotes) }, hosts: []string{pinQuotes.Host}, pins: []appplatform.ProxyPin{pinQuotes}, want: errPlatformManagementInput},
		{name: "nine pins", setup: func(r *platformProxyRunner) { r.servePins(nine...) }, pins: nine, want: errPlatformManagementInput},
		{name: "runner fails to answer", setup: func(r *platformProxyRunner) { r.servePins(pinQuotes); r.fail(context.DeadlineExceeded) }, pins: []appplatform.ProxyPin{pinQuotes}, want: context.DeadlineExceeded},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, actor, installation := platformProxyFixture(t, pg)
			tc.setup(proxyRunnerOf(s))
			before := platformCapabilityTableDump(t, s)
			hosts := tc.hosts
			if hosts == nil {
				hosts = []string{}
			}
			if _, err := platformProxyCreatePins(t, s, actor, installation, hosts, tc.pins, 1<<30); !errors.Is(err, tc.want) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
			if after := platformCapabilityTableDump(t, s); string(after) != string(before) {
				t.Fatal("a refused plan changed the database")
			}
		})
	}
}

func TestPlatformProxyPinRefusals(t *testing.T) { testPlatformProxyPinRefusals(t, false) }
func TestPlatformProxyPinRefusalsPostgres(t *testing.T) {
	runOnPostgres(t, testPlatformProxyPinRefusals)
}

// Consume writes the policy in 1-dev.2, raises the policy revision, and writes one audit row with the pin
// fields. A compare-and-swap loss writes nothing. Fails closed.
func testPlatformProxyPinPlanThenConsume(t *testing.T, pg bool) {
	s, actor, installation := platformProxyFixture(t, pg)
	proxyRunnerOf(s).servePins(pinOther, pinQuotes, pinMoved)
	revision := scalar[int64](t, s.st, `SELECT policy_revision FROM app_platform_installations WHERE id=?`, installation)
	result, err := platformProxyCreatePins(t, s, actor, installation, []string{"qt.gtimg.cn"}, []appplatform.ProxyPin{pinQuotes}, 1<<30)
	if err != nil {
		t.Fatal(err)
	}
	plan := result.Plan
	if plan.Format != platformProxyReviewFormatPins || plan.Runner != transport.NewProxyProfile() || len(plan.PinsAdded) != 1 || plan.PinsAdded[0] != pinQuotes ||
		plan.PinsRemoved == nil || len(plan.PinsRemoved) != 0 || plan.PinsChanged == nil || len(plan.PinsChanged) != 0 || len(plan.RunnerPins) != 1 || plan.RunnerPins[0] != pinQuotes {
		t.Fatalf("plan: %+v", plan)
	}
	if !strings.Contains(result.JSON, `"runner_pins":[{"host":"quotes.internal.anchan.kazuhahub.com","address":"127.0.0.1","port":20443}]`) {
		t.Fatalf("plan json: %s", result.JSON)
	}
	// Planning wrote no grant.
	if granted, _, _ := platformProxyGrantRow(t, s, installation); granted != 0 {
		t.Fatal("a plan granted the capability")
	}
	change, err := s.consumePlatformProxyReview(context.Background(), actor, platformRuntimeEndpoint{}, result.ID, result.SHA256)
	if err != nil || change.PolicyRevision != revision+1 {
		t.Fatalf("consume: %v %+v", err, change)
	}
	want, _ := appplatform.CanonicalProxyPolicy(appplatform.ProxyPolicy{Hosts: []string{"qt.gtimg.cn"}, Pins: []appplatform.ProxyPin{pinQuotes}, DailyBytes: 1 << 30})
	granted, policy, _ := platformProxyGrantRow(t, s, installation)
	if granted != 1 || policy != string(want) || !strings.Contains(policy, `"format":"portal.proxy-policy/1-dev.2"`) {
		t.Fatalf("grant row: %d %s", granted, policy)
	}
	if scalar[int](t, s.st, `SELECT COUNT(*) FROM audit_log WHERE action='app_platform.proxy_policy_changed' AND target_id=?`, installation) != 1 {
		t.Fatal("not exactly one audit row")
	}
	detail := scalar[string](t, s.st, `SELECT detail FROM audit_log WHERE action='app_platform.proxy_policy_changed' AND target_id=?`, installation)
	var row struct {
		PinsAdded   []string `json:"pins_added"`
		PinsRemoved []string `json:"pins_removed"`
		PinsChanged []struct {
			Host string `json:"host"`
			Old  string `json:"old"`
			New  string `json:"new"`
		} `json:"pins_changed"`
		PolicySHA string `json:"policy_sha256"`
	}
	if err := json.Unmarshal([]byte(detail), &row); err != nil || len(row.PinsAdded) != 1 || row.PinsAdded[0] != "quotes.internal.anchan.kazuhahub.com → 127.0.0.1:20443" ||
		row.PinsRemoved == nil || len(row.PinsRemoved) != 0 || row.PinsChanged == nil || len(row.PinsChanged) != 0 || row.PolicySHA != platformDigest(want) {
		t.Fatalf("audit detail: %s (%v)", detail, err)
	}

	// Move the pin and add another: the audit row names what changed, in the three fields.
	moved, err := platformProxyCreatePins(t, s, actor, installation, []string{"qt.gtimg.cn"}, []appplatform.ProxyPin{pinOther, pinMoved}, 1<<30)
	if err != nil {
		t.Fatal(err)
	}
	if len(moved.Plan.PinsAdded) != 1 || moved.Plan.PinsAdded[0] != pinOther || len(moved.Plan.PinsRemoved) != 0 ||
		len(moved.Plan.PinsChanged) != 1 || moved.Plan.PinsChanged[0] != (platformProxyPinChange{Host: pinQuotes.Host, Old: pinQuotes, New: pinMoved}) {
		t.Fatalf("diff: %+v", moved.Plan)
	}
	if err := platformProxyConsume(t, s, actor, moved); err != nil {
		t.Fatal(err)
	}
	detail = scalar[string](t, s.st, `SELECT detail FROM audit_log WHERE action='app_platform.proxy_policy_changed' AND target_id=? ORDER BY id DESC LIMIT 1`, installation)
	for _, want := range []string{`"pins_added":["other.internal.anchan.kazuhahub.com → 10.0.0.5:443"]`, `"pins_removed":[]`, `"pins_changed":[{"host":"quotes.internal.anchan.kazuhahub.com","old":"127.0.0.1:20443","new":"127.0.0.1:20444"}]`} {
		if !strings.Contains(detail, want) {
			t.Fatalf("audit detail lacks %s: %s", want, detail)
		}
	}

	// Remove a pin and keep the host: pins_removed.
	removed, err := platformProxyCreatePins(t, s, actor, installation, []string{"qt.gtimg.cn"}, []appplatform.ProxyPin{pinMoved}, 1<<30)
	if err != nil {
		t.Fatal(err)
	}
	if len(removed.Plan.PinsRemoved) != 1 || removed.Plan.PinsRemoved[0] != pinOther || len(removed.Plan.PinsAdded) != 0 || len(removed.Plan.PinsChanged) != 0 {
		t.Fatalf("diff: %+v", removed.Plan)
	}
	// A compare-and-swap loss writes nothing: the grant row was edited between the plan and the consume.
	mustExec(t, s.st, `UPDATE app_platform_capability_grants SET policy_json=REPLACE(policy_json,'20444','20445') WHERE installation_id=? AND capability='network.proxy'`, installation)
	before := platformCapabilityTableDump(t, s)
	auditRows := scalar[int](t, s.st, `SELECT COUNT(*) FROM audit_log WHERE action='app_platform.proxy_policy_changed'`)
	if err := platformProxyConsume(t, s, actor, removed); !errors.Is(err, errAppPlatformPlanChanged) {
		t.Fatalf("consume against a changed row: %v", err)
	}
	if string(platformCapabilityTableDump(t, s)) != string(before) || scalar[int](t, s.st, `SELECT COUNT(*) FROM audit_log WHERE action='app_platform.proxy_policy_changed'`) != auditRows {
		t.Fatal("a lost compare-and-swap wrote something")
	}
}

func TestPlatformProxyPinPlanThenConsume(t *testing.T) { testPlatformProxyPinPlanThenConsume(t, false) }
func TestPlatformProxyPinPlanThenConsumePostgres(t *testing.T) {
	runOnPostgres(t, testPlatformProxyPinPlanThenConsume)
}

// What the runner reports at consume must still list every pin of the plan; a runner that lost the entry, or
// went back to PRPY0001, is 409 and the database is unchanged. Fails closed.
func testPlatformProxyPinConsumeNeedsTheRunnerStill(t *testing.T, pg bool) {
	for name, change := range map[string]func(r *platformProxyRunner){
		"entry removed":   func(r *platformProxyRunner) { r.servePins() },
		"entry moved":     func(r *platformProxyRunner) { r.servePins(pinMoved) },
		"runner replaced": func(r *platformProxyRunner) { r.mu.Lock(); r.pinsSupported, r.pins = false, nil; r.mu.Unlock() },
	} {
		t.Run(name, func(t *testing.T) {
			s, actor, installation := platformProxyFixture(t, pg)
			proxyRunnerOf(s).servePins(pinQuotes)
			r, err := platformProxyCreatePins(t, s, actor, installation, []string{}, []appplatform.ProxyPin{pinQuotes}, 1<<30)
			if err != nil {
				t.Fatal(err)
			}
			change(proxyRunnerOf(s))
			before := platformCapabilityTableDump(t, s)
			if err := platformProxyConsume(t, s, actor, r); !errors.Is(err, transport.ErrProxyUnsupported) {
				t.Fatalf("got %v", err)
			}
			if after := platformCapabilityTableDump(t, s); string(after) != string(before) {
				t.Fatal("a refused consume changed the database")
			}
		})
	}
	// A plan with no pins is not held to the runner's pins: an operator edit does not invalidate it.
	s, actor, installation := platformProxyFixture(t, pg)
	proxyRunnerOf(s).servePins(pinQuotes)
	r, err := platformProxyCreatePins(t, s, actor, installation, proxyHosts, nil, 1<<30)
	if err != nil {
		t.Fatal(err)
	}
	proxyRunnerOf(s).servePins()
	if err := platformProxyConsume(t, s, actor, r); err != nil {
		t.Fatalf("an unpinned plan after an operator edit: %v", err)
	}
}

func TestPlatformProxyPinConsumeNeedsTheRunnerStill(t *testing.T) {
	testPlatformProxyPinConsumeNeedsTheRunnerStill(t, false)
}
func TestPlatformProxyPinConsumeNeedsTheRunnerStillPostgres(t *testing.T) {
	runOnPostgres(t, testPlatformProxyPinConsumeNeedsTheRunnerStill)
}

// A plan that removes every host and every pin withdraws the grant, and needs neither the runner nor pins
// support. A plan that removes only the pins is a change like any other.
func testPlatformProxyPinWithdraw(t *testing.T, pg bool) {
	s, actor, installation := platformProxyFixture(t, pg)
	proxyRunnerOf(s).servePins(pinQuotes)
	r, err := platformProxyCreatePins(t, s, actor, installation, proxyHosts, []appplatform.ProxyPin{pinQuotes}, 1<<30)
	if err != nil {
		t.Fatal(err)
	}
	if err := platformProxyConsume(t, s, actor, r); err != nil {
		t.Fatal(err)
	}
	proxyRunnerOf(s).fail(transport.ErrProxyUnsupported)
	w, err := platformProxyCreatePins(t, s, actor, installation, []string{}, nil, 0)
	if err != nil {
		t.Fatalf("withdraw with the runner gone: %v", err)
	}
	if len(w.Plan.PinsRemoved) != 1 || len(w.Plan.HostsRemoved) != 2 || w.Plan.Policy != "{}" || len(w.Plan.RunnerPins) != 0 {
		t.Fatalf("withdraw plan: %+v", w.Plan)
	}
	if err := platformProxyConsume(t, s, actor, w); err != nil {
		t.Fatal(err)
	}
	if g, policy, _ := platformProxyGrantRow(t, s, installation); g != 0 || policy != "{}" {
		t.Fatalf("after withdrawal: %d %s", g, policy)
	}
}

func TestPlatformProxyPinWithdraw(t *testing.T) { testPlatformProxyPinWithdraw(t, false) }
func TestPlatformProxyPinWithdrawPostgres(t *testing.T) {
	runOnPostgres(t, testPlatformProxyPinWithdraw)
}

// A 1-dev.2 plan consumed after a 1-dev.1 plan of the same installation is the one the loader reads (the
// higher policy revision), and a grant backed by the older plan alone is not granted. A consumed 1-dev.1 plan
// still backs its own grant. Fails closed for the grant the older plan no longer backs.
func testPlatformProxyPlanFormatsAcrossUpgrade(t *testing.T, pg bool) {
	s, actor, installation := platformProxyFixture(t, pg)
	// An old-format review: a consumed 1-dev.1 plan and its grant, as before the upgrade.
	oldPlan := platformProxySeedGrant(t, s, installation, []string{"qt.gtimg.cn"}, appplatform.ProxyDailyBytesMax)
	if !strings.HasPrefix(scalar[string](t, s.st, `SELECT plan_json FROM app_platform_review_plans WHERE id=?`, oldPlan), `{"format":"portal.proxy-policy-review/1-dev.1","`) {
		t.Fatal("the seeded plan is not 1-dev.1")
	}
	row, err := platformProxyLatest(t, s, actor, installation)
	if err != nil || row.plan.Format != platformProxyReviewFormat || row.id != oldPlan {
		t.Fatalf("the 1-dev.1 plan does not back its grant: %v %+v", err, row.plan.Format)
	}
	view := platformProxyViewOf(t, s, actor, installation)
	if !view.Granted {
		t.Fatal("a consumed 1-dev.1 plan stopped backing its grant after the upgrade")
	}
	// A new review (1-dev.2) is consumed over it.
	proxyRunnerOf(s).servePins(pinQuotes)
	r, err := platformProxyCreatePins(t, s, actor, installation, []string{"qt.gtimg.cn"}, []appplatform.ProxyPin{pinQuotes}, appplatform.ProxyDailyBytesMax)
	if err != nil {
		t.Fatal(err)
	}
	if err := platformProxyConsume(t, s, actor, r); err != nil {
		t.Fatal(err)
	}
	row, err = platformProxyLatest(t, s, actor, installation)
	if err != nil || row.plan.Format != platformProxyReviewFormatPins || row.id != r.ID {
		t.Fatalf("the latest plan is not the 1-dev.2 plan: %v %+v", err, row.id)
	}
	// A grant row restored to the older policy is no grant: the older plan alone does not back it any more.
	old, _ := appplatform.CanonicalProxyPolicy(appplatform.ProxyPolicy{Hosts: []string{"qt.gtimg.cn"}, DailyBytes: appplatform.ProxyDailyBytesMax})
	mustExec(t, s.st, `UPDATE app_platform_capability_grants SET policy_json=? WHERE installation_id=? AND capability='network.proxy'`, string(old), installation)
	if view := platformProxyViewOf(t, s, actor, installation); view.Granted {
		t.Fatal("a grant backed only by the older plan was granted")
	}
}

func TestPlatformProxyPlanFormatsAcrossUpgrade(t *testing.T) {
	testPlatformProxyPlanFormatsAcrossUpgrade(t, false)
}
func TestPlatformProxyPlanFormatsAcrossUpgradePostgres(t *testing.T) {
	runOnPostgres(t, testPlatformProxyPlanFormatsAcrossUpgrade)
}

func platformProxyLatest(t *testing.T, s *Server, actor platformActorSession, installation string) (platformProxyPlanRow, error) {
	t.Helper()
	var row platformProxyPlanRow
	var loadErr error
	if err := s.withAppPlatformActorRead(actor, func(e migExec, _ platformActorFacts, _ platformActorControl) error {
		row, loadErr = loadPlatformLatestProxyPlan(e, installation)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return row, loadErr
}

func platformProxyViewOf(t *testing.T, s *Server, actor platformActorSession, installation string) platformProxyView {
	t.Helper()
	var out platformProxyView
	if err := s.withAppPlatformActorRead(actor, func(e migExec, _ platformActorFacts, _ platformActorControl) error {
		var release, epoch string
		var revision int64
		if err := e.queryRow(`SELECT a.release_sha256,i.controller_epoch,i.policy_revision FROM app_platform_installations i JOIN app_platform_activations a ON a.installation_id=i.id AND a.state='active' WHERE i.id=?`, installation).Scan(&release, &epoch, &revision); err != nil {
			return err
		}
		if _, err := loadPlatformProxyGrant(e, installation, revision, release, epoch); err == nil {
			out.Granted = true
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return out
}

// The 1-dev.1 plan keeps its exact encoding, the 1-dev.2 plan its own, and each is read only in its own
// format. Fails closed: anything not canonical is refused.
func TestPlatformProxyPlanCodecAcrossFormats(t *testing.T) {
	policy, _ := appplatform.CanonicalProxyPolicy(appplatform.ProxyPolicy{Hosts: []string{"qt.gtimg.cn"}, Pins: []appplatform.ProxyPin{pinQuotes}, DailyBytes: appplatform.ProxyDailyBytesMax})
	base := platformProxyPlan{
		Format: platformProxyReviewFormatPins, InstallationID: platformFixtureID(1), AppID: "app", PublisherID: "publisher", Generation: 1, InstallationRevision: 1,
		PolicyRevision: 1, TrustRevision: 1, ControllerEpoch: platformFixtureID(2), ReleaseSHA256: platformDigest([]byte("r")), Capability: platformProxyCapability,
		PreviousPolicySHA256: platformDigest([]byte("{}")), Policy: string(policy), PolicySHA256: platformDigest(policy),
		HostsAdded: []string{"qt.gtimg.cn"}, HostsRemoved: []string{}, DailyBytesOld: appplatform.ProxyDailyBytesMax, DailyBytesNew: appplatform.ProxyDailyBytesMax,
		PinsAdded: []appplatform.ProxyPin{pinQuotes}, PinsRemoved: []appplatform.ProxyPin{}, PinsChanged: []platformProxyPinChange{}, RunnerPins: []appplatform.ProxyPin{pinQuotes},
		Runner: transport.NewProxyProfile(), ActorAuthority: platformDigest([]byte("x")),
	}
	raw, err := json.Marshal(base)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := parsePlatformProxyPlan(string(raw)); err != nil {
		t.Fatalf("a valid 1-dev.2 plan: %v", err)
	}
	// The 1-dev.1 encoding is the one of before: no pin member at all.
	v1 := base
	v1.Format, v1.PinsAdded, v1.PinsRemoved, v1.PinsChanged, v1.RunnerPins = platformProxyReviewFormat, nil, nil, nil, nil
	v1.Policy, v1.HostsAdded = `{"format":"portal.proxy-policy/1-dev.1","hosts":["qt.gtimg.cn"],"daily_bytes":2147483648}`, []string{"qt.gtimg.cn"}
	v1.PolicySHA256 = platformDigest([]byte(v1.Policy))
	raw1, err := json.Marshal(v1)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw1, []byte("pins")) {
		t.Fatalf("1-dev.1 encoding has pin members: %s", raw1)
	}
	if _, err := parsePlatformProxyPlan(string(raw1)); err != nil {
		t.Fatalf("a valid 1-dev.1 plan: %v", err)
	}
	mutate := func(edit func(*platformProxyPlan)) string {
		p := base
		edit(&p)
		out, _ := json.Marshal(p)
		return string(out)
	}
	for name, bad := range map[string]string{
		"runner pins differ from the policy's": mutate(func(p *platformProxyPlan) { p.RunnerPins = []appplatform.ProxyPin{pinMoved} }),
		"runner pins empty":                    mutate(func(p *platformProxyPlan) { p.RunnerPins = []appplatform.ProxyPin{} }),
		"pins added null":                      mutate(func(p *platformProxyPlan) { p.PinsAdded = nil }),
		"pins removed null":                    mutate(func(p *platformProxyPlan) { p.PinsRemoved = nil }),
		"pins changed null":                    mutate(func(p *platformProxyPlan) { p.PinsChanged = nil }),
		"runner pins null":                     mutate(func(p *platformProxyPlan) { p.RunnerPins = nil }),
		"denied pin added": mutate(func(p *platformProxyPlan) {
			p.PinsAdded = []appplatform.ProxyPin{{Host: "a.example.com", Address: "169.254.169.254", Port: 443}}
		}),
		"other runner limits": mutate(func(p *platformProxyPlan) { p.Runner.Tunnels = 99 }),
		"policy without pins but runner pins": mutate(func(p *platformProxyPlan) {
			p.Policy = string(mustProxyPolicy(t, []string{"qt.gtimg.cn"}, nil))
			p.PolicySHA256 = platformDigest([]byte(p.Policy))
		}),
		"extra member":                      strings.TrimSuffix(string(raw), "}") + `,"x":1}`,
		"trailing space":                    string(raw) + " ",
		"1-dev.2 member in a 1-dev.1 plan":  strings.Replace(string(raw1), `"runner":`, `"runner_pins":[],"runner":`, 1),
		"1-dev.1 format on 1-dev.2 members": strings.Replace(string(raw), "1-dev.2", "1-dev.1", 1),
		"unknown format":                    strings.Replace(string(raw), "1-dev.2", "1-dev.3", 1),
	} {
		if _, err := parsePlatformProxyPlan(bad); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func mustProxyPolicy(t *testing.T, hosts []string, pins []appplatform.ProxyPin) []byte {
	t.Helper()
	raw, err := appplatform.CanonicalProxyPolicy(appplatform.ProxyPolicy{Hosts: hosts, Pins: pins, DailyBytes: appplatform.ProxyDailyBytesMax})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// A session for a pinned policy carries the policy text and digest, which the runner's validation accepts. A
// runner that answers only the frozen profile (downgraded) gets a session without the proxy, for a pinned
// policy only. A pin the operator has since removed does not stop the session: it carries the reviewed policy
// and the broker refuses that pin. Fails closed for the proxy.
func testPlatformServiceEgressPinnedSession(t *testing.T, pg bool) {
	s, installation := platformServiceEgressProxyFixture(t, pg)
	platformProxySeedPolicy(t, s, installation, string(mustProxyPolicy(t, []string{"qt.gtimg.cn"}, []appplatform.ProxyPin{pinQuotes})), true)
	f := newFakeEgressRunner()
	f.setProxyPins(pinQuotes)
	c, clock := newEgressTestCoordinator(t, s, f)
	c.tick(context.Background())
	session := nextEgressSession(t, f)
	cmd := session.command
	if cmd.Format != "portal.service-egress-session/1-dev.2" || cmd.Proxy == nil || !strings.Contains(strings.Join(cmd.Resources, ","), "network.proxy") {
		t.Fatalf("a pinned grant: %+v", cmd)
	}
	parsed, err := appplatform.ParseProxyPolicy([]byte(cmd.Proxy.Policy))
	if err != nil || len(parsed.Policy.Pins) != 1 || parsed.Policy.Pins[0] != pinQuotes || cmd.Proxy.PolicySHA256 != parsed.SHA256 {
		t.Fatalf("carried policy: %+v %v", cmd.Proxy, err)
	}

	// The operator removed the pin (a runner restart): the session still carries the reviewed policy.
	f.setProxyPins()
	clock.Advance(4*time.Minute + 10*time.Second)
	c.tick(context.Background())
	renewed := nextEgressSession(t, f)
	if renewed.command.Proxy == nil || renewed.command.Proxy.PolicySHA256 != cmd.Proxy.PolicySHA256 {
		t.Fatalf("a pin the operator removed dropped the proxy: %+v", renewed.command.Proxy)
	}

	// The runner is downgraded: PRPY0001 only. The pinned policy is not carried, the other resources are.
	f.mu.Lock()
	f.pinsSupported, f.proxyPins = false, nil
	f.mu.Unlock()
	clock.Advance(4*time.Minute + 10*time.Second)
	c.tick(context.Background())
	downgraded := nextEgressSession(t, f)
	if downgraded.command.Proxy != nil || strings.Contains(strings.Join(downgraded.command.Resources, ","), "network.proxy") ||
		strings.Join(downgraded.command.Resources, ",") != "network.http,notifications.publish" || downgraded.command.Format != "portal.service-egress-session/1-dev.1" {
		t.Fatalf("a downgraded runner was sent pins: %+v", downgraded.command)
	}
}

func TestPlatformServiceEgressPinnedSession(t *testing.T) {
	testPlatformServiceEgressPinnedSession(t, false)
}
func TestPlatformServiceEgressPinnedSessionPostgres(t *testing.T) {
	runOnPostgres(t, testPlatformServiceEgressPinnedSession)
}

// An operator edit to proxy_pins leaves every unpinned grant's plan, session and console state unchanged. The
// grant here has hosts only; the runner's pins change under it (added, changed, emptied, PRPY0001 only).
func TestPlatformServiceEgressUnpinnedGrantIgnoresOperatorPinEdits(t *testing.T) {
	s, installation := platformServiceEgressProxyFixture(t, false)
	platformProxySeedGrant(t, s, installation, []string{"qt.gtimg.cn"}, appplatform.ProxyDailyBytesMax)
	f := newFakeEgressRunner()
	c, clock := newEgressTestCoordinator(t, s, f)
	c.tick(context.Background())
	first := nextEgressSession(t, f)
	wantPolicy, wantDigest := first.command.Proxy.Policy, first.command.Proxy.PolicySHA256
	a, err := s.loadPlatformServiceEgressCurrent(context.Background(), installation, platformServiceEgressLoad{})
	if err != nil {
		t.Fatal(err)
	}
	pin := a.pin()
	a.Close()
	for name, edit := range map[string]func(){
		"added":     func() { f.setProxyPins(pinQuotes) },
		"changed":   func() { f.setProxyPins(pinMoved, pinOther) },
		"emptied":   func() { f.setProxyPins() },
		"old again": func() { f.mu.Lock(); f.pinsSupported, f.proxyPins = false, nil; f.mu.Unlock() },
	} {
		edit()
		clock.Advance(4*time.Minute + 10*time.Second)
		c.tick(context.Background())
		next := nextEgressSession(t, f)
		if next.command.Format != "portal.service-egress-session/1-dev.2" || next.command.Proxy == nil || next.command.Proxy.Policy != wantPolicy || next.command.Proxy.PolicySHA256 != wantDigest {
			t.Fatalf("%s: the unpinned session changed: %+v", name, next.command.Proxy)
		}
		a, err := s.loadPlatformServiceEgressCurrent(context.Background(), installation, platformServiceEgressLoad{})
		if err != nil {
			t.Fatal(err)
		}
		if a.pin() != pin {
			t.Fatalf("%s: the authority pin moved", name)
		}
		a.Close()
	}
}

// Rollup and refusal audit rows for a pinned host carry pinned and pin; an ordinary host's rows do not.
func TestPlatformServiceEgressProxyReportsOfAPinnedHostAreAnnotated(t *testing.T) {
	s, installation := platformServiceEgressProxyFixture(t, false)
	platformProxySeedPolicy(t, s, installation, string(mustProxyPolicy(t, []string{"qt.gtimg.cn"}, []appplatform.ProxyPin{pinQuotes})), true)
	f := newFakeEgressRunner()
	f.setProxyPins(pinQuotes)
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
		{Host: "qt.gtimg.cn", Tunnels: 1, BytesIn: 10, BytesOut: 5, Closes: map[string]int64{"eof": 1}},
		{Host: pinQuotes.Host, Tunnels: 2, BytesIn: 100, BytesOut: 50, Closes: map[string]int64{"eof": 2}},
	}})
	send(broker.ProxyReport{Kind: broker.ProxyReportRefusal, Host: pinQuotes.Host, Code: "address_denied", Count: 3})
	send(broker.ProxyReport{Kind: broker.ProxyReportRefusal, Host: "qt.gtimg.cn", Code: "busy", Count: 1})
	waitEgress(t, func() bool {
		return len(egressAuditRows(t, s, "app_platform.proxy_rollup", installation)) == 2 && len(egressAuditRows(t, s, "app_platform.proxy_refused", installation)) == 2
	})
	rollups := egressAuditRows(t, s, "app_platform.proxy_rollup", installation)
	byHost := map[string]string{}
	for _, row := range rollups {
		byHost[egressAuditField(t, row, "host").(string)] = row
	}
	if egressAuditField(t, byHost[pinQuotes.Host], "pinned") != true || egressAuditField(t, byHost[pinQuotes.Host], "pin") != "127.0.0.1:20443" {
		t.Fatalf("pinned rollup row: %s", byHost[pinQuotes.Host])
	}
	if strings.Contains(byHost["qt.gtimg.cn"], `"pinned"`) || strings.Contains(byHost["qt.gtimg.cn"], `"pin"`) {
		t.Fatalf("ordinary rollup row is annotated: %s", byHost["qt.gtimg.cn"])
	}
	refused := map[string]string{}
	for _, row := range egressAuditRows(t, s, "app_platform.proxy_refused", installation) {
		refused[egressAuditField(t, row, "host").(string)] = row
	}
	if egressAuditField(t, refused[pinQuotes.Host], "pinned") != true || egressAuditField(t, refused[pinQuotes.Host], "pin") != "127.0.0.1:20443" {
		t.Fatalf("pinned refusal row: %s", refused[pinQuotes.Host])
	}
	if strings.Contains(refused["qt.gtimg.cn"], `"pinned"`) {
		t.Fatalf("ordinary refusal row is annotated: %s", refused["qt.gtimg.cn"])
	}
}
