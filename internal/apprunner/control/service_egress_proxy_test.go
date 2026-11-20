package control

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

func proxyFixture(t *testing.T, hosts []string, daily int64) *ServiceEgressProxy {
	t.Helper()
	raw, err := appplatform.CanonicalProxyPolicy(appplatform.ProxyPolicy{Hosts: hosts, DailyBytes: daily})
	if err != nil {
		t.Fatal(err)
	}
	proxy := NewServiceEgressProxy(raw)
	return &proxy
}

func proxySession(t *testing.T) ServiceEgressSessionCommand {
	command, _, _, _ := serviceEgressFixture(t)
	command.Format = ServiceEgressSessionProxyFormat
	command.Resources = []string{"network.http", "network.proxy", "notifications.publish"}
	command.Proxy = proxyFixture(t, []string{"*.eastmoney.com", "qt.gtimg.cn"}, appplatform.ProxyDailyBytesMax)
	return command
}

func TestServiceEgressProxySessionSignedAndBound(t *testing.T) {
	_, key, authority, now := serviceEgressFixture(t)
	command := proxySession(t)
	raw, signature, err := SignServiceEgressSession(command, key, authority, now)
	if err != nil {
		t.Fatal(err)
	}
	verified, err := VerifyServiceEgressSession(raw, signature, authority, now)
	if err != nil {
		t.Fatal(err)
	}
	got := verified.Command()
	if got.Proxy == nil || got.Format != ServiceEgressSessionProxyFormat || got.Proxy.PolicySHA256 != command.Proxy.PolicySHA256 {
		t.Fatalf("proxy block was not carried: %+v", got.Proxy)
	}
	parsed, err := appplatform.ParseProxyPolicy([]byte(got.Proxy.Policy))
	if err != nil || parsed.SHA256 != got.Proxy.PolicySHA256 {
		t.Fatal("the carried policy does not match its digest")
	}
	// The accessor returns a copy.
	got.Proxy.Tunnels = "99"
	if again := verified.Command(); again.Proxy.Tunnels != strconv.Itoa(appplatform.ProxyTunnelsPerInstallation) {
		t.Fatal("verified command aliased its caller")
	}
	// A proxy-only session is valid.
	only := command
	only.Resources = []string{"network.proxy"}
	if _, _, err := SignServiceEgressSession(only, key, authority, now); err != nil {
		t.Fatalf("proxy-only session refused: %v", err)
	}
	// A lower reviewed budget is valid and is carried.
	lower := command
	lower.Proxy = proxyFixture(t, []string{"qt.gtimg.cn"}, 1<<30)
	if _, _, err := SignServiceEgressSession(lower, key, authority, now); err != nil {
		t.Fatalf("lower budget refused: %v", err)
	}
}

func TestServiceEgressProxySessionRefusals(t *testing.T) {
	_, key, authority, now := serviceEgressFixture(t)
	for name, edit := range map[string]func(*ServiceEgressSessionCommand){
		"proxy block on the first format": func(c *ServiceEgressSessionCommand) { c.Format = ServiceEgressSessionFormat },
		"proxy resource on the first format": func(c *ServiceEgressSessionCommand) {
			c.Format, c.Proxy = ServiceEgressSessionFormat, nil
		},
		"second format without a block": func(c *ServiceEgressSessionCommand) { c.Proxy = nil },
		"block without the resource":    func(c *ServiceEgressSessionCommand) { c.Resources = []string{"network.http", "notifications.publish"} },
		"resource without the block":    func(c *ServiceEgressSessionCommand) { c.Proxy = nil; c.Format = ServiceEgressSessionFormat },
		"digest of another policy":      func(c *ServiceEgressSessionCommand) { c.Proxy.PolicySHA256 = strings.Repeat("a", 64) },
		"policy not canonical":          func(c *ServiceEgressSessionCommand) { c.Proxy.Policy = " " + c.Proxy.Policy },
		"policy with a public suffix": func(c *ServiceEgressSessionCommand) {
			c.Proxy.Policy = strings.Replace(c.Proxy.Policy, "*.eastmoney.com", "*.com", 1)
		},
		"empty policy": func(c *ServiceEgressSessionCommand) {
			c.Proxy.Policy, c.Proxy.PolicySHA256 = "", strings.Repeat("a", 64)
		},
		"tunnels":                          func(c *ServiceEgressSessionCommand) { c.Proxy.Tunnels = "17" },
		"new per minute":                   func(c *ServiceEgressSessionCommand) { c.Proxy.NewPerMinute = "301" },
		"burst":                            func(c *ServiceEgressSessionCommand) { c.Proxy.Burst = "51" },
		"idle":                             func(c *ServiceEgressSessionCommand) { c.Proxy.IdleSeconds = "61" },
		"lifetime":                         func(c *ServiceEgressSessionCommand) { c.Proxy.LifetimeSeconds = "601" },
		"runner tunnels":                   func(c *ServiceEgressSessionCommand) { c.Proxy.RunnerTunnels = "65" },
		"connect timeout":                  func(c *ServiceEgressSessionCommand) { c.Proxy.ConnectSeconds = "11" },
		"first record timeout":             func(c *ServiceEgressSessionCommand) { c.Proxy.FirstRecordSeconds = "11" },
		"bandwidth":                        func(c *ServiceEgressSessionCommand) { c.Proxy.Mbit = "51" },
		"budget differing from the policy": func(c *ServiceEgressSessionCommand) { c.Proxy.DailyBytes = "1048576" },
		"budget above the platform's": func(c *ServiceEgressSessionCommand) {
			c.Proxy.DailyBytes = strconv.FormatInt(appplatform.ProxyDailyBytesMax+1, 10)
		},
		"leading zero": func(c *ServiceEgressSessionCommand) { c.Proxy.Tunnels = "016" },
		"empty limit":  func(c *ServiceEgressSessionCommand) { c.Proxy.Mbit = "" },
	} {
		t.Run(name, func(t *testing.T) {
			c := proxySession(t)
			edit(&c)
			if raw, sig, err := SignServiceEgressSession(c, key, authority, now); err == nil || len(raw) != 0 || len(sig) != 0 {
				t.Fatal("invalid session was signed")
			}
		})
	}
}

// The first format is unchanged: a session without a proxy block encodes exactly as it did
// before the proxy existed, so a runner that predates the proxy still opens it.
func TestServiceEgressFirstFormatEncodingIsUnchanged(t *testing.T) {
	command, key, authority, now := serviceEgressFixture(t)
	raw, _, err := SignServiceEgressSession(command, key, authority, now)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "proxy") {
		t.Fatal("a session without a proxy carries proxy bytes")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	if _, ok := fields["proxy"]; ok {
		t.Fatal("proxy key present")
	}
}
