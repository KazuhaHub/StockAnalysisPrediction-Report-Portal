package appplatform

import (
	"net/netip"
	"slices"
	"strings"
	"testing"
)

const pinHost = "quotes.internal.anchan.kazuhahub.com"

func pin(host, address string, port int) ProxyPin {
	return ProxyPin{Host: host, Address: address, Port: port}
}

func quotesPin() ProxyPin { return pin(pinHost, "127.0.0.1", 20443) }

// A policy without pins must encode byte-identically to 1-dev.1. Fails closed: a policy that carries no pins
// can only be read in its one 1-dev.1 spelling.
func TestPolicyWithoutPinsKeepsTheDev1Encoding(t *testing.T) {
	raw, err := CanonicalProxyPolicy(ProxyPolicy{Hosts: []string{"*.eastmoney.com", "qt.gtimg.cn"}, DailyBytes: ProxyDailyBytesMax})
	if err != nil {
		t.Fatal(err)
	}
	want := `{"format":"portal.proxy-policy/1-dev.1","hosts":["*.eastmoney.com","qt.gtimg.cn"],"daily_bytes":2147483648}`
	if string(raw) != want {
		t.Fatalf("canonical = %s", raw)
	}
	if _, err := ParseProxyPolicy([]byte(strings.Replace(want, "1-dev.1", "1-dev.2", 1))); err == nil {
		t.Fatal("a pin-less policy was accepted under 1-dev.2")
	}
}

func TestPolicyWithPinsIsDev2(t *testing.T) {
	raw, err := CanonicalProxyPolicy(ProxyPolicy{Hosts: []string{"*.eastmoney.com", "qt.gtimg.cn"}, Pins: []ProxyPin{quotesPin()}, DailyBytes: ProxyDailyBytesMax})
	if err != nil {
		t.Fatal(err)
	}
	want := `{"format":"portal.proxy-policy/1-dev.2","hosts":["*.eastmoney.com","qt.gtimg.cn"],"pins":[{"host":"quotes.internal.anchan.kazuhahub.com","address":"127.0.0.1","port":20443}],"daily_bytes":2147483648}`
	if string(raw) != want {
		t.Fatalf("canonical = %s", raw)
	}
	parsed, err := ParseProxyPolicy(raw)
	if err != nil || len(parsed.Policy.Pins) != 1 || parsed.Policy.Pins[0] != quotesPin() || len(parsed.SHA256) != 64 {
		t.Fatalf("parse: %v %+v", err, parsed)
	}
	got, ok := parsed.Policy.Pin(pinHost)
	if !ok || got != quotesPin() {
		t.Fatalf("Pin = %+v %v", got, ok)
	}
	for _, host := range []string{"other.internal.anchan.kazuhahub.com", "QUOTES.internal.anchan.kazuhahub.com", pinHost + ".", "127.0.0.1", ""} {
		if _, ok := parsed.Policy.Pin(host); ok {
			t.Errorf("Pin(%q) matched", host)
		}
	}
}

