package main

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/hostns"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/hostsetup"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/version"
)

// `version` prints exactly what report-portal prints, so the release workflow can compare it with the identity
// setup stamped. It takes no arguments.
func TestVersionPrintsTheStampedIdentity(t *testing.T) {
	oldVersion, oldCommit, oldDate := version.Version, version.Commit, version.BuildDate
	defer func() { version.Version, version.Commit, version.BuildDate = oldVersion, oldCommit, oldDate }()
	version.Version, version.Commit, version.BuildDate = "v2026.47.1", "abcdef0", "2026-11-20T00:00:00Z"
	var out bytes.Buffer
	if code := run(context.Background(), []string{"version"}, &out); code != 0 || out.String() != "2026.47.1 (abcdef0, 2026-11-20T00:00:00Z)\n" {
		t.Fatalf("version: %d %q", code, out.String())
	}
	out.Reset()
	if code := run(context.Background(), []string{"version", "extra"}, &out); code != 2 {
		t.Fatalf("version with an argument: %d", code)
	}
}

// health needs an explicit configuration and never prints a configured path.
func TestHealthCommandSurface(t *testing.T) {
	for _, args := range [][]string{{"health"}, {"health", "--config"}, {"health", "--config", "x", "extra"}, {"health", "--token", "fixture-sensitive"}} {
		var out bytes.Buffer
		if code := run(context.Background(), args, &out); code != 2 || strings.Contains(out.String(), "fixture-sensitive") {
			t.Fatalf("%v: code=%d output=%s", args, code, out.String())
		}
	}
	var out bytes.Buffer
	if code := run(context.Background(), []string{"health", "--config", "/fixture-sensitive/service.json"}, &out); code != 1 {
		t.Fatalf("a missing configuration is healthy: %d", code)
	}
	if strings.Contains(out.String(), "fixture-sensitive") || !strings.Contains(out.String(), "unhealthy") {
		t.Fatalf("health output: %q", out.String())
	}
}

func TestHostSetupFlagsAreStrict(t *testing.T) {
	system := func() (hostsetup.Env, error) {
		t.Fatal("the host was touched by a usage error")
		return hostsetup.Env{}, nil
	}
	for _, args := range [][]string{
		{"--nope"}, {"extra"}, {"--controller-uid", "0"}, {"--controller-gid", "-1"}, {"--controller-uid", "4294967295"}, {"--controller-uid", "x"},
		{"--data-size", "banana"}, {"--checkpoint-size", "0"}, {"--apply=maybe"},
	} {
		var out bytes.Buffer
		if code := hostSetup(context.Background(), args, &out, system); code != 2 {
			t.Errorf("%v: code %d", args, code)
		}
		if !strings.Contains(out.String(), "usage: app-runner host-setup") {
			t.Errorf("%v: no usage:\n%s", args, out.String())
		}
	}
	var out bytes.Buffer
	if code := hostSetup(context.Background(), []string{"--help"}, &out, system); code != 0 || !strings.Contains(out.String(), "--apply") {
		t.Fatalf("help: %d", code)
	}
}

// host-setup is reachable through run, and a host it cannot describe is a failure, not a pass.
func TestHostSetupThroughRunRefusesWithoutAHost(t *testing.T) {
	var out bytes.Buffer
	if code := run(context.Background(), []string{"host-setup", "--bogus"}, &out); code != 2 {
		t.Fatalf("code %d", code)
	}
	if !strings.Contains(usageLine, "host-setup") || !strings.Contains(usageLine, "health") || !strings.Contains(usageLine, "version") {
		t.Fatalf("the usage line omits a command: %s", usageLine)
	}
}

// The entry's descriptor is consumed before any command runs, and a descriptor that is not the runner's own
// executable stops it with a non-zero status. Fails closed.
func TestStartRefusesAnEntryDescriptorThatIsNotTheExecutable(t *testing.T) {
	t.Setenv(hostns.ExecFDEnv, "not-a-descriptor")
	var out bytes.Buffer
	if code := start(context.Background(), []string{"version"}, &out); code != 1 {
		t.Fatalf("start: %d", code)
	}
	if strings.Contains(out.String(), "(") && strings.Contains(out.String(), ", ") && strings.Contains(out.String(), "dev") {
		t.Fatalf("the command ran: %q", out.String())
	}
	if _, set := os.LookupEnv(hostns.ExecFDEnv); set {
		t.Fatal("the variable survived")
	}
	// Without the variable the command runs as before.
	out.Reset()
	if code := start(context.Background(), []string{"version"}, &out); code != 0 {
		t.Fatalf("start without the variable: %d", code)
	}
}
