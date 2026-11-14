//go:build linux

package driver

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func fixtureContainerPost(
	t *testing.T,
	ctx context.Context,
	client *http.Client,
	path string,
	out any,
) {
	t.Helper()
	request, err := http.NewRequestWithContext(
		ctx, http.MethodPost,
		"http://portal-fixture.invalid"+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		t.Fatalf("fixture bound request %s: %d %s",
			path, response.StatusCode, body)
	}
	if err := json.NewDecoder(
		io.LimitReader(response.Body, 65536),
	).Decode(out); err != nil {
		t.Fatal(err)
	}
}

func nativeContainerLogBytes(
	t *testing.T,
	logPath string,
) int64 {
	t.Helper()
	matches, err := filepath.Glob(logPath + "*")
	if err != nil || len(matches) == 0 {
		t.Fatalf("fixture log files: %v %v", matches, err)
	}
	var total int64
	for _, match := range matches {
		info, err := os.Lstat(match)
		if err != nil || !info.Mode().IsRegular() {
			t.Fatalf("fixture log is not a regular file: %s %v",
				match, err)
		}
		total += info.Size()
	}
	return total
}

func verifyNativeCandidateResourceBounds(
	t *testing.T,
	ctx context.Context,
	daemon *http.Client,
	nativeID, socket string,
) {
	t.Helper()
	transport := &http.Transport{
		Proxy:             nil,
		DisableKeepAlives: true,
		DialContext: func(
			ctx context.Context, _, _ string,
		) (net.Conn, error) {
			var dialer net.Dialer
			return dialer.DialContext(ctx, "unix", socket)
		},
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{
		Transport: transport,
		Timeout:   10 * time.Second,
	}

	// The daemon reports the shares the runner set, and runc must have converted them to a cgroup v2
	// cpu.weight below the default of 100. The hard quota is checked by the limits reader.
	var weight struct {
		HostConfig struct{ CpuShares int64 }
		State      struct{ Pid int }
	}
	if err := readJSON(
		ctx, daemon,
		"/v"+APIVersion+"/containers/"+nativeID+"/json",
		&weight,
	); err != nil || weight.HostConfig.CpuShares != 256 || weight.State.Pid <= 0 {
		t.Fatalf("native container CpuShares = %d pid %d, want 256: %v",
			weight.HostConfig.CpuShares, weight.State.Pid, err)
	}
	cgroupLine, err := os.ReadFile(
		fmt.Sprintf("/proc/%d/cgroup", weight.State.Pid))
	if err != nil {
		t.Fatal(err)
	}
	var cgroupPath string
	for _, line := range strings.Split(string(cgroupLine), "\n") {
		if rest, ok := strings.CutPrefix(line, "0::"); ok {
			cgroupPath = rest
		}
	}
	if !strings.HasPrefix(cgroupPath, "/") {
		t.Fatalf("container is not in a cgroup v2 hierarchy: %q", cgroupLine)
	}
	weightRaw, err := os.ReadFile(
		filepath.Join("/sys/fs/cgroup", cgroupPath, "cpu.weight"))
	if err != nil {
		t.Fatal(err)
	}
	cpuWeight, err := strconv.Atoi(strings.TrimSpace(string(weightRaw)))
	if err != nil || cpuWeight < 1 || cpuWeight >= 100 {
		t.Fatalf("cgroup cpu.weight = %q, want below the default of 100: %v",
			weightRaw, err)
	}
	t.Logf("NATIVE_APP_CPU_WEIGHT cpu.weight=%d (CpuShares=256)", cpuWeight)
	t.Log("NATIVE_APP_CPU_WEIGHT_LOW")

	var tmp struct {
		Bytes     int64 `json:"bytes"`
		ENOSPC    bool  `json:"enospc"`
		Recovered bool  `json:"recovered"`
	}
	fixtureContainerPost(
		t, ctx, client, "/fixture/tmp-exhaust", &tmp)
	if tmp.Bytes < 8<<20 || tmp.Bytes > 16<<20 ||
		!tmp.ENOSPC || !tmp.Recovered {
		t.Fatalf("tmpfs exhaustion/cleanup outside its bound: %+v", tmp)
	}
	t.Log("NATIVE_TMP_VOLUME_BOUNDED_AND_RECOVERED")

	var logs struct {
		Bytes int64 `json:"bytes"`
	}
	fixtureContainerPost(
		t, ctx, client, "/fixture/log-flood", &logs)
	if logs.Bytes != 8<<20 {
		t.Fatalf("log flood did not write its bounded fixture: %+v", logs)
	}
	var inspection struct {
		LogPath string `json:"LogPath"`
	}
	if err := readJSON(
		ctx, daemon,
		"/v"+APIVersion+"/containers/"+nativeID+"/json",
		&inspection,
	); err != nil || !filepath.IsAbs(inspection.LogPath) {
		t.Fatalf("fixture log path unavailable: %q %v",
			inspection.LogPath, err)
	}
	var written int64
	deadline := time.Now().Add(2 * time.Second)
	for {
		written = nativeContainerLogBytes(t, inspection.LogPath)
		if written >= 1<<20 || !time.Now().Before(deadline) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if written < 1<<20 || written > 3<<20 {
		t.Fatalf("json-file log bound not enforced: %d", written)
	}
	t.Log("NATIVE_LOG_VOLUME_BOUNDED")
}
