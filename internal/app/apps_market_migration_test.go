package app

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

const canonicalMarketURL = "https://raw.githubusercontent.com/KazuhaHub/StockAnalysisPrediction-Report-Portal-Apps/main/index.json"
const previousMarketURL = "https://raw.githubusercontent.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/main/apps/index.json"

func TestAppMarketDefaultMovesWithoutRewritingOverridesOrInstalledApps(t *testing.T) {
	for _, configured := range []string{"", previousMarketURL, "https://custom.example.test/catalogue.json"} {
		t.Run(configured, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "portal.db")
			st, err := OpenStore("sqlite", path)
			if err != nil {
				t.Fatal(err)
			}
			if configured != "" {
				st.SetSetting(setAppMarketIndexURL, configured)
			}
			if err := st.InstallApp(App{ID: "preserved", Name: "Preserved", Version: "1.0.0", Entry: "index.html", Scopes: []string{"query"}}, map[string]AppFile{"index.html": {Ctype: "text/html", Content: []byte("previous bytes")}}); err != nil {
				t.Fatal(err)
			}
			st.Close()
			opened, err := OpenStore("sqlite", path)
			if err != nil {
				t.Fatal(err)
			}
			defer opened.Close()
			s := &Server{st: opened}
			want := configured
			if want == "" {
				want = canonicalMarketURL
			}
			if got := s.appMarketIndexURL(); got != want {
				t.Fatalf("market after reopen = %q, want %q", got, want)
			}
			if configured == "" && scalar[int](t, opened, `SELECT COUNT(*) FROM meta WHERE k=?`, setAppMarketIndexURL) != 0 {
				t.Fatal("reading the compiled default persisted an override")
			}
			if app, ok := opened.GetApp("preserved"); !ok || app.Version != "1.0.0" {
				t.Fatal("default migration changed the installed app")
			}
			if _, content, ok := opened.AppFile("preserved", "index.html"); !ok || string(content) != "previous bytes" {
				t.Fatal("default migration changed installed assets")
			}
		})
	}
}

func TestUnavailableAppMarketPreservesInstalledApps(t *testing.T) {
	market := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusServiceUnavailable) }))
	defer market.Close()
	s := appMarketServer(t)
	if err := s.st.InstallApp(App{ID: "preserved", Name: "Preserved", Version: "1.0.0", Entry: "index.html"}, map[string]AppFile{"index.html": {Ctype: "text/html", Content: []byte("previous bytes")}}); err != nil {
		t.Fatal(err)
	}
	s.st.SetSetting(setAppMarketIndexURL, market.URL+"/index.json")
	rec := httptest.NewRecorder()
	s.apiAppMarket(rec, httptest.NewRequest("GET", "/api/admin/apps/market", nil), "admin")
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("unavailable market = %d", rec.Code)
	}
	if app, ok := s.st.GetApp("preserved"); !ok || app.Version != "1.0.0" {
		t.Fatal("market outage changed installed app")
	}
	if _, content, ok := s.st.AppFile("preserved", "index.html"); !ok || string(content) != "previous bytes" {
		t.Fatal("market outage changed installed assets")
	}
}

// Explicit network acceptance uses the real canonical catalogue and immutable demo ZIP.
func TestCanonicalAppMarketPublishedInstall(t *testing.T) {
	if os.Getenv("TEST_PUBLIC_APP_MARKET") != "1" {
		t.Skip("set TEST_PUBLIC_APP_MARKET=1 for public catalogue acceptance")
	}
	s := appMarketServer(t)
	s.st.SetSetting(setAppMarketIndexURL, canonicalMarketURL)
	rec := httptest.NewRecorder()
	s.apiAppMarket(rec, httptest.NewRequest("GET", "/api/admin/apps/market", nil), "admin")
	if rec.Code != http.StatusOK {
		t.Fatalf("public catalogue = %d: %s", rec.Code, rec.Body.String())
	}
	var listed struct {
		IndexURL string           `json:"index_url"`
		Apps     []appMarketEntry `json:"apps"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	var demo *appMarketEntry
	for i := range listed.Apps {
		if listed.Apps[i].ID == "demo-symbols" {
			demo = &listed.Apps[i]
		}
	}
	if listed.IndexURL != canonicalMarketURL || demo == nil {
		t.Fatal("canonical catalogue lost the legacy demo identity")
	}
	if _, ok := s.st.GetApp(demo.ID); ok {
		t.Fatal("listing a market installed the app")
	}
	reviewed, _ := json.Marshal(map[string]any{"id": demo.ID, "version": demo.Version, "scopes": demo.Scopes})
	rec = httptest.NewRecorder()
	s.apiAppMarketInstall(rec, httptest.NewRequest("POST", "/api/admin/apps/market/install", bytes.NewReader(reviewed)), "admin")
	if rec.Code != http.StatusOK {
		t.Fatalf("public market install = %d: %s", rec.Code, rec.Body.String())
	}
	app, ok := s.st.GetApp(demo.ID)
	if !ok || app.Version != demo.Version || !sameMarketScopes(app.Scopes, demo.Scopes) {
		t.Fatal("downloaded app differs from reviewed declarations")
	}
	if _, content, ok := s.st.AppFile(demo.ID, "index.html"); !ok || len(content) == 0 {
		t.Fatal("downloaded entry asset is missing")
	}
}
