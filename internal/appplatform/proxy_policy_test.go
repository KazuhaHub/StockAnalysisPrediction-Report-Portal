package appplatform

import (
	"strings"
	"testing"
)

func TestProxyHostEntries(t *testing.T) {
	valid := []string{"qt.gtimg.cn", "*.eastmoney.com", "*.push2.eastmoney.com", "xn--fiqs8s.example.cn", "a.b", "*.example.co.uk", "*.sub.example.co.uk"}
	for _, entry := range valid {
		if !ValidProxyHostEntry(entry) {
			t.Errorf("%q must be valid", entry)
		}
	}
	invalid := []string{
		"", "localhost", "com", "*.com", "*.com.cn", "*.github.io", "*.co.uk", "*", "*.", "**.example.com", "*.*.example.com", "a.*.example.com",
		"Example.com", "example.com.", ".example.com", "exa mple.com", "example..com", "-a.example.com", "a-.example.com", "例子.cn",
		"1.2.3.4", "[::1]", "::1", "example.com:443", "example.com/x", "*.1.2.3.4", strings.Repeat("a", 64) + ".example.com",
		strings.Repeat("a.", 130) + "com",
	}
	for _, entry := range invalid {
		if ValidProxyHostEntry(entry) {
			t.Errorf("%q must be refused", entry)
		}
	}
}

func TestProxyPolicyHostsSortedUniqueBounded(t *testing.T) {
	if !ValidProxyHosts([]string{"*.eastmoney.com", "qt.gtimg.cn"}) {
		t.Fatal("sorted list refused")
	}
	for _, list := range [][]string{{"qt.gtimg.cn", "*.eastmoney.com"}, {"a.example.com", "a.example.com"}} {
		if ValidProxyHosts(list) {
			t.Errorf("%v must be refused", list)
		}
	}
	var many []string
	for i := 0; i <= MaxProxyHosts; i++ {
		many = append(many, "h"+strings.Repeat("a", i/26)+string(rune('a'+i%26))+".example.com")
	}
	if ValidProxyHosts(many) {
		t.Fatal("more than 64 entries accepted")
	}
}

func TestProxyPolicyCanonicalRoundTrip(t *testing.T) {
	raw, err := CanonicalProxyPolicy(ProxyPolicy{Hosts: []string{"*.eastmoney.com", "qt.gtimg.cn"}, DailyBytes: ProxyDailyBytesMax})
	if err != nil {
		t.Fatal(err)
	}
	want := `{"format":"portal.proxy-policy/1-dev.1","hosts":["*.eastmoney.com","qt.gtimg.cn"],"daily_bytes":2147483648}`
	if string(raw) != want {
		t.Fatalf("canonical = %s", raw)
	}
	parsed, err := ParseProxyPolicy(raw)
	if err != nil || len(parsed.SHA256) != 64 || parsed.Policy.DailyBytes != ProxyDailyBytesMax {
		t.Fatalf("parse: %v %+v", err, parsed)
	}
	for _, bad := range []string{
		"", "{}", want + " ", strings.Replace(want, "/1-dev.1", "/2", 1),
		`{"format":"portal.proxy-policy/1-dev.1","hosts":[],"daily_bytes":2147483648}`,
		`{"format":"portal.proxy-policy/1-dev.1","hosts":null,"daily_bytes":2147483648}`,
		`{"format":"portal.proxy-policy/1-dev.1","hosts":["qt.gtimg.cn","*.eastmoney.com"],"daily_bytes":2147483648}`,
		`{"format":"portal.proxy-policy/1-dev.1","hosts":["*.com"],"daily_bytes":2147483648}`,
		`{"format":"portal.proxy-policy/1-dev.1","hosts":["qt.gtimg.cn"],"daily_bytes":2147483649}`,
		`{"format":"portal.proxy-policy/1-dev.1","hosts":["qt.gtimg.cn"],"daily_bytes":0}`,
		`{"daily_bytes":2147483648,"format":"portal.proxy-policy/1-dev.1","hosts":["qt.gtimg.cn"]}`,
		`{"format":"portal.proxy-policy/1-dev.1","hosts":["qt.gtimg.cn"],"daily_bytes":2147483648,"x":1}`,
	} {
		if _, err := ParseProxyPolicy([]byte(bad)); err == nil {
			t.Errorf("policy %q must be refused", bad)
		}
	}
	if _, err := NormalizeProxyPolicy(ProxyPolicy{Hosts: []string{"qt.gtimg.cn"}, DailyBytes: 1 << 30}); err != nil {
		t.Fatalf("a lower budget must be accepted: %v", err)
	}
}

// The stored policy must fit the grant row's policy_json column (16384 bytes): a list of 64 of the longest
// entries is refused as input, not left to fail as a database error at consume.
func TestProxyPolicyFitsTheGrantColumn(t *testing.T) {
	label := func(c byte) string { return strings.Repeat(string(c), 63) }
	var longest []string
	for i := 0; i < MaxProxyHosts; i++ {
		longest = append(longest, label('a')+"."+label('b')+"."+label('c')+".x"+string(rune('a'+i/26))+string(rune('a'+i%26))+strings.Repeat("d", 58))
	}
	for _, host := range longest {
		if len(host) != 253 || !ValidProxyHostEntry(host) {
			t.Fatalf("test fixture host is %d bytes, valid=%v", len(host), ValidProxyHostEntry(host))
		}
	}
	if _, err := CanonicalProxyPolicy(ProxyPolicy{Hosts: longest, DailyBytes: ProxyDailyBytesMax}); err == nil {
		t.Fatal("a policy larger than the grant column was accepted")
	}
	raw, err := CanonicalProxyPolicy(ProxyPolicy{Hosts: longest[:60], DailyBytes: ProxyDailyBytesMax})
	if err != nil || len(raw) > 16384 {
		t.Fatalf("60 of the longest entries: %d bytes, %v", len(raw), err)
	}
}

func TestProxyPolicyMatch(t *testing.T) {
	p := ProxyPolicy{Hosts: []string{"*.eastmoney.com", "*.push2.eastmoney.com", "qt.gtimg.cn"}, DailyBytes: ProxyDailyBytesMax}
	for host, want := range map[string]bool{
		"qt.gtimg.cn": true, "push2.eastmoney.com": true, "82.push2.eastmoney.com": true,
		"eastmoney.com": false, "a.b.push2.eastmoney.com": false, "x.qt.gtimg.cn": false, "gtimg.cn": false,
		"QT.gtimg.cn": false, "qt.gtimg.cn.": false, "1.2.3.4": false, "[::1]": false, "": false, "evil-eastmoney.com": false,
		"push2.eastmoney.com.evil.com": false, ".eastmoney.com": false,
	} {
		if got := p.Match(host); got != want {
			t.Errorf("Match(%q) = %v, want %v", host, got, want)
		}
	}
}
