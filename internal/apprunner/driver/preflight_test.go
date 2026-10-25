package driver

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func versionFixture() map[string]any {
	return map[string]any{"Version": "28.5.0", "ApiVersion": "1.51", "MinAPIVersion": "1.24", "Os": "linux", "Arch": "amd64", "DockerRootDir": "fixture-sensitive"}
}
func infoFixture() map[string]any {
	return map[string]any{"ServerVersion": "28.5.0", "OSType": "linux", "Architecture": "x86_64", "Driver": "overlay2", "CgroupVersion": "2", "CgroupDriver": "systemd",
		"MemoryLimit": true, "SwapLimit": true, "CpuCfsPeriod": true, "CpuCfsQuota": true, "PidsLimit": true, "NCPU": 2, "MemTotal": int64(1 << 30),
		"SecurityOptions": []string{"name=seccomp,profile=builtin", "name=apparmor"}, "HttpProxy": "fixture-sensitive", "Labels": []string{"fixture-sensitive"}}
}

func fixtureClient(t *testing.T, v, i map[string]any) (*http.Client, *[]string) {
	t.Helper()
	paths := []string{}
	client := &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method != "GET" || r.URL.Host != "app-runner-docker.invalid" || r.URL.RawQuery != "" || r.Body != nil || r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
			t.Fatal("inspection escaped the read-only projection")
		}
		paths = append(paths, r.URL.Path)
		var value any
		switch r.URL.Path {
		case "/version":
			value = v
		case "/v1.47/info":
			value = i
		default:
			t.Fatalf("unexpected Docker action: %s", r.URL.Path)
		}
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(raw))), Header: make(http.Header)}, nil
	})}
	return client, &paths
}

func TestInspectionIsRedactedAndNeverExecutionEvidence(t *testing.T) {
	client, paths := fixtureClient(t, versionFixture(), infoFixture())
	report, err := inspectHTTP(context.Background(), client, "amd64")
	if err != nil {
		t.Fatal(err)
	}
	if !report.MetadataCompatible || report.ExecutionReady || report.Platform != "linux/amd64" || len(*paths) != 2 {
		t.Fatalf("invalid metadata report: %+v %v", report, *paths)
	}
	raw, _ := json.Marshal(report)
	if strings.Contains(string(raw), "fixture-sensitive") {
		t.Fatal("daemon secrets/host configuration leaked into report")
	}
	unverified := 0
	for _, finding := range report.Findings {
		if finding.State == "unverified" {
			unverified++
		}
	}
	if unverified != 4 {
		t.Fatal("metadata silently substituted for actual enforcement")
	}
}

func TestInspectionRefusesIncompatibleDaemonClaims(t *testing.T) {
	for _, tc := range []struct {
		name, key string
		value     any
	}{
		{"memory", "MemoryLimit", false}, {"swap", "SwapLimit", false}, {"cpu", "CpuCfsQuota", false}, {"cpu_period", "CpuCfsPeriod", false}, {"pids", "PidsLimit", false},
		{"cgroup_v1", "CgroupVersion", "1"}, {"unknown_cgroup_driver", "CgroupDriver", "unknown"}, {"driver", "Driver", "unverified"}, {"wrong_arch", "Architecture", "aarch64"},
		{"rootless", "SecurityOptions", []string{"name=seccomp,profile=builtin", "name=rootless"}},
		{"userns", "SecurityOptions", []string{"name=seccomp,profile=builtin", "name=userns"}},
		{"unconfined", "SecurityOptions", []string{"name=seccomp,profile=unconfined"}},
		{"malformed_security", "SecurityOptions", []string{"name=seccomp,profile=builtin", "Name=rootless"}},
		{"ambiguous", "SecurityOptions", []string{"name=seccomp,profile=builtin", "name=seccomp,profile=unconfined"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			info := infoFixture()
			info[tc.key] = tc.value
			client, _ := fixtureClient(t, versionFixture(), info)
			report, err := inspectHTTP(context.Background(), client, "amd64")
			if err != nil {
				t.Fatal(err)
			}
			if report.MetadataCompatible || report.ExecutionReady {
				t.Fatal("unsupported profile admitted")
			}
		})
	}
}

func TestAPIRangeCheckedBeforeVersionedRequests(t *testing.T) {
	for _, bounds := range [][2]string{{"1.48", "1.52"}, {"1.24", "1.46"}} {
		v := versionFixture()
		v["MinAPIVersion"], v["ApiVersion"] = bounds[0], bounds[1]
		client, paths := fixtureClient(t, v, infoFixture())
		report, err := inspectHTTP(context.Background(), client, "amd64")
		if err != nil || report.MetadataCompatible || len(*paths) != 1 {
			t.Fatalf("unsupported API was used: %+v %v %v", report, err, *paths)
		}
	}
}

func TestMalformedAndOversizedResponsesRefused(t *testing.T) {
	v, _ := json.Marshal(versionFixture())
	for _, raw := range []string{"null", "{}", `{"Version":"28.5.0"} {}`, strings.Repeat(" ", maxResponseBytes+1), string(v[:len(v)-1]) + `,"Version":"28.5.0"}`, strings.Replace(string(v), `"Version":`, `"version":`, 1)} {
		client := &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
			if r.URL.Path != "/version" {
				body, _ := json.Marshal(infoFixture())
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(body)))}, nil
			}
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(raw))}, nil
		})}
		if _, err := inspectHTTP(context.Background(), client, "amd64"); !errors.Is(err, ErrInvalidResponse) {
			t.Fatalf("accepted malformed projection: %v", err)
		}
	}
}

func TestDaemonRedirectDoesNotFollowLocation(t *testing.T) {
	calls := 0
	client := daemonClient(transportFunc(func(*http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 302, Header: http.Header{"Location": []string{"http://fixture-sensitive.invalid/"}}, Body: io.NopCloser(strings.NewReader("fixture-sensitive"))}, nil
	}))
	if _, err := inspectHTTP(context.Background(), client, "amd64"); !errors.Is(err, ErrUnavailable) || calls != 1 {
		t.Fatalf("redirect followed: calls=%d err=%v", calls, err)
	}
}

func TestCancellationAndUpstreamErrorsAreBounded(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	client := &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) { return nil, r.Context().Err() })}
	if _, err := inspectHTTP(ctx, client, "amd64"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation lost: %v", err)
	}
	client.Transport = transportFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 500, Body: io.NopCloser(strings.NewReader("fixture-sensitive"))}, nil
	})
	if _, err := inspectHTTP(context.Background(), client, "amd64"); !errors.Is(err, ErrUnavailable) || strings.Contains(err.Error(), "fixture-sensitive") {
		t.Fatalf("unsafe upstream error: %v", err)
	}
}
