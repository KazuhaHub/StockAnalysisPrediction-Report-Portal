//go:build linux && (amd64 || arm64)

package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

type platformLifecycleBrowserResult struct {
	Mode        string `json:"mode"`
	Action      string `json:"action"`
	OperationID string `json:"operationID"`
	Stage       string `json:"stage"`
}

type platformLifecycleBrowserHost struct {
	server *httptest.Server
	cookie string
}

var platformLifecycleBrowserResultLine = regexp.MustCompile(
	`APP_LIFECYCLE_BROWSER_(?:CONFIRMED|COMPLETE) (\{[^\r\n]+\})`,
)

func platformLifecycleBrowserConfigured() bool {
	return os.Getenv("APP_LIFECYCLE_BROWSER_RUNNER") != ""
}

func preparePlatformLifecycleBrowserChild(
	t *testing.T, childData string,
) (string, string) {
	t.Helper()
	source := os.Getenv("APP_LIFECYCLE_BROWSER_RUNNER")
	if !filepath.IsAbs(source) || filepath.Clean(source) != source {
		t.Fatal("invalid lifecycle browser runner")
	}
	input, err := os.Open(source)
	if err != nil {
		t.Fatal("open lifecycle browser runner", err)
	}
	defer input.Close()
	raw, err := io.ReadAll(io.LimitReader(input, 64<<10+1))
	if err != nil || len(raw) > 64<<10 {
		t.Fatal("lifecycle browser runner unavailable or oversized", err)
	}
	runner := filepath.Join(childData, "app-platform-lifecycle-browser.cjs")
	if err := os.WriteFile(runner, raw, 0444); err != nil {
		t.Fatal("stage lifecycle browser runner", err)
	}
	evidence := filepath.Join(childData, "lifecycle-browser")
	if err := os.Mkdir(evidence, 0700); err != nil {
		t.Fatal("prepare lifecycle browser evidence", err)
	}
	if err := os.Chown(evidence, 41001, 41001); err != nil {
		t.Fatal("own lifecycle browser evidence", err)
	}
	return runner, evidence
}

func copyPlatformLifecycleBrowserEvidence(source, target string) error {
	if err := os.MkdirAll(target, 0755); err != nil {
		return err
	}
	entries, err := os.ReadDir(source)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !entry.Type().IsRegular() {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(source, entry.Name()))
		if err != nil {
			return err
		}
		if len(raw) > 8<<20 {
			return fmt.Errorf("lifecycle browser evidence is oversized: %s",
				entry.Name())
		}
		if err := os.WriteFile(
			filepath.Join(target, entry.Name()), raw, 0644); err != nil {
			return err
		}
	}
	return nil
}

func startPlatformLifecycleBrowserHost(
	t *testing.T,
	s *Server,
	actor platformActorSession,
	endpoint platformRuntimeEndpoint,
) *platformLifecycleBrowserHost {
	t.Helper()
	if bound, ok := s.platformRuntimeEndpoint(); ok {
		if bound != endpoint {
			t.Fatal("lifecycle browser runtime endpoint changed")
		}
	} else if err := s.bindPlatformBackendHTTP(endpoint); err != nil {
		t.Fatal("bind lifecycle browser runtime", err)
	}
	mux := http.NewServeMux()
	s.wireRoutes(mux)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return &platformLifecycleBrowserHost{
		server: server,
		cookie: s.managedCookie(
			*s.st.GetUser(actor.username), actor.sessionID, actor.expires.Unix()),
	}
}

func runPlatformLifecycleBrowser(
	t *testing.T,
	host *platformLifecycleBrowserHost,
	installation, action, mode, operation string,
) platformLifecycleBrowserResult {
	t.Helper()
	runner := os.Getenv("APP_LIFECYCLE_BROWSER_RUNNER")
	evidence := os.Getenv("APP_LIFECYCLE_BROWSER_EVIDENCE")
	node := os.Getenv("APP_SDK_BROWSER_NODE")
	pkg := os.Getenv("SDK_BROWSER_PACKAGE")
	browsers := os.Getenv("PLAYWRIGHT_BROWSERS_PATH")
	for name, value := range map[string]string{
		"runner": runner, "evidence": evidence, "node": node,
		"playwright package": pkg, "browsers": browsers,
	} {
		if !filepath.IsAbs(value) || filepath.Clean(value) != value {
			t.Fatalf("invalid lifecycle browser %s path", name)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 210*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, node, runner)
	command.Dir = evidence
	command.Env = append(os.Environ(),
		"APP_PLATFORM_BROWSER_URL="+host.server.URL,
		"APP_PLATFORM_BROWSER_COOKIE_NAME="+cookieName,
		"APP_PLATFORM_BROWSER_COOKIE="+host.cookie,
		"APP_PLATFORM_BROWSER_INSTALLATION="+installation,
		"APP_PLATFORM_BROWSER_ACTION="+action,
		"APP_PLATFORM_BROWSER_MODE="+mode,
		"APP_PLATFORM_BROWSER_OPERATION="+operation,
		"APP_PLATFORM_BROWSER_EVIDENCE="+evidence,
	)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("lifecycle browser %s/%s failed: %v\n%s",
			action, mode, err, output)
	}
	match := platformLifecycleBrowserResultLine.FindSubmatch(output)
	if len(match) != 2 {
		t.Fatalf("lifecycle browser %s/%s result missing:\n%s",
			action, mode, output)
	}
	var result platformLifecycleBrowserResult
	if err := json.Unmarshal(match[1], &result); err != nil {
		t.Fatalf("invalid lifecycle browser result: %v\n%s", err, match[1])
	}
	expectedStage := "confirmed"
	if mode == "progress" {
		expectedStage = "complete"
	}
	if result.Action != action || result.Mode != mode ||
		!platformValidUUID(result.OperationID) ||
		result.Stage != expectedStage {
		t.Fatalf("unexpected lifecycle browser result: %+v", result)
	}
	t.Logf("LIFECYCLE_BROWSER_%s_%s",
		strings.ToUpper(action), strings.ToUpper(mode))
	return result
}
