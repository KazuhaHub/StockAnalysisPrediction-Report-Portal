package transport

import (
	"encoding/json"
	"testing"
)

func TestCandidateReadinessObservationCannotDeserialize(t *testing.T) {
	var o CandidateReadinessObservation
	if _, err := json.Marshal(o); err == nil {
		t.Fatal("readiness observation serializable")
	}
	if err := json.Unmarshal([]byte(`{"ready":true}`), &o); err == nil || !o.ReceivedAt().IsZero() || o.CommandSHA256() != "" {
		t.Fatal("JSON minted readiness provenance")
	}
}
