package app

import (
	"encoding/json"
	"net/http"
	"testing"
)

func TestVendorRatePolicyAdminValidationAndRuntimeWiring(t *testing.T) {
	s := quoteAdminServer(t)
	defer publicVendorScheduler.configure(nil)
	bad := quoteAdminSave(t, s, `{"ttlOpenSecs":120,"ratePolicies":{"tencent":{"requestsPerMinute":30,"backgroundPerMinute":30}}}`)
	if bad.Code != http.StatusBadRequest {
		t.Fatalf("invalid reserved capacity accepted: %s", bad.Body.String())
	}
	if cfg := s.quoteConfigLoad(); cfg.TTLOpen != quoteTTLOpen {
		t.Fatal("rejected policy changed another field")
	}
	good := quoteAdminSave(t, s, `{"ratePolicies":{"tencent":{"requestsPerMinute":20,"backgroundPerMinute":4}}}`)
	if good.Code != http.StatusOK {
		t.Fatal(good.Body.String())
	}
	var view struct {
		Policies map[string]vendorRatePolicy `json:"ratePolicies"`
	}
	if err := json.Unmarshal(good.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if view.Policies["tencent"].RequestsPerMinute != 20 {
		t.Fatalf("saved policy not returned: %+v", view)
	}
	s.quoteConfigLoad()
	publicVendorScheduler.mu.Lock()
	actual := publicVendorScheduler.policyLocked("tencent")
	publicVendorScheduler.mu.Unlock()
	if actual.BackgroundPerMinute != 4 || actual.RequestsPerMinute != 20 {
		t.Fatalf("fetch path ignored saved limits: %+v", actual)
	}
	if got := s.vendorPolicies()["sina"]; got != defaultVendorRatePolicy {
		t.Fatalf("partial save changed another source: %+v", got)
	}
}

func TestVendorPolicyReadRejectsCorruptOrUnsafeValues(t *testing.T) {
	s := quoteAdminServer(t)
	for _, raw := range []string{`broken`, `{"tencent":{"requestsPerMinute":0,"backgroundPerMinute":0}}`, `{"tencent":{"requestsPerMinute":1000000,"backgroundPerMinute":1}}`} {
		s.st.SetSetting(setQuoteVendorRates, raw)
		if got := s.vendorPolicies()["tencent"]; got != defaultVendorRatePolicy {
			t.Fatalf("unsafe policy applied: %+v", got)
		}
	}
}
