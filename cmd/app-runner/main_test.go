package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestExplicitSocketAndClosedCommandSurface(t *testing.T) {
	for _, args := range [][]string{nil, {"serve"}, {"serve", "--config"}, {"bootstrap"}, {"inspect-daemon"}, {"inspect-daemon", "--socket", "x", "extra"}, {"inspect-daemon", "--token", "fixture-sensitive"}} {
		var out bytes.Buffer
		if code := run(context.Background(), args, &out); code != 2 || strings.Contains(out.String(), "fixture-sensitive") {
			t.Fatalf("unsafe command parse: code=%d output=%s", code, out.String())
		}
	}
}

func TestServiceCommandsRefusePrivatePathDiagnostics(t *testing.T) {
	for _, command := range []string{"bootstrap", "serve"} {
		var out bytes.Buffer
		if code := run(context.Background(), []string{command, "--config", "/fixture-sensitive/config.json"}, &out); code != 1 {
			t.Fatalf("%s accepted missing config: %d", command, code)
		}
		if strings.Contains(out.String(), "fixture-sensitive") || (command == "bootstrap" && !strings.Contains(out.String(), "configuration is invalid")) {
			t.Fatalf("%s leaked path or omitted refusal: %s", command, out.String())
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
