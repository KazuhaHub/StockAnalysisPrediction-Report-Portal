package journal

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestCandidateReadinessMetadata(t *testing.T) {
	r := CandidateReadiness{Format: "portal.runner.candidate-readiness/1-dev.1", PayloadSHA256: strings.Repeat("a", 64), RunningReceiptSHA256: strings.Repeat("b", 64), NativeID: strings.Repeat("c", 64), Protocol: "portal.backend/1", DataFormat: "1", ObservedMS: "1000"}
	raw, _ := json.Marshal(r)
	if parsed, err := ParseCandidateReadiness(raw); err != nil || parsed != r {
		t.Fatal("canonical readiness", err)
	}
	for _, invalid := range [][]byte{append(raw, ' '), []byte(strings.Replace(string(raw), `"data_format":"1"`, `"data_format":"0"`, 1)), []byte(strings.Replace(string(raw), `"protocol":"portal.backend/1"`, `"protocol":"other"`, 1)), []byte(strings.Replace(string(raw), `"observed_ms":"1000"`, `"observed_ms":"-1"`, 1)), []byte(strings.TrimSuffix(string(raw), "}") + `,"extra":true}`)} {
		if _, err := ParseCandidateReadiness(invalid); err == nil {
			t.Fatal("ambiguous readiness metadata accepted")
		}
	}
	if _, err := new(Store).ObserveCandidateReadiness(context.Background(), nil, nil, nil, nil); err == nil {
		t.Fatal("missing native runtime minted readiness")
	}
}
