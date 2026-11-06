package appplatform

import (
	"bytes"
	"testing"
	"time"
)

func exampleHTTPStreamPolicy() HTTPStreamPolicy {
	return HTTPStreamPolicy{
		Origin:       "https://stream.example.com",
		Paths:        []string{"/v1/events", "/v1/feed"},
		Methods:      []string{"GET", "POST"},
		RequestBytes: 1024,
		StreamBytes:  2 << 20,
		Duration:     20 * time.Second,
		IdleTimeout:  5 * time.Second,
		Concurrency:  2,
		MediaTypes:   []string{"text/event-stream", "application/x-ndjson"},
	}
}

func TestHTTPStreamPolicyCanonicalBinding(t *testing.T) {
	p := exampleHTTPStreamPolicy()
	raw, err := CanonicalHTTPStreamPolicy(p)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ParseHTTPStreamPolicy(raw)
	if err != nil || parsed.Policy.Origin != p.Origin || parsed.Policy.StreamBytes != p.StreamBytes || parsed.Policy.Duration != p.Duration || parsed.Policy.IdleTimeout != p.IdleTimeout || parsed.SHA256 == "" {
		t.Fatalf("parsed %+v %v", parsed, err)
	}
	for _, changed := range [][]byte{
		append([]byte(" "), raw...),
		append(bytes.Clone(raw), '\n'),
		bytes.Replace(raw, []byte(`"GET"`), []byte(`"get"`), 1),
		bytes.Replace(raw, []byte(`"text/event-stream"`), []byte(`"text/plain"`), 1),
	} {
		if _, err := ParseHTTPStreamPolicy(changed); err == nil {
			t.Fatalf("accepted ambiguous policy %s", changed)
		}
	}
}

func TestHTTPStreamPolicyRestrictions(t *testing.T) {
	if err := ValidateHTTPStreamPolicyTarget(exampleHTTPStreamPolicy(), "GET", "https://stream.example.com/v1/events?topic=a"); err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{
		"http://stream.example.com/v1/events",
		"https://other.example.com/v1/events",
		"https://stream.example.com/v1/event",
		"wss://stream.example.com/v1/events",
	} {
		if ValidateHTTPStreamPolicyTarget(exampleHTTPStreamPolicy(), "GET", target) == nil {
			t.Fatal("accepted target", target)
		}
	}
	for _, mutation := range []func(*HTTPStreamPolicy){
		func(p *HTTPStreamPolicy) { p.Methods = []string{"PUT"} },
		func(p *HTTPStreamPolicy) { p.Methods = []string{"CONNECT"} },
		func(p *HTTPStreamPolicy) { p.StreamBytes = MaxHTTPStreamBytes + 1 },
		func(p *HTTPStreamPolicy) { p.Duration = 31 * time.Second },
		func(p *HTTPStreamPolicy) { p.IdleTimeout = p.Duration + time.Millisecond },
		func(p *HTTPStreamPolicy) { p.MediaTypes = []string{"text/plain"} },
		func(p *HTTPStreamPolicy) { p.Concurrency = 5 },
	} {
		p := exampleHTTPStreamPolicy()
		mutation(&p)
		if _, err := CanonicalHTTPStreamPolicy(p); err == nil {
			t.Fatalf("accepted invalid stream policy %+v", p)
		}
	}
}

func TestHTTPStreamContentTypeIsExplicitAndBounded(t *testing.T) {
	p := exampleHTTPStreamPolicy()
	for _, value := range []string{
		"text/event-stream",
		"text/event-stream; charset=utf-8",
		"application/x-ndjson; charset=UTF-8",
	} {
		if _, ok := ValidateHTTPStreamContentType(p, value); !ok {
			t.Fatal("refused", value)
		}
	}
	for _, value := range []string{
		"text/plain",
		"application/octet-stream",
		"text/event-stream; boundary=x",
		"text/event-stream; charset=latin1",
		"text/event-stream\r\nX-Evil: yes",
	} {
		if _, ok := ValidateHTTPStreamContentType(p, value); ok {
			t.Fatal("accepted", value)
		}
	}
}
