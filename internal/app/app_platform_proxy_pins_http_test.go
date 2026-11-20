package app

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/transport"
)

type platformProxyPinsViewBody struct {
	Granted bool `json:"granted"`
	Policy  *struct {
		Hosts []string `json:"hosts"`
		Pins  []struct {
			Host      string `json:"host"`
			Address   string `json:"address"`
			Port      int    `json:"port"`
			Permitted bool   `json:"permitted"`
		} `json:"pins"`
	} `json:"policy"`
	Runner struct {
		Available     bool `json:"available"`
		PinsSupported bool `json:"pins_supported"`
		Pins          []struct {
			Host    string `json:"host"`
			Address string `json:"address"`
			Port    int    `json:"port"`
		} `json:"pins"`
	} `json:"runner"`
	PolicyRevision       string `json:"policy_revision"`
	PreviousPolicySHA256 string `json:"previous_policy_sha256"`
}

func (p *platformProxyHTTP) pinsView(t *testing.T) platformProxyPinsViewBody {
	t.Helper()
	w := p.do("GET", p.root(), "", "", nil)
	if w.Code != 200 {
		t.Fatal("view", w.Code, w.Body.String())
	}
	var v platformProxyPinsViewBody
	if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func pinsBody(v platformProxyPinsViewBody, hosts, pins string) string {
	return `{"policyRevision":"` + v.PolicyRevision + `","previousPolicySha256":"` + v.PreviousPolicySHA256 + `","hosts":` + hosts + `,"pins":` + pins + `,"dailyBytes":1073741824}`
}

const quotesPinJSON = `{"host":"quotes.internal.anchan.kazuhahub.com","address":"127.0.0.1","port":20443}`

// The console's view of the runner's pins, the plan with pins, its diff, the consume, and the "permitted by the
// runner" mark that follows an operator edit.
func testPlatformProxyPinsHTTP(t *testing.T, pg bool) {
	p := platformProxyHTTPEnv(t, pg)
	// A runner that predates pins: the view says so, and a plan with pins is 409.
	v := p.pinsView(t)
	if !v.Runner.Available || v.Runner.PinsSupported || len(v.Runner.Pins) != 0 {
		t.Fatalf("runner before: %+v", v.Runner)
	}
	if w := p.do("POST", p.root()+"/plan", pinsBody(v, `["qt.gtimg.cn"]`, `[`+quotesPinJSON+`]`), "application/json", nil); w.Code != 409 || !strings.Contains(w.Body.String(), "app_platform_runner_unsupported") {
		t.Fatal("pins against a runner without pin support", w.Code, w.Body.String())
	}
	proxyRunnerOf(p.s).servePins(pinOther, pinQuotes)
	v = p.pinsView(t)
	if !v.Runner.PinsSupported || len(v.Runner.Pins) != 2 || v.Runner.Pins[0].Host != pinOther.Host || v.Runner.Pins[1].Port != 20443 {
		t.Fatalf("runner after: %+v", v.Runner)
	}
	planPath := p.root() + "/plan"
	for name, body := range map[string]string{
		"pins null":          pinsBody(v, `["qt.gtimg.cn"]`, `null`),
		"pins an object":     pinsBody(v, `["qt.gtimg.cn"]`, quotesPinJSON),
		"unknown pin key":    pinsBody(v, `["qt.gtimg.cn"]`, `[{"host":"quotes.internal.anchan.kazuhahub.com","address":"127.0.0.1","port":20443,"x":1}]`),
		"missing port":       pinsBody(v, `["qt.gtimg.cn"]`, `[{"host":"quotes.internal.anchan.kazuhahub.com","address":"127.0.0.1"}]`),
		"string port":        pinsBody(v, `["qt.gtimg.cn"]`, `[{"host":"quotes.internal.anchan.kazuhahub.com","address":"127.0.0.1","port":"20443"}]`),
		"fractional port":    pinsBody(v, `["qt.gtimg.cn"]`, `[{"host":"quotes.internal.anchan.kazuhahub.com","address":"127.0.0.1","port":20443.5}]`),
		"port out of range":  pinsBody(v, `["qt.gtimg.cn"]`, `[{"host":"quotes.internal.anchan.kazuhahub.com","address":"127.0.0.1","port":4294987739}]`),
		"negative port":      pinsBody(v, `["qt.gtimg.cn"]`, `[{"host":"quotes.internal.anchan.kazuhahub.com","address":"127.0.0.1","port":-1}]`),
		"duplicate member":   pinsBody(v, `["qt.gtimg.cn"]`, `[{"host":"a.example.com","host":"b.example.com","address":"127.0.0.1","port":443}]`),
		"duplicate pins key": strings.Replace(pinsBody(v, `["qt.gtimg.cn"]`, `[`+quotesPinJSON+`]`), `"pins":[`, `"pins":[],"pins":[`, 1),
		"wildcard pin":       pinsBody(v, `["qt.gtimg.cn"]`, `[{"host":"*.internal.anchan.kazuhahub.com","address":"127.0.0.1","port":20443}]`),
		"denied address":     pinsBody(v, `["qt.gtimg.cn"]`, `[{"host":"quotes.internal.anchan.kazuhahub.com","address":"169.254.169.254","port":443}]`),
		"mapped address":     pinsBody(v, `["qt.gtimg.cn"]`, `[{"host":"quotes.internal.anchan.kazuhahub.com","address":"::ffff:127.0.0.1","port":443}]`),
	} {
		if w := p.do("POST", planPath, body, "application/json", nil); w.Code != 400 {
			t.Errorf("%s: status %d %s", name, w.Code, w.Body.String())
		}
	}
	// The plan, with its diff, then the consume.
	w := p.do("POST", planPath, pinsBody(v, `["qt.gtimg.cn"]`, `[`+quotesPinJSON+`]`), "application/json", nil)
	if w.Code != 200 {
		t.Fatal("plan", w.Code, w.Body.String())
	}
	var plan struct {
		PlanID     string `json:"plan_id"`
		PlanSHA256 string `json:"plan_sha256"`
		Diff       struct {
			Pins struct {
				Added []struct {
					Host    string `json:"host"`
					Address string `json:"address"`
					Port    int    `json:"port"`
				} `json:"added"`
				Removed []any `json:"removed"`
				Changed []any `json:"changed"`
			} `json:"pins"`
		} `json:"diff"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &plan); err != nil || plan.PlanID == "" || len(plan.Diff.Pins.Added) != 1 || plan.Diff.Pins.Added[0].Port != 20443 ||
		plan.Diff.Pins.Removed == nil || plan.Diff.Pins.Changed == nil {
		t.Fatal("plan response", w.Body.String(), err)
	}
	if w := p.do("POST", "/api/admin/app-platform/network-proxy-reviews/"+plan.PlanID, `{"sha256":"`+plan.PlanSHA256+`"}`, "application/json", nil); w.Code != 200 {
		t.Fatal("consume", w.Code, w.Body.String())
	}
	after := p.pinsView(t)
	if !after.Granted || after.Policy == nil || len(after.Policy.Pins) != 1 || after.Policy.Pins[0].Host != pinQuotes.Host || !after.Policy.Pins[0].Permitted {
		t.Fatalf("view after: %+v", after.Policy)
	}
	// The operator removes the entry: the pin is still reviewed and is shown as not permitted by the runner.
	proxyRunnerOf(p.s).servePins(pinOther)
	if v := p.pinsView(t); !v.Granted || len(v.Policy.Pins) != 1 || v.Policy.Pins[0].Permitted {
		t.Fatalf("view after the operator's edit: %+v", v.Policy)
	}
	// A runner that predates pins, or that cannot be asked: the pin is not shown as permitted either.
	proxyRunnerOf(p.s).mu.Lock()
	proxyRunnerOf(p.s).pinsSupported = false
	proxyRunnerOf(p.s).mu.Unlock()
	if v := p.pinsView(t); v.Policy.Pins[0].Permitted || v.Runner.PinsSupported {
		t.Fatalf("view with an old runner: %+v", v)
	}
	proxyRunnerOf(p.s).fail(transport.ErrProxyUnsupported)
	if v := p.pinsView(t); v.Runner.Available || v.Runner.Pins == nil || len(v.Runner.Pins) != 0 || v.Policy.Pins[0].Permitted {
		t.Fatalf("view with no runner: %+v", v)
	}
	// An existing client that sends no pins still works, and a plan without them removes the pin.
	proxyRunnerOf(p.s).mu.Lock()
	proxyRunnerOf(p.s).err = nil
	proxyRunnerOf(p.s).mu.Unlock()
	current := p.pinsView(t)
	withoutKey := `{"policyRevision":"` + current.PolicyRevision + `","previousPolicySha256":"` + current.PreviousPolicySHA256 + `","hosts":["qt.gtimg.cn"],"dailyBytes":1073741824}`
	w = p.do("POST", planPath, withoutKey, "application/json", nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"removed":[{"host":"quotes.internal.anchan.kazuhahub.com"`) {
		t.Fatal("plan without the pins key", w.Code, w.Body.String())
	}
}

func TestPlatformProxyPinsHTTP(t *testing.T)         { testPlatformProxyPinsHTTP(t, false) }
func TestPlatformProxyPinsHTTPPostgres(t *testing.T) { runOnPostgres(t, testPlatformProxyPinsHTTP) }

// The view of the policy keeps hosts-only policies as before: an empty pin list, not a missing member.
func TestPlatformProxyViewOfAHostsOnlyGrantHasNoPins(t *testing.T) {
	p := platformProxyHTTPEnv(t, false)
	platformProxySeedGrant(t, p.s, p.installation, []string{"qt.gtimg.cn"}, appplatform.ProxyDailyBytesMax)
	w := p.do("GET", p.root(), "", "", nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"pins":[]`) {
		t.Fatal(w.Code, w.Body.String())
	}
}
