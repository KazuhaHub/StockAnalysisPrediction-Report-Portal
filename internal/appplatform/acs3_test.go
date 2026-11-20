package appplatform

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"testing"
	"time"
)

func acs3SHAOf(body string) string {
	sum := sha256.Sum256([]byte(body))
	return hex.EncodeToString(sum[:])
}

func acs3Find(t *testing.T, headers []SignedHeader, name string) string {
	t.Helper()
	for _, h := range headers {
		if h.Name == name {
			return string(h.Value)
		}
	}
	t.Fatalf("header %s missing from %v", name, names(headers))
	return ""
}

func names(headers []SignedHeader) []string {
	var out []string
	for _, h := range headers {
		out = append(out, h.Name)
	}
	return out
}

// The example of Alibaba Cloud's "request body and signature mechanism (V3)" page: a
// RunInstances call with an empty body. The documented HashedCanonicalRequest and
// Signature are reproduced exactly.
func TestACS3OfficialVector(t *testing.T) {
	const (
		documentedSignature = "06563a9e1b43f5dfe96b81484da74bceab24a1d853912eee15083a6f0f3283c0"
		documentedSigned    = "host;x-acs-action;x-acs-content-sha256;x-acs-date;x-acs-signature-nonce;x-acs-version"
	)
	headers, err := ACS3Sign(ACS3Input{
		Method:      "POST",
		Target:      "https://ecs.cn-shanghai.aliyuncs.com/?ImageId=win2019_1809_x64_dtc_zh-cn_40G_alibase_20230811.vhd&RegionId=cn-shanghai",
		BodySHA256:  acs3SHAOf(""),
		Action:      "RunInstances",
		Version:     "2014-05-26",
		Date:        time.Date(2023, 10, 26, 10, 22, 32, 0, time.UTC),
		Nonce:       "3156853299f313e23d1673dc12e1703d",
		AccessKeyID: "YourAccessKeyId",
	}, []byte("YourAccessKeySecret"))
	if err != nil {
		t.Fatal(err)
	}
	want := "ACS3-HMAC-SHA256 Credential=YourAccessKeyId,SignedHeaders=" + documentedSigned + ",Signature=" + documentedSignature
	if got := acs3Find(t, headers, "authorization"); got != want {
		t.Fatalf("authorization\n got %s\nwant %s", got, want)
	}
	if got := acs3Find(t, headers, "host"); got != "ecs.cn-shanghai.aliyuncs.com" {
		t.Fatalf("host = %s", got)
	}
}

// statusSignAlibaba is the Status app's implementation (probes.go signAlibaba and
// alibabaAuthorization), copied verbatim in behaviour for the one fixed call it signs.
func statusSignAlibaba(r *http.Request, id, secret, token string, now time.Time, nonce string) {
	r.Header.Set("x-acs-action", "QueryAccountBalance")
	r.Header.Set("x-acs-version", "2017-12-14")
	r.Header.Set("x-acs-date", now.UTC().Format("2006-01-02T15:04:05Z"))
	r.Header.Set("x-acs-signature-nonce", nonce)
	if token != "" {
		r.Header.Set("x-acs-security-token", token)
	}
	hash := acs3SHAOf("")
	r.Header.Set("x-acs-content-sha256", hash)
	headers := map[string]string{"host": r.URL.Host}
	for name, values := range r.Header {
		lower := strings.ToLower(name)
		if strings.HasPrefix(lower, "x-acs-") || lower == "content-type" {
			headers[lower] = strings.TrimSpace(values[0])
		}
	}
	keys := make([]string, 0, len(headers))
	for k := range headers {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	canonicalHeaders := ""
	for _, key := range keys {
		canonicalHeaders += key + ":" + headers[key] + "\n"
	}
	signed := strings.Join(keys, ";")
	canonical := r.Method + "\n/\n" + r.URL.RawQuery + "\n" + canonicalHeaders + "\n" + signed + "\n" + hash
	sum := sha256.Sum256([]byte(canonical))
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte("ACS3-HMAC-SHA256\n" + hex.EncodeToString(sum[:])))
	r.Header.Set("Authorization", "ACS3-HMAC-SHA256 Credential="+id+",SignedHeaders="+signed+",Signature="+hex.EncodeToString(mac.Sum(nil)))
}

