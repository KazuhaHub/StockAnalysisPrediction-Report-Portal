//go:build linux && (amd64 || arm64)

package app

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"strconv"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
)

type platformCandidateFixtureMessage struct{ Raw, Signature []byte }

func readPlatformFixtureCandidate(t *testing.T, path string, a control.Authority) platformCandidateFixtureMessage {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil || len(raw) > 16384 {
		t.Fatal("fixture public candidate command", err)
	}
	var message platformCandidateFixtureMessage
	var c control.CandidateCommand
	if json.Unmarshal(raw, &message) != nil || json.Unmarshal(message.Raw, &c) != nil {
		t.Fatal("invalid fixture command")
	}
	issued, err := strconv.ParseInt(c.IssuedMS, 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	// Cleanup can follow an expired test deadline. Authenticate the historical
	// public intent at issuance, then require every actual native ownership label.
	if _, err := control.VerifyCandidate(message.Raw, message.Signature, a, issued); err != nil {
		t.Fatal("fixture signature", err)
	}
	return message
}

func checkPlatformFixtureCandidate(t *testing.T, path string, a control.Authority, socket string, cleanup bool) string {
	t.Helper()
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return ""
	}
	message := readPlatformFixtureCandidate(t, path, a)
	var c control.CandidateCommand
	if json.Unmarshal(message.Raw, &c) != nil {
		t.Fatal("fixture candidate missing")
	}
	run := func(args ...string) []byte {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		out, err := exec.CommandContext(ctx, "docker", append([]string{"--host", "unix://" + socket}, args...)...).CombinedOutput()
		if err != nil || len(out) > 65536 {
			t.Fatal("owned fixture candidate inspection", err)
		}
		return out
	}
	ids := bytes.Fields(run("container", "ls", "-a", "--no-trunc", "--filter", "label=org.portal.app.resource="+c.Payload.ResourceID, "--format", "{{.ID}}"))
	if len(ids) == 0 {
		return ""
	}
	if len(ids) != 1 || len(ids[0]) != 64 {
		t.Fatal("ambiguous fixture candidate")
	}
	id := string(ids[0])
	var actual []struct {
		ID     string `json:"Id"`
		Config struct{ Labels map[string]string }
		State  struct{ Running bool }
	}
	if err := json.Unmarshal(run("container", "inspect", id), &actual); err != nil || len(actual) != 1 || actual[0].ID != id {
		t.Fatal("fixture container identity", err)
	}
	for name, value := range map[string]string{"format": "candidate/1-dev.1", "deployment": c.DeploymentID, "epoch": c.ControllerEpoch, "installation": c.InstallationID, "operation": c.OperationID, "resource": c.Payload.ResourceID, "namespace": c.Payload.DataNamespaceID, "release": c.Payload.ReleaseSHA256, "generation": c.TargetGeneration, "control-fence": c.InstallationFence, "writer-fence": c.Payload.WriterFence} {
		if actual[0].Config.Labels["org.portal.app."+name] != value {
			t.Fatal("refusing changed fixture ownership", name)
		}
	}
	if cleanup {
		run("container", "rm", "--force", id)
	} else if !actual[0].State.Running {
		t.Fatal("candidate was not actually running")
	}
	return id
}
