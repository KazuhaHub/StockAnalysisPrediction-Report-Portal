package broker

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/url"
	"strings"
	"testing"
)

const echoToken = `Ab3-x"<tok>+/=&q\z9Zk`

func echoJSONEscaped(s string) string {
	raw, _ := json.Marshal(s)
	return string(raw[1 : len(raw)-1])
}

// The platform withholds a response that gives the credential back in the forms
// an upstream commonly does: as is, in any letter case, base64 (either alphabet,
// at any byte alignment), hex, URL-encoded or JSON-escaped.
func TestCredentialResponseEchoRecognisesCommonTransforms(t *testing.T) {
	bearer := []byte("Bearer " + echoToken)
	apiKey := []byte(echoToken)
	for _, c := range []struct {
		name, header string
		value        []byte
	}{{"bearer", "Authorization", bearer}, {"api key", "X-API-Key", apiKey}} {
		forms := map[string]string{
			"raw":               "x " + echoToken + " y",
			"lower case":        "x " + strings.ToLower(echoToken) + " y",
			"upper case":        "x " + strings.ToUpper(echoToken) + " y",
			"base64 std":        "x " + base64.StdEncoding.EncodeToString([]byte(echoToken)) + " y",
			"base64 url":        "x " + base64.URLEncoding.EncodeToString([]byte(echoToken)) + " y",
			"base64 raw":        "x " + base64.RawStdEncoding.EncodeToString([]byte(echoToken)) + " y",
			"base64 offset 1":   "x " + base64.StdEncoding.EncodeToString([]byte("a"+echoToken)) + " y",
			"base64 offset 2":   "x " + base64.StdEncoding.EncodeToString([]byte("ab"+echoToken)) + " y",
			"base64 url off 2":  "x " + base64.RawURLEncoding.EncodeToString([]byte("ab"+echoToken+"cd")) + " y",
			"hex":               "x " + hex.EncodeToString([]byte(echoToken)) + " y",
			"hex upper":         "x " + strings.ToUpper(hex.EncodeToString([]byte(echoToken))) + " y",
			"url query":         "x " + url.QueryEscape(echoToken) + " y",
			"url path":          "x " + url.PathEscape(echoToken) + " y",
			"json":              `{"echo":"` + echoJSONEscaped(echoToken) + `"}`,
			"json whole header": `{"h":"` + echoJSONEscaped(string(bearer)) + `"}`,
		}
		if c.header == "Authorization" {
			forms["header form lower case"] = "x " + strings.ToLower(string(bearer)) + " y"
			forms["header form base64"] = "x " + base64.StdEncoding.EncodeToString(bearer) + " y"
		}
		for name, body := range forms {
			if !credentialResponseEcho([]byte(body), c.header, c.value) {
				t.Errorf("%s/%s: echo not recognised in %q", c.name, name, body)
			}
		}
		for _, body := range []string{
			``, `{"ok":true}`, strings.Repeat("A", 4096),
			"x " + echoToken[:len(echoToken)-3] + " y", // not the whole token
			"x " + base64.StdEncoding.EncodeToString([]byte("another credential entirely")) + " y",
		} {
			if credentialResponseEcho([]byte(body), c.header, c.value) {
				t.Errorf("%s: false positive on %q", c.name, body)
			}
		}
	}
	// Short values are only matched as they are: a transform of a few characters
	// would withhold ordinary responses.
	if credentialResponseEcho([]byte(base64.StdEncoding.EncodeToString([]byte("abc"))+" hello"), "X-API-Key", []byte("abc")) {
		t.Error("short value matched by its base64 form")
	}
	if !credentialResponseEcho([]byte("xabcx"), "X-API-Key", []byte("abc")) {
		t.Error("short value no longer matched as it is")
	}
}

// A transform split across stream chunks is still caught.
func TestStreamEchoGuardCatchesATransformSplitAcrossChunks(t *testing.T) {
	value := []byte(echoToken)
	encoded := []byte(base64.StdEncoding.EncodeToString(value))
	for cut := 1; cut < len(encoded); cut += 5 {
		g := newHTTPStreamEchoGuard("X-API-Key", value)
		first, hit := g.Accept(append([]byte("data: "), encoded[:cut]...))
		if hit {
			continue
		}
		_, hit = g.Accept(append(bytes.Clone(encoded[cut:]), []byte(" more")...))
		if !hit {
			t.Fatalf("split at %d escaped (released %q first)", cut, first)
		}
	}
}
