// Package notification contains shared delivery primitives. Producers own
// audience policy; coordinators own retries. An adapter sends exactly once.
package notification

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// HTTPResult records transport acceptance, not downstream business completion.
// A provider-specific adapter may apply stricter response validation.
type HTTPResult struct {
	Status     int
	Accepted   bool
	Uncertain  bool
	Retryable  bool
	RetryAfter time.Duration
	Err        error
}

// AttemptHTTP executes one request. Callers must validate the destination and
// configure redirects and deadlines for their adapter's compatibility policy.
// Response bodies are discarded with a bound and never exposed as diagnostics.
func AttemptHTTP(client *http.Client, req *http.Request) HTTPResult {
	var wrote atomic.Bool
	trace := &httptrace.ClientTrace{
		WroteHeaders: func() { wrote.Store(true) },
	}
	req = req.WithContext(httptrace.WithClientTrace(req.Context(), trace))
	resp, err := client.Do(req)
	if err != nil {
		if resp != nil && resp.Body != nil {
			resp.Body.Close()
		}
		return HTTPResult{Uncertain: wrote.Load(), Retryable: !wrote.Load(), Err: redactHTTPError(err, req.URL)}
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
	result := HTTPResult{
		Status:    resp.StatusCode,
		Accepted:  resp.StatusCode >= 200 && resp.StatusCode < 300,
		Retryable: resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500,
	}
	if raw := resp.Header.Get("Retry-After"); result.Retryable && raw != "" {
		if seconds, err := strconv.ParseInt(raw, 10, 32); err == nil && seconds >= 0 {
			result.RetryAfter = time.Duration(seconds) * time.Second
		} else if at, err := http.ParseTime(raw); err == nil {
			result.RetryAfter = max(time.Duration(0), time.Until(at))
		}
	}
	return result
}

func redactHTTPError(err error, destination *url.URL) error {
	var wrapped *url.Error
	if errors.As(err, &wrapped) && wrapped.Err != nil {
		err = wrapped.Err
	}
	message := err.Error()
	// Scrub both the whole URL and credential-bearing fragments. A transport can
	// use a normalized spelling or include only the request URI in its error.
	for _, value := range []string{destination.String(), destination.RequestURI(), destination.Path, destination.RawQuery} {
		if value != "" && value != "/" {
			message = strings.ReplaceAll(message, value, "<notification-destination>")
		}
	}
	return errors.New(message)
}
