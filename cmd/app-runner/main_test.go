package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestExplicitSocketAndClosedCommandSurface(t *testing.T) {
	for _, args := range [][]string{nil, {"serve"}, {"inspect-daemon"}, {"inspect-daemon", "--socket", "x", "extra"}, {"inspect-daemon", "--token", "fixture-sensitive"}} {
		var out bytes.Buffer
		if code := run(context.Background(), args, &out); code != 2 || strings.Contains(out.String(), "fixture-sensitive") {
			t.Fatalf("unsafe command parse: code=%d output=%s", code, out.String())
		}
	}
}

func TestInspectionFailureIsJSONAndNeverReady(t *testing.T) {
	t.Setenv("DOCKER_HOST", "http://fixture-sensitive.invalid")
	var out bytes.Buffer
	if code := run(context.Background(), []string{"inspect-daemon", "--socket", "relative.fixture-sensitive"}, &out); code != 1 {
		t.Fatalf("invalid inspection succeeded: %d", code)
	}
	var body struct {
		Report struct {
			ExecutionReady bool `json:"execution_ready"`
		} `json:"report"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal(out.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Report.ExecutionReady || body.Error == "" || strings.Contains(out.String(), "fixture-sensitive") {
		t.Fatalf("unsafe diagnostics: %s", out.String())
	}
}
