package broker

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

func TestHTTPRetryAfterReleasedOnlyFor429And503(t *testing.T) {
	cases := []struct {
		name   string
		status int
		values []string
		want   int
	}{
		{"429 seconds", 429, []string{"30"}, 30},
		{"503 zero", 503, []string{"0"}, 0},
		{"503 upper bound", 503, []string{"86400"}, 86400},
		{"429 above bound", 429, []string{"86401"}, -1},
		{"429 negative", 429, []string{"-1"}, -1},
		{"429 http date", 429, []string{"Wed, 21 Oct 2026 07:28:00 GMT"}, -1},
		{"429 signed", 429, []string{"+5"}, -1},
		{"429 fractional", 429, []string{"1.5"}, -1},
		{"429 empty", 429, []string{""}, -1},
		{"429 repeated", 429, []string{"5", "5"}, -1},
		{"429 absent", 429, nil, -1},
		{"200 ignored", 200, []string{"5"}, -1},
		{"500 ignored", 500, []string{"5"}, -1},
		{"404 ignored", 404, []string{"5"}, -1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h, _, _ := fixture(t, testPolicy(), func(context.Context) error { return nil }, func(w http.ResponseWriter, r *http.Request) {
				for _, v := range c.values {
					w.Header().Add("Retry-After", v)
				}
				w.WriteHeader(c.status)
				_, _ = w.Write([]byte("x"))
			})
			got, err := h.Do(context.Background(), HTTPRequest{Method: "GET", URL: "https://api.example.com/v1"})
			if err != nil || got.Status != c.status || got.RetryAfterSeconds != c.want {
				t.Fatalf("%+v %v", got, err)
			}
		})
	}
}

func TestHTTPRetryAfterNeverMakesOutcomeUncertain(t *testing.T) {
	// A hostile Retry-After value must only be dropped; it must not turn a
	// complete, authorised response into an uncertain outcome.
	for _, v := range []string{"abc", strings.Repeat("9", 5000), "5, 6"} {
		h, _, _ := fixture(t, testPolicy(), func(context.Context) error { return nil }, func(w http.ResponseWriter, r *http.Request) {
			w.Header()["Retry-After"] = []string{v}
			w.WriteHeader(503)
			_, _ = w.Write([]byte("ok"))
		})
		got, err := h.Do(context.Background(), HTTPRequest{Method: "GET", URL: "https://api.example.com/v1"})
		if err != nil || got.Status != 503 || string(got.Body) != "ok" || got.RetryAfterSeconds != -1 {
			t.Fatalf("%q: %+v %v", v, got, err)
		}
	}
}