func TestACS3MatchesTheStatusImplementation(t *testing.T) {
	now := time.Date(2026, 11, 17, 8, 30, 15, 0, time.UTC)
	const nonce = "00112233445566778899aabbccddeeff"
	for _, token := range []string{"", "STS.tokenValue+/=0123456789"} {
		for _, host := range []string{"business.aliyuncs.com", "business.ap-southeast-1.aliyuncs.com"} {
			req, err := http.NewRequest("POST", "https://"+host+"/", nil)
			if err != nil {
				t.Fatal(err)
			}
			statusSignAlibaba(req, "LTAI5tFixtureId", "fixture-secret-0123456789", token, now, nonce)
			headers, err := ACS3Sign(ACS3Input{
				Method: "POST", Target: "https://" + host + "/", BodySHA256: acs3SHAOf(""),
				Action: "QueryAccountBalance", Version: "2017-12-14", Date: now, Nonce: nonce,
				AccessKeyID: "LTAI5tFixtureId", SecurityToken: []byte(token),
			}, []byte("fixture-secret-0123456789"))
			if err != nil {
				t.Fatal(err)
			}
			if got, want := acs3Find(t, headers, "authorization"), req.Header.Get("Authorization"); got != want {
				t.Fatalf("token %q host %s\n got %s\nwant %s", token, host, got, want)
			}
			for _, name := range []string{"x-acs-action", "x-acs-version", "x-acs-date", "x-acs-signature-nonce", "x-acs-content-sha256"} {
				if got := acs3Find(t, headers, name); got != req.Header.Get(name) {
					t.Fatalf("%s = %s, want %s", name, got, req.Header.Get(name))
				}
			}
			if token != "" && acs3Find(t, headers, "x-acs-security-token") != token {
				t.Fatal("security token not carried")
			}
		}
	}
}

// independentACS3 is a second, separately written implementation: it builds the
// canonical request line by line from a url.Values, with its own escaper.
func independentACS3(method, target, contentType, body, action, version, date, nonce, id, secret, token string) string {
	u, _ := url.Parse(target)
	escape := func(s string) string {
		var out []byte
		for _, b := range []byte(s) {
			switch {
			case b >= '0' && b <= '9', b >= 'a' && b <= 'z', b >= 'A' && b <= 'Z', b == '-', b == '_', b == '.', b == '~':
				out = append(out, b)
			default:
				out = append(out, []byte("%"+strings.ToUpper(hex.EncodeToString([]byte{b})))...)
			}
		}
		return string(out)
	}
	var query []string
	values := u.Query()
	keys := make([]string, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		vs := values[k]
		sort.Strings(vs)
		for _, v := range vs {
			query = append(query, escape(k)+"="+escape(v))
		}
	}
	bodyHash := sha256.Sum256([]byte(body))
	bodyHex := hex.EncodeToString(bodyHash[:])
	lines := map[string]string{
		"host": u.Host, "x-acs-action": action, "x-acs-version": version, "x-acs-date": date,
		"x-acs-signature-nonce": nonce, "x-acs-content-sha256": bodyHex,
	}
	if token != "" {
		lines["x-acs-security-token"] = token
	}
	if contentType != "" {
		lines["content-type"] = contentType
	}
	var order []string
	for k := range lines {
		order = append(order, k)
	}
	sort.Strings(order)
	var canonical bytes.Buffer
	canonical.WriteString(method + "\n")
	path := u.Path
	if path == "" {
		path = "/"
	}
	canonical.WriteString(path + "\n")
	canonical.WriteString(strings.Join(query, "&") + "\n")
	for _, k := range order {
		canonical.WriteString(k + ":" + lines[k] + "\n")
	}
	canonical.WriteString("\n")
	canonical.WriteString(strings.Join(order, ";") + "\n")
	canonical.WriteString(bodyHex)
	digest := sha256.Sum256(canonical.Bytes())
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte("ACS3-HMAC-SHA256\n"))
	mac.Write([]byte(hex.EncodeToString(digest[:])))
	return "ACS3-HMAC-SHA256 Credential=" + id + ",SignedHeaders=" + strings.Join(order, ";") + ",Signature=" + hex.EncodeToString(mac.Sum(nil))
}

