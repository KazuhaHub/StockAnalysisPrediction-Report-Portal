package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestQuoteProbeDispatchNeverReadsConfigOrStartsServer(t *testing.T) {
	mkfifo, err := exec.LookPath("mkfifo")
	if err != nil {
		t.Skip("named-pipe guard requires mkfifo")
	}
	dir := t.TempDir()
	config := filepath.Join(dir, "config.yaml")
	if body, err := exec.Command(mkfifo, config).CombinedOutput(); err != nil {
		t.Fatalf("create config read tripwire: %s %v", body, err)
	}
	binary := filepath.Join(dir, "report-portal")
	build := exec.Command("go", "build", "-o", binary, ".")
	if body, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build dispatch guard: %s %v", body, err)
	}
	for _, test := range []struct {
		args    []string
		success bool
		want    string
	}{
		{[]string{"quote-probe", "--help"}, true, "usage:"},
		{[]string{"quote-probe", "capture", "--help"}, true, "new private capture"},
		{[]string{"quote-probe", "compare", "--help"}, true, "replay is offline"},
		{[]string{"quote-probe"}, false, "quote-probe: usage:"},
		{[]string{"quote-probe", "capture", "--out", filepath.Join(dir, "capture")}, false, "bounded valid window"},
		{[]string{"quote-probe", "compare", "--in", filepath.Join(dir, "missing")}, false, "quote-probe:"},
	} {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		command := exec.CommandContext(ctx, binary, test.args...)
		command.Env = append(os.Environ(), "RP_CONFIG="+config)
		body, err := command.CombinedOutput()
		cancel()
		if ctx.Err() == context.DeadlineExceeded {
			t.Fatalf("command opened config pipe or started server: %v", test.args)
		}
		if (err == nil) != test.success || !strings.Contains(string(body), test.want) {
			t.Fatalf("dispatch %v: %s %v", test.args, body, err)
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 2 {
		t.Fatal("dispatch created unexpected state")
	}
}
