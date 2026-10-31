package driver

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestWritableRegistrationOpaqueProofCannotBeDecoded(t *testing.T) {
	var proof WritableBackendRegistrationObservation
	if _, err := proof.Snapshot(); err == nil {
		t.Fatal("zero native file proof")
	}
	if _, err := json.Marshal(proof); err == nil {
		t.Fatal("native file proof serialized")
	}
	if err := json.Unmarshal([]byte(`{"valid":true}`), &proof); err == nil {
		t.Fatal("native file proof decoded")
	}
	if _, err := proof.Snapshot(); err == nil {
		t.Fatal("failed decoding retained proof")
	}
	if reflect.TypeOf(proof).ConvertibleTo(reflect.TypeOf(WritableCandidateReadinessObservation{})) || reflect.TypeOf(proof).ConvertibleTo(reflect.TypeOf(WritableCandidateRunningObservation{})) {
		t.Fatal("file proof convertible to process authority")
	}
}
