package appplatform

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/netip"
	"strings"
	"testing"
	"time"
)

func v2Policy() HTTPPolicyV2 {
	return HTTPPolicyV2{
		Targets: []HTTPTarget{
			{ID: "lan-api", Origin: "http://10.20.0.5:8080", Paths: []string{"/health"}, Methods: []string{"GET"}, Network: "private"},
			{ID: "dify-prod", Origin: "https://dify.example.com", Paths: []string{"/v1/info", "/v1"}, Methods: []string{"POST", "GET"}, Network: "public"},
		},
		RequestBytes: 65536, ResponseBytes: 1048576, Deadline: 15 * time.Second, Concurrency: 4,
	}
}

const v2Canonical = `{"format":"portal.http-policy/2-dev.1","targets":[{"id":"dify-prod","origin":"https://dify.example.com","paths":["/v1","/v1/info"],"methods":["GET","POST"],"network":"public"},{"id":"lan-api","origin":"http://10.20.0.5:8080","paths":["/health"],"methods":["GET"],"network":"private"}],"request_bytes":65536,"response_bytes":1048576,"deadline_ms":15000,"concurrency":4}`

func TestHTTPPolicyV2CanonicalRoundTrip(t *testing.T) {
	p := v2Policy()
	raw, err := CanonicalHTTPPolicyV2(p)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != v2Canonical {
		t.Fatalf("canonical:\n%s", raw)
	}
	if p.Targets[0].ID != "lan-api" || p.Targets[1].Paths[0] != "/v1/info" {
		t.Fatal("caller mutated")
	}
	parsed, err := ParseHTTPPolicyAny(raw)
	if err != nil || parsed.Version != 2 {
		t.Fatal(parsed.Version, err)
	}
	sum := sha256.Sum256(raw)
	if parsed.SHA256 != hex.EncodeToString(sum[:]) {
		t.Fatal("digest is not the raw bytes digest")
	}
	again, err := CanonicalHTTPPolicyV2(parsed.Policy)
	if err != nil || !bytes.Equal(again, raw) {
		t.Fatal("round trip changed bytes")
	}
	if !parsed.Policy.HasPrivate() {
		t.Fatal("private target not seen")
	}
	public := v2Policy()
	public.Targets = public.Targets[1:]
	if public.HasPrivate() {
		t.Fatal("public only reported private")
	}
}

func TestHTTPPolicyV1ReadAsDefaultTarget(t *testing.T) {
	raw, _ := CanonicalHTTPPolicy(exampleHTTPPolicy())
	parsed, err := ParseHTTPPolicyAny(raw)
	if err != nil || parsed.Version != 1 {
		t.Fatal(parsed.Version, err)
	}
	v1, _ := ParseHTTPPolicy(raw)
	if parsed.SHA256 != v1.SHA256 {
		t.Fatal("v1 digest changed")
	}
	if len(parsed.Policy.Targets) != 1 || parsed.Policy.Targets[0].ID != "default" || parsed.Policy.Targets[0].Network != "public" || parsed.Policy.HasPrivate() {
		t.Fatalf("%+v", parsed.Policy.Targets)
	}
	// The v1 default target hashes like its v2 spelling.
	target := HTTPTarget{ID: "default", Origin: "https://api.example.com", Paths: []string{"/v1", "/v2"}, Methods: []string{"GET", "POST"}, Network: "public"}
	want, err := HTTPTargetSHA256(target)
	if err != nil {
		t.Fatal(err)
	}
	eff, err := parsed.Policy.Select("GET", "https://api.example.com/v1/x")
	if err != nil || eff.SHA256 != want || eff.ID != "default" {
		t.Fatal(eff, err)
	}
}

func TestParseHTTPPolicyStillRefusesV2(t *testing.T) {
	if _, err := ParseHTTPPolicy([]byte(v2Canonical)); err == nil {
		t.Fatal("v1 parser accepted v2")
	}
	if _, err := ParseHTTPStreamPolicy([]byte(v2Canonical)); err == nil {
		t.Fatal("stream parser accepted v2")
	}
	if _, err := ParseHTTPPolicyAny(nil); err == nil {
		t.Fatal("empty accepted")
	}
	if _, err := ParseHTTPPolicyAny([]byte(`{"format":"portal.http-policy/3-dev.1"}`)); err == nil {
		t.Fatal("unknown format accepted")
	}
}