// A pins-only policy encodes "hosts":[] and no other spelling is read. Fails closed.
func TestPinsOnlyPolicySpelling(t *testing.T) {
	raw, err := CanonicalProxyPolicy(ProxyPolicy{Pins: []ProxyPin{quotesPin()}, DailyBytes: ProxyDailyBytesMax})
	if err != nil {
		t.Fatal(err)
	}
	want := `{"format":"portal.proxy-policy/1-dev.2","hosts":[],"pins":[{"host":"quotes.internal.anchan.kazuhahub.com","address":"127.0.0.1","port":20443}],"daily_bytes":2147483648}`
	if string(raw) != want {
		t.Fatalf("canonical = %s", raw)
	}
	if _, err := ParseProxyPolicy(raw); err != nil {
		t.Fatal(err)
	}
	pins := `"pins":[{"host":"quotes.internal.anchan.kazuhahub.com","address":"127.0.0.1","port":20443}]`
	for name, bad := range map[string]string{
		"hosts null":       `{"format":"portal.proxy-policy/1-dev.2","hosts":null,` + pins + `,"daily_bytes":2147483648}`,
		"hosts omitted":    `{"format":"portal.proxy-policy/1-dev.2",` + pins + `,"daily_bytes":2147483648}`,
		"pins in dev.1":    `{"format":"portal.proxy-policy/1-dev.1","hosts":[],` + pins + `,"daily_bytes":2147483648}`,
		"empty pins":       `{"format":"portal.proxy-policy/1-dev.2","hosts":["qt.gtimg.cn"],"pins":[],"daily_bytes":2147483648}`,
		"absent pins":      `{"format":"portal.proxy-policy/1-dev.2","hosts":["qt.gtimg.cn"],"daily_bytes":2147483648}`,
		"null pins":        `{"format":"portal.proxy-policy/1-dev.2","hosts":["qt.gtimg.cn"],"pins":null,"daily_bytes":2147483648}`,
		"unknown pin key":  `{"format":"portal.proxy-policy/1-dev.2","hosts":[],"pins":[{"host":"quotes.internal.anchan.kazuhahub.com","address":"127.0.0.1","port":20443,"x":1}],"daily_bytes":2147483648}`,
		"reordered keys":   `{"format":"portal.proxy-policy/1-dev.2","hosts":[],"pins":[{"address":"127.0.0.1","host":"quotes.internal.anchan.kazuhahub.com","port":20443}],"daily_bytes":2147483648}`,
		"trailing space":   want + " ",
		"non-canonical 0s": strings.Replace(want, `"port":20443`, `"port":020443`, 1),
	} {
		if _, err := ParseProxyPolicy([]byte(bad)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// Fails closed: every malformed pin is refused when the policy is normalised, so none reaches a runner.
func TestPinFieldRules(t *testing.T) {
	bad := map[string]ProxyPin{
		"wildcard host":    pin("*.internal.anchan.kazuhahub.com", "127.0.0.1", 20443),
		"ip literal host":  pin("127.0.0.1", "127.0.0.1", 20443),
		"single label":     pin("quotes", "127.0.0.1", 20443),
		"upper case":       pin("Quotes.internal.anchan.kazuhahub.com", "127.0.0.1", 20443),
		"trailing dot":     pin(pinHost+".", "127.0.0.1", 20443),
		"port zero":        pin(pinHost, "127.0.0.1", 0),
		"port 65536":       pin(pinHost, "127.0.0.1", 65536),
		"negative port":    pin(pinHost, "127.0.0.1", -1),
		"zoned address":    pin(pinHost, "fe80::1%eth0", 20443),
		"mapped literal":   pin(pinHost, "::ffff:127.0.0.1", 20443),
		"unparsable":       pin(pinHost, "localhost", 20443),
		"non canonical v4": pin(pinHost, "127.000.000.001", 20443),
		"upper-case v6":    pin(pinHost, "FD00::1", 20443),
		"expanded v6":      pin(pinHost, "fd00:0:0:0:0:0:0:1", 20443),
		"empty address":    pin(pinHost, "", 20443),
		"denied address":   pin(pinHost, "169.254.169.254", 20443),
		"cidr":             pin(pinHost, "127.0.0.1/32", 20443),
	}
	for name, p := range bad {
		if ValidProxyPin(p) {
			t.Errorf("%s: pin accepted", name)
		}
		if _, err := NormalizeProxyPolicy(ProxyPolicy{Pins: []ProxyPin{p}, DailyBytes: ProxyDailyBytesMax}); err == nil {
			t.Errorf("%s: policy accepted", name)
		}
	}
	for _, p := range []ProxyPin{quotesPin(), pin(pinHost, "::1", 1), pin(pinHost, "172.17.0.1", 65535), pin(pinHost, "10.0.0.5", 443), pin(pinHost, "100.64.0.1", 8443), pin(pinHost, "fd00::1", 20443), pin(pinHost, "2606:4700::1111", 443)} {
		if !ValidProxyPin(p) {
			t.Errorf("%+v refused", p)
		}
	}
}

func TestPinSetRules(t *testing.T) {
	a, b := pin("a.example.com", "127.0.0.1", 20443), pin("b.example.com", "127.0.0.1", 20444)
	if _, err := NormalizeProxyPolicy(ProxyPolicy{Pins: []ProxyPin{a, b}, DailyBytes: ProxyDailyBytesMax}); err != nil {
		t.Fatal(err)
	}
	for name, p := range map[string]ProxyPolicy{
		"unsorted":         {Pins: []ProxyPin{b, a}},
		"duplicate name":   {Pins: []ProxyPin{a, pin("a.example.com", "127.0.0.1", 20444)}},
		"exact host twin":  {Hosts: []string{"a.example.com"}, Pins: []ProxyPin{a}},
		"nothing at all":   {},
		"hosts only empty": {Hosts: []string{}},
	} {
		p.DailyBytes = ProxyDailyBytesMax
		if _, err := NormalizeProxyPolicy(p); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	// A wildcard that also covers the pinned name is allowed: the pin wins for its exact name.
	if _, err := NormalizeProxyPolicy(ProxyPolicy{Hosts: []string{"*.example.com"}, Pins: []ProxyPin{a}, DailyBytes: ProxyDailyBytesMax}); err != nil {
		t.Fatalf("wildcard cover refused: %v", err)
	}
	// 8 pins fit; a 9th is refused at parse.
	var pins []ProxyPin
	for i := 0; i < MaxProxyPins; i++ {
		pins = append(pins, pin(string(rune('a'+i))+".example.com", "127.0.0.1", 20443))
	}
	if _, err := NormalizeProxyPolicy(ProxyPolicy{Pins: pins, DailyBytes: ProxyDailyBytesMax}); err != nil {
		t.Fatalf("8 pins refused: %v", err)
	}
	pins = append(pins, pin("z.example.com", "127.0.0.1", 20443))
	if _, err := NormalizeProxyPolicy(ProxyPolicy{Pins: pins, DailyBytes: ProxyDailyBytesMax}); err == nil {
		t.Fatal("a 9th pin was accepted")
	}
	// Hosts and pins together are at most 64.
	var hosts []string
	for i := 0; i < MaxProxyHosts-1; i++ {
		hosts = append(hosts, "h"+strings.Repeat("a", i/26)+string(rune('a'+i%26))+".example.com")
	}
	slices.Sort(hosts)
	if _, err := NormalizeProxyPolicy(ProxyPolicy{Hosts: hosts, Pins: []ProxyPin{a}, DailyBytes: ProxyDailyBytesMax}); err != nil {
		t.Fatalf("64 entries refused: %v", err)
	}
	if _, err := NormalizeProxyPolicy(ProxyPolicy{Hosts: hosts, Pins: []ProxyPin{a, b}, DailyBytes: ProxyDailyBytesMax}); err == nil {
		t.Fatal("a 65th entry was accepted")
	}
}

// The policy text stays within the grant column with the pins in it.
func TestPolicyWithPinsFitsTheGrantColumn(t *testing.T) {
	var pins []ProxyPin
	for i := 0; i < MaxProxyPins; i++ {
		pins = append(pins, pin(strings.Repeat("a", 63)+"."+strings.Repeat("b", 63)+"."+strings.Repeat("c", 63)+".x"+string(rune('a'+i))+strings.Repeat("d", 58), "2606:4700:4700::1111", 65535))
	}
	raw, err := CanonicalProxyPolicy(ProxyPolicy{Pins: pins, DailyBytes: ProxyDailyBytesMax})
	if err != nil || len(raw) > MaxProxyPolicyBytes {
		t.Fatalf("%d bytes: %v", len(raw), err)
	}
}

// Fails closed: a denied address is never admitted, at the first and the last address of every denied range.
func TestPinDeniedRefusesEveryDeniedRange(t *testing.T) {
	ranges := []string{
		"0.0.0.0/8", "169.254.0.0/16", "224.0.0.0/4", "240.0.0.0/4", "192.0.0.0/24",
		"::/128", "fe80::/10", "ff00::/8", "::ffff:0:0/96", "64:ff9b::/96", "64:ff9b:1::/48", "2002::/16", "2001::/32",
	}
	for _, r := range ranges {
		p := netip.MustParsePrefix(r)
		for _, a := range []netip.Addr{p.Masked().Addr(), lastAddr(p)} {
			if !PinDenied(a) {
				t.Errorf("%s: %s admitted", r, a)
			}
		}
	}
	for _, s := range []string{"100.100.100.200", "192.0.0.192", "168.63.129.16", "fd00:ec2::254", "fd20:ce::254", "169.254.169.254", "255.255.255.255", "0.0.0.0"} {
		if !PinDenied(netip.MustParseAddr(s)) {
			t.Errorf("%s admitted", s)
		}
	}
	// IPv6 outside ::1, fc00::/7 and 2000::/3 is refused, whatever else is true of it.
	for _, s := range []string{"::2", "100::1", "4000::1", "8000::1", "e000::1", "fb00::1", "fe00::1", "feff::1"} {
		if !PinDenied(netip.MustParseAddr(s)) {
			t.Errorf("%s admitted", s)
		}
	}
	if !PinDenied(netip.Addr{}) || !PinDenied(netip.MustParseAddr("fe80::1%eth0")) || !PinDenied(netip.MustParseAddr("::ffff:127.0.0.1")) {
		t.Error("an invalid, zoned or mapped address was admitted")
	}
	// Ordinary private space, loopback and the neighbours of the denied singletons are admitted.
	for _, s := range []string{"127.0.0.1", "::1", "172.17.0.1", "10.0.0.5", "100.64.0.1", "192.168.1.1", "1.1.1.1", "168.63.129.15", "168.63.129.17", "192.0.1.0", "fc00::1", "fd00::1", "2606:4700::1111", "127.255.255.255", "127.0.0.0"} {
		if PinDenied(netip.MustParseAddr(s)) {
			t.Errorf("%s refused", s)
		}
	}
}

func lastAddr(p netip.Prefix) netip.Addr {
	raw := p.Masked().Addr().AsSlice()
	for i := p.Bits(); i < len(raw)*8; i++ {
		raw[i/8] |= 0x80 >> (i % 8)
	}
	last, _ := netip.AddrFromSlice(raw)
	return last
}

// Fails closed: AlwaysDenied is unchanged, so network.http and ordinary proxy hosts still refuse loopback.
func TestAlwaysDeniedStillDeniesLoopback(t *testing.T) {
	for _, s := range []string{"127.0.0.1", "127.255.255.255", "::1", "100.100.100.200", "169.254.169.254"} {
		if !AlwaysDenied(netip.MustParseAddr(s)) {
			t.Errorf("%s no longer always denied", s)
		}
	}
	// The two additions of PinDenied are not in AlwaysDenied.
	for _, s := range []string{"192.0.0.192", "168.63.129.16"} {
		if AlwaysDenied(netip.MustParseAddr(s)) {
			t.Errorf("%s joined AlwaysDenied", s)
		}
	}
}

// Fails closed: an operator list that is unsorted, duplicated, too long or holds a bad entry is refused.
func TestOperatorPinList(t *testing.T) {
	a, b := pin("a.example.com", "127.0.0.1", 20443), pin("a.example.com", "127.0.0.1", 20444)
	if err := ValidateProxyOperatorPins([]ProxyPin{a, b, pin("b.example.com", "10.0.0.5", 443)}); err != nil {
		t.Fatalf("two entries for one name must be allowed: %v", err)
	}
	if ValidateProxyOperatorPins(nil) != nil || ValidateProxyOperatorPins([]ProxyPin{}) != nil {
		t.Fatal("an empty list is valid")
	}
	for name, list := range map[string][]ProxyPin{
		"unsorted by port":    {b, a},
		"unsorted by address": {pin("a.example.com", "127.0.0.1", 1), pin("a.example.com", "10.0.0.5", 1)},
		"duplicate":           {a, a},
		"denied address":      {pin("a.example.com", "169.254.169.254", 443)},
		"wildcard":            {pin("*.example.com", "127.0.0.1", 443)},
		"port zero":           {pin("a.example.com", "127.0.0.1", 0)},
	} {
		if ValidateProxyOperatorPins(list) == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	var many []ProxyPin
	for i := 0; i < MaxProxyOperatorPins; i++ {
		many = append(many, pin(string(rune('a'+i))+".example.com", "127.0.0.1", 443))
	}
	if ValidateProxyOperatorPins(many) != nil {
		t.Fatal("16 entries refused")
	}
	if ValidateProxyOperatorPins(append(many, pin("z.example.com", "127.0.0.1", 443))) == nil {
		t.Fatal("17 entries accepted")
	}
}

func TestProxyPinPermitted(t *testing.T) {
	list := []ProxyPin{quotesPin()}
	if !ProxyPinPermitted(list, quotesPin()) {
		t.Fatal("an exact entry was refused")
	}
	for name, p := range map[string]ProxyPin{
		"other port":    pin(pinHost, "127.0.0.1", 20444),
		"other address": pin(pinHost, "127.0.0.2", 20443),
		"other host":    pin("other.internal.anchan.kazuhahub.com", "127.0.0.1", 20443),
	} {
		if ProxyPinPermitted(list, p) {
			t.Errorf("%s: permitted", name)
		}
	}
	if ProxyPinPermitted(nil, quotesPin()) {
		t.Fatal("an empty allowlist permitted a pin")
	}
	// A listed entry in the denied set is never permitted, even if a corrupt list holds it.
	bad := pin(pinHost, "169.254.169.254", 443)
	if ProxyPinPermitted([]ProxyPin{bad}, bad) {
		t.Fatal("a denied address was permitted")
	}
}
