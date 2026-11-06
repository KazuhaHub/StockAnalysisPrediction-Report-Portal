package driver

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestWritableHandshakeOpaqueProofCannotBeDecoded(t *testing.T) {
	var proof WritableBackendHandshakeObservation
	if _, err := proof.Snapshot(); err == nil {
		t.Fatal("zero handshake proof")
	}
	if _, err := json.Marshal(proof); err == nil {
		t.Fatal("handshake proof serialized")
	}
	if err := json.Unmarshal([]byte(`{"valid":true}`), &proof); err == nil {
		t.Fatal("handshake proof decoded")
	}
	if _, err := proof.Snapshot(); err == nil {
		t.Fatal("failed decoding retained handshake proof")
	}
	if reflect.TypeOf(proof).ConvertibleTo(reflect.TypeOf(WritableCandidateRunningObservation{})) || reflect.TypeOf(proof).ConvertibleTo(reflect.TypeOf(WritableBackendRegistrationObservation{})) {
		t.Fatal("handshake convertible to durable effect proof")
	}
}