func TestACS3MatchesAnIndependentImplementation(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	const nonce = "fedcba98765432100123456789abcdef"
	cases := []struct {
		name, method, target, contentType, body, token string
	}{
		{"fixed billing call", "POST", "https://business.aliyuncs.com/", "", "", ""},
		{"with a token", "POST", "https://business.aliyuncs.com/", "", "", "STS.abc+def/ghi=="},
		{"query sorted and escaped", "POST", "https://ecs.cn-hangzhou.aliyuncs.com/?Name=a%20b~c&Region=cn-hangzhou&Tag.1.Key=k%2F1", "", "", ""},
		{"json body", "POST", "https://example.aliyuncs.com/v1/items", "application/json", `{"a":1,"b":"two"}`, ""},
		{"get with path", "GET", "https://example.aliyuncs.com/v2/a.b_c-d~e/list?PageSize=10&PageNumber=1", "", "", ""},
		{"repeated names", "POST", "https://example.aliyuncs.com/?Id=2&Id=1&Id=%2A", "", "", ""},
		{"empty value", "POST", "https://example.aliyuncs.com/?Flag=&A=1", "", "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			u, _ := url.Parse(c.target)
			canonical, err := ACS3CanonicalQuery(u.RawQuery)
			if err != nil {
				t.Fatal(err)
			}
			target := strings.Replace(c.target, "?"+u.RawQuery, "?"+canonical, 1)
			if u.RawQuery == "" {
				target = c.target
			}
			headers, err := ACS3Sign(ACS3Input{
				Method: c.method, Target: target, ContentType: c.contentType, BodySHA256: acs3SHAOf(c.body),
				Action: "QueryAccountBalance", Version: "2017-12-14", Date: now, Nonce: nonce,
				AccessKeyID: "LTAIid", SecurityToken: []byte(c.token),
			}, []byte("secret-key"))
			if err != nil {
				t.Fatal(err)
			}
			want := independentACS3(c.method, target, c.contentType, c.body, "QueryAccountBalance", "2017-12-14", "2026-01-02T03:04:05Z", nonce, "LTAIid", "secret-key", c.token)
			if got := acs3Find(t, headers, "authorization"); got != want {
				t.Fatalf("\n got %s\nwant %s", got, want)
			}
			if err := CheckACS3SignedHeaders(c.method, target, c.contentType, acs3SHAOf(c.body), headers); err != nil {
				t.Fatalf("a header set ACS3Sign produced fails its own check: %v", err)
			}
		})
	}
}

func TestACS3CanonicalQuery(t *testing.T) {
	for raw, want := range map[string]string{
		"":                        "",
		"b=2&a=1":                 "a=1&b=2",
		"a=b%20c":                 "a=b%20c",
		"a=%7e":                   "a=~",
		"a=*":                     "a=%2A",
		"a=%2f":                   "a=%2F",
		"x=2&x=1&w=":              "w=&x=1&x=2",
		"A=1&a=2":                 "A=1&a=2",
		"k":                       "k=",
		"name%2Fx=v":              "name%2Fx=v",
		"q=%E4%BD%A0":             "q=%E4%BD%A0",
		"a=1&b=2&c=3&d=4&e=5&f=6": "a=1&b=2&c=3&d=4&e=5&f=6",
	} {
		got, err := ACS3CanonicalQuery(raw)
		if err != nil || got != want {
			t.Fatalf("%q: got %q, %v; want %q", raw, got, err, want)
		}
	}
	for _, raw := range []string{"a=1&&b=2", "=1", "a=%zz", "a=b+c", "a=1;b=2", "&", "a=1&", "a=%"} {
		if got, err := ACS3CanonicalQuery(raw); err == nil {
			t.Fatalf("%q was accepted as %q", raw, got)
		}
	}
}

