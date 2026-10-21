package notification

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestHTTPAttemptClassifiesResponsesWithoutRetry(t *testing.T) {
	for _, tc := range []struct {
		code                int
		accepted, retryable bool
	}{
		{200, true, false}, {302, false, false}, {400, false, false}, {429, false, true}, {503, false, true},
	} {
		t.Run(http.StatusText(tc.code), func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.Header().Set("Retry-After", "30")
				w.WriteHeader(tc.code)
			}))
			defer server.Close()
			req, _ := http.NewRequestWithContext(context.Background(), "POST", server.URL, strings.NewReader("{}"))
			result := AttemptHTTP(server.Client(), req)
			if result.Status != tc.code || result.Accepted != tc.accepted || result.Retryable != tc.retryable || result.Err != nil || result.Uncertain {
				t.Fatalf("result=%+v", result)
			}
			if calls.Load() != 1 {
				t.Fatalf("attempts=%d", calls.Load())
			}
			if tc.code == 429 && result.RetryAfter != 30*time.Second {
				t.Fatalf("retry-after=%s", result.RetryAfter)
			}
		})
	}
}

type failingTransport struct{}

func (failingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	return nil, errors.New("request failed: " + r.URL.String())
}

func TestHTTPAttemptRedactsDestination(t *testing.T) {
	req, _ := http.NewRequest("POST", "https://example.com/services/private-token?key=secret", nil)
	result := AttemptHTTP(&http.Client{Transport: failingTransport{}}, req)
	if result.Err == nil || strings.Contains(result.Err.Error(), "private-token") || strings.Contains(result.Err.Error(), "secret") {
		t.Fatalf("unsafe error: %v", result.Err)
	}
}

func TestHTTPAttemptLostResponseIsUncertain(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		conn, _, err := w.(http.Hijacker).Hijack()
		if err == nil {
			conn.Close()
		}
	}))
	defer server.Close()
	req, _ := http.NewRequest("POST", server.URL, strings.NewReader("{}"))
	result := AttemptHTTP(server.Client(), req)
	if result.Err == nil || !result.Uncertain || result.Retryable {
		t.Fatalf("lost response=%+v", result)
	}
}
