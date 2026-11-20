package appplatform

import (
	"strings"
	"testing"
)

func TestDecodeSigningRequest(t *testing.T) {
	const ok = `{"credential":"qwen-billing","scheme":"acs3","action":"QueryAccountBalance","version":"2017-12-14"}`
	got, err := DecodeSigningRequest([]byte(ok))
	if err != nil || got != (SigningRequest{Credential: "qwen-billing", Scheme: "acs3", Action: "QueryAccountBalance", Version: "2017-12-14"}) {
		t.Fatalf("%+v %v", got, err)
	}
	for name, raw := range map[string]string{
		"empty":             ``,
		"null":              `null`,
		"array":             `[]`,
		"no object":         `{}`,
		"missing version":   `{"credential":"a","scheme":"acs3","action":"QueryAccountBalance"}`,
		"unknown member":    strings.Replace(ok, `"scheme":"acs3"`, `"scheme":"acs3","x":"y"`, 1),
		"duplicate":         strings.Replace(ok, `"scheme":"acs3"`, `"scheme":"acs3","scheme":"acs3"`, 1),
		"upper-case key":    strings.Replace(ok, `"scheme"`, `"Scheme"`, 1),
		"non-string":        strings.Replace(ok, `"acs3"`, `3`, 1),
		"null member":       strings.Replace(ok, `"acs3"`, `null`, 1),
		"trailing data":     ok + ` {}`,
		"unknown scheme":    strings.Replace(ok, `"acs3"`, `"aws4"`, 1),
		"bad credential":    strings.Replace(ok, `qwen-billing`, `Qwen`, 1),
		"bad action":        strings.Replace(ok, `QueryAccountBalance`, `Query Account`, 1),
		"digit-led action":  strings.Replace(ok, `QueryAccountBalance`, `1Query`, 1),
		"long action":       strings.Replace(ok, `QueryAccountBalance`, "A"+strings.Repeat("a", 64), 1),
		"bad version":       strings.Replace(ok, `2017-12-14`, `20171214`, 1),
		"bad version shape": strings.Replace(ok, `2017-12-14`, `2017-12-1x`, 1),
		"invalid utf8":      "{\"credential\":\"a\xff\"}",
	} {
		if got, err := DecodeSigningRequest([]byte(raw)); err == nil || got != (SigningRequest{}) {
			t.Errorf("%s: accepted %+v", name, got)
		}
	}
}

func TestParseACS3Credential(t *testing.T) {
	good := map[string]string{
		"with token":      `{"access_key_id":"LTAIabc","access_key_secret":"s3cr3t","security_token":"STS.tok"}`,
		"empty token":     `{"access_key_id":"LTAIabc","access_key_secret":"s3cr3t","security_token":""}`,
		"token absent":    `{"access_key_id":"LTAIabc","access_key_secret":"s3cr3t"}`,
		"escaped secret":  `{"access_key_id":"a","access_key_secret":"q\"w\\e"}`,
		"longest members": `{"access_key_id":"` + strings.Repeat("i", 128) + `","access_key_secret":"` + strings.Repeat("s", 256) + `","security_token":"` + strings.Repeat("t", 4096) + `"}`,
	}
	for name, raw := range good {
		c, err := ParseACS3Credential([]byte(raw))
		if err != nil || c.AccessKeyID == "" || len(c.AccessKeySecret) == 0 {
			t.Errorf("%s: %+v %v", name, c, err)
		}
		c.Clear()
		if len(c.AccessKeySecret) != 0 || len(c.SecurityToken) != 0 {
			t.Errorf("%s: Clear left the secret", name)
		}
	}
	c, _ := ParseACS3Credential([]byte(good["escaped secret"]))
	if string(c.AccessKeySecret) != `q"w\e` {
		t.Fatalf("secret decoded as %q", c.AccessKeySecret)
	}
	for name, raw := range map[string]string{
		"empty":             ``,
		"null":              `null`,
		"array":             `[]`,
		"no members":        `{}`,
		"no id":             `{"access_key_secret":"s"}`,
		"no secret":         `{"access_key_id":"a"}`,
		"empty id":          `{"access_key_id":"","access_key_secret":"s"}`,
		"empty secret":      `{"access_key_id":"a","access_key_secret":""}`,
		"unknown member":    `{"access_key_id":"a","access_key_secret":"s","region":"cn"}`,
		"duplicate member":  `{"access_key_id":"a","access_key_id":"b","access_key_secret":"s"}`,
		"upper-case member": `{"Access_Key_Id":"a","access_key_secret":"s"}`,
		"non-string":        `{"access_key_id":1,"access_key_secret":"s"}`,
		"null token":        `{"access_key_id":"a","access_key_secret":"s","security_token":null}`,
		"trailing data":     `{"access_key_id":"a","access_key_secret":"s"} x`,
		"long id":           `{"access_key_id":"` + strings.Repeat("i", 129) + `","access_key_secret":"s"}`,
		"long secret":       `{"access_key_id":"a","access_key_secret":"` + strings.Repeat("s", 257) + `"}`,
		"long token":        `{"access_key_id":"a","access_key_secret":"s","security_token":"` + strings.Repeat("t", 4097) + `"}`,
		"id with a comma":   `{"access_key_id":"a,b","access_key_secret":"s"}`,
		"control in secret": `{"access_key_id":"a","access_key_secret":"s\nx"}`,
		"non-ascii token":   `{"access_key_id":"a","access_key_secret":"s","security_token":"é"}`,
		"too large overall": `{"access_key_id":"a","access_key_secret":"s","security_token":"` + strings.Repeat("t", 9000) + `"}`,
	} {
		if c, err := ParseACS3Credential([]byte(raw)); err == nil {
			t.Errorf("%s: accepted %+v", name, c)
		}
	}
}