func TestACS3SignRefusesATargetThatIsNotAlreadyCanonical(t *testing.T) {
	input := ACS3Input{Method: "POST", BodySHA256: acs3SHAOf(""), Action: "A", Version: "2017-12-14", Date: time.Now(), Nonce: strings.Repeat("a", 32), AccessKeyID: "id"}
	for _, target := range []string{
		"https://h.example.com/?b=2&a=1", // unsorted
		"https://h.example.com/?a=%2f",   // lower-case escape
		"https://h.example.com/?a=*",     // unescaped reserved
		"https://h.example.com/?a=b+c",   // plus
		"http://h.example.com/",          // not https
		"https://H.example.com/",         // not lower case
		"https://u:p@h.example.com/",     // user info
		"https://h.example.com/#frag",    // fragment
		"/relative",
		"",
	} {
		input.Target = target
		if _, err := ACS3Sign(input, []byte("secret")); err == nil {
			t.Fatalf("%q was signed", target)
		}
	}
	input.Target = "https://h.example.com/?a=1&b=2"
	if _, err := ACS3Sign(input, []byte("secret")); err != nil {
		t.Fatal(err)
	}
}

func TestACS3SignRefusesBadFields(t *testing.T) {
	good := func() ACS3Input {
		return ACS3Input{Method: "POST", Target: "https://h.example.com/", BodySHA256: acs3SHAOf(""), Action: "QueryAccountBalance", Version: "2017-12-14", Date: time.Now(), Nonce: strings.Repeat("0", 32), AccessKeyID: "id"}
	}
	secret := []byte("secret")
	if _, err := ACS3Sign(good(), secret); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*ACS3Input){
		"action with a dash":    func(i *ACS3Input) { i.Action = "Query-Balance" },
		"action starting digit": func(i *ACS3Input) { i.Action = "1Query" },
		"action too long":       func(i *ACS3Input) { i.Action = "A" + strings.Repeat("b", 64) },
		"version shape":         func(i *ACS3Input) { i.Version = "2017-1-14" },
		"nonce upper case":      func(i *ACS3Input) { i.Nonce = strings.Repeat("A", 32) },
		"nonce short":           func(i *ACS3Input) { i.Nonce = "abc" },
		"body hash upper":       func(i *ACS3Input) { i.BodySHA256 = strings.ToUpper(acs3SHAOf("")) },
		"empty id":              func(i *ACS3Input) { i.AccessKeyID = "" },
		"id with comma":         func(i *ACS3Input) { i.AccessKeyID = "a,b" },
		"id too long":           func(i *ACS3Input) { i.AccessKeyID = strings.Repeat("a", 129) },
		"token with newline":    func(i *ACS3Input) { i.SecurityToken = []byte("a\nb") },
		"token too long":        func(i *ACS3Input) { i.SecurityToken = bytes.Repeat([]byte("a"), 4097) },
		"zero date":             func(i *ACS3Input) { i.Date = time.Time{} },
		"bad method":            func(i *ACS3Input) { i.Method = "TRACE" },
	} {
		in := good()
		mutate(&in)
		if _, err := ACS3Sign(in, secret); err == nil {
			t.Fatalf("%s was signed", name)
		}
	}
	if _, err := ACS3Sign(good(), nil); err == nil {
		t.Fatal("an empty secret was used")
	}
	if _, err := ACS3Sign(good(), bytes.Repeat([]byte("s"), 257)); err == nil {
		t.Fatal("an over-long secret was used")
	}
}

