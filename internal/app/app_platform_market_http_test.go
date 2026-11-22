package app

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestPlatformMarketHTTPAuthorityAndInput(t *testing.T) {
	s, mux, actor, _, cookie := platformManagementFixture(t, false)
	for _, endpoint := range []string{"catalogue", "stage"} {
		path := "/api/admin/app-platform/market/" + endpoint
		body := `{"catalogue_url":"https://127.0.0.1/index.json"}`
		if endpoint == "stage" {
			body = fmt.Sprintf(`{"catalogue_url":"https://127.0.0.1/index.json","release_sha256":%q}`, strings.Repeat("a", 64))
		}
		if got := sessionTestRequest(mux, "POST", path, "", body); got.Code != 401 {
			t.Fatal("anonymous remote fetch allowed", got.Code)
		}
		if got := sessionTestRequest(mux, "POST", path, cookie, body); got.Code != 503 || strings.Contains(got.Body.String(), "127.0.0.1") {
			t.Fatal("private fetch allowed or exposed target", got.Code, got.Body.String())
		}
		for _, bad := range []string{`null`, `{}`, strings.Replace(body, `https://127.0.0.1/index.json`, `http://127.0.0.1/index.json`, 1), strings.Replace(body, `"catalogue_url":`, `"Catalogue_url":`, 1), strings.Replace(body, `"catalogue_url":`, `"endpoint":"/tmp/socket","catalogue_url":`, 1)} {
			if got := sessionTestRequest(mux, "POST", path, cookie, bad); got.Code != 400 {
				t.Fatal("bad remote fetch input accepted", got.Code, got.Body.String())
			}
		}
		mustExec(t, s.st, `UPDATE users SET role='user' WHERE username=?`, actor.username)
		if got := sessionTestRequest(mux, "POST", path, cookie, body); got.Code != 403 {
			t.Fatal("ordinary user remote fetch allowed", got.Code)
		}
		mustExec(t, s.st, `UPDATE users SET role='admin' WHERE username=?`, actor.username)
	}
	if scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_operations`) != 0 {
		t.Fatal("remote fetch route admitted installation")
	}
}

func TestPlatformMarketFetchCancellation(t *testing.T) {
	requested := make(chan struct{})
	finished := make(chan struct{})
	remote := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(requested)
		<-r.Context().Done()
		close(finished)
	}))
	defer remote.Close()
	client := newPlatformMarketClient()
	client.safe.allowInsecureForTest = true
	platformMarketTestTLS(t, client, remote)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := client.fetch(ctx, "https://127.0.0.1/index.json", 1024); done <- err }()
	select {
	case <-requested:
	case <-time.After(3 * time.Second):
		t.Fatal("TLS fetch did not reach owned fixture")
	}
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("canceled download succeeded")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("download ignored cancellation")
	}
	select {
	case <-finished:
	case <-time.After(2 * time.Second):
		t.Fatal("canceled fetch did not close upstream request")
	}
}

func TestPlatformMarketCatalogueGroupsApps(t *testing.T) {
	s, _, _, _, cookie := platformManagementFixture(t, false)
	signedAs := func(id, version, name string) (digest string, raw, sig []byte) {
		rawFixture, _, _ := platformSignedReleaseFixture(t, version)
		var manifest appplatform.Manifest
		if err := json.Unmarshal(rawFixture, &manifest); err != nil {
			t.Fatal(err)
		}
		manifest.ID, manifest.Version = id, version
		manifest.Presentation.Display.Texts[0].Name = name
		raw, err := json.Marshal(manifest)
		if err != nil {
			t.Fatal(err)
		}
		return platformDigest(raw), raw, platformSignTestDescriptor(t, raw)
	}
	files := map[string][]byte{}
	var releases []appplatform.CatalogueRelease
	add := func(publisher, id, version string, digest string, raw, sig []byte) {
		path := "/" + id + "-" + version
		files[path+".json"], files[path+".sig"] = raw, sig
		releases = append(releases, appplatform.CatalogueRelease{AppID: id, PublisherID: publisher, Version: version, DescriptorSHA256: digest, DescriptorURL: "https://127.0.0.1" + path + ".json", SignatureURL: "https://127.0.0.1" + path + ".sig"})
	}
	for _, v := range []string{"0.1.0-beta.2", "0.1.0", "0.1.0-beta.10"} {
		d, raw, sig := signedAs("notes-alpha", v, "Alpha "+v)
		add("fixture-publisher", "notes-alpha", v, d, raw, sig)
	}
	// Digest listed in the catalogue differs from the bytes served: no display.
	_, raw, sig := signedAs("notes-bravo", "1.0.0", "Bravo")
	add("fixture-publisher", "notes-bravo", "1.0.0", strings.Repeat("b", 64), raw, sig)
	// A descriptor whose identity differs from the catalogue entry: no display.
	d, raw, sig := signedAs("notes-other", "1.0.0", "Other")
	add("fixture-publisher", "notes-charlie", "1.0.0", d, raw, sig)
	// A publisher nobody trusts: not trusted, no display.
	d, raw, sig = signedAs("notes-delta", "1.0.0", "Delta")
	add("stranger", "notes-delta", "1.0.0", d, raw, sig)

	remote := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/index.json" {
			json.NewEncoder(w).Encode(map[string]any{"format": "portal.catalogue/2-dev.1", "releases": releases})
			return
		}
		if body, ok := files[r.URL.Path]; ok {
			w.Write(body)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(remote.Close)
	mux := http.NewServeMux()
	s.platformAdminRoute(mux, "POST /api/admin/app-platform/market/catalogue", func(w http.ResponseWriter, r *http.Request, user string) {
		c := newPlatformMarketClient()
		c.safe.allowInsecureForTest = true
		platformMarketTestTLS(t, c, remote)
		defer c.close()
		s.apiPlatformMarketCatalogueClient(w, r, user, c)
	})
	got := sessionTestRequest(mux, "POST", "/api/admin/app-platform/market/catalogue", cookie, `{"catalogue_url":"https://127.0.0.1/index.json"}`)
	if got.Code != 200 {
		t.Fatal(got.Code, got.Body.String())
	}
	var body struct {
		Releases []json.RawMessage `json:"releases"`
		Apps     []struct {
			PublisherID string `json:"publisher_id"`
			AppID       string `json:"app_id"`
			Versions    []struct {
				Version string `json:"version"`
				SHA     string `json:"descriptor_sha256"`
			} `json:"versions"`
			Display *struct {
				Icon  string `json:"icon"`
				Texts []struct {
					Name string `json:"name"`
				} `json:"texts"`
			} `json:"display"`
			Trusted bool `json:"publisher_trusted"`
		} `json:"apps"`
	}
	if err := json.Unmarshal(got.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Releases) != len(releases) || len(body.Apps) != 4 {
		t.Fatal("releases kept and one entry per app expected", got.Body.String())
	}
	alpha := body.Apps[0]
	order := []string{}
	for _, v := range alpha.Versions {
		order = append(order, v.Version)
	}
	if alpha.AppID != "notes-alpha" || strings.Join(order, ",") != "0.1.0,0.1.0-beta.10,0.1.0-beta.2" || alpha.Versions[0].SHA != releases[1].DescriptorSHA256 {
		t.Fatal("versions not newest first", order)
	}
	if !alpha.Trusted || alpha.Display == nil || alpha.Display.Icon != "file-text" || alpha.Display.Texts[0].Name != "Alpha 0.1.0" {
		t.Fatal("display must come from the newest version", got.Body.String())
	}
	for _, a := range body.Apps[1:3] {
		if !a.Trusted || a.Display != nil {
			t.Fatal("unverifiable preview must leave display out", a.AppID)
		}
	}
	if delta := body.Apps[3]; delta.Trusted || delta.Display != nil {
		t.Fatal("untrusted publisher", delta)
	}
}