func TestHTTPPolicyV2RefusesAmbiguousBytes(t *testing.T) {
	cases := map[string]string{
		"whitespace":        strings.Replace(v2Canonical, `"targets":`, `"targets": `, 1),
		"unsorted":          strings.Replace(strings.Replace(v2Canonical, "dify-prod", "zzz", 1), `"lan-api"`, `"aaa"`, 1),
		"unknown key":       strings.Replace(v2Canonical, `"concurrency":4}`, `"concurrency":4,"extra":1}`, 1),
		"duplicate key":     strings.Replace(v2Canonical, `"concurrency":4}`, `"concurrency":4,"concurrency":4}`, 1),
		"target extra key":  strings.Replace(v2Canonical, `"network":"public"`, `"network":"public","tls":false`, 1),
		"zero override":     strings.Replace(v2Canonical, `"network":"public"}`, `"network":"public","request_bytes":0}`, 1),
		"trailing":          v2Canonical + " ",
		"empty targets":     `{"format":"portal.http-policy/2-dev.1","targets":[],"request_bytes":65536,"response_bytes":1048576,"deadline_ms":15000,"concurrency":4}`,
		"null targets":      `{"format":"portal.http-policy/2-dev.1","targets":null,"request_bytes":65536,"response_bytes":1048576,"deadline_ms":15000,"concurrency":4}`,
		"deadline zero":     strings.Replace(v2Canonical, `"deadline_ms":15000`, `"deadline_ms":0`, 1),
		"deadline too long": strings.Replace(v2Canonical, `"deadline_ms":15000`, `"deadline_ms":15001`, 1),
		"concurrency 5":     strings.Replace(v2Canonical, `"concurrency":4`, `"concurrency":5`, 1),
		"request too big":   strings.Replace(v2Canonical, `"request_bytes":65536`, `"request_bytes":65537`, 1),
		"response too big":  strings.Replace(v2Canonical, `"response_bytes":1048576`, `"response_bytes":1048577`, 1),
		"v1 format":         strings.Replace(v2Canonical, `/2-dev.1`, `/1-dev.1`, 1),
		"not an object":     `[]`,
		"invalid utf8":      "{\"format\":\"\xff\"}",
	}
	for name, raw := range cases {
		if _, err := ParseHTTPPolicyAny([]byte(raw)); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

func v2WithTargets(targets ...HTTPTarget) HTTPPolicyV2 {
	p := v2Policy()
	p.Targets = targets
	return p
}

func pub(id, origin string) HTTPTarget {
	return HTTPTarget{ID: id, Origin: origin, Paths: []string{"/"}, Methods: []string{"GET"}, Network: "public"}
}

func priv(id, origin string) HTTPTarget {
	return HTTPTarget{ID: id, Origin: origin, Paths: []string{"/"}, Methods: []string{"GET"}, Network: "private"}
}

func TestHTTPPolicyV2TargetRules(t *testing.T) {
	good := []HTTPTarget{
		pub("a", "https://api.example.com"),
		pub("a", "https://api.example.com:8443"),
		priv("a", "https://nas.lan.example.com"),
		priv("a", "http://nas.lan.example.com:8080"),
		priv("a", "https://10.20.0.5"),
		priv("a", "http://10.20.0.5"),
		priv("a", "http://10.20.0.5:8044"),
		priv("a", "https://[fd00:1234::5]"),
		priv("a", "https://[fd00:1234::5]:8443"),
		priv("a", "http://[fd00:1234::5]:8080"),
		priv("a-b-1", "https://172.16.0.1"),
		priv("a", "https://100.64.0.1"),
	}
	for _, g := range good {
		if _, err := CanonicalHTTPTarget(g); err != nil {
			t.Errorf("%+v: %v", g, err)
		}
	}
	bad := map[string]HTTPTarget{
		"public http":              pub("a", "http://api.example.com"),
		"public ip literal":        pub("a", "https://93.184.216.34"),
		"public explicit 443":      pub("a", "https://api.example.com:443"),
		"public ipv6":              pub("a", "https://[2606:4700::1]"),
		"private explicit 443":     priv("a", "https://10.20.0.5:443"),
		"private http explicit 80": priv("a", "http://10.20.0.5:80"),
		"private loopback":         priv("a", "https://127.0.0.1"),
		"private link local":       priv("a", "https://169.254.169.254"),
		"private unspecified":      priv("a", "https://0.0.0.0"),
		"private multicast":        priv("a", "https://224.0.0.1"),
		"private 0/8":              priv("a", "https://0.1.2.3"),
		"private 240/4":            priv("a", "https://240.0.0.1"),
		"private v6 loopback":      priv("a", "https://[::1]"),
		"private v6 link local":    priv("a", "https://[fe80::1]"),
		"private v6 unspecified":   priv("a", "https://[::]"),
		"private v6 multicast":     priv("a", "https://[ff02::1]"),
		"private v4 mapped":        priv("a", "https://[::ffff:10.0.0.1]"),
		"private nat64":            priv("a", "https://[64:ff9b::a00:1]"),
		"private 6to4":             priv("a", "https://[2002:a00:1::1]"),
		"private teredo":           priv("a", "https://[2001:0:4136:e378:8000:63bf:3fff:fdd2]"),
		"private ec2 metadata":     priv("a", "https://[fd00:ec2::254]"),
		"zone":                     priv("a", "https://[fe80::1%25eth0]"),
		"non canonical v4":         priv("a", "https://010.20.0.5"),
		"non canonical v6":         priv("a", "https://[FD00:1234::5]"),
		"expanded v6":              priv("a", "https://[fd00:1234:0:0:0:0:0:5]"),
		"unbracketed v6":           priv("a", "https://fd00::5"),
		"wildcard":                 pub("a", "https://*.example.com"),
		"wildcard private":         priv("a", "https://*.lan.example.com"),
		"user info":                priv("a", "https://u:p@10.20.0.5"),
		"path":                     priv("a", "https://10.20.0.5/x"),
		"trailing slash":           priv("a", "https://10.20.0.5/"),
		"query":                    priv("a", "https://10.20.0.5?x=1"),
		"fragment":                 priv("a", "https://10.20.0.5#x"),
		"ftp":                      priv("a", "ftp://10.20.0.5"),
		"uppercase host":           pub("a", "https://API.example.com"),
		"single label":             priv("a", "https://nas"),
		"port zero":                priv("a", "https://10.20.0.5:0"),
		"port padded":              priv("a", "https://10.20.0.5:08080"),
		"port too high":            priv("a", "https://10.20.0.5:65536"),
		"empty port":               priv("a", "https://10.20.0.5:"),
		"bad network":              {ID: "a", Origin: "https://x.example.com", Paths: []string{"/"}, Methods: []string{"GET"}, Network: "lan"},
		"empty network":            {ID: "a", Origin: "https://x.example.com", Paths: []string{"/"}, Methods: []string{"GET"}},
		"id uppercase":             pub("A", "https://api.example.com"),
		"id digit first":           pub("1a", "https://api.example.com"),
		"id empty":                 pub("", "https://api.example.com"),
		"id too long":              pub("a"+strings.Repeat("b", 48), "https://api.example.com"),
		"id underscore":            pub("a_b", "https://api.example.com"),
		"no paths":                 {ID: "a", Origin: "https://x.example.com", Methods: []string{"GET"}, Network: "public"},
		"no methods":               {ID: "a", Origin: "https://x.example.com", Paths: []string{"/"}, Network: "public"},
		"bad method":               {ID: "a", Origin: "https://x.example.com", Paths: []string{"/"}, Methods: []string{"CONNECT"}, Network: "public"},
		"dup path":                 {ID: "a", Origin: "https://x.example.com", Paths: []string{"/a", "/a"}, Methods: []string{"GET"}, Network: "public"},
		"bad path":                 {ID: "a", Origin: "https://x.example.com", Paths: []string{"/a/../b"}, Methods: []string{"GET"}, Network: "public"},
		"path trailing slash":      {ID: "a", Origin: "https://x.example.com", Paths: []string{"/a/"}, Methods: []string{"GET"}, Network: "public"},
		"negative override":        {ID: "a", Origin: "https://x.example.com", Paths: []string{"/"}, Methods: []string{"GET"}, Network: "public", RequestBytes: -1},
		"override over max":        {ID: "a", Origin: "https://x.example.com", Paths: []string{"/"}, Methods: []string{"GET"}, Network: "public", ResponseBytes: MaxHTTPResponseBytes + 1},
		"deadline sub ms":          {ID: "a", Origin: "https://x.example.com", Paths: []string{"/"}, Methods: []string{"GET"}, Network: "public", Deadline: time.Microsecond},
	}
	for name, b := range bad {
		if _, err := CanonicalHTTPTarget(b); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

func TestHTTPPolicyV2PolicyRules(t *testing.T) {
	if _, err := CanonicalHTTPPolicyV2(v2Policy()); err != nil {
		t.Fatal(err)
	}
	if _, err := CanonicalHTTPPolicyV2(v2WithTargets()); err == nil {
		t.Error("zero targets accepted")
	}
	if _, err := CanonicalHTTPPolicyV2(v2WithTargets(pub("a", "https://a.example.com"), pub("a", "https://b.example.com"))); err == nil {
		t.Error("duplicate id accepted")
	}
	if _, err := CanonicalHTTPPolicyV2(v2WithTargets(pub("a", "https://a.example.com"), pub("b", "https://a.example.com"))); err == nil {
		t.Error("duplicate origin accepted")
	}
	// Same host, different scheme or port, is a different origin.
	if _, err := CanonicalHTTPPolicyV2(v2WithTargets(priv("a", "https://nas.example.com"), priv("b", "http://nas.example.com"), priv("c", "https://nas.example.com:8443"))); err != nil {
		t.Error(err)
	}
	var many []HTTPTarget
	for i := 0; i < 128; i++ {
		many = append(many, pub(fmt.Sprintf("t%03d", i), fmt.Sprintf("https://h%03d.example.com", i)))
	}
	big := v2WithTargets(many...)
	if _, err := CanonicalHTTPPolicyV2(big); err != nil {
		t.Errorf("128 targets: %v", err)
	}
	many = append(many, pub("t128", "https://h128.example.com"))
	if _, err := CanonicalHTTPPolicyV2(v2WithTargets(many...)); err == nil {
		t.Error("129 targets accepted")
	}
	// The 16 KiB bound is on canonical bytes.
	var fat []HTTPTarget
	for i := 0; i < 128; i++ {
		var paths []string
		for j := 0; j < 16; j++ {
			paths = append(paths, fmt.Sprintf("/%s%d", strings.Repeat("p", 40), j))
		}
		t := pub(fmt.Sprintf("t%03d", i), fmt.Sprintf("https://h%03d.example.com", i))
		t.Paths = paths
		fat = append(fat, t)
	}
	if _, err := CanonicalHTTPPolicyV2(v2WithTargets(fat...)); err == nil {
		t.Error("oversize canonical policy accepted")
	}
	for name, mutate := range map[string]func(*HTTPPolicyV2){
		"request zero":     func(p *HTTPPolicyV2) { p.RequestBytes = 0 },
		"request max+1":    func(p *HTTPPolicyV2) { p.RequestBytes = MaxHTTPRequestBytes + 1 },
		"response zero":    func(p *HTTPPolicyV2) { p.ResponseBytes = 0 },
		"deadline zero":    func(p *HTTPPolicyV2) { p.Deadline = 0 },
		"deadline max+1ms": func(p *HTTPPolicyV2) { p.Deadline = 15001 * time.Millisecond },
		"deadline sub ms":  func(p *HTTPPolicyV2) { p.Deadline = 1500*time.Millisecond + time.Microsecond },
		"concurrency 0":    func(p *HTTPPolicyV2) { p.Concurrency = 0 },
		"concurrency 5":    func(p *HTTPPolicyV2) { p.Concurrency = 5 },
	} {
		p := v2Policy()
		mutate(&p)
		if _, err := CanonicalHTTPPolicyV2(p); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

func TestHTTPPolicyV2TargetOverridesOnlyNarrow(t *testing.T) {
	p := v2Policy()
	p.Targets[1].RequestBytes = 1024
	p.Targets[1].ResponseBytes = 2048
	p.Targets[1].Deadline = 2500 * time.Millisecond
	raw, err := CanonicalHTTPPolicyV2(p)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"network":"public","request_bytes":1024,"response_bytes":2048,"deadline_ms":2500}`) {
		t.Fatalf("override placement: %s", raw)
	}
	parsed, err := ParseHTTPPolicyAny(raw)
	if err != nil {
		t.Fatal(err)
	}
	eff, err := parsed.Policy.Select("GET", "https://dify.example.com/v1/info")
	if err != nil || eff.RequestBytes != 1024 || eff.ResponseBytes != 2048 || eff.Deadline != 2500*time.Millisecond || eff.Concurrency != 4 {
		t.Fatal(eff, err)
	}
	other, err := parsed.Policy.Select("GET", "http://10.20.0.5:8080/health")
	if err != nil || other.RequestBytes != 65536 || other.ResponseBytes != 1048576 || other.Deadline != 15*time.Second {
		t.Fatal(other, err)
	}
	for name, mutate := range map[string]func(*HTTPTarget){
		"request equal":   func(x *HTTPTarget) { x.RequestBytes = 65536 },
		"response equal":  func(x *HTTPTarget) { x.ResponseBytes = 1048576 },
		"deadline equal":  func(x *HTTPTarget) { x.Deadline = 15 * time.Second },
		"request higher":  func(x *HTTPTarget) { x.RequestBytes = 70000 },
		"deadline longer": func(x *HTTPTarget) { x.Deadline = 20 * time.Second },
	} {
		q := v2Policy()
		mutate(&q.Targets[1])
		if _, err := CanonicalHTTPPolicyV2(q); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	// A narrowed target is a different target digest.
	plain, _ := HTTPTargetSHA256(v2Policy().Targets[1])
	narrow, _ := HTTPTargetSHA256(p.Targets[1])
	if plain == narrow {
		t.Fatal("override left the digest unchanged")
	}
}

func TestHTTPTargetDigestIsOverCanonicalBytes(t *testing.T) {
	target := v2Policy().Targets[1]
	raw, err := CanonicalHTTPTarget(target)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"id":"dify-prod","origin":"https://dify.example.com","paths":["/v1","/v1/info"],"methods":["GET","POST"],"network":"public"}`
	if string(raw) != want {
		t.Fatalf("%s", raw)
	}
	sum := sha256.Sum256(raw)
	got, err := HTTPTargetSHA256(target)
	if err != nil || got != hex.EncodeToString(sum[:]) {
		t.Fatal(got, err)
	}
	// The policy embeds exactly these bytes.
	policy, _ := CanonicalHTTPPolicyV2(v2Policy())
	if !bytes.Contains(policy, raw) {
		t.Fatal("policy does not embed canonical target bytes")
	}
	// Renaming changes the digest (a rename is a remove plus an add).
	renamed := target
	renamed.ID = "dify-prod2"
	other, _ := HTTPTargetSHA256(renamed)
	if other == got {
		t.Fatal("rename kept the digest")
	}
}

func TestHTTPPolicyV2Select(t *testing.T) {
	parsed, err := ParseHTTPPolicyAny([]byte(v2Canonical))
	if err != nil {
		t.Fatal(err)
	}
	p := parsed.Policy
	eff, err := p.Select("POST", "https://dify.example.com/v1/info?x=1")
	if err != nil {
		t.Fatal(err)
	}
	want, _ := HTTPTargetSHA256(p.Targets[0])
	if eff.ID != "dify-prod" || eff.Private || eff.Scheme != "https" || eff.Host != "dify.example.com" || eff.Port != 443 || eff.Literal.IsValid() || eff.SHA256 != want || eff.Origin != "https://dify.example.com" {
		t.Fatalf("%+v", eff)
	}
	lan, err := p.Select("GET", "http://10.20.0.5:8080/health")
	if err != nil {
		t.Fatal(err)
	}
	if lan.ID != "lan-api" || !lan.Private || lan.Scheme != "http" || lan.Port != 8080 || lan.Literal != netip.MustParseAddr("10.20.0.5") || lan.Host != "10.20.0.5" {
		t.Fatalf("%+v", lan)
	}
	v6 := v2WithTargets(priv("six", "https://[fd00:1234::5]:8443"))
	got, err := v6.Select("GET", "https://[fd00:1234::5]:8443/x")
	if err != nil || got.Host != "fd00:1234::5" || got.Port != 8443 || got.Literal != netip.MustParseAddr("fd00:1234::5") {
		t.Fatal(got, err)
	}
	denied := map[string][2]string{
		"unknown host":         {"GET", "https://other.example.com/v1"},
		"wrong scheme":         {"GET", "http://dify.example.com/v1/info"},
		"https to http origin": {"GET", "https://10.20.0.5:8080/health"},
		"wrong port":           {"GET", "http://10.20.0.5:8081/health"},
		"explicit default":     {"GET", "https://dify.example.com:443/v1/info"},
		"method":               {"DELETE", "https://dify.example.com/v1/info"},
		"method on lan":        {"POST", "http://10.20.0.5:8080/health"},
		"path outside":         {"GET", "https://dify.example.com/v2"},
		"path prefix glue":     {"GET", "https://dify.example.com/v1x"},
		"lan path":             {"GET", "http://10.20.0.5:8080/admin"},
		"user info":            {"GET", "https://u:p@dify.example.com/v1"},
		"fragment":             {"GET", "https://dify.example.com/v1#x"},
		"empty fragment":       {"GET", "https://dify.example.com/v1#"},
		"opaque":               {"GET", "https:dify.example.com/v1"},
		"raw path":             {"GET", "https://dify.example.com/v1%2Finfo"},
		"dot dot":              {"GET", "https://dify.example.com/v1/../v2"},
		"double slash":         {"GET", "https://dify.example.com//v1"},
		"ftp":                  {"GET", "ftp://dify.example.com/v1"},
		"uppercase host":       {"GET", "https://DIFY.example.com/v1/info"},
		"empty":                {"GET", ""},
		"too long":             {"GET", "https://dify.example.com/v1/" + strings.Repeat("a", 4096)},
		"empty path":           {"GET", "https://dify.example.com"},
		"trailing dot host":    {"GET", "https://dify.example.com./v1/info"},
	}
	for name, c := range denied {
		if _, err := p.Select(c[0], c[1]); err == nil {
			t.Errorf("%s selected", name)
		}
	}
}

func TestHTTPPolicyV2SelectAgreesWithV1(t *testing.T) {
	v1 := exampleHTTPPolicy()
	raw, _ := CanonicalHTTPPolicy(v1)
	parsed, _ := ParseHTTPPolicyAny(raw)
	for _, c := range [][2]string{
		{"GET", "https://api.example.com/v1"}, {"GET", "https://api.example.com/v2/a/b?x=1"}, {"PUT", "https://api.example.com/v1"},
		{"GET", "https://api.example.com/v3"}, {"GET", "http://api.example.com/v1"}, {"GET", "https://api.example.com:443/v1"},
		{"GET", "https://other.example.com/v1"}, {"GET", "https://api.example.com/v1#"}, {"POST", "https://api.example.com/v2"},
	} {
		_, e2 := parsed.Policy.Select(c[0], c[1])
		e1 := ValidateHTTPPolicyTarget(v1, c[0], c[1])
		if (e1 == nil) != (e2 == nil) {
			t.Errorf("%v: v1=%v v2=%v", c, e1, e2)
		}
	}
	eff, _ := parsed.Policy.Select("GET", "https://api.example.com/v1")
	if eff.RequestBytes != v1.RequestBytes || eff.ResponseBytes != v1.ResponseBytes || eff.Deadline != v1.Deadline || eff.Concurrency != v1.Concurrency {
		t.Fatalf("%+v", eff)
	}
}
