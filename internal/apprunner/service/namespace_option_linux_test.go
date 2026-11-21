//go:build linux && (amd64 || arm64)

package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The default start requires dockerd in the runner's mount AND network namespace; only an explicit option drops
// the network half, and the mount half is required either way.
func TestNamespaceCheckSelection(t *testing.T) {
	oldFull, oldMount := checkDaemonNamespaces, checkDaemonMountNamespace
	defer func() { checkDaemonNamespaces, checkDaemonMountNamespace = oldFull, oldMount }()
	var called []string
	refuse := errors.New("refused")
	checkDaemonNamespaces = func(context.Context, string) error { called = append(called, "full"); return refuse }
	checkDaemonMountNamespace = func(context.Context, string) error { called = append(called, "mount"); return nil }

	if err := runNamespaceCheck(context.Background(), "s", ServeOptions{}); !errors.Is(err, refuse) || strings.Join(called, ",") != "full" {
		t.Fatalf("default: %v %v", err, called)
	}
	called = nil
	if err := runNamespaceCheck(context.Background(), "s", ServeOptions{SeparateNetworkNamespace: true}); err != nil || strings.Join(called, ",") != "mount" {
		t.Fatalf("option: %v %v", err, called)
	}
	// The mount half still refuses with the option.
	checkDaemonMountNamespace = func(context.Context, string) error { return refuse }
	if err := runNamespaceCheck(context.Background(), "s", ServeOptions{SeparateNetworkNamespace: true}); !errors.Is(err, refuse) {
		t.Fatalf("the mount namespace was not required: %v", err)
	}
}

// The option exists for the network acceptance's isolated runner only: the production command never sets it.
func TestTheCommandNeverSetsTheSeparateNetworkNamespaceOption(t *testing.T) {
	files, err := filepath.Glob("../../../cmd/app-runner/*.go")
	if err != nil || len(files) == 0 {
		t.Fatalf("no command sources: %v", err)
	}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), "SeparateNetworkNamespace") || strings.Contains(string(raw), "ServeWith") {
			t.Errorf("%s uses the separate network namespace option", f)
		}
	}
}
