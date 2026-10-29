package transport

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"
)

func TestNetworkRuntimeProfileCanonicalAndBounded(t *testing.T) {
	_, _, _, a, _ := networkSessionFixture(t)
	c := networkEnrollmentFixture{a}
	d, _ := c.ControllerDeployment(context.Background())
	raw, err := encodeNetworkRuntime(d.ControllerEnrollment, 2, 3, false)
	if err != nil {
		t.Fatal(err)
	}
	controller, profile, err := decodeNetworkRuntime(raw)
	if err != nil || controller.Fingerprint != a.Fingerprint || profile.ActiveRequests != 2 || profile.CacheEntries != 3 || profile.WritableBackend || profile.MaxRequestBytes != 65536 || profile.MaxResponseBytes != 1048576 {
		t.Fatalf("%+v %v", profile, err)
	}
	for _, bad := range [][]byte{append(raw, ' '), append(raw, []byte(`{}`)...), bytes.Replace(raw, []byte(`"active_requests":2`), []byte(`"active_requests":2,"active_requests":2`), 1), bytes.Replace(raw, []byte(`"active_requests":2`), []byte(`"active_requests":9`), 1), bytes.Replace(raw, []byte(`"max_request_bytes":65536`), []byte(`"max_request_bytes":65537`), 1), bytes.Replace(raw, []byte(NetworkRuntimeFormat), []byte("portal.runner.network-runtime/unknown"), 1), bytes.Repeat([]byte("x"), 4097)} {
		if _, _, err := decodeNetworkRuntime(bad); err == nil {
			t.Fatal("unknown/noncanonical runtime admitted")
		}
	}
	if _, err := encodeNetworkRuntime(d.ControllerEnrollment, 0, 3, false); err == nil {
		t.Fatal("invalid limit")
	}
	original := NetworkRuntimeObservation{controller: d.ControllerEnrollment, profile: profile}
	if _, err := json.Marshal(original); err == nil {
		t.Fatal("observation serialized")
	}
	copy := original
	if json.Unmarshal([]byte(`{}`), &copy) == nil || copy.profile.ActiveRequests != 0 {
		t.Fatal("observation reconstructed")
	}
	returned := original.Controller()
	returned.PublicKey[0] ^= 1
	if bytes.Equal(returned.PublicKey, original.Controller().PublicKey) {
		t.Fatal("key alias")
	}
}
