package appplatform

import (
	"strings"
	"testing"
)

func TestBackendReadinessContract(t *testing.T) {
	for _, raw := range []string{`{"protocol":"portal.backend/1","ready":true,"data_format":"1"}`, `{ "data_format":"2", "ready":false, "protocol":"portal.backend/1" }`} {
		if _, err := ParseBackendReadiness([]byte(raw)); err != nil {
			t.Fatal(err)
		}
	}
	for _, raw := range []string{`{}`, `null`, `[]`, `{"protocol":"portal.backend/1","ready":true,"data_format":"01"}`, `{"protocol":"portal.backend/1","ready":true,"data_format":1}`, `{"protocol":"portal.backend/1","ready":true,"data_format":"0"}`, `{"protocol":"portal.backend/1","ready":null,"data_format":"1"}`, `{"protocol":"portal.backend/1","ready":"true","data_format":"1"}`, `{"protocol":"other","ready":true,"data_format":"1"}`, `{"protocol":"portal.backend/1","ready":true,"ready":false,"data_format":"1"}`, `{"protocol":"portal.backend/1","ready":true,"data_format":"1","secret":"ignored"}`, `{"protocol":"portal.backend/1","ready":true,"data_format":"1"}{}`, strings.Repeat(" ", 4097)} {
		if _, err := ParseBackendReadiness([]byte(raw)); err == nil {
			t.Fatal("invalid readiness accepted", raw)
		}
	}
}
