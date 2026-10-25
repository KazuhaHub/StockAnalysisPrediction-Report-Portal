// Package driver contains the bounded runtime adapter. Metadata inspection does
// not enroll a runner, admit a workload or prove isolation enforcement.
package driver

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"runtime"
	"strings"
	"time"
)

const Profile = "linux-docker-rootful-projectquota/v1-dev"
const APIVersion = "1.47"
const probeTimeout = 5 * time.Second

var (
	ErrUnsupported     = errors.New("unsupported application runtime host")
	ErrUnsafeSocket    = errors.New("unsafe application runtime socket")
	ErrUnavailable     = errors.New("application runtime unavailable")
	ErrInvalidResponse = errors.New("invalid application runtime response")
	ErrAccessDenied    = errors.New("application runtime socket access denied")
)

var securityOption = regexp.MustCompile(`^name=[a-z][a-z0-9_-]*(,[a-z][a-z0-9_-]*=[A-Za-z0-9._/-]+)*$`)

type Finding struct {
	Code  string `json:"code"`
	State string `json:"state"`
}

type Report struct {
	Profile             string    `json:"profile"`
	API                 string    `json:"api"`
	EngineVersion       string    `json:"engine_version,omitempty"`
	Platform            string    `json:"platform,omitempty"`
	ReportedCPUs        int       `json:"reported_cpus,omitempty"`
	ReportedMemoryBytes int64     `json:"reported_memory_bytes,omitempty"`
	MetadataCompatible  bool      `json:"metadata_compatible"`
	ExecutionReady      bool      `json:"execution_ready"`
	Findings            []Finding `json:"findings"`
}

func initialReport() Report {
	r := Report{Profile: Profile, API: APIVersion, Findings: []Finding{}}
	for _, code := range []string{"hard_quota_enforcement", "sandbox_enforcement", "available_capacity", "controller_authentication"} {
		r.Findings = append(r.Findings, Finding{Code: code, State: "unverified"})
	}
	return r
}

// InspectDaemon contacts only the explicitly configured local socket, on Linux.
// It performs two read-only Docker requests, never container/image/volume actions.
// A successful inspection still returns ExecutionReady=false until the missing
// authenticated enforcement/capacity admission is implemented and verified.
func InspectDaemon(ctx context.Context, socket string) (Report, error) {
	r := initialReport()
	if runtime.GOOS != "linux" || (runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64") {
		r.Findings = append(r.Findings, Finding{"host_profile_unsupported", "unsupported"})
		return r, ErrUnsupported
	}
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	transport := &http.Transport{Proxy: nil, DisableKeepAlives: true, ResponseHeaderTimeout: time.Second,
		DialContext: dockerDialer(socket)}
	defer transport.CloseIdleConnections()
	client := daemonClient(transport)
	return inspectHTTP(ctx, client, runtime.GOARCH)
}

func daemonClient(transport http.RoundTripper) *http.Client {
	return &http.Client{Transport: transport, Timeout: probeTimeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

type versionResponse struct {
	Version       string
	APIVersion    string `json:"ApiVersion"`
	MinAPIVersion string
	OS            string `json:"Os"`
	Arch          string
}
type infoResponse struct {
	OSType, Architecture, ServerVersion, Driver, CgroupVersion, CgroupDriver string
	MemoryLimit, SwapLimit, CpuCfsPeriod, CpuCfsQuota, PidsLimit             bool
	NCPU                                                                     int
	MemTotal                                                                 int64
	SecurityOptions                                                          []string
}

func canonicalArch(value string) string {
	switch value {
	case "amd64", "x86_64":
		return "amd64"
	case "arm64", "aarch64":
		return "arm64"
	}
	return ""
}

func assess(v versionResponse, info infoResponse, hostArch string) Report {
	r := initialReport()
	r.EngineVersion = v.Version
	if (info.OSType == "linux" || info.OSType == "windows") && canonicalArch(info.Architecture) != "" {
		r.Platform = info.OSType + "/" + canonicalArch(info.Architecture)
	}
	r.ReportedCPUs, r.ReportedMemoryBytes = info.NCPU, info.MemTotal
	r.MetadataCompatible = true
	check := func(code string, ok bool) {
		state := "supported"
		if !ok {
			state = "unsupported"
			r.MetadataCompatible = false
		}
		r.Findings = append(r.Findings, Finding{code, state})
	}
	check("native_linux_architecture", v.OS == "linux" && info.OSType == "linux" && canonicalArch(v.Arch) != "" && canonicalArch(v.Arch) == canonicalArch(info.Architecture) && canonicalArch(v.Arch) == hostArch)
	check("cgroup_v2", info.CgroupVersion == "2" && (info.CgroupDriver == "systemd" || info.CgroupDriver == "cgroupfs"))
	check("memory_and_swap_limits", info.MemoryLimit && info.SwapLimit)
	check("cpu_quota_limits", info.CpuCfsPeriod && info.CpuCfsQuota)
	check("pid_limits", info.PidsLimit)
	check("image_storage_profile", info.Driver == "overlay2" || info.Driver == "overlayfs")
	rootless, userns, seccomp, valid := false, false, false, len(info.SecurityOptions) <= 32
	seen := map[string]bool{}
	for _, option := range info.SecurityOptions {
		family, _, _ := strings.Cut(option, ",")
		if len(option) > 256 || seen[family] || !securityOption.MatchString(option) {
			valid = false
		}
		seen[family] = true
		switch family {
		case "name=rootless":
			rootless = true
		case "name=userns":
			userns = true
		case "name=seccomp":
			seccomp = option == "name=seccomp,profile=default" || option == "name=seccomp,profile=builtin"
		}
	}
	check("rootful_profile", !rootless)
	check("unmapped_user_profile", !userns)
	check("builtin_seccomp_profile", valid && seccomp)
	// These flags are daemon claims. None changes ExecutionReady to true.
	return r
}
