package appplatform

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func exampleHTTPPolicy() HTTPPolicy {
	return HTTPPolicy{Origin: "https://api.example.com", Paths: []string{"/v2", "/v1"}, Methods: []string{"POST", "GET"}, RequestBytes: 65536, ResponseBytes: 1048576, Deadline: 1500 * time.Millisecond, Concurrency: 2}
}

func TestHTTPPolicyCanonicalBinding(t *testing.T) {
	p := exampleHTTPPolicy()
	raw, err := CanonicalHTTPPolicy(p)
	if err != nil {
		t.Fatal(err)
	}
	expected := `{"format":"portal.http-policy/1-dev.1","origin":"https://api.example.com","paths":["/v1","/v2"],"methods":["GET","POST"],"request_bytes":65536,"response_bytes":1048576,"deadline_ms":1500,"concurrency":2}`
	if string(raw) != expected {
		t.Fatalf("unexpected policy: %s", raw)
	}
	if p.Paths[0] != "/v2" || p.Methods[0] != "POST" {
		t.Fatal("mutated caller")
	}
	parsed, err := ParseHTTPPolicy(raw)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.SHA256 != "fce3c2a1102d5e27734e8bd8d5a6524f95613b742f8c6e87d0bd25af939bb0a6" {
		t.Fatalf("digest %s", parsed.SHA256)
	}
	again, _ := CanonicalHTTPPolicy(parsed.Policy)
	if !bytes.Equal(raw, again) {
		t.Fatal("policy changed")
	}
	normalized, err := NormalizeHTTPPolicy(p)
	if err != nil {
		t.Fatal(err)
	}
	p.Paths[0] = "/private"
	p.Methods[0] = "DELETE"
	if normalized.Paths[0] != "/v1" || normalized.Methods[0] != "GET" {
		t.Fatal("shared policy slices")
	}
	changed := parsed.Policy
	changed.RequestBytes--
	other, _ := CanonicalHTTPPolicy(changed)
	changedParsed, _ := ParseHTTPPolicy(other)
	if changedParsed.SHA256 == parsed.SHA256 {
		t.Fatal("policy change retained digest")
	}
}

func TestHTTPPolicyRejectsAmbiguousStoredBytes(t *testing.T) {
	raw, _ := CanonicalHTTPPolicy(exampleHTTPPolicy())
	s := string(raw)
	cases := map[string]string{
		"space": " " + s, "newline": s + "\n", "trailing": s + "{}", "unknown": strings.Replace(s, `"concurrency":2`, `"concurrency":2,"extra":1`, 1),
		"duplicate": strings.Replace(s, `"concurrency":2`, `"concurrency":2,"concurrency":2`, 1),
		"alias":     strings.Replace(s, `"origin"`, `"Origin"`, 1), "escaped": strings.Replace(s, `"origin"`, `"\u006frigin"`, 1),
		"float": strings.Replace(s, `"deadline_ms":1500`, `"deadline_ms":1500.0`, 1), "exponent": strings.Replace(s, `"deadline_ms":1500`, `"deadline_ms":15e2`, 1),
		"overflow":  strings.Replace(s, `"deadline_ms":1500`, `"deadline_ms":9223372036854775808`, 1),
		"unordered": strings.Replace(s, `["GET","POST"]`, `["POST","GET"]`, 1), "null": strings.Replace(s, `["/v1","/v2"]`, `null`, 1),
		"version": strings.Replace(s, "1-dev.1", "1-dev.2", 1), "missing": strings.Replace(s, `,"concurrency":2`, "", 1),
		"oversized": strings.Repeat(" ", 8193), "utf8": string([]byte{255}),
	}
	for name, input := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseHTTPPolicy([]byte(input)); err == nil {
				t.Fatal("ambiguous policy accepted")
			}
		})
	}
}

func TestHTTPPolicyTargetAndLimits(t *testing.T) {
	p := exampleHTTPPolicy()
	for _, target := range []string{"https://api.example.com/v1", "https://api.example.com/v2/items?q=a%20b"} {
		if ValidateHTTPPolicyTarget(p, "GET", target) != nil {
			t.Fatal(target)
		}
	}
	for _, target := range []string{"https://api.example.com/v10", "https://api.example.com/v1/..", "https://api.example.com/v1/%2e", "https://api.example.com/v1//x", "https://api.example.com/v1#", "https://api.example.com:443/v1", "https://api.example.com.evil/v1", "https://user@api.example.com/v1", "http://api.example.com/v1", "https://api.example.com/v1\\x", "https://api.example.com/v1/中文", "https://api.example.com/v1/" + strings.Repeat("x", 1024), "https://api.example.com/v1?q=" + strings.Repeat("x", 4096)} {
		if ValidateHTTPPolicyTarget(p, "GET", target) == nil {
			t.Fatal("target accepted", target)
		}
	}
	if ValidateHTTPPolicyTarget(p, "DELETE", "https://api.example.com/v1") == nil {
		t.Fatal("ungranted method")
	}
	p.Origin = "https://api.example.com:8443"
	if ValidateHTTPPolicyTarget(p, "POST", "https://api.example.com:8443/v1") != nil {
		t.Fatal("explicit port refused")
	}
}

