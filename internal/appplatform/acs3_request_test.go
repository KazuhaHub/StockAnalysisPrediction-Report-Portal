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
