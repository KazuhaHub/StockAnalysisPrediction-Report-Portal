package driver

import (
	"encoding/json"
	"testing"
)

func TestWritableBackendRequestCannotImportNativeAuthority(t *testing.T) {
	var o WritableBackendRequestObservation
	if _, err := o.Snapshot(); err == nil {
		t.Fatal("zero request provenance accepted")
	}
	if _, err := json.Marshal(o); err == nil {
		t.Fatal("request provenance serialized")
	}
	if err := json.Unmarshal([]byte(`{"valid":true}`), &o); err == nil {
		t.Fatal("request provenance imported")
	}
	if _, err := o.Snapshot(); err == nil {
		t.Fatal("decode retained authority")
	}
}