func TestHTTPPolicyInvalidDeclarations(t *testing.T) {
	mutations := map[string]func(*HTTPPolicy){
		"http": func(p *HTTPPolicy) { p.Origin = "http://api.example.com" }, "ip": func(p *HTTPPolicy) { p.Origin = "https://8.8.8.8" }, "upper": func(p *HTTPPolicy) { p.Origin = "https://API.example.com" }, "userinfo": func(p *HTTPPolicy) { p.Origin = "https://user@api.example.com" }, "query": func(p *HTTPPolicy) { p.Origin += "?" }, "path": func(p *HTTPPolicy) { p.Origin += "/" }, "dot": func(p *HTTPPolicy) { p.Origin += "." }, "numeric": func(p *HTTPPolicy) { p.Origin = "https://0x7f.0.0.1" }, "port": func(p *HTTPPolicy) { p.Origin += " :8443" },
		"paths": func(p *HTTPPolicy) { p.Paths = nil }, "duplicate": func(p *HTTPPolicy) { p.Paths = []string{"/v1", "/v1"} }, "traversal": func(p *HTTPPolicy) { p.Paths = []string{"/v1/../admin"} }, "trailing": func(p *HTTPPolicy) { p.Paths = []string{"/v1/"} }, "connect": func(p *HTTPPolicy) { p.Methods = []string{"CONNECT"} }, "duplicate method": func(p *HTTPPolicy) { p.Methods = []string{"GET", "GET"} }, "zero request": func(p *HTTPPolicy) { p.RequestBytes = 0 }, "large request": func(p *HTTPPolicy) { p.RequestBytes = 65537 }, "large response": func(p *HTTPPolicy) { p.ResponseBytes = 1048577 }, "zero deadline": func(p *HTTPPolicy) { p.Deadline = 0 }, "fractional ms": func(p *HTTPPolicy) { p.Deadline = time.Millisecond + time.Nanosecond }, "large deadline": func(p *HTTPPolicy) { p.Deadline = 16 * time.Second }, "concurrency": func(p *HTTPPolicy) { p.Concurrency = 5 },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			p := exampleHTTPPolicy()
			mutate(&p)
			if _, err := CanonicalHTTPPolicy(p); err == nil {
				t.Fatal("invalid policy accepted")
			}
			if ValidateHTTPPolicyTarget(p, "GET", "https://api.example.com/v1") == nil {
				t.Fatal("invalid policy used")
			}
		})
	}
}

func TestHTTPPolicyMarshalUsesCanonicalContract(t *testing.T) {
	p := exampleHTTPPolicy()
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	expected, _ := CanonicalHTTPPolicy(p)
	if !bytes.Equal(raw, expected) {
		t.Fatal("different JSON representation")
	}
	var decoded HTTPPolicy
	if json.Unmarshal(raw, &decoded) != nil {
		t.Fatal("canonical decode failed")
	}
	bad := bytes.Replace(raw, []byte(`"origin"`), []byte(`"Origin"`), 1)
	if json.Unmarshal(bad, &decoded) == nil {
		t.Fatal("noncanonical nested object accepted")
	}
}

func TestHTTPPolicyRecordBudget(t *testing.T) {
	p := exampleHTTPPolicy()
	p.Paths = nil
	for i := 0; i < 16; i++ {
		p.Paths = append(p.Paths, "/"+string(rune('a'+i))+strings.Repeat("x", 510))
	}
	if _, err := NormalizeHTTPPolicy(p); err == nil {
		t.Fatal("policy exceeds stored byte budget")
	}
	if _, err := CanonicalHTTPPolicy(p); err == nil {
		t.Fatal("oversized encoding")
	}
	raw, _ := CanonicalHTTPPolicy(exampleHTTPPolicy())
	for _, bad := range [][]byte{nil, []byte("null"), []byte("[]"), append([]byte(`{"x":`+strings.Repeat("[", 25)), []byte(strings.Repeat("]", 25)+"}")...)} {
		parsed, err := ParseHTTPPolicy(bad)
		if err == nil || parsed.SHA256 != "" || parsed.Policy.Origin != "" {
			t.Fatal("invalid input retained authority")
		}
	}
	parsed, err := ParseHTTPPolicy(raw)
	if err != nil || parsed.SHA256 == "" {
		t.Fatal("valid input refused")
	}
}

func FuzzHTTPPolicyRoundTrip(f *testing.F) {
	raw, _ := CanonicalHTTPPolicy(exampleHTTPPolicy())
	f.Add(raw)
	f.Add([]byte(`{}`))
	f.Add([]byte("null"))
	f.Fuzz(func(t *testing.T, raw []byte) {
		parsed, err := ParseHTTPPolicy(raw)
		if err != nil {
			return
		}
		canonical, err := CanonicalHTTPPolicy(parsed.Policy)
		if err != nil || !bytes.Equal(raw, canonical) {
			t.Fatal("accepted noncanonical bytes")
		}
		again, err := ParseHTTPPolicy(canonical)
		if err != nil || again.SHA256 != parsed.SHA256 {
			t.Fatal("unstable binding")
		}
	})
}
