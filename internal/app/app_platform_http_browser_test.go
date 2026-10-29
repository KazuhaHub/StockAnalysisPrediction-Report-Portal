package app

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

// Explicit loopback development fixture. Its temporary app is installed through
// the real no-data lifecycle; no production store or runtime is provisioned.
func TestPlatformHTTPBrowserFixture(t *testing.T) {
	directory := os.Getenv("APP_DOCUMENT_HTTP_FIXTURE_DIR")
	stateFile := os.Getenv("APP_DOCUMENT_HTTP_STATE")
	origin := os.Getenv("APP_DOCUMENT_HTTP_ORIGIN")
	if directory == "" || stateFile == "" || origin == "" {
		t.Skip("HTTP browser fixture not requested")
	}
	parsed, err := url.Parse(origin)
	if err != nil || parsed.Scheme != "http" || parsed.Hostname() != "127.0.0.1" || parsed.Port() == "" || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.User != nil {
		t.Fatal("fixture parent must be an exact loopback HTTP origin")
	}
	var archive bytes.Buffer
	writer := zip.NewWriter(&archive)
	for _, name := range []string{"index.html", "app.js", "style.css"} {
		var content []byte
		if name == "index.html" {
			content = []byte(`<!doctype html><html><head><meta charset="utf-8"><link rel="stylesheet" href="style.css"></head><body><div id="app"></div><script src="app.js"></script></body></html>`)
		} else {
			content, err = os.ReadFile(filepath.Join(directory, "child", name))
			if err != nil {
				t.Fatal(err)
			}
		}
		if len(content) > appplatform.MaxFrontendFileBytes {
			t.Fatal("fixture member exceeds limit")
		}
		file, err := writer.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := file.Write(content); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	s, reader, id := platformFrontendBrowserInstallFixture(t, archive.Bytes())
	if zone := os.Getenv("APP_DOCUMENT_HTTP_NAMED_TIMEZONE"); zone != "" {
		// Named timezone belongs only to this temporary fixture store.
		if zone != "UTC" {
			t.Fatal("fixture named timezone must be UTC")
		}
		s.st.SetSetting("timezone", zone)
	}

	cookie := s.managedCookie(*s.st.GetUser(reader.username), reader.sessionID, reader.expires.Unix())
	mux := http.NewServeMux()
	s.wireRoutes(mux)
	// Fixture controls are compiled only into tests. They never accept a supplied
	// identity, credential or production database. Mutations require the configured
	// parent origin and affect only this temporary store's known installation.
	mux.HandleFunc("GET /__fixture/info", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{"launch": "/api/app-platform/installations/" + id + "/launch"})
	})
	mux.HandleFunc("POST /__fixture/{action}", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Origin") != origin {
			http.Error(w, "origin refused", http.StatusForbidden)
			return
		}
		var err error
		switch r.PathValue("action") {
		case "revoke-access":
			_, err = s.st.exec(`DELETE FROM app_platform_access_grants WHERE installation_id=?`, id)
		case "restore-access":
			_, err = s.st.exec(`INSERT INTO app_platform_access_grants(id,installation_id,principal_type,username,account_created,account_generation,group_id,group_created,revision,created_ms,updated_ms) VALUES(?,?,'user',?,?,?,NULL,NULL,1,1,1) ON CONFLICT DO NOTHING`, platformFixtureID(613), id, reader.username, reader.created, reader.generation)
		case "revoke-session":
			_, err = s.st.exec(`UPDATE user_sessions SET revoked=1 WHERE id=?`, reader.sessionID)
		default:
			http.NotFound(w, r)
			return
		}
		if err != nil {
			http.Error(w, "fixture action failed", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	server := httptest.NewServer(mux)
	defer server.Close()
	state, err := json.Marshal(map[string]string{"url": server.URL, "cookieName": cookieName, "cookie": cookie})
	if err != nil {
		t.Fatal(err)
	}
	output, err := os.OpenFile(stateFile, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := output.Write(state); err != nil {
		output.Close()
		t.Fatal(err)
	}
	if err := output.Close(); err != nil {
		t.Fatal(err)
	}
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	timeout := time.NewTimer(20 * time.Minute)
	defer timeout.Stop()
	for {
		select {
		case <-ticker.C:
			if _, err := os.Stat(stateFile + ".stop"); err == nil {
				return
			} else if !os.IsNotExist(err) {
				t.Fatal(err)
			}
		case <-timeout.C:
			t.Fatal("fixture operator did not stop the loopback server")
		}
	}
}

func platformFrontendBrowserInstallFixture(t *testing.T, archive []byte) (*Server, platformActorSession, string) {
	t.Helper()
	s, actor, _ := platformInstallFixture(t, false)
	reader := platformActivationReader(t, s, "frontend-browser-reader")
	raw, _, _ := platformSignedReleaseFixture(t, "1.0.0-beta.2")
	var manifest appplatform.Manifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	manifest.Compatibility.PlatformAPI = ">=2.0.0-0 <3.0.0"
	manifest.Capabilities.Required = []string{}
	manifest.Presentation.Routes = []appplatform.Route{{ID: "editor", Path: "/editor", Layout: "viewport"}, {ID: "home", Path: "/", Layout: "document"}}
	manifest.Frontend.SHA256 = platformDigest(archive)
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	release, err := s.stagePlatformRelease(actor, raw, platformSignTestDescriptor(t, raw), archive)
	if err != nil {
		t.Fatal(err)
	}
	review, err := s.createPlatformInstallAudienceReview(actor, release.sha256, 1, []platformAudienceTarget{{kind: "user", username: reader.username}})
	if err != nil {
		t.Fatal(err)
	}
	admitted, err := s.consumePlatformInstallReview(actor, review.ID, review.SHA256, platformFixtureID(963))
	if err != nil {
		t.Fatal(err)
	}
	w, err := newPlatformInstallScheduler(s, platformRuntimeEndpoint{}, platformFixtureID(964), 50*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if err = w.tick(context.Background()); err != nil || w.currentStatus().code != "complete" {
		t.Fatal("browser fixture frontend installation failed", err)
	}
	if scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_runtime_bindings`)+scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_data_namespaces`)+scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_operation_effects`) != 0 {
		t.Fatal("browser fixture created runtime/data resources")
	}
	return s, reader, admitted.InstallationID
}