func acs3Fixture(t *testing.T) (ACS3Input, []SignedHeader) {
	t.Helper()
	in := ACS3Input{Method: "POST", Target: "https://business.aliyuncs.com/?A=1", ContentType: "application/json", BodySHA256: acs3SHAOf(`{"a":1}`),
		Action: "QueryAccountBalance", Version: "2017-12-14", Date: time.Date(2026, 5, 6, 7, 8, 9, 0, time.UTC), Nonce: strings.Repeat("ab", 16),
		AccessKeyID: "LTAIfixture", SecurityToken: []byte("sts-token-value")}
	headers, err := ACS3Sign(in, []byte("the-access-key-secret"))
	if err != nil {
		t.Fatal(err)
	}
	return in, headers
}

func TestACS3SignOutputIsSortedBoundedAndFreeOfTheSecret(t *testing.T) {
	_, headers := acs3Fixture(t)
	want := []string{"authorization", "content-type", "host", "x-acs-action", "x-acs-content-sha256", "x-acs-date", "x-acs-security-token", "x-acs-signature-nonce", "x-acs-version"}
	if got := names(headers); strings.Join(got, ",") != strings.Join(want, ",") || len(headers) != ACS3MaxSignedHeaders {
		t.Fatalf("names = %v", got)
	}
	for _, h := range headers {
		if bytes.Contains(h.Value, []byte("the-access-key-secret")) {
			t.Fatalf("header %s carries the secret", h.Name)
		}
		if h.Name != "x-acs-security-token" && bytes.Contains(h.Value, []byte("sts-token-value")) {
			t.Fatalf("header %s carries the token", h.Name)
		}
	}
}

