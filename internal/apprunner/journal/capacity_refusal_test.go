//go:build linux || darwin

package journal

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func refusalIdentity() CapacityIdentity {
	return CapacityIdentity{
		DeploymentID: fixtureID(1), ControllerEpoch: fixtureID(2), InstallationID: fixtureID(3), OperationID: fixtureID(4),
		TargetGeneration: "2", Stage: "start", EffectNumber: "1", EffectID: fixtureID(5), Kind: "update-start", PayloadSHA256: strings.Repeat("a", 64),
	}
}

func TestCapacityRefusalReceiptIsCanonicalAndBound(t *testing.T) {
	id := refusalIdentity()
	receipt := newCapacityRefusal(id, capacityRefusal{dimension: CapacityMemory, requested: 512, remaining: 100}, 1234)
	raw, err := json.Marshal(receipt)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ParseCapacityRefusal(raw)
	if err != nil || parsed != receipt {
		t.Fatalf("round trip: %+v %v", parsed, err)
	}
	if parsed.Outcome != CapacityExhaustedOutcome || parsed.Dimension != CapacityMemory || parsed.Requested != "512" || parsed.Remaining != "100" {
		t.Fatalf("typed fields: %+v", parsed)
	}
	if !parsed.Matches(id) {
		t.Fatal("receipt does not match its own command")
	}
	other := id
	other.EffectID = fixtureID(6)
	if parsed.Matches(other) {
		t.Fatal("receipt matched another effect")
	}
	other = id
	other.PayloadSHA256 = strings.Repeat("b", 64)
	if parsed.Matches(other) {
		t.Fatal("receipt matched another payload")
	}
}

func TestCapacityRefusalParseRefusesEveryMalformedShape(t *testing.T) {
	good := newCapacityRefusal(refusalIdentity(), capacityRefusal{dimension: CapacityCPU, requested: 5, remaining: 0}, 9)
	mutate := map[string]func(*CapacityRefusal){
		"format":      func(r *CapacityRefusal) { r.Format = "x" },
		"outcome":     func(r *CapacityRefusal) { r.Outcome = "failed" },
		"dimension":   func(r *CapacityRefusal) { r.Dimension = "swap" },
		"requested0":  func(r *CapacityRefusal) { r.Requested = "0" },
		"requestedNN": func(r *CapacityRefusal) { r.Requested = "05" },
		"remaining-":  func(r *CapacityRefusal) { r.Remaining = "-1" },
		"stage":       func(r *CapacityRefusal) { r.Stage = "" },
		"effect":      func(r *CapacityRefusal) { r.EffectID = "nope" },
		"payload":     func(r *CapacityRefusal) { r.PayloadSHA256 = "abc" },
		"settled":     func(r *CapacityRefusal) { r.SettledMS = "0" },
	}
	for name, f := range mutate {
		t.Run(name, func(t *testing.T) {
			r := good
			f(&r)
			raw, _ := json.Marshal(r)
			if _, err := ParseCapacityRefusal(raw); err == nil {
				t.Fatal("malformed refusal accepted")
			}
		})
	}
	if _, err := ParseCapacityRefusal([]byte(`{"format":"` + capacityRefusalFormat + `","extra":1}`)); err == nil {
		t.Fatal("unknown field accepted")
	}
	if _, err := ParseCapacityRefusal(nil); err == nil {
		t.Fatal("empty accepted")
	}
}

func TestCapacityRefusalErrorCarriesTheReceipt(t *testing.T) {
	receipt := newCapacityRefusal(refusalIdentity(), capacityRefusal{dimension: CapacityRunningBackends, requested: 1, remaining: 0}, 9)
	var err error = &CapacityRefusalError{Receipt: receipt}
	var typed *CapacityRefusalError
	if !errors.As(err, &typed) || typed.Receipt != receipt || !strings.Contains(err.Error(), "running_backends") {
		t.Fatalf("error = %v", err)
	}
	var _ = context.Background
}
