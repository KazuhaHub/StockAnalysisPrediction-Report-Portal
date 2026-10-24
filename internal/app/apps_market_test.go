package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/config"
)

// marketAppServer stands in for the GitHub raw host: it serves an app-market
// index and a real .zip bundle at fixed paths.
func marketAppServer(t *testing.T) *httptest.Server {
	t.Helper()
	zip := makeZip(t, map[string]string{
		"app.json":   `{"id":"demo","name":"Demo","icon":"🧪","version":"2.0.0","entry":"index.html","scopes":["query"]}`,
		"index.html": "<h1>demo</h1>",
	})
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/index.json":
			idx := `{"apps":[{"id":"demo","name":"Demo","icon":"🧪","version":"2.0.0","description":"a demo","scopes":["query"],"path":"demo.zip"}]}`
			w.Write([]byte(idx))
		case "/demo.zip":
			w.Header().Set("Content-Type", "application/zip")
			w.Write(zip)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
}

func appMarketServer(t *testing.T) *Server {
	t.Helper()
	return &Server{st: newTestStore(t), cfg: &config.Config{SecretKey: "k"}, appTok: newAppTokens(30 * 60 * 1e9)}
}

// The market list reports each entry and flips `installed` once installed.
func TestAppMarketList(t *testing.T) {
	market := marketAppServer(t)
	defer market.Close()
	srv := appMarketServer(t)
	srv.st.SetSetting("app_market_index_url", market.URL+"/index.json")

	rec := httptest.NewRecorder()
	srv.apiAppMarket(rec, httptest.NewRequest("GET", "/api/admin/apps/market", nil), "admin")
	if rec.Code != http.StatusOK {
		t.Fatalf("apiAppMarket → %d: %s", rec.Code, rec.Body.String())
	}
	var out struct {
		IndexURL string `json:"index_url"`
		Apps     []struct {
			ID        string   `json:"id"`
			Name      string   `json:"name"`
			Version   string   `json:"version"`
			Scopes    []string `json:"scopes"`
			Installed bool     `json:"installed"`
		} `json:"apps"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out.Apps) != 1 || out.Apps[0].ID != "demo" || out.Apps[0].Version != "2.0.0" {
		t.Fatalf("apps = %+v", out.Apps)
	}
	if out.Apps[0].Installed {
		t.Fatal("demo should not be installed yet")
	}
	if len(out.Apps[0].Scopes) != 1 || out.Apps[0].Scopes[0] != "query" {
		t.Fatalf("scopes = %v", out.Apps[0].Scopes)
	}

	// Install it, then the list should mark it installed.
	post(t, srv.apiAppMarketInstall, `{"id":"demo"}`)
	rec2 := httptest.NewRecorder()
	srv.apiAppMarket(rec2, httptest.NewRequest("GET", "/api/admin/apps/market", nil), "admin")
	json.Unmarshal(rec2.Body.Bytes(), &out)
	if !out.Apps[0].Installed {
		t.Fatal("demo should be installed after market install")
	}
}

// Installing from the market downloads the bundle and stores the app + its files.
func TestAppMarketInstall(t *testing.T) {
	market := marketAppServer(t)
	defer market.Close()
	srv := appMarketServer(t)
	srv.st.SetSetting("app_market_index_url", market.URL+"/index.json")

	post(t, srv.apiAppMarketInstall, `{"id":"demo"}`)

	a, ok := srv.st.GetApp("demo")
	if !ok {
		t.Fatal("app not installed from market")
	}
	if a.Version != "2.0.0" || a.Icon != "🧪" || a.Entry != "index.html" {
		t.Fatalf("installed app = %+v", a)
	}
	if _, _, ok := srv.st.AppFile("demo", "index.html"); !ok {
		t.Fatal("index.html asset missing after market install")
	}
}

// An unknown id yields 404 from the market install endpoint.
func TestAppMarketInstallUnknown(t *testing.T) {
	market := marketAppServer(t)
	defer market.Close()
	srv := appMarketServer(t)
	srv.st.SetSetting("app_market_index_url", market.URL+"/index.json")

	rec := httptest.NewRecorder()
	srv.apiAppMarketInstall(rec, httptest.NewRequest("POST", "/x", strings.NewReader(`{"id":"nope"}`)), "admin")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown id → %d, want 404: %s", rec.Code, rec.Body.String())
	}
}

func TestAppMarketInstallRejectsDeclarationDrift(t *testing.T) {
	for _, tc := range []struct {
		name, manifest, request string
		status                  int
	}{
		{"different identity", `{"id":"other","version":"2.0.0","scopes":["query"]}`, `{"id":"demo"}`, http.StatusBadGateway},
		{"different version", `{"id":"demo","version":"3.0.0","scopes":["query"]}`, `{"id":"demo"}`, http.StatusBadGateway},
		{"extra capability", `{"id":"demo","version":"2.0.0","scopes":["query","extension.manage"]}`, `{"id":"demo"}`, http.StatusBadGateway},
		{"changed reviewed version", `{"id":"demo","version":"2.0.0","scopes":["query"]}`, `{"id":"demo","version":"1.0.0","scopes":["query"]}`, http.StatusConflict},
		{"changed reviewed scopes", `{"id":"demo","version":"2.0.0","scopes":["query"]}`, `{"id":"demo","version":"2.0.0","scopes":[]}`, http.StatusConflict},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var manifest map[string]any
			if err := json.Unmarshal([]byte(tc.manifest), &manifest); err != nil {
				t.Fatal(err)
			}
			manifest["name"], manifest["entry"] = "Demo", "index.html"
			raw, _ := json.Marshal(manifest)
			bundle := makeZip(t, map[string]string{"app.json": string(raw), "index.html": "changed"})
			market := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/index.json" {
					w.Write([]byte(`{"apps":[{"id":"demo","name":"Demo","version":"2.0.0","scopes":["query"],"path":"demo.zip"}]}`))
				} else {
					w.Write(bundle)
				}
			}))
			defer market.Close()
			srv := appMarketServer(t)
			if err := srv.st.InstallApp(App{ID: "demo", Name: "Previous", Version: "1.0.0", Entry: "index.html", Scopes: []string{"query"}}, map[string]AppFile{"index.html": {Ctype: "text/html", Content: []byte("previous")}}); err != nil {
				t.Fatal(err)
			}
			srv.st.SetSetting(setAppMarketIndexURL, market.URL+"/index.json")
			rec := httptest.NewRecorder()
			srv.apiAppMarketInstall(rec, httptest.NewRequest("POST", "/x", strings.NewReader(tc.request)), "admin")
			if rec.Code != tc.status {
				t.Fatalf("install = %d, want %d: %s", rec.Code, tc.status, rec.Body.String())
			}
			if got, _ := srv.st.GetApp("demo"); got.Version != "1.0.0" {
				t.Fatalf("rejected install changed existing app: %+v", got)
			}
			if _, content, ok := srv.st.AppFile("demo", "index.html"); !ok || string(content) != "previous" {
				t.Fatal("rejected install replaced existing assets")
			}
			if _, ok := srv.st.GetApp("other"); ok {
				t.Fatal("rejected install created a different app")
			}
		})
	}
}

func TestAppMarketReviewedInstallAndDuplicateIdentity(t *testing.T) {
	market := marketAppServer(t)
	defer market.Close()
	srv := appMarketServer(t)
	srv.st.SetSetting(setAppMarketIndexURL, market.URL+"/index.json")
	rec := httptest.NewRecorder()
	srv.apiAppMarketInstall(rec, httptest.NewRequest("POST", "/x", strings.NewReader(`{"id":"demo","version":"2.0.0","scopes":["query"]}`)), "admin")
	if rec.Code != http.StatusOK {
		t.Fatalf("reviewed install = %d: %s", rec.Code, rec.Body.String())
	}
	if !sameMarketScopes([]string{"extension.manage", "extension.read"}, []string{"extension.read", "extension.manage"}) {
		t.Fatal("scope order must not change capability identity")
	}
	duplicate := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"apps":[{"id":"demo"},{"id":"demo"}]}`))
	}))
	defer duplicate.Close()
	srv.st.SetSetting(setAppMarketIndexURL, duplicate.URL+"/index.json")
	rec = httptest.NewRecorder()
	srv.apiAppMarketInstall(rec, httptest.NewRequest("POST", "/x", strings.NewReader(`{"id":"demo"}`)), "admin")
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("ambiguous identity = %d: %s", rec.Code, rec.Body.String())
	}
	if got, _ := srv.st.GetApp("demo"); got.Version != "2.0.0" {
		t.Fatal("ambiguous catalogue changed installed app")
	}
}
