package driver

import (
	"encoding/json"
	"testing"
)

func TestWritableServiceTaskCannotImportNativeAuthority(t *testing.T) {
	var o WritableServiceTaskObservation
	if _, err := o.Snapshot(); err == nil {
		t.Fatal("zero task provenance accepted")
	}
	if _, err := json.Marshal(o); err == nil {
		t.Fatal("task provenance serialized")
	}
	if err := json.Unmarshal([]byte(`{"valid":true}`), &o); err == nil {
		t.Fatal("task provenance imported")
	}
	if _, err := o.Snapshot(); err == nil {
		t.Fatal("decode retained authority")
	}
}
