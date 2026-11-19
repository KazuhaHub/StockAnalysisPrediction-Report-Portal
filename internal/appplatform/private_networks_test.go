package appplatform

import (
	"crypto/sha256"
	"encoding/hex"
	"net/netip"
	"strconv"
	"testing"
)

func pn(prefix string, plain bool, ports ...int) PrivateNetwork {
	return PrivateNetwork{Prefix: prefix, Ports: ports, PlainHTTP: plain}
}

func TestPrivateNetworksValidateAccepts(t *testing.T) {
	good := [][]PrivateNetwork{
		nil,
		{pn("10.20.0.0/24", false, 443, 8080)},
		{pn("10.20.0.5/32", true, 80)},
		{pn("192.168.0.0/16", false, 443), pn("172.16.0.0/16", false, 443)},
		{pn("100.64.0.0/16", false, 443)},
		{pn("fd00:1234:5678::/48", false, 443)},
		{pn("fd00:1234:5678::1/128", true, 8080)},
	}
	for i, n := range good {
		if err := ValidatePrivateNetworks(n); err != nil {
			t.Fatalf("case %d: %v", i, err)
		}
	}
}

func TestPrivateNetworksValidateRefuses(t *testing.T) {
	many := make([]PrivateNetwork, 17)
	for i := range many {
		many[i] = pn("10."+strconv.Itoa(i)+".0.0/16", false, 443)
	}
	manyPorts := make([]int, 17)
	for i := range manyPorts {
		manyPorts[i] = 1000 + i
	}
	bad := map[string][]PrivateNetwork{
		"too many entries":       many,
		"not masked":             {pn("10.20.0.5/24", false, 443)},
		"too wide v4":            {pn("10.0.0.0/8", false, 443)},
		"too wide v6":            {pn("fd00::/16", false, 443)},
		"not canonical text":     {pn("010.20.0.0/24", false, 443)},
		"uppercase v6":           {pn("FD00:1234:5678::/48", false, 443)},
		"garbage":                {pn("nope", false, 443)},
		"empty prefix":           {pn("", false, 443)},
		"zone":                   {pn("fe80::%eth0/64", false, 443)},
		"public v4":              {pn("8.8.0.0/16", false, 443)},
		"public v6":              {pn("2606:4700::/48", false, 443)},
		"loopback":               {pn("127.0.0.0/16", false, 443)},
		"link local":             {pn("169.254.0.0/16", false, 443)},
		"link local v6":          {pn("fe80::/64", false, 443)},
		"ula contains denied":    {pn("fd00:ec2::/48", false, 443)},
		"v4 mapped":              {pn("::ffff:10.20.0.0/120", false, 443)},
		"overlap":                {pn("10.20.0.0/24", false, 443), pn("10.20.0.128/25", false, 443)},
		"duplicate":              {pn("10.20.0.0/24", false, 443), pn("10.20.0.0/24", false, 80)},
		"no ports":               {pn("10.20.0.0/24", false)},
		"port zero":              {pn("10.20.0.0/24", false, 0)},
		"port too high":          {pn("10.20.0.0/24", false, 65536)},
		"unsorted ports":         {pn("10.20.0.0/24", false, 8080, 443)},
		"duplicate ports":        {pn("10.20.0.0/24", false, 443, 443)},
		"too many ports":         {pn("10.20.0.0/24", false, manyPorts...)},
		"straddles private edge": {pn("172.0.0.0/16", false, 443)},
	}
	for name, n := range bad {
		if err := ValidatePrivateNetworks(n); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestPrivateNetworksCanonicalAndDigest(t *testing.T) {
	raw, err := CanonicalPrivateNetworks(nil)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != `{"format":"portal.runner.private-networks/1-dev.1","entries":[]}` {
		t.Fatalf("empty: %s", raw)
	}
	n := []PrivateNetwork{pn("192.168.1.0/24", true, 80), pn("10.20.0.0/24", false, 443, 8080)}
	raw, err = CanonicalPrivateNetworks(n)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"format":"portal.runner.private-networks/1-dev.1","entries":[{"prefix":"10.20.0.0/24","ports":[443,8080],"plain_http":false},{"prefix":"192.168.1.0/24","ports":[80],"plain_http":true}]}`
	if string(raw) != want {
		t.Fatalf("canonical: %s", raw)
	}
	if n[0].Prefix != "192.168.1.0/24" {
		t.Fatal("caller mutated")
	}
	sum := sha256.Sum256([]byte(want))
	got, err := PrivateNetworksSHA256(n)
	if err != nil || got != hex.EncodeToString(sum[:]) {
		t.Fatalf("digest %q %v", got, err)
	}
	reordered, _ := PrivateNetworksSHA256([]PrivateNetwork{n[1], n[0]})
	if reordered != got {
		t.Fatal("order changed digest")
	}
	if _, err := CanonicalPrivateNetworks([]PrivateNetwork{pn("8.8.0.0/16", false, 443)}); err == nil {
		t.Fatal("invalid canonicalised")
	}
	empty, _ := PrivateNetworksSHA256(nil)
	if empty == got || len(empty) != 64 {
		t.Fatal("empty digest")
	}
}

func TestAlwaysDenied(t *testing.T) {
	denied := []string{
		"0.0.0.0", "0.1.2.3", "127.0.0.1", "127.255.255.254", "169.254.169.254", "224.0.0.1", "239.255.255.255", "240.0.0.1", "255.255.255.255",
		"::", "::1", "fe80::1", "ff02::1", "::ffff:10.0.0.1", "::ffff:127.0.0.1", "64:ff9b::a14:5", "64:ff9b:1::1", "2002:a14:5::1", "2001:0:4136:e378:8000:63bf:3fff:fdd2", "fd00:ec2::254",
	}
	for _, s := range denied {
		if !AlwaysDenied(netip.MustParseAddr(s)) {
			t.Errorf("%s not denied", s)
		}
	}
	allowed := []string{"10.20.0.5", "172.16.1.1", "192.168.1.1", "100.64.0.1", "fd00::1", "fd00:ec2::253", "8.8.8.8"}
	for _, s := range allowed {
		if AlwaysDenied(netip.MustParseAddr(s)) {
			t.Errorf("%s denied", s)
		}
	}
	if !AlwaysDenied(netip.Addr{}) || !AlwaysDenied(netip.MustParseAddr("fe80::1%eth0")) {
		t.Fatal("invalid or zoned address allowed")
	}
}

func TestUnmap(t *testing.T) {
	if got := Unmap(netip.MustParseAddr("::ffff:10.20.0.5")); got != netip.MustParseAddr("10.20.0.5") {
		t.Fatal(got)
	}
	if got := Unmap(netip.MustParseAddr("fd00::1")); got != netip.MustParseAddr("fd00::1") {
		t.Fatal(got)
	}
	if Unmap(netip.MustParseAddr("fd00::1%eth0")).IsValid() || Unmap(netip.Addr{}).IsValid() {
		t.Fatal("zone or invalid kept")
	}
}

func TestAdmit(t *testing.T) {
	n := []PrivateNetwork{pn("10.20.0.0/24", false, 443, 8080), pn("192.168.1.0/24", true, 80), pn("fd00:1234:5678::/48", false, 443)}
	type probe struct {
		addr   string
		port   uint16
		scheme string
	}
	ok := []probe{
		{"10.20.0.5", 443, "https"}, {"10.20.0.5", 8080, "https"}, {"192.168.1.9", 80, "http"}, {"192.168.1.9", 80, "https"}, {"fd00:1234:5678::7", 443, "https"}, {"::ffff:10.20.0.5", 443, "https"},
	}
	for _, c := range ok {
		if !Admit(n, netip.MustParseAddr(c.addr), c.port, c.scheme) {
			t.Errorf("%v refused", c)
		}
	}
	no := []probe{
		{"10.20.0.5", 80, "https"}, {"10.20.0.5", 8080, "http"}, {"10.20.1.5", 443, "https"}, {"192.168.1.9", 443, "https"},
		{"10.20.0.5", 443, "ftp"}, {"fd00:1234:5679::7", 443, "https"}, {"127.0.0.1", 443, "https"}, {"10.20.0.5", 0, "https"}, {"fe80::1%eth0", 443, "https"},
	}
	for _, c := range no {
		if Admit(n, netip.MustParseAddr(c.addr), c.port, c.scheme) {
			t.Errorf("%v admitted", c)
		}
	}
	if Admit(nil, netip.MustParseAddr("10.20.0.5"), 443, "https") || Admit(n, netip.Addr{}, 443, "https") {
		t.Error("empty allowlist or invalid address admitted")
	}
}
