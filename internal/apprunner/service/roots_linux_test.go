//go:build linux

package service

import (
	"bytes"
	"errors"
	"log"
	"strings"
	"testing"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/rootfs"
)

func withInspector(t *testing.T, fake func(string) (rootfs.Info, error)) {
	t.Helper()
	previous := inspectRoot
	inspectRoot = fake
	t.Cleanup(func() { inspectRoot = previous })
}

func TestRootsOnExt4AreRefusedWithTheRecipeAndNoPath(t *testing.T) {
	withInspector(t, func(string) (rootfs.Info, error) { return rootfs.Info{Kind: "ext4"}, nil })
	var out bytes.Buffer
	err := requireXFSRoots(log.New(&out, "", 0), Config{DataRoot: "/fixture-sensitive/data", CheckpointRoot: "/fixture-sensitive/checkpoints"})
	text := out.String()
	if !errors.Is(err, ErrNotReady) {
		t.Fatalf("an ext4 root was accepted: %v", err)
	}
	for _, want := range []string{"data_root is on ext4", "checkpoint_root is on ext4", "provision-xfs-images", "static apps are unaffected", "182-cross-platform-backend-runtime.md"} {
		if !strings.Contains(text, want) {
			t.Fatalf("operator message lacks %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "fixture-sensitive") {
		t.Fatalf("a configured path was logged:\n%s", text)
	}
}

func TestRootsOnXFSPassAndAreAdvertised(t *testing.T) {
	withInspector(t, func(path string) (rootfs.Info, error) {
		if strings.HasSuffix(path, "data") {
			return rootfs.Info{Kind: "xfs", LoopBacked: true, BackingKind: "ext4"}, nil
		}
		return rootfs.Info{Kind: "xfs"}, nil
	})
	var out bytes.Buffer
	if err := requireXFSRoots(log.New(&out, "", 0), Config{DataRoot: "/r/data", CheckpointRoot: "/r/checkpoints"}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"data_root filesystem: xfs loop-backed on ext4", "checkpoint_root filesystem: xfs\n"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("diagnostics lack %q:\n%s", want, out.String())
		}
	}
}

func TestOneNonXFSRootRefusesAndAnUnreadableRootIsLeftToTheDataLayer(t *testing.T) {
	withInspector(t, func(path string) (rootfs.Info, error) {
		if strings.HasSuffix(path, "data") {
			return rootfs.Info{}, errors.New("missing")
		}
		return rootfs.Info{Kind: "btrfs"}, nil
	})
	var out bytes.Buffer
	err := requireXFSRoots(log.New(&out, "", 0), Config{DataRoot: "/r/data", CheckpointRoot: "/r/checkpoints"})
	if !errors.Is(err, ErrNotReady) || !strings.Contains(out.String(), "checkpoint_root is on btrfs") || strings.Contains(out.String(), "data_root is on") {
		t.Fatalf("unexpected result: %v\n%s", err, out.String())
	}
}
