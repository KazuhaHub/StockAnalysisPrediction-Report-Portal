package control

import (
	"strings"
	"testing"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

func pinnedProxySession(t *testing.T) ServiceEgressSessionCommand {
	t.Helper()
	command := proxySession(t)
	raw, err := appplatform.CanonicalProxyPolicy(appplatform.ProxyPolicy{
		Hosts: []string{"*.eastmoney.com"}, Pins: []appplatform.ProxyPin{{Host: "quotes.internal.anchan.kazuhahub.com", Address: "127.0.0.1", Port: 20443}},
		DailyBytes: appplatform.ProxyDailyBytesMax})
	if err != nil {
		t.Fatal(err)
	}
	block := NewServiceEgressProxy(raw)
	command.Proxy = &block
	return command
}

// A session for a pinned policy keeps the 1-dev.2 session format, carries the policy text and its digest, and
// the runner's verification accepts it: the pins travel inside the signed policy, with no new field.
func TestServiceEgressSessionCarriesAPinnedPolicy(t *testing.T) {
	_, key, authority, now := serviceEgressFixture(t)
	command := pinnedProxySession(t)
	if command.Format != ServiceEgressSessionProxyFormat || !strings.Contains(command.Proxy.Policy, `"pins":[{"host":"quotes.internal.anchan.kazuhahub.com","address":"127.0.0.1","port":20443}]`) {
		t.Fatalf("%s %s", command.Format, command.Proxy.Policy)
	}
	raw, signature, err := SignServiceEgressSession(command, key, authority, now)
	if err != nil {
		t.Fatal(err)
	}
	verified, err := VerifyServiceEgressSession(raw, signature, authority, now)
	if err != nil {
		t.Fatal(err)
	}
	got := verified.Command()
	parsed, err := appplatform.ParseProxyPolicy([]byte(got.Proxy.Policy))
	if err != nil || parsed.SHA256 != got.Proxy.PolicySHA256 || len(parsed.Policy.Pins) != 1 {
		t.Fatalf("the carried policy lost its pin: %v %+v", err, parsed.Policy)
	}
}

// Fails closed: a tampered pin or a digest of another policy is refused at the runner.
func TestServiceEgressPinnedPolicyIsRefusedWhenTampered(t *testing.T) {
	_, key, authority, now := serviceEgressFixture(t)
	for name, edit := range map[string]func(*ServiceEgressSessionCommand){
		"pin moved, digest kept": func(c *ServiceEgressSessionCommand) {
			c.Proxy.Policy = strings.Replace(c.Proxy.Policy, "20443", "443", 1)
		},
		"denied pin address": func(c *ServiceEgressSessionCommand) {
			c.Proxy.Policy = strings.Replace(c.Proxy.Policy, "127.0.0.1", "169.254.169.254", 1)
			block := NewServiceEgressProxy([]byte(c.Proxy.Policy))
			c.Proxy = &block
		},
		"pins in the 1-dev.1 policy": func(c *ServiceEgressSessionCommand) {
			c.Proxy.Policy = strings.Replace(c.Proxy.Policy, "1-dev.2", "1-dev.1", 1)
			block := NewServiceEgressProxy([]byte(c.Proxy.Policy))
			c.Proxy = &block
		},
	} {
		t.Run(name, func(t *testing.T) {
			command := pinnedProxySession(t)
			edit(&command)
			if _, _, err := SignServiceEgressSession(command, key, authority, now); err == nil {
				t.Fatal("a tampered pinned policy was signed")
			}
		})
	}
}
