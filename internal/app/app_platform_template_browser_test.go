package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

func readTemplateCandidateFile(path string, maximum int64) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > maximum {
		return nil, errors.New("invalid template candidate file")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, maximum+1))
	if err != nil || int64(len(raw)) > maximum {
		return nil, errors.New("template candidate bound exceeded")
	}
	return raw, nil
}
func parseTemplateCandidate(raw, archive []byte) (appplatform.ParsedDescriptor, error) {
	parsed, err := appplatform.ParseDescriptor(raw)
	if err != nil {
		return parsed, err
	}
	if parsed.Manifest.Frontend.Artifact != "frontend.zip" || parsed.Manifest.Frontend.SHA256 != platformDigest(archive) {
		return parsed, errors.New("template frontend digest mismatch")
	}
	_, err = appplatform.BuildFrontendDocument(parsed.Manifest, archive)
	return parsed, err
}
func templateCandidate(t *testing.T, dir string) (appplatform.ParsedDescriptor, []byte) {
	t.Helper()
	raw, err := readTemplateCandidateFile(filepath.Join(dir, "descriptor.json"), appplatform.MaxDescriptorBytes)
	if err != nil {
		t.Fatal(err)
	}
	archive, err := readTemplateCandidateFile(filepath.Join(dir, "frontend.zip"), appplatform.MaxFrontendArchiveBytes)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := parseTemplateCandidate(raw, archive)
	if err != nil {
		t.Fatal("generated template refused", err)
	}
	return parsed, archive
}

func TestGeneratedV2TemplateCandidate(t *testing.T) {
	dir := os.Getenv("APP_V2_AUTHORING_BUILD_DIR")
	if dir == "" {
		t.Skip("explicit generated-template candidate only")
	}
	parsed, archive := templateCandidate(t, dir)
	if parsed.Manifest.ID != "template-v2-check" || parsed.Manifest.Backend != nil || len(parsed.Manifest.Capabilities.Required) != 0 || len(parsed.Manifest.Capabilities.Optional) != 0 {
		t.Fatal("starter authority unexpectedly expanded")
	}
	raw, err := readTemplateCandidateFile(filepath.Join(dir, "descriptor.json"), appplatform.MaxDescriptorBytes)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = parseTemplateCandidate(raw, append(append([]byte{}, archive...), 0)); err == nil {
		t.Fatal("changed ZIP retained its descriptor authority")
	}
	invalid := append([]byte(`{"unknown":true,`), raw[1:]...)
	if _, err = parseTemplateCandidate(invalid, archive); err == nil {
		t.Fatal("unknown declaration field admitted")
	}
}

// Disposable development only: the fixture enrolls its own test publisher and
// user, then installs the real generated frontend/routes through the installer.
// It neither tests production publisher enrollment nor provisions a backend.
func TestGeneratedV2TemplateBrowser(t *testing.T) {
	dir := os.Getenv("APP_V2_AUTHORING_BUILD_DIR")
	runner := os.Getenv("APP_V2_AUTHORING_BROWSER_RUNNER")
	if dir == "" || runner == "" {
		t.Skip("explicit generated-template browser workflow only")
	}
	node, pkg, browsers := os.Getenv("APP_SDK_BROWSER_NODE"), os.Getenv("SDK_BROWSER_PACKAGE"), os.Getenv("PLAYWRIGHT_BROWSERS_PATH")
	for _, path := range []string{dir, runner, node, pkg, browsers} {
		if !filepath.IsAbs(path) || filepath.Clean(path) != path {
			t.Fatal("invalid template browser bootstrap")
		}
	}
	parsed, archive := templateCandidate(t, dir)
	s, reader, installation := platformFrontendBrowserInstallFixture(t, archive, parsed.Manifest.Presentation)
	mux := http.NewServeMux()
	s.wireRoutes(mux)
	mux.HandleFunc("GET /__template_fixture/start", func(w http.ResponseWriter, r *http.Request) {
		cookie := s.managedCookie(*s.st.GetUser(reader.username), reader.sessionID, reader.expires.Unix())
		http.SetCookie(w, &http.Cookie{Name: cookieName, Value: cookie, Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode})
		http.Redirect(w, r, "/apps/v2/"+installation, http.StatusFound)
	})
	listener, err := net.Listen("tcp", "127.0.0.1:31935")
	if err != nil {
		t.Fatal("exclusive template listener", err)
	}
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 10 * time.Second}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	defer func() {
		server.Close()
		if err := <-done; err != http.ErrServerClosed {
			t.Error(err)
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, node, runner)
	command.WaitDelay = 5 * time.Second
	command.Env = []string{"PATH=" + filepath.Dir(node) + ":/usr/bin:/bin", "HOME=" + t.TempDir(), "TMPDIR=" + t.TempDir(), "SDK_BROWSER_PACKAGE=" + pkg, "PLAYWRIGHT_BROWSERS_PATH=" + browsers, "APP_V2_TEMPLATE_PROOF=" + filepath.Join(dir, "proof.jpg"), "APP_V2_TEMPLATE_FAILURE=" + filepath.Join(dir, "failure.jpg")}
	var output bytes.Buffer
	command.Stdout = &output
	command.Stderr = &output
	if err := command.Run(); err != nil {
		t.Fatalf("generated-template browser failed: %v\n%s", err, output.String())
	}
	if !strings.Contains(output.String(), "V2_TEMPLATE_BROWSER_ACCEPTED") {
		t.Fatal("template browser evidence absent")
	}
	report, err := json.Marshal(map[string]any{"development_only": true, "browser_verified": true, "frontend_sha256": parsed.Manifest.Frontend.SHA256, "descriptor_sha256": parsed.SHA256, "test_publisher_identity": true, "production_approved": false, "backend_provisioned": false})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, "browser-evidence.json"), append(report, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	t.Log("V2_TEMPLATE_BROWSER_ACCEPTED")
}
