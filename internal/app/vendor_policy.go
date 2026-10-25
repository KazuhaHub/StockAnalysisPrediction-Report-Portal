package app

import (
	"encoding/json"
	"maps"
	"time"
)

const setQuoteVendorRates = "quote_vendor_rates"

var vendorPolicySources = []string{"tencent", "sina", "eastmoney", "yahoo", "sohu"}

func validVendorRatePolicy(p vendorRatePolicy) bool {
	return p.RequestsPerMinute >= 2 && p.RequestsPerMinute <= 60 && p.BackgroundPerMinute >= 1 && p.BackgroundPerMinute <= p.RequestsPerMinute/2
}

func (s *Server) vendorPolicies() map[string]vendorRatePolicy {
	var stored map[string]vendorRatePolicy
	if s.st != nil {
		_ = json.Unmarshal([]byte(s.st.GetSetting(setQuoteVendorRates, "{}")), &stored)
	}
	policies := map[string]vendorRatePolicy{}
	for _, source := range vendorPolicySources {
		policy := defaultVendorRatePolicy
		if candidate, ok := stored[source]; ok && validVendorRatePolicy(candidate) {
			policy = candidate
		}
		policies[source] = policy
	}
	return policies
}

func (s *vendorScheduler) policyLocked(source string) vendorRatePolicy {
	if policy, ok := s.overrides[source]; ok {
		return policy
	}
	return s.policy
}

func (s *vendorScheduler) configure(policies map[string]vendorRatePolicy) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if maps.Equal(s.overrides, policies) {
		return
	}
	for source, state := range s.sources {
		old := s.policyLocked(source)
		next, ok := policies[source]
		if !ok {
			next = s.policy
		}
		if !state.next.IsZero() {
			state.next = state.next.Add(-time.Minute / time.Duration(old.RequestsPerMinute)).Add(time.Minute / time.Duration(next.RequestsPerMinute))
		}
		if !state.nextBackground.IsZero() {
			state.nextBackground = state.nextBackground.Add(-time.Minute / time.Duration(old.BackgroundPerMinute)).Add(time.Minute / time.Duration(next.BackgroundPerMinute))
		}
	}
	s.overrides = maps.Clone(policies)
	s.notifyLocked()
}