// The runner holds no key: it can only check that what it sends is exactly the shape
// Portal signed. Every change the runner could make to the request, or to the headers,
// is refused here or fails the provider's check of the signature.
func TestCheckACS3SignedHeadersBindsTheRequest(t *testing.T) {
	in, headers := acs3Fixture(t)
	check := func(method, target, contentType, sha string, h []SignedHeader) error {
		return CheckACS3SignedHeaders(method, target, contentType, sha, h)
	}
	if err := check(in.Method, in.Target, in.ContentType, in.BodySHA256, headers); err != nil {
		t.Fatal(err)
	}
	clone := func() []SignedHeader {
		out := make([]SignedHeader, len(headers))
		for i, h := range headers {
			out[i] = SignedHeader{h.Name, bytes.Clone(h.Value)}
		}
		return out
	}
	set := func(h []SignedHeader, name, value string) []SignedHeader {
		for i := range h {
			if h[i].Name == name {
				h[i].Value = []byte(value)
			}
		}
		return h
	}
	drop := func(h []SignedHeader, name string) []SignedHeader {
		var out []SignedHeader
		for _, x := range h {
			if x.Name != name {
				out = append(out, x)
			}
		}
		return out
	}
	swapped := clone()
	swapped[0], swapped[1] = swapped[1], swapped[0]
	for name, h := range map[string][]SignedHeader{
		"an extra header name":                   append(clone(), SignedHeader{"x-forwarded-for", []byte("1.2.3.4")}),
		"an extra x-acs header":                  append(clone(), SignedHeader{"x-acs-zextra", []byte("1")}),
		"a different host":                       set(clone(), "host", "evil.example.com"),
		"a different body hash":                  set(clone(), "x-acs-content-sha256", acs3SHAOf("other")),
		"a different content type":               set(clone(), "content-type", "text/plain"),
		"no content type":                        drop(clone(), "content-type"),
		"no host":                                drop(clone(), "host"),
		"no nonce":                               drop(clone(), "x-acs-signature-nonce"),
		"no date":                                drop(clone(), "x-acs-date"),
		"no authorization":                       drop(clone(), "authorization"),
		"a malformed date":                       set(clone(), "x-acs-date", "2026-05-06 07:08:09"),
		"a malformed nonce":                      set(clone(), "x-acs-signature-nonce", "short"),
		"an invalid action":                      set(clone(), "x-acs-action", "Query Balance"),
		"an invalid version":                     set(clone(), "x-acs-version", "latest"),
		"an authorization without the algorithm": set(clone(), "authorization", "Bearer abc"),
		"a signed list that differs":             set(clone(), "authorization", "ACS3-HMAC-SHA256 Credential=id,SignedHeaders=host,Signature="+strings.Repeat("0", 64)),
		"a short signature":                      set(clone(), "authorization", "ACS3-HMAC-SHA256 Credential=id,SignedHeaders=content-type;host;x-acs-action;x-acs-content-sha256;x-acs-date;x-acs-security-token;x-acs-signature-nonce;x-acs-version,Signature=abc"),
		"an empty value":                         set(clone(), "x-acs-security-token", ""),
		"a value with a newline":                 set(clone(), "x-acs-security-token", "a\nb"),
		"a value with leading space":             set(clone(), "x-acs-security-token", " token"),
		"an unsorted list":                       swapped,
		"a duplicate":                            append(clone(), clone()[8]),
		"too many pairs":                         append(clone(), SignedHeader{"zzz", []byte("1")}),
		"an oversized token":                     set(clone(), "x-acs-security-token", strings.Repeat("t", 17000)),
	} {
		if err := check(in.Method, in.Target, in.ContentType, in.BodySHA256, h); err == nil {
			t.Fatalf("%s was accepted", name)
		}
	}
	// The request side: a runner that sends another body, content type or target.
	for name, args := range map[string][4]string{
		"another body":          {in.Method, in.Target, in.ContentType, acs3SHAOf("other")},
		"another content type":  {in.Method, in.Target, "text/plain", in.BodySHA256},
		"no content type":       {in.Method, in.Target, "", in.BodySHA256},
		"another host":          {in.Method, "https://other.aliyuncs.com/?A=1", in.ContentType, in.BodySHA256},
		"a non-canonical query": {in.Method, "https://business.aliyuncs.com/?B=2&A=1", in.ContentType, in.BodySHA256},
		"a plain-http target":   {in.Method, "http://business.aliyuncs.com/?A=1", in.ContentType, in.BodySHA256},
	} {
		if err := check(args[0], args[1], args[2], args[3], headers); err == nil {
			t.Fatalf("%s was accepted", name)
		}
	}
	// Fewer than nine pairs is fine: no token, no content type.
	bare, err := ACS3Sign(ACS3Input{Method: "POST", Target: "https://business.aliyuncs.com/", BodySHA256: acs3SHAOf(""), Action: "A", Version: "2017-12-14", Date: time.Now(), Nonce: strings.Repeat("1", 32), AccessKeyID: "id"}, []byte("s"))
	if err != nil || len(bare) != 7 || CheckACS3SignedHeaders("POST", "https://business.aliyuncs.com/", "", acs3SHAOf(""), bare) != nil {
		t.Fatalf("bare request: %d pairs, %v", len(bare), err)
	}
}

func TestACS3StandInHeadersKeepTheShapeAndDropTheSecrets(t *testing.T) {
	in, headers := acs3Fixture(t)
	stand := ACS3StandInHeaders(headers)
	if err := CheckACS3SignedHeaders(in.Method, in.Target, in.ContentType, in.BodySHA256, stand); err != nil {
		t.Fatal(err)
	}
	authorization := acs3Find(t, headers, "authorization")
	signature := authorization[len(authorization)-64:]
	for _, h := range stand {
		if h.Name == "x-acs-security-token" && string(h.Value) != "x" {
			t.Fatal("the token stand-in carries the token")
		}
		if h.Name == "authorization" && bytes.Contains(h.Value, []byte(signature)) {
			t.Fatal("the authorization stand-in carries the signature")
		}
	}
	if !ACS3SecretHeader("authorization") || !ACS3SecretHeader("x-acs-security-token") || ACS3SecretHeader("x-acs-date") {
		t.Fatal("secret header classification")
	}
}
