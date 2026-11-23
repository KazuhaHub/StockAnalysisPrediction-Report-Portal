//go:build linux

package driver

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestSeccompProfileRunsPythonScriptFile starts a throwaway container under the exact inline profile and runs a
// script file with CPython, which calls ioctl(FIOCLEX) when it opens the file.
//
//	APP_SECCOMP_PYTHON_IMAGE=python:3.11-slim-bookworm go test ./internal/apprunner/driver -run PythonScriptFile -count=1
//
// The image must carry python3 and already be present locally; nothing is published and the container is removed.
func TestSeccompProfileRunsPythonScriptFile(t *testing.T) {
	image := os.Getenv("APP_SECCOMP_PYTHON_IMAGE")
	if image == "" {
		t.Skip("set APP_SECCOMP_PYTHON_IMAGE to an image with python3 to run this check")
	}
	if candidateSeccompError != nil {
		t.Fatal(candidateSeccompError)
	}
	dir := t.TempDir()
	script := filepath.Join(dir, "script.py")
	if err := os.WriteFile(script, []byte("print('script-ok')\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// The docker CLI reads a profile from a file; the runner sends the same JSON inline through the API.
	profile := filepath.Join(t.TempDir(), "profile.json")
	if err := os.WriteFile(profile, []byte(strings.TrimPrefix(candidateSeccompOption, "seccomp=")), 0o644); err != nil {
		t.Fatal(err)
	}
	name := "seccomp-python-check-" + strings.ReplaceAll(time.Now().Format("150405.000"), ".", "")
	cmd := exec.Command("docker", "run", "--rm", "--name", name, "--pull=never", "--network", "none",
		"--cap-drop", "ALL", "--user", "65532:65532",
		"--security-opt", "no-new-privileges:true", "--security-opt", "seccomp="+profile,
		"-v", dir+":/s:ro", "--entrypoint", "python3", image, "/s/script.py")
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", name).Run() })
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "script-ok") {
		t.Fatalf("python script file failed under the profile: %v: %s", err, out)
	}
}
