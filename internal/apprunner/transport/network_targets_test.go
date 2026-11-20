package transport

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/journal"
)

func targetsNetworks() []appplatform.PrivateNetwork {
	return []appplatform.PrivateNetwork{
		{Prefix: "192.168.1.0/24", Ports: []int{80}, PlainHTTP: true},
		{Prefix: "10.20.0.0/24", Ports: []int{443, 8080}},
	}
}

func TestNetworkTargetsCodecIsCanonicalAndClosed(t *testing.T) {
	empty, err := NewNetworkTargets(nil)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := EncodeNetworkTargets(empty)
	if err != nil {
		t.Fatal(err)
	}
	emptySHA, _ := appplatform.PrivateNetworksSHA256(nil)
	want := `{"format":"portal.runner.network-targets/1-dev.1","policy_formats":["portal.http-policy/1-dev.1","portal.http-policy/2-dev.1"],"private_networks":[],"private_networks_sha256":"` + emptySHA + `"}`
	if string(raw) != want {
		t.Fatalf("%s", raw)
	}
	got, err := ParseNetworkTargets(raw)
	if err != nil || got.PrivateNetworksSHA256 != emptySHA || len(got.PrivateNetworks) != 0 {
		t.Fatalf("%+v %v", got, err)
	}

	targets, err := NewNetworkTargets(targetsNetworks())
	if err != nil {
		t.Fatal(err)
	}
	if targets.PrivateNetworks[0].Prefix != "10.20.0.0/24" {
		t.Fatal("entries are not sorted by prefix")
	}
	raw, err = EncodeNetworkTargets(targets)
	if err != nil || len(raw) > 4096 {
		t.Fatalf("%d %v", len(raw), err)
	}
	sha, _ := appplatform.PrivateNetworksSHA256(targetsNetworks())
	if targets.PrivateNetworksSHA256 != sha {
		t.Fatal("digest is not the allowlist digest")
	}
	back, err := ParseNetworkTargets(raw)
	if err != nil || back.PrivateNetworksSHA256 != sha || len(back.PrivateNetworks) != 2 {
		t.Fatalf("%+v %v", back, err)
	}
	again, _ := EncodeNetworkTargets(back)
	if !bytes.Equal(again, raw) {
		t.Fatal("round trip changed bytes")
	}

	good := string(raw)
	other := strings.Repeat("e", 64)
	bad := map[string]string{
		"empty":             ``,
		"empty object":      `{}`,
		"trailing space":    good + ` `,
		"trailing object":   good + `{}`,
		"extra key":         strings.Replace(good, `"private_networks_sha256"`, `"extra":1,"private_networks_sha256"`, 1),
		"duplicate key":     strings.Replace(good, `"private_networks_sha256"`, `"private_networks_sha256":"`+other+`","private_networks_sha256"`, 1),
		"entry extra key":   strings.Replace(good, `"plain_http":false`, `"plain_http":false,"x":1`, 1),
		"wrong format":      strings.Replace(good, `network-targets/1-dev.1`, `network-targets/2-dev.1`, 1),
		"missing v2":        strings.Replace(good, `,"portal.http-policy/2-dev.1"`, ``, 1),
		"reordered formats": strings.Replace(good, `["portal.http-policy/1-dev.1","portal.http-policy/2-dev.1"]`, `["portal.http-policy/2-dev.1","portal.http-policy/1-dev.1"]`, 1),
		"wrong digest":      strings.Replace(good, sha, other, 1),
		"unsorted entries":  strings.Replace(strings.Replace(strings.Replace(good, `"10.20.0.0/24"`, `"X"`, 1), `"192.168.1.0/24"`, `"10.20.0.0/24"`, 1), `"X"`, `"192.168.1.0/24"`, 1),
		"public prefix":     strings.Replace(good, `"10.20.0.0/24"`, `"8.8.0.0/16"`, 1),
		"null networks":     strings.Replace(good, `"private_networks":[`, `"private_networks":null,"x":[`, 1),
		"invalid utf8":      "\xff",
		"oversize":          string(bytes.Repeat([]byte(" "), 4097)),
	}
	for name, input := range bad {
		if _, err := ParseNetworkTargets([]byte(input)); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	if _, err := NewNetworkTargets([]appplatform.PrivateNetwork{{Prefix: "8.8.0.0/16", Ports: []int{443}}}); err == nil {
		t.Fatal("an invalid allowlist was advertised")
	}
	if _, err := EncodeNetworkTargets(NetworkTargets{Format: NetworkTargetsFormat}); err == nil {
		t.Fatal("an incomplete advertisement was encoded")
	}
}

func TestNetworkTargetsFitsFourKiBAtTheMaximum(t *testing.T) {
	var networks []appplatform.PrivateNetwork
	ports := make([]int, 16)
	for i := range ports {
		ports[i] = 40000 + i
	}
	for i := 0; i < 16; i++ {
		networks = append(networks, appplatform.PrivateNetwork{Prefix: fmt.Sprintf("fd00:1234:5678:%x::/128", i+1), Ports: ports, PlainHTTP: true})
	}
	targets, err := NewNetworkTargets(networks)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := EncodeNetworkTargets(targets); err != nil {
		t.Fatalf("the largest valid allowlist does not fit: %v", err)
	}
}

// Doc 174 F1: the network runtime profile is compared byte for byte with the
// stored plan, so network targets are a separate query and these bytes are
// pinned. A new field here would break foreground for every installation.
func TestNetworkRuntimeBytesAreUnchangedByNetworkTargets(t *testing.T) {
	key := bytes.Repeat([]byte{1}, 32)
	sum := sha256.Sum256(key)
	enrollment := journal.ControllerEnrollment{DeploymentID: "10000000-0000-4000-8000-000000000001", ControllerEpoch: "20000000-0000-4000-8000-000000000001", ControlRevision: 1, PublicKey: key, Fingerprint: hex.EncodeToString(sum[:])}
	raw, err := encodeNetworkRuntime(enrollment, 2, 3, false)
	if err != nil {
		t.Fatal(err)
	}
	const wantRuntime = `{"controller":{"format":"portal.runner.enrollment/1-dev.1","deployment_id":"10000000-0000-4000-8000-000000000001","controller_epoch":"20000000-0000-4000-8000-000000000001","control_revision":"1","public_key":"AQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQE","fingerprint":"72cd6e8422c407fb6d098690f1130b7ded7ec2f7f5e1d30bd9d521f015363793"},"profile":{"format":"portal.runner.network-runtime/1-dev.1","request_format":"portal.runner.viewer-http-request/1-dev.1","confirmation_format":"portal.runner.viewer-http-check/1-dev.1","policy_format":"portal.http-policy/1-dev.1","max_request_bytes":65536,"max_response_bytes":1048576,"max_deadline_ms":15000,"max_concurrency":4,"active_requests":2,"cache_entries":3,"writable_backend":false,"stream_format":"portal.http-stream-policy/1-dev.1","max_stream_bytes":4194304,"max_stream_frame_bytes":32768,"max_stream_duration_ms":30000,"max_stream_idle_ms":15000}}`
	if string(raw) != wantRuntime {
		t.Fatalf("encodeNetworkRuntime bytes changed:\n%s", raw)
	}
	profile, err := json.Marshal(networkRuntimeProfile(2, 3, false))
	if err != nil {
		t.Fatal(err)
	}
	const wantProfile = `{"format":"portal.runner.network-runtime/1-dev.1","request_format":"portal.runner.viewer-http-request/1-dev.1","confirmation_format":"portal.runner.viewer-http-check/1-dev.1","policy_format":"portal.http-policy/1-dev.1","max_request_bytes":65536,"max_response_bytes":1048576,"max_deadline_ms":15000,"max_concurrency":4,"active_requests":2,"cache_entries":3,"writable_backend":false,"stream_format":"portal.http-stream-policy/1-dev.1","max_stream_bytes":4194304,"max_stream_frame_bytes":32768,"max_stream_duration_ms":30000,"max_stream_idle_ms":15000}`
	if string(profile) != wantProfile {
		t.Fatalf("network profile bytes changed:\n%s", profile)
	}
	if networkRuntimeProfile(2, 3, false).PolicyFormat != appplatform.HTTPPolicyFormat {
		t.Fatal("the profile must keep advertising the v1 policy format")
	}
	for _, word := range []string{"private", "target", "http-policy/2"} {
		if strings.Contains(string(raw), word) {
			t.Fatalf("profile mentions %q", word)
		}
	}
}

func TestNetworkResponseBoundFollowsTheSelectedTarget(t *testing.T) {
	narrow := appplatform.HTTPTarget{ID: "narrow", Origin: "https://api.example.com", Paths: []string{"/v1"}, Methods: []string{"POST"}, Network: "public", ResponseBytes: 100}
	wide := appplatform.HTTPTarget{ID: "wide", Origin: "https://other.example.com", Paths: []string{"/v1"}, Methods: []string{"POST"}, Network: "public"}
	raw, err := appplatform.CanonicalHTTPPolicyV2(appplatform.HTTPPolicyV2{Targets: []appplatform.HTTPTarget{narrow, wide}, RequestBytes: 64, ResponseBytes: 1000, Deadline: 1000000, Concurrency: 1})
	if err != nil {
		t.Fatal(err)
	}
	c := control.NetworkHTTPRequestCommand{PolicyRaw: raw, Method: "POST", URL: "https://api.example.com/v1/x"}
	if got, err := networkResponseBound(c); err != nil || got != 100 {
		t.Fatalf("%d %v", got, err)
	}
	c.URL = "https://other.example.com/v1/x"
	if got, err := networkResponseBound(c); err != nil || got != 1000 {
		t.Fatalf("%d %v", got, err)
	}
	c.URL = "https://unknown.example.com/v1/x"
	if _, err := networkResponseBound(c); err == nil {
		t.Fatal("a URL outside every target has a bound")
	}
	v1, _ := appplatform.CanonicalHTTPPolicy(appplatform.HTTPPolicy{Origin: "https://api.example.com", Paths: []string{"/v1"}, Methods: []string{"POST"}, RequestBytes: 64, ResponseBytes: 64, Deadline: 1000000, Concurrency: 1})
	c = control.NetworkHTTPRequestCommand{PolicyRaw: v1, Method: "POST", URL: "https://api.example.com/v1/x"}
	if got, err := networkResponseBound(c); err != nil || got != 64 {
		t.Fatalf("v1: %d %v", got, err)
	}
}
