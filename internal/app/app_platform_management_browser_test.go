package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

// Explicitly requested loopback browser fixture with a disposable database and
// synthetic publisher/session. No listener exists in ordinary tests or binaries.
func TestPlatformManagementBrowserFixture(t *testing.T) {
	stateFile := os.Getenv("APP_INSTALL_HTTP_STATE")
	if stateFile == "" {
		t.Skip("installation browser fixture not requested")
	}
	var s *Server
	var actor platformActorSession
	var mux *http.ServeMux
	var operation, installation string
	scenario := os.Getenv("APP_INSTALL_HTTP_SCENARIO")
	switch scenario {
	case "progress", "paused", "recovery":
		var release string
		s, mux, actor, release, _ = platformManagementFixture(t, false)
		review, err := s.createPlatformFrontendInstallReview(actor, release, 1, nil)
		if err != nil {
			t.Fatal(err)
		}
		admitted, err := s.consumePlatformInstallReview(actor, review.ID, review.SHA256, platformFixtureID(5501))
		if err != nil {
			t.Fatal(err)
		}
		operation = admitted.OperationID
		switch scenario {
		case "paused":
			mustExec(t, s.st, `UPDATE meta SET v='1' WHERE k='app_platform_restore_paused'`)
		case "recovery":
			now, err := platformDatabaseNow(s.st.poolExec())
			if err != nil {
				t.Fatal(err)
			}
			mustExec(t, s.st, `UPDATE app_platform_operations SET stage='recovery_required',state_revision=state_revision+1,row_revision=row_revision+1,error_code='fixture_review_required',completed_ms=?,lease_owner=NULL,lease_until_ms=NULL,updated_ms=? WHERE id=?`, now, now, operation)
			mustExec(t, s.st, `UPDATE app_platform_installations SET phase='recovery_required',revision=revision+1,control_fence=control_fence+1,updated_ms=? WHERE id=(SELECT installation_id FROM app_platform_operations WHERE id=?)`, now, operation)
		}
	case "updateable":
		s, actor, _, _ = platformUpdateFixture(t, []int{1})
		mux = http.NewServeMux()
		s.wireRoutes(mux)
	case "disabled":
		s, actor, _, _ = platformFrontendDisabledFixture(t, false)
		mux = http.NewServeMux()
		s.wireRoutes(mux)
	case "settings":
		s, actor, _, installation = platformObjectsFixture(t)
		mux = http.NewServeMux()
		s.wireRoutes(mux)
	default:
		if os.Getenv("APP_INSTALL_HTTP_FRESH") == "1" {
			s, actor = platformActorTestServer(t, false)
			mux = http.NewServeMux()
			s.wireRoutes(mux)
		} else {
			s, mux, actor, _, _ = platformManagementFixture(t, false)
		}
	}
	if os.Getenv("APP_INSTALL_HTTP_MARKET") == "1" {
		mux = platformMarketBrowserMux(t, s, mux)
	}
	if scenario != "updateable" && scenario != "disabled" && scenario != "progress" && scenario != "settings" {
		platformStartFrontendService(t, s)
	}
	server := httptest.NewServer(mux)
	defer server.Close()
	cookie := s.managedCookie(*s.st.GetUser(actor.username), actor.sessionID, actor.expires.Unix())
	raw, err := json.Marshal(map[string]string{"url": server.URL, "cookieName": cookieName, "cookie": cookie, "scenario": scenario, "operation": operation, "installation": installation})
	if err != nil {
		t.Fatal(err)
	}
	output, err := os.OpenFile(stateFile, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = output.Write(raw); err != nil {
		output.Close()
		t.Fatal(err)
	}
	if err = output.Close(); err != nil {
		t.Fatal(err)
	}
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	timer := time.NewTimer(15 * time.Minute)
	defer timer.Stop()
	for {
		select {
		case <-ticker.C:
			if _, err := os.Stat(stateFile + ".stop"); err == nil {
				return
			} else if !os.IsNotExist(err) {
				t.Fatal(err)
			}
		case <-timer.C:
			t.Fatal("installation browser fixture expired")
		}
	}
}

func platformMarketBrowserMux(t *testing.T, s *Server, original *http.ServeMux) *http.ServeMux {
	t.Helper()
	raw, _, archive := platformSignedReleaseFixture(t, "1.0.1-beta.1")
	var manifest appplatform.Manifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	manifest.ID = "notes-market-sample"
	manifest.Compatibility.PlatformAPI = ">=2.0.0-0 <3.0.0"
	manifest.Capabilities.Required = []string{}
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	signature := platformSignTestDescriptor(t, raw)
	remote := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/index.json":
			json.NewEncoder(w).Encode(map[string]any{"format": "portal.catalogue/2-dev.1", "releases": []appplatform.CatalogueRelease{{AppID: manifest.ID, PublisherID: manifest.Publisher, Version: manifest.Version, DescriptorSHA256: platformDigest(raw), DescriptorURL: "https://127.0.0.1/app.json", SignatureURL: "https://127.0.0.1/app.sig"}}})
		case "/app.json":
			w.Write(raw)
		case "/app.sig":
			w.Write(signature)
		case "/" + manifest.Frontend.Artifact:
			w.Write(archive)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(remote.Close)
	fixture := http.NewServeMux()
	client := func() *platformMarketClient {
		c := newPlatformMarketClient()
		c.safe.allowInsecureForTest = true
		platformMarketTestTLS(t, c, remote)
		return c
	}
	// Only this opt-in test listener maps canonical HTTPS to its owned TLS
	// fixture. Production routes retain their closed public-network client.
	s.platformAdminRoute(fixture, "POST /api/admin/app-platform/market/catalogue", func(w http.ResponseWriter, r *http.Request, user string) {
		c := client()
		defer c.close()
		s.apiPlatformMarketCatalogueClient(w, r, user, c)
	})
	s.platformAdminRoute(fixture, "POST /api/admin/app-platform/market/stage", func(w http.ResponseWriter, r *http.Request, user string) {
		c := client()
		defer c.close()
		s.apiPlatformMarketStageClient(w, r, user, c)
	})
	fixture.Handle("/", original)
	return fixture
}
