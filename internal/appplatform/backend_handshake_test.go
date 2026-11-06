package appplatform

import (
	"strings"
	"testing"
)

func TestBackendHandshakeMessageParsing(t *testing.T) {
	for _, field := range []string{"challenge", "answer"} {
		parse := ParseBackendHandshakeChallenge
		if field == "answer" {
			parse = ParseBackendHandshakeAnswer
		}
		valid := `{"` + field + `":"abc_def.ghi-JK"}`
		for _, raw := range []string{valid, " \n" + valid + "\t", `{ "` + field + `" : "abc_def.ghi-JK" }`} {
			if token, err := parse([]byte(raw)); err != nil || token != "abc_def.ghi-JK" {
				t.Fatal("valid message", field, err)
			}
		}
		for _, raw := range []string{"", `null`, `{}`, `[]`, valid + ` {}`, `{"` + field + `":null}`, `{"` + field + `":12}`, `{"` + field + `":"abc"}`, `{"` + field + `":"a.b.c"}`, `{"` + field + `":"a.b","extra":1}`, `{"` + field + `":"a.b","` + field + `":"c.d"}`, `{"Answer":"a.b"}`, `{"` + field + `":"` + strings.Repeat("a", 4096) + `.b"}`, strings.Repeat(" ", 8193)} {
			if _, err := parse([]byte(raw)); err == nil {
				t.Fatal("invalid message accepted", field, raw[:min(len(raw), 50)])
			}
		}
	}
	if _, err := BackendHandshakeRequest("a.b"); err != nil {
		t.Fatal(err)
	}
	for _, token := range []string{"", "a", "a.b.c", "a. b", "a\nb.c", "a.b/", "a.b="} {
		if _, err := BackendHandshakeRequest(token); err == nil {
			t.Fatal("invalid challenge encoded")
		}
	}
}
