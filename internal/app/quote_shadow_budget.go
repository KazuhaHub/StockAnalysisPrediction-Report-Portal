package app

import (
	"context"
	"net/http"
	"sync"
)

const (
	forecastShadowMaxComparisons  = 2
	forecastShadowMaxHTTPAttempts = 4
)

// Separate counters distinguish a full source observation from its constituent HTTP requests.
// The client override belongs only to this pass; redirects use the same transport and budget.
func withQuoteShadowHTTPBudget(ctx context.Context) (context.Context, *quoteShadowHTTPBudget) {
	client := *vendorHTTPClient(ctx)
	budget := &quoteShadowHTTPBudget{base: client.Transport}
	if budget.base == nil {
		budget.base = http.DefaultTransport
	}
	client.Transport = budget
	return context.WithValue(ctx, quoteProbeClientKey{}, &client), budget
}

type quoteShadowHTTPBudget struct {
	mu     sync.Mutex
	used   int
	denied int
	base   http.RoundTripper
}

func (budget *quoteShadowHTTPBudget) RoundTrip(request *http.Request) (*http.Response, error) {
	if err := request.Context().Err(); err != nil {
		return nil, err
	}
	budget.mu.Lock()
	if budget.used >= forecastShadowMaxHTTPAttempts {
		budget.denied++
		budget.mu.Unlock()
		return nil, &ledgerRefusal{Code: "shadow_budget"}
	}
	budget.used++
	budget.mu.Unlock()
	response, err := budget.base.RoundTrip(request)
	if isVendorDeferred(err) {
		budget.mu.Lock()
		budget.used--
		budget.mu.Unlock()
	}
	return response, err
}

func (budget *quoteShadowHTTPBudget) attempts() int {
	budget.mu.Lock()
	defer budget.mu.Unlock()
	return budget.used
}

func (budget *quoteShadowHTTPBudget) denials() int {
	budget.mu.Lock()
	defer budget.mu.Unlock()
	return budget.denied
}
