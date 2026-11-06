package driver

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestWritableReadinessOpaqueProofCannotBeReconstructed(t *testing.T) {
	var proof WritableCandidateReadinessObservation
	if _, err := proof.Snapshot(); err == nil {
		t.Fatal("zero native readiness")
	}
	if _, err := json.Marshal(proof); err == nil {
		t.Fatal("serialized native readiness")
	}
	if err := json.Unmarshal([]byte(`{"valid":true}`), &proof); err == nil {
		t.Fatal("decoded native readiness")
	}
	if _, err := proof.Snapshot(); err == nil {
		t.Fatal("failed decoding retained native readiness")
	}
	if reflect.TypeOf(proof).ConvertibleTo(reflect.TypeOf(WritableCandidateRunningObservation{})) || reflect.TypeOf(proof).ConvertibleTo(reflect.TypeOf(WritableCandidateRecoveryObservation{})) {
		t.Fatal("readiness proof convertible to process evidence")
	}
}
